package codec

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type policyHistoryEventResolver struct {
	registry      *schema.Registry
	digest        golem.SchemaDigest
	refuseHistory bool
}

func (resolver policyHistoryEventResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return resolver.registry, resolver.digest, resolver.registry != nil && reference.EventSchema == resolver.digest && (reference.FormatVersion == 3 || reference.FormatVersion == 4)
}

func (resolver policyHistoryEventResolver) ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool) {
	return resolver.registry, !resolver.refuseHistory && resolver.registry != nil && resolver.registry.GenerationDigest() == generation && resolver.registry.PolicyCaptureFingerprint() == projection
}

func policyHistoryEventRow(t *testing.T, fixture schematest.GraphFixture, event byte) (mutationfact.OutboxRow, policyHistoryEventResolver) {
	t.Helper()
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, enabled := model.EventSchema()
	if !enabled {
		t.Fatal("fixture lacks subscribed model")
	}
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{7})),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	fact, err := mutationfact.New(fixture.Registry, digest, mutationfact.EventID{event}, requirement, mutationfact.CausationID{2}, 2, 41, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := mutationfact.NewPolicyHistoryReference(1, fixture.Registry.PolicyCaptureFingerprint(), golem.SchemaDigest{9})
	if err != nil {
		t.Fatal(err)
	}
	fact, err = fact.WithPolicyHistory(reference)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(123, 456000))
	if err != nil {
		t.Fatal(err)
	}
	return stored, policyHistoryEventResolver{registry: fixture.Registry, digest: digest}
}

func TestPolicyHistoryEventV1PreservesNestedV4(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	stored, resolver := policyHistoryEventRow(t, fixture, 1)
	envelope, err := EncodeStoredRow(stored, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.FormatVersion() != 1 || envelope.CodecIdentity() != "golem.event.v1" {
		t.Fatal("outer event format changed")
	}
	metadata, snapshot := nestedParts(t, envelope.Encoded())
	if !bytes.Equal(metadata, stored.Metadata) || !bytes.Equal(snapshot, stored.DeleteSnapshot) {
		t.Fatal("outer codec rewrote nested V4 bytes")
	}
	encoded := envelope.Encoded()
	decoded, err := Decode(encoded, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Fact().FormatVersion() != 4 || decoded.Fact().CodecIdentity() != "golem.fact.v4" {
		t.Fatal("nested codec identity lost")
	}
	reference, present := decoded.Fact().PolicyHistory()
	if !present || reference.FormatVersion() != 1 || reference.ProjectionFingerprint() != fixture.Registry.PolicyCaptureFingerprint() || reference.Digest() != (golem.SchemaDigest{9}) {
		t.Fatal("nested graph reference changed")
	}
	if !bytes.Equal(decoded.Encoded(), encoded) || decoded.CausationID() != envelope.CausationID() || decoded.TransactionOrdinal() != envelope.TransactionOrdinal() || decoded.GenerationDigest() != fixture.Registry.GenerationDigest() {
		t.Fatal("outer envelope changed causal identities or bytes")
	}
	encoded[0] ^= 1
	if bytes.Equal(decoded.Encoded(), encoded) {
		t.Fatal("event retained caller byte storage")
	}
}

func TestPolicyHistoryEventRequiresExactProducerSchema(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	stored, resolver := policyHistoryEventRow(t, fixture, 1)
	envelope, err := EncodeStoredRow(stored, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, registry := range map[string]*schema.Registry{
		"missing": nil,
		"registry without matching capture despite advertised event schema": schematest.NewSubscribedGraph(t).Registry,
	} {
		t.Run(name, func(t *testing.T) {
			wrong := policyHistoryEventResolver{registry: registry, digest: resolver.digest}
			if _, err := EncodeStoredRow(stored, wrong, Limits{}); err == nil {
				t.Fatal("stored history event accepted without exact producer")
			}
			if _, err := Decode(envelope.Encoded(), wrong, Limits{}); err == nil {
				t.Fatal("wire history event accepted without exact producer")
			}
		})
	}
	metadata, snapshot := nestedParts(t, envelope.Encoded())
	metadata = bytes.Clone(metadata)
	// V4 retains the V3 prefix: magic, version, length-prefixed codec, event ID, generation.
	generationOffset := len("GOLEMFACT") + 2 + 4 + len("golem.fact.v4") + 16
	metadata[generationOffset] ^= 1
	wrongGeneration, err := encodeParts(envelope.RecordedAt(), metadata, snapshot, HardMaxEncodedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(wrongGeneration, resolver, Limits{}); err == nil {
		t.Fatal("outer event accepted a different exact producer generation")
	}
}

func TestPolicyHistoryEventRejectsSwappedStoredMetadata(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	stored, resolver := policyHistoryEventRow(t, fixture, 1)
	other, _ := policyHistoryEventRow(t, fixture, 3)
	for name, change := range map[string]func(*mutationfact.OutboxRow){
		"swapped metadata":      func(row *mutationfact.OutboxRow) { row.Metadata = other.Metadata },
		"V3 duplicated version": func(row *mutationfact.OutboxRow) { row.FactVersion = 3 },
		"V3 duplicated codec":   func(row *mutationfact.OutboxRow) { row.CodecIdentity = "golem.fact.v3" },
		"wrong generation":      func(row *mutationfact.OutboxRow) { row.GenerationFingerprint = "wrong" },
		"wrong ordinal":         func(row *mutationfact.OutboxRow) { row.TransactionOrdinal++ },
	} {
		t.Run(name, func(t *testing.T) {
			row := stored
			change(&row)
			if _, err := EncodeStoredRow(row, resolver, Limits{}); err == nil {
				t.Fatal("mismatched stored history event accepted")
			}
		})
	}
}

func policyHistoryAnchoredEventRows(t *testing.T) ([]mutationfact.OutboxRow, []mutationfact.OutboxRow, mutationfact.PolicyHistory, policyHistoryEventResolver, string) {
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
	secret := strings.Repeat("private-graph-only-owner;", 4096)
	userID := mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: 1}))
	user := row(fixture.User, userID, mutationdecode.Value(policyir.FieldID(fixture.UserName), text(secret)))
	post := func(title string) mutationdecode.Row {
		return row(fixture.Post,
			mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 7})),
			mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
			mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text(title)))
	}
	before, middle, after := post("before"), post("middle"), post("after")
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
	resolver := policyHistoryEventResolver{registry: fixture.Registry, digest: digest}
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

func TestPolicyHistoryEventProjectsAnchorWithoutPrivateGraph(t *testing.T) {
	base, sealed, companion, resolver, secret := policyHistoryAnchoredEventRows(t)
	if !bytes.Contains(sealed[0].DeleteSnapshot, []byte(secret)) || bytes.Contains(sealed[1].DeleteSnapshot, []byte(secret)) {
		t.Fatal("fixture must store the graph only in ordinal one's private evidence")
	}
	if err := mutationfact.ValidatePolicyHistoryCausation(sealed, companion, resolver); err != nil {
		t.Fatal(err)
	}
	missingHistory := resolver
	missingHistory.refuseHistory = true
	if _, err := EncodeStoredRow(sealed[0], missingHistory, Limits{}); err == nil {
		t.Fatal("stored anchor accepted without exact graph schema resolution")
	}
	for index, stored := range sealed {
		envelope, err := EncodeStoredRow(stored, resolver, Limits{MaxEncodedBytes: 4096})
		if err != nil {
			t.Fatal(err)
		}
		metadata, snapshot := nestedParts(t, envelope.Encoded())
		if !bytes.Equal(metadata, stored.Metadata) {
			t.Fatal("projection changed nested fact metadata")
		}
		if !bytes.Contains(snapshot, base[index].DeleteSnapshot) {
			t.Fatal("projection changed the canonical base before-image bytes")
		}
		decoded, err := Decode(envelope.Encoded(), resolver, Limits{MaxEncodedBytes: 4096})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded.Encoded(), envelope.Encoded()) || decoded.TransactionOrdinal() != uint32(index+1) || decoded.FormatVersion() != 1 {
			t.Fatal("projected event changed canonical bytes, ordinal, or outer version")
		}
		reference, present := decoded.Fact().PolicyHistory()
		if !present || reference.Digest() != companion.Digest() || reference.ProjectionFingerprint() != companion.ProjectionFingerprint() {
			t.Fatal("transport lost its authoritative graph reference")
		}
		original, err := mutationfact.ValidateStoredRow(base[index], resolver)
		if err != nil {
			t.Fatal(err)
		}
		before, originalPresent := original.PrivateDeleteSnapshot()
		projectedBefore, projectedPresent := decoded.Fact().PrivateDeleteSnapshot()
		if !originalPresent || !projectedPresent || !reflect.DeepEqual(before, projectedBefore) {
			t.Fatal("transport changed the private before-image")
		}
		notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(notice.Encoded(), []byte("private-graph-only-owner;")) || bytes.Contains(notice.Encoded(), companion.Encoded()) {
			t.Fatal("private causal graph leaked into transport notice")
		}
		if index == 0 {
			missingAnchor := stored
			missingAnchor.DeleteSnapshot = bytes.Clone(snapshot)
			if _, err := EncodeStoredRow(missingAnchor, resolver, Limits{}); err == nil {
				t.Fatal("stored ordinal one accepted the graph-free transport projection")
			}
			withGraph, err := encodeParts(envelope.RecordedAt(), metadata, stored.DeleteSnapshot, HardMaxEncodedBytes)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(withGraph, resolver, Limits{}); err == nil {
				t.Fatal("transport accepted embedded private graph evidence")
			}
		}
	}
}
