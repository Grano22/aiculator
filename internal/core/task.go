// Package core is the pure task state machine. It performs no I/O and reads no
// clock: Apply(task, cmd, now, policy) is a deterministic function, which is
// what makes it possible to fuzz it and to test timeouts with a fake clock.
package core

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// State is a task lifecycle state.
type State string

const (
	Draft    State = "draft"
	Ready    State = "ready"
	Claimed  State = "claimed"
	Running  State = "running"
	Review   State = "review"
	Merging  State = "merging"
	Done     State = "done"
	Failed   State = "failed"
	Parked   State = "parked"
	Canceled State = "canceled"
)

// AllStates lists every state, for validation and for exhaustive tests.
var AllStates = []State{Draft, Ready, Claimed, Running, Review, Merging, Done, Failed, Parked, Canceled}

// Terminal reports whether no transition can leave s.
func (s State) Terminal() bool { return s == Done || s == Canceled }

// Live reports whether a task in s can still produce work for some agent.
// Parked tasks wait on a human, so they do not keep agents alive.
func (s State) Live() bool {
	switch s {
	case Draft, Ready, Claimed, Running, Review, Merging, Failed:
		return true
	}
	return false
}

// SystemPrefix marks actors that are the swarm itself rather than an agent.
// Agent identities come from signed tokens, and the token issuer refuses this
// prefix, so an agent cannot act as the system.
const SystemPrefix = "system:"

// IsSystem reports whether actor is the swarm itself.
func IsSystem(actor string) bool { return strings.HasPrefix(actor, SystemPrefix) }

// Note is one entry of a task's handoff history. A new attempt receives these,
// so escalation does not start from nothing.
type Note struct {
	Attempt int       `json:"attempt"`
	Tier    int       `json:"tier"`
	Actor   string    `json:"actor"`
	Text    string    `json:"text"`
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at"`
}

const maxNotes = 32

// Task is the durable task record. Its JSON form is the value stored in KV.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Spec  string `json:"spec,omitempty"`

	// PlanEpoch is the planning epoch that created the task; 0 means no plan.
	PlanEpoch uint64   `json:"plan_epoch,omitempty"`
	Tier      int      `json:"tier"`
	Tags      []string `json:"tags,omitempty"`
	Priority  int      `json:"priority,omitempty"`
	// Labels are small facts about a task, for people, filters and connectors. Anything
	// large belongs in the separate metadata store, because this record is rewritten on
	// every heartbeat.
	Labels map[string]string `json:"labels,omitempty"`
	// Aging overrides the server's aging steps for this task.
	Aging   []AgingStep `json:"aging,omitempty"`
	Deps    []string    `json:"deps,omitempty"`
	Touches []string    `json:"touches,omitempty"`
	Checks  []string    `json:"checks,omitempty"`

	// RequireReview demands an independent agent review as well as checks.
	RequireReview bool `json:"require_review,omitempty"`
	// AllowEmpty lets a submission with no file changes pass machine checks.
	AllowEmpty bool `json:"allow_empty,omitempty"`

	State State `json:"state"`

	Attempt    int    `json:"attempt"`
	LeaseEpoch uint64 `json:"lease_epoch"`
	Owner      string `json:"owner,omitempty"`

	LeaseDeadline    time.Time `json:"lease_deadline,omitzero"`
	ProgressDeadline time.Time `json:"progress_deadline,omitzero"`
	LastEvidence     string    `json:"last_evidence,omitempty"`

	Submitter    string `json:"submitter,omitempty"`
	Ref          string `json:"ref,omitempty"`
	ChecksRun    bool   `json:"checks_run,omitempty"`
	ChecksPassed bool   `json:"checks_passed,omitempty"`
	Verifier     string `json:"verifier,omitempty"`

	Failures int    `json:"failures"`
	Reason   string `json:"reason,omitempty"`
	Notes    []Note `json:"notes,omitempty"`

	// Budget and Spent are in micro-dollars; Budget 0 means no limit.
	Budget int64 `json:"budget,omitempty"`
	Spent  int64 `json:"spent,omitempty"`
	Tokens int64 `json:"tokens,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	ReadyAt   time.Time `json:"ready_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Clone returns a deep copy, so that Apply never mutates its input.
func (t Task) Clone() Task {
	t.Tags = slices.Clone(t.Tags)
	t.Deps = slices.Clone(t.Deps)
	t.Touches = slices.Clone(t.Touches)
	t.Checks = slices.Clone(t.Checks)
	t.Notes = slices.Clone(t.Notes)
	t.Labels = maps.Clone(t.Labels)
	t.Aging = slices.Clone(t.Aging)
	return t
}

// AgingStep raises a task's priority by Boost once it has been ready for AfterSec. The
// highest step reached counts; steps do not add up.
type AgingStep struct {
	AfterSec int64 `json:"after_sec"`
	Boost    int   `json:"boost"`
}

// After is how long a task must wait to reach this step.
func (a AgingStep) After() time.Duration { return time.Duration(a.AfterSec) * time.Second }

// AgingBoost returns the boost earned after waiting this long, and how much longer until
// the next step. A wait of 0 means there are no more steps. It needs sorted steps.
func AgingBoost(steps []AgingStep, waited time.Duration) (boost int, next time.Duration) {
	for _, s := range steps {
		if waited < s.After() {
			return boost, s.After() - waited
		}
		boost = s.Boost
	}
	return boost, 0
}

// Limits on what a task record may carry. They keep commits fast, and keep priority
// arithmetic far from overflowing.
const (
	MaxLabels     = 64
	MaxLabelValue = 256
	MaxAgingSteps = 16
	MaxPriority   = 1_000_000
)

var labelKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)

// ValidateAttrs checks the fields a person or an outside system can set. It is separate
// from Validate because these fields are set rarely, and Validate runs on every command.
func ValidateAttrs(t Task) error {
	if t.Priority > MaxPriority || t.Priority < -MaxPriority {
		return fmt.Errorf("%s: priority %d is outside +-%d", t.ID, t.Priority, MaxPriority)
	}
	if len(t.Labels) > MaxLabels {
		return fmt.Errorf("%s: %d labels, over the limit of %d", t.ID, len(t.Labels), MaxLabels)
	}
	for k, v := range t.Labels {
		if !labelKeyRE.MatchString(k) {
			return fmt.Errorf("%s: label name %q", t.ID, k)
		}
		if len(v) > MaxLabelValue {
			return fmt.Errorf("%s: label %s is longer than %d bytes", t.ID, k, MaxLabelValue)
		}
	}
	if len(t.Aging) > MaxAgingSteps {
		return fmt.Errorf("%s: %d aging steps, over the limit of %d", t.ID, len(t.Aging), MaxAgingSteps)
	}
	for i, s := range t.Aging {
		if s.AfterSec <= 0 || s.Boost > MaxPriority || s.Boost < -MaxPriority {
			return fmt.Errorf("%s: aging step %+v", t.ID, s)
		}
		if i > 0 && s.AfterSec <= t.Aging[i-1].AfterSec {
			return fmt.Errorf("%s: aging steps must be in order of time", t.ID)
		}
	}
	return nil
}

// Amendment changes what a task says, never how far along it is. A nil field is left
// alone. A label set to "" is removed.
type Amendment struct {
	Title    *string           `json:"title,omitempty"`
	Spec     *string           `json:"spec,omitempty"`
	Priority *int              `json:"priority,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Aging    *[]AgingStep      `json:"aging,omitempty"`
}

// Usage is what a piece of work consumed.
type Usage struct {
	Cost   int64 // Micro-dollars.
	Tokens int64
}

// Policy holds the settings the transitions depend on.
type Policy struct {
	MaxTier             int
	FailuresBeforeRaise int
}

// DefaultPolicy is three tiers with two failures per tier.
func DefaultPolicy() Policy { return Policy{MaxTier: 3, FailuresBeforeRaise: 2} }

// CmdKind names a state machine command.
type CmdKind string

const (
	CmdPublish   CmdKind = "publish"   // draft → ready
	CmdGrant     CmdKind = "grant"     // ready → claimed, or takes a review
	CmdHeartbeat CmdKind = "heartbeat" // extends the lease only
	CmdProgress  CmdKind = "progress"  // new evidence extends both timers
	CmdSubmit    CmdKind = "submit"    // running → review
	CmdChecks    CmdKind = "checks"    // machine check result
	CmdVerdict   CmdKind = "verdict"   // independent agent review
	CmdMerged    CmdKind = "merged"    // merging → done
	CmdFail      CmdKind = "fail"      // claimed/running/review/merging → failed
	CmdRelease   CmdKind = "release"   // claimed/running → ready, with no penalty
	CmdExpire    CmdKind = "expire"    // lease or progress timeout
	CmdRecover   CmdKind = "recover"   // failed → ready or parked
	CmdUnpark    CmdKind = "unpark"    // parked → ready
	CmdCancel    CmdKind = "cancel"    // any non-terminal → canceled
	CmdAmend     CmdKind = "amend"     // any → same state, new title, labels and so on
)

// Cmd is one command. Which fields matter depends on Kind.
type Cmd struct {
	Kind        CmdKind       `json:"kind"`
	Actor       string        `json:"actor"`
	Lease       uint64        `json:"lease,omitempty"`
	AgentTier   int           `json:"agent_tier,omitempty"`
	LeaseTTL    time.Duration `json:"lease_ttl,omitempty"`
	ProgressTTL time.Duration `json:"progress_ttl,omitempty"`
	Evidence    string        `json:"evidence,omitempty"`
	Ref         string        `json:"ref,omitempty"`
	Note        string        `json:"note,omitempty"`
	Pass        bool          `json:"pass,omitempty"`
	Cost        int64         `json:"cost,omitempty"`
	Tokens      int64         `json:"tokens,omitempty"`
	AddBudget   int64         `json:"add_budget,omitempty"`
	Amend       *Amendment    `json:"amend,omitempty"`
}

// Errors returned by Apply. Callers compare them with errors.Is.
var (
	ErrIllegal    = errors.New("illegal transition")
	ErrFenced     = errors.New("stale lease")
	ErrForbidden  = errors.New("actor not allowed")
	ErrSelfVerify = errors.New("an agent cannot verify its own work")
	ErrTier       = errors.New("agent tier below task tier")
)

func illegal(t Task, c Cmd) error {
	return fmt.Errorf("%w: %s from %s", ErrIllegal, c.Kind, t.State)
}

func (t *Task) addNote(c Cmd, text string, now time.Time) {
	if text == "" {
		return
	}
	// Clip first: the previous task shares this array, and must not see the new note.
	t.Notes = append(slices.Clip(t.Notes), Note{Attempt: t.Attempt, Tier: t.Tier, Actor: c.Actor, Text: text, Ref: c.Ref, At: now})
	if len(t.Notes) > maxNotes {
		t.Notes = t.Notes[len(t.Notes)-maxNotes:]
	}
}

func (t *Task) clearLease() {
	t.Owner = ""
	t.LeaseDeadline = time.Time{}
	t.ProgressDeadline = time.Time{}
}

// fence accepts only the current owner holding the current lease. A zombie that
// lost its lease carries an old epoch, so it is rejected here.
func fence(t Task, c Cmd) error {
	if t.Owner == "" || c.Actor != t.Owner || c.Lease != t.LeaseEpoch {
		return fmt.Errorf("%w: %s holds lease %d for %s", ErrFenced, c.Actor, c.Lease, t.ID)
	}
	return nil
}

// Apply returns the task after cmd, or an error if cmd is not legal. It never
// mutates t, and any task it returns satisfies Validate.
func Apply(t Task, c Cmd, now time.Time, p Policy) (Task, error) {
	// An old lease is reported as such in any state. Otherwise a zombie would get
	// a confusing "illegal transition" once its task has moved on.
	if !IsSystem(c.Actor) && c.Lease != 0 && c.Lease < t.LeaseEpoch {
		return t, fmt.Errorf("%w: lease %d is older than %d", ErrFenced, c.Lease, t.LeaseEpoch)
	}
	// The same goes for a lease the task has already taken back from this actor.
	switch c.Kind {
	case CmdHeartbeat, CmdProgress, CmdSubmit, CmdFail, CmdRelease:
		if !IsSystem(c.Actor) && c.Lease != 0 && t.Owner != c.Actor {
			return t, fmt.Errorf("%w: %s no longer holds %s", ErrFenced, c.Actor, t.ID)
		}
	}
	// A shallow copy is enough, and saves an allocation per list on every heartbeat. No
	// command writes into a list: addNote copies before it appends, and amend builds new ones.
	n := t
	n.UpdatedAt = now
	n.Spent += max(c.Cost, 0)
	n.Tokens += max(c.Tokens, 0)

	switch c.Kind {
	case CmdPublish:
		if t.State != Draft {
			return t, illegal(t, c)
		}
		n.State, n.ReadyAt = Ready, now

	case CmdGrant:
		if c.Actor == "" || IsSystem(c.Actor) {
			return t, fmt.Errorf("%w: grant to %q", ErrForbidden, c.Actor)
		}
		if c.AgentTier < t.Tier {
			return t, fmt.Errorf("%w: %d < %d", ErrTier, c.AgentTier, t.Tier)
		}
		switch {
		case t.State == Ready:
			n.State = Claimed
			n.Attempt++
			n.ProgressDeadline = now.Add(c.ProgressTTL)
			n.LastEvidence = ""
		case t.State == Review && t.Owner == "":
			if c.Actor == t.Submitter {
				return t, ErrSelfVerify
			}
		default:
			return t, illegal(t, c)
		}
		n.Owner = c.Actor
		n.LeaseEpoch++
		n.LeaseDeadline = now.Add(c.LeaseTTL)

	case CmdHeartbeat:
		if t.State != Claimed && t.State != Running && t.State != Review {
			return t, illegal(t, c)
		}
		if err := fence(t, c); err != nil {
			return t, err
		}
		if t.State == Claimed {
			n.State = Running
		}
		n.LeaseDeadline = now.Add(c.LeaseTTL)

	case CmdProgress:
		if t.State != Claimed && t.State != Running {
			return t, illegal(t, c)
		}
		if err := fence(t, c); err != nil {
			return t, err
		}
		n.State = Running
		n.LeaseDeadline = now.Add(c.LeaseTTL)
		// Only changed evidence counts. An agent that keeps calling progress
		// without changing anything is still a zombie.
		if c.Evidence != "" && c.Evidence != t.LastEvidence {
			n.LastEvidence = c.Evidence
			n.ProgressDeadline = now.Add(c.ProgressTTL)
		}
		if c.Ref != "" {
			n.Ref = c.Ref
		}
		n.addNote(c, c.Note, now)

	case CmdSubmit:
		if t.State != Claimed && t.State != Running {
			return t, illegal(t, c)
		}
		if err := fence(t, c); err != nil {
			return t, err
		}
		n.State = Review
		n.Submitter = t.Owner
		n.Verifier = ""
		n.ChecksRun, n.ChecksPassed = false, false
		if c.Evidence != "" {
			n.LastEvidence = c.Evidence
		}
		if c.Ref != "" {
			n.Ref = c.Ref
		}
		n.clearLease()
		n.addNote(c, c.Note, now)

	case CmdChecks:
		if !IsSystem(c.Actor) {
			return t, fmt.Errorf("%w: only the swarm reports checks", ErrForbidden)
		}
		if t.State != Review || t.ChecksRun {
			return t, illegal(t, c)
		}
		n.ChecksRun, n.ChecksPassed = true, c.Pass
		n.addNote(c, c.Note, now)
		switch {
		case !c.Pass:
			n.clearLease()
			markFailed(&n, "checks_failed")
		case !t.RequireReview:
			n.clearLease()
			n.State, n.Verifier = Merging, c.Actor
		}

	case CmdVerdict:
		if t.State != Review {
			return t, illegal(t, c)
		}
		if c.Actor == t.Submitter {
			return t, ErrSelfVerify
		}
		if err := fence(t, c); err != nil {
			return t, err
		}
		n.addNote(c, c.Note, now)
		n.clearLease()
		if !c.Pass {
			markFailed(&n, "review_rejected")
			break
		}
		// Declared checks must have passed before any reviewer can approve.
		if len(t.Checks) > 0 && !t.ChecksPassed {
			return t, fmt.Errorf("%w: checks have not passed", ErrIllegal)
		}
		n.State, n.Verifier = Merging, c.Actor

	case CmdMerged:
		if !IsSystem(c.Actor) {
			return t, fmt.Errorf("%w: only the merge queue merges", ErrForbidden)
		}
		if t.State != Merging {
			return t, illegal(t, c)
		}
		n.State = Done
		if c.Ref != "" {
			n.Ref = c.Ref
		}

	case CmdFail:
		switch t.State {
		case Claimed, Running:
			if !IsSystem(c.Actor) {
				if err := fence(t, c); err != nil {
					return t, err
				}
			}
		case Review, Merging:
			if !IsSystem(c.Actor) {
				return t, fmt.Errorf("%w: only the swarm fails %s", ErrForbidden, t.State)
			}
		default:
			return t, illegal(t, c)
		}
		n.addNote(c, c.Note, now)
		n.clearLease()
		reason := c.Note
		if reason == "" {
			reason = "agent_reported_failure"
		}
		markFailed(&n, reason)

	case CmdRelease:
		// The harness was at fault, not the task: a missing login, say, or a failed
		// launch. Counting this as a failure would escalate tasks to pricier tiers
		// for no reason.
		if t.State != Claimed && t.State != Running {
			return t, illegal(t, c)
		}
		if err := fence(t, c); err != nil {
			return t, err
		}
		n.clearLease()
		n.addNote(c, c.Note, now)
		n.State, n.ReadyAt, n.Reason = Ready, now, "released"

	case CmdExpire:
		if !IsSystem(c.Actor) {
			return t, fmt.Errorf("%w: only the swarm expires leases", ErrForbidden)
		}
		switch t.State {
		case Claimed: // Never started, so it costs the task nothing.
			n.clearLease()
			n.State, n.ReadyAt, n.Reason = Ready, now, "claim_expired"
		case Running:
			reason := c.Note
			if reason == "" {
				reason = "lease_expired"
			}
			n.addNote(c, reason, now)
			n.clearLease()
			markFailed(&n, reason)
		case Review: // The reviewer vanished; the task stays in review.
			if t.Owner == "" {
				return t, illegal(t, c)
			}
			n.clearLease()
			n.Reason = "review_lease_expired"
		default:
			return t, illegal(t, c)
		}

	case CmdRecover:
		if t.State != Failed {
			return t, illegal(t, c)
		}
		if t.Budget > 0 && n.Spent >= t.Budget {
			n.State, n.Reason = Parked, "budget_exhausted"
			break
		}
		if t.Failures >= p.FailuresBeforeRaise {
			if t.Tier >= p.MaxTier {
				n.State, n.Reason = Parked, "needs_resplit"
				break
			}
			n.Tier++
			n.Failures = 0
			n.Reason = "escalated"
		} else {
			n.Reason = "retry"
		}
		n.State, n.ReadyAt = Ready, now

	case CmdUnpark:
		if t.State != Parked {
			return t, illegal(t, c)
		}
		n.Budget += max(c.AddBudget, 0)
		n.Failures = 0
		n.State, n.ReadyAt, n.Reason = Ready, now, "unparked"
		n.addNote(c, c.Note, now)

	case CmdCancel:
		if t.State.Terminal() {
			return t, illegal(t, c)
		}
		n.clearLease()
		n.State, n.Reason = Canceled, c.Note

	case CmdAmend:
		if !IsSystem(c.Actor) {
			return t, fmt.Errorf("%w: only the swarm amends tasks", ErrForbidden)
		}
		if c.Amend == nil {
			return t, fmt.Errorf("%w: amend with nothing to change", ErrIllegal)
		}
		if a := c.Amend; a.Title != nil {
			n.Title = *a.Title
		}
		if a := c.Amend; a.Spec != nil {
			n.Spec = *a.Spec
		}
		if a := c.Amend; a.Priority != nil {
			n.Priority = *a.Priority
		}
		if a := c.Amend; a.Aging != nil {
			n.Aging = *a.Aging
		}
		if len(c.Amend.Labels) > 0 {
			n.Labels = maps.Clone(t.Labels)
			if n.Labels == nil {
				n.Labels = map[string]string{}
			}
			for k, v := range c.Amend.Labels {
				if v == "" {
					delete(n.Labels, k)
				} else {
					n.Labels[k] = v
				}
			}
			if len(n.Labels) == 0 {
				n.Labels = nil
			}
		}
		if err := ValidateAttrs(n); err != nil {
			return t, fmt.Errorf("%w: %v", ErrIllegal, err)
		}

	default:
		return t, fmt.Errorf("%w: unknown command %q", ErrIllegal, c.Kind)
	}

	if err := Validate(n); err != nil {
		return t, fmt.Errorf("refusing invalid result: %w", err)
	}
	return n, nil
}

func markFailed(n *Task, reason string) {
	n.State, n.Reason = Failed, reason
	n.Failures++
}

// Validate checks the invariants every stored task must satisfy. The crash test
// runs it over the whole store after each kill -9.
func Validate(t Task) error {
	if t.ID == "" {
		return errors.New("task without id")
	}
	if !slices.Contains(AllStates, t.State) {
		return fmt.Errorf("%s: unknown state %q", t.ID, t.State)
	}
	if t.Tier < 1 {
		return fmt.Errorf("%s: tier %d", t.ID, t.Tier)
	}
	if t.Failures < 0 || t.Attempt < 0 {
		return fmt.Errorf("%s: negative counters", t.ID)
	}
	switch t.State {
	case Claimed, Running:
		if t.Owner == "" || t.LeaseEpoch == 0 || t.LeaseDeadline.IsZero() {
			return fmt.Errorf("%s: %s without a lease", t.ID, t.State)
		}
	case Review:
		if t.Submitter == "" {
			return fmt.Errorf("%s: review without a submitter", t.ID)
		}
		if t.Owner != "" && t.Owner == t.Submitter {
			return fmt.Errorf("%s: submitter reviewing itself", t.ID)
		}
	case Merging, Done:
		if t.Owner != "" {
			return fmt.Errorf("%s: %s still has an owner", t.ID, t.State)
		}
		if t.Verifier == "" || t.Verifier == t.Submitter {
			return fmt.Errorf("%s: %s without independent verification", t.ID, t.State)
		}
	default:
		if t.Owner != "" {
			return fmt.Errorf("%s: %s still has an owner", t.ID, t.State)
		}
	}
	if t.State == Ready && t.ReadyAt.IsZero() {
		return fmt.Errorf("%s: ready without ready_at", t.ID)
	}
	return nil
}
