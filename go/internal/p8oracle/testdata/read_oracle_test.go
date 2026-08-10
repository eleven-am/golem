package oracle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

const (
	aliceIDText      = "81000000-0000-0000-0000-000000000001"
	bobIDText        = "81000000-0000-0000-0000-000000000002"
	alicePrivateText = "82000000-0000-0000-0000-000000000001"
	bobPublicText    = "82000000-0000-0000-0000-000000000002"
	bobPrivateText   = "82000000-0000-0000-0000-000000000003"
	alicePublicText  = "82000000-0000-0000-0000-000000000004"
	bobSecondText    = "82000000-0000-0000-0000-000000000005"
	bobCommentText   = "83000000-0000-0000-0000-000000000001"
	protectedCanary  = "P8_PROTECTED_READ_CANARY_7ea8"
)

type fixture struct {
	t          *testing.T
	ctx        context.Context
	database   *provider.Database
	app        *social.App[social.Principal]
	caller     *social.Caller[social.Principal]
	graph      *social.GraphQLServer
	handler    http.Handler
	principal  social.Principal
	aliceID    golem.UUID
	bobID      golem.UUID
	privateBob golem.UUID
	trustedMu  sync.Mutex
	trusted    []string
	observed   *observationTrace
}

type oraclePost struct {
	ID        string  `json:"id"`
	AuthorID  string  `json:"authorID"`
	Title     string  `json:"title"`
	Published bool    `json:"published"`
	Body      *string `json:"body"`
	BodyState string  `json:"bodyState"`
}

type oracleUser struct {
	ID         string  `json:"id"`
	Handle     string  `json:"handle"`
	Email      *string `json:"email"`
	EmailState string  `json:"emailState"`
}

type relationSnapshot struct {
	Post         oraclePost `json:"post"`
	Author       oracleUser `json:"author"`
	CommentCount int64      `json:"commentCount"`
	Comments     []string   `json:"comments"`
}

type sqlPost struct {
	ID        string `db:"id"`
	AuthorID  string `db:"author_id"`
	Title     string `db:"title"`
	Body      string `db:"body"`
	Published bool   `db:"published"`
}

type sqlUser struct {
	ID     string `db:"id"`
	Handle string `db:"handle"`
	Email  string `db:"email"`
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphError    `json:"errors"`
}

type graphError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions"`
}

type observedOperation struct {
	Kind       observe.Kind
	Model      golem.ModelID
	Operation  observe.Operation
	Outcome    observe.Outcome
	Reason     observe.Reason
	Statements int
	Aggregate  int64
}

type observationTrace struct {
	mu     sync.Mutex
	values []observedOperation
}

func (trace *observationTrace) ObserveGolem(_ context.Context, value observe.Observation) {
	recordObservationCoverage(value.Provider(), value.Operation())
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = append(trace.values, observedOperation{
		Kind: value.Kind(), Model: value.ModelID(), Operation: value.Operation(), Outcome: value.Outcome(),
		Reason: value.Reason(), Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
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

func (trace *observationTrace) snapshot() []observedOperation {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]observedOperation(nil), trace.values...)
}

type readTrace struct {
	mu     sync.Mutex
	phases []social.PostReadHookPhase
	rows   []int
}

type displayCodeLoad struct {
	Keys   []string
	Prefix string
}

type displayCodeTrace struct {
	mu    sync.Mutex
	loads []displayCodeLoad
}

type rejectingReadHook struct{ cause error }

func (hook rejectingReadHook) ObservePostReadHook(context.Context, social.PostReadHookPhase, []golem.Row[social.Post]) error {
	return hook.cause
}

func (trace *displayCodeTrace) ObservePostDisplayCodeLoad(_ context.Context, keys []golem.UUID, arguments social.DisplayCodeArgs) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	values := make([]string, len(keys))
	for index, key := range keys {
		values[index] = key.String()
	}
	trace.loads = append(trace.loads, displayCodeLoad{Keys: values, Prefix: arguments.Prefix})
	return nil
}

func (trace *displayCodeTrace) snapshot() []displayCodeLoad {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	result := make([]displayCodeLoad, len(trace.loads))
	for index, load := range trace.loads {
		result[index] = displayCodeLoad{Keys: append([]string(nil), load.Keys...), Prefix: load.Prefix}
	}
	return result
}

func (trace *readTrace) ObservePostReadHook(_ context.Context, phase social.PostReadHookPhase, rows []golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases = append(trace.phases, phase)
	trace.rows = append(trace.rows, len(rows))
	return nil
}

func (trace *readTrace) snapshot() ([]social.PostReadHookPhase, []int) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]social.PostReadHookPhase(nil), trace.phases...), append([]int(nil), trace.rows...)
}

func TestP8ExternalOracleScenario(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "cross-entry-point":
		f.crossEntryPoint()
	case "mask-error-pagination":
		f.maskErrorPagination()
	case "custom-capability":
		f.customCapability()
	case "caller-transaction":
		f.callerTransaction()
	default:
		t.Fatalf("unknown P8 oracle scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	database := openDatabase(t, ctx)
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	observed := &observationTrace{}
	app, err := social.Open(ctx, social.Config[social.Principal]{
		Database:       database,
		EventTransport: transport,
		Observer:       observed,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			if !principal.Development {
				return social.Actor{}, nil
			}
			return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-read-oracle-principal" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	aliceID := mustUUID(t, aliceIDText)
	bobID := mustUUID(t, bobIDText)
	seed(t, ctx, app.System(), aliceID, bobID)
	principal := social.Principal{Development: true, DevUserID: aliceID}
	caller, err := app.ForPrincipal(ctx, principal)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result := &fixture{
		t: t, ctx: ctx, database: database, app: app, caller: caller,
		principal: principal, aliceID: aliceID, bobID: bobID,
		privateBob: mustUUID(t, bobPrivateText), observed: observed,
	}
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) { return principal, true },
		ReportInternalError: func(_ context.Context, err error) {
			result.trustedMu.Lock()
			result.trusted = append(result.trusted, err.Error())
			result.trustedMu.Unlock()
		},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result.graph, result.handler = graph, graph.Handler()
	return result
}

func (f *fixture) close() {
	if trusted := f.takeTrustedErrors(); len(trusted) != 0 {
		f.t.Errorf("unexpected trusted GraphQL errors: %v", trusted)
	}
	if err := f.graph.Shutdown(context.Background()); err != nil {
		f.t.Error(err)
	}
	if err := f.database.Close(); err != nil {
		f.t.Error(err)
	}
}

func (f *fixture) takeTrustedErrors() []string {
	f.trustedMu.Lock()
	defer f.trustedMu.Unlock()
	result := append([]string(nil), f.trusted...)
	f.trusted = nil
	return result
}

func openDatabase(t *testing.T, ctx context.Context) *provider.Database {
	t.Helper()
	var (
		database *provider.Database
		err      error
	)
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

func seed(t *testing.T, ctx context.Context, system social.System[social.Principal], aliceID, bobID golem.UUID) {
	t.Helper()
	for _, item := range []struct {
		id, handle, email string
	}{
		{aliceIDText, "alice", "alice@p8.test"},
		{bobIDText, "bob", protectedCanary + "@p8.test"},
	} {
		if _, err := system.Users.Create(ctx, social.Users.Create(
			social.Users.ID.Create(mustUUID(t, item.id)),
			social.Users.Handle.Create(item.handle),
			social.Users.Email.Create(item.email),
		)); err != nil {
			t.Fatalf("seed user %s: %v", item.handle, err)
		}
	}
	posts := []struct {
		id, title, body string
		author          golem.UUID
		published       bool
	}{
		{alicePrivateText, "alpha-owner-private", "owner private body", aliceID, false},
		{bobPublicText, "bravo-public", protectedCanary + " published body", bobID, true},
		{bobPrivateText, "charlie-" + protectedCanary, protectedCanary + " private body", bobID, false},
		{alicePublicText, "delta-owner-public", "owner public body", aliceID, true},
		{bobSecondText, "echo-public", protectedCanary + " second published body", bobID, true},
	}
	date := mustDate(t, "2026-08-09")
	clock := mustTime(t, "12:34:56")
	metadata := mustJSON(t, `{"language":"en","pinned":false}`)
	for _, item := range posts {
		if _, err := system.Posts.Create(ctx, social.Posts.Create(
			social.Posts.ID.Create(mustUUID(t, item.id)), social.Posts.AuthorID.Create(item.author),
			social.Posts.Title.Create(item.title), social.Posts.Body.Create(item.body),
			social.Posts.Published.Create(item.published), social.Posts.LiveDate.Create(date),
			social.Posts.LiveTime.Create(clock), social.Posts.Metadata.Create(metadata),
			social.Posts.Visibility.Create(social.VisibilityPublic),
			social.Posts.Topics.Create(golem.List[string]{"p8", "oracle"}),
		)); err != nil {
			t.Fatalf("seed post %s: %v", item.id, err)
		}
	}
	if _, err := system.Comments.Create(ctx, social.Comments.Create(
		social.Comments.ID.Create(mustUUID(t, bobCommentText)),
		social.Comments.PostID.Create(mustUUID(t, bobPublicText)),
		social.Comments.AuthorID.Create(bobID),
		social.Comments.Body.Create("authorized relation comment"),
	)); err != nil {
		t.Fatalf("seed relation comment: %v", err)
	}
}

func (f *fixture) crossEntryPoint() {
	expected := f.expectedVisiblePosts()
	callerRows, err := f.caller.Posts.FindMany(f.ctx,
		social.Posts.OrderBy(social.Posts.ID.Asc()),
		social.Posts.Take(50),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller", normalizePostRows(callerRows), expected)

	var transactionRows []golem.Row[social.Post]
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var findErr error
		transactionRows, findErr = tx.Posts.FindMany(f.ctx,
			social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(50),
			social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
		)
		return findErr
	}); err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller transaction", normalizePostRows(transactionRows), expected)

	ordinary := f.graphQL(`query {
  posts(orderBy: [{id: asc}], take: 50) { id authorID title body published }
}`)
	assertNoGraphErrors(f.t, ordinary)
	assertPosts(f.t, "generated GraphQL", graphPosts(f.t, ordinary, "posts"), expected)

	customRows, err := social.SearchPosts(f.ctx, f.caller, social.SearchPostsArgs{Where: golem.All[social.Post](), Take: 50})
	if err != nil {
		f.t.Fatal(err)
	}
	customExpected := f.expectedCustomPosts()
	assertPosts(f.t, "custom direct", normalizePostRows(customRows), customExpected)
	custom := f.graphQL(`query {
  searchPosts(where: {all: true}, take: 50) { id authorID title body published }
}`)
	assertNoGraphErrors(f.t, custom)
	assertPosts(f.t, "custom GraphQL", graphPosts(f.t, custom, "searchPosts"), customExpected)

	encoded, _ := json.Marshal(ordinary)
	if bytes.Contains(encoded, []byte("charlie-"+protectedCanary)) || bytes.Contains(encoded, []byte(protectedCanary+" private body")) {
		f.t.Fatal("generated GraphQL disclosed an invisible row canary")
	}
	f.assertSharedReadClassification(len(expected))
	f.assertSuccessfulReadOperations(expected)
	f.assertLoaderIsolation()
}

func (f *fixture) assertSuccessfulReadOperations(expected []oraclePost) {
	f.t.Helper()
	alicePrivate := mustUUID(f.t, alicePrivateText)
	wantUnique := expectedPost(f.t, expected, alicePrivateText)
	f.observed.reset()
	unique, err := f.caller.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(alicePrivate),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "successful caller findUnique", normalizePostRows([]golem.Row[social.Post]{unique}), []oraclePost{wantUnique})
	assertSingleObservation(f.t, "caller findUnique", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindUnique, 1)

	var txUnique golem.Row[social.Post]
	f.observed.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var findErr error
		txUnique, findErr = tx.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(alicePrivate),
			social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
		)
		return findErr
	}); err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "successful CallerTx findUnique", normalizePostRows([]golem.Row[social.Post]{txUnique}), []oraclePost{wantUnique})
	assertSingleObservation(f.t, "CallerTx findUnique", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindUnique, 1)

	f.observed.reset()
	graphUnique := f.graphQL(fmt.Sprintf(`query {
  post(where: {ID: %q}) { id authorID title body published }
}`, alicePrivateText))
	assertNoGraphErrors(f.t, graphUnique)
	value := graphRoot(f.t, graphUnique)["post"]
	assertPosts(f.t, "successful GraphQL findUnique", []oraclePost{graphPost(f.t, value)}, []oraclePost{wantUnique})
	assertSingleObservation(f.t, "GraphQL findUnique", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindUnique, 1)
	assertSingleObservation(f.t, "GraphQL findUnique", f.observed.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, 1)

	wantFirst := expectedPost(f.t, expected, alicePrivateText)
	f.observed.reset()
	first, found, err := f.caller.Posts.FindFirst(f.ctx,
		social.Posts.OrderBy(social.Posts.Title.Asc(), social.Posts.ID.Asc()),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil || !found {
		f.t.Fatalf("caller findFirst found=%t error=%v", found, err)
	}
	assertPosts(f.t, "caller findFirst", normalizePostRows([]golem.Row[social.Post]{first}), []oraclePost{wantFirst})
	assertSingleObservation(f.t, "caller findFirst", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindFirst, 1)
	var txFirst golem.Row[social.Post]
	var txFound bool
	f.observed.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var findErr error
		txFirst, txFound, findErr = tx.Posts.FindFirst(f.ctx,
			social.Posts.OrderBy(social.Posts.Title.Asc(), social.Posts.ID.Asc()),
			social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
		)
		return findErr
	}); err != nil || !txFound {
		f.t.Fatalf("CallerTx findFirst found=%t error=%v", txFound, err)
	}
	assertPosts(f.t, "CallerTx findFirst", normalizePostRows([]golem.Row[social.Post]{txFirst}), []oraclePost{wantFirst})
	assertSingleObservation(f.t, "CallerTx findFirst", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindFirst, 1)
	graphFirst := f.graphQL(`query { posts(orderBy: [{title: asc}, {id: asc}], take: 1) { id authorID title body published } }`)
	assertNoGraphErrors(f.t, graphFirst)
	assertPosts(f.t, "GraphQL first-row surface", graphPosts(f.t, graphFirst, "posts"), []oraclePost{wantFirst})

	f.observed.reset()
	count, err := f.caller.Posts.Count(f.ctx)
	if err != nil || count != int64(len(expected)) {
		f.t.Fatalf("caller count=%d want=%d error=%v", count, len(expected), err)
	}
	assertSingleObservation(f.t, "caller count", f.observed.snapshot(), observe.KindRead, observe.OperationReadCount, 1)
	var txCount int64
	f.observed.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var countErr error
		txCount, countErr = tx.Posts.Count(f.ctx)
		return countErr
	}); err != nil || txCount != int64(len(expected)) {
		f.t.Fatalf("CallerTx count=%d want=%d error=%v", txCount, len(expected), err)
	}
	assertSingleObservation(f.t, "CallerTx count", f.observed.snapshot(), observe.KindRead, observe.OperationReadCount, 1)
	graphCount := f.graphQL(`query { posts(orderBy: [{id: asc}], take: 50) { id } }`)
	assertNoGraphErrors(f.t, graphCount)
	if got := len(graphRoot(f.t, graphCount)["posts"].([]any)); got != len(expected) {
		f.t.Fatalf("GraphQL authorized row count=%d want=%d", got, len(expected))
	}

	f.assertRelationRead()
}

func (f *fixture) assertRelationRead() {
	f.t.Helper()
	want := relationSnapshot{
		Post:   oraclePost{ID: bobPublicText, AuthorID: bobIDText, Title: "bravo-public", Published: true, BodyState: "masked-null"},
		Author: oracleUser{ID: bobIDText, Handle: "bob", EmailState: "masked-null"}, CommentCount: 1,
		Comments: []string{bobCommentText + ":authorized relation comment"},
	}
	projection := social.Posts.Select(
		social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published,
		social.Posts.Author.Select(social.Users.ID, social.Users.Handle, social.Users.Email),
		social.Posts.Comments.Args(
			social.Comments.OrderBy(social.Comments.ID.Asc()),
			social.Comments.Select(social.Comments.ID, social.Comments.Body),
		),
		social.Posts.Comments.Count(),
	)
	f.observed.reset()
	row, err := f.caller.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, bobPublicText)), projection)
	if err != nil {
		f.t.Fatal(err)
	}
	if got := normalizeRelation(f.t, row); !reflect.DeepEqual(got, want) {
		f.t.Fatalf("caller relation=%s want=%s", mustEncode(got), mustEncode(want))
	}
	f.assertRelationObservations("caller relation", f.observed.snapshot(), false)
	var txRow golem.Row[social.Post]
	f.observed.reset()
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var findErr error
		txRow, findErr = tx.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, bobPublicText)), projection)
		return findErr
	}); err != nil {
		f.t.Fatal(err)
	}
	if got := normalizeRelation(f.t, txRow); !reflect.DeepEqual(got, want) {
		f.t.Fatalf("CallerTx relation=%s want=%s", mustEncode(got), mustEncode(want))
	}
	f.assertRelationObservations("CallerTx relation", f.observed.snapshot(), true)
	f.observed.reset()
	response := f.graphQL(fmt.Sprintf(`query {
  post(where: {ID: %q}) {
		id authorID title body published
		author { id handle email }
		comments(orderBy: [{id: asc}]) { id body }
		_count { comments }
  }
}`, bobPublicText))
	assertNoGraphErrors(f.t, response)
	if got := graphRelation(f.t, graphRoot(f.t, response)["post"]); !reflect.DeepEqual(got, want) {
		f.t.Fatalf("GraphQL relation=%s want=%s", mustEncode(got), mustEncode(want))
	}
	f.assertRelationObservations("GraphQL relation", f.observed.snapshot(), false)
	assertSingleObservation(f.t, "GraphQL relation", f.observed.snapshot(), observe.KindGraphQL, observe.OperationGraphQLQuery, 1)
}

func (f *fixture) assertRelationObservations(label string, values []observedOperation, transaction bool) {
	f.t.Helper()
	assertSingleObservation(f.t, label, values, observe.KindRead, observe.OperationReadFindUnique, 1)
	assertObservationModelCountAggregate(f.t, label+" correlated relation path", values,
		observe.KindRelationLoad, social.GolemGeneratedUserDescriptor.Metadata().ModelID(),
		observe.OperationRelationLoad, 0, 1, 1)
	assertObservationModelCountAggregate(f.t, label+" correlated comments path", values,
		observe.KindRelationLoad, social.GolemGeneratedCommentDescriptor.Metadata().ModelID(),
		observe.OperationRelationLoad, 0, 1, 1)
	assertObservationCountAggregate(f.t, label+" all correlated relation paths", values,
		observe.KindRelationLoad, observe.OperationRelationLoad, 0, 1, 2)
	if transaction {
		assertSingleObservation(f.t, label, values, observe.KindTransaction, observe.OperationCallerTransaction, 1)
	}
}

func (f *fixture) maskErrorPagination() {
	expectedUsers := f.expectedUsers()
	rows, err := f.caller.Users.FindMany(f.ctx,
		social.Users.OrderBy(social.Users.ID.Asc()),
		social.Users.Select(social.Users.ID, social.Users.Handle, social.Users.Email),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	assertUsers(f.t, "caller", normalizeUserRows(rows), expectedUsers)
	var txRows []golem.Row[social.User]
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var txErr error
		txRows, txErr = tx.Users.FindMany(f.ctx,
			social.Users.OrderBy(social.Users.ID.Asc()),
			social.Users.Select(social.Users.ID, social.Users.Handle, social.Users.Email),
		)
		return txErr
	}); err != nil {
		f.t.Fatal(err)
	}
	assertUsers(f.t, "caller transaction", normalizeUserRows(txRows), expectedUsers)
	response := f.graphQL(`query { users(orderBy: [{id: asc}], take: 25) { id handle email } }`)
	assertNoGraphErrors(f.t, response)
	assertUsers(f.t, "generated GraphQL", graphUsers(f.t, response), expectedUsers)

	expectedPage := f.expectedPostPage(1, 2)
	page, err := f.caller.Posts.FindMany(f.ctx,
		social.Posts.OrderBy(social.Posts.Title.Asc(), social.Posts.ID.Asc()),
		social.Posts.Skip(1), social.Posts.Take(2),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller page", normalizePostRows(page), expectedPage)
	var txPage []golem.Row[social.Post]
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var txErr error
		txPage, txErr = tx.Posts.FindMany(f.ctx,
			social.Posts.OrderBy(social.Posts.Title.Asc(), social.Posts.ID.Asc()),
			social.Posts.Skip(1), social.Posts.Take(2),
			social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
		)
		return txErr
	}); err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller transaction page", normalizePostRows(txPage), expectedPage)
	graphPage := f.graphQL(`query {
  posts(orderBy: [{title: asc}, {id: asc}], skip: 1, take: 2) { id authorID title body published }
}`)
	assertNoGraphErrors(f.t, graphPage)
	assertPosts(f.t, "GraphQL page", graphPosts(f.t, graphPage, "posts"), expectedPage)

	f.observed.reset()
	callerFailure := publicError(f.t, findManyNegativeTake(f.ctx, f.caller))
	assertObservation(f.t, "caller preflight refusal", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindMany, observe.OutcomeRefused, observe.ReasonInvalidInput, 0)
	var txFailure *golem.Error
	f.observed.reset()
	err = f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, readErr := tx.Posts.FindMany(f.ctx, social.Posts.Take(-1))
		txFailure = publicError(f.t, readErr)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if callerFailure.Code != golem.CodeBadUserInput || txFailure.Code != callerFailure.Code || txFailure.Operation != callerFailure.Operation || txFailure.Message != callerFailure.Message {
		f.t.Fatalf("stable caller errors differ: caller=%+v tx=%+v", callerFailure, txFailure)
	}
	assertObservation(f.t, "CallerTx preflight refusal", f.observed.snapshot(), observe.KindRead, observe.OperationReadFindMany, observe.OutcomeRefused, observe.ReasonInvalidInput, 0)
	graphFailure := f.graphQL(`query { posts(take: -1) { id } }`)
	if len(graphFailure.Errors) != 1 || graphFailure.Errors[0].Extensions["code"] != string(golem.CodeBadUserInput) {
		f.t.Fatalf("GraphQL stable error=%+v", graphFailure.Errors)
	}

	f.assertRejectedHookErrorParity()
	f.assertMissingInvisibleParity()
}

func (f *fixture) customCapability() {
	rows, err := social.SearchPosts(f.ctx, f.caller, social.SearchPostsArgs{
		Where: social.Posts.ID.Eq(f.privateBob), Take: 10,
	})
	if err != nil || len(rows) != 0 {
		f.t.Fatalf("custom direct bypass rows=%d error=%v", len(rows), err)
	}
	response := f.graphQL(fmt.Sprintf(`query {
  searchPosts(where: {id: {equals: %q}}, take: 10) { id title body }
}`, bobPrivateText))
	assertNoGraphErrors(f.t, response)
	if got := graphPosts(f.t, response, "searchPosts"); len(got) != 0 {
		f.t.Fatalf("custom GraphQL bypass rows=%+v", got)
	}
	ordinary := f.graphQL(fmt.Sprintf(`query { post(where: {ID: %q}) { id title body } }`, bobPrivateText))
	assertNoGraphErrors(f.t, ordinary)
	if value := graphRoot(f.t, ordinary)["post"]; value != nil {
		f.t.Fatalf("ordinary GraphQL exposed invisible row=%v", value)
	}
	row, err := f.app.System().Posts.FindUnique(f.ctx, social.Posts.ByID.Value(f.privateBob),
		social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.Body),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	title, ok := golem.Value(row, social.Posts.Title).Get()
	if !ok || title != "charlie-"+protectedCanary {
		f.t.Fatalf("system capability did not retain exact private row title=%q present=%t", title, ok)
	}
	assertCustomSourceCapability(f.t)
}

func (f *fixture) callerTransaction() {
	expected := f.expectedVisiblePosts()
	baseline, err := f.caller.Posts.FindMany(f.ctx,
		social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(50),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller baseline", normalizePostRows(baseline), expected)
	var posts []golem.Row[social.Post]
	var users []golem.Row[social.User]
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		var readErr error
		posts, readErr = tx.Posts.FindMany(f.ctx,
			social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(50),
			social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
		)
		if readErr != nil {
			return readErr
		}
		users, readErr = tx.Users.FindMany(f.ctx,
			social.Users.OrderBy(social.Users.ID.Asc()),
			social.Users.Select(social.Users.ID, social.Users.Handle, social.Users.Email),
		)
		return readErr
	}); err != nil {
		f.t.Fatal(err)
	}
	assertPosts(f.t, "caller transaction", normalizePostRows(posts), expected)
	assertUsers(f.t, "caller transaction masks", normalizeUserRows(users), f.expectedUsers())

	before := f.directPostCount()
	sentinel := errors.New("p8 read-only rollback sentinel")
	err = f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		rows, readErr := tx.Posts.FindMany(f.ctx, social.Posts.Take(2), social.Posts.OrderBy(social.Posts.ID.Asc()))
		if readErr != nil || len(rows) != 2 {
			return fmt.Errorf("transaction read rows=%d: %w", len(rows), readErr)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		f.t.Fatalf("transaction rollback error=%v", err)
	}
	if after := f.directPostCount(); after != before {
		f.t.Fatalf("read transaction changed rows before=%d after=%d", before, after)
	}
	f.assertMissingInvisibleParity()
}

func (f *fixture) expectedVisiblePosts() []oraclePost {
	f.t.Helper()
	var rows []sqlPost
	if err := f.database.UnsafeSQLX().SelectContext(f.ctx, &rows, `SELECT id,author_id,title,body,published FROM posts ORDER BY id`); err != nil {
		f.t.Fatal(err)
	}
	return f.authorizeSQLPosts(rows)
}

func (f *fixture) expectedCustomPosts() []oraclePost {
	f.t.Helper()
	var rows []sqlPost
	if err := f.database.UnsafeSQLX().SelectContext(f.ctx, &rows, `SELECT id,author_id,title,body,published FROM posts ORDER BY created_at DESC,id ASC`); err != nil {
		f.t.Fatal(err)
	}
	return f.authorizeSQLPosts(rows)
}

func (f *fixture) authorizeSQLPosts(rows []sqlPost) []oraclePost {
	f.t.Helper()
	result := make([]oraclePost, 0, len(rows))
	for _, row := range rows {
		if !row.Published && row.AuthorID != aliceIDText {
			continue
		}
		item := oraclePost{ID: row.ID, AuthorID: row.AuthorID, Title: row.Title, Published: row.Published}
		if row.AuthorID == aliceIDText {
			item.Body, item.BodyState = stringPointer(row.Body), "present"
		} else {
			item.BodyState = "masked-null"
		}
		result = append(result, item)
	}
	return result
}

func (f *fixture) expectedPostPage(skip, take int) []oraclePost {
	result := f.expectedVisiblePosts()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Title == result[j].Title {
			return result[i].ID < result[j].ID
		}
		return result[i].Title < result[j].Title
	})
	result = append([]oraclePost(nil), result[skip:skip+take]...)
	return result
}

func (f *fixture) expectedUsers() []oracleUser {
	f.t.Helper()
	var rows []sqlUser
	if err := f.database.UnsafeSQLX().SelectContext(f.ctx, &rows, `SELECT id,handle,email FROM users ORDER BY id`); err != nil {
		f.t.Fatal(err)
	}
	result := make([]oracleUser, 0, len(rows))
	for _, row := range rows {
		item := oracleUser{ID: row.ID, Handle: row.Handle}
		if row.ID == aliceIDText {
			item.Email, item.EmailState = stringPointer(row.Email), "present"
		} else {
			item.EmailState = "masked-null"
		}
		result = append(result, item)
	}
	return result
}

func (f *fixture) assertMissingInvisibleParity() {
	f.t.Helper()
	missing := mustUUID(f.t, "82000000-0000-0000-0000-000000000099")
	invisible := publicError(f.t, findPost(f.ctx, f.caller, f.privateBob))
	absent := publicError(f.t, findPost(f.ctx, f.caller, missing))
	if invisible.Code != golem.CodeNotFound || absent.Code != golem.CodeNotFound || invisible.Operation != absent.Operation || invisible.Message != absent.Message || invisible.Error() != absent.Error() {
		f.t.Fatalf("missing/invisible caller errors differ invisible=%+v missing=%+v", invisible, absent)
	}
	var txInvisible, txAbsent *golem.Error
	if err := f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, first := tx.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(f.privateBob))
		_, second := tx.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(missing))
		txInvisible, txAbsent = publicError(f.t, first), publicError(f.t, second)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	if txInvisible.Error() != invisible.Error() || txAbsent.Error() != absent.Error() {
		f.t.Fatalf("transaction stable errors differ caller=%v/%v tx=%v/%v", invisible, absent, txInvisible, txAbsent)
	}
	response := f.graphQL(fmt.Sprintf(`query {
  invisible: post(where: {ID: %q}) { id }
  missing: post(where: {ID: %q}) { id }
}`, bobPrivateText, missing.String()))
	assertNoGraphErrors(f.t, response)
	root := graphRoot(f.t, response)
	if root["invisible"] != nil || root["missing"] != nil {
		f.t.Fatalf("GraphQL missing/invisible shape differs data=%s", response.Data)
	}
}

func (f *fixture) assertRejectedHookErrorParity() {
	f.t.Helper()
	cause := errors.New("P8_PRIVATE_HOOK_ERROR_CANARY")
	ctx := social.WithPostReadHookObserver(f.ctx, rejectingReadHook{cause: cause})
	_, callerErr := f.caller.Posts.FindMany(ctx, social.Posts.Take(2))
	callerFailure := publicError(f.t, callerErr)
	var txFailure *golem.Error
	if err := f.caller.Transaction(ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, readErr := tx.Posts.FindMany(ctx, social.Posts.Take(2))
		txFailure = publicError(f.t, readErr)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	_, customErr := social.SearchPosts(ctx, f.caller, social.SearchPostsArgs{Where: golem.All[social.Post](), Take: 2})
	customFailure := publicError(f.t, customErr)
	for label, failure := range map[string]*golem.Error{"callerTx": txFailure, "custom": customFailure} {
		if failure.Code != callerFailure.Code || failure.Operation != callerFailure.Operation || failure.Message != callerFailure.Message {
			f.t.Fatalf("%s rejected-hook error=%+v caller=%+v", label, failure, callerFailure)
		}
		if strings.Contains(failure.Error(), cause.Error()) {
			f.t.Fatalf("%s disclosed trusted hook cause", label)
		}
	}
	ordinary := f.graphQLContext(ctx, `query { posts(take: 2) { id } }`)
	custom := f.graphQLContext(ctx, `query { searchPosts(where: {all: true}, take: 2) { id } }`)
	for label, response := range map[string]graphResponse{"ordinary GraphQL": ordinary, "custom GraphQL": custom} {
		if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != string(callerFailure.Code) {
			f.t.Fatalf("%s rejected-hook errors=%+v", label, response.Errors)
		}
		encoded, _ := json.Marshal(response)
		if bytes.Contains(encoded, []byte(cause.Error())) {
			f.t.Fatalf("%s disclosed trusted hook cause", label)
		}
	}
	trusted := f.takeTrustedErrors()
	if len(trusted) != 0 {
		f.t.Fatalf("public hook failures were misclassified as internal: %v", trusted)
	}
}

func (f *fixture) assertLoaderIsolation() {
	f.t.Helper()
	queries := []struct{ prefix, want string }{{"left:", "left:" + alicePrivateText}, {"right:", "right:" + alicePrivateText}}
	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(queries))
	f.observed.reset()
	for _, query := range queries {
		query := query
		wait.Add(1)
		go func() {
			defer wait.Done()
			trace := &displayCodeTrace{}
			requestContext := social.WithPostDisplayCodeLoaderObserver(f.ctx, trace)
			response := f.graphQLContext(requestContext, fmt.Sprintf(`query {
  post(where: {ID: %q}) { displayCode(prefix: %q) }
}`, alicePrivateText, query.prefix))
			if len(response.Errors) != 0 {
				errorsChannel <- fmt.Errorf("prefix %s errors=%v", query.prefix, response.Errors)
				return
			}
			post, ok := graphRoot(f.t, response)["post"].(map[string]any)
			if !ok || post["displayCode"] != query.want {
				errorsChannel <- fmt.Errorf("prefix %s data=%s", query.prefix, response.Data)
				return
			}
			wantLoads := []displayCodeLoad{{Keys: []string{alicePrivateText}, Prefix: query.prefix}}
			if loads := trace.snapshot(); !reflect.DeepEqual(loads, wantLoads) {
				errorsChannel <- fmt.Errorf("prefix %s operation-local loads=%v want=%v", query.prefix, loads, wantLoads)
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		f.t.Error(err)
	}
	values := f.observed.snapshot()
	assertObservationCount(f.t, "isolated loader reads", values, observe.KindRead, observe.OperationReadFindUnique, 1, 2)
	assertObservationCount(f.t, "isolated loader GraphQL roots", values, observe.KindGraphQL, observe.OperationGraphQLQuery, 1, 2)
	assertObservationCountAggregate(f.t, "isolated loader batches", values, observe.KindGraphQL, observe.OperationGraphQLBatchedComputed, 0, 1, 2)
}

func (f *fixture) assertSharedReadClassification(expectedRows int) {
	f.t.Helper()
	assertTrace := func(label string, trace *readTrace) {
		f.t.Helper()
		phases, rows := trace.snapshot()
		wantPhases := []social.PostReadHookPhase{social.PostHookBeforeFindMany, social.PostHookAfterFindMany}
		if !reflect.DeepEqual(phases, wantPhases) || !reflect.DeepEqual(rows, []int{0, expectedRows}) {
			f.t.Fatalf("%s shared read classification phases=%v rows=%v", label, phases, rows)
		}
	}
	callerTrace := &readTrace{}
	callerContext := social.WithPostReadHookObserver(f.ctx, callerTrace)
	f.observed.reset()
	if _, err := f.caller.Posts.FindMany(callerContext, social.Posts.Take(50)); err != nil {
		f.t.Fatal(err)
	}
	assertTrace("caller", callerTrace)
	f.assertObservedRead("caller", false)

	txTrace := &readTrace{}
	txContext := social.WithPostReadHookObserver(f.ctx, txTrace)
	f.observed.reset()
	if err := f.caller.Transaction(txContext, func(tx *social.CallerTx[social.Principal]) error {
		_, err := tx.Posts.FindMany(txContext, social.Posts.Take(50))
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	assertTrace("caller transaction", txTrace)
	f.assertObservedRead("caller transaction", true)

	customTrace := &readTrace{}
	customContext := social.WithPostReadHookObserver(f.ctx, customTrace)
	f.observed.reset()
	if _, err := social.SearchPosts(customContext, f.caller, social.SearchPostsArgs{Where: golem.All[social.Post](), Take: 50}); err != nil {
		f.t.Fatal(err)
	}
	assertTrace("custom direct", customTrace)
	f.assertObservedRead("custom direct", false)

	ordinaryGraphTrace := &readTrace{}
	ordinaryContext := social.WithPostReadHookObserver(f.ctx, ordinaryGraphTrace)
	f.observed.reset()
	response := f.graphQLContext(ordinaryContext, `query { posts(take: 50) { id } }`)
	assertNoGraphErrors(f.t, response)
	assertTrace("ordinary GraphQL", ordinaryGraphTrace)
	f.assertObservedGraphQLRead("ordinary GraphQL", false)

	customGraphTrace := &readTrace{}
	customGraphContext := social.WithPostReadHookObserver(f.ctx, customGraphTrace)
	f.observed.reset()
	response = f.graphQLContext(customGraphContext, `query { searchPosts(where: {all: true}, take: 50) { id } }`)
	assertNoGraphErrors(f.t, response)
	assertTrace("custom GraphQL", customGraphTrace)
	f.assertObservedGraphQLRead("custom GraphQL", true)
}

func (f *fixture) assertObservedRead(label string, transaction bool) {
	f.t.Helper()
	values := f.observed.snapshot()
	assertSingleObservation(f.t, label, values, observe.KindRead, observe.OperationReadFindMany, 1)
	if transaction {
		assertSingleObservation(f.t, label, values, observe.KindTransaction, observe.OperationCallerTransaction, 1)
	}
}

func (f *fixture) assertObservedGraphQLRead(label string, custom bool) {
	f.t.Helper()
	values := f.observed.snapshot()
	assertSingleObservation(f.t, label, values, observe.KindRead, observe.OperationReadFindMany, 1)
	assertSingleObservation(f.t, label, values, observe.KindGraphQL, observe.OperationGraphQLQuery, 1)
	if custom {
		assertSingleObservation(f.t, label, values, observe.KindGraphQL, observe.OperationGraphQLCustomQuery, 1)
	}
}

func assertSingleObservation(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, statements int) {
	t.Helper()
	assertObservation(t, label, values, kind, operation, observe.OutcomeSuccess, observe.ReasonNone, statements)
}

func assertObservation(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, outcome observe.Outcome, reason observe.Reason, statements int) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	want := observedOperation{Kind: kind, Operation: operation, Outcome: outcome, Reason: reason, Statements: statements}
	if len(matches) != 1 || !sameObservedCore(matches[0], want) {
		t.Fatalf("%s observation %s/%s matches=%+v all=%+v want=%+v", label, kind, operation, matches, values, want)
	}
}

func assertSingleObservationAggregate(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, statements int, aggregate int64) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	want := observedOperation{Kind: kind, Operation: operation, Outcome: observe.OutcomeSuccess, Reason: observe.ReasonNone, Statements: statements, Aggregate: aggregate}
	if len(matches) != 1 || !sameObservedCore(matches[0], want) || matches[0].Aggregate != want.Aggregate {
		t.Fatalf("%s aggregate observation matches=%+v all=%+v want=%+v", label, matches, values, want)
	}
}

func assertObservationCount(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, statements, count int) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	want := observedOperation{Kind: kind, Operation: operation, Outcome: observe.OutcomeSuccess, Reason: observe.ReasonNone, Statements: statements}
	if len(matches) != count {
		t.Fatalf("%s observation count=%d matches=%+v all=%+v want=%d", label, len(matches), matches, values, count)
	}
	for _, match := range matches {
		if !sameObservedCore(match, want) {
			t.Fatalf("%s observation=%+v want=%+v all=%+v", label, match, want, values)
		}
	}
}

func assertObservationCountAggregate(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, statements int, aggregate int64, count int) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	want := observedOperation{Kind: kind, Operation: operation, Outcome: observe.OutcomeSuccess, Reason: observe.ReasonNone, Statements: statements, Aggregate: aggregate}
	if len(matches) != count {
		t.Fatalf("%s aggregate observation count=%d matches=%+v all=%+v want=%d", label, len(matches), matches, values, count)
	}
	for _, match := range matches {
		if !sameObservedCore(match, want) || match.Aggregate != want.Aggregate {
			t.Fatalf("%s aggregate observation=%+v want=%+v all=%+v", label, match, want, values)
		}
	}
}

func assertObservationModelCountAggregate(t *testing.T, label string, values []observedOperation, kind observe.Kind, model golem.ModelID, operation observe.Operation, statements int, aggregate int64, count int) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Model == model && value.Operation == operation {
			matches = append(matches, value)
		}
	}
	want := observedOperation{Kind: kind, Model: model, Operation: operation, Outcome: observe.OutcomeSuccess, Reason: observe.ReasonNone, Statements: statements, Aggregate: aggregate}
	if len(matches) != count {
		t.Fatalf("%s aggregate observation count=%d matches=%+v all=%+v want=%d", label, len(matches), matches, values, count)
	}
	for _, match := range matches {
		if match != want {
			t.Fatalf("%s aggregate observation=%+v want=%+v all=%+v", label, match, want, values)
		}
	}
}

func sameObservedCore(left, right observedOperation) bool {
	return left.Kind == right.Kind && left.Operation == right.Operation && left.Outcome == right.Outcome &&
		left.Reason == right.Reason && left.Statements == right.Statements
}

func (f *fixture) directPostCount() int {
	f.t.Helper()
	var count int
	if err := f.database.UnsafeSQLX().GetContext(f.ctx, &count, `SELECT COUNT(*) FROM posts`); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *fixture) graphQL(query string) graphResponse {
	f.t.Helper()
	return f.graphQLContext(f.ctx, query)
}

func (f *fixture) graphQLContext(ctx context.Context, query string) graphResponse {
	f.t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		f.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request = request.WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		f.t.Fatalf("GraphQL status=%d body=%s", recorder.Code, recorder.Body.Bytes())
	}
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		f.t.Fatal(err)
	}
	return response
}

func graphRoot(t *testing.T, response graphResponse) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(response.Data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func graphPosts(t *testing.T, response graphResponse, field string) []oraclePost {
	t.Helper()
	value := graphRoot(t, response)[field]
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("GraphQL field %s is %T", field, value)
	}
	result := make([]oraclePost, 0, len(items))
	for _, value := range items {
		result = append(result, graphPost(t, value))
	}
	return result
}

func graphPost(t *testing.T, value any) oraclePost {
	t.Helper()
	item, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL post is %T", value)
	}
	body, bodyState := graphNullableString(item, "body")
	return oraclePost{
		ID: stringField(t, item, "id"), AuthorID: stringField(t, item, "authorID"),
		Title: stringField(t, item, "title"), Published: boolField(t, item, "published"),
		Body: body, BodyState: bodyState,
	}
}

func graphRelation(t *testing.T, value any) relationSnapshot {
	t.Helper()
	postMap, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL relation post is %T", value)
	}
	authorMap, ok := postMap["author"].(map[string]any)
	if !ok {
		t.Fatalf("GraphQL author is %T", postMap["author"])
	}
	email, emailState := graphNullableString(authorMap, "email")
	countMap, ok := postMap["_count"].(map[string]any)
	if !ok {
		t.Fatalf("GraphQL _count is %T", postMap["_count"])
	}
	count, ok := countMap["comments"].(float64)
	if !ok {
		t.Fatalf("GraphQL comment count is %T", countMap["comments"])
	}
	commentValues, ok := postMap["comments"].([]any)
	if !ok {
		t.Fatalf("GraphQL comments is %T", postMap["comments"])
	}
	comments := make([]string, len(commentValues))
	for index, value := range commentValues {
		comment, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL comment is %T", value)
		}
		comments[index] = stringField(t, comment, "id") + ":" + stringField(t, comment, "body")
	}
	return relationSnapshot{
		Post:         graphPost(t, postMap),
		Author:       oracleUser{ID: stringField(t, authorMap, "id"), Handle: stringField(t, authorMap, "handle"), Email: email, EmailState: emailState},
		CommentCount: int64(count),
		Comments:     comments,
	}
}

func graphUsers(t *testing.T, response graphResponse) []oracleUser {
	t.Helper()
	value := graphRoot(t, response)["users"]
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("GraphQL users is %T", value)
	}
	result := make([]oracleUser, 0, len(items))
	for _, value := range items {
		item := value.(map[string]any)
		email, state := graphNullableString(item, "email")
		result = append(result, oracleUser{ID: stringField(t, item, "id"), Handle: stringField(t, item, "handle"), Email: email, EmailState: state})
	}
	return result
}

func normalizePostRows(rows []golem.Row[social.Post]) []oraclePost {
	result := make([]oraclePost, 0, len(rows))
	for _, row := range rows {
		id, _ := golem.Value(row, social.Posts.ID).Get()
		author, _ := golem.Value(row, social.Posts.AuthorID).Get()
		title, _ := golem.Value(row, social.Posts.Title).Get()
		published, _ := golem.Value(row, social.Posts.Published).Get()
		bodyValue := golem.Value(row, social.Posts.Body)
		item := oraclePost{ID: id.String(), AuthorID: author.String(), Title: title, Published: published}
		if body, present := bodyValue.Get(); present {
			item.Body, item.BodyState = stringPointer(body), "present"
		} else if bodyValue.IsNull() {
			item.BodyState = "masked-null"
		} else {
			item.BodyState = "unselected"
		}
		result = append(result, item)
	}
	return result
}

func normalizeRelation(t *testing.T, row golem.Row[social.Post]) relationSnapshot {
	t.Helper()
	post := normalizePostRows([]golem.Row[social.Post]{row})[0]
	author, present := golem.One(row, social.Posts.Author.ToOne).Get()
	if !present {
		t.Fatal("selected author relation is absent")
	}
	authorID, idPresent := golem.Value(author, social.Users.ID).Get()
	handle, handlePresent := golem.Value(author, social.Users.Handle).Get()
	if !idPresent || !handlePresent {
		t.Fatal("selected author identity is absent")
	}
	emailValue := golem.Value(author, social.Users.Email)
	normalizedAuthor := oracleUser{ID: authorID.String(), Handle: handle}
	if email, ok := emailValue.Get(); ok {
		normalizedAuthor.Email, normalizedAuthor.EmailState = stringPointer(email), "present"
	} else if emailValue.IsNull() {
		normalizedAuthor.EmailState = "masked-null"
	} else {
		normalizedAuthor.EmailState = "unselected"
	}
	count, countPresent := golem.RelationCount(row, social.Posts.Comments.ToMany).Get()
	if !countPresent {
		t.Fatal("selected comment relation count is absent")
	}
	commentRows, commentsPresent := golem.Many(row, social.Posts.Comments.ToMany).Get()
	if !commentsPresent {
		t.Fatal("selected comment relation rows are absent")
	}
	comments := make([]string, len(commentRows))
	for index, comment := range commentRows {
		id, idPresent := golem.Value(comment, social.Comments.ID).Get()
		body, bodyPresent := golem.Value(comment, social.Comments.Body).Get()
		if !idPresent || !bodyPresent {
			t.Fatal("selected comment relation value is absent")
		}
		comments[index] = id.String() + ":" + body
	}
	return relationSnapshot{Post: post, Author: normalizedAuthor, CommentCount: count, Comments: comments}
}

func normalizeUserRows(rows []golem.Row[social.User]) []oracleUser {
	result := make([]oracleUser, 0, len(rows))
	for _, row := range rows {
		id, _ := golem.Value(row, social.Users.ID).Get()
		handle, _ := golem.Value(row, social.Users.Handle).Get()
		emailValue := golem.Value(row, social.Users.Email)
		item := oracleUser{ID: id.String(), Handle: handle}
		if email, present := emailValue.Get(); present {
			item.Email, item.EmailState = stringPointer(email), "present"
		} else if emailValue.IsNull() {
			item.EmailState = "masked-null"
		} else {
			item.EmailState = "unselected"
		}
		result = append(result, item)
	}
	return result
}

func expectedPost(t *testing.T, rows []oraclePost, id string) oraclePost {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("expected post %s is absent", id)
	return oraclePost{}
}

func findManyNegativeTake(ctx context.Context, caller *social.Caller[social.Principal]) error {
	_, err := caller.Posts.FindMany(ctx, social.Posts.Take(-1))
	return err
}

func findPost(ctx context.Context, caller *social.Caller[social.Principal], id golem.UUID) error {
	_, err := caller.Posts.FindUnique(ctx, social.Posts.ByID.Value(id))
	return err
}

func publicError(t *testing.T, err error) *golem.Error {
	t.Helper()
	var result *golem.Error
	if !errors.As(err, &result) {
		t.Fatalf("expected public Golem error, got %T %v", err, err)
	}
	return result
}

func assertCustomSourceCapability(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("P8_ORACLE_EXAMPLE"), "social", "extensions.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "SearchPosts" {
			continue
		}
		parameterFound := false
		for _, parameter := range function.Type.Params.List {
			if star, ok := parameter.Type.(*ast.StarExpr); ok {
				if index, ok := star.X.(*ast.IndexExpr); ok {
					if selector, ok := index.X.(*ast.Ident); ok && selector.Name == "Caller" {
						parameterFound = true
					}
				}
			}
		}
		if !parameterFound {
			t.Fatal("SearchPosts does not receive the generated Caller capability")
		}
		forbidden := ""
		ast.Inspect(function, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				if value.Name == "System" || value.Name == "Database" || value.Name == "UnsafeSQLX" {
					forbidden = value.Name
				}
			case *ast.SelectorExpr:
				if value.Sel.Name == "System" || value.Sel.Name == "UnsafeSQLX" {
					forbidden = value.Sel.Name
				}
			}
			return forbidden == ""
		})
		if forbidden != "" {
			t.Fatalf("SearchPosts body reaches forbidden capability %s", forbidden)
		}
		return
	}
	t.Fatal("SearchPosts declaration not found")
}

func assertPosts(t *testing.T, label string, got, want []oraclePost) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s posts mismatch\n got=%s\nwant=%s", label, mustEncode(got), mustEncode(want))
	}
}

func assertUsers(t *testing.T, label string, got, want []oracleUser) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s users mismatch\n got=%s\nwant=%s", label, mustEncode(got), mustEncode(want))
	}
}

func assertNoGraphErrors(t *testing.T, response graphResponse) {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors=%+v data=%s", response.Errors, response.Data)
	}
}

func graphNullableString(item map[string]any, field string) (*string, string) {
	value, selected := item[field]
	if !selected {
		return nil, "unselected"
	}
	if value == nil {
		return nil, "masked-null"
	}
	text := value.(string)
	return &text, "present"
}

func stringField(t *testing.T, item map[string]any, field string) string {
	t.Helper()
	value, ok := item[field].(string)
	if !ok {
		t.Fatalf("field %s is %T", field, item[field])
	}
	return value
}

func boolField(t *testing.T, item map[string]any, field string) bool {
	t.Helper()
	value, ok := item[field].(bool)
	if !ok {
		t.Fatalf("field %s is %T", field, item[field])
	}
	return value
}

func stringPointer(value string) *string { return &value }

func mustEncode(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
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
