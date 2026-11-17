package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/graphql"
	p7gqlgen "github.com/eleven-am/golem/go/graphql/testdata/p7subscription/golemgqlgen"
)

func TestResultByteBudgetUsesRenderedDataAcrossHTTPAndDirectExecution(t *testing.T) {
	data := map[string]any{"users": []any{map[string]any{"name": "<é&\n", "id": "u1"}}}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range []bool{false, true} {
		for _, allowed := range []bool{true, false} {
			t.Run(fmt.Sprintf("executable=%t/allowed=%t", executable, allowed), func(t *testing.T) {
				maximum := len(encoded)
				if !allowed {
					maximum--
				}
				var schema gqlgengraphql.ExecutableSchema
				if executable {
					schema = p5SocialExecutable()
				}
				server := orderServerWithLimits(t, p5SocialSDL(t), schema, orderExecutor{response: staticData(data), bounds: p5SocialListBounds}, graphql.Limits{MaxResultBytes: maximum})
				response := server.Execute(context.Background(), 1, graphql.Request{Query: `{users{name id}}`})
				var httpResponse graphql.Response
				if err := json.Unmarshal([]byte(orderHTTP(t, server, `{users{name id}}`)), &httpResponse); err != nil {
					t.Fatal(err)
				}
				for _, result := range []graphql.Response{response, httpResponse} {
					if allowed {
						if len(result.Errors) != 0 || result.Data == nil {
							t.Fatalf("exact byte boundary refused: %#v", result)
						}
						continue
					}
					if result.Data != nil || len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "LIMIT_EXCEEDED" || result.Errors[0].Extensions["limit"] != "result_bytes" {
						t.Fatalf("oversized data escaped: %#v", result)
					}
				}
			})
		}
	}
}

func TestResultByteBudgetCoversWebSocketAndSSEResponses(t *testing.T) {
	event := graphql.Response{Data: map[string]any{"ticks": graphql.PreparedObject{"eventID": strings.Repeat("x", 80)}}}
	const query = `subscription{ticks{eventID}}`
	for _, executable := range []bool{false, true} {
		t.Run(fmt.Sprintf("executable=%t", executable), func(t *testing.T) {
			var schema gqlgengraphql.ExecutableSchema
			if executable {
				schema = p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}})
			}
			server := orderServerWithLimits(t, p7gqlgenSchema, schema, orderExecutor{response: staticData(map[string]any{}), event: event}, graphql.Limits{MaxResultBytes: 16})
			var frame struct {
				Type    string           `json:"type"`
				Payload graphql.Response `json:"payload"`
			}
			if err := json.Unmarshal([]byte(orderSubscriptionFrame(t, server, query)), &frame); err != nil {
				t.Fatal(err)
			}
			assertRefusal := func(response graphql.Response) {
				t.Helper()
				if response.Data != nil || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "LIMIT_EXCEEDED" || response.Errors[0].Extensions["limit"] != "result_bytes" || response.Errors[0].Extensions["max"] != float64(16) {
					t.Fatalf("oversized subscription result escaped: %#v", response)
				}
			}
			if frame.Type != "next" {
				t.Fatalf("refusal was not a GraphQL response: %#v", frame)
			}
			assertRefusal(frame.Payload)
			request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(liveBody(query))))
			request.Header.Set("Accept", "text/event-stream")
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			withCredentialDeadline(server.Handler()).ServeHTTP(recorder, request)
			body, err := io.ReadAll(recorder.Result().Body)
			if err != nil {
				t.Fatal(err)
			}
			refusals := 0
			for _, line := range strings.Split(string(body), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var response graphql.Response
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.Errors) != 0 {
					assertRefusal(response)
					refusals++
				} else if response.Data != nil {
					t.Fatalf("oversized SSE data escaped: %s", body)
				}
			}
			if refusals != 1 {
				t.Fatalf("SSE did not report the limit: %s", body)
			}
		})
	}
}

func TestResultByteBudgetRunsAfterGraphQLNullPropagation(t *testing.T) {
	data := map[string]any{"failed": nil, "other": map[string]any{"name": strings.Repeat("large", 100), "id": "t1"}}
	failure := graphql.Error{Message: "unavailable", Path: []any{"failed"}, Extensions: map[string]any{"code": "FORBIDDEN"}}
	server := orderServerWithLimits(t, p5SocialSDL(t), p5SocialExecutable(), orderExecutor{response: staticData(data, failure), bounds: p5SocialListBounds}, graphql.Limits{MaxResultBytes: 4})
	response := server.Execute(context.Background(), 1, graphql.Request{Query: `mutation{failed:createTag(data:{id:"00000000-0000-0000-0000-000000000001",name:"a"}){name id} other:createTag(data:{id:"00000000-0000-0000-0000-000000000002",name:"b"}){name id}}`})
	if response.Data != nil || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("bytes from a nulled result affected the budget: %#v", response)
	}
}

func TestResultByteBudgetPreservesNativeDirectExecutionTypes(t *testing.T) {
	server := orderServerWithLimits(t, `type Query{viewer:Int!}`, nil, orderExecutor{response: staticData(map[string]any{"viewer": int32(7)})}, graphql.Limits{MaxResultBytes: len(`{"viewer":7}`)})
	response := server.Execute(context.Background(), 1, graphql.Request{Query: `{viewer}`})
	if len(response.Errors) != 0 || response.Data.(map[string]any)["viewer"] != int32(7) {
		t.Fatalf("byte accounting changed direct execution values: %#v", response)
	}
}
