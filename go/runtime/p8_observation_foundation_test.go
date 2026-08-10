package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

type p8ObservationSnapshot struct {
	kind       observe.Kind
	phase      observe.Phase
	outcome    observe.Outcome
	reason     observe.Reason
	provider   golem.Provider
	model      golem.ModelID
	operation  observe.Operation
	statements int
	attempt    int
	depth      int
	limit      int
	aggregate  int64
}

func p8SnapshotObservation(value observe.Observation) p8ObservationSnapshot {
	return p8ObservationSnapshot{
		kind: value.Kind(), phase: value.Phase(), outcome: value.Outcome(), reason: value.Reason(),
		provider: value.Provider(), model: value.ModelID(), operation: value.Operation(),
		statements: value.StatementCount(), attempt: value.Attempt(), depth: value.QueueDepth(),
		limit: value.QueueLimit(), aggregate: value.AggregateCount(),
	}
}

type p8ObservationCollector struct {
	mu     sync.Mutex
	values []p8ObservationSnapshot
}

func (collector *p8ObservationCollector) ObserveGolem(_ context.Context, value observe.Observation) {
	collector.mu.Lock()
	collector.values = append(collector.values, p8SnapshotObservation(value))
	collector.mu.Unlock()
}

func (collector *p8ObservationCollector) matching(kind observe.Kind, operation observe.Operation) []p8ObservationSnapshot {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	var result []p8ObservationSnapshot
	for _, value := range collector.values {
		if value.kind == kind && value.operation == operation {
			result = append(result, value)
		}
	}
	return result
}

func TestP8ObservationPreflightRefusalIsVisibleAndExecutesZeroSQL(t *testing.T) {
	fixture := newMutationResultFixture(t)
	collector := &p8ObservationCollector{}
	fixture.app.observer = collector
	caller, err := fixture.app.ForPrincipal(context.Background(), mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerFindMany(context.Background(), caller, fixture.postDescriptor, golem.Take[mutationResultPost](-1)); err == nil {
		t.Fatal("negative take was accepted")
	}
	values := collector.matching(observe.KindRead, observe.OperationReadFindMany)
	if len(values) != 1 {
		t.Fatalf("read observations=%v", values)
	}
	want := p8ObservationSnapshot{
		kind: observe.KindRead, phase: observe.PhaseFinish, outcome: observe.OutcomeRefused,
		reason: observe.ReasonInvalidInput, provider: golem.SQLite, model: fixture.schema.Post,
		operation: observe.OperationReadFindMany,
	}
	if values[0] != want {
		t.Fatalf("read refusal=%+v want=%+v", values[0], want)
	}
}

type p8BlockingObserver struct {
	kind      observe.Kind
	operation observe.Operation
	entered   chan p8ObservationSnapshot
	release   chan struct{}
	once      sync.Once
}

func (observer *p8BlockingObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Kind() != observer.kind || value.Operation() != observer.operation {
		return
	}
	observer.once.Do(func() {
		observer.entered <- p8SnapshotObservation(value)
		<-observer.release
	})
}

func p8AssertPoolReleasedWhileObserverBlocked[P, A any](t *testing.T, app *App[P, A], entered <-chan p8ObservationSnapshot) p8ObservationSnapshot {
	t.Helper()
	select {
	case value := <-entered:
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		connection, err := app.database.Connx(ctx)
		if err != nil {
			t.Fatalf("observer ran while the only database connection remained held: %v", err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		if inUse := app.database.Stats().InUse; inUse != 0 {
			t.Fatalf("connections in use while observer is blocked=%d", inUse)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("observer was not invoked")
		return p8ObservationSnapshot{}
	}
}

func TestP8StandaloneMutationDeliversObserverOnlyAfterConnectionRelease(t *testing.T) {
	fixture := newMutationResultFixture(t)
	observer := &p8BlockingObserver{
		kind: observe.KindMutation, operation: observe.OperationMutationCreate,
		entered: make(chan p8ObservationSnapshot, 1), release: make(chan struct{}),
	}
	fixture.app.observer = observer
	done := make(chan error, 1)
	go func() {
		_, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(81, golem.UUID{15: 1}, "observed"))
		done <- err
	}()
	value := p8AssertPoolReleasedWhileObserverBlocked(t, fixture.app, observer.entered)
	if value.outcome != observe.OutcomeSuccess || value.reason != observe.ReasonNone || value.statements != 3 {
		t.Fatalf("mutation observation=%+v", value)
	}
	close(observer.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mutation did not return after observer release")
	}
}

func TestP8CallerTransactionAggregatesChildStatementsAndReleasesConnectionBeforeDelivery(t *testing.T) {
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &p8BlockingObserver{
		kind: observe.KindRead, operation: observe.OperationReadFindMany,
		entered: make(chan p8ObservationSnapshot, 1), release: make(chan struct{}),
	}
	fixture.app.observer = observer
	done := make(chan error, 1)
	go func() {
		done <- CallerTransaction(context.Background(), caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			_, err := CallerTxFindMany(context.Background(), transaction, fixture.postDescriptor, golem.Take[mutationResultPost](1))
			return err
		})
	}()
	child := p8AssertPoolReleasedWhileObserverBlocked(t, fixture.app, observer.entered)
	if child.outcome != observe.OutcomeSuccess || child.statements != 1 {
		t.Fatalf("transaction child observation=%+v", child)
	}
	close(observer.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("transaction did not return after observer release")
	}
	// Capture the transaction record independently because the blocking observer
	// deliberately only intercepts the child read operation.
	collector := &p8ObservationCollector{}
	fixture.app.observer = collector
	if err := CallerTransaction(context.Background(), caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxFindMany(context.Background(), transaction, fixture.postDescriptor, golem.Take[mutationResultPost](1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reads := collector.matching(observe.KindRead, observe.OperationReadFindMany)
	transactions := collector.matching(observe.KindTransaction, observe.OperationCallerTransaction)
	if len(reads) != 1 || len(transactions) != 1 || reads[0].statements != 1 || transactions[0].statements != reads[0].statements {
		t.Fatalf("read=%v transaction=%v", reads, transactions)
	}
}

func TestP8LegacyEventObserverAdaptsIntoUnifiedClosedRecord(t *testing.T) {
	collector := &p8ObservationCollector{}
	model := golem.ModelID{15: 91}
	events.Observe(
		adaptEventObserver(collector, golem.PostgreSQL), context.Background(), model, golem.EventUpdated,
		events.ObservationSuppression, events.OutcomeSuppressed, events.SuppressionUnauthorized,
		2, 3, 7, 4*time.Millisecond, 11,
	)
	values := collector.matching(observe.KindSubscription, observe.OperationSubscriptionSuppression)
	if len(values) != 1 {
		t.Fatalf("unified event observations=%v", values)
	}
	want := p8ObservationSnapshot{
		kind: observe.KindSubscription, phase: observe.PhaseSuppress, outcome: observe.OutcomeSuppressed,
		reason: observe.ReasonAuthorization, provider: golem.PostgreSQL, model: model,
		operation: observe.OperationSubscriptionSuppression, attempt: 2, depth: 3, limit: 7, aggregate: 11,
	}
	if values[0] != want {
		t.Fatalf("adapted observation=%+v want=%+v", values[0], want)
	}
}

func TestP8ObservationCoverageMutationHookAndSystemTransactionEdges(t *testing.T) {
	hookFactory := func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookRequest[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(_ context.Context, request *golem.UpdateManyHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, title, "coverage-delete")))
				return nil
			}),
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookRequest[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(_ context.Context, request *golem.DeleteManyHookRequest[mutationResultPost]) error {
				request.ReplaceWhere(request.Where())
				return nil
			}),
		}
	}
	run := func(t *testing.T, fixture mutationResultFixture) {
		collector := &p8ObservationCollector{}
		fixture.app.observer = collector
		ctx := context.Background()
		seed := func(ids ...byte) {
			for _, id := range ids {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "coverage-edge")); err != nil {
					t.Fatal(err)
				}
			}
		}
		seed(241, 242)
		caller := mustMutationResultCaller(t, fixture)
		if count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 241}, golem.UUID{15: 242}), fixture.updateManyTitle("ignored")); err != nil || count != 2 {
			t.Fatalf("updateMany count=%d err=%v", count, err)
		}
		if count, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 241}, golem.UUID{15: 242})); err != nil || count != 2 {
			t.Fatalf("deleteMany count=%d err=%v", count, err)
		}
		p8AssertBatchRootAndHooks(t, collector, false)
		p8AppendDynamicCoverage(t, collector.values)

		seed(243, 244)
		collector.mu.Lock()
		collector.values = nil
		collector.mu.Unlock()
		if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if count, err := CallerTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 243}, golem.UUID{15: 244}), fixture.updateManyTitle("ignored")); err != nil || count != 2 {
				return fmt.Errorf("CallerTx updateMany count=%d: %w", count, err)
			}
			if count, err := CallerTxDeleteMany(ctx, transaction, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 243}, golem.UUID{15: 244})); err != nil || count != 2 {
				return fmt.Errorf("CallerTx deleteMany count=%d: %w", count, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		p8AssertBatchRootAndHooks(t, collector, true)
		p8AppendDynamicCoverage(t, collector.values)

		seed(245, 246)
		collector.mu.Lock()
		collector.values = nil
		collector.mu.Unlock()
		execution, err := NewCallerMutationExecution(caller, CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor))
		if err != nil {
			t.Fatal(err)
		}
		where, err := fixture.postID.In(golem.UUID{15: 245}, golem.UUID{15: 246}).Freeze(fixture.postDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		input, err := golem.RuntimeFreezeUpdateManyInput(fixture.updateManyTitle("ignored"))
		if err != nil {
			t.Fatal(err)
		}
		updateRequest, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationUpdateMany, Model: fixture.schema.Post, Where: &where, Input: &input})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := execution.ExecuteFrozenMutation(ctx, updateRequest); err != nil {
			t.Fatal(err)
		} else if count, ok := result.Count(); !ok || count != 2 {
			t.Fatalf("frozen GraphQL updateMany count=%d/%t", count, ok)
		}
		deleteRequest, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationDeleteMany, Model: fixture.schema.Post, Where: &where})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := execution.ExecuteFrozenMutation(ctx, deleteRequest); err != nil {
			t.Fatal(err)
		} else if count, ok := result.Count(); !ok || count != 2 {
			t.Fatalf("frozen GraphQL deleteMany count=%d/%t", count, ok)
		}
		p8AssertBatchRootAndHooks(t, collector, false)
		p8AppendDynamicCoverage(t, collector.values)

		collector.mu.Lock()
		collector.values = nil
		collector.mu.Unlock()
		if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, golem.Predicate[mutationResultPost]{}, fixture.updateManyTitle("invalid-where")); err == nil {
			t.Fatal("empty update-many predicate preflight was accepted")
		}
		refusals := collector.matching(observe.KindMutation, observe.OperationMutationUpdateMany)
		if len(refusals) != 1 || refusals[0].outcome != observe.OutcomeRefused || refusals[0].statements != 0 {
			t.Fatalf("batch preflight observations=%v", refusals)
		}

		collector.mu.Lock()
		collector.values = nil
		collector.mu.Unlock()
		if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			_, err := SystemTxCount(ctx, transaction, fixture.postDescriptor)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(collector.matching(observe.KindTransaction, observe.OperationSystemTransaction)) != 1 {
			t.Fatalf("missing system transaction observation: %v", collector.values)
		}
		p8AppendDynamicCoverage(t, collector.values)
	}
	t.Run("sqlite", func(t *testing.T) {
		run(t, newMutationResultFixtureWithHooks(t, MutationLimits{}, hookFactory, func(context.Context, golem.AfterCommitFailure) {}))
	})
	for _, profile := range []struct{ name, namespace, environment string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := os.Getenv(profile.environment)
			if dsn == "" {
				t.Skip(profile.environment + " is required")
			}
			base := newMutationResultFixtureWithHooks(t, MutationLimits{}, hookFactory, func(context.Context, golem.AfterCommitFailure) {})
			fixture, _, _ := newPostgreSQLMutationOracleFixtureFromBase(t, dsn, profile.namespace, base)
			run(t, fixture)
		})
	}
}

func p8AssertBatchRootAndHooks(t *testing.T, collector *p8ObservationCollector, callerTransaction bool) {
	t.Helper()
	for _, expectation := range []struct {
		kind      observe.Kind
		operation observe.Operation
	}{{observe.KindMutation, observe.OperationMutationUpdateMany}, {observe.KindMutation, observe.OperationMutationDeleteMany}, {observe.KindHook, observe.OperationHookUpdateMany}, {observe.KindHook, observe.OperationHookDeleteMany}} {
		if values := collector.matching(expectation.kind, expectation.operation); len(values) != 1 {
			t.Fatalf("production observations %s/%s=%v; want exactly one", expectation.kind, expectation.operation, values)
		}
	}
	transactions := collector.matching(observe.KindTransaction, observe.OperationCallerTransaction)
	if callerTransaction && len(transactions) != 1 {
		t.Fatalf("caller transaction observations=%v; want one", transactions)
	}
	if !callerTransaction && len(transactions) != 0 {
		t.Fatalf("unexpected caller transaction observations=%v", transactions)
	}
}

func p8AppendDynamicCoverage(t *testing.T, values []p8ObservationSnapshot) {
	t.Helper()
	path := os.Getenv("P8_OBSERVATION_COVERAGE_FILE")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if _, err := fmt.Fprintln(file, value.provider, value.operation); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
