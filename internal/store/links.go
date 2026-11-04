package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Link ties a task to the item it came from, with what each side last said. Keeping this
// outside the task means a task knows nothing about connectors, and a sync write can
// never conflict with a commit.
type Link struct {
	Source string `json:"source"`
	Key    string `json:"key"`
	TaskID string `json:"task_id"`
	URL    string `json:"url,omitempty"`
	// Version is the item's version when last read.
	Version string `json:"version,omitempty"`
	// RemoteStatus is the remote status as last seen or set. Comparing with it is what
	// stops a change from echoing back and forth between the two systems.
	RemoteStatus string    `json:"remote_status,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
}

func (s *Store) stateKV() (jetstream.KeyValue, error) {
	s.lazy.mu.Lock()
	defer s.lazy.mu.Unlock()
	if s.lazy.state != nil {
		return s.lazy.state, nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	kv, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "connect", History: 1, Storage: jetstream.FileStorage})
	if err != nil {
		return nil, fmt.Errorf("connector bucket: %w", err)
	}
	s.lazy.state = kv
	return kv, nil
}

func linkKey(source, taskID string) string { return "link." + source + "." + taskID }

// GetLink reads a link and its revision. Revision 0 means there is none.
func (s *Store) GetLink(source, taskID string) (Link, uint64, error) {
	kv, err := s.stateKV()
	if err != nil {
		return Link{}, 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	e, err := kv.Get(ctx, linkKey(source, taskID))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Link{}, 0, nil
	}
	if err != nil {
		return Link{}, 0, err
	}
	var l Link
	if err := json.Unmarshal(e.Value(), &l); err != nil {
		return Link{}, 0, fmt.Errorf("decode link: %w", err)
	}
	return l, e.Revision(), nil
}

// PutLink writes a link with compare-and-swap. Revision 0 means it must be new.
func (s *Store) PutLink(l Link, rev uint64) (uint64, error) {
	kv, err := s.stateKV()
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	var n uint64
	if rev == 0 {
		n, err = kv.Create(ctx, linkKey(l.Source, l.TaskID), b)
	} else {
		n, err = kv.Update(ctx, linkKey(l.Source, l.TaskID), b, rev)
	}
	return n, mapErr(err)
}

// GetCursor reads a named position, such as how far a poller has read.
func (s *Store) GetCursor(name string) (string, error) {
	kv, err := s.stateKV()
	if err != nil {
		return "", err
	}
	ctx, cancel := opCtx()
	defer cancel()
	e, err := kv.Get(ctx, "cursor."+name)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(e.Value()), nil
}

// PutCursor saves a named position.
func (s *Store) PutCursor(name, value string) error {
	kv, err := s.stateKV()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	_, err = kv.PutString(ctx, "cursor."+name, value)
	return err
}
