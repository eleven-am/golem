package outbox

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type publisherHistoryResolver struct {
	registry *schema.Registry
	digest   golem.SchemaDigest
}

func (resolver publisherHistoryResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return resolver.registry, resolver.digest, resolver.registry != nil && reference.EventSchema == resolver.digest && (reference.FormatVersion == 3 || reference.FormatVersion == 4)
}

func (resolver publisherHistoryResolver) ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool) {
	return resolver.registry, resolver.registry != nil && resolver.registry.GenerationDigest() == generation && resolver.registry.PolicyCaptureFingerprint() == projection
}

func (resolver publisherHistoryResolver) CanDeliverEventSchema(model golem.ModelID, digest golem.EventSchemaDigest) bool {
	return (publisherTestResolver{registry: resolver.registry}).CanDeliverEventSchema(model, digest)
}

func publisherHistoryRows(t *testing.T) ([]mutationfact.OutboxRow, []mutationfact.OutboxRow, mutationfact.PolicyHistory, publisherHistoryResolver, string) {
	t.Helper()
	return publisherHistoryRowsWithValues(t, "private-graph-only-owner", "before", "middle", "after")
}

func publisherHistoryRowsWithValues(t *testing.T, secret, beforeTitle, middleTitle, afterTitle string) ([]mutationfact.OutboxRow, []mutationfact.OutboxRow, mutationfact.PolicyHistory, publisherHistoryResolver, string) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	text := func(value string) policyir.Value {
		parsed, err := policyir.StringValue(value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	row := func(model golem.ModelID, cells ...mutationdecode.Cell) mutationdecode.Row {
		value, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(model), cells)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	userID := mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: 1}))
	user := row(fixture.User, userID, mutationdecode.Value(policyir.FieldID(fixture.UserName), text(secret)))
	post := func(title string) mutationdecode.Row {
		return row(fixture.Post,
			mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 7})),
			mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
			mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text(title)))
	}
	before, middle, after := post(beforeTitle), post(middleTitle), post(afterTitle)
	graph, err := history.New(fixture.Registry, policyir.ProviderSQLite, history.Input{
		Rows:  []history.PhaseRow{{Phase: history.Before, Row: user}, {Phase: history.Before, Row: before}, {Phase: history.After, Row: user}, {Phase: history.After, Row: after}},
		Roots: []history.Root{{Identity: row(fixture.User, userID), Before: history.Presence{Present: true, Row: 1}, After: history.Presence{Present: true, Row: 3}}},
		Extents: []history.Extent{
			{Phase: history.Before, Parent: 1, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{2}},
			{Phase: history.After, Parent: 3, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{4}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := model.EventSchema()
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	key := []policyir.FieldID{policyir.FieldID(fixture.PostID)}
	requirement, err := mutationir.NewUpdateFactRequirement(key, key, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{policyir.FieldID(fixture.PostTitle)})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	resolver := publisherHistoryResolver{registry: fixture.Registry, digest: digest}
	var base []mutationfact.OutboxRow
	for index, images := range [][2]mutationdecode.Row{{before, middle}, {middle, after}} {
		fact, err := mutationfact.New(fixture.Registry, digest, mutationfact.EventID{byte(index + 1)}, requirement, mutationfact.CausationID{8}, uint32(index+1), int64(index+10), &images[0], &images[1])
		if err != nil {
			t.Fatal(err)
		}
		stored, err := fact.OutboxRow(time.Unix(100, int64(index)*1000))
		if err != nil {
			t.Fatal(err)
		}
		base = append(base, stored)
	}
	sealed, companion, err := mutationfact.SealPolicyHistoryCausation(base, graph, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return base, sealed, companion, resolver, secret
}

func publisherHistoryLease(rows []mutationfact.OutboxRow) eventprovider.Lease {
	lease := eventprovider.Lease{Delivery: eventprovider.Delivery{
		CausationID: rows[0].CausationID, Status: eventprovider.StatusLeased,
		LeaseToken: "00000000-0000-4000-8000-000000000001", AttemptCount: 1,
	}}
	for _, stored := range rows {
		lease.Facts = append(lease.Facts, eventprovider.FactRow{
			EventID: stored.EventID, FactVersion: stored.FactVersion, CodecIdentity: stored.CodecIdentity,
			GenerationFingerprint: stored.GenerationFingerprint, ModelID: stored.ModelID, Action: stored.Action,
			BeforeIdentity: bytes.Clone(stored.BeforeIdentity), AfterIdentity: bytes.Clone(stored.AfterIdentity),
			CausationID: stored.CausationID, TransactionOrdinal: stored.TransactionOrdinal,
			Metadata: bytes.Clone(stored.Metadata), DeleteSnapshot: bytes.Clone(stored.DeleteSnapshot), RecordedAt: stored.RecordedAt,
		})
	}
	return lease
}

func TestPublisherPolicyHistoryPublishesCompleteCausation(t *testing.T) {
	_, sealed, companion, resolver, secret := publisherHistoryRows(t)
	coordinator := &publisherTestCoordinator{renewed: true}
	transport := &captureTransport{}
	publisher := publisherForTest(t, coordinator, resolver, transport)
	if err := publisher.publishLease(context.Background(), time.Now(), publisherHistoryLease(sealed)); err != nil {
		t.Fatal(err)
	}
	if len(transport.batches) != 1 || coordinator.ackCalls != 1 || coordinator.blockCalls != 0 || coordinator.retryCalls != 0 {
		t.Fatal("valid historical causation was not published and acknowledged exactly once")
	}
	notices := transport.batches[0].Events()
	if len(notices) != 2 {
		t.Fatal("publication lost a causation member")
	}
	for index, notice := range notices {
		decoded, err := eventcodec.Decode(notice.Encoded(), resolver, eventcodec.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		reference, present := decoded.Fact().PolicyHistory()
		if !present || reference.Digest() != companion.Digest() || notice.TransactionOrdinal() != uint32(index+1) {
			t.Fatal("published historical reference or ordering changed")
		}
		if bytes.Contains(notice.Encoded(), []byte(secret)) {
			t.Fatal("publication leaked the private graph")
		}
	}
}

func TestPublisherPolicyHistoryBlocksOversizedValidCausation(t *testing.T) {
	// The boundary graph omits the intermediate title, but the second fact's
	// private before-image must retain it. Both events fit independently; only
	// their combined immutable evidence exceeds the released causal budget.
	base, sealed, companion, resolver, _ := publisherHistoryRowsWithValues(t,
		strings.Repeat("u", 440_000), strings.Repeat("b", 40_000), strings.Repeat("m", 150_000), strings.Repeat("a", 40_000))
	digest, err := mutationfact.PolicyHistoryFactsDigest(base, resolver)
	if err != nil || digest != companion.FactsDigest() {
		t.Fatalf("oversized fixture must retain a valid base fact commitment: %v", err)
	}
	storedBytes, transportBytes := 0, 0
	for _, row := range sealed {
		if row.EncodedBytes() >= eventprovider.MaximumCausationEvidenceBytes {
			t.Fatal("fixture must exceed the budget only when the complete group is counted")
		}
		if _, err := mutationfact.ValidateStoredRow(row, resolver); err != nil {
			t.Fatalf("oversized fixture contains an invalid individual stored fact: %v", err)
		}
		projected, err := eventcodec.EncodeStoredRow(row, resolver, eventcodec.Limits{MaxEncodedBytes: 256 << 10})
		if err != nil {
			t.Fatalf("individual event projection must fit transport: %v", err)
		}
		storedBytes += row.EncodedBytes()
		transportBytes += len(projected.Encoded())
	}
	if storedBytes <= eventprovider.MaximumCausationEvidenceBytes || transportBytes >= eventprovider.MaximumCausationEvidenceBytes {
		t.Fatalf("fixture must have oversized stored evidence and small transport projections: stored=%d transport=%d", storedBytes, transportBytes)
	}
	coordinator := &quarantineCoordinator{}
	transport := &captureTransport{}
	publisher := publisherForTest(t, coordinator, resolver, transport)
	if err := publisher.publishLease(context.Background(), time.Now(), publisherHistoryLease(sealed)); err != nil {
		t.Fatal(err)
	}
	codes := coordinator.blocked()
	if len(codes) != 1 || codes[0] != "policy-history-invalid" || len(transport.batches) != 0 || coordinator.ackCalls != 0 || coordinator.retryCalls != 0 {
		t.Fatalf("oversized causation escaped quarantine: codes=%v batches=%d acknowledgements=%d retries=%d", codes, len(transport.batches), coordinator.ackCalls, coordinator.retryCalls)
	}
}

func TestPublisherPolicyHistoryBlocksWholeDamagedCausation(t *testing.T) {
	base, sealed, _, resolver, _ := publisherHistoryRows(t)
	for _, test := range []struct {
		name              string
		change            func(*testing.T, *eventprovider.Lease)
		individuallyValid bool
		code              string
	}{
		{name: "valid before-image changed", individuallyValid: true, change: func(t *testing.T, lease *eventprovider.Lease) {
			before := lease.Facts[1].DeleteSnapshot
			lease.Facts[1].DeleteSnapshot = bytes.Replace(before, []byte("middle"), []byte("forged"), 1)
			if bytes.Equal(before, lease.Facts[1].DeleteSnapshot) {
				t.Fatal("fixture did not replace the second private before-image")
			}
		}},
		{name: "recorded times swapped", individuallyValid: true, change: func(t *testing.T, lease *eventprovider.Lease) {
			lease.Facts[0].RecordedAt, lease.Facts[1].RecordedAt = lease.Facts[1].RecordedAt, lease.Facts[0].RecordedAt
		}},
		{name: "V3 first member", individuallyValid: true, change: func(t *testing.T, lease *eventprovider.Lease) {
			lease.Facts[0] = publisherHistoryLease(base).Facts[0]
		}},
		{name: "V3 second member", individuallyValid: true, change: func(t *testing.T, lease *eventprovider.Lease) {
			lease.Facts[1] = publisherHistoryLease(base).Facts[1]
		}},
		{name: "anchor omitted", change: func(t *testing.T, lease *eventprovider.Lease) {
			fact, err := mutationfact.ValidateStoredRow(sealed[0], resolver)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := fact.TransportRow(sealed[0].RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			lease.Facts[0].DeleteSnapshot = projected.DeleteSnapshot
		}},
		{name: "second anchor", change: func(t *testing.T, lease *eventprovider.Lease) {
			lease.Facts[1].DeleteSnapshot = bytes.Clone(lease.Facts[0].DeleteSnapshot)
		}},
		{name: "incomplete group", individuallyValid: true, change: func(t *testing.T, lease *eventprovider.Lease) {
			lease.Facts = lease.Facts[:1]
		}},
		{name: "V4 metadata disguised as V3 columns", code: "fact-invalid", change: func(t *testing.T, lease *eventprovider.Lease) {
			for index := range lease.Facts {
				lease.Facts[index].FactVersion = 3
				lease.Facts[index].CodecIdentity = mutationfact.CodecIdentity
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := publisherHistoryLease(sealed)
			test.change(t, &lease)
			if test.individuallyValid {
				for _, row := range lease.Facts {
					if _, err := eventcodec.EncodeStoredRow(codecFactRow(row), resolver, eventcodec.Limits{}); err != nil {
						t.Fatalf("fixture must pass individual event validation: %v", err)
					}
				}
			}
			coordinator := &quarantineCoordinator{}
			transport := &captureTransport{}
			publisher := publisherForTest(t, coordinator, resolver, transport)
			if err := publisher.publishLease(context.Background(), time.Now(), lease); err != nil {
				t.Fatal(err)
			}
			code := test.code
			if code == "" {
				code = "policy-history-invalid"
			}
			codes := coordinator.blocked()
			if len(codes) != 1 || codes[0] != code || len(transport.batches) != 0 || coordinator.ackCalls != 0 || coordinator.retryCalls != 0 {
				t.Fatalf("damaged causation escaped quarantine: codes=%v batches=%d acknowledgements=%d retries=%d", codes, len(transport.batches), coordinator.ackCalls, coordinator.retryCalls)
			}
		})
	}
}
