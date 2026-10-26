package graphql

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzGraphQLSSERequestDecoder(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"query":"{ viewer }"}`),
		[]byte(`{"query":"query Read($value: String!) { echo(value: $value) }","operationName":"Read","variables":{"value":"ok"}}`),
		[]byte(`{"query":"subscription { ticks }"}`),
		[]byte(`{"query":"query {"}`),
		[]byte(`{"query":"{ viewer }","unknown":"private-driver-token"}`),
		[]byte(`{"query":"{ echo(value: \"line\\none\") }"}`),
		[]byte(`{"query":"query One { viewer } query Two { viewer }"}`),
		[]byte(`{"query":"{ viewer }","variables":null}`),
		[]byte(`{"query":"{ viewer }","variables":[1]}`),
		[]byte(`{"query":"{ viewer }"} {}`),
		[]byte(`{"query":""}`),
		bytes.Repeat([]byte{'x'}, 1025),
		append([]byte{'{'}, []byte{0xff, '}'}...),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 4096 {
			t.Skip()
		}
		server := newFuzzServer(t)
		decoded, failure := decodeHTTPRequest(bytes.NewReader(body), server.limits)
		if failure == nil && (decoded.Query == "" || decoded.Variables == nil) {
			t.Fatalf("decoder accepted an incomplete request: %#v", decoded)
		}
		if failure != nil {
			assertBoundedPublicResponse(t, Response{Errors: []Error{*failure}})
		}
		request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		request.Header.Set("Accept", "text/event-stream")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Body.Len() > 4096+64 {
			t.Fatalf("GraphQL SSE response exceeded fixed bound: %d bytes", recorder.Body.Len())
		}
		if recorder.Code != http.StatusOK {
			if recorder.Code < 400 || recorder.Code > 499 || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("GraphQL SSE refusal status=%d type=%q", recorder.Code, recorder.Header().Get("Content-Type"))
			}
			var refused Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &refused); err != nil || len(refused.Errors) == 0 {
				t.Fatalf("GraphQL SSE refusal is not a JSON error: %v; body=%q", err, recorder.Body.Bytes())
			}
			assertBoundedPublicResponse(t, refused)
			if failure == nil && refused.Errors[0].Extensions["code"] == "BAD_REQUEST" {
				t.Fatalf("decoder accepted a request the SSE transport refused as malformed: %q", body)
			}
			return
		}
		if failure != nil {
			t.Fatalf("SSE opened a stream for a request the decoder refused: %#v", failure)
		}
		if recorder.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("GraphQL SSE stream type = %q", recorder.Header().Get("Content-Type"))
		}
		frames := strings.Split(strings.TrimSuffix(recorder.Body.String(), "\n\n"), "\n\n")
		if len(frames) != 2 || !strings.HasPrefix(frames[0], "event: next\ndata: ") || strings.Count(frames[0], "\n") != 1 || frames[1] != "event: complete" {
			t.Fatalf("GraphQL SSE stream is not one next and one complete: %q", recorder.Body.String())
		}
		var next Response
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "event: next\ndata: ")), &next); err != nil {
			t.Fatalf("GraphQL SSE next is not JSON: %v; frame=%q", err, frames[0])
		}
		assertBoundedPublicResponse(t, next)
	})
}
