package fact

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// This is a wire bound, separate from the graph's one-MiB scalar budget and
// 16K work budget. It also bounds hostile input before any decoder allocation.
const MaxPolicyHistoryEncodedBytes = 4 << 20

var policyHistoryMagic = []byte("GOLEMPOLICYHISTORY")

// PolicyHistorySchemaResolver resolves the exact producer generation and
// static projection contract. An event-schema-only resolver is insufficient.
type PolicyHistorySchemaResolver interface {
	ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool)
}

// PolicyHistory seals physical graph evidence and an ordered fact commitment.
// Its construction does not certify the database snapshot or prove that the
// facts match; the transaction producer and whole-causation validator own those
// respective checks. Encoded returns an owned copy of private evidence.
type PolicyHistory struct {
	causation                             CausationID
	generation, projection, facts, digest golem.SchemaDigest
	graph                                 history.Graph
	encoded                               []byte
}

func (value PolicyHistory) CausationID() CausationID                  { return value.causation }
func (value PolicyHistory) Generation() golem.SchemaDigest            { return value.generation }
func (value PolicyHistory) ProjectionFingerprint() golem.SchemaDigest { return value.projection }
func (value PolicyHistory) FactsDigest() golem.SchemaDigest           { return value.facts }
func (value PolicyHistory) Digest() golem.SchemaDigest                { return value.digest }
func (value PolicyHistory) Graph() history.Graph                      { return value.graph }
func (value PolicyHistory) Encoded() []byte                           { return append([]byte(nil), value.encoded...) }
func (value PolicyHistory) Reference() (PolicyHistoryReference, error) {
	return NewPolicyHistoryReference(PolicyHistoryFormatVersion, value.projection, value.digest)
}

func NewPolicyHistory(causation CausationID, facts golem.SchemaDigest, graph history.Graph) (PolicyHistory, error) {
	if causation == (CausationID{}) || facts == (golem.SchemaDigest{}) || graph.Generation() == (golem.SchemaDigest{}) || graph.ProjectionFingerprint() == (golem.SchemaDigest{}) {
		return PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: causation, fact commitment and validated capture graph are required")
	}
	value := PolicyHistory{causation: causation, generation: graph.Generation(), projection: graph.ProjectionFingerprint(), facts: facts, graph: graph}
	encoded, err := encodePolicyHistory(value)
	if err != nil {
		return PolicyHistory{}, err
	}
	value.encoded = encoded
	value.digest = policyHistoryDigest(encoded)
	return value, nil
}

func encodePolicyHistory(value PolicyHistory) ([]byte, error) {
	e := encoder{limit: MaxPolicyHistoryEncodedBytes}
	e.raw(policyHistoryMagic)
	e.u16(PolicyHistoryFormatVersion)
	e.text(PolicyHistoryCodecIdentity)
	e.u8(uint8(value.graph.Provider()))
	e.raw(value.causation[:])
	e.raw(value.generation[:])
	e.raw(value.projection[:])
	e.raw(value.facts[:])
	input := value.graph.Input()
	e.u32(uint32(len(input.Rows)))
	for _, row := range input.Rows {
		e.u8(uint8(row.Phase))
		e.row(row.Row)
	}
	e.u32(uint32(len(input.Roots)))
	for _, root := range input.Roots {
		e.row(root.Identity)
		e.u32(uint32(root.Before.Row))
		e.u32(uint32(root.After.Row))
	}
	e.u32(uint32(len(input.Extents)))
	for _, extent := range input.Extents {
		e.u8(uint8(extent.Phase))
		e.u32(uint32(extent.Parent))
		e.raw(extent.Field[:])
		e.raw(extent.Relation[:])
		e.u32(uint32(len(extent.Children)))
		for _, child := range extent.Children {
			e.u32(uint32(child))
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	return e.data, nil
}

func policyHistoryDigest(encoded []byte) golem.SchemaDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:policy-history-bytes:v1\x00"))
	_, _ = hash.Write(encoded)
	var result golem.SchemaDigest
	copy(result[:], hash.Sum(nil))
	return result
}

func DecodePolicyHistory(payload []byte, resolver PolicyHistorySchemaResolver) (PolicyHistory, error) {
	if resolver == nil {
		return PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: exact historical schema resolver is required")
	}
	if len(payload) > MaxPolicyHistoryEncodedBytes {
		return PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: encoded graph exceeds its byte limit")
	}
	d := decoder{data: append([]byte(nil), payload...)}
	magic, err := d.raw(len(policyHistoryMagic))
	if err != nil || !bytes.Equal(magic, policyHistoryMagic) {
		return PolicyHistory{}, d.fail("invalid policy history magic")
	}
	version, err := d.u16()
	if err != nil || version != PolicyHistoryFormatVersion {
		return PolicyHistory{}, d.fail("unsupported policy history version")
	}
	codec, err := d.text()
	if err != nil || codec != PolicyHistoryCodecIdentity {
		return PolicyHistory{}, d.fail("unsupported policy history codec")
	}
	provider, err := d.u8()
	if err != nil {
		return PolicyHistory{}, err
	}
	causation, err := d.fixed16()
	if err != nil {
		return PolicyHistory{}, err
	}
	generation, err := d.fixed32()
	if err != nil {
		return PolicyHistory{}, err
	}
	projection, err := d.fixed32()
	if err != nil {
		return PolicyHistory{}, err
	}
	facts, err := d.fixed32()
	if err != nil {
		return PolicyHistory{}, err
	}
	if causation == ([16]byte{}) || generation == ([32]byte{}) || projection == ([32]byte{}) || facts == ([32]byte{}) {
		return PolicyHistory{}, d.fail("policy history identities and commitments must be non-zero")
	}
	registry, found := resolver.ResolvePolicyHistorySchema(golem.SchemaDigest(generation), golem.SchemaDigest(projection))
	if !found || registry == nil || registry.GenerationDigest() != golem.SchemaDigest(generation) || registry.PolicyCaptureFingerprint() != golem.SchemaDigest(projection) {
		return PolicyHistory{}, d.fail("exact policy history schema is unavailable")
	}
	budget := policyHistoryWireBudget{remaining: history.MaxNodes}
	count, err := budget.count(&d, 21)
	if err != nil {
		return PolicyHistory{}, err
	}
	input := history.Input{Rows: make([]history.PhaseRow, int(count))}
	for index := range input.Rows {
		phase, err := d.u8()
		if err != nil {
			return PolicyHistory{}, err
		}
		row, err := d.rowLimited(registry, uint32(budget.remaining), true)
		if err != nil {
			return PolicyHistory{}, err
		}
		budget.remaining -= len(row.Cells())
		input.Rows[index] = history.PhaseRow{Phase: history.Phase(phase), Row: row}
	}
	count, err = budget.count(&d, 28)
	if err != nil {
		return PolicyHistory{}, err
	}
	input.Roots = make([]history.Root, int(count))
	for index := range input.Roots {
		identity, err := d.rowLimited(registry, uint32(budget.remaining), true)
		if err != nil {
			return PolicyHistory{}, err
		}
		budget.remaining -= len(identity.Cells())
		before, err := d.u32()
		if err != nil {
			return PolicyHistory{}, err
		}
		after, err := d.u32()
		if err != nil {
			return PolicyHistory{}, err
		}
		input.Roots[index] = history.Root{Identity: identity, Before: history.Presence{Present: before != 0, Row: history.RowRef(before)}, After: history.Presence{Present: after != 0, Row: history.RowRef(after)}}
	}
	count, err = budget.count(&d, 41)
	if err != nil {
		return PolicyHistory{}, err
	}
	input.Extents = make([]history.Extent, int(count))
	for index := range input.Extents {
		phase, err := d.u8()
		if err != nil {
			return PolicyHistory{}, err
		}
		parent, err := d.u32()
		if err != nil {
			return PolicyHistory{}, err
		}
		field, err := d.fixed16()
		if err != nil {
			return PolicyHistory{}, err
		}
		relation, err := d.fixed16()
		if err != nil {
			return PolicyHistory{}, err
		}
		children, err := budget.count(&d, 4)
		if err != nil {
			return PolicyHistory{}, err
		}
		extent := history.Extent{Phase: history.Phase(phase), Parent: history.RowRef(parent), Field: policyir.FieldID(field), Relation: policyir.RelationID(relation), Children: make([]history.RowRef, int(children))}
		for child := range extent.Children {
			reference, err := d.u32()
			if err != nil {
				return PolicyHistory{}, err
			}
			extent.Children[child] = history.RowRef(reference)
		}
		input.Extents[index] = extent
	}
	if d.remaining() != 0 {
		return PolicyHistory{}, d.fail("trailing policy history bytes")
	}
	graph, err := history.New(registry, policyir.Provider(provider), input)
	if err != nil {
		return PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: invalid physical graph: %w", err)
	}
	value, err := NewPolicyHistory(CausationID(causation), golem.SchemaDigest(facts), graph)
	if err != nil {
		return PolicyHistory{}, err
	}
	if !bytes.Equal(value.encoded, payload) {
		return PolicyHistory{}, d.fail("policy history is not canonically encoded")
	}
	return value, nil
}

type policyHistoryWireBudget struct{ remaining int }

func (budget *policyHistoryWireBudget) count(d *decoder, minimumBytes uint64) (uint32, error) {
	count, err := d.u32()
	if err != nil {
		return 0, err
	}
	if uint64(count) > uint64(budget.remaining) || uint64(count)*minimumBytes > uint64(d.remaining()) {
		return 0, d.fail("invalid policy history collection count")
	}
	budget.remaining -= int(count)
	return count, nil
}
