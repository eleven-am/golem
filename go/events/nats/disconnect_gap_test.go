package nats

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	subscriptionhub "github.com/eleven-am/golem/go/internal/subscription"
	"github.com/eleven-am/golem/go/internal/subscription/subscriptiontest"
)

func disconnectGapHub(t *testing.T, transport *Transport) *subscriptionhub.ModelHub[golem.EventID] {
	t.Helper()
	return disconnectGapHubWithSource(t, transport.Subscribe)
}

func disconnectGapHubWithSource(t *testing.T, source subscriptionhub.SourceFactory) *subscriptionhub.ModelHub[golem.EventID] {
	t.Helper()
	hub, err := subscriptionhub.NewModelHub(subscriptionhub.Config[golem.EventID]{
		Generation: subscriptiontest.Generation, Model: subscriptiontest.Model, Source: source,
		Limits: events.Limits{SubscriberQueue: 8, HubInputQueue: 8, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Evaluate: func(_ context.Context, notice events.Notice, _ subscriptionhub.SubscriberKey) (subscriptionhub.Evaluation[golem.EventID], error) {
			return subscriptionhub.Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hub.Shutdown(ctx)
	})
	return hub
}

func disconnectGapKey(t *testing.T) subscriptionhub.SubscriberKey {
	t.Helper()
	identity := func(domain string) subscriptionhub.CanonicalIdentity {
		result, err := subscriptionhub.NewCanonicalIdentity(domain, []byte(domain))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result, err := subscriptionhub.NewSubscriberKey(subscriptionhub.SubscriberKeyInput{
		Generation: subscriptiontest.Generation, Model: subscriptiontest.Model, Principal: identity("principal"), PolicyGeneration: identity("policy"),
		Filter: identity("filter"), Selection: identity("selection"), Dependencies: identity("dependencies"),
		EncoderShape: identity("encoder"), Membership: identity("membership"), Shareable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDisconnectEndsEveryStreamSoSubscribersResync(t *testing.T) {
	transport, publish := natsSubscriptionTestTransport(t, 64)
	requested, err := eventvalue.NewRoutedSubscription(subscriptiontest.Generation, golem.EventSchemaDigest(subscriptiontest.Generation), subscriptiontest.Model)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := transport.Subscribe(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := disconnectGapHub(t, transport).Subscribe(context.Background(), disconnectGapKey(t))
	if err != nil {
		t.Fatal(err)
	}
	publish(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if value, err := subscriber.Recv(ctx); err != nil || value != (golem.EventID{1}) {
		t.Fatalf("before the disconnect value=%v err=%v", value, err)
	}
	if _, err := raw.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	transport.markDisconnected()
	if transport.TransportAvailable() {
		t.Fatal("a disconnected transport reported available")
	}
	if _, err := raw.Recv(ctx); eventCode(err) != events.CodeEventTransport {
		t.Fatalf("a stream survived the disconnect: %v", err)
	}
	if _, err := subscriber.Recv(ctx); eventCode(err) != events.CodeSubscriptionResync {
		t.Fatalf("a subscriber survived the disconnect without %s: %v", events.CodeSubscriptionResync, err)
	}
}

func TestSubscribeDuringAnOutageGoesLiveOnlyAfterTheReconnect(t *testing.T) {
	transport, publish := natsSubscriptionTestTransport(t, 64)
	requested, err := eventvalue.NewRoutedSubscription(subscriptiontest.Generation, golem.EventSchemaDigest(subscriptiontest.Generation), subscriptiontest.Model)
	if err != nil {
		t.Fatal(err)
	}
	transport.markDisconnected()
	if stream, err := transport.Subscribe(context.Background(), requested); stream != nil || eventCode(err) != events.CodeEventTransport {
		t.Fatalf("a subscribe during the outage returned stream=%v err=%v", stream != nil, err)
	}
	hub := disconnectGapHub(t, transport)
	type result struct {
		stream *subscriptionhub.Stream[golem.EventID]
		err    error
	}
	subscribed := make(chan result, 1)
	go func() {
		stream, err := hub.Subscribe(context.Background(), disconnectGapKey(t))
		subscribed <- result{stream: stream, err: err}
	}()
	select {
	case early := <-subscribed:
		t.Fatalf("a hub subscribe went live during the outage: err=%v", early.err)
	default:
	}
	transport.markReconnected(maximumInboundPayload)
	live := <-subscribed
	if live.err != nil {
		t.Fatal(live.err)
	}
	publish(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if value, err := live.stream.Recv(ctx); err != nil || value != (golem.EventID{2}) {
		t.Fatalf("after the reconnect value=%v err=%v", value, err)
	}
}

func TestSubscribeDuringAnOutageEndsWithTheCallerContext(t *testing.T) {
	transport, _ := natsSubscriptionTestTransport(t, 64)
	transport.markDisconnected()
	refused := make(chan struct{}, 64)
	hub := disconnectGapHubWithSource(t, func(ctx context.Context, request events.Subscription) (events.Stream, error) {
		stream, err := transport.Subscribe(ctx, request)
		if err != nil {
			refused <- struct{}{}
		}
		return stream, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		stream *subscriptionhub.Stream[golem.EventID]
		err    error
	}
	subscribed := make(chan result, 1)
	go func() {
		stream, err := hub.Subscribe(ctx, disconnectGapKey(t))
		subscribed <- result{stream: stream, err: err}
	}()
	select {
	case <-refused:
	case early := <-subscribed:
		t.Fatalf("a subscribe during the outage returned before any refusal: stream=%v err=%v", early.stream != nil, early.err)
	}
	cancel()
	ended := <-subscribed
	if ended.stream != nil || eventCode(ended.err) != events.CodeSubscriptionCancelled {
		t.Fatalf("a subscribe cancelled during the outage returned stream=%v err=%v, want no stream and %s", ended.stream != nil, ended.err, events.CodeSubscriptionCancelled)
	}
}
