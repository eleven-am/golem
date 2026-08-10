package oracle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

const (
	aliceIDText             = "a1000000-0000-0000-0000-000000000001"
	bobIDText               = "a1000000-0000-0000-0000-000000000002"
	absentParentCommentText = "a3000000-0000-0000-0000-000000000003"
	hiddenPostCommentText   = "a3000000-0000-0000-0000-000000000004"
)

var postSeeds = []struct {
	id, title, author, budget string
	published                 bool
	views                     int64
	rating                    float64
	visibility                social.Visibility
}{
	{"a2000000-0000-0000-0000-000000000001", "a-private", aliceIDText, "100.10", false, 10, 1.5, social.VisibilityPrivate},
	{"a2000000-0000-0000-0000-000000000002", "b-public", aliceIDText, "200.20", true, 20, 2.5, social.VisibilityPublic},
	{"a2000000-0000-0000-0000-000000000003", "c-followers", aliceIDText, "300.30", true, 30, 3.5, social.VisibilityFollowers},
	{"a2000000-0000-0000-0000-000000000004", "d-public", bobIDText, "400.40", true, 40, 4.5, social.VisibilityPublic},
	{"a2000000-0000-0000-0000-000000000005", "e-hidden", bobIDText, "500.50", false, 50, 5.5, social.VisibilityPrivate},
	{"a2000000-0000-0000-0000-000000000006", "f-followers", bobIDText, "600.60", true, 60, 6.5, social.VisibilityFollowers},
}

type principalKey struct{}

type observed struct {
	Kind       observe.Kind
	Operation  observe.Operation
	Outcome    observe.Outcome
	Reason     observe.Reason
	Model      golem.ModelID
	Statements int
	Aggregate  int64
}

type observationTrace struct {
	mu     sync.Mutex
	values []observed
}

func (trace *observationTrace) ObserveGolem(_ context.Context, value observe.Observation) {
	recordObservationCoverage(value.Provider(), value.Operation())
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = append(trace.values, observed{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Reason: value.Reason(),
		Model: value.ModelID(), Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
	})
}

var observationCoverageMu sync.Mutex

func recordObservationCoverage(provider golem.Provider, operation observe.Operation) {
	path := os.Getenv("P8_OBSERVATION_COVERAGE_FILE")
	if path == "" {
		return
	}
	observationCoverageMu.Lock()
	defer observationCoverageMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic("open observation coverage sink")
	}
	if _, err := fmt.Fprintln(file, provider, operation); err != nil {
		_ = file.Close()
		panic("write observation coverage sink")
	}
	if err := file.Close(); err != nil {
		panic("close observation coverage sink")
	}
}

func (trace *observationTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = nil
}

func (trace *observationTrace) snapshot() []observed {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]observed(nil), trace.values...)
}

type auditTrace struct {
	mu     sync.Mutex
	values []golem.ScopedAuditRecord
}

func (trace *auditTrace) add(_ context.Context, record golem.ScopedAuditRecord) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = append(trace.values, record)
}

func (trace *auditTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = nil
}

func (trace *auditTrace) snapshot() []golem.ScopedAuditRecord {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]golem.ScopedAuditRecord(nil), trace.values...)
}

type graphError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions"`
}

type graphResponse struct {
	Data   map[string]any `json:"data"`
	Errors []graphError   `json:"errors"`
	Raw    string         `json:"-"`
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	db      *provider.Database
	app     *social.App[social.Principal]
	caller  *social.Caller[social.Principal]
	graph   *social.GraphQLServer
	handler http.Handler
	trace   *observationTrace
	audits  *auditTrace
	alice   golem.UUID
}

func TestP8ExternalOracleScenario(t *testing.T) {
	limits := golemruntime.AnalyticsLimits{}
	if os.Getenv("P8_ORACLE_SCENARIO") == "exact-scalar-limit" {
		limits.MaxProgrammaticGroups = 2
	}
	f := newFixture(t, limits)
	defer f.close()
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "analytics-cross-entry":
		f.analyticsCrossEntry()
	case "scoped-red-team":
		f.scopedRedTeam()
	case "exact-scalar-limit":
		f.exactScalarAndLimits()
	case "unsupported-relation":
		f.unsupportedRelation()
	default:
		t.Fatalf("unknown analytics oracle scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func newFixture(t *testing.T, limits golemruntime.AnalyticsLimits) *fixture {
	t.Helper()
	ctx := context.Background()
	database := openDatabase(t, ctx)
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	trace := &observationTrace{}
	audits := &auditTrace{}
	app, err := social.Open(ctx, social.Config[social.Principal]{
		Database: database, EventTransport: transport, Observer: trace, AnalyticsLimits: limits,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			if principal.Development {
				return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
			}
			return social.Actor{}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-analytics-alice" },
		ReportScopedQuery:   audits.add,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			t.Errorf("after-commit failure: %v", failure.Cause())
		},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	alice := mustUUID(t, aliceIDText)
	seed(t, ctx, app.System())
	assertDirectSQLSeed(t, database)
	principal := social.Principal{Development: true, DevUserID: alice}
	caller, err := app.ForPrincipal(ctx, principal)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(ctx context.Context) (social.Principal, bool) {
			value, ok := ctx.Value(principalKey{}).(social.Principal)
			return value, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("trusted GraphQL error: %v", err) },
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalKey{}, principal)
		graph.Handler().ServeHTTP(writer, request.WithContext(ctx))
	})
	trace.reset()
	audits.reset()
	return &fixture{t: t, ctx: ctx, db: database, app: app, caller: caller, graph: graph, handler: handler, trace: trace, audits: audits, alice: alice}
}

func openDatabase(t *testing.T, ctx context.Context) *provider.Database {
	t.Helper()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	default:
		t.Fatalf("unknown provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func (f *fixture) close() {
	if f.graph != nil {
		if err := f.graph.Shutdown(context.Background()); err != nil {
			f.t.Error(err)
		}
	}
	if f.db != nil {
		if err := f.db.Close(); err != nil {
			f.t.Error(err)
		}
	}
}

func seed(t *testing.T, ctx context.Context, system social.System[social.Principal]) {
	t.Helper()
	for _, user := range []struct{ id, handle string }{{aliceIDText, "alice"}, {bobIDText, "bob"}} {
		if _, err := system.Users.Create(ctx, social.Users.Create(
			social.Users.ID.Create(mustUUID(t, user.id)), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.handle+"@p8.invalid"),
		)); err != nil {
			t.Fatal(err)
		}
	}
	date := mustDate(t, "2026-08-09")
	clock := mustTime(t, "12:34:56")
	metadata := mustJSON(t, `{"language":"en","pinned":false}`)
	for _, post := range postSeeds {
		if _, err := system.Posts.Create(ctx, social.Posts.Create(
			social.Posts.ID.Create(mustUUID(t, post.id)), social.Posts.AuthorID.Create(mustUUID(t, post.author)),
			social.Posts.Title.Create(post.title), social.Posts.Body.Create("body-"+post.title), social.Posts.Published.Create(post.published),
			social.Posts.Views.Create(post.views), social.Posts.Rating.Create(post.rating), social.Posts.Budget.Create(mustDecimal(t, post.budget)),
			social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock), social.Posts.Metadata.Create(metadata),
			social.Posts.Visibility.Create(post.visibility), social.Posts.Topics.Create(golem.List[string]{"p8", "analytics"}),
		)); err != nil {
			t.Fatalf("seed post %s: %v", post.title, err)
		}
	}
	comments := []struct{ id, post, author, body string }{
		{"a3000000-0000-0000-0000-000000000001", postSeeds[1].id, aliceIDText, "b-one"},
		{"a3000000-0000-0000-0000-000000000002", postSeeds[1].id, bobIDText, "b-two"},
		{"a3000000-0000-0000-0000-000000000003", postSeeds[3].id, bobIDText, "d-one"},
		{"a3000000-0000-0000-0000-000000000004", postSeeds[4].id, bobIDText, "hidden-one"},
	}
	for _, comment := range comments {
		if _, err := system.Comments.Create(ctx, social.Comments.Create(
			social.Comments.ID.Create(mustUUID(t, comment.id)), social.Comments.PostID.Create(mustUUID(t, comment.post)),
			social.Comments.AuthorID.Create(mustUUID(t, comment.author)), social.Comments.Body.Create(comment.body),
		)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertDirectSQLSeed(t *testing.T, database *provider.Database) {
	t.Helper()
	rows, err := database.UnsafeSQLX().QueryxContext(context.Background(), `SELECT id,author_id,title FROM posts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, author, title string
		if err := rows.Scan(&id, &author, &title); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"/"+author+"/"+title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := make([]string, len(postSeeds))
	for index, post := range postSeeds {
		want[index] = post.id + "/" + post.author + "/" + post.title
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct SQL seed=%v want=%v", got, want)
	}
}

type aggregateTruth struct {
	Count     int64
	ViewsSum  string
	ViewsAvg  float64
	RatingSum float64
	RatingAvg float64
	BudgetSum string
	BudgetAvg string
}

var handAggregate = aggregateTruth{Count: 5, ViewsSum: "160", ViewsAvg: 32, RatingSum: 18.5, RatingAvg: 3.7, BudgetSum: "1601.6", BudgetAvg: "320.32"}

type groupTruth struct {
	Key   string
	Count int64
	Views string
}

var handPublishedGroups = []groupTruth{{Key: "false", Count: 1, Views: "10"}, {Key: "true", Count: 4, Views: "150"}}
var handAuthorGroups = []groupTruth{{Key: "alice", Count: 3, Views: "60"}, {Key: "bob", Count: 2, Views: "100"}}

func (f *fixture) analyticsCrossEntry() {
	wantAggregate := handAggregate
	wantPublished := handPublishedGroups
	wantAuthors := handAuthorGroups

	f.trace.reset()
	callerAggregate := f.callAggregate(f.caller.Posts.Aggregate)
	assertAggregate(f.t, "caller aggregate", callerAggregate, wantAggregate)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsAggregate, observe.OutcomeSuccess, observe.ReasonNone, 1, 1)

	var txAggregate aggregateTruth
	f.trace.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var err error
		txAggregate, err = f.callAggregateE(tx.Posts.Aggregate)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	assertAggregate(f.t, "CallerTx aggregate", txAggregate, wantAggregate)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsAggregate, observe.OutcomeSuccess, observe.ReasonNone, 1, 1)
	assertObservation(f.t, f.trace.snapshot(), observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	graphAggregate := f.graphAggregate()
	assertAggregate(f.t, "GraphQL aggregate", graphAggregate, wantAggregate)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsAggregate, observe.OutcomeSuccess, observe.ReasonNone, 1, 1)
	assertObservation(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	callerGroups := f.callPublishedGroups(f.caller.Posts.GroupBy)
	assertGroups(f.t, "caller group", callerGroups, wantPublished)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)

	var txGroups []groupTruth
	f.trace.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var err error
		txGroups, err = f.callPublishedGroupsE(tx.Posts.GroupBy)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	assertGroups(f.t, "CallerTx group", txGroups, wantPublished)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)
	assertObservation(f.t, f.trace.snapshot(), observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	graphGroups := f.graphPublishedGroups()
	assertGroups(f.t, "GraphQL group", graphGroups, wantPublished)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)
	assertObservation(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	callerRelations := f.callAuthorGroups(f.caller.Posts.RelationGroupBy)
	assertGroups(f.t, "caller relation group", callerRelations, wantAuthors)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsRelationGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)

	var txRelations []groupTruth
	f.trace.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var err error
		txRelations, err = f.callAuthorGroupsE(tx.Posts.RelationGroupBy)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	assertGroups(f.t, "CallerTx relation group", txRelations, wantAuthors)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsRelationGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)
	assertObservation(f.t, f.trace.snapshot(), observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	graphRelations := f.graphAuthorGroups()
	assertGroups(f.t, "GraphQL relation group", graphRelations, wantAuthors)
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsRelationGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 2)
	assertObservation(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)
}

type aggregateCall func(context.Context, golem.AggregateRequest[social.Post]) (golem.AggregateResult[social.Post], error)

func (f *fixture) callAggregate(call aggregateCall) aggregateTruth {
	got, err := f.callAggregateE(call)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *fixture) callAggregateE(call aggregateCall) (aggregateTruth, error) {
	count := social.Posts.CountAll()
	viewsSum, viewsAvg := social.Posts.Views.Sum(), social.Posts.Views.Avg()
	ratingSum, ratingAvg := social.Posts.Rating.Sum(), social.Posts.Rating.Avg()
	budgetSum, budgetAvg := social.Posts.Budget.Sum(), social.Posts.Budget.Avg()
	result, err := call(f.ctx, social.Posts.Aggregate(social.Posts.AggregateSelect(count, viewsSum, viewsAvg, ratingSum, ratingAvg, budgetSum, budgetAvg)))
	if err != nil {
		return aggregateTruth{}, err
	}
	return aggregateTruth{
		Count: mustAggregateValue(f.t, result, count), ViewsSum: mustAggregateValue(f.t, result, viewsSum).String(),
		ViewsAvg: mustAggregateValue(f.t, result, viewsAvg), RatingSum: mustAggregateValue(f.t, result, ratingSum),
		RatingAvg: mustAggregateValue(f.t, result, ratingAvg), BudgetSum: mustAggregateValue(f.t, result, budgetSum).String(),
		BudgetAvg: mustAggregateValue(f.t, result, budgetAvg).String(),
	}, nil
}

type groupCall func(context.Context, golem.GroupRequest[social.Post]) ([]golem.GroupRow[social.Post], error)

func (f *fixture) callPublishedGroups(call groupCall) []groupTruth {
	got, err := f.callPublishedGroupsE(call)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *fixture) callPublishedGroupsE(call groupCall) ([]groupTruth, error) {
	dimension, count, sum := social.Posts.Published.Dimension(), social.Posts.CountAll(), social.Posts.Views.Sum()
	rows, err := call(f.ctx, social.Posts.GroupBy(
		social.Posts.GroupDimensions(dimension), social.Posts.GroupMeasures(count, sum),
		social.Posts.GroupOrderBy(dimension.Asc()), social.Posts.GroupTake(100),
	))
	if err != nil {
		return nil, err
	}
	result := make([]groupTruth, len(rows))
	for index, row := range rows {
		result[index] = groupTruth{Key: fmt.Sprint(mustGroupValue(f.t, row, dimension)), Count: mustGroupValue(f.t, row, count), Views: mustGroupValue(f.t, row, sum).String()}
	}
	return result, nil
}

type relationGroupCall func(context.Context, golem.RelationGroupRequest[social.Post]) ([]golem.RelationGroupRow[social.Post], error)

func (f *fixture) callAuthorGroups(call relationGroupCall) []groupTruth {
	got, err := f.callAuthorGroupsE(call)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *fixture) callAuthorGroupsE(call relationGroupCall) ([]groupTruth, error) {
	dimension, count, sum := social.Posts.AuthorHandle, social.Posts.CountAll(), social.Posts.Views.Sum()
	rows, err := call(f.ctx, social.Posts.RelationGroupBy(
		social.Posts.RelationGroupDimensions(dimension), social.Posts.RelationGroupMeasures(count, sum),
		social.Posts.RelationGroupOrderBy(dimension.Asc()), social.Posts.RelationGroupTake(100),
	))
	if err != nil {
		return nil, err
	}
	result := make([]groupTruth, len(rows))
	for index, row := range rows {
		result[index] = groupTruth{Key: mustRelationGroupValue(f.t, row, dimension), Count: mustRelationGroupValue(f.t, row, count), Views: mustRelationGroupValue(f.t, row, sum).String()}
	}
	return result, nil
}

func (f *fixture) graphAggregate() aggregateTruth {
	response := f.graphql(`query { aggregatePosts { count sum { views rating budget } avg { views rating budget } } }`)
	assertNoGraphErrors(f.t, response)
	root := object(f.t, response.Data["aggregatePosts"])
	sum, avg := object(f.t, root["sum"]), object(f.t, root["avg"])
	return aggregateTruth{Count: parseInt64(f.t, root["count"]), ViewsSum: fmt.Sprint(sum["views"]), ViewsAvg: number(f.t, avg["views"]), RatingSum: number(f.t, sum["rating"]), RatingAvg: number(f.t, avg["rating"]), BudgetSum: fmt.Sprint(sum["budget"]), BudgetAvg: fmt.Sprint(avg["budget"])}
}

func (f *fixture) graphPublishedGroups() []groupTruth {
	response := f.graphql(`query { groupByPosts(by: [published], orderBy: [{key: {published: asc}}], take: 100) { key { published } count sum { views } } }`)
	assertNoGraphErrors(f.t, response)
	return graphGroups(f.t, response.Data["groupByPosts"], "published")
}

func (f *fixture) graphAuthorGroups() []groupTruth {
	response := f.graphql(`query { relationGroupByPosts(by: [authorHandle], orderBy: [{key: {authorHandle: asc}}], take: 100) { key { authorHandle } count sum { views } } }`)
	assertNoGraphErrors(f.t, response)
	return graphGroups(f.t, response.Data["relationGroupByPosts"], "authorHandle")
}

func graphGroups(t *testing.T, value any, key string) []groupTruth {
	t.Helper()
	values := list(t, value)
	result := make([]groupTruth, len(values))
	for index, item := range values {
		row := object(t, item)
		result[index] = groupTruth{Key: fmt.Sprint(object(t, row["key"])[key]), Count: parseInt64(t, row["count"]), Views: fmt.Sprint(object(t, row["sum"])["views"])}
	}
	return result
}

type scopedPair struct{ Title, Handle string }

func (f *fixture) scopedRedTeam() {
	f.assertScopedHopIndistinguishability()

	posts := social.Posts.Scope()
	authors := golem.InnerJoin(posts, social.Posts.Author)
	title, handle := social.Posts.Title.At(posts), social.Users.Handle.At(authors)
	query := golem.From(posts).Join(authors).Select(title, handle).OrderBy(title.Asc())

	f.trace.reset()
	f.audits.reset()
	rows, err := f.caller.Posts.Scoped(f.ctx, query)
	if err != nil {
		f.t.Fatal(err)
	}
	got := normalizeScopedPairs(f.t, rows, title, handle)
	want := []scopedPair{{"a-private", "alice"}, {"b-public", "alice"}, {"c-followers", "alice"}, {"d-public", "bob"}, {"f-followers", "bob"}}
	if !reflect.DeepEqual(got, want) {
		f.t.Fatalf("scoped authorized join=%v want=%v", got, want)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 5)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 5, false, 2, 1, []golem.ScopedJoinKind{golem.ScopedInnerJoin})

	var txRows []golem.ScopedRow
	f.trace.reset()
	f.audits.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var inner error
		txRows, inner = tx.Posts.Scoped(f.ctx, query)
		return inner
	}); err != nil {
		f.t.Fatal(err)
	}
	if got := normalizeScopedPairs(f.t, txRows, title, handle); !reflect.DeepEqual(got, want) {
		f.t.Fatalf("CallerTx scoped join=%v want=%v", got, want)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 5)
	assertObservation(f.t, f.trace.snapshot(), observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 5, false, 2, 1, []golem.ScopedJoinKind{golem.ScopedInnerJoin})

	posts = social.Posts.Scope()
	comments := golem.InnerJoin(posts, social.Posts.Comments)
	pairs := posts.Count()
	f.trace.reset()
	f.audits.reset()
	pairRows, err := f.caller.Posts.Scoped(f.ctx, golem.From(posts).Join(comments).Select(pairs))
	if err != nil || len(pairRows) != 1 {
		f.t.Fatalf("to-many scoped rows=%d error=%v", len(pairRows), err)
	}
	if count, ok := golem.ScopedValue(pairRows[0], pairs).Get(); !ok || count != 3 {
		f.t.Fatalf("authorized joined pair count=%d present=%t", count, ok)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 1)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 1, false, 2, 1, []golem.ScopedJoinKind{golem.ScopedInnerJoin})

	// The row set is invariant under adding a conditionally masked projection.
	posts = social.Posts.Scope()
	title = social.Posts.Title.At(posts)
	f.trace.reset()
	f.audits.reset()
	plainRows, err := f.caller.Posts.Scoped(f.ctx, golem.From(posts).Select(title).OrderBy(title.Asc()))
	if err != nil || len(plainRows) != 5 {
		f.t.Fatalf("plain scoped row cardinality=%d error=%v", len(plainRows), err)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 5)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 5, false, 1, 0, nil)

	// An undischargeable conditional field is represented as a mixed masked
	// column. Row visibility remains governed by the model predicate while the
	// field lens returns ReadNull for rows the principal may not inspect.
	posts = social.Posts.Scope()
	title = social.Posts.Title.At(posts)
	body := social.Posts.Body.At(posts)
	f.trace.reset()
	f.audits.reset()
	rows, err = f.caller.Posts.Scoped(f.ctx, golem.From(posts).Select(title, body).OrderBy(title.Asc()))
	if err != nil || len(rows) != 5 {
		f.t.Fatalf("masked conditional field rows=%d error=%v", len(rows), err)
	}
	wantStates := map[string]golem.ReadState{
		"a-private":   golem.ReadPresent,
		"b-public":    golem.ReadPresent,
		"c-followers": golem.ReadPresent,
		"d-public":    golem.ReadNull,
		"f-followers": golem.ReadNull,
	}
	for _, row := range rows {
		name := mustScopedValue(f.t, row, title)
		if got, want := golem.ScopedValue(row, body).State(), wantStates[name]; got != want {
			f.t.Fatalf("masked conditional field %s state=%v want=%v", name, got, want)
		}
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 5)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 5, false, 1, 0, nil)

	// The same field is legal once the query proves ownership.
	posts = social.Posts.Scope()
	authorID, body := social.Posts.AuthorID.At(posts), social.Posts.Body.At(posts)
	f.trace.reset()
	f.audits.reset()
	owned, err := f.caller.Posts.Scoped(f.ctx, golem.From(posts).Where(authorID.Eq(f.alice)).Select(body).OrderBy(body.Asc()))
	if err != nil || len(owned) != 3 {
		f.t.Fatalf("discharged scoped body rows=%d error=%v", len(owned), err)
	}
	var bodies []string
	for _, row := range owned {
		bodies = append(bodies, mustScopedValue(f.t, row, body))
	}
	if want := []string{"body-a-private", "body-b-public", "body-c-followers"}; !reflect.DeepEqual(bodies, want) {
		f.t.Fatalf("owned scoped bodies=%v want=%v", bodies, want)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeSuccess, observe.ReasonNone, 1, 3)

	// Mixed query identities and programmatic resource excess are rejected
	// before the provider sees a statement, with an immutable refusal audit.
	first, second := social.Posts.Scope(), social.Posts.Scope()
	foreignTitle := social.Posts.Title.At(second)
	f.trace.reset()
	f.audits.reset()
	if rows, err := f.caller.Posts.Scoped(f.ctx, golem.From(first).Select(foreignTitle)); err == nil || rows != nil {
		f.t.Fatalf("mixed-root scoped rows=%d error=%v", len(rows), err)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeRefused, observe.ReasonInvalidInput, 0, 0)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeRefused, 0, false, 0, 0, nil)

	posts = social.Posts.Scope()
	title = social.Posts.Title.At(posts)
	f.trace.reset()
	f.audits.reset()
	if rows, err := f.caller.Posts.Scoped(f.ctx, golem.From(posts).Select(title).Take(100_001)); err == nil || rows != nil {
		f.t.Fatalf("over-limit scoped rows=%d error=%v", len(rows), err)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindScopedRead, observe.OperationScopedRead, observe.OutcomeRefused, observe.ReasonInvalidInput, 0, 0)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeRefused, 0, false, 1, 0, nil)
}

func (f *fixture) assertScopedHopIndistinguishability() {
	hiddenID := mustUUID(f.t, hiddenPostCommentText)
	absentID := mustUUID(f.t, absentParentCommentText)
	projection := social.Comments.Select(
		social.Comments.ID,
		social.Comments.Post.Select(social.Posts.ID),
		social.Comments.Parent.Select(social.Comments.ID),
	)
	assertRows := func(label string, hidden, absent golem.Row[social.Comment]) {
		f.t.Helper()
		if got, ok := golem.Value(hidden, social.Comments.ID).Get(); !ok || got != hiddenID {
			f.t.Fatalf("%s hidden comment identity=%v present=%t", label, got, ok)
		}
		if got := golem.One(hidden, social.Comments.Post.ToOne); !got.IsNull() {
			f.t.Fatalf("%s hidden-but-existing post state=%v want=null", label, got.State())
		}
		if got, ok := golem.Value(absent, social.Comments.ID).Get(); !ok || got != absentID {
			f.t.Fatalf("%s absent-parent comment identity=%v present=%t", label, got, ok)
		}
		if got := golem.One(absent, social.Comments.Parent.ToOne); !got.IsNull() {
			f.t.Fatalf("%s absent parent state=%v want=null", label, got.State())
		}
	}

	hidden, err := f.caller.Comments.FindUnique(f.ctx, social.Comments.ByID.Value(hiddenID), projection)
	if err != nil {
		f.t.Fatal(err)
	}
	absent, err := f.caller.Comments.FindUnique(f.ctx, social.Comments.ByID.Value(absentID), projection)
	if err != nil {
		f.t.Fatal(err)
	}
	assertRows("Caller", hidden, absent)

	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		txHidden, inner := tx.Comments.FindUnique(f.ctx, social.Comments.ByID.Value(hiddenID), projection)
		if inner != nil {
			return inner
		}
		txAbsent, inner := tx.Comments.FindUnique(f.ctx, social.Comments.ByID.Value(absentID), projection)
		if inner != nil {
			return inner
		}
		assertRows("CallerTx", txHidden, txAbsent)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}

	response := f.graphql(fmt.Sprintf(`query {
  hidden: comment(where: {ID: %q}) { id post { id } }
  absent: comment(where: {ID: %q}) { id parent { id } }
}`, hiddenPostCommentText, absentParentCommentText))
	assertNoGraphErrors(f.t, response)
	hiddenGraph := object(f.t, response.Data["hidden"])
	absentGraph := object(f.t, response.Data["absent"])
	if fmt.Sprint(hiddenGraph["id"]) != hiddenPostCommentText || hiddenGraph["post"] != nil {
		f.t.Fatalf("GraphQL hidden-but-existing target=%v", hiddenGraph)
	}
	if fmt.Sprint(absentGraph["id"]) != absentParentCommentText || absentGraph["parent"] != nil {
		f.t.Fatalf("GraphQL absent target=%v", absentGraph)
	}

	assertDirectSQLHopTruth(f.t, f.db)
	f.assertScopedHiddenPost(hiddenID)
	f.assertScopedAbsentParent(absentID)
}

func (f *fixture) assertScopedHiddenPost(hiddenID golem.UUID) {
	roots := social.Posts.Scope()
	authors := golem.InnerJoin(roots, social.Posts.Author)
	comments := golem.InnerJoin(authors, social.Users.Comments)
	posts := golem.LeftJoin(comments, social.Comments.Post)
	rootID := social.Posts.ID.At(roots)
	commentID := social.Comments.ID.At(comments)
	postID := social.Posts.ID.At(posts)
	query := golem.From(roots).
		Join(authors).
		Join(comments).
		Join(posts).
		Where(golem.AndScoped(rootID.Eq(mustUUID(f.t, postSeeds[3].id)), commentID.Eq(hiddenID))).
		Select(rootID, commentID, postID).
		Take(1)

	assertRows := func(label string, rows []golem.ScopedRow) {
		f.t.Helper()
		if len(rows) != 1 || mustScopedValue(f.t, rows[0], rootID) != mustUUID(f.t, postSeeds[3].id) || mustScopedValue(f.t, rows[0], commentID) != hiddenID {
			got := make([]string, 0, len(rows))
			for _, row := range rows {
				got = append(got, fmt.Sprintf("%v/%v", golem.ScopedValue(row, rootID), golem.ScopedValue(row, commentID)))
			}
			f.t.Fatalf("%s hidden-post scoped rows=%d values=%v", label, len(rows), got)
		}
		if state := golem.ScopedValue(rows[0], postID).State(); state != golem.ReadNull {
			f.t.Fatalf("%s hidden-post target state=%v want=%v", label, state, golem.ReadNull)
		}
	}

	f.trace.reset()
	f.audits.reset()
	rows, err := f.caller.Posts.Scoped(f.ctx, query)
	if err != nil {
		f.t.Fatal(err)
	}
	assertRows("Caller", rows)
	assertScopedObservation(f.t, f.trace.snapshot(), social.GolemGeneratedPostDescriptor.Metadata().ModelID(), 1, 1)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 1, false, 3, 3, []golem.ScopedJoinKind{golem.ScopedInnerJoin, golem.ScopedInnerJoin, golem.ScopedLeftJoin})

	f.trace.reset()
	f.audits.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var inner error
		rows, inner = tx.Posts.Scoped(f.ctx, query)
		return inner
	}); err != nil {
		f.t.Fatal(err)
	}
	assertRows("CallerTx", rows)
	assertScopedObservation(f.t, f.trace.snapshot(), social.GolemGeneratedPostDescriptor.Metadata().ModelID(), 1, 1)
	assertObservation(f.t, f.trace.snapshot(), observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 1, false, 3, 3, []golem.ScopedJoinKind{golem.ScopedInnerJoin, golem.ScopedInnerJoin, golem.ScopedLeftJoin})
}

func (f *fixture) assertScopedAbsentParent(absentID golem.UUID) {
	roots := social.Posts.Scope()
	authors := golem.InnerJoin(roots, social.Posts.Author)
	comments := golem.InnerJoin(authors, social.Users.Comments)
	parents := golem.LeftJoin(comments, social.Comments.Parent)
	rootID := social.Posts.ID.At(roots)
	commentID := social.Comments.ID.At(comments)
	parentID := social.Comments.ID.At(parents)
	query := golem.From(roots).
		Join(authors).
		Join(comments).
		Join(parents).
		Where(golem.AndScoped(rootID.Eq(mustUUID(f.t, postSeeds[3].id)), commentID.Eq(absentID))).
		Select(rootID, commentID, parentID).
		Take(1)

	f.trace.reset()
	f.audits.reset()
	rows, err := f.caller.Posts.Scoped(f.ctx, query)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(rows) != 1 || mustScopedValue(f.t, rows[0], commentID) != absentID || golem.ScopedValue(rows[0], parentID).State() != golem.ReadNull {
		f.t.Fatalf("absent-parent scoped rows=%d", len(rows))
	}
	assertScopedObservation(f.t, f.trace.snapshot(), social.GolemGeneratedPostDescriptor.Metadata().ModelID(), 1, 1)
	assertScopedAudit(f.t, f.audits.snapshot(), golem.ScopedOutcomeSucceeded, 1, false, 3, 3, []golem.ScopedJoinKind{golem.ScopedInnerJoin, golem.ScopedInnerJoin, golem.ScopedLeftJoin})
}

func assertDirectSQLHopTruth(t *testing.T, database *provider.Database) {
	t.Helper()
	query := database.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM comments AS c JOIN posts AS p ON p.id=c.post_id WHERE c.id=? AND p.id=?`)
	var hiddenExists int64
	if err := database.UnsafeSQLX().QueryRowxContext(context.Background(), query, hiddenPostCommentText, postSeeds[4].id).Scan(&hiddenExists); err != nil {
		t.Fatal(err)
	}
	if hiddenExists != 1 {
		t.Fatalf("direct SQL hidden target count=%d want=1", hiddenExists)
	}
	query = database.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM comments WHERE id=? AND parent_id IS NULL`)
	var absentCount int64
	if err := database.UnsafeSQLX().QueryRowxContext(context.Background(), query, absentParentCommentText).Scan(&absentCount); err != nil {
		t.Fatal(err)
	}
	if absentCount != 1 {
		t.Fatalf("direct SQL absent target count=%d want=1", absentCount)
	}
}

func assertScopedObservation(t *testing.T, values []observed, model golem.ModelID, statements int, aggregate int64) {
	t.Helper()
	var matches []observed
	for _, value := range values {
		if value.Kind == observe.KindScopedRead && value.Operation == observe.OperationScopedRead {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("scoped observation matches=%+v all=%+v", matches, values)
	}
	got := matches[0]
	if got.Outcome != observe.OutcomeSuccess || got.Reason != observe.ReasonNone || got.Model != model || got.Statements != statements || got.Aggregate != aggregate {
		t.Fatalf("scoped observation=%+v want model=%s statements=%d aggregate=%d", got, model, statements, aggregate)
	}
}

func (f *fixture) exactScalarAndLimits() {
	assertAggregate(f.t, "exact scalar caller", f.callAggregate(f.caller.Posts.Aggregate), handAggregate)
	assertAggregate(f.t, "exact scalar GraphQL", f.graphAggregate(), handAggregate)

	dimension := social.Posts.Visibility.Dimension()
	f.trace.reset()
	request := social.Posts.GroupBy(
		social.Posts.GroupDimensions(dimension), social.Posts.GroupMeasures(social.Posts.CountAll()),
		social.Posts.GroupOrderBy(dimension.Asc()), social.Posts.GroupTake(3),
	)
	if rows, err := f.caller.Posts.GroupBy(f.ctx, request); err == nil || rows != nil {
		f.t.Fatalf("programmatic max group rows=%d error=%v", len(rows), err)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeRefused, observe.ReasonInvalidInput, 0, 0)

	f.trace.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, err := tx.Posts.GroupBy(f.ctx, request)
		return err
	}); err == nil {
		f.t.Fatal("CallerTx programmatic max group was accepted")
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeRefused, observe.ReasonInvalidInput, 0, 0)

	f.trace.reset()
	accepted := f.graphql(`query { groupByPosts(by: [visibility], orderBy: [{key: {visibility: asc}}], take: 3) { key { visibility } count } }`)
	assertNoGraphErrors(f.t, accepted)
	if got := len(list(f.t, accepted.Data["groupByPosts"])); got != 3 {
		f.t.Fatalf("GraphQL explicit groups=%d want=3", got)
	}
	assertObservation(f.t, f.trace.snapshot(), observe.KindAnalytics, observe.OperationAnalyticsGroupBy, observe.OutcomeSuccess, observe.ReasonNone, 1, 3)
	assertObservation(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, observe.OutcomeSuccess, observe.ReasonNone, 1, 0)

	f.trace.reset()
	refused := f.graphql(`query { groupByPosts(by: [visibility], take: 101) { key { visibility } count } }`)
	assertGraphCode(f.t, refused, "BAD_USER_INPUT")
	assertObservation(f.t, f.trace.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, observe.OutcomeRefused, observe.ReasonInvalidInput, 0, 0)
}

func (f *fixture) unsupportedRelation() {
	// The Go surface is closed: neither Caller nor CallerTx can construct a
	// relation-group request from the generated to-many Comments handle.
	if _, ok := any(social.Posts.Comments).(golem.RelationGroupDimension[social.Post]); ok {
		f.t.Fatal("to-many Comments unexpectedly satisfies RelationGroupDimension")
	}

	f.trace.reset()
	invalid := f.graphql(`query { relationGroupByPosts(by: [comments]) { count } }`)
	if len(invalid.Errors) != 1 || fmt.Sprint(invalid.Errors[0].Extensions["code"]) != "GRAPHQL_VALIDATION_FAILED" {
		f.t.Fatalf("unsupported GraphQL relation response=%s", invalid.Raw)
	}
	for _, value := range f.trace.snapshot() {
		if value.Kind == observe.KindAnalytics {
			f.t.Fatalf("unsupported GraphQL relation reached analytics execution: %+v", f.trace.snapshot())
		}
	}

	// Both generated executable entry points still accept a valid configured
	// to-one relation dimension, proving refusal is selective rather than a
	// missing analytics subsystem.
	assertGroups(f.t, "configured caller relation", f.callAuthorGroups(f.caller.Posts.RelationGroupBy), handAuthorGroups)
	var tx []groupTruth
	if err := f.caller.Transaction(f.ctx, func(callerTx *social.CallerTx[social.Principal]) error {
		var err error
		tx, err = f.callAuthorGroupsE(callerTx.Posts.RelationGroupBy)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	assertGroups(f.t, "configured CallerTx relation", tx, handAuthorGroups)
}

func normalizeScopedPairs(t *testing.T, rows []golem.ScopedRow, title golem.ScopedResult[string], handle golem.ScopedResult[string]) []scopedPair {
	t.Helper()
	result := make([]scopedPair, len(rows))
	for index, row := range rows {
		result[index] = scopedPair{Title: mustScopedValue(t, row, title), Handle: mustScopedValue(t, row, handle)}
	}
	return result
}

func assertScopedAudit(t *testing.T, values []golem.ScopedAuditRecord, outcome golem.ScopedOutcome, rows int64, system bool, models, relations int, joins []golem.ScopedJoinKind) {
	t.Helper()
	if len(values) != 1 {
		t.Fatalf("scoped audits=%d want=1", len(values))
	}
	record := values[0]
	if record.Outcome() != outcome || record.RowCount() != rows || record.IsSystem() != system || len(record.Models()) != models || len(record.Relations()) != relations || !reflect.DeepEqual(record.JoinKinds(), joins) {
		t.Fatalf("scoped audit outcome=%v rows=%d system=%t models=%d relations=%d joins=%v", record.Outcome(), record.RowCount(), record.IsSystem(), len(record.Models()), len(record.Relations()), record.JoinKinds())
	}
	if record.PrincipalAuditID() != "p8-analytics-alice" || record.ExecutionID() == 0 || record.Provider() == "" || record.Duration() < 0 {
		t.Fatalf("scoped audit identity=%q/%d provider=%q duration=%v", record.PrincipalAuditID(), record.ExecutionID(), record.Provider(), record.Duration())
	}
	modelsCopy := record.Models()
	if len(modelsCopy) != 0 {
		modelsCopy[0] = golem.ModelID{}
		if reflect.DeepEqual(modelsCopy, record.Models()) {
			t.Fatal("scoped audit model inventory aliases internal storage")
		}
	}
	if outcome == golem.ScopedOutcomeSucceeded && record.SQLFingerprint() == (golem.SchemaDigest{}) {
		t.Fatal("successful scoped audit has empty SQL fingerprint")
	}
}

func assertObservation(t *testing.T, values []observed, kind observe.Kind, operation observe.Operation, outcome observe.Outcome, reason observe.Reason, statements int, aggregate int64) {
	t.Helper()
	var matches []observed
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("observation %s/%s matches=%+v all=%+v", kind, operation, matches, values)
	}
	got := matches[0]
	if got.Outcome != outcome || got.Reason != reason || got.Statements != statements || got.Aggregate != aggregate || got.Model != social.GolemGeneratedPostDescriptor.Metadata().ModelID() && kind != observe.KindGraphQL && kind != observe.KindTransaction {
		t.Fatalf("observation %s/%s=%+v want outcome=%s reason=%s statements=%d aggregate=%d all=%+v", kind, operation, got, outcome, reason, statements, aggregate, values)
	}
}

func assertAggregate(t *testing.T, label string, got, want aggregateTruth) {
	t.Helper()
	if got.Count != want.Count || got.ViewsSum != want.ViewsSum || math.Abs(got.ViewsAvg-want.ViewsAvg) > 1e-12 || math.Abs(got.RatingSum-want.RatingSum) > 1e-12 || math.Abs(got.RatingAvg-want.RatingAvg) > 1e-12 || got.BudgetSum != want.BudgetSum || got.BudgetAvg != want.BudgetAvg {
		t.Fatalf("%s=%+v want=%+v", label, got, want)
	}
}

func assertGroups(t *testing.T, label string, got, want []groupTruth) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s=%v want=%v", label, got, want)
	}
}

func mustAggregateValue[M, V any](t *testing.T, result golem.AggregateResult[M], measure golem.Measure[M, V]) V {
	t.Helper()
	value, ok := golem.AggregateValue(result, measure).Get()
	if !ok {
		t.Fatal("aggregate value absent")
	}
	return value
}

func mustGroupValue[M, V any](t *testing.T, row golem.GroupRow[M], cell golem.GroupCell[M, V]) V {
	t.Helper()
	value, ok := golem.GroupValue(row, cell).Get()
	if !ok {
		t.Fatal("group value absent")
	}
	return value
}

func mustRelationGroupValue[M, V any](t *testing.T, row golem.RelationGroupRow[M], cell golem.RelationGroupCell[M, V]) V {
	t.Helper()
	value, ok := golem.RelationGroupValue(row, cell).Get()
	if !ok {
		t.Fatal("relation group value absent")
	}
	return value
}

func mustScopedValue[V any](t *testing.T, row golem.ScopedRow, expression golem.ScopedResult[V]) V {
	t.Helper()
	value, ok := golem.ScopedValue(row, expression).Get()
	if !ok {
		t.Fatal("scoped value absent")
	}
	return value
}

func (f *fixture) graphql(document string) graphResponse {
	f.t.Helper()
	payload, err := json.Marshal(map[string]any{"query": document})
	if err != nil {
		f.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		f.t.Fatalf("decode GraphQL response %q: %v", recorder.Body.String(), err)
	}
	response.Raw = recorder.Body.String()
	return response
}

func assertNoGraphErrors(t *testing.T, response graphResponse) {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors=%+v response=%s", response.Errors, response.Raw)
	}
}

func assertGraphCode(t *testing.T, response graphResponse, code string) {
	t.Helper()
	if len(response.Errors) != 1 || fmt.Sprint(response.Errors[0].Extensions["code"]) != code {
		t.Fatalf("GraphQL code=%q response=%s", code, response.Raw)
	}
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value %#v is not an object", value)
	}
	return result
}

func list(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not a list", value)
	}
	return result
}

func number(t *testing.T, value any) float64 {
	t.Helper()
	result, ok := value.(float64)
	if !ok {
		t.Fatalf("value %#v is not a number", value)
	}
	return result
}

func parseInt64(t *testing.T, value any) int64 {
	t.Helper()
	result, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDecimal(t *testing.T, value string) golem.Decimal {
	t.Helper()
	result, err := golem.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDate(t *testing.T, value string) golem.Date {
	t.Helper()
	result, err := golem.ParseDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustTime(t *testing.T, value string) golem.Time {
	t.Helper()
	result, err := golem.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustJSON(t *testing.T, value string) golem.JSON[any] {
	t.Helper()
	result, err := golem.NewJSONDocument[any]([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return result
}
