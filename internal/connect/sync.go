package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/internal/vlog"
)

// Syncing is a side effect of the swarm, and is built so that it stays one. It reads the
// commit stream on its own goroutines, after each commit, through a durable consumer. A
// slow or dead remote system can delay a push, but it cannot delay a claim, and nothing is
// lost: an unacknowledged revision is delivered again after a restart.
//
// Each side owns different things. The remote system owns what a task says: its title,
// description, labels and priority. The swarm owns how far along it is, and the remote can
// only stop a task, through on_remote. Remembering the last status either side saw, in the
// task's link, is what stops a change from echoing between them. spec/tla/Sync.tla checks
// that this always settles, and that the two sides end up agreeing.

// Defaults for sync. A remote API has rate limits, so pushes run a few at a time.
const (
	defaultPullEvery = 5 * time.Minute
	pushWorkers      = 4
	maxBackoff       = 5 * time.Minute
	maxHookBody      = 1 << 20
	hookQueue        = 1024
)

// Status describes one source's sync.
type Status struct {
	Name      string    `json:"name"`
	Direction string    `json:"direction"`
	Push      string    `json:"push,omitempty"`
	Pull      string    `json:"pull,omitempty"`
	Pushed    int64     `json:"pushed"`
	Echoes    int64     `json:"echoes_suppressed"`
	Pulled    int64     `json:"pulled"`
	Canceled  int64     `json:"canceled_by_remote"`
	Failed    int64     `json:"failed"`
	Pending   int       `json:"pending"`
	PushP50Ms int64     `json:"push_p50_ms"`
	PushP99Ms int64     `json:"push_p99_ms"`
	LastError string    `json:"last_error,omitempty"`
	LastPush  time.Time `json:"last_push,omitzero"`
	LastPull  time.Time `json:"last_pull,omitzero"`
}

// Options configures Start.
type Options struct {
	// Open replaces how connectors are opened, for tests.
	Open func(*Source) (Connector, error)
}

// Syncer runs every source that has a sync section.
type Syncer struct {
	workers []*worker
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// pendingPush is the newest state a task must reach on the other side. Newer revisions
// replace older ones, so a burst of changes costs one call, and the last state wins.
type pendingPush struct {
	state      core.State
	seq        uint64
	committed  time.Time
	deliveries []store.Delivery
	attempts   int
	nextTry    time.Time
	busy       bool
}

type worker struct {
	src   *Source
	sync  *SyncConfig
	conn  Connector
	push  Pusher
	get   Getter
	hooks HookReader
	svc   *swarm.Service

	mu      sync.Mutex
	pending map[string]*pendingPush
	seen    map[string]core.State
	lastErr string
	samples []time.Duration
	lastP   time.Time
	lastQ   time.Time

	wake chan struct{}
	hook chan string

	pushed, echoes, pulled, canceled, failed atomic.Int64
}

// Start begins syncing. A source that cannot start is reported in Statuses, and stops
// neither the others nor the swarm.
func Start(ctx context.Context, c *Config, svc *swarm.Service, o Options) *Syncer {
	ctx, cancel := context.WithCancel(ctx)
	y := &Syncer{cancel: cancel}
	open := o.Open
	if open == nil {
		open = c.Open
	}
	for _, s := range c.Sources {
		if s.Sync == nil {
			continue
		}
		w := &worker{
			src: s, sync: s.Sync, svc: svc,
			pending: map[string]*pendingPush{}, seen: map[string]core.State{},
			wake: make(chan struct{}, 1), hook: make(chan string, hookQueue),
		}
		y.workers = append(y.workers, w)
		conn, err := open(s)
		if err != nil {
			w.setErr(err)
			log.Error("sync not started", "source", s.Name, "err", err)
			continue
		}
		w.conn = conn
		w.push, _ = conn.(Pusher)
		w.get, _ = conn.(Getter)
		w.hooks, _ = conn.(HookReader)
		w.start(ctx, &y.wg)
	}
	return y
}

// Close stops syncing and waits. Unsent changes are handed back, so the next start picks
// them up again.
func (y *Syncer) Close() {
	y.cancel()
	y.wg.Wait()
}

func (w *worker) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		w.lastErr = ""
	} else {
		w.lastErr = err.Error()
	}
}

func (w *worker) direction() string {
	if w.sync.Direction == "" {
		return "both"
	}
	return w.sync.Direction
}

func (w *worker) start(ctx context.Context, wg *sync.WaitGroup) {
	dir := w.direction()
	if dir != "pull" {
		if w.push == nil {
			w.setErr(fmt.Errorf("%s cannot be written to", w.src.Kind))
		} else {
			filter, _ := json.Marshal(w.src.Name)
			stop, err := w.svc.Store.FollowTasks(ctx, "sync-"+w.src.Name, []byte(`"`+LabelSource+`":`+string(filter)), w.onCommit)
			if err != nil {
				w.setErr(err)
				log.Error("sync cannot follow commits", "source", w.src.Name, "err", err)
			} else {
				pushed := make(chan struct{})
				wg.Go(func() {
					defer close(pushed)
					w.pushLoop(ctx)
				})
				// The order matters. Handed back while the old subscription is still open, a
				// change could be delivered straight back to this worker, and sit there.
				wg.Go(func() {
					<-ctx.Done()
					stop()
					<-pushed
					w.handBack()
				})
			}
		}
	}
	if dir != "push" {
		wg.Go(func() { w.pullLoop(ctx) })
		if w.sync.Pull.Mode == "webhook" {
			wg.Go(func() { w.hookLoop(ctx) })
		}
	}
}

// onCommit runs for each committed revision of one of this source's tasks. It only records
// what is wanted, so that a slow remote cannot back up the stream.
func (w *worker) onCommit(t core.Task, seq uint64, d store.Delivery) {
	w.mu.Lock()
	defer w.mu.Unlock()
	last, known := w.seen[t.ID]
	w.seen[t.ID] = t.State
	wanted := w.sync.Status[string(t.State)]
	onlyLast := w.sync.Push.Updates == "last" && !t.State.Terminal()
	// A heartbeat or an amendment does not change the state. That is nearly every commit.
	if wanted == "" || onlyLast || (known && last == t.State && w.pending[t.ID] == nil) {
		d.Ack()
		return
	}
	p := w.pending[t.ID]
	if p == nil {
		p = &pendingPush{}
		w.pending[t.ID] = p
	}
	if seq < p.seq {
		d.Ack() // A redelivered old revision. A newer one is already waiting.
		return
	}
	p.deliveries = append(p.deliveries, d)
	if seq == p.seq {
		return // The same one again, while it is being retried: keep its backoff.
	}
	p.seq, p.state, p.committed = seq, t.State, t.UpdatedAt
	p.attempts, p.nextTry = 0, time.Time{}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) pushLoop(ctx context.Context) {
	// A realtime push starts as soon as onCommit signals. The ticker retries failures, and
	// in interval mode it is the only thing that starts a push.
	period := time.Second
	interval := w.sync.Push.Mode == "interval"
	if interval {
		period = w.sync.Push.Every
	}
	tick := time.NewTicker(period)
	defer tick.Stop()
	sem := make(chan struct{}, pushWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			if interval {
				continue
			}
		case <-tick.C:
		}
		w.dispatch(ctx, sem, &wg)
	}
}

// handBack returns what was not sent, so that the next start does not have to wait for it.
func (w *worker) handBack() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, p := range w.pending {
		for _, d := range p.deliveries {
			d.Nak()
		}
		delete(w.pending, id)
	}
}

func (w *worker) dispatch(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	now := time.Now()
	w.mu.Lock()
	var ready []string
	for id, p := range w.pending {
		if !p.busy && !now.Before(p.nextTry) {
			ready = append(ready, id)
		}
	}
	w.mu.Unlock()
	for _, id := range ready {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		w.mu.Lock()
		p := w.pending[id]
		if p == nil || p.busy {
			w.mu.Unlock()
			<-sem
			continue
		}
		p.busy = true
		state, seq, committed := p.state, p.seq, p.committed
		w.mu.Unlock()
		wg.Go(func() {
			defer func() { <-sem }()
			w.pushOne(ctx, id, state, seq, committed)
		})
	}
}

func (w *worker) pushOne(ctx context.Context, id string, state core.State, seq uint64, committed time.Time) {
	err := w.send(ctx, id, state, committed)
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.pending[id]
	p.busy = false
	// A permanent failure will not get better with time. It is reported, and released so
	// that it cannot hold up the stream. The next state change tries again.
	if err != nil && !errors.Is(err, ErrPermanent) {
		if ctx.Err() != nil {
			return
		}
		p.attempts++
		delay := min(time.Second<<min(p.attempts, 10), maxBackoff)
		p.nextTry = time.Now().Add(delay)
		w.lastErr = err.Error()
		log.V(vlog.LevelDefault).Warn("push failed; will retry", "source", w.src.Name, "task", id, "in", delay, "err", err)
		return
	}
	if err != nil {
		w.failed.Add(1)
		w.lastErr = err.Error()
		log.Error("push refused", "source", w.src.Name, "task", id, "err", err)
	} else {
		w.lastErr = ""
	}
	for _, d := range p.deliveries {
		d.Ack()
	}
	p.deliveries = nil
	if p.seq == seq {
		delete(w.pending, id)
		return
	}
	select { // A newer state arrived during the call, so there is more to do.
	case w.wake <- struct{}{}:
	default:
	}
}

// send makes the remote status match, unless it already does. That comparison is the echo
// check: a status that came from the remote has already been recorded in the link.
func (w *worker) send(ctx context.Context, id string, state core.State, committed time.Time) error {
	want := w.sync.Status[string(state)]
	link, rev, err := w.svc.Store.GetLink(w.src.Name, id)
	if err != nil {
		return err
	}
	if rev == 0 {
		return nil // Not imported from here, so not ours to report on.
	}
	if strings.EqualFold(link.RemoteStatus, want) {
		w.echoes.Add(1)
		return nil
	}
	// Look before writing. Someone may have just canceled this over there, and a poll may
	// be minutes away, so this keeps a push from overwriting their decision.
	if w.get != nil && w.direction() == "both" {
		it, err := w.get.Get(ctx, link.Key)
		if err != nil {
			return err
		}
		if err := w.apply(ctx, it); err != nil {
			return err
		}
		if w.onRemoteCancels(it.Status) {
			return nil
		}
		if strings.EqualFold(it.Status, want) {
			w.echoes.Add(1)
			return nil
		}
		if link, rev, err = w.svc.Store.GetLink(w.src.Name, id); err != nil {
			return err
		}
	}
	if err := w.push.SetStatus(ctx, link.Key, want); err != nil {
		return err
	}
	w.pushed.Add(1)
	took := time.Since(committed)
	w.mu.Lock()
	w.samples = append(w.samples, took)
	if len(w.samples) > 4096 {
		w.samples = w.samples[2048:]
	}
	w.lastP = time.Now()
	w.mu.Unlock()
	if v := log.V(vlog.LevelChanges); v.Enabled() {
		v.Info("status pushed", "source", w.src.Name, "task", id, "status", want, "after", took)
	}
	return w.recordStatus(link, rev, want)
}

// recordStatus saves what the remote now says. If it is lost, the worst outcome is one
// repeat of the same status, which changes nothing.
func (w *worker) recordStatus(link store.Link, rev uint64, status string) error {
	for range 8 {
		link.RemoteStatus, link.UpdatedAt = status, time.Now()
		if _, err := w.svc.Store.PutLink(link, rev); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrConflict) {
			return err
		}
		var err error
		if link, rev, err = w.svc.Store.GetLink(w.src.Name, link.TaskID); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: link for %s kept changing", store.ErrConflict, link.TaskID)
}

func (w *worker) pullEvery() time.Duration {
	if w.sync.Pull.Every > 0 {
		return w.sync.Pull.Every
	}
	return defaultPullEvery
}

// pullLoop reads the remote: once at the start in every mode, which also catches what was
// missed while the server was down, and then on a schedule in interval mode.
func (w *worker) pullLoop(ctx context.Context) {
	w.pullOnce(ctx)
	if w.sync.Pull.Mode == "once" || w.sync.Pull.Mode == "webhook" {
		return
	}
	tick := time.NewTicker(w.pullEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.pullOnce(ctx)
		}
	}
}

func (w *worker) pullOnce(ctx context.Context) {
	started := time.Now()
	var since time.Time
	cursor, err := w.svc.Store.GetCursor("pull." + w.src.Name)
	if err != nil {
		w.setErr(err)
		return
	}
	if cursor != "" {
		since, _ = time.Parse(time.RFC3339, cursor)
	}
	err = w.conn.Pull(ctx, PullOptions{Since: since}, func(it Item) error { return w.apply(ctx, it) })
	if err != nil {
		if ctx.Err() == nil {
			w.setErr(err)
			log.V(vlog.LevelDefault).Warn("pull failed", "source", w.src.Name, "err", err)
		}
		return
	}
	// Saved only after a full read, so a failure reads the same stretch again.
	if err := w.svc.Store.PutCursor("pull."+w.src.Name, started.UTC().Format(time.RFC3339)); err != nil {
		w.setErr(err)
		return
	}
	w.mu.Lock()
	w.lastQ = time.Now()
	w.mu.Unlock()
}

// apply brings one remote item into the swarm.
func (w *worker) apply(ctx context.Context, it Item) error {
	if w.src.Skip(it) {
		return nil
	}
	id := TaskID(w.src.Name, it.Key)
	have, err := w.svc.Engine.Existing(ctx, []string{id})
	if err != nil {
		return err
	}
	t, exists := have[id]
	if !exists && !w.sync.Create {
		return nil
	}
	m, err := w.src.Map(it)
	if err != nil {
		w.setErr(err) // One bad item should not stop the rest.
		return nil
	}
	if exists {
		// Same version, same status: nothing happened here. Most polls are all this.
		if l, rev, err := w.svc.Store.GetLink(w.src.Name, id); err == nil && rev != 0 &&
			l.Version == m.Link.Version && l.RemoteStatus == m.Link.RemoteStatus {
			return nil
		}
	}
	res, err := w.svc.Import(ctx, []swarm.ImportItem{{Spec: m.Spec, Props: m.Props, Link: m.Link}}, true)
	if err != nil {
		return err
	}
	if len(res) == 1 && res[0].Error != "" {
		w.setErr(errors.New(res[0].Error))
		return nil
	}
	w.pulled.Add(1)
	if exists && !t.State.Terminal() && w.onRemoteCancels(m.Link.RemoteStatus) {
		if _, err := w.svc.Engine.Cancel(ctx, id, "remote_status: "+m.Link.RemoteStatus); err != nil && !errors.Is(err, core.ErrIllegal) {
			return err
		}
		w.canceled.Add(1)
	}
	return nil
}

func (w *worker) onRemoteCancels(status string) bool {
	for s, action := range w.sync.OnRemote {
		if action == "cancel" && strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}

func (w *worker) hookLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-w.hook:
			// Each delivery names an item, which is read once, here.
			it, err := w.get.Get(ctx, key)
			if err == nil {
				err = w.apply(ctx, it)
			}
			if err != nil && ctx.Err() == nil {
				w.setErr(err)
				log.V(vlog.LevelDefault).Warn("hook item failed", "source", w.src.Name, "key", key, "err", err)
			}
		}
	}
}

// ServeHook handles one webhook delivery. It answers quickly and reads the item afterwards,
// because a slow answer makes the sender retry.
func (y *Syncer) ServeHook(w http.ResponseWriter, r *http.Request, source string) {
	wk := y.worker(source)
	if wk == nil || wk.sync.Pull.Mode != "webhook" || wk.hooks == nil {
		http.Error(w, "no webhook here", http.StatusNotFound)
		return
	}
	body, err := readLimited(r)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	secret := os.Getenv(wk.sync.HookSecretEnv)
	if secret == "" {
		// Setting up: Notion sends its token unsigned, once, and it can do nothing until a
		// person has both pasted it into Notion's form and set it as the secret.
		var hello struct {
			Token string `json:"verification_token"`
		}
		if wk.src.Kind == "notion" && json.Unmarshal(body, &hello) == nil && hello.Token != "" {
			if err := wk.svc.Store.PutCursor("hook-token."+source, hello.Token); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "webhook secret is not set", http.StatusServiceUnavailable)
		return
	}
	if err := wk.hooks.VerifyHook(secret, r.Header, body); err != nil {
		log.V(vlog.LevelDefault).Warn("webhook refused", "source", source, "err", err)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	keys, err := wk.hooks.HookKeys(body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	for _, k := range keys {
		select {
		case wk.hook <- k:
		default:
			// Full, so the change waits for the catch-up read at the next start.
			log.V(vlog.LevelDefault).Warn("webhook queue is full", "source", source, "key", k)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func readLimited(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxHookBody {
		return nil, errors.New("too large")
	}
	return b, nil
}

func (y *Syncer) worker(name string) *worker {
	for _, w := range y.workers {
		if w.src.Name == name {
			return w
		}
	}
	return nil
}

// HookToken returns the token Notion sent when a webhook was first set up.
func (y *Syncer) HookToken(source string) (string, error) {
	w := y.worker(source)
	if w == nil {
		return "", fmt.Errorf("%w: no synced source named %s", store.ErrNotFound, source)
	}
	return w.svc.Store.GetCursor("hook-token." + source)
}

// SyncStatus implements api.SyncAPI.
func (y *Syncer) SyncStatus() any { return y.Statuses() }

// Statuses reports every source's sync.
func (y *Syncer) Statuses() []Status {
	out := make([]Status, 0, len(y.workers))
	for _, w := range y.workers {
		out = append(out, w.status())
	}
	return out
}

func (w *worker) status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Status{
		Name: w.src.Name, Direction: w.direction(), Pending: len(w.pending), LastError: w.lastErr,
		Pushed: w.pushed.Load(), Echoes: w.echoes.Load(), Pulled: w.pulled.Load(),
		Canceled: w.canceled.Load(), Failed: w.failed.Load(), LastPush: w.lastP, LastPull: w.lastQ,
	}
	if s.Direction != "pull" {
		s.Push = w.sync.Push.Mode
		if s.Push == "" {
			s.Push = "realtime"
		}
		if w.sync.Push.Mode == "interval" {
			s.Push += " every " + w.sync.Push.Every.String()
		}
		updates := w.sync.Push.Updates
		if updates == "" {
			updates = "partial"
		}
		s.Push += ", " + updates + " updates"
	}
	if s.Direction != "push" {
		s.Pull = w.sync.Pull.Mode
		if s.Pull == "" {
			s.Pull = "interval"
		}
		if s.Pull == "interval" {
			s.Pull += " every " + w.pullEvery().String()
		}
	}
	if n := len(w.samples); n > 0 {
		sorted := slices.Clone(w.samples)
		slices.Sort(sorted)
		s.PushP50Ms = sorted[n/2].Milliseconds()
		s.PushP99Ms = sorted[min(n*99/100, n-1)].Milliseconds()
	}
	return s
}

// Downloaders returns how to read each configured source's attachments. A source whose
// credentials are not set gets nothing here, and its links go through the plain fetcher.
func (c *Config) Downloaders() func(string) meta.Downloader {
	found := map[string]meta.Downloader{}
	for _, s := range c.Sources {
		if s.Kind != "jira" && s.Kind != "notion" {
			continue
		}
		conn, err := c.Open(s)
		if err != nil {
			log.V(vlog.LevelDefault).Info("attachments will be fetched without credentials", "source", s.Name, "err", err)
			continue
		}
		if d, ok := conn.(meta.Downloader); ok {
			found[s.Name] = d
		}
	}
	return func(name string) meta.Downloader { return found[name] }
}
