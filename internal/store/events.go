package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Grano22/aiculator/internal/core"
)

// Event is one committed task revision.
type Event struct {
	Rev  uint64    `json:"rev"`
	Task core.Task `json:"task"`
}

// StartProjector follows the bucket's stream with a durable consumer and copies
// each committed revision into the event log. It runs at-least-once; the message
// ID is the revision, so a replay after a crash cannot create a duplicate.
func (s *Store) StartProjector(ctx context.Context) error {
	cons, err := s.js.CreateOrUpdateConsumer(ctx, "KV_"+bucket, jetstream.ConsumerConfig{
		Durable:       "projector",
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: "$KV." + bucket + "." + taskPrefix + ">",
		AckWait:       5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("projector consumer: %w", err)
	}
	done := make(chan struct{})
	cc, err := cons.Consume(func(m jetstream.Msg) {
		meta, err := m.Metadata()
		if err != nil {
			_ = m.Nak()
			return
		}
		id := strings.TrimPrefix(m.Subject(), "$KV."+bucket+"."+taskPrefix)
		rev := meta.Sequence.Stream
		pubCtx, cancel := opCtx()
		defer cancel()
		_, err = s.js.Publish(pubCtx, eventSubject+"."+id, m.Data(),
			jetstream.WithMsgID(fmt.Sprintf("%s:%d", id, rev)))
		if err != nil {
			_ = m.NakWithDelay(100 * time.Millisecond)
			return
		}
		_ = m.Ack()
	})
	if err != nil {
		return fmt.Errorf("projector consume: %w", err)
	}
	s.mu.Lock()
	s.projectStop = func() { cc.Stop(); close(done) }
	s.projectDone = done
	s.mu.Unlock()
	return nil
}

// TailEvents calls fn for each event at or after seq, then keeps following until
// ctx ends. The dashboard's live feed uses it.
func (s *Store) TailEvents(ctx context.Context, fn func(core.Task)) error {
	cons, err := s.stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		var t core.Task
		if json.Unmarshal(m.Data(), &t) == nil {
			fn(t)
		}
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}

// ReplayEvents returns the whole event log in commit order. Replaying it must
// produce the same final task states as the bucket, and a test asserts that.
func (s *Store) ReplayEvents(ctx context.Context) ([]core.Task, error) {
	info, err := s.stream.Info(ctx)
	if err != nil {
		return nil, err
	}
	total := info.State.Msgs
	if total == 0 {
		return nil, nil
	}
	cons, err := s.stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		return nil, err
	}
	out := make([]core.Task, 0, total)
	for uint64(len(out)) < total {
		m, err := cons.Next(jetstream.FetchContext(ctx))
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("replay read %d of %d events: %w", len(out), total, err)
			}
			return nil, err
		}
		var t core.Task
		if err := json.Unmarshal(m.Data(), &t); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		out = append(out, t)
	}
	return out, nil
}

// WaitProjected blocks until the projector has caught up with the bucket or ctx
// ends. It is used by replay and by tests.
func (s *Store) WaitProjected(ctx context.Context) error {
	for {
		c, err := s.js.Consumer(ctx, "KV_"+bucket, "projector")
		if err != nil {
			return err
		}
		info, err := c.Info(ctx)
		if err != nil {
			return err
		}
		if info.NumPending == 0 && info.NumAckPending == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
