package fact

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type policyHistoryCodecResolver struct {
	registry *schema.Registry
	event    golem.SchemaDigest
}

func (resolver policyHistoryCodecResolver) ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool) {
	return resolver.registry, resolver.registry != nil && resolver.registry.GenerationDigest() == generation && resolver.registry.PolicyCaptureFingerprint() == projection
}
func (resolver policyHistoryCodecResolver) ResolveFactSchema(reference SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return resolver.registry, resolver.event, resolver.registry != nil && resolver.event == reference.EventSchema
}

func policyHistoryCodecFixture(t *testing.T) (history.Graph, []OutboxRow, policyHistoryCodecResolver) {
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
	user := row(fixture.User, userID, mutationdecode.Value(policyir.FieldID(fixture.UserName), text("owner")))
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
	metadata, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := metadata.EventSchema()
	event, err := ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	key := []policyir.FieldID{policyir.FieldID(fixture.PostID)}
	requirement, err := mutationir.NewUpdateFactRequirement(key, key, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{policyir.FieldID(fixture.PostTitle)})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(event))
	if err != nil {
		t.Fatal(err)
	}
	var rows []OutboxRow
	for index, images := range [][2]mutationdecode.Row{{before, middle}, {middle, after}} {
		envelope, err := New(fixture.Registry, event, EventID{byte(index + 1)}, requirement, CausationID{8}, uint32(index+1), int64(index+10), &images[0], &images[1])
		if err != nil {
			t.Fatal(err)
		}
		stored, err := envelope.OutboxRow(time.Unix(100, int64(index)*1000))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, stored)
	}
	return graph, rows, policyHistoryCodecResolver{registry: fixture.Registry, event: event}
}

func TestPolicyHistoryGraphCodecAndWholeCausationSealing(t *testing.T) {
	graph, rows, resolver := policyHistoryCodecFixture(t)
	beforeDigest, err := PolicyHistoryFactsDigest(rows, resolver)
	if err != nil {
		t.Fatal(err)
	}
	sealed, companion, err := SealPolicyHistoryCausation(rows, graph, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if companion.FactsDigest() != beforeDigest || companion.Generation() != graph.Generation() || companion.ProjectionFingerprint() != graph.ProjectionFingerprint() {
		t.Fatal("companion lost its producer or fact commitment")
	}
	afterDigest, err := PolicyHistoryFactsDigest(sealed, resolver)
	if err != nil || afterDigest != beforeDigest {
		t.Fatalf("sealing changed base fact commitment: %v", err)
	}
	if err := ValidatePolicyHistoryCausation(sealed, companion, resolver); err != nil {
		t.Fatal(err)
	}
	for index, stored := range sealed {
		base, graph, err := decodePolicyHistoryPrivateEvidence(stored.DeleteSnapshot)
		if err != nil {
			t.Fatal(err)
		}
		if (index == 0) != (len(graph) != 0) {
			t.Fatal("graph was not stored exactly once on the first fact")
		}
		if rows[index].FactVersion != 3 || stored.FactVersion != 4 || !bytes.Equal(base, rows[index].DeleteSnapshot) || !bytes.Equal(stored.BeforeIdentity, rows[index].BeforeIdentity) || !bytes.Equal(stored.AfterIdentity, rows[index].AfterIdentity) {
			t.Fatal("sealing changed existing fact images or original rows")
		}
	}
	encoded := companion.Encoded()
	decoded, err := DecodePolicyHistory(encoded, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest() != companion.Digest() || !reflect.DeepEqual(decoded.Graph().Input(), graph.Input()) || !bytes.Equal(decoded.Encoded(), encoded) {
		t.Fatal("historical graph or canonical bytes changed")
	}
	if err := ValidatePolicyHistoryCausation(sealed, decoded, resolver); err != nil {
		t.Fatal(err)
	}
	encoded[0] ^= 1
	if bytes.Equal(encoded, decoded.Encoded()) {
		t.Fatal("historical graph retained caller-owned bytes")
	}
	if _, _, err := SealPolicyHistoryCausation(sealed, graph, resolver); err == nil {
		t.Fatal("a sealed causation could be rebound")
	}
}

func TestPolicyHistoryRejectsMixedMissingAndSwappedCausations(t *testing.T) {
	graph, rows, resolver := policyHistoryCodecFixture(t)
	sealed, companion, err := SealPolicyHistoryCausation(rows, graph, resolver)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		rows      []OutboxRow
		companion PolicyHistory
	}{
		{"missing", sealed, PolicyHistory{}},
		{"unsealed", rows, companion},
		{"partial", sealed[:1], companion},
		{"reordered", []OutboxRow{sealed[1], sealed[0]}, companion},
		{"mixed", []OutboxRow{sealed[0], rows[1]}, companion},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePolicyHistoryCausation(test.rows, test.companion, resolver); err == nil {
				t.Fatal("invalid causation was accepted")
			}
		})
	}
	changed := append([]OutboxRow(nil), sealed...)
	changed[1].RecordedAt = changed[1].RecordedAt.Add(time.Microsecond)
	if err := ValidatePolicyHistoryCausation(changed, companion, resolver); err == nil {
		t.Fatal("changed recorded time did not break the fact commitment")
	}
	changed = append([]OutboxRow(nil), sealed...)
	envelope, err := ValidateStoredRow(changed[1], resolver)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, known := envelope.PrivateDeleteSnapshot()
	if !known {
		t.Fatal("fixture must carry a before-image")
	}
	text, err := policyir.StringValue("different-valid-before-image")
	if err != nil {
		t.Fatal(err)
	}
	cells := snapshot.Cells()
	cells[0] = mutationdecode.Value(cells[0].FieldID(), text)
	replacement, err := mutationdecode.NewRow(resolver.registry, snapshot.ModelID(), cells)
	if err != nil {
		t.Fatal(err)
	}
	envelope.deleteSnapshot = &replacement
	changed[1], err = envelope.OutboxRow(changed[1].RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyHistoryCausation(changed, companion, resolver); err == nil {
		t.Fatal("changed private snapshot did not break the fact commitment")
	}
	other, err := NewPolicyHistory(CausationID{9}, companion.FactsDigest(), graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyHistoryCausation(sealed, other, resolver); err == nil {
		t.Fatal("graph from another causation was accepted")
	}
	other, err = NewPolicyHistory(companion.CausationID(), golem.SchemaDigest{9}, graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyHistoryCausation(sealed, other, resolver); err == nil {
		t.Fatal("graph from another fact set was accepted")
	}
}

func TestPolicyHistoryGraphWireRejectsHostileAndNoncanonicalInputs(t *testing.T) {
	graph, rows, resolver := policyHistoryCodecFixture(t)
	_, companion, err := SealPolicyHistoryCausation(rows, graph, resolver)
	if err != nil {
		t.Fatal(err)
	}
	encoded := companion.Encoded()
	for cut := 0; cut < len(encoded); cut++ {
		if _, err := DecodePolicyHistory(encoded[:cut], resolver); err == nil {
			t.Fatalf("truncated graph of %d bytes accepted", cut)
		}
	}
	header := len(policyHistoryMagic) + 2 + 4 + len(PolicyHistoryCodecIdentity)
	rowCountOffset := header + 1 + 16 + 32*3
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"magic", func(value []byte) []byte { value[0] ^= 1; return value }},
		{"version", func(value []byte) []byte { value[len(policyHistoryMagic)+1] ^= 1; return value }},
		{"provider", func(value []byte) []byte { value[header] = 255; return value }},
		{"generation", func(value []byte) []byte { value[header+1+16] ^= 1; return value }},
		{"projection", func(value []byte) []byte { value[header+1+16+32] ^= 1; return value }},
		{"rows-over-budget", func(value []byte) []byte {
			binary.BigEndian.PutUint32(value[rowCountOffset:], history.MaxNodes+1)
			return value
		}},
		{"cells-over-budget", func(value []byte) []byte {
			binary.BigEndian.PutUint32(value[rowCountOffset+4+1+16:], history.MaxNodes+1)
			return value
		}},
		{"trailing", func(value []byte) []byte { return append(value, 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodePolicyHistory(test.mutate(bytes.Clone(encoded)), resolver); err == nil {
				t.Fatal("malformed graph accepted")
			}
		})
	}
	if _, err := DecodePolicyHistory(make([]byte, MaxPolicyHistoryEncodedBytes+1), resolver); err == nil {
		t.Fatal("oversized graph accepted")
	}
	if _, err := DecodePolicyHistory(encoded, nil); err == nil {
		t.Fatal("graph accepted without exact-generation resolver")
	}
	// Swap two same-phase rows and remap every affected reference. This
	// preserves the graph's meaning but violates canonical wire ordering.
	input := graph.Input()
	rowBytes := func(row history.PhaseRow) []byte {
		e := encoder{}
		e.u8(uint8(row.Phase))
		e.row(row.Row)
		return e.data
	}
	first, second := rowBytes(input.Rows[0]), rowBytes(input.Rows[1])
	start := rowCountOffset + 4
	noncanonical := append([]byte(nil), encoded[:start]...)
	noncanonical = append(noncanonical, second...)
	noncanonical = append(noncanonical, first...)
	noncanonical = append(noncanonical, encoded[start+len(first)+len(second):]...)
	d := decoder{data: noncanonical}
	if _, err := d.raw(start); err != nil {
		t.Fatal(err)
	}
	for range input.Rows {
		if _, err := d.u8(); err != nil {
			t.Fatal(err)
		}
		if _, err := d.row(resolver.registry); err != nil {
			t.Fatal(err)
		}
	}
	remap := func() {
		offset := d.offset
		reference, err := d.u32()
		if err != nil {
			t.Fatal(err)
		}
		if reference == 1 {
			reference = 2
		} else if reference == 2 {
			reference = 1
		}
		binary.BigEndian.PutUint32(noncanonical[offset:], reference)
	}
	roots, err := d.u32()
	if err != nil {
		t.Fatal(err)
	}
	for range roots {
		if _, err := d.row(resolver.registry); err != nil {
			t.Fatal(err)
		}
		remap()
		remap()
	}
	extents, err := d.u32()
	if err != nil {
		t.Fatal(err)
	}
	for range extents {
		if _, err := d.u8(); err != nil {
			t.Fatal(err)
		}
		remap()
		if _, err := d.raw(32); err != nil {
			t.Fatal(err)
		}
		children, err := d.u32()
		if err != nil {
			t.Fatal(err)
		}
		for range children {
			remap()
		}
	}
	if _, err := DecodePolicyHistory(noncanonical, resolver); err == nil || !strings.Contains(err.Error(), "not canonically encoded") {
		t.Fatalf("equivalent noncanonical wire graph was not rejected at canonicality check: %v", err)
	}
	// Cross-phase references remain malformed even in producer input.
	input.Roots[0].Before, input.Roots[0].After = input.Roots[0].After, input.Roots[0].Before
	if _, err := history.New(resolver.registry, graph.Provider(), input); err == nil {
		t.Fatal("cross-phase root references accepted")
	}
}
