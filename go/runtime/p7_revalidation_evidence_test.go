package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestRevalidationFailureDisconnectsWithStableError(t *testing.T) {
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
	const privateFailure = "principal=alice private revalidation database failure"
	fixture.app.resolvePrincipal = func(context.Context, p7EventPrincipal) (p7EventActor, error) {
		return p7EventActor{}, errors.New(privateFailure)
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1901, "visible", false))
	receiveContext, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, receiveErr := stream.Recv(receiveContext)
	code, ok := events.CodeOf(receiveErr)
	if !ok || code != events.CodeSubscriptionRevalidation {
		t.Fatalf("revalidation failure code=%q error=%v", code, receiveErr)
	}
	if strings.Contains(receiveErr.Error(), "alice") || strings.Contains(receiveErr.Error(), "database") || strings.Contains(receiveErr.Error(), "private") {
		t.Fatalf("private revalidation cause leaked: %v", receiveErr)
	}
}
