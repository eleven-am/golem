package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/queue"
	"github.com/jmoiron/sqlx"
)

type hookEnqueuePayload struct {
	Title string `json:"title"`
}

type hookEnqueueFixture struct {
	mutationVocabularyFixture
	jobType queue.Type[hookEnqueuePayload]
	handled chan int
	hook    func(context.Context, golem.HookExecutor) error
	onPost  func(context.Context, golem.HookExecutor) error
}

func newHookEnqueueJobType(t *testing.T, handle func(context.Context, queue.Job[hookEnqueuePayload]) error) (*queue.Registry, queue.Type[hookEnqueuePayload]) {
	t.Helper()
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[hookEnqueuePayload]{Type: "hook.enqueue", Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	return registry, jobType
}

func openHookEnqueueFixture(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture, withQueue bool, limits queue.Limits) *hookEnqueueFixture {
	t.Helper()
	ctx := context.Background()
	state := &hookEnqueueFixture{handled: make(chan int, 4)}
	var registry *queue.Registry
	registry, state.jobType = newHookEnqueueJobType(t, func(ctx context.Context, _ queue.Job[hookEnqueuePayload]) error {
		state.handled <- countPosts(t, state.mutationResultFixture, 121)
		return nil
	})
	state.mutationVocabularyFixture = openMutationVocabularyFixtureConfigured(t, database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		if withQueue {
			config.Queue = &QueueConfig{Registry: registry, Limits: limits}
		}
	})
	user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, state.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, state.userName, "alice"),
	)
	if _, err := SystemCreate(ctx, state.app.System(), state.userDescriptor, user); err != nil {
		t.Fatal(err)
	}
	run := func(hookContext context.Context, executor golem.HookExecutor) error {
		if state.hook == nil {
			return nil
		}
		return state.hook(hookContext, executor)
	}
	runPost := func(hookContext context.Context, executor golem.HookExecutor) error {
		if state.onPost == nil {
			return nil
		}
		return state.onPost(hookContext, executor)
	}
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		return rules.Freeze(schema.Post)
	})
	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](schema.User, golem.HookUpdate, func(hookContext context.Context, result golem.UpdateHookResult[mutationResultUser]) error {
			return run(hookContext, result.Executor())
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(hookContext context.Context, result golem.UpdateManyHookResult[mutationResultPost]) error {
			return run(hookContext, result.Executor())
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			return runPost(hookContext, result.Executor())
		}),
	}
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	state.app.bindings = bindings
	return state
}

func (fixture *hookEnqueueFixture) pending(t *testing.T, title string, options ...queue.Option) queue.Pending {
	t.Helper()
	pending, err := fixture.jobType.New(hookEnqueuePayload{Title: title}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func (fixture *hookEnqueueFixture) trigger(t *testing.T) error {
	t.Helper()
	return triggerVocabularyHookExecutor(t, fixture.mutationVocabularyFixture)
}

func (fixture *hookEnqueueFixture) requireNoJob(t *testing.T, id queue.JobID) {
	t.Helper()
	_, err := fixture.app.QueueOperator().Inspect(context.Background(), id)
	if code, ok := queue.CodeOf(err); !ok || code != queue.CodeJobNotFound {
		t.Fatalf("job %q survived: %v", id, err)
	}
}

func (fixture *hookEnqueueFixture) requirePending(t *testing.T, id queue.JobID) queue.Status {
	t.Helper()
	status, err := fixture.app.QueueOperator().Inspect(context.Background(), id)
	if err != nil || status.State != queue.StatePending || status.Type != "hook.enqueue" {
		t.Fatalf("job %q status=%#v err=%v", id, status, err)
	}
	return status
}

func forEachHookEnqueueProvider(t *testing.T, withQueue bool, limits queue.Limits, run func(*testing.T, *hookEnqueueFixture)) {
	t.Helper()
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		run(t, openHookEnqueueFixture(t, database, provider, schema, withQueue, limits))
	})
}

func hookCreatePost(t *testing.T, fixture *hookEnqueueFixture, hookContext context.Context, executor golem.HookExecutor) {
	t.Helper()
	author := golem.UUID{15: 1}
	if _, err := golem.HookCreateRow(hookContext, executor, fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 121, &author, "from-hook")); err != nil {
		t.Fatal(err)
	}
}

func TestHookEnqueueCommitsTheJobWithTheHookWrite(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			var err error
			id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "draft"))
			return err
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		fixture.requirePending(t, id)
		if count := countPosts(t, fixture.mutationResultFixture, 121); count != 1 {
			t.Fatalf("committed hook write rows=%d", count)
		}
	})
}

func TestHookEnqueueRollsBackWithAFailingHook(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		refusal := errors.New("hook refused after enqueue")
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			var err error
			if id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "draft")); err != nil {
				return err
			}
			return refusal
		}
		if err := fixture.trigger(t); !errors.Is(err, refusal) {
			t.Fatalf("trigger reported %v", err)
		}
		if id == "" {
			t.Fatal("hook never enqueued")
		}
		fixture.requireNoJob(t, id)
		if count := countPosts(t, fixture.mutationResultFixture, 121); count != 0 {
			t.Fatalf("rolled-back hook write rows=%d", count)
		}
	})
}

func TestHookEnqueueOversizePayloadCanBeHandledByTheHook(t *testing.T) {
	limits := queue.DefaultLimits()
	limits.MaxPayloadBytes = 64
	forEachHookEnqueueProvider(t, true, limits, func(t *testing.T, fixture *hookEnqueueFixture) {
		var enqueueErr error
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			_, enqueueErr = golem.HookEnqueue(hookContext, executor, fixture.pending(t, strings.Repeat("x", 200)))
			return nil
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		if code, ok := queue.CodeOf(enqueueErr); !ok || code != queue.CodePayloadInvalid {
			t.Fatalf("oversize enqueue reported %v", enqueueErr)
		}
		if count := countPosts(t, fixture.mutationResultFixture, 121); count != 1 {
			t.Fatalf("swallowed enqueue failure left %d post rows", count)
		}
		page, err := fixture.app.QueueOperator().List(context.Background(), queue.JobQuery{Limit: 10})
		if err != nil || len(page.Jobs) != 0 {
			t.Fatalf("oversize enqueue left jobs=%#v err=%v", page, err)
		}
	})
}

func TestHookEnqueueDedupeCoalescesWithAnExistingJob(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		existing, err := fixture.app.Enqueue(context.Background(), fixture.pending(t, "first", queue.Dedupe("same")))
		if err != nil {
			t.Fatal(err)
		}
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			var err error
			id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "second", queue.Dedupe("same")))
			return err
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		if id != existing {
			t.Fatalf("deduped hook enqueue returned %q, want %q", id, existing)
		}
	})
}

func TestHookEnqueueAfterSetsTheAvailableTime(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			var err error
			id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "later", queue.After(time.Hour)))
			return err
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		if status := fixture.requirePending(t, id); status.AvailableAt.Before(time.Now().Add(50 * time.Minute)) {
			t.Fatalf("available at %v is not delayed", status.AvailableAt)
		}
	})
}

func TestHookEnqueueWithoutAQueueIsConfigInvalidAndRollsBack(t *testing.T) {
	forEachHookEnqueueProvider(t, false, queue.Limits{}, func(t *testing.T, fixture *hookEnqueueFixture) {
		var enqueueErr error
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			_, enqueueErr = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "draft"))
			return enqueueErr
		}
		if err := fixture.trigger(t); err == nil {
			t.Fatal("hook enqueue without a queue committed")
		}
		if code, ok := queue.CodeOf(enqueueErr); !ok || code != queue.CodeConfigInvalid {
			t.Fatalf("enqueue without a queue reported %v", enqueueErr)
		}
		if count := countPosts(t, fixture.mutationResultFixture, 121); count != 0 {
			t.Fatalf("refused enqueue left %d post rows", count)
		}
	})
}

func TestHookEnqueueIsRefusedAfterTheHookReturns(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		var kept golem.HookExecutor
		fixture.hook = func(_ context.Context, executor golem.HookExecutor) error {
			kept = executor
			return nil
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		if _, err := golem.HookEnqueue(context.Background(), kept, fixture.pending(t, "late")); !errors.Is(err, errHookExecutorExpired) {
			t.Fatalf("expired executor enqueue reported %v", err)
		}
	})
}

func TestHookEnqueueInsideABatchHook(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx := context.Background()
		author := golem.UUID{15: 1}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 10, &author, "seed")); err != nil {
			t.Fatal(err)
		}
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			var err error
			id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "batch"))
			return err
		}
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), fixture.updateManyTitle("batched")); err != nil {
			t.Fatal(errorChain(err))
		}
		fixture.requirePending(t, id)
	})
}

func TestHookEnqueueInsideANestedCreateHook(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx := context.Background()
		var ids []queue.JobID
		fixture.onPost = func(hookContext context.Context, executor golem.HookExecutor) error {
			id, err := golem.HookEnqueue(hookContext, executor, fixture.pending(t, "nested"))
			ids = append(ids, id)
			return err
		}
		schema := fixture.schema
		child := systemAuthorPost(fixture.mutationVocabularyFixture, 20, nil, "nested-child")
		target := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey,
			golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 1}))
		update := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedSetFieldValue(schema.User, fixture.userName, "nested-trigger"),
			golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
		)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, update); err != nil {
			t.Fatal(errorChain(err))
		}
		if len(ids) != 1 {
			t.Fatalf("nested create ran %d enqueueing hooks, want 1", len(ids))
		}
		for _, id := range ids {
			fixture.requirePending(t, id)
		}
	})
}

func TestHookEnqueueWakesTheWorkerOnlyAfterCommit(t *testing.T) {
	limits := queue.DefaultLimits()
	limits.PollInterval = time.Minute
	forEachHookEnqueueProvider(t, true, limits, func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		running := make(chan error, 1)
		go func() { running <- fixture.app.RunQueueWorker(ctx) }()
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			_, err := golem.HookEnqueue(hookContext, executor, fixture.pending(t, "wake"))
			return err
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(err)
		}
		select {
		case rows := <-fixture.handled:
			if rows != 1 {
				t.Fatalf("handler ran before the hook's write was committed: rows=%d", rows)
			}
		case <-ctx.Done():
			t.Fatal("the worker never claimed the committed job")
		}
		cancel()
		<-running
	})
}

func TestHookEnqueueInsideABatchHookRollsBackWithTheBatch(t *testing.T) {
	forEachHookEnqueueProvider(t, true, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx := context.Background()
		author := golem.UUID{15: 1}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 10, &author, "seed")); err != nil {
			t.Fatal(err)
		}
		refusal := errors.New("batch hook refused after enqueue")
		var id queue.JobID
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			var err error
			if id, err = golem.HookEnqueue(hookContext, executor, fixture.pending(t, "batch")); err != nil {
				return err
			}
			return refusal
		}
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), fixture.updateManyTitle("batched")); !errors.Is(err, refusal) {
			t.Fatalf("batch reported %v", err)
		}
		if id == "" {
			t.Fatal("hook never enqueued")
		}
		fixture.requireNoJob(t, id)
	})
}

func requireWorkerClaimsCommittedJob(t *testing.T, fixture *hookEnqueueFixture, trigger func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	running := make(chan error, 1)
	go func() { running <- fixture.app.RunQueueWorker(ctx) }()
	trigger(ctx)
	select {
	case <-fixture.handled:
	case <-ctx.Done():
		t.Fatal("the worker never claimed the committed job")
	}
	cancel()
	<-running
}

func TestHookEnqueueInsideABatchHookWakesTheWorkerAfterCommit(t *testing.T) {
	limits := queue.DefaultLimits()
	limits.PollInterval = time.Minute
	forEachHookEnqueueProvider(t, true, limits, func(t *testing.T, fixture *hookEnqueueFixture) {
		author := golem.UUID{15: 1}
		if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 10, &author, "seed")); err != nil {
			t.Fatal(err)
		}
		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			_, err := golem.HookEnqueue(hookContext, executor, fixture.pending(t, "batch-wake"))
			return err
		}
		requireWorkerClaimsCommittedJob(t, fixture, func(ctx context.Context) {
			caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
			if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), fixture.updateManyTitle("batched")); err != nil {
				t.Error(errorChain(err))
			}
		})
	})
}

func TestHookEnqueueInsideANestedCreateHookWakesTheWorkerAfterCommit(t *testing.T) {
	limits := queue.DefaultLimits()
	limits.PollInterval = time.Minute
	forEachHookEnqueueProvider(t, true, limits, func(t *testing.T, fixture *hookEnqueueFixture) {
		fixture.onPost = func(hookContext context.Context, executor golem.HookExecutor) error {
			_, err := golem.HookEnqueue(hookContext, executor, fixture.pending(t, "nested-wake"))
			return err
		}
		schema := fixture.schema
		child := systemAuthorPost(fixture.mutationVocabularyFixture, 20, nil, "nested-child")
		target := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey,
			golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 1}))
		update := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedSetFieldValue(schema.User, fixture.userName, "nested-trigger"),
			golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
		)
		requireWorkerClaimsCommittedJob(t, fixture, func(ctx context.Context) {
			caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
			if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, update); err != nil {
				t.Error(errorChain(err))
			}
		})
	})
}
