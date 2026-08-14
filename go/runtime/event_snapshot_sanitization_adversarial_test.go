package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
)

type secretPrincipalSnapshotError string

func (failure secretPrincipalSnapshotError) Error() string { return string(failure) }

func TestSnapshotPrincipalFailureIsSanitizedBeforePublicStreamReturn(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "principal=alice token=private-value"
	fixture.app.snapshotPrincipal = func(p7EventPrincipal) (p7EventPrincipal, error) {
		return p7EventPrincipal{}, secretPrincipalSnapshotError(secret)
	}
	_, err = CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor)
	if err == nil {
		t.Fatal("failed principal snapshot was accepted")
	}
	code, ok := events.CodeOf(err)
	if !ok || code != events.CodeSubscriptionRevalidation {
		t.Fatalf("snapshot failure code=%q ok=%t error=%v", code, ok, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("public subscription error leaked SnapshotPrincipal cause: %v", err)
	}
}

type p7MutablePrincipalSecret struct{ Token string }
type p7MutablePrincipal struct{ Session *p7MutablePrincipalSecret }

func TestDefaultMutablePrincipalValidationIsSanitized(t *testing.T) {
	const secret = "postgres-password=do-not-leak"
	principal := p7MutablePrincipal{Session: &p7MutablePrincipalSecret{Token: secret}}
	_, err := snapshotEventPrincipal(principal, nil)
	if err == nil {
		t.Fatal("mutable principal was retained without SnapshotPrincipal")
	}
	code, ok := events.CodeOf(err)
	if !ok || code != events.CodeSubscriptionRevalidation {
		t.Fatalf("mutable principal code=%q ok=%t error=%v", code, ok, err)
	}
	for _, forbidden := range []string{secret, "Session", "Token", "pointer", "principal."} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("mutable principal validation leaked %q: %v", forbidden, err)
		}
	}

	// Principal resolution itself remains independent of subscription snapshot
	// ownership and therefore retains the existing P3-P6 behavior.
	fixture := newP7EventRuntimeFixture(t)
	if _, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"}); err != nil {
		t.Fatalf("ordinary ForPrincipal behavior changed: %v", err)
	}
}
