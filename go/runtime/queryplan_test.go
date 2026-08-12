package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queryplan"
	"github.com/jmoiron/sqlx"
)

type queryPlanHookControl uint8

const (
	queryPlanHookDeny queryPlanHookControl = iota + 1
	queryPlanHookVeto
)

type queryPlanObservationRecorder struct {
	mu      sync.Mutex
	values  []observe.Observation
	entered chan struct{}
	release chan struct{}
	panic   bool
}

func (recorder *queryPlanObservationRecorder) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Kind() != observe.KindQueryPlan {
		return
	}
	recorder.mu.Lock()
	recorder.values = append(recorder.values, value)
	entered, release, panicNow := recorder.entered, recorder.release, recorder.panic
	recorder.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if panicNow {
		panic("private query-plan observer panic")
	}
}

func (recorder *queryPlanObservationRecorder) snapshot() []observe.Observation {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]observe.Observation(nil), recorder.values...)
}

type queryPlanFixture struct {
	app          *App[testPrincipal, testActor]
	caller       *Caller[testPrincipal, testActor]
	database     *sqlx.DB
	trace        *p5SQLTrace
	users        golem.ModelDescriptor[testUser]
	id           golem.EqualField[testUser, golem.UUID]
	name         golem.TextField[testUser, string]
	beforeCalls  *atomic.Int64
	afterCalls   *atomic.Int64
	policyBuilds *atomic.Int64
}

func newQueryPlanFixture(t *testing.T, observer observe.Observer) queryPlanFixture {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	plainDSN := "file:" + filepath.Join(t.TempDir(), "query-plan.sqlite")
	bootstrap, _, err := sqlite.New().Open(ctx, plainDSN)
	if err != nil {
		t.Fatal(err)
	}
	driver := bootstrap.Driver()
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	trace := &p5SQLTrace{}
	base := p5DriverConnector{driver: driver, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}
	database := sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "sqlite")
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}

	users := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	id := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[testUser, string](fixture.UserName)
	policyBuilds := &atomic.Int64{}
	allowUsers := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		policyBuilds.Add(1)
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		rules.CannotReadFields(golem.All[testUser](), name)
		return rules.Freeze(fixture.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		policyBuilds.Add(1)
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	beforeCalls, afterCalls := &atomic.Int64{}, &atomic.Int64{}
	before := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindManyHookRequest[testUser]](fixture.User, golem.HookFindMany, func(ctx context.Context, request *golem.FindManyHookRequest[testUser]) error {
		beforeCalls.Add(1)
		if !golem.ActorFrom[testActor](ctx).Allow {
			return errors.New("wrong caller actor snapshot")
		}
		switch ctx.Value(queryPlanHookControl(0)) {
		case queryPlanHookDeny:
			request.ReplaceOptions(golem.Select[testUser](name))
		case queryPlanHookVeto:
			return errors.New("private before-hook veto")
		default:
			request.AppendOptions(golem.Take[testUser](1))
		}
		return nil
	})
	beforeFirst := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindFirstHookRequest[testUser]](fixture.User, golem.HookFindFirst, func(context.Context, *golem.FindFirstHookRequest[testUser]) error {
		beforeCalls.Add(1)
		return nil
	})
	beforeOne := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindOneHookRequest[testUser]](fixture.User, golem.HookFindOne, func(context.Context, *golem.FindOneHookRequest[testUser]) error {
		beforeCalls.Add(1)
		return nil
	})
	after := golem.GeneratedAfterHookBinding[testActor, testUser, golem.FindManyHookResult[testUser]](fixture.User, golem.HookFindMany, func(context.Context, golem.FindManyHookResult[testUser]) error {
		afterCalls.Add(1)
		return nil
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUsers, allowPosts}, []golem.HookBinding[testActor]{before, beforeFirst, beforeOne, after}))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, Observer: observer,
		ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{Allow: true}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	caller, err := app.ForPrincipal(ctx, testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	trace.reset()
	return queryPlanFixture{app: app, caller: caller, database: database, trace: trace, users: users, id: id, name: name, beforeCalls: beforeCalls, afterCalls: afterCalls, policyBuilds: policyBuilds}
}

func TestQueryPlanAuthorizationHooksAndExecutionPreparationAreOneBoundary(t *testing.T) {
	recorder := &queryPlanObservationRecorder{}
	fixture := newQueryPlanFixture(t, recorder)
	report, err := CallerExplainFindMany(context.Background(), fixture.caller, fixture.users, golem.Select[testUser](fixture.id))
	if err != nil {
		t.Fatal(err)
	}
	if report.Operation() != queryplan.OperationFindMany || report.Provider() != golem.SQLite || report.RootModelID() != fixture.users.Metadata().ModelID() {
		t.Fatalf("report header=%q/%q/%x", report.Operation(), report.Provider(), report.RootModelID())
	}
	if fixture.beforeCalls.Load() != 1 || fixture.afterCalls.Load() != 0 || fixture.policyBuilds.Load() != 2 {
		t.Fatalf("hook/policy counts before=%d after=%d policies=%d", fixture.beforeCalls.Load(), fixture.afterCalls.Load(), fixture.policyBuilds.Load())
	}
	statements := fixture.trace.snapshot()
	if len(statements) != 1 || !strings.HasPrefix(statements[0], "EXPLAIN QUERY PLAN SELECT ") {
		t.Fatalf("explain statements=%q", statements)
	}
	explainedSQL := strings.TrimPrefix(statements[0], "EXPLAIN QUERY PLAN ")
	if !strings.Contains(explainedSQL, " LIMIT ") {
		t.Fatalf("before-hook transform was not present in shared statement: %q", explainedSQL)
	}
	fixture.trace.reset()
	if _, err := CallerFindMany(context.Background(), fixture.caller, fixture.users, golem.Select[testUser](fixture.id)); err != nil {
		t.Fatal(err)
	}
	executed := fixture.trace.snapshot()
	if len(executed) != 1 || executed[0] != explainedSQL {
		t.Fatalf("shared prepare/render drifted:\nexplain=%q\nexecute=%q", explainedSQL, executed)
	}
	if fixture.beforeCalls.Load() != 2 || fixture.afterCalls.Load() != 1 || fixture.policyBuilds.Load() != 2 {
		t.Fatalf("post-execution hook/policy counts before=%d after=%d policies=%d", fixture.beforeCalls.Load(), fixture.afterCalls.Load(), fixture.policyBuilds.Load())
	}
	values := recorder.snapshot()
	if len(values) != 1 || values[0].Phase() != observe.PhaseFinish || values[0].Operation() != observe.OperationQueryPlanExplain || values[0].StatementCount() != 1 || values[0].AggregateCount() < 1 {
		t.Fatalf("query-plan observations=%#v", values)
	}
}

func TestQueryPlanProviderFailureIsClosedAndReturnsNoPartialReport(t *testing.T) {
	recorder := &queryPlanObservationRecorder{}
	fixture := newQueryPlanFixture(t, recorder)
	if err := fixture.database.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := CallerExplainCount(context.Background(), fixture.caller, fixture.users)
	if code, ok := queryplan.CodeOf(err); !ok || code != queryplan.ErrorUnavailable {
		t.Fatalf("closed provider error=%v code=(%q,%t)", err, code, ok)
	}
	if err.Error() != "query plan is unavailable" || report.FormatVersion() != 0 || len(report.Statements()) != 0 {
		t.Fatalf("provider failure leaked or returned partial report: report=%#v err=%q", report, err)
	}
	values := recorder.snapshot()
	if len(values) != 1 || values[0].Outcome() != observe.OutcomeRefused || values[0].Reason() != observe.ReasonProvider || values[0].StatementCount() != 0 || values[0].AggregateCount() != 0 {
		t.Fatalf("provider failure observation=%#v", values)
	}
}

func TestQueryPlanHookVetoAndTransformedFieldDenialExecuteZeroProviderStatements(t *testing.T) {
	fixture := newQueryPlanFixture(t, nil)
	for _, control := range []queryPlanHookControl{queryPlanHookVeto, queryPlanHookDeny} {
		fixture.trace.reset()
		ctx := context.WithValue(context.Background(), queryPlanHookControl(0), control)
		report, err := CallerExplainFindMany(ctx, fixture.caller, fixture.users, golem.Select[testUser](fixture.id))
		if err == nil || report.FormatVersion() != 0 || len(report.Statements()) != 0 {
			t.Fatalf("control=%d report=%#v err=%v", control, report, err)
		}
		if len(fixture.trace.snapshot()) != 0 {
			t.Fatalf("control=%d reached provider: %q", control, fixture.trace.snapshot())
		}
	}
}

func TestQueryPlanInvalidOriginalInputRunsNoHookAndNoProviderStatement(t *testing.T) {
	tests := []struct {
		name string
		run  func(queryPlanFixture) (queryplan.Report, error)
	}{
		{
			name: "findMany invalid original option",
			run: func(fixture queryPlanFixture) (queryplan.Report, error) {
				invalid := golem.GeneratedEqualField[testUser, golem.UUID](golem.FieldID{})
				return CallerExplainFindMany(context.Background(), fixture.caller, fixture.users, golem.Select[testUser](invalid))
			},
		},
		{
			name: "findFirst invalid original option",
			run: func(fixture queryPlanFixture) (queryplan.Report, error) {
				invalid := golem.GeneratedEqualField[testUser, golem.UUID](golem.FieldID{})
				return CallerExplainFindFirst(context.Background(), fixture.caller, fixture.users, golem.Select[testUser](invalid))
			},
		},
		{
			name: "findUnique foreign selector",
			run: func(fixture queryPlanFixture) (queryplan.Report, error) {
				foreign := golem.GeneratedUniqueSelectorValue[testUser](golem.ModelID{0xff}, golem.KeyID{0xfe}, golem.GeneratedSelectorComponent(golem.FieldID{0xfd}, golem.UUID{0xfc}))
				return CallerExplainFindUnique(context.Background(), fixture.caller, fixture.users, foreign)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newQueryPlanFixture(t, nil)
			report, err := test.run(fixture)
			if err == nil || report.FormatVersion() != 0 || len(report.Statements()) != 0 {
				t.Fatalf("invalid original input report=%#v err=%v", report, err)
			}
			if fixture.beforeCalls.Load() != 0 || fixture.afterCalls.Load() != 0 || len(fixture.trace.snapshot()) != 0 {
				t.Fatalf("invalid original input crossed boundary: before=%d after=%d statements=%q", fixture.beforeCalls.Load(), fixture.afterCalls.Load(), fixture.trace.snapshot())
			}
		})
	}
}

func TestQueryPlanReleasesMaxOpenOneConnectionBeforeBlockingOrPanickingObserver(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	recorder := &queryPlanObservationRecorder{entered: entered, release: release}
	fixture := newQueryPlanFixture(t, recorder)
	done := make(chan error, 1)
	go func() {
		_, err := CallerExplainCount(context.Background(), fixture.caller, fixture.users)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking observer was not reached")
	}
	if inUse := fixture.database.Stats().InUse; inUse != 0 {
		t.Fatalf("observer retained %d database connections", inUse)
	}
	pingContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.database.PingContext(pingContext); err != nil {
		t.Fatalf("MaxOpen=1 pool remained pinned during observation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	panicRecorder := &queryPlanObservationRecorder{panic: true}
	panicFixture := newQueryPlanFixture(t, panicRecorder)
	if _, err := CallerExplainCount(context.Background(), panicFixture.caller, panicFixture.users); err != nil {
		t.Fatalf("observer panic changed explain result: %v", err)
	}
	if inUse := panicFixture.database.Stats().InUse; inUse != 0 {
		t.Fatalf("panic observer retained %d database connections", inUse)
	}
}
