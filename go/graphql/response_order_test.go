package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/graphql"
	p7gqlgen "github.com/eleven-am/golem/go/graphql/testdata/p7subscription/golemgqlgen"
	p5gqlgen "github.com/eleven-am/golem/go/runtime/testdata/p5social/golemgqlgen"
	"github.com/gorilla/websocket"
)

type orderExecutor struct {
	response func(graphql.Operation) graphql.Response
	event    graphql.Response
}

func (executor orderExecutor) Execute(_ context.Context, _ int, operation graphql.Operation) graphql.Response {
	return executor.response(operation)
}

func (executor orderExecutor) Subscribe(context.Context, int, graphql.Operation) (graphql.ResponseStream, error) {
	return &orderStream{event: executor.event}, nil
}

type orderStream struct {
	event graphql.Response
	sent  bool
}

func (stream *orderStream) Recv(context.Context) (graphql.Response, error) {
	if stream.sent {
		return graphql.Response{}, io.EOF
	}
	stream.sent = true
	return stream.event, nil
}

func (*orderStream) Close() error { return nil }

func p5SocialSDL(t testing.TB) string {
	t.Helper()
	sdl, err := os.ReadFile(filepath.Join("..", "runtime", "testdata", "p5social", "zz_golem_graphql.schema.graphqls"))
	if err != nil {
		t.Fatal(err)
	}
	return string(sdl)
}

func p5SocialExecutable() gqlgengraphql.ExecutableSchema {
	return p5gqlgen.NewExecutableSchema(p5gqlgen.Config{Resolvers: &p5gqlgen.Resolver{}})
}

func orderServer(t testing.TB, sdl string, executable gqlgengraphql.ExecutableSchema, executor orderExecutor) *graphql.Server[int] {
	t.Helper()
	return orderServerWithLimits(t, sdl, executable, executor, graphql.Limits{})
}

func orderServerWithLimits(t testing.TB, sdl string, executable gqlgengraphql.ExecutableSchema, executor orderExecutor, limits graphql.Limits) *graphql.Server[int] {
	t.Helper()
	server, err := graphql.NewServer(sdl, graphql.Config[int]{
		Limits:               limits,
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
		Introspection:        true,
		ExecutableSchema:     executable,
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func orderHTTP(t testing.TB, server *graphql.Server[int], query string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func staticData(data map[string]any, failures ...graphql.Error) func(graphql.Operation) graphql.Response {
	return func(graphql.Operation) graphql.Response { return graphql.Response{Data: data, Errors: failures} }
}

type orderCase struct {
	name     string
	query    string
	response func(graphql.Operation) graphql.Response
	want     string
}

func executableOrderCases() []orderCase {
	return []orderCase{
		{
			name:  "roots and aliases",
			query: `{ zeta: users { name id } alpha: tags { name id } middle: users { id } }`,
			response: staticData(map[string]any{
				"zeta":   []any{map[string]any{"name": "Ada", "id": "u1"}},
				"alpha":  []any{map[string]any{"name": "go", "id": "t1"}},
				"middle": []any{map[string]any{"id": "u1"}},
			}),
			want: `{"data":{"zeta":[{"name":"Ada","id":"u1"}],"alpha":[{"name":"go","id":"t1"}],"middle":[{"id":"u1"}]}}`,
		},
		{
			name:  "fragments inline fragments and typename",
			query: `query { users { ...Names id ... on User { name _count { posts comments } } } } fragment Names on User { name __typename }`,
			response: staticData(map[string]any{
				"users": []any{map[string]any{"name": "Ada", "id": "u1", "_count": map[string]any{"posts": 2, "comments": 1}}},
			}),
			want: `{"data":{"users":[{"name":"Ada","__typename":"User","id":"u1","_count":{"posts":2,"comments":1}}]}}`,
		},
		{
			name:  "nested lists",
			query: `{ users(take: 2) { posts(take: 2) { title id author { name id } } name } }`,
			response: staticData(map[string]any{
				"users": []any{map[string]any{"name": "Ada", "posts": []any{
					map[string]any{"title": "b", "id": "p2", "author": map[string]any{"name": "Ada", "id": "u1"}},
					map[string]any{"title": "a", "id": "p1", "author": nil},
				}}},
			}),
			want: `{"data":{"users":[{"posts":[{"title":"b","id":"p2","author":{"name":"Ada","id":"u1"}},{"title":"a","id":"p1","author":null}],"name":"Ada"}]}}`,
		},
		{
			name:     "root typename and introspection",
			query:    `{ zeta: __typename __type(name: "Tag") { name kind } __schema { queryType { name kind } } }`,
			response: staticData(map[string]any{}),
			want:     `{"data":{"zeta":"Query","__type":{"name":"Tag","kind":"OBJECT"},"__schema":{"queryType":{"name":"Query","kind":"OBJECT"}}}}`,
		},
		{
			name:  "mutation roots",
			query: `mutation { zeta: createTag(data: {id: "00000000-0000-0000-0000-000000000001", name: "z"}) { name id } alpha: deleteTag(where: {ID: "00000000-0000-0000-0000-000000000002"}) { name id } }`,
			response: staticData(map[string]any{
				"zeta":  map[string]any{"name": "z", "id": "00000000-0000-0000-0000-000000000001"},
				"alpha": map[string]any{"name": "a", "id": "00000000-0000-0000-0000-000000000002"},
			}),
			want: `{"data":{"zeta":{"name":"z","id":"00000000-0000-0000-0000-000000000001"},"alpha":{"name":"a","id":"00000000-0000-0000-0000-000000000002"}}}`,
		},
		{
			name:  "errors with partial data",
			query: `{ zeta: user(where: {ID: "00000000-0000-0000-0000-000000000001"}) { name id } alpha: users { name id } }`,
			response: staticData(map[string]any{
				"zeta":  nil,
				"alpha": []any{map[string]any{"name": nil, "id": "u1"}},
			}, graphql.Error{Message: "forbidden", Path: []any{"zeta"}, Extensions: map[string]any{"code": "FORBIDDEN"}},
				graphql.Error{Message: "hidden", Path: []any{"alpha", 0, "name"}, Extensions: map[string]any{"code": "FORBIDDEN"}}),
			want: `{"data":{"zeta":null,"alpha":[{"name":null,"id":"u1"}]},"errors":[{"message":"forbidden","path":["zeta"],"extensions":{"code":"FORBIDDEN"}},{"message":"hidden","path":["alpha",0,"name"],"extensions":{"code":"FORBIDDEN"}}]}`,
		},
		{
			name:  "non-null root failure keeps data omitted",
			query: `mutation { zeta: createTag(data: {id: "00000000-0000-0000-0000-000000000001", name: "z"}) { name id } }`,
			response: staticData(map[string]any{"zeta": nil},
				graphql.Error{Message: "forbidden", Path: []any{"zeta"}, Extensions: map[string]any{"code": "FORBIDDEN"}}),
			want: `{"errors":[{"message":"forbidden","path":["zeta"],"extensions":{"code":"FORBIDDEN"}}]}`,
		},
		{
			name:  "string and number encoding is unchanged",
			query: `{ users { name id _count { posts } } }`,
			response: staticData(map[string]any{
				"users": []any{
					map[string]any{"name": "<a&b>", "id": "line sep end", "_count": map[string]any{"posts": 2147483647}},
					map[string]any{"name": "ctl\x01\b\f\t\n\r\x1f", "id": "quote\"back\\slash/", "_count": map[string]any{"posts": 0}},
					map[string]any{"name": "é emoji 😀", "id": "bad\xffutf8", "_count": nil},
				},
			}),
			want: `{"data":{"users":[{"name":"\u003ca\u0026b\u003e","id":"line\u2028sep\u2029end","_count":{"posts":2147483647}},{"name":"ctl\u0001\b\f\t\n\r\u001f","id":"quote\"back\\slash/","_count":{"posts":0}},{"name":"é emoji 😀","id":"bad�utf8","_count":null}]}}`,
		},
	}
}

func TestHTTPResponsesFollowSelectionOrderThroughTheExecutable(t *testing.T) {
	sdl := p5SocialSDL(t)
	for _, testCase := range executableOrderCases() {
		t.Run(testCase.name, func(t *testing.T) {
			server := orderServer(t, sdl, p5SocialExecutable(), orderExecutor{response: testCase.response})
			if got := orderHTTP(t, server, testCase.query); got != testCase.want+"\n" {
				t.Fatalf("body\n got=%s\nwant=%s", got, testCase.want)
			}
		})
	}
}

func TestHTTPResponsesFollowSelectionOrderWithoutAnExecutable(t *testing.T) {
	sdl := p5SocialSDL(t)
	cases := []orderCase{
		{
			name:  "roots aliases fragments and typename",
			query: `query Q($hide: Boolean!) { zeta: users { ...Names id posts { title } ... on User { name _count { posts comments } posts { id authorID } } skipped: id @skip(if: $hide) } alpha: tags { name id } } fragment Names on User { name __typename }`,
			response: staticData(map[string]any{
				"alpha": []any{map[string]any{"name": "go", "id": "t1"}},
				"zeta":  []any{map[string]any{"name": "Ada", "__typename": "User", "id": "u1", "_count": map[string]any{"posts": 2, "comments": 1}, "posts": []any{map[string]any{"authorID": "u1", "id": "p1", "title": "t"}}}},
			}),
			want: `{"data":{"zeta":[{"name":"Ada","__typename":"User","id":"u1","posts":[{"title":"t","id":"p1","authorID":"u1"}],"_count":{"posts":2,"comments":1}}],"alpha":[{"name":"go","id":"t1"}]}}`,
		},
		{
			name:  "errors with partial data and leaf JSON values",
			query: `{ zeta: user(where: {ID: "00000000-0000-0000-0000-000000000001"}) { name id } alpha: users { name id } }`,
			response: staticData(map[string]any{
				"zeta":  nil,
				"alpha": []any{map[string]any{"name": map[string]any{"z": 1, "a": "<"}, "id": "u1"}},
			}, graphql.Error{Message: "forbidden", Path: []any{"zeta"}, Extensions: map[string]any{"code": "FORBIDDEN"}}),
			want: `{"data":{"zeta":null,"alpha":[{"name":{"a":"\u003c","z":1},"id":"u1"}]},"errors":[{"message":"forbidden","path":["zeta"],"extensions":{"code":"FORBIDDEN"}}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := orderServer(t, sdl, nil, orderExecutor{response: testCase.response})
			body, err := json.Marshal(map[string]any{"query": testCase.query, "variables": map[string]any{"hide": true}})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if got := recorder.Body.String(); got != testCase.want+"\n" {
				t.Fatalf("body\n got=%s\nwant=%s", got, testCase.want)
			}
		})
	}
}

func TestDirectExecuteKeepsDecodedGoValues(t *testing.T) {
	sdl := p5SocialSDL(t)
	for _, executable := range []gqlgengraphql.ExecutableSchema{p5SocialExecutable(), nil} {
		server := orderServer(t, sdl, executable, orderExecutor{response: staticData(map[string]any{
			"zeta": []any{map[string]any{"name": "Ada", "id": "u1"}},
		})})
		response := server.Execute(context.Background(), 1, graphql.Request{Query: `{ zeta: users { name id } }`})
		data, ok := response.Data.(map[string]any)
		if !ok || len(response.Errors) != 0 {
			t.Fatalf("executable=%v data=%T %#v errors=%#v", executable != nil, response.Data, response.Data, response.Errors)
		}
		users, _ := data["zeta"].([]any)
		if len(users) != 1 || users[0].(map[string]any)["name"] != "Ada" {
			t.Fatalf("executable=%v data=%#v", executable != nil, data)
		}
	}
}

func TestSubscriptionFramesFollowSelectionOrder(t *testing.T) {
	event := graphql.Response{Data: map[string]any{"ticks": graphql.PreparedObject{
		"__typename": "TickEvent", "type": "CREATED", "eventID": "e1", "kind": "CREATED",
		"entity": graphql.PreparedObject{"value": 3},
	}}}
	query := `subscription { ticks { type __typename ... on TickEvent { entity { value } } eventID kind: type } }`
	want := `{"id":"one","type":"next","payload":{"data":{"ticks":{"type":"CREATED","__typename":"TickEvent","entity":{"value":3},"eventID":"e1","kind":"CREATED"}}}}` + "\n"
	for name, executable := range map[string]gqlgengraphql.ExecutableSchema{
		"executable": p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}),
		"direct":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			server := orderServer(t, p7gqlgenSchema, executable, orderExecutor{response: staticData(map[string]any{}), event: event})
			if got := orderSubscriptionFrame(t, server, query); got != want {
				t.Fatalf("frame\n got=%s\nwant=%s", got, want)
			}
		})
	}
}

func orderSubscriptionFrame(t *testing.T, server *graphql.Server[int], query string) string {
	t.Helper()
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	connection, _, err := (&websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}).Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	readOrderFrame(t, connection)
	if err := connection.WriteJSON(map[string]any{"id": "one", "type": "subscribe", "payload": map[string]any{"query": query}}); err != nil {
		t.Fatal(err)
	}
	return readOrderFrame(t, connection)
}

func readOrderFrame(t *testing.T, connection *websocket.Conn) string {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func largeListData(users, posts int) map[string]any {
	list := make([]any, users)
	for index := range list {
		items := make([]any, posts)
		for item := range items {
			items[item] = map[string]any{"title": fmt.Sprintf("post <%d> of user %d", item, index), "id": fmt.Sprintf("00000000-0000-0000-0001-%012d", index*posts+item)}
		}
		list[index] = map[string]any{
			"id": fmt.Sprintf("00000000-0000-0000-0000-%012d", index), "name": fmt.Sprintf("user %d", index),
			"_count": map[string]any{"posts": posts, "comments": index}, "posts": items, "__typename": "User",
		}
	}
	return map[string]any{"users": list}
}

const largeListQuery = `{ users(take: 1000) { name id __typename _count { posts comments } posts(take: 5) { title id } } }`

func benchmarkLargeList(b *testing.B, executable gqlgengraphql.ExecutableSchema) {
	server := orderServerWithLimits(b, p5SocialSDL(b), executable, orderExecutor{response: staticData(largeListData(1000, 5))}, graphql.Limits{MaxComplexity: 100_000, MaxPageSize: 1_000})
	body, err := json.Marshal(map[string]any{"query": largeListQuery})
	if err != nil {
		b.Fatal(err)
	}
	handler := server.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || bytes.Contains(recorder.Body.Bytes(), []byte(`"errors"`)) {
			b.Fatalf("status=%d body=%.300s", recorder.Code, recorder.Body.String())
		}
	}
}

func BenchmarkHTTPLargeListThroughExecutable(b *testing.B) {
	benchmarkLargeList(b, p5SocialExecutable())
}

func BenchmarkHTTPLargeListWithoutExecutable(b *testing.B) { benchmarkLargeList(b, nil) }

const abstractOrderSchema = `interface Node { id: ID! }
type Cat implements Node { id: ID! name: String lives: Int! }
type Dog implements Node { id: ID! name: String barks: Boolean! }
union Pet = Cat | Dog
type Query { pets: [Pet!]! nodes: [Node!]! }
`

type abstractShape struct {
	name     string
	cat, dog []string
}

type abstractRow struct {
	parent, root, placement, shape string
	value                          any
	extra                          bool
	cat, dog                       []string
}

func (row abstractRow) name() string {
	return fmt.Sprintf("%s/%s/%v/extra=%v/%s", row.parent, row.placement, row.value, row.extra, row.shape)
}

func (row abstractRow) query() string {
	cat := strings.Join(row.cat, " ")
	dog := strings.Join(row.dog, " ")
	prefix, fragments := "", ""
	switch row.placement {
	case "direct":
		prefix = "kind: __typename "
	case "fragment":
		prefix = "... @skip(if: true) { kind: __typename } ... @include(if: true) { ...Outer } "
		fragments = fmt.Sprintf(" fragment Outer on %s { ... on %s { ...Kind } } fragment Kind on %s { kind: __typename }", row.parent, row.parent, row.parent)
	case "inactive-branch":
		cat += " kind: __typename"
		dog = "kind: name " + dog
	case "active-branch":
		cat = "kind: name " + cat
		dog += " kind: __typename"
	}
	return fmt.Sprintf("{ %s { %s... on Cat { %s } ... on Dog { %s } } }%s", row.root, prefix, cat, dog, fragments)
}

func (row abstractRow) collected(typename string) (fields []string, typenameField bool) {
	branch := row.dog
	if typename == "Cat" {
		branch = row.cat
	}
	switch row.placement {
	case "direct", "fragment":
		return append([]string{"kind"}, branch...), true
	case "inactive-branch":
		if typename == "Cat" {
			return append(append([]string{}, branch...), "kind"), true
		}
		return append([]string{"kind"}, branch...), false
	case "active-branch":
		if typename == "Dog" {
			return append(append([]string{}, branch...), "kind"), true
		}
		return append([]string{"kind"}, branch...), false
	default:
		return branch, false
	}
}

func (row abstractRow) object() map[string]any {
	values := map[string]any{"id": "d", "name": "Rex", "barks": true, "kind": row.value}
	fields, _ := row.collected("Dog")
	object := map[string]any{}
	for _, field := range fields {
		object[field] = values[field]
	}
	if row.extra {
		object["extra"] = true
	}
	return object
}

func (row abstractRow) expectedType() string {
	object := row.object()
	type rank struct {
		name                                string
		contradicted, incomplete, confirmed bool
		present                             int
	}
	ranks := []rank{}
	for _, typename := range []string{"Cat", "Dog"} {
		fields, typenameField := row.collected(typename)
		current := rank{name: typename}
		for _, field := range fields {
			if _, ok := object[field]; ok {
				current.present++
			} else {
				current.incomplete = true
			}
		}
		if value, ok := object["kind"]; typenameField && ok {
			current.contradicted = value != typename
			current.confirmed = value == typename
		}
		ranks = append(ranks, current)
	}
	cat, dog := ranks[0], ranks[1]
	switch {
	case cat.contradicted != dog.contradicted:
		return map[bool]string{true: "Dog", false: "Cat"}[cat.contradicted]
	case cat.incomplete != dog.incomplete:
		return map[bool]string{true: "Dog", false: "Cat"}[cat.incomplete]
	case cat.present != dog.present:
		return map[bool]string{true: "Cat", false: "Dog"}[cat.present > dog.present]
	case cat.confirmed != dog.confirmed:
		return map[bool]string{true: "Cat", false: "Dog"}[cat.confirmed]
	default:
		return "Cat"
	}
}

func (row abstractRow) conforming() bool {
	switch row.placement {
	case "absent":
		return true
	case "inactive-branch":
		_, ok := row.value.(string)
		return ok
	default:
		return row.value == "Dog"
	}
}

func (row abstractRow) want(t *testing.T, typename string) string {
	t.Helper()
	object := row.object()
	fields, _ := row.collected(typename)
	written := map[string]bool{}
	var body strings.Builder
	write := func(field string) {
		encoded, err := json.Marshal(object[field])
		if err != nil {
			t.Fatal(err)
		}
		if body.Len() != 0 {
			body.WriteByte(',')
		}
		body.WriteString(`"` + field + `":`)
		body.Write(encoded)
		written[field] = true
	}
	for _, field := range fields {
		if _, ok := object[field]; ok && !written[field] {
			write(field)
		}
	}
	remaining := []string{}
	for field := range object {
		if !written[field] {
			remaining = append(remaining, field)
		}
	}
	sort.Strings(remaining)
	for _, field := range remaining {
		write(field)
	}
	return `{"data":{"` + row.root + `":[{` + body.String() + `}]}}`
}

func abstractRows() []abstractRow {
	shapes := []abstractShape{
		{"distinguishable", []string{"lives", "name"}, []string{"name", "barks"}},
		{"dog-superset", []string{"name"}, []string{"barks", "name"}},
		{"dog-subset", []string{"lives", "name"}, []string{"name"}},
		{"identical", []string{"id", "name"}, []string{"name", "id"}},
	}
	var rows []abstractRow
	for _, parent := range [][2]string{{"Pet", "pets"}, {"Node", "nodes"}} {
		for _, placement := range []string{"direct", "fragment", "inactive-branch", "active-branch", "absent"} {
			values := []any{"Dog", "Rex", "Cat", 7}
			if placement == "absent" {
				values = []any{nil}
			}
			for _, value := range values {
				for _, extra := range []bool{false, true} {
					for _, shape := range shapes {
						rows = append(rows, abstractRow{parent: parent[0], root: parent[1], placement: placement, shape: shape.name, value: value, extra: extra, cat: shape.cat, dog: shape.dog})
					}
				}
			}
		}
	}
	return rows
}

func TestAbstractRuntimeTypeResolutionTable(t *testing.T) {
	rows := abstractRows()
	if len(rows) != 272 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, row := range rows {
		t.Run(row.name(), func(t *testing.T) {
			typename := row.expectedType()
			if row.conforming() && typename != "Dog" && row.shape != "identical" {
				t.Fatalf("oracle chose %s for a conforming Dog with a distinguishable shape", typename)
			}
			want := row.want(t, typename)
			server := orderServer(t, abstractOrderSchema, nil, orderExecutor{response: staticData(map[string]any{row.root: []any{row.object()}})})
			if got := orderHTTP(t, server, row.query()); got != want+"\n" {
				t.Fatalf("query %s\n got=%s\nwant=%s (%s)", row.query(), got, want, typename)
			}
		})
	}
}
