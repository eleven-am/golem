package loadconsumer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

const loadUserID = "b0000000-0000-0000-0000-000000000001"

type observed struct {
	Kind       observe.Kind
	Operation  observe.Operation
	Outcome    observe.Outcome
	Reason     observe.Reason
	Statements int
	Aggregate  int64
	Attempt    int
	QueueDepth int
	QueueLimit int
}

type trace struct {
	mu     sync.Mutex
	values []observed
}

func (trace *trace) ObserveGolem(_ context.Context, value observe.Observation) {
	trace.mu.Lock()
	trace.values = append(trace.values, observed{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Reason: value.Reason(),
		Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
		Attempt: value.Attempt(), QueueDepth: value.QueueDepth(), QueueLimit: value.QueueLimit(),
	})
	trace.mu.Unlock()
}

func (trace *trace) reset() {
	trace.mu.Lock()
	trace.values = nil
	trace.mu.Unlock()
}

func (trace *trace) snapshot() []observed {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]observed(nil), trace.values...)
}

type fixture struct {
	t           testing.TB
	ctx         context.Context
	database    *provider.Database
	app         *social.App[social.Principal]
	caller      *social.Caller[social.Principal]
	graph       *social.GraphQLServer
	handler     http.Handler
	trace       *trace
	userID      golem.UUID
	preSeedHeap uint64
}

func TestP8ExternalOracleScenario(t *testing.T) {
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "statement-connection":
		f := newFixture(t, 64)
		defer f.close()
		f.statementConnection()
	case "goroutine-queue-evaluation":
		f := newFixture(t, 128)
		defer f.close()
		f.goroutineQueueEvaluation()
	case "cardinality-ramp":
		f := newFixture(t, 128)
		defer f.close()
		f.cardinalityRamp()
	default:
		t.Fatalf("unknown load scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func BenchmarkP8ExternalReferenceApplication(b *testing.B) {
	f := newFixture(b, 128)
	defer f.close()
	pool := f.database.Pool()
	b.Logf("profile provider=%s provider_version=%s go=%s os=%s arch=%s cpu=%d dataset_posts=128 dataset_comments=128 max_open=%d max_idle=%d shapes=caller_relation,graphql_batched_computed,analytics_group,update_many concurrency=1,parallel",
		f.database.Provider(), benchmarkProviderVersion(b, f.database), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), pool.MaximumOpen(), pool.MaximumIdle())

	b.Run("caller-read-relation", func(b *testing.B) {
		projection := social.Posts.Select(
			social.Posts.ID, social.Posts.Title,
			social.Posts.Author.Select(social.Users.ID, social.Users.Handle),
			social.Posts.Comments.Count(),
		)
		b.ReportAllocs()
		b.ResetTimer()
		for iteration := 0; iteration < b.N; iteration++ {
			rows, err := f.caller.Posts.FindMany(f.ctx, social.Posts.Take(128), projection)
			if err != nil || len(rows) != 128 {
				b.Fatalf("rows=%d error=%v", len(rows), err)
			}
		}
	})
	b.Run("graphql-batched-computed", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for iteration := 0; iteration < b.N; iteration++ {
			response := f.graphQL(`query { posts(orderBy: [{id: asc}], take: 128) { id displayCode(prefix: "bench-") } }`)
			if len(response.Errors) != 0 {
				b.Fatalf("GraphQL errors=%v", response.Errors)
			}
		}
	})
	b.Run("analytics-two-groups", func(b *testing.B) {
		dimension, count := social.Posts.Published.Dimension(), social.Posts.CountAll()
		request := social.Posts.GroupBy(social.Posts.GroupDimensions(dimension), social.Posts.GroupMeasures(count), social.Posts.GroupTake(2))
		b.ReportAllocs()
		b.ResetTimer()
		for iteration := 0; iteration < b.N; iteration++ {
			groups, err := f.caller.Posts.GroupBy(f.ctx, request)
			if err != nil || len(groups) != 2 {
				b.Fatalf("groups=%d error=%v", len(groups), err)
			}
		}
	})
	b.Run("update-many-eight-identities", func(b *testing.B) {
		identities := make([]golem.UUID, 8)
		for index := range identities {
			identities[index] = loadPostID(b, index+1)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for iteration := 0; iteration < b.N; iteration++ {
			count, err := f.caller.Posts.UpdateMany(f.ctx, social.Posts.ID.In(identities...),
				social.Posts.UpdateMany(social.Posts.Priority.Set(int32(iteration%2))),
			)
			if err != nil || count != 8 {
				b.Fatalf("updated=%d error=%v", count, err)
			}
		}
	})
	b.Run("caller-read-parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(parallel *testing.PB) {
			for parallel.Next() {
				rows, err := f.caller.Posts.FindMany(f.ctx, social.Posts.Take(8), social.Posts.Select(social.Posts.ID, social.Posts.Title))
				if err != nil || len(rows) != 8 {
					b.Errorf("rows=%d error=%v", len(rows), err)
					return
				}
			}
		})
	})
}

func benchmarkProviderVersion(b *testing.B, database *provider.Database) string {
	b.Helper()
	var version string
	statement := "SELECT sqlite_version()"
	if database.Provider() == golem.PostgreSQL {
		statement = "SHOW server_version"
	}
	if err := database.UnsafeSQLX().GetContext(context.Background(), &version, statement); err != nil {
		b.Fatal(err)
	}
	return version
}

func newFixture(t testing.TB, posts int) *fixture {
	t.Helper()
	ctx := context.Background()
	database := openDatabase(t, ctx)
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
	if err != nil {
		t.Fatal(err)
	}
	observed := &trace{}
	app, err := social.Open(ctx, appConfig(database, transport, observed))
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	runtime.GC()
	var preSeed runtime.MemStats
	runtime.ReadMemStats(&preSeed)
	userID := mustUUID(t, loadUserID)
	if _, err := app.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("load-user"), social.Users.Email.Create("load@example.test"),
	)); err != nil {
		t.Fatal(err)
	}
	seedPosts(t, ctx, app.System(), userID, posts)
	principal := social.Principal{Development: true, DevUserID: userID}
	caller, err := app.ForPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	result := &fixture{t: t, ctx: ctx, database: database, app: app, caller: caller, trace: observed, userID: userID, preSeedHeap: preSeed.HeapAlloc}
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) { return principal, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("unexpected trusted GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result.graph, result.handler = graph, graph.Handler()
	observed.reset()
	return result
}

func appConfig(database *provider.Database, transport events.EventTransport, observer observe.Observer) social.Config[social.Principal] {
	return social.Config[social.Principal]{
		Database: database, EventTransport: transport, Observer: observer,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			return social.Actor{UserID: principal.DevUserID, Authenticated: principal.Development}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-load-principal" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	}
}

func (f *fixture) close() {
	if err := f.graph.Shutdown(context.Background()); err != nil {
		f.t.Error(err)
	}
	if stats := f.database.UnsafeSQLX().Stats(); stats.InUse != 0 {
		f.t.Errorf("connections still in use=%d", stats.InUse)
	}
	if err := f.database.Close(); err != nil {
		f.t.Error(err)
	}
}

func (f *fixture) statementConnection() {
	projection := social.Posts.Select(
		social.Posts.ID, social.Posts.Title,
		social.Posts.Author.Select(social.Users.ID, social.Users.Handle),
		social.Posts.Comments.Args(social.Comments.Select(social.Comments.ID, social.Comments.Body)),
		social.Posts.Comments.Count(),
	)
	for _, size := range []int{1, 8, 32, 64} {
		f.trace.reset()
		rows, err := f.caller.Posts.FindMany(f.ctx,
			social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(size), projection,
		)
		if err != nil || len(rows) != size {
			f.t.Fatalf("ramp=%d rows=%d error=%v", size, len(rows), err)
		}
		assertReadPlan(f.t, f.trace.snapshot(), size)
		assertPoolBound(f.t, f.database)
	}

	f.trace.reset()
	response := f.graphQL(`query { posts(orderBy: [{id: asc}], take: 64) { id title author { id handle } comments { id body } _count { comments } } }`)
	if len(response.Errors) != 0 {
		f.t.Fatalf("GraphQL errors=%v", response.Errors)
	}
	var data struct {
		Posts []json.RawMessage `json:"posts"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || len(data.Posts) != 64 {
		f.t.Fatalf("GraphQL rows=%d error=%v", len(data.Posts), err)
	}
	assertReadPlan(f.t, f.trace.snapshot(), 64)
	assertOne(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, 1)
	assertPoolBound(f.t, f.database)
	f.analyticsBounds()
	f.relationDepthBounds()
}

func (f *fixture) relationDepthBounds() {
	selection := "id"
	for depth := 0; depth < 40; depth++ {
		selection = "replies { " + selection + " }"
	}
	f.trace.reset()
	response := f.graphQL("query { comments(take: 1) { " + selection + " } }")
	if len(response.Errors) == 0 {
		f.t.Fatal("over-depth recursive relation selection was accepted")
	}
	assertRefused(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLRequest, 0)
	assertPoolBound(f.t, f.database)
}

func (f *fixture) analyticsBounds() {
	dimension, count := social.Posts.Published.Dimension(), social.Posts.CountAll()
	f.trace.reset()
	groups, err := f.caller.Posts.GroupBy(f.ctx, social.Posts.GroupBy(
		social.Posts.GroupDimensions(dimension), social.Posts.GroupMeasures(count),
		social.Posts.GroupOrderBy(dimension.Asc()), social.Posts.GroupTake(2),
	))
	if err != nil || len(groups) != 2 {
		f.t.Fatalf("group cardinality=%d error=%v", len(groups), err)
	}
	assertOne(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, 1)

	f.trace.reset()
	refused := f.graphQL(`query { groupByPosts(by: [published], take: 101) { key { published } count } }`)
	if len(refused.Errors) == 0 {
		f.t.Fatal("GraphQL maxGroups excess was accepted")
	}
	assertRefused(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, 0)
	assertPoolBound(f.t, f.database)
}

func assertReadPlan(t testing.TB, values []observed, parents int) {
	t.Helper()
	assertOne(t, values, observe.KindRead, observe.OperationReadFindMany, 1)
	relations, aggregate := 0, int64(0)
	for _, value := range values {
		if value.Kind == observe.KindRelationLoad && value.Operation == observe.OperationRelationLoad {
			if value.Statements != 0 {
				t.Fatalf("correlated relation emitted SQL statements=%d values=%v", value.Statements, values)
			}
			relations++
			aggregate += value.Aggregate
		}
	}
	if relations != 2 || aggregate != int64(2*parents) {
		t.Fatalf("relation plan records=%d aggregate=%d want records=2 aggregate=%d values=%v", relations, aggregate, 2*parents, values)
	}
}

func assertOne(t testing.TB, values []observed, kind observe.Kind, operation observe.Operation, statements int) {
	t.Helper()
	count := 0
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			count++
			if value.Outcome != observe.OutcomeSuccess || value.Statements != statements {
				t.Fatalf("observation=%v want success statements=%d", value, statements)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s/%s count=%d values=%v", kind, operation, count, values)
	}
}

func assertPoolBound(t testing.TB, database *provider.Database) {
	t.Helper()
	status, stats := database.Pool(), database.UnsafeSQLX().Stats()
	if stats.InUse != 0 || stats.OpenConnections > status.MaximumOpen() || stats.Idle > status.MaximumIdle() {
		t.Fatalf("pool stats=%+v configured open=%d idle=%d", stats, status.MaximumOpen(), status.MaximumIdle())
	}
}

type blockingObserver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type batchProbe struct {
	mu    sync.Mutex
	sizes []int
}

func (probe *batchProbe) ObservePostDisplayCodeLoad(_ context.Context, keys []golem.UUID, _ social.DisplayCodeArgs) error {
	probe.mu.Lock()
	probe.sizes = append(probe.sizes, len(keys))
	probe.mu.Unlock()
	return nil
}

func (probe *batchProbe) snapshot() []int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]int(nil), probe.sizes...)
}

func (observer *blockingObserver) ObserveGolem(context.Context, observe.Observation) {
	observer.once.Do(func() { close(observer.entered) })
	<-observer.release
}

func (f *fixture) goroutineQueueEvaluation() {
	baseline := runtime.NumGoroutine()
	var group sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 8; iteration++ {
				rows, err := f.caller.Posts.FindMany(f.ctx, social.Posts.Take(4), social.Posts.Select(social.Posts.ID))
				if err != nil || len(rows) != 4 {
					f.t.Errorf("concurrent read rows=%d error=%v", len(rows), err)
					return
				}
			}
		}()
	}
	group.Wait()
	assertPoolBound(f.t, f.database)
	f.computedBatchConcurrency()
	f.eventResourceBounds()
	f.trace.reset()
	posts := social.Posts.Scope()
	postID := social.Posts.ID.At(posts)
	if rows, err := f.caller.Posts.Scoped(f.ctx, golem.From(posts).Select(postID).Take(100_001)); err == nil || rows != nil {
		f.t.Fatalf("over-limit preflight rows=%d error=%v", len(rows), err)
	}
	assertRefused(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, 0)

	target := &blockingObserver{entered: make(chan struct{}), release: make(chan struct{})}
	dispatcher, err := observe.NewDispatcher(target, observe.DispatcherConfig{QueueCapacity: 2})
	if err != nil {
		f.t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		f.t.Fatal(err)
	}
	blockedApp, err := social.Open(f.ctx, appConfig(f.database, transport, dispatcher))
	if err != nil {
		f.t.Fatal(err)
	}
	select {
	case <-target.entered:
	case <-time.After(time.Second):
		f.t.Fatal("dispatcher worker did not enter target")
	}
	for iteration := 0; iteration < 32; iteration++ {
		if _, err := blockedApp.System().Posts.Count(f.ctx); err != nil {
			f.t.Fatal(err)
		}
	}
	if dispatcher.Dropped() == 0 {
		f.t.Fatal("bounded dispatcher did not drop a full-queue observation")
	}
	close(target.release)
	shutdownContext, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(shutdownContext); err != nil {
		f.t.Fatal(err)
	}
	before := dispatcher.Dropped()
	if _, err := blockedApp.System().Posts.Count(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if dispatcher.Dropped() != before+1 {
		f.t.Fatalf("closed dispatcher dropped=%d want=%d", dispatcher.Dropped(), before+1)
	}
	assertPoolBound(f.t, f.database)
	settleGoroutines()
	if after := runtime.NumGoroutine(); after > baseline+6 {
		f.t.Fatalf("goroutines baseline=%d after=%d", baseline, after)
	}
}

func (f *fixture) eventResourceBounds() {
	f.clearEventFacts()
	baseline := runtime.NumGoroutine()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
	if err != nil {
		f.t.Fatal(err)
	}
	eventTrace := &trace{}
	config := appConfig(f.database, transport, eventTrace)
	config.EventLimits = events.Limits{
		ClaimRows: 4, PublisherConcurrency: 2, SubscriberQueue: 1,
		HubInputQueue: 8, EvaluationConcurrency: 2,
	}
	eventApp, err := social.Open(f.ctx, config)
	if err != nil {
		f.t.Fatal(err)
	}
	caller, err := eventApp.ForPrincipal(f.ctx, social.Principal{Development: true, DevUserID: f.userID})
	if err != nil {
		f.t.Fatal(err)
	}
	stream, err := caller.Posts.Events(f.ctx,
		golem.EventWhere(social.Posts.Title.StartsWith("load-event-")),
		golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	defer stream.Close()
	waitForObservation(f.t, eventTrace, observe.OperationSubscriptionMembership, 1)
	publisherContext, cancelPublisher := context.WithCancel(f.ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- eventApp.RunEventPublisher(publisherContext) }()
	waitForPublisher(f.t, eventApp)
	for index := 0; index < 16; index++ {
		f.createLoadEventPost(eventApp.System(), index)
	}
	waitForDeliveredFacts(f.t, f.database, 16)
	waitForObservation(f.t, eventTrace, observe.OperationSubscriptionOverflow, 1)
	cancelPublisher()
	select {
	case err := <-publisherDone:
		if err != nil && publisherContext.Err() == nil {
			f.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("bounded publisher did not stop")
	}

	claims, attempts, acknowledgements, evaluations, overflows := 0, 0, 0, 0, 0
	for _, value := range eventTrace.snapshot() {
		if value.QueueLimit != 0 && value.QueueDepth > value.QueueLimit {
			f.t.Fatalf("queue depth=%d exceeds closed limit=%d observation=%v", value.QueueDepth, value.QueueLimit, value)
		}
		switch value.Operation {
		case observe.OperationEventPublisherClaim:
			claims++
			if value.Aggregate < 1 || value.Aggregate > 4 || value.Statements != 0 {
				f.t.Fatalf("claim observation=%v", value)
			}
		case observe.OperationEventPublisherAttempt:
			attempts++
		case observe.OperationEventPublisherAcknowledge:
			acknowledgements++
		case observe.OperationSubscriptionEvaluation:
			evaluations++
		case observe.OperationSubscriptionOverflow:
			overflows++
			if value.QueueLimit != 1 {
				f.t.Fatalf("overflow queue limit=%d want=1", value.QueueLimit)
			}
		}
	}
	if claims != 4 || attempts != 16 || acknowledgements != 16 || evaluations < 1 || evaluations > 16 || overflows != 1 {
		f.t.Fatalf("event resources claims=%d attempts=%d acknowledgements=%d evaluations=%d overflows=%d observations=%v", claims, attempts, acknowledgements, evaluations, overflows, eventTrace.snapshot())
	}
	if _, err := stream.Recv(f.ctx); eventErrorCode(err) != events.CodeSubscriptionOverflow {
		f.t.Fatalf("overflow stream error=%v code=%s", err, eventErrorCode(err))
	}
	f.assertCDCWorkerBound(baseline)
}

type cdcConcurrency struct {
	active atomic.Int32
	peak   atomic.Int32
	starts atomic.Int32
}

type loadCDC struct {
	identity events.CDCIdentity
	counts   *cdcConcurrency
}

func (adapter *loadCDC) Identity() events.CDCIdentity { return adapter.identity }

func (*loadCDC) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}

func (adapter *loadCDC) Run(ctx context.Context, _ events.CDCEmitter) error {
	current := adapter.counts.active.Add(1)
	for current > adapter.counts.peak.Load() && !adapter.counts.peak.CompareAndSwap(adapter.counts.peak.Load(), current) {
	}
	adapter.counts.starts.Add(1)
	<-ctx.Done()
	adapter.counts.active.Add(-1)
	return nil
}

func (f *fixture) assertCDCWorkerBound(baseline int) {
	counts := &cdcConcurrency{}
	adapters := make([]events.CDCAdapter, 3)
	for index := range adapters {
		adapters[index] = &loadCDC{identity: events.CDCIdentity{
			Name: fmt.Sprintf("p8-load-%d", index), Version: "v1", Provider: f.database.Provider(),
		}, counts: counts}
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		f.t.Fatal(err)
	}
	config := appConfig(f.database, transport, f.trace)
	config.CDCAdapters = adapters
	app, err := social.Open(f.ctx, config)
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- app.RunEventPublisher(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for counts.starts.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if counts.starts.Load() != 3 || counts.peak.Load() != 3 {
		f.t.Fatalf("CDC starts=%d peak=%d want=3", counts.starts.Load(), counts.peak.Load())
	}
	if got := len(app.EventCapabilities().CDCAdapterIdentities()); got != 3 {
		f.t.Fatalf("CDC capability identities=%d want=3", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			f.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("CDC workers did not stop")
	}
	if counts.active.Load() != 0 {
		f.t.Fatalf("CDC active after stop=%d", counts.active.Load())
	}
	settleGoroutines()
	if current := runtime.NumGoroutine(); current > baseline+6 {
		f.t.Fatalf("event/CDC goroutines baseline=%d after=%d", baseline, current)
	}
	assertPoolBound(f.t, f.database)
}

func (f *fixture) computedBatchConcurrency() {
	f.trace.reset()
	probes := make([]*batchProbe, 8)
	var group sync.WaitGroup
	for index := range probes {
		probes[index] = &batchProbe{}
		group.Add(1)
		go func(probe *batchProbe) {
			defer group.Done()
			ctx := social.WithPostDisplayCodeLoaderObserver(f.ctx, probe)
			response := f.graphQLContext(ctx, `query { posts(orderBy: [{id: asc}], take: 128) { id displayCode(prefix: "load-") } }`)
			if len(response.Errors) != 0 {
				f.t.Errorf("computed GraphQL errors=%v", response.Errors)
			}
		}(probes[index])
	}
	group.Wait()
	for index, probe := range probes {
		if sizes := probe.snapshot(); len(sizes) != 2 || sizes[0] != 64 || sizes[1] != 64 {
			f.t.Fatalf("computed operation %d batches=%v want [64 64]", index, sizes)
		}
	}
	reads, graphs, computed := 0, 0, 0
	for _, value := range f.trace.snapshot() {
		switch {
		case value.Kind == observe.KindRead && value.Operation == observe.OperationReadFindMany:
			if value.Statements != 1 {
				f.t.Fatalf("computed read statements=%d", value.Statements)
			}
			reads++
		case value.Kind == observe.KindGraphQL && value.Operation == observe.OperationGraphQLQuery:
			graphs++
		case value.Kind == observe.KindGraphQL && value.Operation == observe.OperationGraphQLBatchedComputed:
			if value.Aggregate != 64 {
				f.t.Fatalf("computed batch aggregate=%d want=64", value.Aggregate)
			}
			computed++
		}
	}
	if reads != 8 || graphs != 8 || computed != 16 {
		f.t.Fatalf("computed observations reads=%d graphs=%d batches=%d values=%v", reads, graphs, computed, f.trace.snapshot())
	}
}

func assertRefused(t testing.TB, values []observed, kind observe.Kind, operation observe.Operation, statements int) {
	t.Helper()
	count := 0
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			count++
			if value.Outcome != observe.OutcomeRefused || value.Statements != statements {
				t.Fatalf("observation=%v want refused statements=%d", value, statements)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s/%s refused count=%d values=%v", kind, operation, count, values)
	}
}

func (f *fixture) cardinalityRamp() {
	baselineGoroutines := runtime.NumGoroutine()
	for _, size := range []int{1, 8, 32, 128} {
		f.trace.reset()
		rows, err := f.caller.Posts.FindMany(f.ctx,
			social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(size),
			social.Posts.Select(social.Posts.ID, social.Posts.Title),
		)
		if err != nil || len(rows) != size {
			f.t.Fatalf("cardinality=%d rows=%d error=%v", size, len(rows), err)
		}
		assertOne(f.t, f.trace.snapshot(), observe.KindRead, observe.OperationReadFindMany, 1)
		assertPoolBound(f.t, f.database)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for iteration := 0; iteration < 20; iteration++ {
		rows, err := f.caller.Posts.FindMany(f.ctx, social.Posts.Take(128), social.Posts.Select(social.Posts.ID, social.Posts.Title))
		if err != nil || len(rows) != 128 {
			f.t.Fatalf("retained-heap read rows=%d error=%v", len(rows), err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	// The fixed application/runtime harness is measured after Open and before
	// any dataset rows exist. The cardinality budget then grants 32 KiB per
	// persisted Post or Comment (256 entities total), covering the declared
	// 160-byte title, UUIDs, row/mask wrappers, driver scan storage, and one
	// durable-fact representation. It therefore scales from the dataset shape
	// instead of hiding a process-wide arbitrary allowance.
	const retainedAllowancePerEntity = 32 << 10
	const persistedEntities = 128 + 128
	maximumRetainedHeap := f.preSeedHeap + uint64(persistedEntities*retainedAllowancePerEntity)
	if after.HeapAlloc > maximumRetainedHeap {
		f.t.Fatalf("retained heap pre-seed=%d before-ramp=%d after=%d plan-derived maximum=%d", f.preSeedHeap, before.HeapAlloc, after.HeapAlloc, maximumRetainedHeap)
	}
	settleGoroutines()
	if current := runtime.NumGoroutine(); current > baselineGoroutines+4 {
		f.t.Fatalf("cardinality goroutines baseline=%d after=%d", baselineGoroutines, current)
	}
	assertPoolBound(f.t, f.database)
	f.mutationCardinalityRamp()
}

func (f *fixture) mutationCardinalityRamp() {
	for _, size := range []int{1, 2, 4, 8} {
		inputs := make([]social.CommentCreateInput, size)
		for index := range inputs {
			inputs[index] = loadCommentInput(f.t, 1000+size*20+index, f.userID)
		}
		f.trace.reset()
		_, err := f.caller.Posts.Update(f.ctx,
			social.Posts.ByID.Value(loadPostID(f.t, size)),
			social.Posts.Update(createManyComments(inputs)),
		)
		if err != nil {
			f.t.Fatalf("nested createMany size=%d: %v", size, err)
		}
		statements := singleStatements(f.t, f.trace.snapshot(), observe.KindMutation, observe.OperationMutationUpdate, observe.OutcomeSuccess)
		// The reviewed nested plan has six fixed parent/transaction/fact
		// statements and exactly three statements per independently authorized
		// child. This freezes linear—not merely generous—growth.
		if want := 6 + 3*size; statements != want {
			f.t.Fatalf("nested size=%d statements=%d want exact reviewed plan=%d", size, statements, want)
		}
	}

	for _, size := range []int{1, 8, 32, 128} {
		identities := make([]golem.UUID, size)
		for index := range identities {
			identities[index] = loadPostID(f.t, index+1)
		}
		f.trace.reset()
		count, err := f.caller.Posts.UpdateMany(f.ctx, social.Posts.ID.In(identities...),
			social.Posts.UpdateMany(social.Posts.Priority.Set(int32(size))),
		)
		if err != nil || count != int64(size) {
			f.t.Fatalf("updateMany size=%d count=%d error=%v", size, count, err)
		}
		statements := singleStatements(f.t, f.trace.snapshot(), observe.KindMutation, observe.OperationMutationUpdateMany, observe.OutcomeSuccess)
		// The bounded identity capture is one statement through 32 identities;
		// the 128-identity shape crosses exactly one provider-safe bind chunk.
		want := 6
		if size == 128 {
			want = 7
		}
		if statements != want {
			f.t.Fatalf("updateMany size=%d statements=%d want exact reviewed plan=%d", size, statements, want)
		}
	}

	limitedTrace := &trace{}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		f.t.Fatal(err)
	}
	config := appConfig(f.database, transport, limitedTrace)
	config.MutationLimits = golemruntime.MutationLimits{MaxTouchedRows: 4}
	limitedApp, err := social.Open(f.ctx, config)
	if err != nil {
		f.t.Fatal(err)
	}
	limitedCaller, err := limitedApp.ForPrincipal(f.ctx, social.Principal{Development: true, DevUserID: f.userID})
	if err != nil {
		f.t.Fatal(err)
	}
	inputs := make([]social.CommentCreateInput, 5)
	for index := range inputs {
		inputs[index] = loadCommentInput(f.t, 2000+index, f.userID)
	}
	limitedTrace.reset()
	if _, err := limitedCaller.Posts.Update(f.ctx, social.Posts.ByID.Value(loadPostID(f.t, 16)),
		social.Posts.Update(createManyComments(inputs))); err == nil {
		f.t.Fatal("nested touched-row excess was accepted")
	}
	singleStatements(f.t, limitedTrace.snapshot(), observe.KindMutation, observe.OperationMutationUpdate, observe.OutcomeRefused)
	assertPoolBound(f.t, f.database)
}

func createManyComments(inputs []social.CommentCreateInput) golem.NestedValue[social.Post] {
	return social.Posts.Comments.CreateMany(inputs[0], inputs[1:]...)
}

func singleStatements(t testing.TB, values []observed, kind observe.Kind, operation observe.Operation, outcome observe.Outcome) int {
	t.Helper()
	matches := []observed{}
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 || matches[0].Outcome != outcome {
		t.Fatalf("%s/%s outcome=%s matches=%v values=%v", kind, operation, outcome, matches, values)
	}
	return matches[0].Statements
}

func loadCommentInput(t testing.TB, index int, author golem.UUID) social.CommentCreateInput {
	t.Helper()
	return social.Comments.Create(
		social.Comments.ID.Create(mustUUID(t, fmt.Sprintf("b3000000-0000-0000-0000-%012d", index))),
		social.Comments.AuthorID.Create(author), social.Comments.Body.Create("nested load comment"),
	)
}

func loadPostID(t testing.TB, index int) golem.UUID {
	t.Helper()
	return mustUUID(t, fmt.Sprintf("b1000000-0000-0000-0000-%012d", index))
}

func (f *fixture) createLoadEventPost(system social.System[social.Principal], index int) {
	f.t.Helper()
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		f.t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56")
	if err != nil {
		f.t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":false}`))
	if err != nil {
		f.t.Fatal(err)
	}
	id := mustUUID(f.t, fmt.Sprintf("b4000000-0000-0000-0000-%012d", index+1))
	if _, err := system.Posts.Create(f.ctx, social.Posts.Create(
		social.Posts.ID.Create(id), social.Posts.AuthorID.Create(f.userID),
		social.Posts.Title.Create(fmt.Sprintf("load-event-%02d", index)), social.Posts.Body.Create("event load body"),
		social.Posts.Published.Create(true), social.Posts.Visibility.Create(social.VisibilityPublic),
		social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Topics.Create(golem.List[string]{"load"}),
	)); err != nil {
		f.t.Fatalf("create load event post %d: %v", index, err)
	}
}

func (f *fixture) clearEventFacts() {
	f.t.Helper()
	for _, table := range eventTables(f.database.Provider()) {
		if _, err := f.database.UnsafeSQLX().ExecContext(f.ctx, "DELETE FROM "+table); err != nil {
			f.t.Fatalf("clear %s: %v", table, err)
		}
	}
}

func eventTables(providerID golem.Provider) []string {
	if providerID == golem.PostgreSQL {
		return []string{`"_golem"."_golem_outbox_delivery"`, `"_golem"."_golem_outbox"`}
	}
	return []string{`"_golem_outbox_delivery"`, `"_golem_outbox"`}
}

func waitForDeliveredFacts(t testing.TB, database *provider.Database, want int) {
	t.Helper()
	table := `"_golem_outbox_delivery"`
	if database.Provider() == golem.PostgreSQL {
		table = `"_golem"."_golem_outbox_delivery"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.UnsafeSQLX().GetContext(context.Background(), &count, "SELECT COUNT(*) FROM "+table+" WHERE status='delivered'"); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("delivered fact count did not reach %d", want)
}

func waitForObservation(t testing.TB, trace *trace, operation observe.Operation, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, value := range trace.snapshot() {
			if value.Operation == operation {
				count++
			}
		}
		if count >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("observation %s did not reach %d: %v", operation, want, trace.snapshot())
}

func waitForPublisher(t testing.TB, app *social.App[social.Principal]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !app.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !app.EventCapabilities().PublisherRunning() {
		t.Fatal("event publisher did not report running")
	}
}

func eventErrorCode(err error) events.ErrorCode {
	if code, ok := events.CodeOf(err); ok {
		return code
	}
	return ""
}

func settleGoroutines() {
	for iteration := 0; iteration < 3; iteration++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (f *fixture) graphQL(query string) graphResponse {
	return f.graphQLContext(f.ctx, query)
}

func (f *fixture) graphQLContext(ctx context.Context, query string) graphResponse {
	payload, _ := json.Marshal(map[string]any{"query": query})
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	var result graphResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		f.t.Fatalf("decode GraphQL response=%q: %v", response.Body.String(), err)
	}
	return result
}

func openDatabase(t testing.TB, ctx context.Context) *provider.Database {
	t.Helper()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	default:
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func seedPosts(t testing.TB, ctx context.Context, system social.System[social.Principal], author golem.UUID, count int) {
	t.Helper()
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":false}`))
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= count; index++ {
		postID := mustUUID(t, fmt.Sprintf("b1000000-0000-0000-0000-%012d", index))
		commentID := mustUUID(t, fmt.Sprintf("b2000000-0000-0000-0000-%012d", index))
		visibility := []social.Visibility{social.VisibilityPrivate, social.VisibilityFollowers, social.VisibilityPublic}[(index-1)%3]
		if _, err := system.Posts.Create(ctx, social.Posts.Create(
			social.Posts.ID.Create(postID), social.Posts.AuthorID.Create(author),
			social.Posts.Title.Create(fmt.Sprintf("load post %03d", index)), social.Posts.Body.Create("load body"),
			social.Posts.Published.Create(index%2 == 0), social.Posts.Visibility.Create(visibility),
			social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
			social.Posts.Metadata.Create(metadata), social.Posts.Topics.Create(golem.List[string]{"load"}),
		)); err != nil {
			t.Fatalf("seed post %d: %v", index, err)
		}
		if _, err := system.Comments.Create(ctx, social.Comments.Create(
			social.Comments.ID.Create(commentID), social.Comments.PostID.Create(postID), social.Comments.AuthorID.Create(author),
			social.Comments.Body.Create("load comment"),
		)); err != nil {
			t.Fatalf("seed comment %d: %v", index, err)
		}
	}
}

func mustUUID(t testing.TB, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
