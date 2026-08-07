package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestP7InvalidForeignOrForgedEventOptionsTouchHubZeroTimes(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var forged golem.EventOption[p7EventPost]
	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor, forged); err == nil {
		t.Fatal("nil/forged event option was accepted")
	}

	identity := golem.GeneratedIdentityMetadata(fixture.schema.User, fixture.schema.UserKey, golem.PrimaryIdentity, fixture.schema.UserID)
	foreign := golem.GeneratedModelDescriptor[p7EventUser](fixture.schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.schema.UserID, fixture.schema.UserName}, nil, []golem.IdentityMetadata{identity}, nil,
	))
	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventUser, p7EventOracleValue](context.Background(), caller, foreign); err == nil {
		t.Fatal("subscription-disabled foreign model was accepted")
	}
	fixture.app.eventMu.Lock()
	hubs := len(fixture.app.eventHubs)
	fixture.app.eventMu.Unlock()
	if hubs != 0 {
		t.Fatalf("invalid requests created %d hubs", hubs)
	}
	select {
	case <-fixture.transport.subscribed:
		t.Fatal("invalid requests started transport work")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestP7SubscriptionFilterSelectionClassificationBeforeSourceWork(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	foreignField := golem.GeneratedTextField[p7EventPost, string](fixture.schema.UserName)
	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor,
		golem.EventWhere[p7EventPost](foreignField.Eq("secret"))); err == nil {
		t.Fatal("foreign-model filter reached source admission")
	}
	relationAsScalar := golem.GeneratedTextField[p7EventPost, string](fixture.schema.PostAuthor)
	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor,
		golem.EventSelect[p7EventPost](relationAsScalar)); err == nil {
		t.Fatal("relation field classified as a stored scalar selection")
	}

	fixture.app.eventMu.Lock()
	hubs := len(fixture.app.eventHubs)
	fixture.app.eventMu.Unlock()
	if hubs != 0 {
		t.Fatalf("invalid filter/selection created %d hubs", hubs)
	}
	select {
	case <-fixture.transport.subscribed:
		t.Fatal("invalid filter/selection started transport source work")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestP7CallerAndGraphQLSubscribeAuthorizeReadBeforeRegistration(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	fixture.allow.Store(false)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "denied"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor); err == nil {
		t.Fatal("typed caller registered a model-read-denied subscription")
	}
	graphQLRead, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany,
		Model:     fixture.schema.Post,
		Selection: []golem.RuntimeReadSelectionInput{{
			Kind: golem.RuntimeReadScalar, Field: fixture.schema.PostTitle,
		}},
		Projection: golem.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	// CallerFrozenReadEvents is the generated GraphQL adapter's sole runtime
	// handoff. It must execute the same read gate before eventHub registration.
	if _, err := CallerFrozenReadEvents[p7EventPrincipal, p7EventActor, p7EventOracleValue](context.Background(), caller, graphQLRead, true); err == nil {
		t.Fatal("GraphQL handoff registered a model-read-denied subscription")
	}

	fixture.app.eventMu.Lock()
	hubs := len(fixture.app.eventHubs)
	fixture.app.eventMu.Unlock()
	if hubs != 0 {
		t.Fatalf("denied caller/GraphQL requests registered %d hubs", hubs)
	}
	select {
	case <-fixture.transport.subscribed:
		t.Fatal("denied caller/GraphQL request started transport source work")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestP7FreshPrincipalActorPolicyAndExecutionPerEvent(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("transport subscription did not start")
	}
	resolvedBefore := fixture.resolves.Load()
	executionBefore := fixture.app.nextExecution.Load()
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1801, "visible", false))
	if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1801) {
		t.Fatal("first event identity changed")
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1802, "visible", false))
	if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1802) {
		t.Fatal("second event identity changed")
	}
	if got := fixture.resolves.Load() - resolvedBefore; got != 2 {
		t.Fatalf("per-event principal resolutions=%d want=2", got)
	}
	if got := fixture.app.nextExecution.Load() - executionBefore; got != 2 {
		t.Fatalf("per-event execution identities=%d want=2", got)
	}
}

func TestP7RevocationSuppressesNextEventAndGrantPermitsNext(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("transport subscription did not start")
	}
	fixture.allow.Store(false)
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1810, "visible", false))
	select {
	case observation := <-fixture.suppressed:
		if observation.Kind() != events.ObservationSuppression {
			t.Fatalf("revocation observation=%#v", observation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked event was not suppressed")
	}
	fixture.allow.Store(true)
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1811, "visible", false))
	if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1811) {
		t.Fatalf("revoked event escaped suppression before grant: %x", event.validated.Metadata().EventID())
	}
}
