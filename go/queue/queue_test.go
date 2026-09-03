package queue_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

type payload struct {
	Address string `json:"address"`
	Bulk    string `json:"bulk,omitempty"`
}

type poisonPayload struct{}

func (poisonPayload) MarshalJSON() ([]byte, error) { return []byte(`"not-an-object"`), nil }

func (*poisonPayload) UnmarshalJSON([]byte) error { return errors.New("poison payload") }

func handler(context.Context, queue.Job[payload]) error { return nil }

func TestOperatorStatusIsStructurallyPayloadFree(t *testing.T) {
	typeOf := reflect.TypeOf(queue.Status{})
	if _, exists := typeOf.FieldByName("Payload"); exists {
		t.Fatal("operator status exposes payload")
	}
}

func TestRegistrationRefusals(t *testing.T) {
	rows := []struct {
		name       string
		registry   *queue.Registry
		definition queue.Definition[payload]
		code       queue.ErrorCode
	}{
		{name: "nil registry", registry: nil, definition: queue.Definition[payload]{Type: "a.b", Handle: handler}, code: queue.CodeConfigInvalid},
		{name: "empty type", registry: queue.NewRegistry(), definition: queue.Definition[payload]{Handle: handler}, code: queue.CodeConfigInvalid},
		{name: "uppercase type", registry: queue.NewRegistry(), definition: queue.Definition[payload]{Type: "Email.Welcome", Handle: handler}, code: queue.CodeConfigInvalid},
		{name: "nil handler", registry: queue.NewRegistry(), definition: queue.Definition[payload]{Type: "a.b"}, code: queue.CodeConfigInvalid},
		{name: "negative attempts", registry: queue.NewRegistry(), definition: queue.Definition[payload]{Type: "a.b", Handle: handler, MaxAttempts: -1}, code: queue.CodeConfigInvalid},
		{name: "cap below base", registry: queue.NewRegistry(), definition: queue.Definition[payload]{Type: "a.b", Handle: handler, Backoff: queue.Backoff{Base: time.Minute, Cap: time.Second}}, code: queue.CodeConfigInvalid},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := queue.Register(row.registry, row.definition)
			code, classified := queue.CodeOf(err)
			if !classified || code != row.code {
				t.Fatalf("code=%q classified=%t error=%v", code, classified, err)
			}
		})
	}
	registry := queue.NewRegistry()
	if _, err := queue.Register(registry, queue.Definition[payload]{Type: "email.welcome", Handle: handler}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Register(registry, queue.Definition[payload]{Type: "email.welcome", Handle: handler}); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
}

func TestRegistrationDefaultsAndTypeErasure(t *testing.T) {
	registry := queue.NewRegistry()
	seen := ""
	jobType, err := queue.Register(registry, queue.Definition[payload]{
		Type:        "email.welcome",
		ExclusiveBy: func(value payload) string { return value.Address },
		Handle: func(_ context.Context, job queue.Job[payload]) error {
			seen = job.Payload.Address
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, found := registry.Lookup("email.welcome")
	if !found || registration.MaxAttempts != 5 || registration.Timeout != 10*time.Minute || registration.Backoff != (queue.Backoff{Base: 5 * time.Second, Cap: 5 * time.Minute}) {
		t.Fatalf("registration=%#v found=%t", registration, found)
	}
	if names := registry.Registrations(); len(names) != 1 || names[0].Type != "email.welcome" {
		t.Fatalf("registrations=%#v", names)
	}
	pending, err := jobType.New(payload{Address: "reader@example.test"}, queue.After(time.Minute), queue.Dedupe("welcome-1"))
	if err != nil {
		t.Fatal(err)
	}
	if pending.TypeName() != "email.welcome" || pending.Delay() != time.Minute || pending.DedupeKey() != "welcome-1" || pending.ExclusiveKey() != "reader@example.test" || pending.MaxAttempts() != 5 {
		t.Fatalf("pending=%#v", pending)
	}
	if err := registration.Handle(context.Background(), pending.Payload(), queue.Meta{ID: "job-1", Attempt: 1, MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	if seen != "reader@example.test" {
		t.Fatalf("handler observed %q", seen)
	}
	outcome := queue.Classify(registration.Handle(context.Background(), []byte("not json"), queue.Meta{ID: "job-2"}))
	if outcome.Resolution != queue.ResolutionFailed {
		t.Fatalf("undecodable payload outcome=%#v", outcome)
	}
	if code, classified := queue.CodeOf(outcome.Err); !classified || code != queue.CodePayloadInvalid {
		t.Fatalf("undecodable payload code=%q classified=%t", code, classified)
	}
}

func TestNewRejectsPayloadThatCannotRoundTrip(t *testing.T) {
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[poisonPayload]{
		Type:   "gate.poison",
		Handle: func(context.Context, queue.Job[poisonPayload]) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobType.New(poisonPayload{}); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodePayloadInvalid
	}() {
		t.Fatalf("poison payload returned %v", err)
	}
}

func TestRegistryCloneIsIndependentAndKeepsResolvedHandlers(t *testing.T) {
	source := queue.NewRegistry()
	seen := ""
	if _, err := queue.Register(source, queue.Definition[payload]{
		Type: "clone.first", MaxAttempts: 7, Timeout: time.Minute, MaxConcurrent: 2,
		Backoff: queue.Backoff{Base: time.Second, Cap: time.Minute},
		Handle: func(_ context.Context, job queue.Job[payload]) error {
			seen = job.Payload.Address
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Register(source, queue.Definition[payload]{Type: "clone.second", Handle: handler}); err != nil {
		t.Fatal(err)
	}
	clone := source.Clone()
	registrations := clone.Registrations()
	if len(registrations) != 2 || registrations[0].Type != "clone.first" || registrations[1].Type != "clone.second" {
		t.Fatalf("cloned registrations=%#v", registrations)
	}
	first := registrations[0]
	if first.MaxAttempts != 7 || first.Timeout != time.Minute || first.MaxConcurrent != 2 || first.Backoff != (queue.Backoff{Base: time.Second, Cap: time.Minute}) {
		t.Fatalf("cloned resolved registration=%#v", first)
	}
	if err := first.Handle(context.Background(), []byte(`{"address":"clone@example.test"}`), queue.Meta{ID: "clone-job"}); err != nil {
		t.Fatal(err)
	}
	if seen != "clone@example.test" {
		t.Fatalf("cloned handler observed %q", seen)
	}
	if _, err := queue.Register(clone, queue.Definition[payload]{Type: "clone.only", Handle: handler}); err != nil {
		t.Fatal(err)
	}
	if _, found := source.Lookup("clone.only"); found {
		t.Fatal("clone registration mutated source")
	}
	if _, err := queue.Register(source, queue.Definition[payload]{Type: "source.only", Handle: handler}); err != nil {
		t.Fatal(err)
	}
	if _, found := clone.Lookup("source.only"); found {
		t.Fatal("source registration mutated clone")
	}
}

func TestPendingRefusesOversizedPayload(t *testing.T) {
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[payload]{Type: "email.welcome", Handle: handler})
	if err != nil {
		t.Fatal(err)
	}
	_, err = jobType.New(payload{Bulk: strings.Repeat("x", queue.MaximumPayloadBytes)})
	if code, classified := queue.CodeOf(err); !classified || code != queue.CodePayloadInvalid {
		t.Fatalf("code=%q classified=%t error=%v", code, classified, err)
	}
	if _, err := jobType.New(payload{}, queue.Dedupe("")); err == nil {
		t.Fatal("empty dedupe key was accepted")
	}
	if _, err := jobType.New(payload{}, queue.After(-time.Second)); err == nil {
		t.Fatal("negative delay was accepted")
	}
}

func TestOutcomeVocabulary(t *testing.T) {
	cause := errors.New("transport refused")
	rows := []struct {
		name       string
		err        error
		resolution queue.Resolution
		code       string
		delay      time.Duration
		scheduled  bool
		uncounted  bool
	}{
		{name: "nil succeeds", err: nil, resolution: queue.ResolutionSucceeded},
		{name: "plain error retries", err: cause, resolution: queue.ResolutionRetry},
		{name: "terminal fails", err: queue.Terminal(cause), resolution: queue.ResolutionFailed},
		{name: "retry in schedules", err: queue.RetryIn(90*time.Second, cause), resolution: queue.ResolutionRetry, delay: 90 * time.Second, scheduled: true},
		{name: "uncounted retry schedules", err: queue.RetryInWithoutAttempt(90*time.Second, cause), resolution: queue.ResolutionRetry, delay: 90 * time.Second, scheduled: true, uncounted: true},
		{name: "completed records", err: queue.CompletedWith("sprites_partial", cause), resolution: queue.ResolutionSucceeded, code: "sprites_partial"},
		{name: "wrapped terminal fails", err: fmt.Errorf("layer: %w", queue.Terminal(cause)), resolution: queue.ResolutionFailed},
		{name: "non canonical code", err: queue.CompletedWith("Sprites Partial", cause), resolution: queue.ResolutionSucceeded, code: queue.InvalidCode},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			outcome := queue.Classify(row.err)
			if outcome.Resolution != row.resolution || outcome.Code != row.code || outcome.Delay != row.delay || outcome.Scheduled != row.scheduled || outcome.Uncounted != row.uncounted {
				t.Fatalf("outcome=%#v", outcome)
			}
			if row.err != nil && !errors.Is(outcome.Err, cause) {
				t.Fatalf("cause was discarded: %v", outcome.Err)
			}
		})
	}
}

func TestBackoffIsExponentialFullJitterUnderCap(t *testing.T) {
	backoff := queue.Backoff{Base: time.Second, Cap: 8 * time.Second}
	distinct := make(map[time.Duration]struct{})
	for attempt := 1; attempt <= 6; attempt++ {
		ceiling := time.Duration(1<<(attempt-1)) * time.Second
		if ceiling > backoff.Cap {
			ceiling = backoff.Cap
		}
		for sample := 0; sample < 64; sample++ {
			delay := backoff.Delay(attempt)
			if delay < 0 || delay >= ceiling {
				t.Fatalf("attempt %d delay %s is outside [0,%s)", attempt, delay, ceiling)
			}
			distinct[delay] = struct{}{}
		}
	}
	if len(distinct) < 16 {
		t.Fatalf("full jitter produced %d distinct delays", len(distinct))
	}
}

func TestLimitsValidation(t *testing.T) {
	defaults := queue.DefaultLimits()
	if defaults.Concurrency != 4 || defaults.ClaimBatch != 16 || defaults.LeaseDuration != 30*time.Second || defaults.PollInterval != 250*time.Millisecond || defaults.ShutdownGrace != 15*time.Second || defaults.AbandonGrace != 5*time.Second || defaults.MaxPayloadBytes != queue.MaximumPayloadBytes || defaults.RetentionAge != 30*24*time.Hour || defaults.RetentionEvery != time.Minute || defaults.RetentionRows != queue.MaximumOperatorBatch {
		t.Fatalf("defaults=%#v", defaults)
	}
	if (queue.Limits{}).Resolved() != defaults {
		t.Fatalf("zero limits do not resolve to the defaults")
	}
	rows := []struct {
		name    string
		limits  queue.Limits
		refused bool
	}{
		{name: "zero is default", limits: queue.Limits{}},
		{name: "negative concurrency", limits: queue.Limits{Concurrency: -1}, refused: true},
		{name: "oversized payload", limits: queue.Limits{MaxPayloadBytes: queue.MaximumPayloadBytes + 1}, refused: true},
		{name: "brief lease", limits: queue.Limits{LeaseDuration: time.Millisecond}, refused: true},
		{name: "long lease", limits: queue.Limits{LeaseDuration: time.Hour}, refused: true},
		{name: "long abandon grace", limits: queue.Limits{AbandonGrace: 3 * time.Minute}, refused: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			err := row.limits.Validate()
			code, classified := queue.CodeOf(err)
			if row.refused != (err != nil) {
				t.Fatalf("error=%v", err)
			}
			if row.refused && (!classified || code != queue.CodeConfigInvalid) {
				t.Fatalf("code=%q classified=%t", code, classified)
			}
		})
	}
}

func TestCodeOfIgnoresForeignErrors(t *testing.T) {
	if code, classified := queue.CodeOf(errors.New("QUEUE_CONFIG_INVALID: text lookalike")); classified {
		t.Fatalf("message text was classified as %q", code)
	}
	if _, classified := queue.CodeOf(nil); classified {
		t.Fatal("nil was classified")
	}
}

func TestRetentionCanBeDisabled(t *testing.T) {
	disabled := queue.Limits{RetentionEvery: queue.RetentionDisabled}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled retention was refused: %v", err)
	}
	if resolved := disabled.Resolved(); resolved.RetentionEvery != queue.RetentionDisabled {
		t.Fatalf("disabled retention resolved to %v", resolved.RetentionEvery)
	}
	if disabled.RetentionEnabled() {
		t.Fatal("RetentionDisabled still reports retention as enabled")
	}
	for _, retaining := range []queue.Limits{{}, queue.DefaultLimits(), {RetentionAge: time.Hour}} {
		if !retaining.RetentionEnabled() {
			t.Fatalf("limits %#v no longer retain", retaining)
		}
		if resolved := retaining.Resolved(); resolved.RetentionEvery != time.Minute || resolved.RetentionAge != 30*24*time.Hour && resolved.RetentionAge != time.Hour {
			t.Fatalf("default retention schedule is %v/%v", resolved.RetentionEvery, resolved.RetentionAge)
		}
	}
	for _, refused := range []queue.Limits{{RetentionEvery: -2 * time.Second}, {RetentionAge: queue.RetentionDisabled}, {RetentionEvery: time.Second}} {
		code, classified := queue.CodeOf(refused.Validate())
		if !classified || code != queue.CodeConfigInvalid {
			t.Fatalf("limits %#v were accepted", refused)
		}
	}
}
