package worker

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
	"github.com/jmoiron/sqlx"
)

func noopDefinition(name string, maxConcurrent int) queue.Definition[gatePayload] {
	return queue.Definition[gatePayload]{
		Type: name, MaxConcurrent: maxConcurrent,
		Handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
	}
}

func planWorker(t *testing.T, store queueprovider.Store, limits queue.Limits, provider golem.Provider, observer observe.Observer, resources ...queue.Resource) *Worker {
	t.Helper()
	registry := queue.NewRegistry()
	register(t, registry, noopDefinition("gate.plan.capped", 1))
	register(t, registry, noopDefinition("gate.plan.pooled", 0))
	worker, err := New(store, registry, limits, provider, observer, nil, resources...)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func startWorker(t *testing.T, worker *Worker) (*Worker, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("worker ended with %v", err)
		}
	}
	t.Cleanup(stop)
	return worker, stop
}

func claimRecorder(fixture *harness) (stubStore, *[]queueprovider.ClaimOptions) {
	claims := &[]queueprovider.ClaimOptions{}
	return stubStore{Store: fixture.store, claim: func(_ context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
		*claims = append(*claims, options)
		return nil, nil
	}}, claims
}

func dispatchOnce(t *testing.T, worker *Worker) {
	t.Helper()
	var handlers sync.WaitGroup
	if _, err := worker.dispatch(context.Background(), context.Background(), nil, &handlers); err != nil {
		t.Fatal(err)
	}
}

func claimFor(claims []queueprovider.ClaimOptions, jobType string) (queueprovider.ClaimOptions, bool) {
	for _, claim := range claims {
		for _, name := range claim.Types {
			if name == jobType {
				return claim, true
			}
		}
	}
	return queueprovider.ClaimOptions{}, false
}

func TestPlanStartsAtGenerationOneWithOpenValues(t *testing.T) {
	fixture := newHarness(t)
	worker := planWorker(t, fixture.store, gateLimits(), "", nil, queue.Resource{Name: "gate.shared", Concurrency: 3, Costs: map[string]int{"gate.plan.pooled": 2}})
	status := worker.Plan()
	want := queue.PlanStatus{
		Generation: 1, Concurrency: 4, ClaimBatch: 8,
		MaxConcurrent: map[string]int{"gate.plan.capped": 1, "gate.plan.pooled": 0},
		Capacities:    map[string]int{"gate.shared": 3},
		AppliedAt:     status.AppliedAt,
	}
	if !reflect.DeepEqual(status, want) || status.AppliedAt.IsZero() {
		t.Fatalf("initial plan = %#v", status)
	}
	status.MaxConcurrent["gate.plan.capped"] = 99
	if worker.Plan().MaxConcurrent["gate.plan.capped"] != 1 {
		t.Fatal("a returned plan aliases the active one")
	}
}

func TestPlanRefusalsLeaveTheActivePlan(t *testing.T) {
	fixture := newHarness(t)
	worker := planWorker(t, fixture.store, gateLimits(), "", nil, queue.Resource{Name: "gate.shared", Concurrency: 3, Costs: map[string]int{"gate.plan.pooled": 2}})
	before := worker.Plan()
	rows := []struct {
		name string
		plan queue.Plan
	}{
		{"negative concurrency", queue.Plan{Concurrency: -1}},
		{"negative claim batch", queue.Plan{ClaimBatch: -1}},
		{"claim batch above the maximum", queue.Plan{ClaimBatch: queueprovider.MaximumClaimJobs + 1}},
		{"unregistered type", queue.Plan{MaxConcurrent: map[string]int{"gate.plan.absent": 2}}},
		{"negative type cap", queue.Plan{MaxConcurrent: map[string]int{"gate.plan.capped": -1}}},
		{"unknown resource", queue.Plan{Capacities: map[string]int{"gate.absent": 4}}},
		{"zero capacity", queue.Plan{Capacities: map[string]int{"gate.shared": 0}}},
		{"negative capacity", queue.Plan{Capacities: map[string]int{"gate.shared": -3}}},
		{"capacity below the largest cost", queue.Plan{Capacities: map[string]int{"gate.shared": 1}}},
		{"valid part beside an invalid part", queue.Plan{Concurrency: 9, Capacities: map[string]int{"gate.shared": 1}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			status, err := worker.Apply(context.Background(), row.plan)
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("refusal reported %v", err)
			}
			if !reflect.DeepEqual(status, queue.PlanStatus{}) {
				t.Fatalf("refused plan returned status %#v", status)
			}
			if after := worker.Plan(); !reflect.DeepEqual(after, before) {
				t.Fatalf("refused plan changed the active plan: %#v", after)
			}
		})
	}
}

func TestPlanOverridesReachTheNextClaim(t *testing.T) {
	fixture := newHarness(t)
	store, claims := claimRecorder(fixture)
	worker := planWorker(t, store, gateLimits(), "", nil, queue.Resource{Name: "gate.shared", Concurrency: 3, Costs: map[string]int{"gate.plan.pooled": 2}})

	dispatchOnce(t, worker)
	capped, _ := claimFor(*claims, "gate.plan.capped")
	pooled, _ := claimFor(*claims, "gate.plan.pooled")
	if capped.Limit != 1 || pooled.Limit != 4 || pooled.Resource.Concurrency != 3 {
		t.Fatalf("initial claims capped=%#v pooled=%#v", capped, pooled)
	}

	*claims = nil
	status, err := worker.Apply(context.Background(), queue.Plan{
		ClaimBatch: 2, MaxConcurrent: map[string]int{"gate.plan.capped": 3}, Capacities: map[string]int{"gate.shared": 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != 2 || status.ClaimBatch != 2 || status.MaxConcurrent["gate.plan.capped"] != 3 || status.Capacities["gate.shared"] != 6 || status.Concurrency != 4 {
		t.Fatalf("applied plan = %#v", status)
	}
	dispatchOnce(t, worker)
	capped, _ = claimFor(*claims, "gate.plan.capped")
	pooled, _ = claimFor(*claims, "gate.plan.pooled")
	if capped.Limit != 2 || pooled.Limit != 2 || pooled.Resource.Concurrency != 6 || pooled.Resource.Costs["gate.plan.pooled"] != 2 {
		t.Fatalf("reconfigured claims capped=%#v pooled=%#v", capped, pooled)
	}

	*claims = nil
	if _, err := worker.Apply(context.Background(), queue.Plan{MaxConcurrent: map[string]int{"gate.plan.capped": 0}, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	dispatchOnce(t, worker)
	capped, _ = claimFor(*claims, "gate.plan.capped")
	pooled, _ = claimFor(*claims, "gate.plan.pooled")
	if capped.Limit != 1 || pooled.Limit != 1 || pooled.Resource.Concurrency != 3 {
		t.Fatalf("a plan restates every field; absent ones return to the Open value: capped=%#v pooled=%#v", capped, pooled)
	}
	if got := worker.Plan(); got.Generation != 3 || got.MaxConcurrent["gate.plan.capped"] != 0 || got.Concurrency != 1 || got.ClaimBatch != 8 {
		t.Fatalf("plan after the uncapped restatement = %#v", got)
	}
}

func TestPlanApplyWaitsForTheDispatchRoundInFlight(t *testing.T) {
	fixture := newHarness(t)
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var limits []int
	var mutex sync.Mutex
	store := stubStore{Store: fixture.store, claim: func(_ context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
		mutex.Lock()
		limits = append(limits, options.Limit)
		first := len(limits) == 1
		mutex.Unlock()
		if first {
			close(entered)
			<-proceed
		}
		return nil, nil
	}}
	registry := queue.NewRegistry()
	register(t, registry, noopDefinition("gate.plan.single", 0))
	worker, err := New(store, registry, gateLimits(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rounds := make(chan struct{})
	go func() {
		var handlers sync.WaitGroup
		_, _ = worker.dispatch(context.Background(), context.Background(), nil, &handlers)
		close(rounds)
	}()
	awaitSignal(t, entered, "the first claim to start")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := worker.Apply(canceled, queue.Plan{ClaimBatch: 2}); err != context.Canceled {
		t.Fatalf("Apply during a claim round reported %v", err)
	}
	if worker.Plan().Generation != 1 {
		t.Fatal("an Apply that could not take the dispatch lock changed the plan")
	}

	close(proceed)
	awaitSignal(t, rounds, "the in-flight round to finish")
	if _, err := worker.Apply(context.Background(), queue.Plan{ClaimBatch: 2}); err != nil {
		t.Fatal(err)
	}
	dispatchOnce(t, worker)
	mutex.Lock()
	defer mutex.Unlock()
	if len(limits) != 2 || limits[0] != 4 || limits[1] != 2 {
		t.Fatalf("claim limits across the swap = %v", limits)
	}
}

func TestPlanLoweringNeverCancelsRunningHandlers(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	admitted := make(chan struct{}, 8)
	release := make(chan struct{})
	var current, canceled atomic.Int64
	var mutex sync.Mutex
	var lateEntries []int64
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.plan.flow",
		Handle: func(ctx context.Context, job queue.Job[gatePayload]) error {
			entry := current.Add(1)
			defer current.Add(-1)
			if job.Payload.Value == "late" {
				mutex.Lock()
				lateEntries = append(lateEntries, entry)
				mutex.Unlock()
			}
			admitted <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				canceled.Add(1)
			}
			return nil
		},
	})
	pending := func(value string) queue.Pending {
		built, err := jobType.New(gatePayload{Value: value})
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	var early []string
	for range 3 {
		early = append(early, fixture.enqueue(t, pending("early")))
	}
	limits := gateLimits()
	limits.Concurrency = 1
	worker, stop := fixture.start(t, fixture.store, registry, limits)
	awaitSignal(t, admitted, "the first early job")

	if _, err := worker.Apply(context.Background(), queue.Plan{Concurrency: 3}); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, admitted, "the second early job")
	awaitSignal(t, admitted, "the third early job")

	if _, err := worker.Apply(context.Background(), queue.Plan{Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	var late []string
	for range 2 {
		late = append(late, fixture.enqueue(t, pending("late")))
	}
	for range early {
		release <- struct{}{}
	}
	for _, identity := range early {
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
	}
	awaitSignal(t, admitted, "the first late job")
	release <- struct{}{}
	awaitSignal(t, admitted, "the second late job")
	release <- struct{}{}
	for _, identity := range late {
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
	}
	stop()

	mutex.Lock()
	defer mutex.Unlock()
	if canceled.Load() != 0 {
		t.Fatalf("lowering the cap cancelled %d running handlers", canceled.Load())
	}
	if len(lateEntries) != 2 || lateEntries[0] != 1 || lateEntries[1] != 1 {
		t.Fatalf("jobs claimed after lowering started alongside others: %v", lateEntries)
	}
}

func TestPlanApplyWorksWithoutRun(t *testing.T) {
	fixture := newHarness(t)
	worker := planWorker(t, fixture.store, gateLimits(), "", nil)
	status, err := worker.Apply(context.Background(), queue.Plan{Concurrency: 2})
	if err != nil || status.Generation != 2 || status.Concurrency != 2 || !reflect.DeepEqual(worker.Plan(), status) {
		t.Fatalf("Apply without Run = %#v, %v", status, err)
	}
}

func TestPlanConcurrentApplyIsRaceFreeBesideRun(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	jobType := register(t, registry, noopDefinition("gate.plan.race", 2))
	var identities []string
	for range 24 {
		identities = append(identities, fixture.enqueue(t, newPending(t, jobType)))
	}
	worker, stop := fixture.start(t, fixture.store, registry, gateLimits())
	var applied atomic.Uint64
	var group sync.WaitGroup
	for caller := range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			for round := range 20 {
				_, err := worker.Apply(context.Background(), queue.Plan{
					Concurrency: 1 + (caller+round)%5, ClaimBatch: 1 + round%4,
					MaxConcurrent: map[string]int{"gate.plan.race": (caller + round) % 4},
				})
				if err != nil {
					t.Error(err)
					return
				}
				applied.Add(1)
				worker.Plan()
			}
		}()
	}
	group.Wait()
	for _, identity := range identities {
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
	}
	stop()
	if got := worker.Plan().Generation; got != 1+applied.Load() {
		t.Fatalf("generation %d after %d accepted plans", got, applied.Load())
	}
}

func TestPlanApplyIsObserved(t *testing.T) {
	fixture := newHarness(t)
	records := make(chan observe.Observation, 4)
	observer := queueObserverFunc(func(_ context.Context, value observe.Observation) {
		if value.Phase() == observe.PhaseApply {
			records <- value
		}
	})
	worker := planWorker(t, fixture.store, gateLimits(), golem.SQLite, observer)
	if _, err := worker.Apply(context.Background(), queue.Plan{Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Apply(context.Background(), queue.Plan{Concurrency: -1}); err == nil {
		t.Fatal("an invalid plan was accepted")
	}
	got := awaitObservations(t, records, 2, "the plan observations")
	if got[0].QueueType() != "plan" || got[0].Outcome() != observe.OutcomeSuccess || got[0].Provider() != golem.SQLite {
		t.Fatalf("accepted plan observation = %#v", got[0])
	}
	if got[1].QueueType() != "plan" || got[1].Outcome() != observe.OutcomeFailure || got[1].Reason() != observe.ReasonInvalidInput {
		t.Fatalf("refused plan observation = %#v", got[1])
	}
}

type planStoreCase struct {
	name  string
	store func(t *testing.T) queueprovider.Store
}

func planStoreCases() []planStoreCase {
	return []planStoreCase{
		{"sqlite", func(t *testing.T) queueprovider.Store { return newHarness(t).store }},
		{"postgresql", func(t *testing.T) queueprovider.Store {
			dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
			database, err := sqlx.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if _, err := database.Exec(`CREATE SCHEMA "queueplan"`); err != nil {
				t.Fatal(err)
			}
			store, err := postgresql.New().QueueStoreAt(database, physical.PhysicalName("queueplan"), physical.QueueUnmanagedObjects()[:4])
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureSchema(context.Background()); err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
}

func TestRaisingAResourceFromTwoToEightNeedsNoRestart(t *testing.T) {
	for _, row := range planStoreCases() {
		t.Run(row.name, func(t *testing.T) {
			fixture := &harness{store: row.store(t)}
			registry := queue.NewRegistry()
			admitted := make(chan struct{}, 16)
			release := make(chan struct{})
			var current, peak atomic.Int64
			jobType := register(t, registry, queue.Definition[gatePayload]{
				Type: "gate.analyze", MaxConcurrent: 2,
				Handle: func(context.Context, queue.Job[gatePayload]) error {
					now := current.Add(1)
					for {
						seen := peak.Load()
						if now <= seen || peak.CompareAndSwap(seen, now) {
							break
						}
					}
					admitted <- struct{}{}
					<-release
					current.Add(-1)
					return nil
				},
			})
			var identities []string
			for range 10 {
				identities = append(identities, fixture.enqueue(t, newPending(t, jobType)))
			}
			limits := gateLimits()
			limits.Concurrency = 16
			limits.ClaimBatch = 16
			worker, err := New(fixture.store, registry, limits, "", nil, nil, queue.Resource{Name: "gate.analyze_pool", Concurrency: 2, Costs: map[string]int{"gate.analyze": 1}})
			if err != nil {
				t.Fatal(err)
			}
			_, stop := startWorker(t, worker)

			awaitSignal(t, admitted, "the first analyze job")
			awaitSignal(t, admitted, "the second analyze job")
			leased := 0
			for _, identity := range identities {
				if fixture.inspect(t, identity).State == queueprovider.StateLeased {
					leased++
				}
			}
			if leased != 2 {
				t.Fatalf("%d analyze jobs were leased under a capacity of 2", leased)
			}

			status, err := worker.Apply(context.Background(), queue.Plan{
				MaxConcurrent: map[string]int{"gate.analyze": 8}, Capacities: map[string]int{"gate.analyze_pool": 8},
			})
			if err != nil || status.Capacities["gate.analyze_pool"] != 8 || status.MaxConcurrent["gate.analyze"] != 8 {
				t.Fatalf("raise reported %#v, %v", status, err)
			}
			release <- struct{}{}
			release <- struct{}{}
			for range 8 {
				awaitSignal(t, admitted, "an analyze job under the raised plan")
			}
			if current.Load() != 8 {
				t.Fatalf("%d analyze jobs ran at once after the raise", current.Load())
			}
			for range 8 {
				release <- struct{}{}
			}
			for _, identity := range identities {
				awaitState(t, fixture, identity, queueprovider.StateSucceeded)
			}
			stop()
			if peak.Load() != 8 {
				t.Fatalf("peak concurrency %d, want 8", peak.Load())
			}
		})
	}
}
