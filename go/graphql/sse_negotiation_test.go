package graphql

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
)

const urqlDefaultAccept = "application/graphql-response+json, application/graphql+json, application/json, text/event-stream, multipart/mixed"

func TestNegotiateEventStreamWeighsQualityAndSpecificity(t *testing.T) {
	for accept, want := range map[string]eventStreamNegotiation{
		"":                                 eventStreamRefused,
		"application/json":                 eventStreamRefused,
		"text/event-stream":                eventStreamPreferred,
		"TEXT/Event-Stream; charset=utf-8": eventStreamPreferred,
		"text/event-stream;q=0":            eventStreamRefused,
		"text/event-stream;q=0, */*":       eventStreamRefused,
		urqlDefaultAccept:                  eventStreamAcceptable,
		"application/json;q=0.5, text/event-stream":                        eventStreamPreferred,
		"text/event-stream;q=0.5, application/json":                        eventStreamAcceptable,
		"text/event-stream;q=0.9, application/graphql-response+json;q=0.9": eventStreamAcceptable,
		"text/event-stream, */*":                                           eventStreamAcceptable,
		"text/event-stream, */*;q=0.8":                                     eventStreamPreferred,
		"*/*":                                                              eventStreamAcceptable,
		"text/*":                                                           eventStreamPreferred,
		"text/event-stream;q=2":                                            eventStreamRefused,
		"text/event-stream;q=x, application/json":                          eventStreamRefused,
		"text/event-stream, application/json;q=0":                          eventStreamPreferred,
		"*/event-stream":                                                   eventStreamRefused,
	} {
		var values []string
		if accept != "" {
			values = []string{accept}
		}
		if got := negotiateEventStream(values); got != want {
			t.Fatalf("Accept %q negotiated %d, want %d", accept, got, want)
		}
	}
	if got := negotiateEventStream([]string{"application/json;q=0.4", "text/event-stream;q=0.6"}); got != eventStreamPreferred {
		t.Fatalf("repeated Accept headers negotiated %d", got)
	}
}

func postGraphQL(t *testing.T, url, body, accept string) (*http.Response, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer valid")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(payload)
}

func TestSSEAcceptNegotiationChoosesTheTransportPerOperation(t *testing.T) {
	const query = `{"query":"query Current { viewer }","operationName":"Current"}`
	const subscription = `{"query":"subscription { ticks }"}`
	const queryJSON = "{\"data\":{\"viewer\":41}}\n"
	const queryStream = "event: next\ndata: {\"data\":{\"viewer\":41}}\n\nevent: complete\n\n"
	const tickStream = "event: next\ndata: {\"data\":{\"ticks\":1}}\n\nevent: complete\n\n"
	const jsonType = "application/json; charset=utf-8"
	for _, testCase := range []struct {
		name, accept, body, contentType, want string
	}{
		{"urql query", urqlDefaultAccept, query, jsonType, queryJSON},
		{"urql subscription", urqlDefaultAccept, subscription, eventStreamMediaType, tickStream},
		{"preferred by quality", "application/json;q=0.5, text/event-stream", query, eventStreamMediaType, queryStream},
		{"outweighed by quality", "text/event-stream;q=0.5, application/json", query, jsonType, queryJSON},
		{"outweighed subscription", "text/event-stream;q=0.5, application/json", subscription, eventStreamMediaType, tickStream},
		{"tied with wildcard", "text/event-stream, */*", query, jsonType, queryJSON},
		{"wildcard subscription", "*/*", subscription, eventStreamMediaType, tickStream},
		{"refused by quality", "text/event-stream;q=0, application/json", subscription, jsonType, "SUBSCRIPTION_TRANSPORT_REQUIRED"},
		{"no Accept", "", subscription, jsonType, "SUBSCRIPTION_TRANSPORT_REQUIRED"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stream := newSSETestStream()
			stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
			close(stream.values)
			_, _, host := newSSEServer(t, events.Limits{}, stream)
			response, body := postGraphQL(t, host.URL, testCase.body, testCase.accept)
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != testCase.contentType {
				t.Fatalf("status=%d type=%q body=%q", response.StatusCode, response.Header.Get("Content-Type"), body)
			}
			refusal := strings.HasPrefix(testCase.want, "SUBSCRIPTION_")
			if refusal && !strings.Contains(body, `"code":"`+testCase.want+`"`) || !refusal && body != testCase.want {
				t.Fatalf("body = %q, want %q", body, testCase.want)
			}
		})
	}
}
