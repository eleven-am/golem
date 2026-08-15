package subscription

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
)

func TestEquivalentSubscriberGroupingOracle(t *testing.T) {
	source := newFakeSource()
	var evaluations atomic.Int64
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(_ context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
		evaluations.Add(1)
		return Deliver(notice.EventID()), nil
	}, nil)
	key := testKey(t, "principal", "policy", "filter", "selection", "dependencies", "encoder", "member-a", true)
	other := testKey(t, "principal", "policy", "filter", "selection", "dependencies", "encoder", "member-b", true)
	first := subscribe(t, hub, key)
	second := subscribe(t, hub, other)
	notice := testNotice(t, 1)
	source.send(notice)
	if got := recv(t, first); got != notice.EventID() {
		t.Fatal("first subscriber received wrong event")
	}
	if got := recv(t, second); got != notice.EventID() {
		t.Fatal("second subscriber received wrong event")
	}
	if evaluations.Load() != 1 {
		t.Fatalf("equivalent subscribers evaluated %d times", evaluations.Load())
	}
	shutdown(t, hub)
}

func TestDifferentPrincipalsNeverShareEvaluation(t *testing.T) {
	assertDifferentDimensionDoesNotShare(t, func(input *keyStrings) { input.principal = "another-principal" })
}

func TestDifferentSelectionFilterOrPolicyVersionNeverShareResult(t *testing.T) {
	tests := map[string]func(*keyStrings){
		"policy":       func(input *keyStrings) { input.policy = "policy-v2" },
		"filter":       func(input *keyStrings) { input.filter = "other-filter" },
		"selection":    func(input *keyStrings) { input.selection = "other-selection" },
		"dependencies": func(input *keyStrings) { input.dependencies = "other-dependencies" },
		"encoder":      func(input *keyStrings) { input.encoder = "other-encoder" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) { assertDifferentDimensionDoesNotShare(t, mutate) })
	}
}

func TestAuditPrincipalCollisionDoesNotAuthorizeSharing(t *testing.T) {
	// SubscriberKey intentionally has no audit-principal field. Two equal audit
	// labels therefore cannot erase the distinct retained-principal identities.
	assertDifferentDimensionDoesNotShare(t, func(input *keyStrings) { input.principal = "different-snapshot-same-audit-label" })
}

func TestNonShareableEvaluationIncludesMembershipIdentity(t *testing.T) {
	source := newFakeSource()
	var evaluations atomic.Int64
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(_ context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
		evaluations.Add(1)
		return Deliver(notice.EventID()), nil
	}, nil)
	first := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "one", false))
	second := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "two", false))
	source.send(testNotice(t, 1))
	_ = recv(t, first)
	_ = recv(t, second)
	if evaluations.Load() != 2 {
		t.Fatalf("non-shareable work evaluated %d times", evaluations.Load())
	}
	shutdown(t, hub)
}

func TestStatefulMembershipOwnsStateUntilRemovalAndDropsExactlyOnce(t *testing.T) {
	source := newFakeSource()
	state := &struct{ marker string }{marker: "owned"}
	seen := make(chan any, 1)
	var drops atomic.Int64
	hub, err := NewModelHub(Config[golem.EventID]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2},
		Limits: events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source: sourceFactory(source),
		EvaluateState: func(_ context.Context, notice events.Notice, _ SubscriberKey, retained any) (Evaluation[golem.EventID], error) {
			seen <- retained
			return Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := hub.SubscribeWithState(ctx, testKey(t, "p", "v", "f", "s", "d", "e", "member", false), state, func(retained any) {
		if retained != state {
			t.Errorf("dropped state = %p, want %p", retained, state)
		}
		drops.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	source.send(testNotice(t, 1))
	if got := recv(t, stream); got != (golem.EventID{1}) {
		t.Fatal("stateful subscriber received wrong event")
	}
	select {
	case retained := <-seen:
		if retained != state {
			t.Fatal("evaluator received another member's state")
		}
	case <-time.After(time.Second):
		t.Fatal("stateful evaluator did not run")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for drops.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if drops.Load() != 1 {
		t.Fatalf("drop count = %d", drops.Load())
	}
	_ = stream.Close()
	if drops.Load() != 1 {
		t.Fatalf("drop repeated after Close: %d", drops.Load())
	}
	shutdown(t, hub)
}

func TestStatefulSubscriptionRefusesShareableKeyBeforeSourceStart(t *testing.T) {
	var starts atomic.Int64
	hub, err := NewModelHub(Config[golem.EventID]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2}, Limits: events.Limits{},
		Source: func(context.Context, events.Subscription) (events.Stream, error) {
			starts.Add(1)
			return nil, events.Failure(events.CodeSubscriptionSourceClosed)
		},
		EvaluateState: func(context.Context, events.Notice, SubscriberKey, any) (Evaluation[golem.EventID], error) {
			return Evaluation[golem.EventID]{}, nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "member", true), struct{}{}, nil); code(t, err) != events.CodeSubscriptionInvalid {
		t.Fatalf("shareable stateful subscription error = %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("source started %d times", starts.Load())
	}
	shutdown(t, hub)
}

func TestSubscriptionStreamStopInstallerHandlesBothCloseOrderings(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		name := "install-before-close"
		if closeFirst {
			name = "close-before-install"
		}
		t.Run(name, func(t *testing.T) {
			item := &member[int]{id: 1, runID: 1, done: make(chan struct{}), queue: make(chan int, 1)}
			hub := &ModelHub[int]{members: map[uint64]*member[int]{item.id: item}, runs: make(map[uint64]*hubRun[int])}
			stream := &Stream[int]{hub: hub, id: item.id, member: item}
			stops := 0
			stop := func() bool {
				stops++
				return true
			}
			if closeFirst {
				stream.closeWith(events.CodeSubscriptionCancelled)
				stream.installStop(stop)
			} else {
				stream.installStop(stop)
				stream.closeWith(events.CodeSubscriptionCancelled)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if stops != 1 {
				t.Fatalf("stop callback count=%d", stops)
			}
			select {
			case <-item.done:
			default:
				t.Fatal("closed subscriber did not signal completion")
			}
			if _, present := hub.members[item.id]; present {
				t.Fatal("closed subscriber remains registered")
			}
			stream.stopMu.Lock()
			closed, retainedStop := stream.closed, stream.stop
			stream.stopMu.Unlock()
			if !closed || retainedStop != nil {
				t.Fatalf("closed=%t retainedStop=%t", closed, retainedStop != nil)
			}
		})
	}
}

func TestStateCleanupMayBlockAndReenterCloseWithoutHoldingHubLock(t *testing.T) {
	source := newFakeSource()
	hub, err := NewModelHub(Config[golem.EventID]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2},
		Limits: events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source: sourceFactory(source),
		EvaluateState: func(_ context.Context, notice events.Notice, _ SubscriberKey, _ any) (Evaluation[golem.EventID], error) {
			return Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, reentered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first *Stream[golem.EventID]
	first, err = hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "one", false), struct{}{}, func(any) {
		close(entered)
		<-release
		_ = first.Close()
		close(reentered)
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		_ = first.Close()
		close(closed)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	// A blocked application cleanup must not own hub.mu: membership work on the
	// same hub must remain immediately available.
	secondReady := make(chan *Stream[golem.EventID], 1)
	go func() {
		stream, subscribeErr := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "two", false), struct{}{}, nil)
		if subscribeErr == nil {
			secondReady <- stream
			return
		}
		secondReady <- nil
	}()
	var second *Stream[golem.EventID]
	select {
	case second = <-secondReady:
		if second == nil {
			t.Fatal("membership failed while cleanup blocked")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked cleanup held the hub mutex")
	}
	close(release)
	select {
	case <-reentered:
	case <-time.After(time.Second):
		t.Fatal("cleanup reentrant Close deadlocked")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("outer Close did not return")
	}
	_ = second.Close()
	shutdown(t, hub)
}

func TestSharedEvaluationStillOwnsEachSubscriberResult(t *testing.T) {
	source := newFakeSource()
	var evaluations atomic.Int64
	hub, err := NewModelHub(Config[[]byte]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2},
		Limits: events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source: sourceFactory(source),
		Evaluate: func(context.Context, events.Notice, SubscriberKey) (Evaluation[[]byte], error) {
			evaluations.Add(1)
			return Deliver([]byte{1, 2, 3}), nil
		},
		Clone: func(value []byte) ([]byte, error) { return bytes.Clone(value), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	first := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m1", true))
	second := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m2", true))
	source.send(testNotice(t, 1))
	firstValue, secondValue := recv(t, first), recv(t, second)
	firstValue[0] = 9
	if !bytes.Equal(secondValue, []byte{1, 2, 3}) {
		t.Fatal("fan-out shared mutable result storage")
	}
	if evaluations.Load() != 1 {
		t.Fatalf("shared evaluation count = %d", evaluations.Load())
	}
	shutdown(t, hub)
}

func TestSubscriberQueueExactBoundaryAndOverflowDisconnect(t *testing.T) {
	source := newFakeSource()
	evaluated := make(chan golem.EventID, 3)
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(_ context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
		evaluated <- notice.EventID()
		return Deliver(notice.EventID()), nil
	}, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	source.send(testNotice(t, 1))
	waitID(t, evaluated)
	if got := recv(t, stream); got != (golem.EventID{1}) {
		t.Fatal("exact queue boundary did not deliver")
	}
	source.send(testNotice(t, 2))
	waitID(t, evaluated)
	source.send(testNotice(t, 3))
	waitID(t, evaluated)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); code(t, err) != events.CodeSubscriptionOverflow {
		t.Fatalf("overflow error = %v", err)
	}
	shutdown(t, hub)
}

func TestEvaluationConcurrencyHardBound(t *testing.T) {
	source := newFakeSource()
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var active, maximum atomic.Int64
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(ctx context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			active.Add(-1)
			return Evaluation[golem.EventID]{}, ctx.Err()
		case <-release:
		}
		active.Add(-1)
		return Deliver(notice.EventID()), nil
	}, nil)
	streams := make([]*Stream[golem.EventID], 4)
	for index := range streams {
		streams[index] = subscribe(t, hub, testKey(t, "p"+string(rune('a'+index)), "v", "f", "s", "d", "e", "m"+string(rune('a'+index)), true))
	}
	source.send(testNotice(t, 1))
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("evaluators did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("evaluation concurrency exceeded hard bound")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, stream := range streams {
		_ = recv(t, stream)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	shutdown(t, hub)
}

func TestCancelDuringEvaluationUnwinds(t *testing.T) {
	source := newFakeSource()
	started, stopped := make(chan struct{}), make(chan struct{})
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(ctx context.Context, _ events.Notice, _ SubscriberKey) (Evaluation[int], error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return Evaluation[int]{}, ctx.Err()
	}, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	source.send(testNotice(t, 1))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("evaluation did not start")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("evaluation context was not cancelled")
	}
	shutdown(t, hub)
}

func TestCancellationDoesNotStartQueuedEvaluation(t *testing.T) {
	source := newFakeSource()
	started := make(chan struct{})
	var calls atomic.Int64
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(ctx context.Context, _ events.Notice, _ SubscriberKey) (Evaluation[int], error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return Evaluation[int]{}, ctx.Err()
	}, nil)
	first := subscribe(t, hub, testKey(t, "p1", "v", "f", "s", "d", "e", "m1", true))
	second := subscribe(t, hub, testKey(t, "p2", "v", "f", "s", "d", "e", "m2", true))
	source.send(testNotice(t, 1))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first evaluation did not start")
	}
	_ = first.Close()
	_ = second.Close()
	shutdown(t, hub)
	if calls.Load() != 1 {
		t.Fatalf("Evaluate started %d times after cancellation with one queued job", calls.Load())
	}
}

func TestTransportReconnectPreservesSubscriber(t *testing.T) {
	first, second := newFakeSource(), newFakeSource()
	factory := sequentialFactory(first, second)
	hub := newTestHub(t, factory, events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	first.fail(events.Failure(events.CodeEventTransport))
	select {
	case <-second.opened:
	case <-time.After(time.Second):
		t.Fatal("source did not reconnect")
	}
	notice := testNotice(t, 1)
	second.send(notice)
	if got := recv(t, stream); got != notice.EventID() {
		t.Fatal("subscriber was not preserved across reconnect")
	}
	shutdown(t, hub)
}

func TestHubPreservesAtLeastOnceDuplicates(t *testing.T) {
	source := newFakeSource()
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 2, HubInputQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	notice := testNotice(t, 1)
	source.send(notice)
	source.send(notice)
	if first, second := recv(t, stream), recv(t, stream); first != notice.EventID() || second != notice.EventID() {
		t.Fatal("at-least-once duplicate was changed or suppressed")
	}
	hub.mu.Lock()
	for _, run := range hub.runs {
		if cap(run.input) != 1 {
			t.Fatalf("hub input capacity = %d", cap(run.input))
		}
	}
	hub.mu.Unlock()
	shutdown(t, hub)
}

func TestTerminalSourceCloseDisconnectsWithSanitizedCode(t *testing.T) {
	source := newFakeSource()
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	source.fail(events.Failure(events.CodeEventSourceClosed))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); code(t, err) != events.CodeSubscriptionSourceClosed || err.Error() != string(events.CodeSubscriptionSourceClosed) {
		t.Fatalf("terminal source error = %v", err)
	}
	shutdown(t, hub)
}

func TestImmediateTerminalSourceFailureCannotOrphanFirstMember(t *testing.T) {
	factory := func(context.Context, events.Subscription) (events.Stream, error) {
		return nil, events.Failure(events.CodeEventSourceClosed)
	}
	hub := newTestHub(t, factory, events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); code(t, err) != events.CodeSubscriptionSourceClosed {
		t.Fatalf("immediate terminal failure = %v", err)
	}
	shutdown(t, hub)
}

func TestSourceCloseLastMemberAndApplicationShutdownNoLeak(t *testing.T) {
	source := newFakeSource()
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, nil)
	first := subscribe(t, hub, testKey(t, "p1", "v", "f", "s", "d", "e", "m1", true))
	second := subscribe(t, hub, testKey(t, "p2", "v", "f", "s", "d", "e", "m2", true))
	_ = first.Close()
	select {
	case <-source.closed:
		t.Fatal("source closed before last member left")
	case <-time.After(20 * time.Millisecond):
	}
	_ = second.Close()
	select {
	case <-source.closed:
	case <-time.After(time.Second):
		t.Fatal("last member did not close source")
	}
	shutdown(t, hub)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.members) != 0 || len(hub.runs) != 0 {
		t.Fatalf("lifecycle retained members=%d runs=%d", len(hub.members), len(hub.runs))
	}
}

func TestObserverPanicIsCorrectnessNeutral(t *testing.T) {
	source := newFakeSource()
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, identityEvaluator, panicObserver{})
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "m", true))
	notice := testNotice(t, 1)
	source.send(notice)
	if got := recv(t, stream); got != notice.EventID() {
		t.Fatal("observer panic changed delivery")
	}
	shutdown(t, hub)
}

func assertDifferentDimensionDoesNotShare(t *testing.T, mutate func(*keyStrings)) {
	t.Helper()
	source := newFakeSource()
	var evaluations atomic.Int64
	hub := newTestHub(t, sourceFactory(source), events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond}, func(_ context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
		evaluations.Add(1)
		return Deliver(notice.EventID()), nil
	}, nil)
	base := keyStrings{"principal", "policy", "filter", "selection", "dependencies", "encoder", "member-a"}
	other := base
	other.membership = "member-b"
	mutate(&other)
	first := subscribe(t, hub, base.build(t, true))
	second := subscribe(t, hub, other.build(t, true))
	source.send(testNotice(t, 1))
	_ = recv(t, first)
	_ = recv(t, second)
	if evaluations.Load() != 2 {
		t.Fatalf("different key dimension evaluated %d times", evaluations.Load())
	}
	shutdown(t, hub)
}

type keyStrings struct{ principal, policy, filter, selection, dependencies, encoder, membership string }

func (input keyStrings) build(t testing.TB, shareable bool) SubscriberKey {
	return testKey(t, input.principal, input.policy, input.filter, input.selection, input.dependencies, input.encoder, input.membership, shareable)
}
func testKey(t testing.TB, principal, policy, filter, selection, dependencies, encoder, membership string, shareable bool) SubscriberKey {
	t.Helper()
	identity := func(domain, value string) CanonicalIdentity {
		result, err := NewCanonicalIdentity(domain, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result, err := NewSubscriberKey(SubscriberKeyInput{Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2}, Principal: identity("principal", principal), PolicyGeneration: identity("policy", policy), Filter: identity("filter", filter), Selection: identity("selection", selection), Dependencies: identity("dependencies", dependencies), EncoderShape: identity("encoder", encoder), Membership: identity("membership", membership), Shareable: shareable})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newTestHub[T any](t testing.TB, source SourceFactory, limits events.Limits, evaluator Evaluator[T], observer events.Observer) *ModelHub[T] {
	t.Helper()
	hub, err := NewModelHub(Config[T]{Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2}, Limits: limits, Source: source, Evaluate: evaluator, Clone: func(value T) (T, error) { return value, nil }, Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	return hub
}
func subscribe[T any](t testing.TB, hub *ModelHub[T], key SubscriberKey) *Stream[T] {
	t.Helper()
	stream, err := hub.Subscribe(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}
func recv[T any](t testing.TB, stream *Stream[T]) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func shutdown[T any](t testing.TB, hub *ModelHub[T]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := hub.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func waitID(t testing.TB, values <-chan golem.EventID) {
	t.Helper()
	select {
	case <-values:
	case <-time.After(time.Second):
		t.Fatal("evaluation did not complete")
	}
}
func code(t testing.TB, err error) events.ErrorCode {
	t.Helper()
	value, ok := events.CodeOf(err)
	if !ok {
		t.Fatalf("error = %v", err)
	}
	return value
}

func identityEvaluator(_ context.Context, notice events.Notice, _ SubscriberKey) (Evaluation[golem.EventID], error) {
	return Deliver(notice.EventID()), nil
}

func testNotice(t testing.TB, value byte) events.Notice {
	t.Helper()
	notice, err := eventvalue.NewNotice(golem.EventID{value}, golem.SchemaDigest{1}, golem.ModelID{2}, golem.EventCreated, golem.CausationID{9}, uint32(value), []byte{value})
	if err != nil {
		t.Fatal(err)
	}
	return notice
}

type sourceResult struct {
	notice events.Notice
	err    error
}
type fakeSource struct {
	results chan sourceResult
	opened  chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newFakeSource() *fakeSource {
	return &fakeSource{results: make(chan sourceResult, 16), opened: make(chan struct{}), closed: make(chan struct{})}
}
func (source *fakeSource) Recv(ctx context.Context) (events.Notice, error) {
	select {
	case <-ctx.Done():
		return events.Notice{}, ctx.Err()
	case <-source.closed:
		return events.Notice{}, events.Failure(events.CodeEventSourceClosed)
	case result := <-source.results:
		return result.notice, result.err
	}
}
func (source *fakeSource) Close() error              { source.once.Do(func() { close(source.closed) }); return nil }
func (source *fakeSource) send(notice events.Notice) { source.results <- sourceResult{notice: notice} }
func (source *fakeSource) fail(err error)            { source.results <- sourceResult{err: err} }
func sourceFactory(source *fakeSource) SourceFactory { return sequentialFactory(source) }
func sequentialFactory(sources ...*fakeSource) SourceFactory {
	var mu sync.Mutex
	index := 0
	return func(context.Context, events.Subscription) (events.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(sources) {
			return nil, events.Failure(events.CodeEventSourceClosed)
		}
		source := sources[index]
		index++
		select {
		case <-source.opened:
		default:
			close(source.opened)
		}
		return source, nil
	}
}

type panicObserver struct{}

func (panicObserver) ObserveEvent(context.Context, events.Observation) {
	panic("observer must be isolated")
}
