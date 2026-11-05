// Package store wraps embedded NATS JetStream. The task KV bucket is the only
// source of truth, and a single compare-and-swap write to it is the commit point
// of every transition. The event log is derived from the bucket's own stream, so
// a crash can never leave the state and the log disagreeing permanently.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Grano22/aiculator/internal/core"
)

const (
	bucket       = "swarm"
	presenceName = "presence"
	eventsStream = "EVENTS"
	eventSubject = "swarm.ev"
	taskPrefix   = "task."
	planningKey  = "planning"
	opTimeout    = 5 * time.Second
)

// ErrConflict means a compare-and-swap lost: someone else wrote the key.
var ErrConflict = errors.New("stale revision")

// ErrNotFound means the key does not exist.
var ErrNotFound = errors.New("not found")

// Options configures the embedded server.
type Options struct {
	Dir string
	// SyncAlways fsyncs every write. kill -9 does not need it, because written
	// data survives in the page cache. Power loss does.
	SyncAlways bool
}

// Store owns the embedded server and its streams.
type Store struct {
	ns       *server.Server
	nc       *nats.Conn
	js       jetstream.JetStream
	kv       jetstream.KeyValue
	presence jetstream.KeyValue
	stream   jetstream.Stream
	dir      string
	lazy     lazyBuckets

	mu          sync.Mutex
	projectStop func()
	projectDone chan struct{}
}

// Open starts the embedded server over an in-process connection, with no network
// listener, and creates the bucket and streams if they are missing.
func Open(ctx context.Context, o Options) (*Store, error) {
	ns, err := server.NewServer(&server.Options{
		ServerName: "swarm",
		JetStream:  true,
		StoreDir:   o.Dir,
		DontListen: true,
		NoSigs:     true,
		NoLog:      true,
		SyncAlways: o.SyncAlways,
	})
	if err != nil {
		return nil, fmt.Errorf("nats server: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(30 * time.Second) {
		ns.Shutdown()
		return nil, errors.New("nats server did not become ready")
	}
	nc, err := nats.Connect("", nats.InProcessServer(ns), nats.MaxReconnects(-1), nats.Name("swarm"))
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	s := &Store{ns: ns, nc: nc, dir: o.Dir}
	if err := s.init(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(ctx context.Context) error {
	var err error
	if s.js, err = jetstream.New(s.nc); err != nil {
		return err
	}
	if s.kv, err = s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  bucket,
		History: 16,
		Storage: jetstream.FileStorage,
	}); err != nil {
		return fmt.Errorf("task bucket: %w", err)
	}
	// Presence shows who is around. It is informational only: leases run on the
	// writer's clock, because a fake clock cannot control a server-side TTL.
	if s.presence, err = s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         presenceName,
		Storage:        jetstream.FileStorage,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		return fmt.Errorf("presence bucket: %w", err)
	}
	if s.stream, err = s.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       eventsStream,
		Subjects:   []string{eventSubject + ".>"},
		Storage:    jetstream.FileStorage,
		Duplicates: time.Hour,
	}); err != nil {
		return fmt.Errorf("events stream: %w", err)
	}
	return nil
}

// Close stops the projector and shuts the server down.
func (s *Store) Close() {
	s.mu.Lock()
	stop, done := s.projectStop, s.projectDone
	s.projectStop = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	if s.nc != nil {
		s.nc.Close()
	}
	s.ns.Shutdown()
	s.ns.WaitForShutdown()
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return ErrNotFound
	case errors.Is(err, jetstream.ErrKeyRevisionMismatch), errors.Is(err, jetstream.ErrKeyExists):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

// CreateTask writes a new task and fails if the ID is already taken.
func (s *Store) CreateTask(t core.Task) (uint64, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	rev, err := s.kv.Create(ctx, taskPrefix+t.ID, b)
	return rev, mapErr(err)
}

// UpdateTask is the commit point: a compare-and-swap on the task's revision.
func (s *Store) UpdateTask(t core.Task, rev uint64) (uint64, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	n, err := s.kv.Update(ctx, taskPrefix+t.ID, b, rev)
	return n, mapErr(err)
}

// GetTask reads one task and its revision.
func (s *Store) GetTask(id string) (core.Task, uint64, error) {
	ctx, cancel := opCtx()
	defer cancel()
	e, err := s.kv.Get(ctx, taskPrefix+id)
	if err != nil {
		return core.Task{}, 0, mapErr(err)
	}
	var t core.Task
	if err := json.Unmarshal(e.Value(), &t); err != nil {
		return core.Task{}, 0, fmt.Errorf("decode %s: %w", id, err)
	}
	return t, e.Revision(), nil
}

// Stored pairs a task with its revision.
type Stored struct {
	Task core.Task
	Rev  uint64
}

// Unreadable is a stored record that cannot be decoded. It is kept, not thrown away.
type Unreadable struct {
	ID  string
	Raw []byte
	Rev uint64
	Err error
}

// LoadTasks reads every task. Startup recovery uses it. One record that cannot be decoded
// must not stop the swarm from starting, so it is returned apart for the caller to handle.
func (s *Store) LoadTasks(ctx context.Context) ([]Stored, []Unreadable, error) {
	lister, err := s.kv.ListKeys(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = lister.Stop() }()
	var out []Stored
	var bad []Unreadable
	for key := range lister.Keys() {
		id, ok := strings.CutPrefix(key, taskPrefix)
		if !ok {
			continue
		}
		getCtx, cancel := opCtx()
		e, err := s.kv.Get(getCtx, key)
		cancel()
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var t core.Task
		if err := json.Unmarshal(e.Value(), &t); err != nil {
			bad = append(bad, Unreadable{ID: id, Raw: e.Value(), Rev: e.Revision(), Err: err})
			continue
		}
		if t.ID == "" {
			t.ID = id // The key says which task it is, even if the record lost it.
		}
		out = append(out, Stored{t, e.Revision()})
	}
	return out, bad, nil
}

// Quarantine keeps a record as it was before a repair replaced it, so nothing is lost.
func (s *Store) Quarantine(id string, rev uint64, raw []byte, why string) error {
	kv, err := s.quarantineKV()
	if err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{"task_id": id, "rev": rev, "why": why, "at": time.Now().UTC(), "raw": string(raw)})
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	_, err = kv.Put(ctx, fmt.Sprintf("q.%s.%d", id, rev), b)
	return err
}

// Quarantined lists what has been kept from repairs.
func (s *Store) Quarantined(ctx context.Context) ([]json.RawMessage, error) {
	kv, err := s.quarantineKV()
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lister.Stop() }()
	var out []json.RawMessage
	for k := range lister.Keys() {
		e, err := kv.Get(ctx, k)
		if err == nil {
			out = append(out, e.Value())
		}
	}
	return out, nil
}

// RawPut overwrites a task with compare-and-swap, without the state machine. Only repair uses
// it, for a record the state machine cannot accept because it is already broken.
func (s *Store) RawPut(t core.Task, rev uint64) (uint64, error) {
	return s.UpdateTask(t, rev)
}

// Planning is the durable planner lease.
type Planning struct {
	Epoch     uint64      `json:"epoch"`
	Committed uint64      `json:"committed"`
	Active    bool        `json:"active"`
	Planner   string      `json:"planner,omitempty"`
	Phase     string      `json:"phase,omitempty"`
	Expected  map[int]int `json:"expected_tasks,omitempty"`
	ETAMs     int64       `json:"eta_ms,omitempty"`
	Deadline  time.Time   `json:"deadline,omitzero"`
	UpdatedAt time.Time   `json:"updated_at,omitzero"`
	LostAt    time.Time   `json:"lost_at,omitzero"`
}

// GetPlanning returns the planner lease; revision 0 means it was never written.
func (s *Store) GetPlanning() (Planning, uint64, error) {
	ctx, cancel := opCtx()
	defer cancel()
	e, err := s.kv.Get(ctx, planningKey)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Planning{}, 0, nil
	}
	if err != nil {
		return Planning{}, 0, err
	}
	var p Planning
	if err := json.Unmarshal(e.Value(), &p); err != nil {
		return Planning{}, 0, fmt.Errorf("decode planning: %w", err)
	}
	return p, e.Revision(), nil
}

// PutPlanning writes the planner lease with compare-and-swap.
func (s *Store) PutPlanning(p Planning, rev uint64) (uint64, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	var n uint64
	if rev == 0 {
		n, err = s.kv.Create(ctx, planningKey, b)
	} else {
		n, err = s.kv.Update(ctx, planningKey, b, rev)
	}
	return n, mapErr(err)
}

// RawPutForTest overwrites a key without compare-and-swap, simulating a foreign
// writer. Tests use it to prove that stale writes are rejected.
func (s *Store) RawPutForTest(t core.Task) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	_, err = s.kv.Put(ctx, taskPrefix+t.ID, b)
	return err
}

// RawBytesForTest stores bytes under a task's key, as damage would. Tests use it.
func (s *Store) RawBytesForTest(id string, b []byte) error {
	ctx, cancel := opCtx()
	defer cancel()
	_, err := s.kv.Put(ctx, taskPrefix+id, b)
	return err
}

// TouchPresence records that an agent is around, for ttl from now. A key's TTL
// can be set only at create, so renewal purges and recreates the key.
func (s *Store) TouchPresence(agent string, info []byte, ttl time.Duration) error {
	ctx, cancel := opCtx()
	defer cancel()
	key := "agent." + agent
	if _, err := s.presence.Create(ctx, key, info, jetstream.KeyTTL(ttl)); err == nil {
		return nil
	}
	if err := s.presence.Purge(ctx, key); err != nil {
		return err
	}
	_, err := s.presence.Create(ctx, key, info, jetstream.KeyTTL(ttl))
	return err
}

// PresentAgents lists agents whose presence key has not yet expired.
func (s *Store) PresentAgents(ctx context.Context) ([]string, error) {
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lister.Stop() }()
	var out []string
	for k := range lister.Keys() {
		out = append(out, strings.TrimPrefix(k, "agent."))
	}
	return out, nil
}
