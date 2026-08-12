package providertest

import (
	"testing"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

func TestWholeCausationOracleAcceptsExactOrderedLease(t *testing.T) {
	assertWholeCausation(t, eventprovider.Lease{
		Delivery: eventprovider.Delivery{CausationID: "cause", Status: eventprovider.StatusLeased},
		Facts:    []eventprovider.FactRow{{CausationID: "cause", TransactionOrdinal: 1}, {CausationID: "cause", TransactionOrdinal: 2}},
	}, "cause", 2)
}
