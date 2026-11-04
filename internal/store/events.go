package store

import (
	"bytes"
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

// ProjectorLag says how many committed revisions the event log has not recorded yet.
func (s *Store) ProjectorLag(ctx context.Context) (uint64, error) {
	c, err := s.js.Consumer(ctx, "KV_"+bucket, "projector")
	if err != nil {
		return 0, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return 0, err
	}
	return info.NumPending + uint64(info.NumAckPending), nil
}

// WaitProjected blocks until the projector has recorded every task revision committed before
// this call, or ctx ends. It is used by replay and by tests.
//
// It waits for the newest revision by its sequence number. Asking the consumer whether it has
// anything pending is not enough: a revision just written can take a moment to count as
// pending, so a busy machine could report "caught up" with the last events still missing.
func (s *Store) WaitProjected(ctx context.Context) error {
	src, err := s.js.Stream(ctx, "KV_"+bucket)
	if err != nil {
		return err
	}
	last, err := src.GetLastMsgForSubject(ctx, "$KV."+bucket+"."+taskPrefix+">")
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil // No task has ever been written.
	}
	if err != nil {
		return err
	}
	c, err := s.js.Consumer(ctx, "KV_"+bucket, "projector")
	if err != nil {
		return err
	}
	for {
		info, err := c.Info(ctx)
		if err != nil {
			return err
		}
		if info.AckFloor.Stream >= last.Sequence {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// followAckWait is how long after a crash a change that was mid-flight is delivered again.
// A follower still working on one sees it come back, which must do no harm.
const followAckWait = 15 * time.Second

// Delivery settles one revision. Ack means it is finished with. Nak hands it back.
type Delivery struct {
	Ack, Nak func()
}

// FollowTasks delivers every committed task revision to a durable consumer, starting from
// now on the first use, and from where it stopped after that. It reads the bucket's stream
// directly, so a follower sees a commit as soon as it lands and adds nothing to the commit.
//
// Revisions that do not contain filter are acknowledged without being decoded, which is
// what keeps a follower cheap when most tasks are not its own. A revision that is not
// acknowledged is delivered again, soon after a crash and at once after Nak.
func (s *Store) FollowTasks(ctx context.Context, durable string, filter []byte, fn func(t core.Task, seq uint64, d Delivery)) (stop func(), err error) {
	cons, err := s.js.CreateOrUpdateConsumer(ctx, "KV_"+bucket, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		FilterSubject: "$KV." + bucket + "." + taskPrefix + ">",
		AckWait:       followAckWait,
		MaxAckPending: 8192,
	})
	if err != nil {
		return nil, fmt.Errorf("consumer %s: %w", durable, err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if len(filter) > 0 && !bytes.Contains(m.Data(), filter) {
			_ = m.Ack()
			return
		}
		var t core.Task
		meta, err := m.Metadata()
		if err != nil || json.Unmarshal(m.Data(), &t) != nil {
			_ = m.Ack() // Not a task record; holding it would stop nothing but this consumer.
			return
		}
		fn(t, meta.Sequence.Stream, Delivery{Ack: func() { _ = m.Ack() }, Nak: func() { _ = m.Nak() }})
	}, jetstream.PullMaxMessages(512))
	if err != nil {
		return nil, fmt.Errorf("consume %s: %w", durable, err)
	}
	return cc.Stop, nil
}
