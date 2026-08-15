package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestCapabilitiesReportExternalWritesObservedFalse(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	capabilities := fixture.app.EventCapabilities()
	if capabilities.ExternalWritesObserved() || len(capabilities.CDCAdapterIdentities()) != 0 {
		t.Fatalf("no-CDC app reported adapters=%v externalWritesObserved=%t", capabilities.CDCAdapterIdentities(), capabilities.ExternalWritesObserved())
	}
	if capabilities.Provider() != golem.SQLite || !capabilities.PublisherEnabled() || len(capabilities.SubscriptionModelIDs()) != 1 {
		t.Fatalf("capability snapshot omitted actual event runtime state: provider=%s publisher=%t models=%x", capabilities.Provider(), capabilities.PublisherEnabled(), capabilities.SubscriptionModelIDs())
	}
}

func TestExternalInsertUpdateDeleteInvisibleWithoutCDC(t *testing.T) {
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
	id := mutationResultUUIDText(10)
	if _, err := fixture.app.database.Exec(`INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, id, mutationResultUUIDText(1), "external"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.Exec(`UPDATE "posts" SET "title"=? WHERE "id"=?`, "external-updated", id); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.Exec(`DELETE FROM "posts" WHERE "id"=?`, id); err != nil {
		t.Fatal(err)
	}
	receiveContext, stop := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer stop()
	_, receiveErr := stream.Recv(receiveContext)
	eventCode, ok := events.CodeOf(receiveErr)
	if !ok || eventCode != events.CodeSubscriptionCancelled {
		t.Fatalf("external SQL unexpectedly produced an event: %v", receiveErr)
	}
	if fixture.app.EventCapabilities().ExternalWritesObserved() {
		t.Fatal("direct SQL changed the no-CDC capability claim")
	}
}
