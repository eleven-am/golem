package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
)

func TestIdentityOnlyEventStillReauthorizes(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany,
		Model:     fixture.schema.Post,
		Selection: []golem.RuntimeReadSelectionInput{{
			Kind: golem.RuntimeReadScalar, Field: fixture.schema.PostID,
		}},
		Projection: golem.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerFrozenReadEvents[p7EventPrincipal, p7EventActor, p7EventOracleValue](ctx, caller, read, false)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("identity-only subscription source did not start")
	}
	fixture.allow.Store(false)
	for len(fixture.resolved) != 0 {
		<-fixture.resolved
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1850, "visible", false))
	select {
	case allowed := <-fixture.resolved:
		if allowed {
			t.Fatal("identity-only event reused stale authorization")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity-only event skipped fresh authorization")
	}
	select {
	case observation := <-fixture.suppressed:
		if observation.Operation() != observe.OperationSubscriptionSuppression {
			t.Fatalf("identity-only suppression=%#v", observation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked identity-only event was not suppressed")
	}
}
