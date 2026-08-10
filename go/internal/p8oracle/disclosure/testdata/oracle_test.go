package disclosureconsumer

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
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

const (
	aliceIDText      = "a1000000-0000-0000-0000-000000000001"
	bobIDText        = "a1000000-0000-0000-0000-000000000002"
	alicePostIDText  = "a2000000-0000-0000-0000-000000000001"
	bobPublicIDText  = "a2000000-0000-0000-0000-000000000002"
	bobPrivateIDText = "a2000000-0000-0000-0000-000000000003"
	missingIDText    = "a2000000-0000-0000-0000-000000000099"
)

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

type closedObservation struct {
	Kind       observe.Kind      `json:"kind"`
	Operation  observe.Operation `json:"operation"`
	Outcome    observe.Outcome   `json:"outcome"`
	Reason     observe.Reason    `json:"reason"`
	Statements int               `json:"statements"`
	Aggregate  int64             `json:"aggregate"`
}

type closedOperatorAudit struct {
	Action     events.OperatorAuditAction  `json:"action"`
	Outcome    events.OperatorAuditOutcome `json:"outcome"`
	Causations int                         `json:"causations"`
	Facts      int                         `json:"facts"`
}

type fixture struct {
	t              testing.TB
	ctx            context.Context
	database       *provider.Database
	app            *social.App[social.Principal]
	caller         *social.Caller[social.Principal]
	graph          *social.GraphQLServer
	handler        http.Handler
	principal      social.Principal
	principalToken string
	rowCanary      string
	errorCanary    string
	eventCanary    string
	mu             sync.Mutex
	trusted        []string
	observed       []closedObservation
	scopedAudits   []golem.ScopedAuditRecord
	operatorAudits []closedOperatorAudit
	publisherStop  context.CancelFunc
	publisherDone  chan error
}

func (fixture *fixture) ObserveGolem(_ context.Context, value observe.Observation) {
	fixture.mu.Lock()
	fixture.observed = append(fixture.observed, closedObservation{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Reason: value.Reason(),
		Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
	})
	fixture.mu.Unlock()
}

func TestP8ExternalOracleScenario(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	scenario := os.Getenv("P8_ORACLE_SCENARIO")
	switch {
	case scenario == "caller-graphql-events":
		fixture.callerGraphQLEvents()
	case scenario == "missing-invisible-masked":
		fixture.missingInvisibleMasked()
	case scenario == "hook-computed-custom-analytics":
		fixture.hookComputedCustomAnalytics()
	default:
		t.Fatalf("unknown disclosure scenario %q", scenario)
	}
}

func FuzzP8ExternalPublicInput(f *testing.F) {
	for _, seed := range [][]byte{
		{0}, {0, '{'}, {1}, {1, 'b', 'o', 'b'}, {2}, {3}, {4}, {5},
		{0, '\x00', '\xff', '{', '}'}, {1, 'P', '8', '-', 'l', 'o', 'o', 'k', 'i', 'n', 'g'},
	} {
		f.Add(seed)
	}
	fixture := newFixture(f)
	fixture.seedPosts()
	f.Cleanup(func() {
		fixture.assertProtectedStateIntact()
		fixture.assertClosedChannels()
		fixture.close()
	})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 128 {
			input = input[:128]
		}
		fixture.fuzzPublicInput(t, input)
	})
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	ctx := context.Background()
	database := openDatabase(t, ctx)
	clearDisclosureState(t, ctx, database)
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	principalBytes, rowCanary, errorCanary, eventCanary := disclosureCanaries(t)
	var tokenHash [32]byte
	copy(tokenHash[:], principalBytes)
	aliceID := mustUUID(t, aliceIDText)
	result := &fixture{
		t: t, ctx: ctx, database: database,
		principal:      social.Principal{TokenHash: tokenHash, Development: true, DevUserID: aliceID},
		principalToken: hex.EncodeToString(principalBytes), rowCanary: rowCanary,
		errorCanary: errorCanary, eventCanary: eventCanary,
	}
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
			payload := append([]byte("p8-disclosure-principal-v1\x00"), principal.TokenHash[:]...)
			digest := sha256.Sum256(payload)
			return hex.EncodeToString(digest[:16])
		},
		ReportScopedQuery: func(_ context.Context, record golem.ScopedAuditRecord) {
			result.mu.Lock()
			result.scopedAudits = append(result.scopedAudits, record)
			result.mu.Unlock()
		},
		ReportEventOperator: func(_ context.Context, record events.OperatorAuditRecord) {
			result.mu.Lock()
			result.operatorAudits = append(result.operatorAudits, closedOperatorAudit{
				Action: record.Action(), Outcome: record.Outcome(),
				Causations: record.Causations(), Facts: record.Facts(),
			})
			result.mu.Unlock()
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result.app = app
	if os.Getenv("P8_ORACLE_SCENARIO") == "caller-graphql-events" {
		publisherContext, stopPublisher := context.WithCancel(ctx)
		result.publisherStop = stopPublisher
		result.publisherDone = make(chan error, 1)
		go func() { result.publisherDone <- app.RunEventPublisher(publisherContext) }()
		deadline := time.Now().Add(3 * time.Second)
		for !app.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !app.EventCapabilities().PublisherRunning() {
			stopPublisher()
			_ = database.Close()
			t.Fatal("event publisher did not start")
		}
	}
	seedUser(t, ctx, app.System(), aliceID, "alice", "alice@disclosure.test")
	seedUser(t, ctx, app.System(), mustUUID(t, bobIDText), "bob", result.rowCanary+"@disclosure.test")
	caller, err := app.ForPrincipal(ctx, result.principal)
	if err != nil {
		t.Fatal(err)
	}
	result.caller = caller
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) { return result.principal, true },
		ReportInternalError: func(_ context.Context, err error) {
			result.mu.Lock()
			result.trusted = append(result.trusted, err.Error())
			result.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result.graph, result.handler = graph, graph.Handler()
	return result
}

func clearDisclosureState(t testing.TB, ctx context.Context, database *provider.Database) {
	t.Helper()
	statements := []string{
		`DELETE FROM post_tags`, `DELETE FROM comments`, `DELETE FROM sessions`,
		`DELETE FROM posts`, `DELETE FROM tags`, `DELETE FROM users`,
	}
	if database.Provider() == golem.PostgreSQL {
		statements = append(statements, `DELETE FROM "_golem"."_golem_outbox_delivery"`, `DELETE FROM "_golem"."_golem_outbox"`)
	} else {
		statements = append(statements, `DELETE FROM _golem_outbox_delivery`, `DELETE FROM _golem_outbox`)
	}
	for _, statement := range statements {
		if _, err := database.UnsafeSQLX().ExecContext(ctx, statement); err != nil {
			_ = database.Close()
			t.Fatalf("reset disclosure fuzz state: %v", err)
		}
	}
}

func (fixture *fixture) close() {
	if trusted := fixture.takeTrusted(); len(trusted) != 0 {
		fixture.t.Errorf("unconsumed trusted GraphQL errors=%v", trusted)
	}
	if err := fixture.graph.Shutdown(context.Background()); err != nil {
		fixture.t.Error(err)
	}
	if fixture.publisherStop != nil {
		fixture.publisherStop()
		select {
		case <-fixture.publisherDone:
		case <-time.After(3 * time.Second):
			fixture.t.Error("event publisher did not stop")
		}
	}
	if err := fixture.database.Close(); err != nil {
		fixture.t.Error(err)
	}
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

func (fixture *fixture) seedPosts() {
	fixture.t.Helper()
	system := fixture.app.System()
	posts := []struct {
		id, author, title, body string
		published               bool
	}{
		{alicePostIDText, aliceIDText, "alice protected", "alice-owned-body", false},
		{bobPublicIDText, bobIDText, "bob public", fixture.eventCanary, true},
		{bobPrivateIDText, bobIDText, fixture.rowCanary, fixture.rowCanary + "-private-body", false},
	}
	for _, post := range posts {
		if _, err := system.Posts.Create(fixture.ctx, postInput(fixture.t, post.id, post.author, post.title, post.body, post.published)); err != nil {
			fixture.t.Fatal(err)
		}
	}
}

func (fixture *fixture) callerGraphQLEvents() {
	callerStream, err := fixture.caller.Posts.Events(fixture.ctx,
		golem.EventWhere(social.Posts.ID.In(mustUUID(fixture.t, bobPrivateIDText), mustUUID(fixture.t, bobPublicIDText))),
		golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		fixture.t.Fatal(err)
	}

	server, connection := fixture.openGraphQLSubscription()
	type callerResult struct {
		event social.PostEvent
		err   error
	}
	callerResults := make(chan callerResult, 1)
	go func() {
		receiveContext, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
		defer cancel()
		event, receiveErr := callerStream.Recv(receiveContext)
		callerResults <- callerResult{event: event, err: receiveErr}
	}()
	type graphResult struct {
		frame wsFrame
		err   error
	}
	graphResults := make(chan graphResult, 1)
	go func() {
		frame, receiveErr := awaitGraphQLEvent(connection, bobPublicIDText, append(fixture.canaries(), bobPrivateIDText)...)
		graphResults <- graphResult{frame: frame, err: receiveErr}
	}()
	fixture.waitSubscriptionMembership(2)
	fixture.seedPosts()

	callerReceived := <-callerResults
	event, err := callerReceived.event, callerReceived.err
	if err != nil || event.ID().String() != bobPublicIDText {
		fixture.t.Fatalf("caller event id=%s error=%v", event.ID(), err)
	}
	entity, present := event.Entity()
	if !present {
		fixture.t.Fatal("caller event entity is absent")
	}
	if _, bodyPresent := golem.Value(entity, social.Posts.Body).Get(); bodyPresent {
		fixture.t.Fatal("caller event exposed conditionally masked body")
	}
	assertNoCanary(fixture.t, "caller event", mustJSONEncode(event.ID().String()), fixture.canaries()...)

	quietContext, stop := context.WithTimeout(fixture.ctx, 150*time.Millisecond)
	if trailing, trailingErr := callerStream.Recv(quietContext); trailingErr == nil {
		stop()
		fixture.t.Fatalf("caller stream delivered unauthorized trailing event %s", trailing.ID())
	}
	stop()

	graphReceived := <-graphResults
	if graphReceived.err != nil {
		fixture.t.Fatal(graphReceived.err)
	}
	frame := graphReceived.frame
	assertNoCanary(fixture.t, "GraphQL event", frame.Payload, fixture.canaries()...)
	var payload map[string]any
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		fixture.t.Fatal(err)
	}
	if bytes.Contains(frame.Payload, []byte(`"body":"`)) {
		fixture.t.Fatalf("GraphQL event encoded masked body=%s", frame.Payload)
	}

	response := fixture.graphQL(`query { posts(orderBy: [{id: asc}], take: 20) { id title body } }`, nil)
	assertNoGraphErrors(fixture.t, response)
	assertNoCanary(fixture.t, "GraphQL read", mustJSONEncode(response), fixture.rowCanary, fixture.eventCanary, fixture.principalToken)
	rows, err := fixture.caller.Posts.FindMany(fixture.ctx,
		social.Posts.OrderBy(social.Posts.ID.Asc()), social.Posts.Take(20),
		social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.Body),
	)
	if err != nil || len(rows) != 2 {
		fixture.t.Fatalf("caller authorized rows=%d error=%v", len(rows), err)
	}
	for _, row := range rows {
		if id, _ := golem.Value(row, social.Posts.ID).Get(); id.String() == bobPrivateIDText {
			fixture.t.Fatal("caller read exposed invisible row")
		}
		if body, present := golem.Value(row, social.Posts.Body).Get(); present && strings.Contains(body, fixture.eventCanary) {
			fixture.t.Fatal("caller read exposed masked body canary")
		}
	}
	fixture.assertProtectedStateIntact()
	fixture.assertClosedChannels()
	_ = callerStream.Close()
	_ = connection.Close()
	server.Close()
	fixture.assertSafeGraphQLCancellation()
}

func (fixture *fixture) assertSafeGraphQLCancellation() {
	fixture.t.Helper()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		fixture.mu.Lock()
		count := len(fixture.trusted)
		fixture.mu.Unlock()
		if count != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	trusted := fixture.takeTrusted()
	// A clean close need not be reported as an internal error. If the
	// transport observes the cancellation race, the trusted report must be the
	// stable, data-free classification and nothing else.
	if len(trusted) > 1 || len(trusted) == 1 && trusted[0] != "GOLEM_SUBSCRIPTION_CANCELLED" {
		fixture.t.Fatalf("GraphQL cancellation trusted reports=%v", trusted)
	}
	if len(trusted) == 1 {
		assertNoCanary(fixture.t, "GraphQL cancellation", []byte(trusted[0]), fixture.canaries()...)
	}
}

func (fixture *fixture) waitSubscriptionMembership(want int) {
	fixture.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fixture.mu.Lock()
		count := 0
		for _, value := range fixture.observed {
			if value.Operation == observe.OperationSubscriptionMembership && value.Outcome == observe.OutcomeSuccess {
				count++
			}
		}
		fixture.mu.Unlock()
		if count >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	fixture.t.Fatalf("subscription memberships did not reach %d", want)
}

func (fixture *fixture) openGraphQLSubscription() (*httptest.Server, *websocket.Conn) {
	fixture.t.Helper()
	server := httptest.NewServer(fixture.handler)
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		fixture.t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		fixture.t.Fatal(err)
	}
	readFrame(fixture.t, connection, "connection_ack")
	if err := connection.WriteJSON(map[string]any{
		"id": "disclosure", "type": "subscribe",
		"payload": map[string]any{"query": `subscription {
  postEvents(where: {id: {in: ["` + bobPrivateIDText + `", "` + bobPublicIDText + `"]}}) {
    type id entity { id title body published }
  }
}`},
	}); err != nil {
		fixture.t.Fatal(err)
	}
	return server, connection
}

func (fixture *fixture) missingInvisibleMasked() {
	fixture.seedPosts()
	invisible := fixture.findError(mustUUID(fixture.t, bobPrivateIDText))
	missing := fixture.findError(mustUUID(fixture.t, missingIDText))
	if invisible.Code != golem.CodeNotFound || missing.Code != golem.CodeNotFound || invisible.Error() != missing.Error() {
		fixture.t.Fatalf("caller missing/invisible differ invisible=%v missing=%v", invisible, missing)
	}
	response := fixture.graphQL(`query Compare {
  invisible: post(where: {ID: "`+bobPrivateIDText+`"}) { id title body }
  missing: post(where: {ID: "`+missingIDText+`"}) { id title body }
  masked: post(where: {ID: "`+bobPublicIDText+`"}) { id title body }
}`, nil)
	assertNoGraphErrors(fixture.t, response)
	root := graphRoot(fixture.t, response)
	if root["invisible"] != nil || root["missing"] != nil {
		fixture.t.Fatalf("GraphQL missing/invisible data=%s", response.Data)
	}
	masked := graphObject(fixture.t, root["masked"])
	if masked["body"] != nil || masked["id"] != bobPublicIDText {
		fixture.t.Fatalf("GraphQL masked shape=%v", masked)
	}
	assertNoCanary(fixture.t, "missing/invisible/masked GraphQL", mustJSONEncode(response), fixture.canaries()...)

	invisibleBuckets, missingBuckets := make([]int, 24), make([]int, 24)
	for index := range invisibleBuckets {
		measure := func(id string) int {
			start := time.Now()
			_ = fixture.findError(mustUUID(fixture.t, id))
			return durationBucket(time.Since(start))
		}
		if index%2 == 0 {
			invisibleBuckets[index], missingBuckets[index] = measure(bobPrivateIDText), measure(missingIDText)
		} else {
			missingBuckets[index], invisibleBuckets[index] = measure(missingIDText), measure(bobPrivateIDText)
		}
	}
	if difference := medianBucket(invisibleBuckets) - medianBucket(missingBuckets); difference < -2 || difference > 2 {
		fixture.t.Fatalf("coarse timing buckets diverge invisible=%v missing=%v", invisibleBuckets, missingBuckets)
	}
	fixture.assertProtectedStateIntact()
	fixture.assertClosedChannels()
}

func (fixture *fixture) hookComputedCustomAnalytics() {
	fixture.seedPosts()
	reject := rejectingReadHook{cause: errors.New(fixture.errorCanary)}
	ctx := social.WithPostReadHookObserver(fixture.ctx, reject)
	_, callerErr := fixture.caller.Posts.FindMany(ctx, social.Posts.Take(2))
	assertPublicErrorNoCanary(fixture.t, "caller hook", callerErr, fixture.errorCanary)
	_, customErr := social.SearchPosts(ctx, fixture.caller, social.SearchPostsArgs{Where: golem.All[social.Post](), Take: 2})
	assertPublicErrorNoCanary(fixture.t, "custom hook", customErr, fixture.errorCanary)
	hookGraph := fixture.graphQLContext(ctx, `query { posts(take: 2) { id } searchPosts(where: {all: true}, take: 2) { id } }`, nil)
	assertNoCanary(fixture.t, "GraphQL hook", mustJSONEncode(hookGraph), fixture.errorCanary)
	if len(hookGraph.Errors) == 0 {
		fixture.t.Fatal("GraphQL hook rejection has no public errors")
	}

	computed := fixture.graphQL(`query { post(where: {ID: "`+bobPublicIDText+`"}) { id body excerpt(maximum: 8) } }`, nil)
	assertNoCanary(fixture.t, "computed dependency", mustJSONEncode(computed), fixture.eventCanary, fixture.rowCanary)
	computedPost := graphObject(fixture.t, graphRoot(fixture.t, computed)["post"])
	if computedPost["body"] != nil || computedPost["excerpt"] != nil || len(computed.Errors) != 1 {
		fixture.t.Fatalf("computed masked result=%v errors=%v", computedPost, computed.Errors)
	}
	trusted := fixture.takeTrusted()
	if len(trusted) != 1 || strings.Contains(trusted[0], fixture.eventCanary) {
		fixture.t.Fatalf("computed trusted channel=%v", trusted)
	}

	custom, err := social.SearchPosts(fixture.ctx, fixture.caller, social.SearchPostsArgs{
		Where: social.Posts.ID.Eq(mustUUID(fixture.t, bobPrivateIDText)), Take: 5,
	})
	if err != nil || len(custom) != 0 {
		fixture.t.Fatalf("custom private result rows=%d error=%v", len(custom), err)
	}
	customGraph := fixture.graphQL(`query { searchPosts(where: {id: {equals: "`+bobPrivateIDText+`"}}, take: 5) { id title body } }`, nil)
	assertNoGraphErrors(fixture.t, customGraph)
	assertNoCanary(fixture.t, "custom GraphQL", mustJSONEncode(customGraph), fixture.canaries()...)

	countMeasure := social.Posts.CountAll()
	aggregate, err := fixture.caller.Posts.Aggregate(fixture.ctx,
		social.Posts.Aggregate(social.Posts.AggregateSelect(countMeasure)),
	)
	if err != nil {
		fixture.t.Fatal(err)
	}
	count, present := golem.AggregateValue(aggregate, countMeasure).Get()
	if !present || count != 2 {
		fixture.t.Fatalf("authorized aggregate count=%d present=%t", count, present)
	}
	graphAggregate := fixture.graphQL(`query { aggregatePosts { count } }`, nil)
	assertNoGraphErrors(fixture.t, graphAggregate)
	assertNoCanary(fixture.t, "GraphQL analytics", mustJSONEncode(graphAggregate), fixture.canaries()...)
	fixture.assertProtectedStateIntact()
	fixture.assertClosedChannels()
}

func (fixture *fixture) fuzzPublicInput(t testing.TB, input []byte) {
	t.Helper()
	fixture.resetClosedChannels()
	discriminator, payload := byte(0), input
	if len(input) != 0 {
		discriminator, payload = input[0]%6, input[1:]
	}
	value := string(payload)
	switch discriminator {
	case 0:
		// The bytes are the GraphQL document itself, not merely data that is
		// encoded and inspected locally. This covers malformed and valid parser
		// input through the public HTTP handler.
		response := fixture.graphQLFor(t, value, map[string]any{"input": value})
		assertNoCanary(t, "fuzz GraphQL document", mustJSONEncode(response), fixture.canaries()...)
	case 1:
		response := fixture.graphQLFor(t, `query Search($value: String!) {
  posts(where: {title: {contains: $value}}, take: 3) { id title body }
}`, map[string]any{"value": value})
		assertNoCanary(t, "fuzz GraphQL variable", mustJSONEncode(response), fixture.canaries()...)
	case 2:
		rows, err := fixture.caller.Posts.FindMany(fixture.ctx,
			social.Posts.Where(social.Posts.Title.Contains(value)),
			social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.Body), social.Posts.Take(3),
		)
		if err != nil {
			assertPublicErrorNoCanary(t, "fuzz Caller predicate", err, fixture.canaries()...)
		} else {
			fixture.assertPostRowsSafe(t, "fuzz Caller predicate", rows)
		}
	case 3:
		rows, err := social.SearchPosts(fixture.ctx, fixture.caller, social.SearchPostsArgs{
			Where: social.Posts.Title.Contains(value), Take: 3,
		})
		if err != nil {
			assertPublicErrorNoCanary(t, "fuzz custom query", err, fixture.canaries()...)
		} else {
			fixture.assertPostRowsSafe(t, "fuzz custom query", rows)
		}
	case 4:
		posts := social.Posts.Scope()
		title, body := social.Posts.Title.At(posts), social.Posts.Body.At(posts)
		rows, err := fixture.caller.Posts.Scoped(fixture.ctx,
			golem.From(posts).Where(title.Contains(value)).Select(title, body).Take(3),
		)
		if err != nil {
			assertPublicErrorNoCanary(t, "fuzz scoped query", err, fixture.canaries()...)
		} else {
			for _, row := range rows {
				if result, present := golem.ScopedValue(row, title).Get(); present {
					assertNoCanary(t, "fuzz scoped title", []byte(result), fixture.canaries()...)
				}
				if result, present := golem.ScopedValue(row, body).Get(); present {
					assertNoCanary(t, "fuzz scoped body", []byte(result), fixture.canaries()...)
				}
			}
		}
	case 5:
		stream, err := fixture.caller.Posts.Events(fixture.ctx,
			golem.EventWhere(social.Posts.Title.Contains(value)),
			golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title, social.Posts.Body),
		)
		if err != nil {
			assertPublicErrorNoCanary(t, "fuzz event filter", err, fixture.canaries()...)
		} else if err := stream.Close(); err != nil {
			assertPublicErrorNoCanary(t, "fuzz event close", err, fixture.canaries()...)
		}
	}
	for _, trusted := range fixture.takeTrusted() {
		assertNoCanary(t, "fuzz trusted error", []byte(trusted), fixture.canaries()...)
	}
	fixture.assertClosedChannelsFor(t)
}

func (fixture *fixture) assertPostRowsSafe(t testing.TB, channel string, rows []golem.Row[social.Post]) {
	t.Helper()
	for _, row := range rows {
		if value, present := golem.Value(row, social.Posts.ID).Get(); present {
			assertNoCanary(t, channel+" id", []byte(value.String()), fixture.canaries()...)
			if value.String() == bobPrivateIDText {
				t.Fatalf("%s exposed an invisible post", channel)
			}
		}
		if value, present := golem.Value(row, social.Posts.Title).Get(); present {
			assertNoCanary(t, channel+" title", []byte(value), fixture.canaries()...)
		}
		if value, present := golem.Value(row, social.Posts.Body).Get(); present {
			assertNoCanary(t, channel+" body", []byte(value), fixture.canaries()...)
		}
	}
}

func (fixture *fixture) resetClosedChannels() {
	fixture.mu.Lock()
	fixture.trusted = nil
	fixture.observed = nil
	fixture.scopedAudits = nil
	fixture.operatorAudits = nil
	fixture.mu.Unlock()
}

func (fixture *fixture) assertProtectedStateIntact() {
	fixture.t.Helper()
	query := fixture.database.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM posts WHERE id IN (?,?,?)`)
	var rows int
	if err := fixture.database.UnsafeSQLX().GetContext(fixture.ctx, &rows, query, alicePostIDText, bobPublicIDText, bobPrivateIDText); err != nil || rows != 3 {
		fixture.t.Fatalf("protected state rows=%d error=%v", rows, err)
	}
	query = fixture.database.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM posts WHERE (id=? AND body=?) OR (id=? AND title=? AND body=?)`)
	var canaries int
	if err := fixture.database.UnsafeSQLX().GetContext(fixture.ctx, &canaries, query,
		bobPublicIDText, fixture.eventCanary, bobPrivateIDText, fixture.rowCanary, fixture.rowCanary+"-private-body"); err != nil || canaries != 2 {
		fixture.t.Fatalf("protected state canaries=%d error=%v", canaries, err)
	}
}

func (fixture *fixture) assertClosedChannels() {
	fixture.assertClosedChannelsFor(fixture.t)
}

func (fixture *fixture) assertClosedChannelsFor(t testing.TB) {
	t.Helper()
	fixture.mu.Lock()
	observed := append([]closedObservation(nil), fixture.observed...)
	audits := append([]golem.ScopedAuditRecord(nil), fixture.scopedAudits...)
	operatorAudits := append([]closedOperatorAudit(nil), fixture.operatorAudits...)
	fixture.mu.Unlock()
	encoded, _ := json.Marshal(observed)
	assertNoCanary(t, "observations", encoded, fixture.canaries()...)
	encoded, _ = json.Marshal(operatorAudits)
	assertNoCanary(t, "event operator audits", encoded, fixture.canaries()...)
	for _, record := range audits {
		assertNoCanary(t, "scoped audit principal", []byte(record.PrincipalAuditID()), fixture.canaries()...)
	}
	if strings.Contains(hex.EncodeToString(fixture.principal.TokenHash[:]), fixture.rowCanary) {
		t.Fatal("test canary collision")
	}
}

func (fixture *fixture) findError(id golem.UUID) *golem.Error {
	fixture.t.Helper()
	_, err := fixture.caller.Posts.FindUnique(fixture.ctx, social.Posts.ByID.Value(id))
	var failure *golem.Error
	if !errors.As(err, &failure) {
		fixture.t.Fatalf("find %s error=%v", id, err)
	}
	return failure
}

func (fixture *fixture) graphQL(query string, variables map[string]any) graphResponse {
	return fixture.graphQLContext(fixture.ctx, query, variables)
}

func (fixture *fixture) graphQLFor(t testing.TB, query string, variables map[string]any) graphResponse {
	return fixture.graphQLContextFor(t, fixture.ctx, query, variables)
}

func (fixture *fixture) graphQLContext(ctx context.Context, query string, variables map[string]any) graphResponse {
	return fixture.graphQLContextFor(fixture.t, ctx, query, variables)
}

func (fixture *fixture) graphQLContextFor(t testing.TB, ctx context.Context, query string, variables map[string]any) graphResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GraphQL body=%s: %v", recorder.Body.String(), err)
	}
	return response
}

func (fixture *fixture) takeTrusted() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	result := append([]string(nil), fixture.trusted...)
	fixture.trusted = nil
	return result
}

func (fixture *fixture) canaries() []string {
	return []string{fixture.rowCanary, fixture.errorCanary, fixture.eventCanary, fixture.principalToken}
}

type rejectingReadHook struct{ cause error }

func (hook rejectingReadHook) ObservePostReadHook(context.Context, social.PostReadHookPhase, []golem.Row[social.Post]) error {
	return hook.cause
}

type wsFrame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func readFrame(t testing.TB, connection *websocket.Conn, want string) wsFrame {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame wsFrame
	if err := connection.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != want {
		t.Fatalf("WebSocket frame=%s want=%s payload=%s", frame.Type, want, frame.Payload)
	}
	return frame
}

func awaitGraphQLEvent(connection *websocket.Conn, id string, forbidden ...string) (wsFrame, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		var frame wsFrame
		if err := connection.ReadJSON(&frame); err != nil {
			return wsFrame{}, err
		}
		if frame.Type != "next" {
			return wsFrame{}, fmt.Errorf("GraphQL subscription ended type=%s payload=%s", frame.Type, frame.Payload)
		}
		for _, value := range forbidden {
			if value != "" && bytes.Contains(frame.Payload, []byte(value)) {
				return wsFrame{}, fmt.Errorf("GraphQL subscription disclosed a protected value")
			}
		}
		if bytes.Contains(frame.Payload, []byte(id)) {
			return frame, nil
		}
	}
	return wsFrame{}, fmt.Errorf("GraphQL subscription did not deliver target event")
}

func seedUser(t testing.TB, ctx context.Context, system social.System[social.Principal], id golem.UUID, handle, email string) {
	t.Helper()
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(id), social.Users.Handle.Create(handle), social.Users.Email.Create(email),
	)); err != nil {
		t.Fatal(err)
	}
}

func postInput(t testing.TB, id, author, title, body string, published bool) golem.CreateInput[social.Post] {
	t.Helper()
	return social.Posts.Create(
		social.Posts.ID.Create(mustUUID(t, id)), social.Posts.AuthorID.Create(mustUUID(t, author)),
		social.Posts.Title.Create(title), social.Posts.Body.Create(body), social.Posts.Published.Create(published),
		social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "12:34:56")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
		social.Posts.Topics.Create(golem.List[string]{"p8", "disclosure"}),
	)
}

func graphRoot(t testing.TB, response graphResponse) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatalf("decode GraphQL data=%s: %v", response.Data, err)
	}
	return result
}

func graphObject(t testing.TB, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL object is %T: %v", value, value)
	}
	return result
}

func assertNoGraphErrors(t testing.TB, response graphResponse) {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors=%+v data=%s", response.Errors, response.Data)
	}
}

func assertPublicErrorNoCanary(t testing.TB, label string, err error, canaries ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s has no public error", label)
	}
	assertNoCanary(t, label, []byte(err.Error()), canaries...)
}

func assertNoCanary(t testing.TB, label string, data []byte, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if canary != "" && bytes.Contains(data, []byte(canary)) {
			t.Fatalf("%s disclosed protected canary %q in %s", label, canary, data)
		}
	}
}

func durationBucket(duration time.Duration) int {
	microseconds := uint64(duration.Microseconds() + 1)
	return bits.Len64(microseconds) - 1
}

func medianBucket(values []int) int {
	copyValues := append([]int(nil), values...)
	sort.Ints(copyValues)
	return copyValues[len(copyValues)/2]
}

func randomCanary(t testing.TB, family string) string {
	t.Helper()
	return "P8_" + family + "_" + hex.EncodeToString(randomBytes(t, 16))
}

func disclosureCanaries(t testing.TB) ([]byte, string, string, string) {
	t.Helper()
	encoded := strings.TrimSpace(os.Getenv("P8_ORACLE_FUZZ_CANARY_SEED"))
	if encoded == "" {
		return randomBytes(t, 32), randomCanary(t, "ROW"), randomCanary(t, "ERROR"), randomCanary(t, "EVENT")
	}
	seed, err := hex.DecodeString(encoded)
	if err != nil || len(seed) != 32 {
		t.Fatalf("invalid external fuzz canary seed")
	}
	derive := func(label string) []byte {
		digest := sha256.Sum256(append(append([]byte("p8-disclosure-fuzz-v1\x00"), seed...), label...))
		return digest[:]
	}
	canary := func(family string) string {
		return "P8_" + family + "_" + hex.EncodeToString(derive(family)[:16])
	}
	return derive("PRINCIPAL"), canary("ROW"), canary("ERROR"), canary("EVENT")
}

func randomBytes(t testing.TB, count int) []byte {
	t.Helper()
	result := make([]byte, count)
	if _, err := cryptorand.Read(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func mustJSONEncode(value any) []byte {
	result, _ := json.Marshal(value)
	return result
}

func mustUUID(t testing.TB, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDate(t testing.TB, value string) golem.Date {
	t.Helper()
	result, err := golem.ParseDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustTime(t testing.TB, value string) golem.Time {
	t.Helper()
	result, err := golem.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustJSON(t testing.TB, value string) golem.JSON[any] {
	t.Helper()
	result, err := golem.NewJSONDocument[any]([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return result
}
