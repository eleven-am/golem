package slog

import (
	"context"
	standardslog "log/slog"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"github.com/eleven-am/golem/go/observe"
)

type captureHandler struct {
	mu      sync.Mutex
	records []standardslog.Record
	ready   chan struct{}
}

func (*captureHandler) Enabled(context.Context, standardslog.Level) bool { return true }
func (handler *captureHandler) Handle(_ context.Context, record standardslog.Record) error {
	handler.mu.Lock()
	handler.records = append(handler.records, record.Clone())
	handler.mu.Unlock()
	select {
	case handler.ready <- struct{}{}:
	default:
	}
	return nil
}
func (handler *captureHandler) WithAttrs([]standardslog.Attr) standardslog.Handler { return handler }
func (handler *captureHandler) WithGroup(string) standardslog.Handler              { return handler }

func TestSlogAdapterEmitsOnlyStableClosedAttributes(t *testing.T) {
	handler := &captureHandler{ready: make(chan struct{}, 1)}
	adapter, err := New(Config{Logger: standardslog.New(handler), QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	model := golem.ModelID{15: 9}
	internalvalue.Emit(adapter, internalvalue.Value{
		KindValue: string(observe.KindMutation), PhaseValue: string(observe.PhaseFinish),
		OutcomeValue: string(observe.OutcomeRefused), ReasonValue: string(observe.ReasonAuthorization),
		ProviderValue: golem.PostgreSQL, ModelIDValue: model, OperationValue: string(observe.OperationMutationUpdate),
		DurationValue: 7 * time.Millisecond, StatementCountValue: 3, AttemptValue: 2,
		QueueDepthValue: 4, QueueLimitValue: 8, AggregateCountValue: 5,
	})
	select {
	case <-handler.ready:
	case <-time.After(time.Second):
		t.Fatal("slog record not delivered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := adapter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.records) != 1 || handler.records[0].Message != "golem.observation.v1" || handler.records[0].Level != standardslog.LevelInfo {
		t.Fatalf("records=%v", handler.records)
	}
	got := make(map[string]any)
	handler.records[0].Attrs(func(attribute standardslog.Attr) bool {
		got[attribute.Key] = attribute.Value.Any()
		return true
	})
	want := map[string]any{
		"golem.kind": "mutation", "golem.phase": "finish", "golem.outcome": "refused",
		"golem.reason": "authorization", "golem.provider": "postgresql",
		"golem.operation": "mutation.update", "golem.model_id": "00000000000000000000000000000009",
		"golem.duration_ns": int64(7 * time.Millisecond), "golem.statement_count": int64(3),
		"golem.attempt": int64(2), "golem.queue_depth": int64(4), "golem.queue_limit": int64(8),
		"golem.aggregate_count": int64(5),
	}
	if len(got) != len(want) {
		t.Fatalf("attribute count=%d values=%v", len(got), got)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("attribute %s=%v want %v", key, got[key], expected)
		}
	}
}

func TestSlogAdapterRequiresLogger(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil logger accepted")
	}
}
