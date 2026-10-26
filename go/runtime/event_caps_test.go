package runtime

import (
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestPrincipalSubscriptionCapRequiresAPrincipalKey(t *testing.T) {
	_, err := validateEventConfiguration(Config[string, string]{EventLimits: events.Limits{MaxSubscriptionsPerPrincipal: 2}}, nil, golem.SQLite)
	if code, ok := events.CodeOf(err); !ok || code != events.CodeEventConfig {
		t.Fatalf("a cap without SubscriptionPrincipalKey was accepted: %v", err)
	}
	limits, err := validateEventConfiguration(Config[string, string]{
		EventLimits:              events.Limits{MaxSubscriptionsPerPrincipal: 2},
		SubscriptionPrincipalKey: func(principal string) (string, bool) { return principal, true },
	}, nil, golem.SQLite)
	if err != nil || limits.MaxSubscriptionsPerPrincipal != 2 {
		t.Fatalf("a keyed cap was refused: limits=%d err=%v", limits.MaxSubscriptionsPerPrincipal, err)
	}
	if _, err := validateEventConfiguration(Config[string, string]{}, nil, golem.SQLite); err != nil {
		t.Fatalf("an uncapped config without a key was refused: %v", err)
	}
}

func TestPrincipalSubscriptionCapCountsOnlyKeyedPrincipals(t *testing.T) {
	app := &App[string, string]{
		eventLimits:              events.Limits{MaxSubscriptionsPerPrincipal: 1},
		subscriptionPrincipalKey: func(principal string) (string, bool) { return principal, principal != "" },
	}
	release, err := app.reserveSubscription("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.reserveSubscription("alice"); err == nil {
		t.Fatal("a second stream for one principal was admitted over a cap of one")
	} else if code, _ := events.CodeOf(err); code != events.CodeSubscriptionLimit {
		t.Fatalf("over-cap code=%v", err)
	}
	for range 3 {
		if _, err := app.reserveSubscription(""); err != nil {
			t.Fatalf("an unkeyed principal was counted: %v", err)
		}
	}
	release()
	release()
	again, err := app.reserveSubscription("alice")
	if err != nil {
		t.Fatalf("releasing did not free the slot: %v", err)
	}
	again()
	if len(app.subscriptionCounts.counts) != 0 {
		t.Fatalf("released principals remain counted: %v", app.subscriptionCounts.counts)
	}
}
