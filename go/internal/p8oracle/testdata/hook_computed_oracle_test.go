package p8oracleconsumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
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
	aliceIDText                = "91000000-0000-0000-0000-000000000001"
	bobIDText                  = "91000000-0000-0000-0000-000000000002"
	privateBodyCanary          = "P8_PRIVATE_COMPUTED_DEPENDENCY_7fb2d38c"
	concurrentLoaderOperations = 12
)

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

type hookRow struct {
	Phase     social.PostHookPhase
	ID        string
	AuthorID  string
	Title     string
	BodyState string
}

type hookTrace struct {
	mu              sync.Mutex
	rows            []hookRow
	readPhases      []social.PostReadHookPhase
	readRows        [][]hookRow
	failAfterCommit error
}

func (trace *hookTrace) ObservePostHook(_ context.Context, phase social.PostHookPhase, row golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	value := hookRow{Phase: phase}
	if id, ok := golem.Value(row, social.Posts.ID).Get(); ok {
		value.ID = id.String()
	}
	if author, ok := golem.Value(row, social.Posts.AuthorID).Get(); ok {
		value.AuthorID = author.String()
	}
	if title, ok := golem.Value(row, social.Posts.Title).Get(); ok {
		value.Title = title
	}
	if _, ok := golem.Value(row, social.Posts.Body).Get(); ok {
		value.BodyState = "present"
	} else {
		value.BodyState = "absent"
	}
	trace.rows = append(trace.rows, value)
	if phase == social.PostHookAfterCommitCreate && trace.failAfterCommit != nil {
		return trace.failAfterCommit
	}
	return nil
}

func (trace *hookTrace) ObservePostReadHook(_ context.Context, phase social.PostReadHookPhase, rows []golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.readPhases = append(trace.readPhases, phase)
	normalized := make([]hookRow, 0, len(rows))
	for _, row := range rows {
		value := hookRow{Phase: social.PostHookPhase(phase)}
		if id, ok := golem.Value(row, social.Posts.ID).Get(); ok {
			value.ID = id.String()
		}
		if _, ok := golem.Value(row, social.Posts.Body).Get(); ok {
			value.BodyState = "present"
		} else {
			value.BodyState = "absent"
		}
		normalized = append(normalized, value)
	}
	trace.readRows = append(trace.readRows, normalized)
	return nil
}

func (trace *hookTrace) snapshot() ([]hookRow, []social.PostReadHookPhase, [][]hookRow) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	rows := append([]hookRow(nil), trace.rows...)
	readPhases := append([]social.PostReadHookPhase(nil), trace.readPhases...)
	readRows := make([][]hookRow, len(trace.readRows))
	for index := range trace.readRows {
		readRows[index] = append([]hookRow(nil), trace.readRows[index]...)
	}
	return rows, readPhases, readRows
}

type displayLoad struct {
	Keys   []string
	Prefix string
}

type displayTrace struct {
	mu    sync.Mutex
	loads []displayLoad
}

func (trace *displayTrace) ObservePostDisplayCodeLoad(_ context.Context, keys []golem.UUID, arguments social.DisplayCodeArgs) error {
	value := displayLoad{Prefix: arguments.Prefix, Keys: make([]string, len(keys))}
	for index, key := range keys {
		value.Keys[index] = key.String()
	}
	sort.Strings(value.Keys)
	trace.mu.Lock()
	trace.loads = append(trace.loads, value)
	trace.mu.Unlock()
	return nil
}

func (trace *displayTrace) snapshot() []displayLoad {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	result := make([]displayLoad, len(trace.loads))
	copy(result, trace.loads)
	return result
}

type afterCommitRecord struct {
	Operation golem.HookOperation
	Model     golem.ModelID
	Cause     error
}

type observedOperation struct {
	Kind       observe.Kind
	Operation  observe.Operation
	Outcome    observe.Outcome
	Statements int
	Aggregate  int64
}

type fixture struct {
	t             *testing.T
	ctx           context.Context
	database      *provider.Database
	app           *social.App[social.Principal]
	caller        *social.Caller[social.Principal]
	graph         *social.GraphQLServer
	handler       http.Handler
	principal     social.Principal
	mu            sync.Mutex
	afterCommit   []afterCommitRecord
	trustedErrors []string
	observations  []observedOperation
}

func (fixture *fixture) ObserveGolem(_ context.Context, value observe.Observation) {
	recordObservationCoverage(value.Provider(), value.Operation())
	fixture.mu.Lock()
	fixture.observations = append(fixture.observations, observedOperation{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
	})
	fixture.mu.Unlock()
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

func TestP8ExternalOracleScenario(t *testing.T) {
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "resolver-capability-inventory":
		assertResolverCapabilities(t)
		return
	}
	fixture := newFixture(t)
	defer fixture.close()
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "hook-phase-result":
		fixture.hookPhaseAndResult()
	case "computed-batched-disclosure":
		fixture.computedAndBatchedDisclosure()
	case "after-commit-failure":
		fixture.afterCommitFailure()
	default:
		t.Fatalf("unknown P8 hook/computed scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
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
	aliceID := mustUUID(t, aliceIDText)
	result := &fixture{t: t, ctx: ctx, database: database, principal: social.Principal{Development: true, DevUserID: aliceID}}
	app, err := social.Open(ctx, social.Config[social.Principal]{
		Database: database, EventTransport: transport, Observer: result,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			if !principal.Development {
				return social.Actor{}, nil
			}
			return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
		},
		SnapshotPrincipal: func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:     func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal: func(principal social.Principal) string {
			return "p8-hook-computed-" + principal.DevUserID.String()
		},
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			result.mu.Lock()
			result.afterCommit = append(result.afterCommit, afterCommitRecord{
				Operation: failure.Operation(), Model: failure.Model(), Cause: failure.Cause(),
			})
			result.mu.Unlock()
		},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result.app = app
	seedUser(t, ctx, app.System(), aliceID, "alice", "alice@p8.test")
	seedUser(t, ctx, app.System(), mustUUID(t, bobIDText), "bob", "bob@p8.test")
	caller, err := app.ForPrincipal(ctx, result.principal)
	if err != nil {
		t.Fatal(err)
	}
	result.caller = caller
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) { return result.principal, true },
		ReportInternalError: func(_ context.Context, err error) {
			result.mu.Lock()
			result.trustedErrors = append(result.trustedErrors, err.Error())
			result.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result.graph, result.handler = graph, graph.Handler()
	return result
}

func (fixture *fixture) close() {
	fixture.mu.Lock()
	trusted := append([]string(nil), fixture.trustedErrors...)
	fixture.mu.Unlock()
	if len(trusted) != 0 {
		fixture.t.Errorf("unexpected trusted GraphQL errors: %v", trusted)
	}
	if err := fixture.graph.Shutdown(context.Background()); err != nil {
		fixture.t.Error(err)
	}
	if err := fixture.database.Close(); err != nil {
		fixture.t.Error(err)
	}
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
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func seedUser(t *testing.T, ctx context.Context, system social.System[social.Principal], id golem.UUID, handle, email string) {
	t.Helper()
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(id), social.Users.Handle.Create(handle), social.Users.Email.Create(email),
	)); err != nil {
		t.Fatal(err)
	}
}

func postInput(t *testing.T, id golem.UUID, title, body string, published bool) golem.CreateInput[social.Post] {
	t.Helper()
	return postInputWithAuthor(t, id, title, body, published, golem.UUID{})
}

func postInputWithAuthor(t *testing.T, id golem.UUID, title, body string, published bool, author golem.UUID) golem.CreateInput[social.Post] {
	t.Helper()
	values := []golem.CreateValue[social.Post]{
		social.Posts.ID.Create(id), social.Posts.Title.Create(title), social.Posts.Body.Create(body),
		social.Posts.Published.Create(published), social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")),
		social.Posts.LiveTime.Create(mustTime(t, "12:34:56")), social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
		social.Posts.Topics.Create(golem.List[string]{"p8", "oracle"}),
	}
	if author != (golem.UUID{}) {
		values = append(values, social.Posts.AuthorID.Create(author))
	}
	return social.Posts.Create(values...)
}

func graphPostInput(id, title, body string, published bool) map[string]any {
	return map[string]any{
		"id": id, "title": title, "body": body, "published": published,
		"liveDate": "2026-08-09", "liveTime": "12:34:56", "metadata": map[string]any{"language": "en", "pinned": false},
		"topics": []string{"p8", "oracle"},
	}
}

func (fixture *fixture) hookPhaseAndResult() {
	fixture.assertGraphQLRejectsClientAuthorAttribution()
	callerID := mustUUID(fixture.t, "92000000-0000-0000-0000-000000000001")
	callerTrace := &hookTrace{}
	ctx := social.WithPostHookObserver(fixture.ctx, callerTrace)
	row, err := fixture.caller.Posts.Create(ctx, postInput(fixture.t, callerID, "caller hook", "caller body", false),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body),
	)
	if err != nil {
		fixture.t.Fatal(err)
	}
	assertCreatedRow(fixture.t, row, callerID.String(), "caller hook")
	assertCreateHookTrace(fixture.t, "caller", callerTrace, callerID.String(), "caller hook")

	graphID := "92000000-0000-0000-0000-000000000002"
	graphTrace := &hookTrace{}
	response := fixture.graphQLContext(social.WithPostHookObserver(fixture.ctx, graphTrace), `mutation Create($data: PostCreateInput!) {
  createPost(data: $data) { id authorID title body }
}`, map[string]any{"data": graphPostInput(graphID, "GraphQL hook", "GraphQL body", false)})
	assertNoGraphErrors(fixture.t, response)
	post := graphObject(fixture.t, graphRoot(fixture.t, response)["createPost"])
	if post["id"] != graphID || post["authorID"] != aliceIDText || post["title"] != "GraphQL hook" || post["body"] != "GraphQL body" {
		fixture.t.Fatalf("GraphQL hook result=%v", post)
	}
	assertCreateHookTrace(fixture.t, "GraphQL", graphTrace, graphID, "GraphQL hook")

	rollbackID := mustUUID(fixture.t, "92000000-0000-0000-0000-000000000003")
	rollbackTrace := &hookTrace{}
	rollbackContext := social.WithPostHookObserver(fixture.ctx, rollbackTrace)
	sentinel := errors.New("P8 intentional rollback")
	err = fixture.caller.Transaction(rollbackContext, func(tx *social.CallerTx[social.Principal]) error {
		if _, createErr := tx.Posts.Create(rollbackContext, postInput(fixture.t, rollbackID, "rollback", "rollback body", false)); createErr != nil {
			return createErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		fixture.t.Fatalf("rollback error=%v", err)
	}
	rows, _, _ := rollbackTrace.snapshot()
	if phases(rows) != "before_create,after_create" {
		fixture.t.Fatalf("rollback hook phases=%s", phases(rows))
	}
	if count := fixture.directPostCount(rollbackID); count != 0 {
		fixture.t.Fatalf("rollback row count=%d", count)
	}

	fixture.assertReadHookSurfaces(callerID, "present")
	maskedID := mustUUID(fixture.t, "92000000-0000-0000-0000-000000000004")
	if _, err := fixture.app.System().Posts.Create(fixture.ctx,
		postInputWithAuthor(fixture.t, maskedID, "masked hook row", privateBodyCanary, true, mustUUID(fixture.t, bobIDText))); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.assertReadHookSurfaces(maskedID, "absent")
	fixture.assertCoordinatedUpsertDoesNotReplayCreateHooks()
}

func (fixture *fixture) assertGraphQLRejectsClientAuthorAttribution() {
	fixture.t.Helper()
	tests := []struct {
		name  string
		id    string
		field string
		value any
	}{
		{name: "scalar", id: "92000000-0000-0000-0000-000000000090", field: "authorID", value: bobIDText},
		{name: "relation", id: "92000000-0000-0000-0000-000000000091", field: "author", value: map[string]any{"connect": map[string]any{"ID": bobIDText}}},
	}
	for _, test := range tests {
		data := graphPostInput(test.id, "forged attribution", "forged body", false)
		data[test.field] = test.value
		response := fixture.graphQL(`mutation Create($data: PostCreateInput!) {
  createPost(data: $data) { id authorID }
}`, map[string]any{"data": data})
		if len(response.Errors) == 0 {
			fixture.t.Fatalf("GraphQL accepted client-owned %s attribution: data=%s", test.name, response.Data)
		}
		if count := fixture.directPostCount(mustUUID(fixture.t, test.id)); count != 0 {
			fixture.t.Fatalf("GraphQL client-owned %s attribution wrote %d rows", test.name, count)
		}
	}
	if trusted := fixture.takeTrustedErrors(); len(trusted) != 0 {
		fixture.t.Fatalf("GraphQL author-attribution preflight reported internal errors=%v", trusted)
	}
}

func (fixture *fixture) assertCoordinatedUpsertDoesNotReplayCreateHooks() {
	fixture.t.Helper()
	id := mustUUID(fixture.t, "92000000-0000-0000-0000-000000000005")
	traces := []*hookTrace{{}, {}}
	inputs := []golem.CreateInput[social.Post]{
		postInputWithAuthor(fixture.t, id, "coordinated create", "coordinated body", false, fixture.principal.DevUserID),
		postInputWithAuthor(fixture.t, id, "coordinated create", "coordinated body", false, fixture.principal.DevUserID),
	}
	errorsChannel := make(chan error, len(traces))
	var wait sync.WaitGroup
	for index, trace := range traces {
		index, trace := index, trace
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx := social.WithPostHookObserver(fixture.ctx, trace)
			_, err := fixture.caller.Posts.Upsert(ctx, social.Posts.ByID.Value(id),
				inputs[index],
				social.Posts.Update(social.Posts.Title.Set(fmt.Sprintf("coordinated update %d", index))),
			)
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			fixture.t.Fatalf("coordinated upsert: %v", err)
		}
	}
	var combined []hookRow
	for _, trace := range traces {
		rows, _, _ := trace.snapshot()
		combined = append(combined, rows...)
	}
	if got := phases(combined); got != "before_create,after_create,after_commit_create" {
		fixture.t.Fatalf("coordinated upsert replayed create hooks phases=%s rows=%+v", got, combined)
	}
	if count := fixture.directPostCount(id); count != 1 {
		fixture.t.Fatalf("coordinated upsert row count=%d", count)
	}
}

func (fixture *fixture) assertReadHookSurfaces(id golem.UUID, bodyState string) {
	callerTrace := &hookTrace{}
	ctx := social.WithPostReadHookObserver(fixture.ctx, callerTrace)
	rows, err := fixture.caller.Posts.FindMany(ctx,
		social.Posts.Where(social.Posts.ID.Eq(id)), social.Posts.Select(social.Posts.ID, social.Posts.Body),
	)
	if err != nil || len(rows) != 1 {
		fixture.t.Fatalf("caller read-hook rows=%d error=%v", len(rows), err)
	}
	assertReadTrace(fixture.t, "caller", callerTrace, id.String(), bodyState)

	graphTrace := &hookTrace{}
	response := fixture.graphQLContext(social.WithPostReadHookObserver(fixture.ctx, graphTrace),
		`query Read($id: UUID!) { posts(where: {id: {equals: $id}}, take: 1) { id body } }`, map[string]any{"id": id.String()})
	assertNoGraphErrors(fixture.t, response)
	graphRows, ok := graphRoot(fixture.t, response)["posts"].([]any)
	if !ok || len(graphRows) != 1 {
		fixture.t.Fatalf("GraphQL read-hook rows=%v", graphRows)
	}
	graphBody := graphObject(fixture.t, graphRows[0])["body"]
	if (bodyState == "present" && graphBody == nil) || (bodyState == "absent" && graphBody != nil) {
		fixture.t.Fatalf("GraphQL read-hook body=%v state=%s", graphBody, bodyState)
	}
	assertReadTrace(fixture.t, "GraphQL", graphTrace, id.String(), bodyState)

	customTrace := &hookTrace{}
	customContext := social.WithPostReadHookObserver(fixture.ctx, customTrace)
	if _, err := social.SearchPosts(customContext, fixture.caller, social.SearchPostsArgs{Where: social.Posts.ID.Eq(id), Take: 1}); err != nil {
		fixture.t.Fatal(err)
	}
	assertReadTrace(fixture.t, "custom query", customTrace, id.String(), bodyState)
}

func assertCreateHookTrace(t *testing.T, label string, trace *hookTrace, id, title string) {
	t.Helper()
	rows, _, _ := trace.snapshot()
	if phases(rows) != "before_create,after_create,after_commit_create" {
		t.Fatalf("%s hook phases=%s rows=%+v", label, phases(rows), rows)
	}
	if rows[0].ID != "" || rows[0].AuthorID != "" || rows[0].BodyState != "absent" {
		t.Fatalf("%s before hook received persisted result=%+v", label, rows[0])
	}
	for _, index := range []int{1, 2} {
		if rows[index].ID != id || rows[index].AuthorID != aliceIDText || rows[index].Title != title || rows[index].BodyState != "present" {
			t.Fatalf("%s hook row[%d]=%+v", label, index, rows[index])
		}
	}
}

func assertReadTrace(t *testing.T, label string, trace *hookTrace, id, bodyState string) {
	t.Helper()
	_, phases, rows := trace.snapshot()
	if !reflect.DeepEqual(phases, []social.PostReadHookPhase{social.PostHookBeforeFindMany, social.PostHookAfterFindMany}) {
		t.Fatalf("%s read phases=%v", label, phases)
	}
	if len(rows) != 2 || len(rows[0]) != 0 || len(rows[1]) != 1 || rows[1][0].ID != id || rows[1][0].BodyState != bodyState {
		t.Fatalf("%s authorized/masked hook results=%+v", label, rows)
	}
}

func (fixture *fixture) computedAndBatchedDisclosure() {
	ownerID := mustUUID(fixture.t, "93000000-0000-0000-0000-000000000001")
	privateDependencyID := mustUUID(fixture.t, "93000000-0000-0000-0000-000000000002")
	if _, err := fixture.app.System().Posts.Create(fixture.ctx,
		postInputWithAuthor(fixture.t, ownerID, "owner computed", "owner visible body", true, fixture.principal.DevUserID)); err != nil {
		fixture.t.Fatal(err)
	}
	if _, err := fixture.app.System().Posts.Create(fixture.ctx,
		postInputWithAuthor(fixture.t, privateDependencyID, "masked computed", privateBodyCanary, true, mustUUID(fixture.t, bobIDText))); err != nil {
		fixture.t.Fatal(err)
	}

	owner := fixture.graphQL(`query Owner($id: UUID!) {
  post(where: {ID: $id}) { id body excerpt(maximum: 5) }
}`, map[string]any{"id": ownerID.String()})
	assertNoGraphErrors(fixture.t, owner)
	ownerPost := graphObject(fixture.t, graphRoot(fixture.t, owner)["post"])
	if ownerPost["body"] != "owner visible body" || ownerPost["excerpt"] != "owner" {
		fixture.t.Fatalf("owner computed result=%v", ownerPost)
	}

	masked := fixture.graphQL(`query Masked($id: UUID!) {
  post(where: {ID: $id}) { id body excerpt(maximum: 8) }
}`, map[string]any{"id": privateDependencyID.String()})
	encoded, _ := json.Marshal(masked)
	if bytes.Contains(encoded, []byte(privateBodyCanary)) {
		fixture.t.Fatalf("masked computed dependency disclosed: %s", encoded)
	}
	maskedPost := graphObject(fixture.t, graphRoot(fixture.t, masked)["post"])
	if maskedPost["body"] != nil || maskedPost["excerpt"] != nil {
		fixture.t.Fatalf("masked computed shape=%v", maskedPost)
	}
	if len(masked.Errors) != 1 || masked.Errors[0].Extensions["code"] == nil {
		fixture.t.Fatalf("masked dependency stable error=%+v", masked.Errors)
	}
	if masked.Errors[0].Message != "internal server error" || masked.Errors[0].Extensions["code"] != "INTERNAL_SERVER_ERROR" {
		fixture.t.Fatalf("masked dependency public classification=%+v", masked.Errors[0])
	}
	trusted := fixture.takeTrustedErrors()
	if len(trusted) != 1 || !strings.Contains(trusted[0], "masked dependency body") || strings.Contains(trusted[0], privateBodyCanary) {
		fixture.t.Fatalf("masked dependency trusted classification=%v", trusted)
	}

	aliasTrace := &displayTrace{}
	aliasContext := social.WithPostDisplayCodeLoaderObserver(fixture.ctx, aliasTrace)
	alias := fixture.graphQLContext(aliasContext, `query Aliases($id: UUID!) {
  first: post(where: {ID: $id}) { displayCode(prefix: "same:") }
  second: post(where: {ID: $id}) { displayCode(prefix: "same:") }
}`, map[string]any{"id": ownerID.String()})
	assertNoGraphErrors(fixture.t, alias)
	wantLoad := []displayLoad{{Keys: []string{ownerID.String()}, Prefix: "same:"}}
	if loads := aliasTrace.snapshot(); !reflect.DeepEqual(loads, wantLoad) {
		fixture.t.Fatalf("operation-local alias cache loads=%v want=%v", loads, wantLoad)
	}

	var wait sync.WaitGroup
	errorsChannel := make(chan error, concurrentLoaderOperations)
	for index := 0; index < concurrentLoaderOperations; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			prefix := fmt.Sprintf("request-%02d:", index)
			trace := &displayTrace{}
			ctx := social.WithPostDisplayCodeLoaderObserver(fixture.ctx, trace)
			response := fixture.graphQLContext(ctx, `query Batch($id: UUID!, $prefix: String!) {
  post(where: {ID: $id}) { displayCode(prefix: $prefix) }
}`, map[string]any{"id": ownerID.String(), "prefix": prefix})
			if len(response.Errors) != 0 {
				errorsChannel <- fmt.Errorf("%s errors=%v", prefix, response.Errors)
				return
			}
			want := []displayLoad{{Keys: []string{ownerID.String()}, Prefix: prefix}}
			if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
				errorsChannel <- fmt.Errorf("%s loads=%v want=%v", prefix, got, want)
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		fixture.t.Error(err)
	}
	fixture.assertComputedObservations()
}

func (fixture *fixture) assertComputedObservations() {
	fixture.mu.Lock()
	values := append([]observedOperation(nil), fixture.observations...)
	fixture.mu.Unlock()
	var computed, computedSuccess, computedFailure, batched int
	for _, value := range values {
		switch value.Operation {
		case observe.OperationGraphQLComputed:
			computed++
			if value.Kind != observe.KindGraphQL || value.Statements != 0 || value.Aggregate != 1 {
				fixture.t.Fatalf("computed observation=%+v", value)
			}
			switch value.Outcome {
			case observe.OutcomeSuccess:
				computedSuccess++
			case observe.OutcomeFailure:
				computedFailure++
			default:
				fixture.t.Fatalf("computed observation outcome=%+v", value)
			}
		case observe.OperationGraphQLBatchedComputed:
			batched++
			if value.Kind != observe.KindGraphQL || value.Outcome != observe.OutcomeSuccess || value.Statements != 0 || value.Aggregate != 1 {
				fixture.t.Fatalf("batched observation=%+v", value)
			}
		}
	}
	if computed != 2 || computedSuccess != 1 || computedFailure != 1 || batched != 1+concurrentLoaderOperations {
		fixture.t.Fatalf("computed observations computed=%d success=%d failure=%d batched=%d all=%+v", computed, computedSuccess, computedFailure, batched, values)
	}
}

func (fixture *fixture) afterCommitFailure() {
	cause := errors.New("P8_TRUSTED_AFTER_COMMIT_FAILURE_CANARY")
	callerID := mustUUID(fixture.t, "94000000-0000-0000-0000-000000000001")
	trace := &hookTrace{failAfterCommit: cause}
	ctx := social.WithPostHookObserver(fixture.ctx, trace)
	row, err := fixture.caller.Posts.Create(ctx, postInput(fixture.t, callerID, "caller committed", "caller committed body", false),
		social.Posts.Select(social.Posts.ID, social.Posts.AuthorID, social.Posts.Title),
	)
	if err != nil {
		fixture.t.Fatalf("after-commit failure changed caller result: %v", err)
	}
	assertCreatedRow(fixture.t, row, callerID.String(), "caller committed")
	assertCommittedFailure(fixture.t, fixture, callerID, cause, 1)

	graphID := "94000000-0000-0000-0000-000000000002"
	graphTrace := &hookTrace{failAfterCommit: cause}
	response := fixture.graphQLContext(social.WithPostHookObserver(fixture.ctx, graphTrace), `mutation Create($data: PostCreateInput!) {
  createPost(data: $data) { id title }
}`, map[string]any{"data": graphPostInput(graphID, "GraphQL committed", "GraphQL committed body", false)})
	if len(response.Errors) != 0 {
		fixture.t.Fatalf("after-commit failure changed GraphQL result=%+v", response.Errors)
	}
	encoded, _ := json.Marshal(response)
	if bytes.Contains(encoded, []byte(cause.Error())) {
		fixture.t.Fatalf("GraphQL disclosed trusted after-commit cause: %s", encoded)
	}
	post := graphObject(fixture.t, graphRoot(fixture.t, response)["createPost"])
	if post["id"] != graphID || post["title"] != "GraphQL committed" {
		fixture.t.Fatalf("GraphQL committed result=%v", post)
	}
	assertCommittedFailure(fixture.t, fixture, mustUUID(fixture.t, graphID), cause, 2)
	assertCreateHookTrace(fixture.t, "caller after-commit failure", trace, callerID.String(), "caller committed")
	assertCreateHookTrace(fixture.t, "GraphQL after-commit failure", graphTrace, graphID, "GraphQL committed")
}

func assertCommittedFailure(t *testing.T, fixture *fixture, id golem.UUID, cause error, wantReports int) {
	t.Helper()
	if count := fixture.directPostCount(id); count != 1 {
		t.Fatalf("committed row count=%d", count)
	}
	fixture.mu.Lock()
	reports := append([]afterCommitRecord(nil), fixture.afterCommit...)
	fixture.mu.Unlock()
	if len(reports) != wantReports {
		t.Fatalf("after-commit reports=%+v want=%d", reports, wantReports)
	}
	last := reports[len(reports)-1]
	if last.Operation != golem.HookCreate || last.Model != social.GolemGeneratedPostDescriptor.Metadata().ModelID() || !errors.Is(last.Cause, cause) {
		t.Fatalf("after-commit report=%+v", last)
	}
}

func (fixture *fixture) directPostCount(id golem.UUID) int {
	fixture.t.Helper()
	query := fixture.database.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM posts WHERE id=?`)
	var count int
	if err := fixture.database.UnsafeSQLX().GetContext(fixture.ctx, &count, query, id.String()); err != nil {
		fixture.t.Fatal(err)
	}
	return count
}

func (fixture *fixture) takeTrustedErrors() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	result := append([]string(nil), fixture.trustedErrors...)
	fixture.trustedErrors = nil
	return result
}

func phases(rows []hookRow) string {
	result := make([]string, len(rows))
	for index, row := range rows {
		result[index] = string(row.Phase)
	}
	return strings.Join(result, ",")
}

func assertCreatedRow(t *testing.T, row golem.Row[social.Post], id, title string) {
	t.Helper()
	gotID, idPresent := golem.Value(row, social.Posts.ID).Get()
	gotAuthor, authorPresent := golem.Value(row, social.Posts.AuthorID).Get()
	gotTitle, titlePresent := golem.Value(row, social.Posts.Title).Get()
	if !idPresent || !authorPresent || !titlePresent || gotID.String() != id || gotAuthor.String() != aliceIDText || gotTitle != title {
		t.Fatalf("created row id=%v/%t author=%v/%t title=%q/%t", gotID, idPresent, gotAuthor, authorPresent, gotTitle, titlePresent)
	}
}

func (fixture *fixture) graphQL(query string, variables map[string]any) graphResponse {
	return fixture.graphQLContext(fixture.ctx, query, variables)
}

func (fixture *fixture) graphQLContext(ctx context.Context, query string, variables map[string]any) graphResponse {
	fixture.t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		fixture.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		fixture.t.Fatalf("decode GraphQL status=%d body=%s: %v", recorder.Code, recorder.Body.String(), err)
	}
	return response
}

func graphRoot(t *testing.T, response graphResponse) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatalf("decode GraphQL data %s: %v", response.Data, err)
	}
	return result
}

func graphObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL value is %T: %v", value, value)
	}
	return result
}

func assertNoGraphErrors(t *testing.T, response graphResponse) {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors=%+v data=%s", response.Errors, response.Data)
	}
}

func assertResolverCapabilities(t *testing.T) {
	t.Helper()
	root := os.Getenv("P8_ORACLE_EXAMPLE")
	files := []string{filepath.Join(root, "social", "extensions.go"), filepath.Join(root, "social", "hooks.go")}
	functions := map[string]*ast.FuncDecl{}
	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, specification := range parsed.Imports {
			value := strings.Trim(specification.Path.Value, `"`)
			if strings.Contains(value, "/internal/") || strings.Contains(value, "sqlx") || strings.Contains(value, "/provider") {
				t.Fatalf("authored resolver file %s imports capability %q", filepath.Base(path), value)
			}
		}
		parsed, err = parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok {
				functions[function.Name.Name] = function
			}
		}
	}
	assertFunctionParameters(t, functions, "SearchPosts", []string{"context.Context", "*Caller[Principal]", "SearchPostsArgs"})
	assertFunctionParameters(t, functions, "PublishPost", []string{"context.Context", "*Caller[Principal]", "PublishPostArgs"})
	assertFunctionParameters(t, functions, "Excerpt", []string{"context.Context", "golem.Row[Post]", "ExcerptArgs"})
	assertFunctionParameters(t, functions, "LoadPostDisplayCodes", []string{"context.Context", "[]golem.UUID", "DisplayCodeArgs"})
	assertFunctionParameters(t, functions, "BeforeCreate", []string{"context.Context", "*PostCreateRequest"})
	assertFunctionParameters(t, functions, "AfterCreate", []string{"context.Context", "PostCreateResult"})
	assertFunctionParameters(t, functions, "AfterCommitCreate", []string{"context.Context", "PostCreateResult"})

	for _, name := range []string{"SearchPosts", "PublishPost", "Excerpt", "LoadPostDisplayCodes", "BeforeCreate", "AfterCreate", "AfterCommitCreate"} {
		function := functions[name]
		ast.Inspect(function.Body, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "UnsafeSQLX", "Database", "System":
				t.Fatalf("resolver %s reaches forbidden capability %s", name, selector.Sel.Name)
			}
			return true
		})
	}
}

func assertFunctionParameters(t *testing.T, functions map[string]*ast.FuncDecl, name string, want []string) {
	t.Helper()
	function := functions[name]
	if function == nil {
		t.Fatalf("missing authored function %s", name)
	}
	var got []string
	for _, field := range function.Type.Params.List {
		var buffer bytes.Buffer
		if err := formatNode(&buffer, field.Type); err != nil {
			t.Fatal(err)
		}
		for range field.Names {
			got = append(got, buffer.String())
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s parameters=%v want=%v", name, got, want)
	}
}

func formatNode(buffer *bytes.Buffer, node ast.Node) error {
	return formatNodeWithSet(token.NewFileSet(), buffer, node)
}

func formatNodeWithSet(set *token.FileSet, buffer *bytes.Buffer, node ast.Node) error {
	return printer.Fprint(buffer, set, node)
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
