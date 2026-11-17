package outbox

import (
	"errors"
	"reflect"
	"testing"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestStoredCausationValidationDoesNotRequireActiveDelivery(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := publisherValidLease(t, fixture)
	resolver := incompatibleDeliveryResolver{publisherTestResolver{fixture.Registry}}
	before := codecFactRow(lease.Facts[0])
	batch, err := ValidateStoredCausation(lease.Delivery.CausationID, lease.Facts, resolver, ValidationLimits{})
	if err != nil || !batch.Valid() || len(batch.Events()) != len(lease.Facts) {
		t.Fatalf("valid historical archive group: batch=%v error=%v", batch.Valid(), err)
	}
	if !reflect.DeepEqual(before, codecFactRow(lease.Facts[0])) {
		t.Fatal("archive validation changed immutable fact storage")
	}
	_, err = ValidateDeliveryCausation(lease.Delivery.CausationID, lease.Facts, resolver, resolver, ValidationLimits{})
	assertCausationValidationCode(t, err, "event-schema-incompatible")
	lease.Facts[0].TransactionOrdinal++
	_, err = ValidateStoredCausation(lease.Delivery.CausationID, lease.Facts, resolver, ValidationLimits{})
	assertCausationValidationCode(t, err, "fact-order-invalid")
}

func TestStoredCausationValidationRequiresCompletePolicyHistory(t *testing.T) {
	_, sealed, _, resolver, _ := publisherHistoryRows(t)
	lease := publisherHistoryLease(sealed)
	if _, err := ValidateStoredCausation(lease.Delivery.CausationID, lease.Facts, resolver, ValidationLimits{}); err != nil {
		t.Fatalf("valid V4 archive group: %v", err)
	}
	for _, change := range []struct {
		name  string
		apply func([]eventprovider.FactRow) []eventprovider.FactRow
	}{
		{"missing member", func(rows []eventprovider.FactRow) []eventprovider.FactRow { return rows[:1] }},
		{"changed recorded times", func(rows []eventprovider.FactRow) []eventprovider.FactRow {
			rows[0].RecordedAt, rows[1].RecordedAt = rows[1].RecordedAt, rows[0].RecordedAt
			return rows
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			rows := change.apply(publisherHistoryLease(sealed).Facts)
			_, err := ValidateStoredCausation(lease.Delivery.CausationID, rows, resolver, ValidationLimits{})
			assertCausationValidationCode(t, err, "policy-history-invalid")
		})
	}
	// Merely relabeling the columns cannot turn V4 history into legacy V3.
	for index := range lease.Facts {
		lease.Facts[index].FactVersion = int64(mutationfact.FormatVersion)
		lease.Facts[index].CodecIdentity = mutationfact.CodecIdentity
	}
	_, err := ValidateStoredCausation(lease.Delivery.CausationID, lease.Facts, resolver, ValidationLimits{})
	assertCausationValidationCode(t, err, "fact-invalid")
}

func assertCausationValidationCode(t *testing.T, err error, want string) {
	t.Helper()
	var invalid *ValidationError
	if !errors.As(err, &invalid) || invalid.Code != want {
		t.Fatalf("validation error=%v, want %s", err, want)
	}
}
