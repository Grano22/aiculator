package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

func benchTask() core.Task {
	now := time.Now()
	return core.Task{ID: "t-123", Title: "a task with a title", Spec: "do the thing well", Tier: 2, Priority: 3,
		Labels: map[string]string{"severity": "critical", "source": "jira"}, Deps: []string{"a", "b"},
		Touches: []string{"internal/x"}, Checks: []string{"go test ./..."}, State: core.Running, Attempt: 1,
		LeaseEpoch: 7, Owner: "w1", LeaseDeadline: now, ProgressDeadline: now, CreatedAt: now, UpdatedAt: now, ReadyAt: now}
}

func BenchmarkMarshalTaskValue(b *testing.B) {
	t := benchTask()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = json.Marshal(t)
	}
}

func BenchmarkMarshalTaskPointer(b *testing.B) {
	t := benchTask()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = json.Marshal(&t)
	}
}
