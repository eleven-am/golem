package providertest

import (
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

func TestWholeCausationOracleAcceptsExactOrderedLease(t *testing.T) {
	deadline := time.Unix(1, 0).UTC()
	assertWholeCausation(t, eventprovider.Lease{
		Delivery: eventprovider.Delivery{CausationID: "cause", Status: eventprovider.StatusLeased, AvailableAt: deadline, LeaseUntil: &deadline},
		Facts:    []eventprovider.FactRow{{CausationID: "cause", TransactionOrdinal: 1}, {CausationID: "cause", TransactionOrdinal: 2}},
	}, "cause", 2)
}
