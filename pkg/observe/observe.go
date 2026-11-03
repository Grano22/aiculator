// Package observe holds the live numbers behind the dashboards: rates, latency, jitter,
// flow between states, usage, and health. It is public so that plugins outside this
// repository can read the same data. Counters live in memory and restart with the
// server. Task totals come from durable state, so they survive.
package observe

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Window is how many seconds of per-second history each series keeps.
const Window = 300

// series counts events per second in a ring.
type series struct {
	counts [Window]int64
	stamps [Window]int64
	total  int64
}

func (s *series) add(now int64, n int64) {
	i := now % Window
	if s.stamps[i] != now {
		s.stamps[i], s.counts[i] = now, 0
	}
	s.counts[i] += n
	s.total += n
}

func (s *series) sum(now, seconds int64) int64 {
	var out int64
	for sec := now - seconds + 1; sec <= now; sec++ {
		if i := ((sec % Window) + Window) % Window; s.stamps[i] == sec {
			out += s.counts[i]
		}
	}
	return out
}

// tail returns the last n seconds, oldest first, leaving out the current partial second.
func (s *series) tail(now int64, n int) []int64 {
	out := make([]int64, n)
	for k := range n {
		sec := now - int64(n) + int64(k)
		if i := ((sec % Window) + Window) % Window; s.stamps[i] == sec {
			out[k] = s.counts[i]
		}
	}
	return out
}

const reservoir = 2048

// latency keeps recent samples, and jitter as a smoothed difference between consecutive
// samples, the same estimator RFC 3550 uses for network jitter.
type latency struct {
	samples []time.Duration
	next    int
	count   int64
	prev    time.Duration
	jitter  float64
	max     time.Duration
}

func (l *latency) observe(d time.Duration) {
	if len(l.samples) < reservoir {
		l.samples = append(l.samples, d)
	} else {
		l.samples[l.next] = d
		l.next = (l.next + 1) % reservoir
	}
	if l.count > 0 {
		diff := float64(d - l.prev)
		if diff < 0 {
			diff = -diff
		}
		l.jitter += (diff - l.jitter) / 16
	}
	l.prev = d
	l.count++
	l.max = max(l.max, d)
}

// Stats summarizes one latency. Times are in microseconds.
type Stats struct {
	Count  int64 `json:"count"`
	P50    int64 `json:"p50_us"`
	P90    int64 `json:"p90_us"`
	P99    int64 `json:"p99_us"`
	Max    int64 `json:"max_us"`
	Mean   int64 `json:"mean_us"`
	Jitter int64 `json:"jitter_us"`
}

func (l *latency) stats() Stats {
	st := Stats{Count: l.count, Max: l.max.Microseconds(), Jitter: int64(l.jitter / 1000)}
	if len(l.samples) == 0 {
		return st
	}
	s := slices.Clone(l.samples)
	slices.Sort(s)
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	at := func(p int) int64 { return s[min(len(s)*p/100, len(s)-1)].Microseconds() }
	st.P50, st.P90, st.P99, st.Mean = at(50), at(90), at(99), (sum / time.Duration(len(s))).Microseconds()
	return st
}

// Names of what the collector tracks.
const (
	SeriesGrants  = "grants"
	SeriesDone    = "done"
	SeriesFailed  = "failed"
	SeriesCommits = "commits"
	SeriesAPI     = "api"
	SeriesZombies = "zombies"

	LatencyQueue  = "queue_wait"
	LatencyCommit = "commit"
	LatencyCycle  = "task_cycle"
	LatencyAPI    = "api"
)

// AgentUsage is what one agent has done since the server started.
type AgentUsage struct {
	Tier   int   `json:"tier"`
	Tokens int64 `json:"tokens"`
	Cost   int64 `json:"cost"`
	Done   int64 `json:"done"`
	Failed int64 `json:"failed"`
	last   time.Time
}

// Collector is safe for concurrent use.
type Collector struct {
	mu      sync.Mutex
	started time.Time
	series  map[string]*series
	lat     map[string]*latency
	flow    map[[2]string]int64
	agents  map[string]*AgentUsage
}

// New returns an empty collector.
func New(now time.Time) *Collector {
	return &Collector{started: now, series: map[string]*series{}, lat: map[string]*latency{}, flow: map[[2]string]int64{}, agents: map[string]*AgentUsage{}}
}

// Count adds to a per-second series.
func (c *Collector) Count(name string, now time.Time, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.series[name]
	if s == nil {
		s = &series{}
		c.series[name] = s
	}
	s.add(now.Unix(), n)
}

// Observe records one latency sample.
func (c *Collector) Observe(name string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.lat[name]
	if l == nil {
		l = &latency{}
		c.lat[name] = l
	}
	l.observe(d)
}

// Transition records a task moving between states.
func (c *Collector) Transition(from, to string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flow[[2]string{from, to}]++
}

// Seen records that an agent is around.
func (c *Collector) Seen(id string, tier int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agent(id)
	a.Tier, a.last = tier, now
}

func (c *Collector) agent(id string) *AgentUsage {
	a := c.agents[id]
	if a == nil {
		a = &AgentUsage{}
		c.agents[id] = a
	}
	return a
}

// Usage adds tokens and cost to an agent.
func (c *Collector) Usage(id string, tokens, cost int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agent(id)
	a.Tokens += tokens
	a.Cost += cost
}

// Outcome credits a finished or failed task to the agent that worked on it.
func (c *Collector) Outcome(id string, done bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agent(id)
	if done {
		a.Done++
	} else {
		a.Failed++
	}
}

// Throughput is event rates over recent windows.
type Throughput struct {
	GrantsPerMin  float64 `json:"grants_per_min"`
	DonePerMin    float64 `json:"done_per_min"`
	FailedPerMin  float64 `json:"failed_per_min"`
	CommitsPerSec float64 `json:"commits_per_sec"`
	APIPerSec     float64 `json:"api_per_sec"`
	// TotalDone and the others count since the server started.
	TotalGrants  int64 `json:"total_grants"`
	TotalDone    int64 `json:"total_done"`
	TotalFailed  int64 `json:"total_failed"`
	TotalZombies int64 `json:"total_zombies"`
	// Per-second history for the last minute, oldest first.
	Commits []int64 `json:"commits_history"`
	Grants  []int64 `json:"grants_history"`
	Done    []int64 `json:"done_history"`
}

// FlowEdge is one transition and how often it has happened.
type FlowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int64  `json:"count"`
}

// Measures is what the collector itself can report.
type Measures struct {
	Uptime     time.Duration
	Throughput Throughput
	Latency    map[string]Stats
	Flow       []FlowEdge
	Agents     map[string]AgentUsage
	LastSeen   map[string]time.Time
}

// Read takes a consistent copy.
func (c *Collector) Read(now time.Time) Measures {
	c.mu.Lock()
	defer c.mu.Unlock()
	sec := now.Unix()
	get := func(name string) *series {
		if s := c.series[name]; s != nil {
			return s
		}
		return &series{}
	}
	// Over a young server, a full-minute rate would read too low.
	span := min(int64(now.Sub(c.started).Seconds())+1, 60)
	perMin := func(name string) float64 {
		return float64(get(name).sum(sec, span)) * 60 / float64(span)
	}
	short := min(span, 10)
	perSec := func(name string) float64 {
		return float64(get(name).sum(sec, short)) / float64(short)
	}
	m := Measures{
		Uptime: now.Sub(c.started),
		Throughput: Throughput{
			GrantsPerMin: perMin(SeriesGrants), DonePerMin: perMin(SeriesDone), FailedPerMin: perMin(SeriesFailed),
			CommitsPerSec: perSec(SeriesCommits), APIPerSec: perSec(SeriesAPI),
			TotalGrants: get(SeriesGrants).total, TotalDone: get(SeriesDone).total,
			TotalFailed: get(SeriesFailed).total, TotalZombies: get(SeriesZombies).total,
			Commits: get(SeriesCommits).tail(sec, 60), Grants: get(SeriesGrants).tail(sec, 60), Done: get(SeriesDone).tail(sec, 60),
		},
		Latency:  map[string]Stats{},
		Agents:   map[string]AgentUsage{},
		LastSeen: map[string]time.Time{},
	}
	for name, l := range c.lat {
		m.Latency[name] = l.stats()
	}
	for k, n := range c.flow {
		m.Flow = append(m.Flow, FlowEdge{k[0], k[1], n})
	}
	sort.Slice(m.Flow, func(i, j int) bool {
		if m.Flow[i].Count != m.Flow[j].Count {
			return m.Flow[i].Count > m.Flow[j].Count
		}
		return m.Flow[i].From+m.Flow[i].To < m.Flow[j].From+m.Flow[j].To
	})
	for id, a := range c.agents {
		m.Agents[id] = *a
		m.LastSeen[id] = a.last
	}
	return m
}

// Health values for a task holding a lease.
const (
	HealthOK      = "ok"
	HealthAtRisk  = "at_risk"
	HealthExpired = "expired"
	HealthQueued  = "queued"
)

// TaskLive is one task's current condition.
type TaskLive struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	State             string `json:"state"`
	Tier              int    `json:"tier"`
	Owner             string `json:"owner,omitempty"`
	Attempt           int    `json:"attempt"`
	Failures          int    `json:"failures"`
	AgeMs             int64  `json:"age_ms"`
	LeaseRemainingMs  *int64 `json:"lease_remaining_ms,omitempty"`
	ProgressRemaining *int64 `json:"progress_remaining_ms,omitempty"`
	Health            string `json:"health"`
	Reason            string `json:"reason,omitempty"`
	Tokens            int64  `json:"tokens,omitempty"`
	Spent             int64  `json:"spent,omitempty"`
	Budget            int64  `json:"budget,omitempty"`
}

// AgentLive is one agent's current condition.
type AgentLive struct {
	ID        string `json:"id"`
	Tier      int    `json:"tier"`
	State     string `json:"state"` // working, waiting, idle or gone.
	Task      string `json:"task,omitempty"`
	SeenAgoMs int64  `json:"seen_ago_ms"`
	Tokens    int64  `json:"tokens"`
	Cost      int64  `json:"cost"`
	Done      int64  `json:"done"`
	Failed    int64  `json:"failed"`
}

// TierUsage is usage summed over one tier's tasks.
type TierUsage struct {
	Tier   int   `json:"tier"`
	Tasks  int   `json:"tasks"`
	Tokens int64 `json:"tokens"`
	Spent  int64 `json:"spent"`
	Budget int64 `json:"budget"`
}

// BudgetLine is one task's budget, for those closest to running out.
type BudgetLine struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Spent  int64  `json:"spent"`
	Budget int64  `json:"budget"`
	State  string `json:"state"`
}

// Usage sums durable task state, so unlike rates it survives a restart. Money is in
// micro-dollars, as reported by agents. Without a report, it stays zero.
type Usage struct {
	Tokens     int64        `json:"tokens"`
	Spent      int64        `json:"spent"`
	Budgeted   int64        `json:"budgeted"`
	OverBudget int          `json:"over_budget"`
	ByTier     []TierUsage  `json:"by_tier"`
	Tightest   []BudgetLine `json:"tightest_budgets"`
}

// Probe is one health check.
type Probe struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyUs int64  `json:"latency_us"`
	Detail    string `json:"detail,omitempty"`
}

// Planning is the planner's condition.
type Planning struct {
	Epoch  uint64 `json:"epoch"`
	Active bool   `json:"active"`
	Phase  string `json:"phase,omitempty"`
	Lost   bool   `json:"lost,omitempty"`
}

// Snapshot is everything a dashboard shows.
type Snapshot struct {
	Time       time.Time        `json:"time"`
	Version    string           `json:"version"`
	UptimeMs   int64            `json:"uptime_ms"`
	Ready      bool             `json:"ready"`
	States     map[string]int   `json:"states"`
	Waiting    int              `json:"waiting_agents"`
	OldestMs   int64            `json:"oldest_ready_ms"`
	Throughput Throughput       `json:"throughput"`
	Latency    map[string]Stats `json:"latency"`
	Flow       []FlowEdge       `json:"flow"`
	Tasks      []TaskLive       `json:"tasks"`
	Truncated  int              `json:"tasks_not_shown"`
	Agents     []AgentLive      `json:"agents"`
	Usage      Usage            `json:"usage"`
	Planning   Planning         `json:"planning"`
	Probes     []Probe          `json:"probes"`
}

// Prometheus renders a snapshot in the Prometheus text format.
func (s Snapshot) Prometheus() string {
	var b strings.Builder
	gauge := func(name, help string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}
	counter := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	gauge("aiculator_uptime_seconds", "Time since the server started.", s.UptimeMs/1000)
	gauge("aiculator_waiting_agents", "Agents waiting for work.", s.Waiting)
	gauge("aiculator_oldest_ready_seconds", "Age of the oldest ready task.", float64(s.OldestMs)/1000)
	b.WriteString("# HELP aiculator_tasks Tasks by state.\n# TYPE aiculator_tasks gauge\n")
	for _, k := range slices.Sorted(maps.Keys(s.States)) {
		fmt.Fprintf(&b, "aiculator_tasks{state=%q} %d\n", k, s.States[k])
	}
	counter("aiculator_grants_total", "Tasks granted since start.", s.Throughput.TotalGrants)
	counter("aiculator_done_total", "Tasks finished since start.", s.Throughput.TotalDone)
	counter("aiculator_failed_total", "Task failures since start.", s.Throughput.TotalFailed)
	counter("aiculator_zombies_total", "Zombies caught since start.", s.Throughput.TotalZombies)
	gauge("aiculator_tokens", "Tokens used, summed over all tasks.", s.Usage.Tokens)
	gauge("aiculator_spent_microdollars", "Money spent, summed over all tasks.", s.Usage.Spent)
	b.WriteString("# HELP aiculator_latency_seconds Latency by operation.\n# TYPE aiculator_latency_seconds gauge\n")
	for _, k := range slices.Sorted(maps.Keys(s.Latency)) {
		st := s.Latency[k]
		for _, q := range []struct {
			name string
			v    int64
		}{{"p50", st.P50}, {"p90", st.P90}, {"p99", st.P99}, {"jitter", st.Jitter}} {
			fmt.Fprintf(&b, "aiculator_latency_seconds{op=%q,stat=%q} %g\n", k, q.name, float64(q.v)/1e6)
		}
	}
	b.WriteString("# HELP aiculator_probe_ok Whether a health probe passes.\n# TYPE aiculator_probe_ok gauge\n")
	for _, p := range s.Probes {
		v := 0
		if p.OK {
			v = 1
		}
		fmt.Fprintf(&b, "aiculator_probe_ok{probe=%q} %d\n", p.Name, v)
	}
	return b.String()
}
