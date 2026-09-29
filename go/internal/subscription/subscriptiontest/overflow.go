package subscriptiontest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/subscription"
)

var (
	Generation = golem.SchemaDigest{1}
	Model      = golem.ModelID{2}
)

const (
	gatedUpdates = 10
	sentinel     = gatedUpdates + 1
)

type outcome struct {
	received map[golem.EventID]bool
	err      error
}

func AssertOverflowEndsSubscribersWithResync(t *testing.T, source subscription.SourceFactory, publish func(testing.TB, byte)) {
	t.Helper()
	opened, reopened := make(chan struct{}), make(chan struct{})
	var opens atomic.Int64
	factory := func(ctx context.Context, request events.Subscription) (events.Stream, error) {
		stream, err := source(ctx, request)
		switch opens.Add(1) {
		case 1:
			close(opened)
		case 2:
			close(reopened)
		}
		return stream, err
	}
	entered, gate := make(chan struct{}), make(chan struct{})
	var evaluations atomic.Int64
	hub, err := subscription.NewModelHub(subscription.Config[golem.EventID]{
		Generation: Generation, Model: Model, Source: factory,
		Limits: events.Limits{SubscriberQueue: 64, HubInputQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Evaluate: func(_ context.Context, notice events.Notice, _ subscription.SubscriberKey) (subscription.Evaluation[golem.EventID], error) {
			if evaluations.Add(1) == 1 {
				close(entered)
				<-gate
			}
			return subscription.Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hub.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	stream, err := hub.Subscribe(context.Background(), key(t))
	if err != nil {
		t.Fatal(err)
	}
	<-opened
	publish(t, 1)
	<-entered
	for value := byte(2); value <= gatedUpdates; value++ {
		publish(t, value)
	}
	close(gate)
	done := make(chan outcome, 1)
	go func() {
		received := map[golem.EventID]bool{}
		for {
			value, err := stream.Recv(context.Background())
			if err != nil {
				done <- outcome{received: received, err: err}
				return
			}
			if value == (golem.EventID{sentinel}) {
				done <- outcome{received: received}
				return
			}
			received[value] = true
		}
	}()
	var result outcome
	select {
	case <-reopened:
		publish(t, sentinel)
		result = <-done
	case result = <-done:
	}
	var missing []byte
	for value := byte(1); value <= gatedUpdates; value++ {
		if !result.received[golem.EventID{value}] {
			missing = append(missing, value)
		}
	}
	if len(missing) == 0 {
		t.Fatalf("the source never overflowed, so the scenario proved nothing: %v", result.err)
	}
	if result.err == nil {
		t.Fatalf("the hub kept the subscriber across a lost source and silently dropped updates %v", missing)
	}
	if code, ok := events.CodeOf(result.err); !ok || code != events.CodeSubscriptionResync {
		t.Fatalf("a subscriber that missed updates %v ended with %v, not %s", missing, result.err, events.CodeSubscriptionResync)
	}
}

func key(t testing.TB) subscription.SubscriberKey {
	t.Helper()
	identity := func(domain string) subscription.CanonicalIdentity {
		result, err := subscription.NewCanonicalIdentity(domain, []byte(domain))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result, err := subscription.NewSubscriberKey(subscription.SubscriberKeyInput{
		Generation: Generation, Model: Model, Principal: identity("principal"), PolicyGeneration: identity("policy"),
		Filter: identity("filter"), Selection: identity("selection"), Dependencies: identity("dependencies"),
		EncoderShape: identity("encoder"), Membership: identity("membership"), Shareable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func AssertTransientConnectFailureNeverSkipsSilently(t *testing.T, source subscription.SourceFactory, publish func(testing.TB, byte)) {
	t.Helper()
	connected := make(chan struct{})
	var opens atomic.Int64
	factory := func(ctx context.Context, request events.Subscription) (events.Stream, error) {
		if opens.Add(1) == 1 {
			return nil, events.Failure(events.CodeEventTransport)
		}
		stream, err := source(ctx, request)
		if err == nil && opens.Load() == 2 {
			close(connected)
		}
		return stream, err
	}
	hub, err := subscription.NewModelHub(subscription.Config[golem.EventID]{
		Generation: Generation, Model: Model, Source: factory,
		Limits: events.Limits{SubscriberQueue: 64, HubInputQueue: 8, EvaluationConcurrency: 1, RetryBase: 50 * time.Millisecond, RetryCap: 50 * time.Millisecond},
		Evaluate: func(_ context.Context, notice events.Notice, _ subscription.SubscriberKey) (subscription.Evaluation[golem.EventID], error) {
			return subscription.Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hub.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	stream, err := hub.Subscribe(context.Background(), key(t))
	if err != nil {
		t.Fatal(err)
	}
	publish(t, 1)
	<-connected
	publish(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err != nil {
		code, ok := events.CodeOf(err)
		if !ok || code != events.CodeSubscriptionResync {
			t.Fatalf("a subscriber whose source failed to connect ended with %v, not %s", err, events.CodeSubscriptionResync)
		}
		return
	}
	if value != (golem.EventID{1}) {
		t.Fatalf("the hub silently skipped an update published after Subscribe returned: first delivered %v after %d source attempts", value, opens.Load())
	}
	if value, err := stream.Recv(ctx); err != nil || value != (golem.EventID{2}) {
		t.Fatalf("second update=%v err=%v", value, err)
	}
}

type membershipCounter struct{ successes atomic.Int64 }

func (counter *membershipCounter) ObserveEvent(_ context.Context, observation events.Observation) {
	if observation.Kind() == events.ObservationHubMembership && observation.Outcome() == events.OutcomeSuccess {
		counter.successes.Add(1)
	}
}

func AssertSubscribeFailsWhenTheSourceNeverGoesLive(t *testing.T, source subscription.SourceFactory, publish func(testing.TB, byte)) {
	t.Helper()
	var opens atomic.Int64
	cancelled := make(chan struct{})
	var once atomic.Bool
	factory := func(ctx context.Context, request events.Subscription) (events.Stream, error) {
		switch opens.Add(1) {
		case 1:
			return nil, events.Failure(events.CodeEventSourceClosed)
		case 2, 3:
			if opens.Load() == 3 && once.CompareAndSwap(false, true) {
				close(cancelled)
			}
			return nil, events.Failure(events.CodeEventTransport)
		default:
			return source(ctx, request)
		}
	}
	counter := &membershipCounter{}
	hub, err := subscription.NewModelHub(subscription.Config[golem.EventID]{
		Generation: Generation, Model: Model, Source: factory, Observer: counter,
		Limits: events.Limits{SubscriberQueue: 64, HubInputQueue: 8, EvaluationConcurrency: 1, RetryBase: 20 * time.Millisecond, RetryCap: 20 * time.Millisecond},
		Evaluate: func(_ context.Context, notice events.Notice, _ subscription.SubscriberKey) (subscription.Evaluation[golem.EventID], error) {
			return subscription.Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hub.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	stream, err := hub.Subscribe(context.Background(), key(t))
	if code, ok := events.CodeOf(err); stream != nil || !ok || code != events.CodeSubscriptionSourceClosed {
		t.Fatalf("a terminal source failure before going live returned stream=%v err=%v, want no stream and %s", stream != nil, err, events.CodeSubscriptionSourceClosed)
	}
	subscribeContext, cancel := context.WithCancel(context.Background())
	go func() {
		<-cancelled
		cancel()
	}()
	stream, err = hub.Subscribe(subscribeContext, key(t))
	if code, ok := events.CodeOf(err); stream != nil || !ok || code != events.CodeSubscriptionCancelled {
		t.Fatalf("a subscribe cancelled while connecting returned stream=%v err=%v, want no stream and %s", stream != nil, err, events.CodeSubscriptionCancelled)
	}
	if got := counter.successes.Load(); got != 0 {
		t.Fatalf("failed subscribes recorded %d successful memberships", got)
	}
	live, err := hub.Subscribe(context.Background(), key(t))
	if err != nil {
		t.Fatalf("an ordinary subscribe after the failures: %v", err)
	}
	publish(t, 1)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if value, err := live.Recv(ctx); err != nil || value != (golem.EventID{1}) {
		t.Fatalf("live subscriber received %v err=%v", value, err)
	}
	if got := counter.successes.Load(); got != 1 {
		t.Fatalf("successful memberships=%d want 1", got)
	}
}
