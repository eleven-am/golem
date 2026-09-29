package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type refusingSubscribeTransport struct {
	events.EventTransport
	failure error
}

func (transport refusingSubscribeTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport refusingSubscribeTransport) Subscribe(context.Context, events.Subscription) (events.Stream, error) {
	return nil, transport.failure
}

func TestCallerEventsFailsWhenItEndsBeforeTheTransportIsLive(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		cancel  bool
		want    events.ErrorCode
	}{
		{name: "terminal", failure: events.Failure(events.CodeEventSourceClosed), want: events.CodeSubscriptionSourceClosed},
		{name: "cancelled", failure: events.Failure(events.CodeEventTransport), cancel: true, want: events.CodeSubscriptionCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			schema := schematest.NewSubscribedBytesKeyed(t)
			provider := sqlite.New()
			database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "prelive.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
				t.Fatal(err)
			}
			allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultUser]()
				rules.CanRead(golem.All[mutationResultUser]())
				return rules.Freeze(schema.User)
			})
			allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultPost]()
				rules.CanRead(golem.All[mutationResultPost]())
				return rules.Freeze(schema.Post)
			})
			bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(),
				golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, nil))
			if err != nil {
				t.Fatal(err)
			}
			reader := newBytesKeyedReader(t, database, golem.SQLite, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
				config.Bindings = bindings
				config.EventTransport = refusingSubscribeTransport{EventTransport: config.EventTransport, failure: test.failure}
			})
			caller, err := reader.fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			subscribeContext, cancel := context.WithCancel(ctx)
			defer cancel()
			if test.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			stream, err := CallerEvents[mutationResultPrincipal, mutationResultActor, mutationResultPost, any](subscribeContext, caller, reader.posts)
			if code, ok := events.CodeOf(err); stream != nil || !ok || code != test.want {
				t.Fatalf("stream=%v err=%v, want no stream and %s", stream != nil, err, test.want)
			}
		})
	}
}
