package failureconsumer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/gorilla/websocket"
)

const failureUserID = "c0000000-0000-0000-0000-000000000001"

type failureFixture struct {
	t        *testing.T
	ctx      context.Context
	database *provider.Database
	app      *social.App[social.Principal]
	caller   *social.Caller[social.Principal]
	userID   golem.UUID
}

func TestP8ExternalOracleScenario(t *testing.T) {
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "cancellation-slow-client":
		testCancellationAndSlowClient(t)
	case "provider-contention-pool-starvation":
		testProviderContentionAndPoolStarvation(t)
	case "hook-computed-observer-failure":
		testHookComputedAndObserverFailure(t)
	case "publisher-cdc-migration-crash":
		testPublisherCDCAndMigrationCrash(t)
	case "graceful-forced-shutdown":
		testGracefulAndForcedShutdown(t)
	default:
		t.Fatalf("unknown failure scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func newFailureFixture(t *testing.T, observer observe.Observer, limits events.Limits, transport events.EventTransport) *failureFixture {
	t.Helper()
	ctx := context.Background()
	database := openFailureDatabase(t, ctx, 0)
	if transport == nil {
		var err error
		transport, err = events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
		if err != nil {
			t.Fatal(err)
		}
	}
	app, err := social.Open(ctx, failureConfig(database, transport, observer, limits))
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	userID := failureUUID(t, failureUserID)
	if _, err := app.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("failure-user"), social.Users.Email.Create("failure@example.test"),
	)); err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	return &failureFixture{t: t, ctx: ctx, database: database, app: app, caller: caller, userID: userID}
}

func failureConfig(database *provider.Database, transport events.EventTransport, observer observe.Observer, limits events.Limits) social.Config[social.Principal] {
	return social.Config[social.Principal]{
		Database: database, EventTransport: transport, EventLimits: limits, Observer: observer,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			return social.Actor{UserID: principal.DevUserID, Authenticated: principal.Development}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-failure-principal" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	}
}

func (f *failureFixture) close() {
	f.t.Helper()
	if stats := f.database.UnsafeSQLX().Stats(); stats.InUse != 0 {
		f.t.Errorf("connections in use before close=%d", stats.InUse)
	}
	if err := f.database.Close(); err != nil {
		f.t.Error(err)
	}
}

func testCancellationAndSlowClient(t *testing.T) {
	baseline := runtime.NumGoroutine()
	f := newFailureFixture(t, nil, events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, ClaimRows: 4, PublisherConcurrency: 1}, nil)
	defer f.close()
	f.createPost(f.ctx, 1, "cancel-base")

	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	rows, err := f.caller.Posts.FindMany(cancelled, social.Posts.Take(1), social.Posts.Select(social.Posts.ID))
	if err == nil || rows != nil {
		t.Fatalf("cancelled caller read rows=%v error=%v", rows, err)
	}
	if count, err := f.caller.Posts.Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("post-cancellation recovery count=%d error=%v", count, err)
	}

	graph, err := f.app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) {
			return social.Principal{Development: true, DevUserID: f.userID}, true
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Shutdown(context.Background())
	probe := &cancelledLoader{entered: make(chan struct{}, 4)}
	requestContext, cancelRequest := context.WithCancel(social.WithPostDisplayCodeLoaderObserver(f.ctx, probe))
	done := make(chan graphResult, 1)
	go func() {
		done <- executeGraphQL(t, graph.Handler(), requestContext, `query { posts(take: 1) { id displayCode(prefix: "slow-") } }`)
	}()
	select {
	case <-probe.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow computed client did not enter loader")
	}
	cancelRequest()
	select {
	case result := <-done:
		if len(result.Errors) == 0 {
			t.Fatalf("cancelled slow GraphQL result=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled slow GraphQL operation did not return")
	}
	if recovered := executeGraphQL(t, graph.Handler(), f.ctx, `query { posts(take: 1) { id } }`); len(recovered.Errors) != 0 {
		t.Fatalf("GraphQL did not recover after cancellation: %+v", recovered)
	}
	testDisconnectedSlowWebSocket(t, f, graph, probe)
	settleFailureGoroutines()
	if current := runtime.NumGoroutine(); current > baseline+5 {
		t.Fatalf("cancellation goroutines baseline=%d current=%d", baseline, current)
	}
	assertFailurePoolReleased(t, f.database)
}

type cancelledLoader struct {
	entered chan struct{}
}

func (loader *cancelledLoader) ObservePostDisplayCodeLoad(ctx context.Context, _ []golem.UUID, _ social.DisplayCodeArgs) error {
	select {
	case loader.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func testDisconnectedSlowWebSocket(t *testing.T, f *failureFixture, graph *social.GraphQLServer, probe *cancelledLoader) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := social.WithPostDisplayCodeLoaderObserver(request.Context(), probe)
		graph.Handler().ServeHTTP(writer, request.WithContext(ctx))
	}))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := connection.ReadJSON(&frame); err != nil || frame["type"] != "connection_ack" {
		t.Fatalf("websocket ack=%v error=%v", frame, err)
	}
	if err := connection.WriteJSON(map[string]any{
		"id": "slow", "type": "subscribe", "payload": map[string]any{
			"query": `subscription { postEvents(where: {title: {startsWith: "ws-slow-"}}) { id entity { id displayCode(prefix: "slow-") } } }`,
		},
	}); err != nil {
		t.Fatal(err)
	}
	publisherContext, cancelPublisher := context.WithCancel(f.ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- f.app.RunEventPublisher(publisherContext) }()
	waitFailurePublisher(t, f.app)
	f.createPost(f.ctx, 2, "ws-slow-disconnect")
	select {
	case <-probe.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow websocket event did not enter computed loader")
	}
	_ = connection.Close()
	cancelPublisher()
	select {
	case err := <-publisherDone:
		if err != nil && publisherContext.Err() == nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not recover from disconnected slow websocket")
	}
	if count, err := f.caller.Posts.Count(f.ctx); err != nil || count != 2 {
		t.Fatalf("post-websocket recovery count=%d error=%v", count, err)
	}
}

func testProviderContentionAndPoolStarvation(t *testing.T) {
	f := newFailureFixture(t, nil, events.Limits{}, nil)
	defer f.close()
	f.createPost(f.ctx, 1, "contention-base")

	maximum := f.database.Pool().MaximumOpen()
	connections := make([]*providerConnection, 0, maximum)
	for index := 0; index < maximum; index++ {
		connection, err := f.database.UnsafeSQLX().Connx(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, &providerConnection{close: connection.Close})
	}
	starved, cancel := context.WithTimeout(f.ctx, 75*time.Millisecond)
	defer cancel()
	if _, err := f.caller.Posts.Count(starved); err == nil {
		t.Fatal("pool-starved read returned false success")
	}
	for _, connection := range connections {
		if err := connection.close(); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := f.caller.Posts.Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("pool recovery count=%d error=%v", count, err)
	}

	tx, err := f.database.UnsafeSQLX().BeginTxx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	query := f.database.UnsafeSQLX().Rebind(`UPDATE posts SET title=? WHERE id=?`)
	if _, err := tx.ExecContext(f.ctx, query, "held-title", failurePostID(t, 1).String()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	blocked, stop := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer stop()
	_, updateErr := f.caller.Posts.Update(blocked, social.Posts.ByID.Value(failurePostID(t, 1)), social.Posts.Update(social.Posts.Title.Set("must-not-commit")))
	if updateErr == nil {
		_ = tx.Rollback()
		t.Fatal("lock-conflicted update returned false success")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(failurePostID(t, 1)), social.Posts.Update(social.Posts.Title.Set("recovered-title"))); err != nil {
		t.Fatalf("post-contention update: %v", err)
	}
	row, err := f.caller.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(failurePostID(t, 1)), social.Posts.Select(social.Posts.Title))
	if err != nil {
		t.Fatal(err)
	}
	if title, present := golem.Value(row, social.Posts.Title).Get(); !present || title != "recovered-title" {
		t.Fatalf("contention recovery title=%q present=%t", title, present)
	}
	testGeneratedUniqueConflict(t, f)
	if f.database.Provider() == golem.PostgreSQL {
		testPostgreSQLSerializationRecovery(t, f)
	}
	assertFailurePoolReleased(t, f.database)
}

func testGeneratedUniqueConflict(t *testing.T, f *failureFixture) {
	t.Helper()
	var group sync.WaitGroup
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := f.app.System().Users.Create(f.ctx, social.Users.Create(
				social.Users.ID.Create(failureUUID(t, fmt.Sprintf("c2000000-0000-0000-0000-%012d", index+1))),
				social.Users.Handle.Create("row17-unique-race"),
				social.Users.Email.Create(fmt.Sprintf("unique-%d@example.test", index)),
			))
			results <- err
		}()
	}
	group.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var public *golem.Error
		if errors.As(err, &public) && public.Code == golem.CodeConflict {
			conflicted++
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("generated unique race succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func testPostgreSQLSerializationRecovery(t *testing.T, f *failureFixture) {
	t.Helper()
	first, err := f.database.UnsafeSQLX().BeginTxx(f.ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.database.UnsafeSQLX().BeginTxx(f.ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		_ = first.Rollback()
		t.Fatal(err)
	}
	var firstViews, secondViews int64
	query := `SELECT views FROM posts WHERE id=$1`
	if err := first.GetContext(f.ctx, &firstViews, query, failurePostID(t, 1).String()); err != nil {
		t.Fatal(err)
	}
	if err := second.GetContext(f.ctx, &secondViews, query, failurePostID(t, 1).String()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ExecContext(f.ctx, `UPDATE posts SET views=$1 WHERE id=$2`, firstViews+1, failurePostID(t, 1).String()); err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	go func() {
		_, err := second.ExecContext(f.ctx, `UPDATE posts SET views=$1 WHERE id=$2`, secondViews+1, failurePostID(t, 1).String())
		if err == nil {
			err = second.Commit()
		} else {
			_ = second.Rollback()
		}
		secondResult <- err
	}()
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondResult:
		if err == nil {
			t.Fatal("serializable conflict returned false success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serializable conflict did not resolve")
	}
	if _, err := f.caller.Posts.Count(f.ctx); err != nil {
		t.Fatalf("caller did not recover from serialization conflict: %v", err)
	}
}

type providerConnection struct{ close func() error }

type panicObserver struct{ calls atomic.Int64 }

func (observer *panicObserver) ObserveGolem(context.Context, observe.Observation) {
	observer.calls.Add(1)
	panic("sanitized observer panic")
}

type panicPostHook struct{ phase social.PostHookPhase }

func (hook panicPostHook) ObservePostHook(_ context.Context, phase social.PostHookPhase, _ golem.Row[social.Post]) error {
	if phase == hook.phase {
		panic("sanitized hook panic")
	}
	return nil
}

type panicComputed struct{}

func (panicComputed) ObservePostDisplayCodeLoad(context.Context, []golem.UUID, social.DisplayCodeArgs) error {
	panic("sanitized computed panic")
}

type blockingPostHook struct {
	entered chan struct{}
}

func (hook blockingPostHook) ObservePostHook(ctx context.Context, phase social.PostHookPhase, _ golem.Row[social.Post]) error {
	if phase != social.PostHookBeforeCreate {
		return nil
	}
	select {
	case <-hook.entered:
	default:
		close(hook.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}

type blockingObserver struct {
	enabled atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (observer *blockingObserver) ObserveGolem(context.Context, observe.Observation) {
	if !observer.enabled.Load() {
		return
	}
	observer.once.Do(func() {
		close(observer.entered)
		<-observer.release
	})
}

func testHookComputedAndObserverFailure(t *testing.T) {
	observer := &panicObserver{}
	f := newFailureFixture(t, observer, events.Limits{}, nil)
	defer f.close()

	panicContext := social.WithPostHookObserver(f.ctx, panicPostHook{phase: social.PostHookBeforeCreate})
	if _, err := f.caller.Posts.Create(panicContext, failurePostInput(t, f.userID, 1, "hook-panic")); err == nil {
		t.Fatal("panicking before hook returned false success")
	} else if strings.Contains(err.Error(), "sanitized hook panic") {
		t.Fatalf("hook panic payload reached public Caller error: %v", err)
	}
	if count, err := f.caller.Posts.Count(f.ctx); err != nil || count != 0 {
		t.Fatalf("hook panic partial commit count=%d error=%v", count, err)
	}
	f.createPost(f.ctx, 2, "observer-panic-recovery")
	if observer.calls.Load() == 0 {
		t.Fatal("panicking observer was never invoked")
	}

	graph, err := f.app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) {
			return social.Principal{Development: true, DevUserID: f.userID}, true
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Shutdown(context.Background())
	graphHookID := failurePostID(t, 3).String()
	graphHook := executeGraphQLVariables(t, graph.Handler(), social.WithPostHookObserver(f.ctx, panicPostHook{phase: social.PostHookBeforeCreate}),
		`mutation Create($data: PostCreateInput!) { createPost(data: $data) { id } }`,
		map[string]any{"data": failureGraphPostInput(graphHookID, "graphql-hook-panic")})
	rawHook, _ := json.Marshal(graphHook)
	if len(graphHook.Errors) == 0 || bytes.Contains(rawHook, []byte("sanitized hook panic")) {
		t.Fatalf("GraphQL hook panic was not closed: %s", rawHook)
	}
	if _, err := f.app.System().Posts.FindUnique(f.ctx, social.Posts.ByID.Value(failurePostID(t, 3)), social.Posts.Select(social.Posts.ID)); err == nil {
		t.Fatal("GraphQL before-hook panic committed a row")
	}

	afterContext := social.WithPostHookObserver(f.ctx, panicPostHook{phase: social.PostHookAfterCreate})
	factsBeforeAfterHook := countFailureFacts(t, f.database)
	if _, err := f.caller.Posts.Create(afterContext, failurePostInput(t, f.userID, 4, "after-hook-panic")); err == nil {
		t.Fatal("panicking transaction-after hook returned false success")
	}
	if _, err := f.app.System().Posts.FindUnique(f.ctx, social.Posts.ByID.Value(failurePostID(t, 4)), social.Posts.Select(social.Posts.ID)); err == nil {
		t.Fatal("transaction-after panic committed row/fact")
	}
	if facts := countFailureFacts(t, f.database); facts != factsBeforeAfterHook {
		t.Fatalf("transaction-after panic persisted an outbox fact: before=%d after=%d", factsBeforeAfterHook, facts)
	}
	testAfterCommitPanic(t, f)
	testHookAndObserverLatency(t, f)

	result := executeGraphQL(t, graph.Handler(), social.WithPostDisplayCodeLoaderObserver(f.ctx, panicComputed{}), `query { posts(take: 1) { id displayCode(prefix: "panic-") } }`)
	if len(result.Errors) == 0 {
		t.Fatalf("panicking computed field returned false success: %+v", result)
	}
	if recovered := executeGraphQL(t, graph.Handler(), f.ctx, `query { posts(take: 1) { id displayCode(prefix: "ok-") } }`); len(recovered.Errors) != 0 {
		t.Fatalf("computed path did not recover: %+v", recovered)
	}
	if count, err := f.caller.Posts.Count(f.ctx); err != nil || count != 2 {
		t.Fatalf("observer failure altered correctness count=%d error=%v", count, err)
	}
	assertFailurePoolReleased(t, f.database)
}

func testHookAndObserverLatency(t *testing.T, f *failureFixture) {
	t.Helper()
	hook := blockingPostHook{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(social.WithPostHookObserver(f.ctx, hook))
	result := make(chan error, 1)
	go func() {
		_, err := f.caller.Posts.Create(ctx, failurePostInput(t, f.userID, 6, "cancelled-hook"))
		result <- err
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow before-hook did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled before-hook returned false success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled before-hook did not return")
	}
	if _, err := f.app.System().Posts.FindUnique(f.ctx, social.Posts.ByID.Value(failurePostID(t, 6)), social.Posts.Select(social.Posts.ID)); err == nil {
		t.Fatal("cancelled before-hook committed a row")
	}

	observer := &blockingObserver{entered: make(chan struct{}), release: make(chan struct{})}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		t.Fatal(err)
	}
	app, err := social.Open(f.ctx, failureConfig(f.database, memory, observer, events.Limits{}))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(f.ctx, social.Principal{Development: true, DevUserID: f.userID})
	if err != nil {
		t.Fatal(err)
	}
	observer.enabled.Store(true)
	countResult := make(chan struct {
		count int64
		err   error
	}, 1)
	go func() {
		count, err := caller.Posts.Count(f.ctx)
		countResult <- struct {
			count int64
			err   error
		}{count: count, err: err}
	}()
	select {
	case <-observer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow observer did not start")
	}
	if inUse := f.database.UnsafeSQLX().Stats().InUse; inUse != 0 {
		t.Fatalf("observer retained database resources: in_use=%d", inUse)
	}
	close(observer.release)
	select {
	case result := <-countResult:
		if result.err != nil || result.count != 2 {
			t.Fatalf("slow observer changed operation result count=%d error=%v", result.count, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow observer did not release operation")
	}
}

func testAfterCommitPanic(t *testing.T, f *failureFixture) {
	t.Helper()
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan golem.AfterCommitFailure, 1)
	config := failureConfig(f.database, memory, nil, events.Limits{})
	config.AfterCommitError = func(_ context.Context, failure golem.AfterCommitFailure) { reported <- failure }
	app, err := social.Open(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(f.ctx, social.Principal{Development: true, DevUserID: f.userID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := social.WithPostHookObserver(f.ctx, panicPostHook{phase: social.PostHookAfterCommitCreate})
	factsBefore := countFailureFacts(t, f.database)
	if _, err := caller.Posts.Create(ctx, failurePostInput(t, f.userID, 5, "after-commit-panic")); err != nil {
		t.Fatalf("after-commit panic changed committed result: %v", err)
	}
	select {
	case failure := <-reported:
		if failure.Operation() != golem.HookCreate || failure.Cause() == nil {
			t.Fatalf("after-commit report=%+v", failure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("after-commit panic was not reported")
	}
	if _, err := f.app.System().Posts.FindUnique(f.ctx, social.Posts.ByID.Value(failurePostID(t, 5)), social.Posts.Select(social.Posts.ID)); err != nil {
		t.Fatalf("after-commit panic lost committed row: %v", err)
	}
	if facts := countFailureFacts(t, f.database); facts != factsBefore+1 {
		t.Fatalf("after-commit panic lost durable fact: before=%d after=%d", factsBefore, facts)
	}
}

func testPublisherCDCAndMigrationCrash(t *testing.T) {
	example := os.Getenv("P8_ORACLE_EXAMPLE")
	if example == "" {
		t.Fatal("P8_ORACLE_EXAMPLE is required")
	}
	hostBinary := filepath.Join(t.TempDir(), "social-host")
	fixtureBinary := filepath.Join(t.TempDir(), "social-recovery-fixture")
	runFailureCommand(t, example, os.Environ(), "go", "build", "-o", hostBinary, "./cmd/social")
	runFailureCommand(t, example, os.Environ(), "go", "build", "-o", fixtureBinary, "./cmd/social-recovery-fixture")
	environment := failureEnvironment(os.Environ(), "GOLEM_PROVIDER", os.Getenv("P8_ORACLE_PROVIDER"))
	environment = failureEnvironment(environment, "GOLEM_DATABASE_DSN", os.Getenv("P8_ORACLE_DSN"))
	seed := runFailureCommand(t, example, environment, fixtureBinary, "seed")
	if !strings.Contains(seed, `"status":"pending"`) {
		t.Fatalf("recovery seed omitted pending fact: %s", seed)
	}

	crashed := startFailureHost(t, example, hostBinary)
	assertFailureHostReady(t, crashed.address)
	stopFailureHost(t, crashed, syscall.SIGKILL)
	database := openFailureDatabase(t, context.Background(), 0)
	defer database.Close()
	restarted := startFailureHost(t, example, hostBinary)
	assertFailureHostReady(t, restarted.address)
	waitFailureDeliveryDeadline(t, database, 1, 45*time.Second)
	stopFailureHost(t, restarted, syscall.SIGTERM)
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		t.Fatal(err)
	}
	verificationApp, err := social.Open(context.Background(), failureConfig(database, memory, nil, events.Limits{}))
	if err != nil {
		t.Fatal(err)
	}
	recoveryID := failureUUID(t, "80000000-0000-0000-0000-000000000002")
	row, err := verificationApp.System().Posts.FindUnique(context.Background(), social.Posts.ByID.Value(recoveryID), social.Posts.Select(social.Posts.Title))
	if err != nil {
		t.Fatal(err)
	}
	if title, present := golem.Value(row, social.Posts.Title).Get(); !present || title != "P8 recovery canary post" {
		t.Fatalf("publisher restart row title=%q present=%t", title, present)
	}
	testDuplicateAcceptanceWindow(t, database)
	testCDCWorkerRestart(t, database)
	testMigrationInterruption(t, database, example)
}

type duplicateWindowTransport struct {
	events.EventTransport
	mu        sync.Mutex
	publishes [][]golem.EventID
}

func (transport *duplicateWindowTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport *duplicateWindowTransport) Publish(ctx context.Context, batch events.EventBatch) error {
	identities := make([]golem.EventID, len(batch.Events()))
	for index, notice := range batch.Events() {
		identities[index] = notice.EventID()
	}
	transport.mu.Lock()
	transport.publishes = append(transport.publishes, identities)
	attempt := len(transport.publishes)
	transport.mu.Unlock()
	if err := transport.EventTransport.Publish(ctx, batch); err != nil {
		return err
	}
	if attempt == 1 {
		// This is the exact duplicate-acceptance window: transport accepted the
		// immutable event but its acknowledgement was lost.
		return events.Failure(events.CodeEventTransport)
	}
	return nil
}

func (transport *duplicateWindowTransport) snapshot() [][]golem.EventID {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	result := make([][]golem.EventID, len(transport.publishes))
	for index := range transport.publishes {
		result[index] = append([]golem.EventID(nil), transport.publishes[index]...)
	}
	return result
}

func testDuplicateAcceptanceWindow(t *testing.T, database *provider.Database) {
	t.Helper()
	clearFailureFacts(t, database)
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
	if err != nil {
		t.Fatal(err)
	}
	transport := &duplicateWindowTransport{EventTransport: memory}
	limits := events.Limits{ClaimRows: 1, PublisherConcurrency: 1, RetryBase: 10 * time.Millisecond, RetryCap: 20 * time.Millisecond}
	app, err := social.Open(context.Background(), failureConfig(database, transport, nil, limits))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunEventPublisher(ctx) }()
	waitFailurePublisher(t, app)
	userID := failureUUID(t, "80000000-0000-0000-0000-000000000001")
	if _, err := app.System().Posts.Create(context.Background(), failurePostInput(t, userID, 91, "duplicate-window")); err != nil {
		t.Fatal(err)
	}
	waitFailureDelivery(t, database, 1)
	deadline := time.Now().Add(5 * time.Second)
	for len(transport.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	publications := transport.snapshot()
	if len(publications) != 2 || len(publications[0]) != 1 || len(publications[1]) != 1 || publications[0][0] != publications[1][0] {
		t.Fatalf("duplicate retry identities=%v", publications)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate-window publisher did not stop")
	}
	assertFailurePoolReleased(t, database)
}

type restartCDC struct {
	provider   golem.Provider
	batch      events.CDCBatchInput
	starts     atomic.Int32
	active     atomic.Int32
	emits      atomic.Int32
	checkpoint atomic.Bool
}

type recordingTransport struct {
	events.EventTransport
	publications atomic.Int32
}

func (transport *recordingTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport *recordingTransport) Publish(ctx context.Context, batch events.EventBatch) error {
	if err := transport.EventTransport.Publish(ctx, batch); err != nil {
		return err
	}
	transport.publications.Add(1)
	return nil
}

func (adapter *restartCDC) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "p8-row17-restart", Version: "v1", Provider: adapter.provider}
}

func (*restartCDC) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}

func (adapter *restartCDC) Run(ctx context.Context, emitter events.CDCEmitter) error {
	adapter.starts.Add(1)
	adapter.active.Add(1)
	defer adapter.active.Add(-1)
	if !adapter.checkpoint.Load() {
		if err := emitter.Emit(ctx, adapter.batch); err != nil {
			return err
		}
		// The adapter-owned checkpoint advances only after core durably accepts
		// the complete source transaction. A restart must therefore resume after
		// this cursor rather than duplicate it.
		adapter.checkpoint.Store(true)
		adapter.emits.Add(1)
	}
	<-ctx.Done()
	return nil
}

func testCDCWorkerRestart(t *testing.T, database *provider.Database) {
	t.Helper()
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil {
		t.Fatal(err)
	}
	transport := &recordingTransport{EventTransport: memory}
	clearFailureFacts(t, database)
	row := insertFailureExternalPost(t, database)
	adapter := &restartCDC{provider: database.Provider(), batch: events.CDCBatchInput{
		SourceTransactionID: "p8-row17-external-transaction",
		RecordedAt:          time.Date(2026, 8, 9, 13, 0, 0, 123000000, time.UTC),
		Cursor:              []byte("p8-row17-cursor-1"),
		Changes: []events.CDCChangeInput{{
			Ordinal: 1, Model: social.GolemGeneratedPostDescriptor.Metadata().ModelID(), Action: golem.EventCreated, After: &row,
		}},
	}}
	config := failureConfig(database, transport, nil, events.Limits{})
	config.CDCAdapters = []events.CDCAdapter{adapter}
	app, err := social.Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for run := int32(1); run <= 2; run++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- app.RunEventPublisher(ctx) }()
		deadline := time.Now().Add(5 * time.Second)
		for (adapter.starts.Load() < run || !adapter.checkpoint.Load() || transport.publications.Load() < 1) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if adapter.starts.Load() != run || adapter.active.Load() != 1 {
			t.Fatalf("CDC run=%d starts=%d active=%d", run, adapter.starts.Load(), adapter.active.Load())
		}
		if !adapter.checkpoint.Load() || adapter.emits.Load() != 1 {
			t.Fatalf("CDC run=%d checkpoint=%t emits=%d", run, adapter.checkpoint.Load(), adapter.emits.Load())
		}
		if publications := transport.publications.Load(); publications != 1 {
			t.Fatalf("CDC run=%d publications=%d want=1", run, publications)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("CDC publisher did not stop")
		}
		if adapter.active.Load() != 0 {
			t.Fatalf("CDC active after run %d=%d", run, adapter.active.Load())
		}
	}
	if adapter.emits.Load() != 1 {
		t.Fatalf("CDC restart duplicated checkpointed source transaction: emits=%d", adapter.emits.Load())
	}
	if publications := transport.publications.Load(); publications != 1 {
		t.Fatalf("CDC restart duplicated publication: publications=%d", publications)
	}
}

func insertFailureExternalPost(t *testing.T, database *provider.Database) golem.RuntimeModelRow {
	t.Helper()
	id := failureUUID(t, "80000000-0000-0000-0000-000000000003")
	authorID := failureUUID(t, "80000000-0000-0000-0000-000000000001")
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		t.Fatal(err)
	}
	clock, err := golem.ParseTime("13:00:00")
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("0.00")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"source":"cdc-restart"}`))
	if err != nil {
		t.Fatal(err)
	}
	topics := golem.List[string]{"failure", "cdc"}
	instant := time.Date(2026, 8, 9, 13, 0, 1, 123456000, time.UTC)
	statement := database.UnsafeSQLX().Rebind(`INSERT INTO posts
(id,author_id,title,body,published,reactions,priority,views,momentum,rating,budget,live_date,live_time,metadata,visibility,topics,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	arguments := []any{id.String(), authorID.String(), "CDC checkpoint recovery", "external body", true, int16(0), int32(0), int64(0), float32(0), float64(0), "0.00", date.String(), clock.String(), string(metadata.Bytes()), string(social.VisibilityPublic), `["failure","cdc"]`, instant, instant}
	if database.Provider() == golem.SQLite {
		arguments[10] = int64(0)
		arguments[12] = "13:00:00.000000"
		arguments[16], arguments[17] = instant.UnixMicro(), instant.UnixMicro()
	}
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), statement, arguments...); err != nil {
		t.Fatalf("external CDC insert: %v", err)
	}
	fields := social.GolemGeneratedPostDescriptor.Metadata().ScanFields()
	if len(fields) != 18 {
		t.Fatalf("Post scan fields=%d want=18", len(fields))
	}
	cells := []golem.RuntimeReadCell{
		golem.RuntimePresentReadCell(fields[0], id, nil), golem.RuntimePresentReadCell(fields[1], authorID, nil),
		golem.RuntimePresentReadCell(fields[2], "CDC checkpoint recovery", nil), golem.RuntimePresentReadCell(fields[3], "external body", nil),
		golem.RuntimePresentReadCell(fields[4], true, nil), golem.RuntimePresentReadCell(fields[5], int16(0), nil),
		golem.RuntimePresentReadCell(fields[6], int32(0), nil), golem.RuntimePresentReadCell(fields[7], int64(0), nil),
		golem.RuntimePresentReadCell(fields[8], float32(0), nil), golem.RuntimePresentReadCell(fields[9], float64(0), nil),
		golem.RuntimePresentReadCell(fields[10], decimal, nil), golem.RuntimePresentReadCell(fields[11], date, nil),
		golem.RuntimePresentReadCell(fields[12], clock, nil), golem.RuntimePresentReadCell(fields[13], metadata, cloneFailureJSON),
		golem.RuntimePresentReadCell(fields[14], social.VisibilityPublic, nil), golem.RuntimePresentReadCell(fields[15], topics, cloneFailureTopics),
		golem.RuntimePresentReadCell(fields[16], instant, nil), golem.RuntimePresentReadCell(fields[17], instant, nil),
	}
	row, err := golem.RuntimeCDCModelRow(social.GolemGeneratedPostDescriptor.Metadata().ModelID(), cells...)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func cloneFailureJSON(value golem.JSON[any]) golem.JSON[any] {
	clone, err := golem.NewJSONDocument[any](value.Bytes())
	if err != nil {
		panic(err)
	}
	return clone
}

func cloneFailureTopics(value golem.List[string]) golem.List[string] {
	return append(golem.List[string](nil), value...)
}

func testMigrationInterruption(t *testing.T, database *provider.Database, example string) {
	t.Helper()
	boundaries := []struct {
		name      string
		committed bool
	}{
		{name: "before_first_phase"},
		{name: "inside_transaction_before_ledger"},
		{name: "inside_transaction_before_commit"},
		{name: "after_phase_commit", committed: true},
		{name: "during_final_verification", committed: true},
	}
	root := filepath.Dir(filepath.Dir(example))
	for index, boundary := range boundaries {
		targetDSN, cleanup := freshMigrationTarget(t, database, index)
		func() {
			defer cleanup()
			environment := failureEnvironment(os.Environ(), "GOLEM_P8_MIGRATION_HELPER", "1")
			environment = failureEnvironment(environment, "GOLEM_P8_MIGRATION_BOUNDARY", boundary.name)
			environment = failureEnvironment(environment, "GOLEM_P8_MIGRATION_PROVIDER", os.Getenv("P8_ORACLE_PROVIDER"))
			environment = failureEnvironment(environment, "GOLEM_P8_MIGRATION_DSN", targetDSN)
			environment = failureEnvironment(environment, "GOLEM_P8_MIGRATION_EXAMPLE", example)
			command := exec.Command("go", "test", "./cmd/golem", "-run", "^TestP8MigrationInterruptionProcess$", "-count=1")
			command.Dir, command.Env = root, environment
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("boundary %s helper returned false success: %s", boundary.name, output)
			}
			ledger, schema := migrationTargetState(t, targetDSN)
			if boundary.committed {
				if ledger != 1 || !schema {
					t.Fatalf("boundary %s committed outcome ledger=%d schema=%t", boundary.name, ledger, schema)
				}
			} else if ledger != 0 || schema {
				t.Fatalf("boundary %s rollback outcome ledger=%d schema=%t", boundary.name, ledger, schema)
			}
			cli := os.Getenv("P8_ORACLE_CLI")
			runFailureCommand(t, example, os.Environ(), cli, "migration", "apply", "--provider", os.Getenv("P8_ORACLE_PROVIDER"), "--dsn", targetDSN, "--migrations", "migrations")
			if ledger, schema := migrationTargetState(t, targetDSN); ledger != 2 || !schema {
				t.Fatalf("boundary %s recovery ledger=%d schema=%t", boundary.name, ledger, schema)
			}
		}()
	}
}

func freshMigrationTarget(t *testing.T, database *provider.Database, index int) (string, func()) {
	t.Helper()
	if database.Provider() == golem.SQLite {
		return "file:" + filepath.Join(t.TempDir(), fmt.Sprintf("migration-boundary-%d.sqlite", index)), func() {}
	}
	parsed, err := url.Parse(os.Getenv("P8_ORACLE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("p8_failure_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), index)
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `CREATE DATABASE "`+name+`" TEMPLATE template0`); err != nil {
		t.Fatal(err)
	}
	parsed.Path, parsed.RawPath = "/"+name, ""
	cleanup := func() {
		_, _ = database.UnsafeSQLX().ExecContext(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, name)
		if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `DROP DATABASE "`+name+`"`); err != nil {
			t.Errorf("drop migration target: %v", err)
		}
	}
	return parsed.String(), cleanup
}

func migrationTargetState(t *testing.T, dsn string) (int, bool) {
	t.Helper()
	ctx := context.Background()
	var target *provider.Database
	var err error
	if os.Getenv("P8_ORACLE_PROVIDER") == "sqlite" {
		target, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	} else {
		target, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: dsn})
	}
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	ledger, schema := 0, false
	if target.Provider() == golem.SQLite {
		var ledgerExists, posts int
		if err := target.UnsafeSQLX().GetContext(ctx, &ledgerExists, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='_golem_migrations'`); err != nil {
			t.Fatal(err)
		}
		if ledgerExists != 0 {
			if err := target.UnsafeSQLX().GetContext(ctx, &ledger, `SELECT count(*) FROM "_golem_migrations"`); err != nil {
				t.Fatal(err)
			}
		}
		if err := target.UnsafeSQLX().GetContext(ctx, &posts, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='posts'`); err != nil {
			t.Fatal(err)
		}
		schema = posts == 1
	} else {
		var ledgerExists, posts int
		if err := target.UnsafeSQLX().GetContext(ctx, &ledgerExists, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname='_golem' AND tablename='_golem_migrations'`); err != nil {
			t.Fatal(err)
		}
		if ledgerExists != 0 {
			if err := target.UnsafeSQLX().GetContext(ctx, &ledger, `SELECT count(*) FROM "_golem"."_golem_migrations"`); err != nil {
				t.Fatal(err)
			}
		}
		if err := target.UnsafeSQLX().GetContext(ctx, &posts, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname='public' AND tablename='posts'`); err != nil {
			t.Fatal(err)
		}
		schema = posts == 1
	}
	return ledger, schema
}

func clearFailureFacts(t *testing.T, database *provider.Database) {
	t.Helper()
	tables := []string{`"_golem_outbox_delivery"`, `"_golem_outbox"`}
	if database.Provider() == golem.PostgreSQL {
		tables = []string{`"_golem"."_golem_outbox_delivery"`, `"_golem"."_golem_outbox"`}
	}
	for _, table := range tables {
		if _, err := database.UnsafeSQLX().ExecContext(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
}

func countFailureFacts(t *testing.T, database *provider.Database) int {
	t.Helper()
	table := `"_golem_outbox"`
	if database.Provider() == golem.PostgreSQL {
		table = `"_golem"."_golem_outbox"`
	}
	var count int
	if err := database.UnsafeSQLX().GetContext(context.Background(), &count, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatal(err)
	}
	return count
}

func waitFailureDelivery(t *testing.T, database *provider.Database, want int) {
	waitFailureDeliveryDeadline(t, database, want, 10*time.Second)
}

func waitFailureDeliveryDeadline(t *testing.T, database *provider.Database, want int, timeout time.Duration) {
	t.Helper()
	table := `"_golem_outbox_delivery"`
	if database.Provider() == golem.PostgreSQL {
		table = `"_golem"."_golem_outbox_delivery"`
	}
	deadline := time.Now().Add(timeout)
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
	t.Fatalf("delivered facts did not reach %d", want)
}

func waitFailurePublisher(t *testing.T, app *social.App[social.Principal]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !app.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !app.EventCapabilities().PublisherRunning() {
		t.Fatal("publisher did not report running")
	}
}

func failureMigrationLedger(t *testing.T, database *provider.Database) string {
	t.Helper()
	table := `"_golem_migrations"`
	if database.Provider() == golem.PostgreSQL {
		table = `"_golem"."_golem_migrations"`
	}
	var rows []string
	if err := database.UnsafeSQLX().SelectContext(context.Background(), &rows, "SELECT migration_id || ':' || chain_hash FROM "+table+" ORDER BY migration_id"); err != nil {
		t.Fatal(err)
	}
	return strings.Join(rows, "|")
}

func testGracefulAndForcedShutdown(t *testing.T) {
	example := os.Getenv("P8_ORACLE_EXAMPLE")
	if example == "" {
		t.Fatal("P8_ORACLE_EXAMPLE is required")
	}
	binary := filepath.Join(t.TempDir(), "social-host")
	fixtureBinary := filepath.Join(t.TempDir(), "social-recovery-fixture")
	runFailureCommand(t, example, os.Environ(), "go", "build", "-o", binary, "./cmd/social")
	runFailureCommand(t, example, os.Environ(), "go", "build", "-o", fixtureBinary, "./cmd/social-recovery-fixture")
	environment := failureEnvironment(os.Environ(), "GOLEM_PROVIDER", os.Getenv("P8_ORACLE_PROVIDER"))
	environment = failureEnvironment(environment, "GOLEM_DATABASE_DSN", os.Getenv("P8_ORACLE_DSN"))
	runFailureCommand(t, example, environment, fixtureBinary, "seed")
	database := openFailureDatabase(t, context.Background(), 0)
	defer database.Close()
	pending := readShutdownState(t, database)
	if pending.Status != "pending" || pending.Title != "P8 recovery canary post" {
		t.Fatalf("shutdown seed state=%+v", pending)
	}

	first := startFailureHost(t, example, binary)
	assertFailureHostReady(t, first.address)
	waitFailureDeliveryDeadline(t, database, 1, 10*time.Second)
	stopFailureHost(t, first, syscall.SIGTERM)
	delivered := readShutdownState(t, database)
	assertSameShutdownIdentity(t, pending, delivered)
	if delivered.Status != "delivered" {
		t.Fatalf("graceful shutdown state=%+v", delivered)
	}

	forced := startFailureHost(t, example, binary)
	assertFailureHostReady(t, forced.address)
	stopFailureHost(t, forced, syscall.SIGKILL)
	assertSameShutdownIdentity(t, delivered, readShutdownState(t, database))

	restarted := startFailureHost(t, example, binary)
	assertFailureHostReady(t, restarted.address)
	response := failureHTTPGraphQL(t, restarted.address, `query { posts(take: 1) { id } }`)
	if !bytes.Contains(response, []byte(`"posts"`)) || bytes.Contains(response, []byte(`"errors"`)) {
		t.Fatalf("post-kill GraphQL response=%s", response)
	}
	stopFailureHost(t, restarted, syscall.SIGTERM)
	assertSameShutdownIdentity(t, delivered, readShutdownState(t, database))
}

type shutdownState struct {
	EventID     string `db:"event_id"`
	CausationID string `db:"causation_id"`
	Status      string `db:"status"`
	Title       string `db:"title"`
}

func readShutdownState(t *testing.T, database *provider.Database) shutdownState {
	t.Helper()
	outbox, delivery := `"_golem_outbox"`, `"_golem_outbox_delivery"`
	if database.Provider() == golem.PostgreSQL {
		outbox, delivery = `"_golem"."_golem_outbox"`, `"_golem"."_golem_outbox_delivery"`
	}
	query := `SELECT o.event_id,o.causation_id,d.status,p.title FROM ` + outbox + ` o JOIN ` + delivery + ` d ON d.causation_id=o.causation_id JOIN posts p ON p.id='80000000-0000-0000-0000-000000000002'`
	var result shutdownState
	if err := database.UnsafeSQLX().GetContext(context.Background(), &result, query); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSameShutdownIdentity(t *testing.T, before, after shutdownState) {
	t.Helper()
	if before.EventID == "" || before.CausationID == "" || before.EventID != after.EventID || before.CausationID != after.CausationID || before.Title != after.Title {
		t.Fatalf("shutdown identity changed before=%+v after=%+v", before, after)
	}
}

type failureHost struct {
	command *exec.Cmd
	exited  chan struct{}
	address string
	output  *strings.Builder
	stopped atomic.Bool
	errMu   sync.Mutex
	err     error
}

func startFailureHost(t *testing.T, directory, binary string) *failureHost {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	command := exec.Command(binary)
	command.Dir = directory
	command.Env = failureEnvironment(os.Environ(), "GOLEM_PROVIDER", os.Getenv("P8_ORACLE_PROVIDER"))
	command.Env = failureEnvironment(command.Env, "GOLEM_DATABASE_DSN", os.Getenv("P8_ORACLE_DSN"))
	command.Env = failureEnvironment(command.Env, "GOLEM_HTTP_ADDRESS", address)
	output := &strings.Builder{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	host := &failureHost{command: command, exited: make(chan struct{}), address: address, output: output}
	go func() {
		err := command.Wait()
		host.errMu.Lock()
		host.err = err
		host.errMu.Unlock()
		close(host.exited)
	}()
	t.Cleanup(func() {
		if host.stopped.CompareAndSwap(false, true) {
			_ = host.command.Process.Kill()
			select {
			case <-host.exited:
			case <-time.After(5 * time.Second):
			}
		}
	})
	return host
}

func assertFailureHostReady(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + "/health/ready")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent && len(bytes.TrimSpace(body)) == 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("host %s did not become ready", address)
}

func stopFailureHost(t *testing.T, host *failureHost, signal syscall.Signal) {
	t.Helper()
	if !host.stopped.CompareAndSwap(false, true) {
		t.Fatal("host was already stopped")
	}
	if err := host.command.Process.Signal(signal); err != nil {
		t.Fatalf("signal host: %v output=%s", err, host.output)
	}
	select {
	case <-host.exited:
		host.errMu.Lock()
		err := host.err
		host.errMu.Unlock()
		if signal == syscall.SIGTERM && err != nil {
			t.Fatalf("graceful host exit: %v output=%s", err, host.output)
		}
		if signal == syscall.SIGKILL && err == nil {
			t.Fatal("forced host unexpectedly reported successful exit")
		}
	case <-time.After(10 * time.Second):
		_ = host.command.Process.Kill()
		t.Fatalf("host did not stop after %s output=%s", signal, host.output)
	}
}

func failureHTTPGraphQL(t *testing.T, address, query string) []byte {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"query": query})
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/graphql", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GraphQL status=%d error=%v", response.StatusCode, err)
	}
	return body
}

func runFailureCommand(t *testing.T, directory string, environment []string, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir, command.Env = directory, environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %s: %v\n%s", filepath.Base(executable), strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func failureEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

type graphResult struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func executeGraphQL(t *testing.T, handler http.Handler, ctx context.Context, query string) graphResult {
	return executeGraphQLVariables(t, handler, ctx, query, nil)
}

func executeGraphQLVariables(t *testing.T, handler http.Handler, ctx context.Context, query string, variables map[string]any) graphResult {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result graphResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode GraphQL response %q: %v", response.Body.String(), err)
	}
	return result
}

func failureGraphPostInput(id, title string) map[string]any {
	return map[string]any{
		"id": id, "title": title, "body": "failure body", "published": true,
		"liveDate": "2026-08-09", "liveTime": "12:34:56",
		"metadata": map[string]any{"language": "en", "pinned": false}, "topics": []any{"failure"},
	}
}

func (f *failureFixture) createPost(ctx context.Context, index int, title string) {
	f.t.Helper()
	if _, err := f.caller.Posts.Create(ctx, failurePostInput(f.t, f.userID, index, title)); err != nil {
		f.t.Fatalf("create post %d: %v", index, err)
	}
}

func failurePostInput(t *testing.T, author golem.UUID, index int, title string) social.PostCreateInput {
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
	return social.Posts.Create(
		social.Posts.ID.Create(failurePostID(t, index)), social.Posts.AuthorID.Create(author),
		social.Posts.Title.Create(title), social.Posts.Body.Create("failure body"),
		social.Posts.Published.Create(true), social.Posts.Visibility.Create(social.VisibilityPublic),
		social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock), social.Posts.Metadata.Create(metadata),
		social.Posts.Topics.Create(golem.List[string]{"failure"}),
	)
}

func failurePostID(t *testing.T, index int) golem.UUID {
	t.Helper()
	return failureUUID(t, fmt.Sprintf("c1000000-0000-0000-0000-%012d", index))
}

func failureUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	id, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func openFailureDatabase(t *testing.T, ctx context.Context, maximumOpen int) *provider.Database {
	t.Helper()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN"), Pool: postgresql.PoolConfig{MaximumOpen: maximumOpen, MaximumIdle: maximumOpen}})
	default:
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func assertFailurePoolReleased(t *testing.T, database *provider.Database) {
	t.Helper()
	stats := database.UnsafeSQLX().Stats()
	if stats.InUse != 0 || stats.OpenConnections > database.Pool().MaximumOpen() {
		t.Fatalf("pool not released stats=%+v maximum=%d", stats, database.Pool().MaximumOpen())
	}
}

func settleFailureGoroutines() {
	for iteration := 0; iteration < 3; iteration++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}
