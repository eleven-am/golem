package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/gorilla/websocket"
)

func TestGraphQLResponseOrderSQLite(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/order.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runGraphQLResponseOrder(t, database)
}

func TestGraphQLResponseOrderPostgreSQL(t *testing.T) {
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runGraphQLResponseOrder(t, database)
}

func runGraphQLResponseOrder(t *testing.T, database *provider.Database) {
	ctx := context.Background()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		t.Fatal(err)
	}
	viewerID := mustUUID(t, "13000000-0000-0000-0000-000000000001")
	otherID := mustUUID(t, "13000000-0000-0000-0000-000000000002")
	for _, user := range []struct {
		id     golem.UUID
		handle string
	}{{viewerID, "orderly"}, {otherID, "another"}} {
		if _, err := application.System().Users.Create(ctx, social.Users.Create(
			social.Users.ID.Create(user.id), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.handle+"@example.test"),
		)); err != nil {
			t.Fatal(err)
		}
	}
	principal := social.Principal{Development: true, DevUserID: viewerID}
	caller, err := application.ForPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	postID := mustUUID(t, "23000000-0000-0000-0000-000000000001")
	if _, err := caller.Posts.Create(ctx, social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.Title.Create("<ordered & escaped>"), social.Posts.Body.Create("body"),
		social.Posts.Published.Create(true),
		social.Posts.LiveDate.Create(mustDate(t, "2026-01-02")), social.Posts.LiveTime.Create(mustTime(t, "10:11:12")),
		social.Posts.Metadata.Create(mustJSON(t, `{"zulu":1,"alpha":"<"}`)), social.Posts.Topics.Create(golem.List[string]{"order"}),
	)); err != nil {
		t.Fatal(err)
	}
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		Introspection:        true,
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graph.Shutdown(context.Background()) })
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		graph.Handler().ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
	createHiddenBodyPost := func(t *testing.T) {
		other, err := application.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: otherID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Posts.Create(ctx, social.Posts.Create(
			social.Posts.ID.Create(mustUUID(t, "23000000-0000-0000-0000-000000000003")), social.Posts.Title.Create("zz hidden body"), social.Posts.Body.Create("secret"),
			social.Posts.Published.Create(true),
			social.Posts.LiveDate.Create(mustDate(t, "2026-01-02")), social.Posts.LiveTime.Create(mustTime(t, "10:11:12")),
			social.Posts.Metadata.Create(mustJSON(t, `{}`)), social.Posts.Topics.Create(golem.List[string]{"order"}),
		)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		setup func(*testing.T)
		query string
		want  string
	}{
		{
			name:  "roots aliases fragments typename and nested lists",
			query: `query { zeta: users(orderBy: [{handle: desc}]) { ...Handles id ... on User { handle _count { posts comments } posts { title id metadata __typename } } } alpha: tags { name } kind: __typename } fragment Handles on User { handle __typename }`,
			want:  `{"data":{"zeta":[{"handle":"orderly","__typename":"User","id":"13000000-0000-0000-0000-000000000001","_count":{"posts":1,"comments":0},"posts":[{"title":"\u003cordered \u0026 escaped\u003e","id":"23000000-0000-0000-0000-000000000001","metadata":{"alpha":"\u003c","zulu":1},"__typename":"Post"}]},{"handle":"another","__typename":"User","id":"13000000-0000-0000-0000-000000000002","_count":{"posts":0,"comments":0},"posts":[]}],"alpha":[],"kind":"Query"}}`,
		},
		{
			name:  "introspection",
			query: `{ __type(name: "Tag") { name kind fields { name } } __schema { queryType { name kind } } }`,
			want:  `{"data":{"__type":{"name":"Tag","kind":"OBJECT","fields":[{"name":"id"},{"name":"name"},{"name":"postTags"},{"name":"_count"}]},"__schema":{"queryType":{"name":"Query","kind":"OBJECT"}}}}`,
		},
		{
			name:  "mutation roots",
			query: `mutation { zeta: createTag(data: {id: "33000000-0000-0000-0000-000000000002", name: "zeta"}) { name id } alpha: createTag(data: {id: "33000000-0000-0000-0000-000000000001", name: "alpha"}) { name id } }`,
			want:  `{"data":{"zeta":{"name":"zeta","id":"33000000-0000-0000-0000-000000000002"},"alpha":{"name":"alpha","id":"33000000-0000-0000-0000-000000000001"}}}`,
		},
		{
			name:  "masked and denied fields",
			setup: createHiddenBodyPost,
			query: `{ zeta: posts(orderBy: [{title: desc}]) { excerpt(maximum: 4) title id } alpha: user(where: {ID: "13000000-0000-0000-0000-000000000001"}) { handle email id } }`,
			want:  `{"data":{"zeta":[{"excerpt":null,"title":"zz hidden body","id":"23000000-0000-0000-0000-000000000003"},{"excerpt":"body","title":"\u003cordered \u0026 escaped\u003e","id":"23000000-0000-0000-0000-000000000001"}],"alpha":{"handle":"orderly","email":"orderly@example.test","id":"13000000-0000-0000-0000-000000000001"}}}`,
		},
		{
			name:  "conflicting mutation keeps data omitted",
			query: `mutation { again: createTag(data: {id: "33000000-0000-0000-0000-000000000002", name: "zeta"}) { name id } }`,
			want:  `{"errors":[{"message":"mutation conflicted","path":["again"],"extensions":{"code":"CONFLICT"}}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.setup != nil {
				testCase.setup(t)
			}
			if got := responseOrderHTTP(t, handler, testCase.query); got != testCase.want+"\n" {
				t.Fatalf("body\n got=%s\nwant=%s", got, testCase.want)
			}
		})
	}
	t.Run("subscription frames", func(t *testing.T) {
		assertResponseOrderSubscription(t, application, handler, caller)
	})
}

func responseOrderHTTP(t *testing.T, handler http.Handler, query string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func assertResponseOrderSubscription(t *testing.T, application *social.App[social.Principal], handler http.Handler, caller *social.Caller[social.Principal]) {
	publisherContext, stopPublisher := context.WithCancel(context.Background())
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	t.Cleanup(func() {
		stopPublisher()
		<-publisherDone
	})
	awaitPublisher(t, application)
	host := httptest.NewServer(handler)
	defer host.Close()
	connection, _, err := (&websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}).Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	readWSFrame(t, connection, "connection_ack")
	query := `subscription { postEvents { type __typename entity { title ...Ident metadata } kind: type id } } fragment Ident on Post { id __typename }`
	if err := connection.WriteJSON(map[string]any{"id": "ordered", "type": "subscribe", "payload": map[string]any{"query": query}}); err != nil {
		t.Fatal(err)
	}
	postID := mustUUID(t, "23000000-0000-0000-0000-000000000002")
	if _, err := caller.Posts.Create(context.Background(), social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.Title.Create("subscribed"), social.Posts.Body.Create("body"),
		social.Posts.LiveDate.Create(mustDate(t, "2026-01-02")), social.Posts.LiveTime.Create(mustTime(t, "10:11:12")),
		social.Posts.Metadata.Create(mustJSON(t, `{"zulu":1,"alpha":2}`)), social.Posts.Topics.Create(golem.List[string]{"order"}),
	)); err != nil {
		t.Fatal(err)
	}
	var frame []byte
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(frame, []byte(postID.String())) {
		_ = connection.SetReadDeadline(deadline)
		if _, frame, err = connection.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	want := `{"id":"ordered","type":"next","payload":{"data":{"postEvents":{"type":"CREATED","__typename":"PostEvent","entity":{"title":"subscribed","id":"` + postID.String() + `","__typename":"Post","metadata":{"alpha":2,"zulu":1}},"kind":"CREATED","id":"` + postID.String() + `"}}}}` + "\n"
	if string(frame) != want {
		t.Fatalf("frame\n got=%s\nwant=%s", frame, want)
	}
}
