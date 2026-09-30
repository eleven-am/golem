package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const graphqlJSIntrospectionQuery = `
    query IntrospectionQuery {
      __schema {

        queryType { name kind }
        mutationType { name kind }
        subscriptionType { name kind }
        types {
          ...FullType
        }
        directives {
          name
          description



          locations
          args {
            ...InputValue
          }
        }
      }
    }

    fragment FullType on __Type {
      kind
      name
      description


      fields(includeDeprecated: true) {
        name
        description
        args {
          ...InputValue
        }
        type {
          ...TypeRef
        }
        isDeprecated
        deprecationReason
      }
      inputFields {
        ...InputValue
      }
      interfaces {
        ...TypeRef
      }
      enumValues(includeDeprecated: true) {
        name
        description
        isDeprecated
        deprecationReason
      }
      possibleTypes {
        ...TypeRef
      }
    }

    fragment InputValue on __InputValue {
      name
      description
      type { ...TypeRef }
      defaultValue


    }

    fragment TypeRef on __Type {
      kind
      name
      ofType {
        name
        kind
        ofType {
          name
          kind
          ofType {
            name
            kind
            ofType {
              name
              kind
              ofType {
                name
                kind
                ofType {
                  name
                  kind
                  ofType {
                    name
                    kind
                    ofType {
                      name
                      kind
                      ofType {
                        name
                        kind
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  `

func TestGraphQLIntrospectionSQLite(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/introspection.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runGraphQLIntrospection(t, database)
}

func TestGraphQLIntrospectionPostgreSQL(t *testing.T) {
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runGraphQLIntrospection(t, database)
}

type introspectionWorld struct {
	application *social.App[social.Principal]
	caller      *social.Caller[social.Principal]
	principal   social.Principal
	userID      golem.UUID
}

func runGraphQLIntrospection(t *testing.T, database *provider.Database) {
	ctx := context.Background()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		t.Fatal(err)
	}
	userID := mustUUID(t, "12000000-0000-0000-0000-000000000001")
	if _, err := application.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("introspector"), social.Users.Email.Create("introspector@example.test"),
	)); err != nil {
		t.Fatal(err)
	}
	principal := social.Principal{Development: true, DevUserID: userID}
	caller, err := application.ForPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	world := introspectionWorld{application: application, caller: caller, principal: principal, userID: userID}
	t.Run("enabled", world.assertEnabled)
	t.Run("repeated introspection roots", world.assertRepeatedIntrospectionRefused)
	t.Run("disabled", world.assertDisabled)
	t.Run("introspection publishes exactly the SDL", world.assertIntrospectionMatchesSDL)
	t.Run("subscriptions", world.assertSubscriptions)
}

func (world introspectionWorld) graph(t *testing.T, introspection bool) (*social.GraphQLServer, http.Handler) {
	t.Helper()
	graph, err := world.application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		Introspection:        introspection,
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graph.Shutdown(context.Background()) })
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		graph.Handler().ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, world.principal)))
	})
	return graph, handler
}

func introspectionJSON(t *testing.T, response graphResponse) string {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("errors=%v body=%s", response.Errors, response.Raw)
	}
	encoded, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func introspectionErrorCode(response graphResponse) string {
	if len(response.Errors) == 0 {
		return ""
	}
	extensions, _ := response.Errors[0]["extensions"].(map[string]any)
	code, _ := extensions["code"].(string)
	return code
}

func (world introspectionWorld) assertEnabled(t *testing.T) {
	_, handler := world.graph(t, true)
	user := world.userID.String()
	for _, testCase := range []struct{ name, query, want string }{
		{"schema", `{ __schema { queryType { name } mutationType { name } subscriptionType { name } } }`, `{"__schema":{"mutationType":{"name":"Mutation"},"queryType":{"name":"Query"},"subscriptionType":{"name":"Subscription"}}}`},
		{"type", `{ __type(name: "BatchPayload") { name fields { name } } }`, `{"__type":{"fields":[{"name":"count"}],"name":"BatchPayload"}}`},
		{"typename", `{ __typename }`, `{"__typename":"Query"}`},
		{"schema beside a read", `{ __schema { queryType { name } } users { id } }`, `{"__schema":{"queryType":{"name":"Query"}},"users":[{"id":"` + user + `"}]}`},
		{"typename beside a read", `{ __typename users { id } }`, `{"__typename":"Query","users":[{"id":"` + user + `"}]}`},
		{"meta roots through fragments", `query Q { ...Meta users { ... on User { id } } } fragment Meta on Query { kind: __typename __type(name: "BatchPayload") { name } }`, `{"__type":{"name":"BatchPayload"},"kind":"Query","users":[{"id":"` + user + `"}]}`},
		{"mutation typename", `mutation { __typename }`, `{"__typename":"Mutation"}`},
		{"mutation typename beside a mutation", `mutation { __typename updateManyComments(where: { body: { equals: "absent" } }, data: { body: { set: "never" } }) { count } }`, `{"__typename":"Mutation","updateManyComments":{"count":0}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := introspectionJSON(t, graphqlHTTP(t, handler, "unused", testCase.query, nil)); got != testCase.want {
				t.Fatalf("data=%s want=%s", got, testCase.want)
			}
		})
	}
	t.Run("graphql-js introspection query at default limits", func(t *testing.T) {
		response := graphqlHTTP(t, handler, "unused", graphqlJSIntrospectionQuery, nil)
		if len(response.Errors) != 0 {
			t.Fatalf("errors=%v", response.Errors)
		}
		schema := graphMap(t, graphMap(t, response.Data)["__schema"])
		if graphMap(t, schema["queryType"])["name"] != "Query" || len(graphSlice(t, schema["types"])) == 0 {
			t.Fatalf("__schema=%v", schema)
		}
	})
}

func (world introspectionWorld) assertRepeatedIntrospectionRefused(t *testing.T) {
	_, handler := world.graph(t, true)
	for _, query := range []string{
		`{ a: __schema { types { name } } b: __schema { types { name } } }`,
		`{ t1: __type(name: "Post") { name } t2: __type(name: "Post") { name } t3: __type(name: "Post") { name } t4: __type(name: "Post") { name } t5: __type(name: "Post") { name } t6: __type(name: "Post") { name } t7: __type(name: "Post") { name } t8: __type(name: "Post") { name } t9: __type(name: "Post") { name } }`,
	} {
		if code := introspectionErrorCode(graphqlHTTP(t, handler, "unused", query, nil)); code != "QUERY_LIMIT_EXCEEDED" {
			t.Fatalf("%s code=%q", query, code)
		}
	}
}

func (world introspectionWorld) assertDisabled(t *testing.T) {
	_, handler := world.graph(t, false)
	for _, query := range []string{
		`{ __schema { queryType { name } } }`,
		`{ __type(name: "BatchPayload") { name } }`,
		`{ users { id } ...Meta } fragment Meta on Query { __schema { queryType { name } } }`,
		graphqlJSIntrospectionQuery,
	} {
		if code := introspectionErrorCode(graphqlHTTP(t, handler, "unused", query, nil)); code != "GRAPHQL_VALIDATION_FAILED" {
			t.Fatalf("%s code=%q", query, code)
		}
	}
	for query, want := range map[string]string{
		`{ __typename }`:              `{"__typename":"Query"}`,
		`mutation { __typename }`:     `{"__typename":"Mutation"}`,
		`{ __typename users { id } }`: `{"__typename":"Query","users":[{"id":"` + world.userID.String() + `"}]}`,
	} {
		if got := introspectionJSON(t, graphqlHTTP(t, handler, "unused", query, nil)); got != want {
			t.Fatalf("%s data=%s want=%s", query, got, want)
		}
	}
}

func (world introspectionWorld) assertIntrospectionMatchesSDL(t *testing.T) {
	graph, handler := world.graph(t, true)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "published.graphql", Input: graph.SDL()})
	if err != nil {
		t.Fatal(err)
	}
	fromSDL := map[string]bool{}
	for name, definition := range schema.Types {
		fromSDL["type "+name] = true
		for _, field := range definition.Fields {
			if definition.Name == "Query" && (field.Name == "__schema" || field.Name == "__type") {
				continue
			}
			fromSDL["field "+name+"."+field.Name] = true
			for _, argument := range field.Arguments {
				fromSDL["argument "+name+"."+field.Name+"("+argument.Name+")"] = true
			}
		}
		for _, value := range definition.EnumValues {
			fromSDL["value "+name+"."+value.Name] = true
		}
	}
	for name, directive := range schema.Directives {
		fromSDL["directive "+name] = true
		for _, argument := range directive.Arguments {
			fromSDL["directive "+name+"("+argument.Name+")"] = true
		}
	}
	response := graphqlHTTP(t, handler, "unused", graphqlJSIntrospectionQuery, nil)
	if len(response.Errors) != 0 {
		t.Fatalf("errors=%v", response.Errors)
	}
	introspected := map[string]bool{}
	published := graphMap(t, graphMap(t, response.Data)["__schema"])
	for _, raw := range graphSlice(t, published["types"]) {
		definition := graphMap(t, raw)
		name, _ := definition["name"].(string)
		introspected["type "+name] = true
		for _, list := range []string{"fields", "inputFields"} {
			if definition[list] == nil {
				continue
			}
			for _, rawField := range graphSlice(t, definition[list]) {
				field := graphMap(t, rawField)
				fieldName, _ := field["name"].(string)
				introspected["field "+name+"."+fieldName] = true
				if field["args"] == nil {
					continue
				}
				for _, rawArgument := range graphSlice(t, field["args"]) {
					argumentName, _ := graphMap(t, rawArgument)["name"].(string)
					introspected["argument "+name+"."+fieldName+"("+argumentName+")"] = true
				}
			}
		}
		if definition["enumValues"] != nil {
			for _, rawValue := range graphSlice(t, definition["enumValues"]) {
				valueName, _ := graphMap(t, rawValue)["name"].(string)
				introspected["value "+name+"."+valueName] = true
			}
		}
	}
	for _, raw := range graphSlice(t, published["directives"]) {
		directive := graphMap(t, raw)
		name, _ := directive["name"].(string)
		introspected["directive "+name] = true
		for _, rawArgument := range graphSlice(t, directive["args"]) {
			argumentName, _ := graphMap(t, rawArgument)["name"].(string)
			introspected["directive "+name+"("+argumentName+")"] = true
		}
	}
	var onlyIntrospected, onlySDL []string
	for key := range introspected {
		if !fromSDL[key] {
			onlyIntrospected = append(onlyIntrospected, key)
		}
	}
	for key := range fromSDL {
		if !introspected[key] {
			onlySDL = append(onlySDL, key)
		}
	}
	sort.Strings(onlyIntrospected)
	sort.Strings(onlySDL)
	if len(onlyIntrospected) != 0 || len(onlySDL) != 0 {
		t.Fatalf("introspection and SDL differ: only introspected=%v only SDL=%v", onlyIntrospected, onlySDL)
	}
	encoded := strings.ToLower(response.Raw + graph.SDL())
	for _, withheld := range []string{"session", "tokenhash", "token_hash", "expiresat", "updatemanyversionednotes", "deletemanyversionednotes", "aggregateusers", "_golem"} {
		if strings.Contains(encoded, withheld) {
			t.Fatalf("SDL or introspection publishes withheld %q", withheld)
		}
	}
	for _, published := range []string{"field Query.posts", "field Mutation.publishPost", "field Post.excerpt", "field Subscription.postEvents", "field User.email", "field Post.body"} {
		if !introspected[published] {
			t.Fatalf("introspection lacks %s", published)
		}
	}
}

func (world introspectionWorld) assertSubscriptions(t *testing.T) {
	_, handler := world.graph(t, true)
	if code := introspectionErrorCode(graphqlHTTP(t, handler, "unused", `subscription { __typename }`, nil)); code != "GRAPHQL_VALIDATION_FAILED" {
		t.Fatalf("subscription root __typename over HTTP code=%q", code)
	}
	publisherContext, stopPublisher := context.WithCancel(context.Background())
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- world.application.RunEventPublisher(publisherContext) }()
	t.Cleanup(func() {
		stopPublisher()
		<-publisherDone
	})
	awaitPublisher(t, world.application)
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
	if err := connection.WriteJSON(map[string]any{"id": "root", "type": "subscribe", "payload": map[string]any{"query": `subscription { __typename }`}}); err != nil {
		t.Fatal(err)
	}
	refused := readWSFrame(t, connection, "error")
	if refused.ID != "root" || !bytes.Contains(refused.Payload, []byte("GRAPHQL_VALIDATION_FAILED")) {
		t.Fatalf("subscription root __typename frame=%s", refused.Payload)
	}
	if err := connection.WriteJSON(map[string]any{"id": "nested", "type": "subscribe", "payload": map[string]any{"query": `subscription { postEvents { __typename type entity { __typename id } } }`}}); err != nil {
		t.Fatal(err)
	}
	postID := mustUUID(t, "22000000-0000-0000-0000-000000000001")
	if _, err := world.caller.Posts.Create(context.Background(), social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.Title.Create("introspection"), social.Posts.Body.Create("body"),
		social.Posts.LiveDate.Create(mustDate(t, "2026-01-02")), social.Posts.LiveTime.Create(mustTime(t, "10:11:12")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)), social.Posts.Topics.Create(golem.List[string]{"meta"}),
	)); err != nil {
		t.Fatal(err)
	}
	next := readWSFrame(t, connection, "next")
	var decoded any
	if err := json.Unmarshal(next.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"data":{"postEvents":{"__typename":"PostEvent","entity":{"__typename":"Post","id":"` + postID.String() + `"},"type":"CREATED"}}}`
	if next.ID != "nested" || string(canonical) != want {
		t.Fatalf("nested __typename frame id=%q payload=%s want=%s", next.ID, next.Payload, want)
	}
}
