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

	"aiculation/internal/core"
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
	s := &Store{ns: ns, nc: nc}
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

// LoadTasks reads every task. Startup recovery uses it.
func (s *Store) LoadTasks(ctx context.Context) ([]Stored, error) {
	lister, err := s.kv.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lister.Stop() }()
	var out []Stored
	for key := range lister.Keys() {
		id, ok := strings.CutPrefix(key, taskPrefix)
		if !ok {
			continue
		}
		t, rev, err := s.GetTask(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Stored{t, rev})
	}
	return out, nil
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
