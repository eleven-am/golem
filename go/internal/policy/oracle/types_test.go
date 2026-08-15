// Package oracle owns the provider-neutral P2-H agreement corpus and the
// comparison protocol shared by the Go evaluator and SQL-provider adapters.
package oracle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// CanonicalSeed is recorded with every checked-in corpus. Generated agreement
// failures must report this value (or a deterministically derived value) so the
// same predicate tree can be reproduced.
const CanonicalSeed int64 = 0x2_474f_4c45_4d

// Identity is a provider-independent stable logical key tuple. It is opaque to
// the harness; provider seeders decide how the tuple is encoded in physical
// columns and return exactly this value after query execution.
type Identity string

// CellKind describes one provider-neutral seed cell.
type CellKind uint8

const (
	CellNull    CellKind = 1
	CellValue   CellKind = 2
	CellRawJSON CellKind = 3
)

// SeedScope separates rows in the normally constrained migrated schema from
// the deliberate dangling-relation subfixture. Providers must not disable
// foreign keys on their shared pool to insert the latter.
type SeedScope uint8

const (
	SeedNormal           SeedScope = 1
	SeedDanglingRelation SeedScope = 2
)

// SeedCell is one scalar column value. RawJSON is reserved for deliberately
// malformed-but-physically-insertable scalar-list/JSON fixtures; it is never a
// policy operand and must be inserted through the provider's JSON codec path.
type SeedCell struct {
	field   ir.FieldID
	kind    CellKind
	value   ir.Value
	rawJSON string
}

func NullCell(field ir.FieldID) SeedCell { return SeedCell{field: field, kind: CellNull} }

func ValueCell(field ir.FieldID, value ir.Value) (SeedCell, error) {
	if field == (ir.FieldID{}) {
		return SeedCell{}, fmt.Errorf("policy oracle: seed cell has zero field ID")
	}
	if err := value.Validate(); err != nil {
		return SeedCell{}, fmt.Errorf("policy oracle: invalid seed value: %w", err)
	}
	return SeedCell{field: field, kind: CellValue, value: value}, nil
}

func RawJSONCell(field ir.FieldID, raw string) (SeedCell, error) {
	if field == (ir.FieldID{}) {
		return SeedCell{}, fmt.Errorf("policy oracle: raw JSON cell has zero field ID")
	}
	if strings.TrimSpace(raw) == "" {
		return SeedCell{}, fmt.Errorf("policy oracle: raw JSON cell is empty")
	}
	return SeedCell{field: field, kind: CellRawJSON, rawJSON: raw}, nil
}

func (cell SeedCell) FieldID() ir.FieldID { return cell.field }
func (cell SeedCell) Kind() CellKind      { return cell.kind }
func (cell SeedCell) Value() (ir.Value, bool) {
	return cell.value, cell.kind == CellValue
}
func (cell SeedCell) RawJSON() (string, bool) { return cell.rawJSON, cell.kind == CellRawJSON }

// Row contains both the canonical seed cells and the fully dependency-loaded
// evaluator record for one logical identity.
type Row struct {
	identity Identity
	model    ir.ModelID
	scope    SeedScope
	cells    []SeedCell
	record   evaluate.Record
}

func (row Row) Identity() Identity               { return row.identity }
func (row Row) ModelID() ir.ModelID              { return row.model }
func (row Row) Scope() SeedScope                 { return row.scope }
func (row Row) Cells() []SeedCell                { return append([]SeedCell(nil), row.cells...) }
func (row Row) EvaluatorRecord() evaluate.Record { return row.record }

// FieldSpec is the logical schema information needed by a fixture seeder. SQL
// identifiers here describe only the checked-in fixture; rendered policy SQL
// must still resolve identifiers from the P1 physical registry.
type FieldSpec struct {
	id       ir.FieldID
	model    ir.ModelID
	name     string
	column   string
	typeRef  ir.TypeRef
	nullable bool
}

func (field FieldSpec) ID() ir.FieldID      { return field.id }
func (field FieldSpec) ModelID() ir.ModelID { return field.model }
func (field FieldSpec) Name() string        { return field.name }
func (field FieldSpec) Column() string      { return field.column }
func (field FieldSpec) Type() ir.TypeRef    { return field.typeRef }
func (field FieldSpec) Nullable() bool      { return field.nullable }

// ModelSpec describes a corpus model and its ordered logical identity fields.
type ModelSpec struct {
	id             ir.ModelID
	name           string
	table          string
	identityFields []ir.FieldID
}

func (model ModelSpec) ID() ir.ModelID { return model.id }
func (model ModelSpec) Name() string   { return model.name }
func (model ModelSpec) Table() string  { return model.table }
func (model ModelSpec) IdentityFields() []ir.FieldID {
	return append([]ir.FieldID(nil), model.identityFields...)
}

// Correlation is one ordered parent-to-child scalar-field pair.
type Correlation struct {
	parent ir.FieldID
	child  ir.FieldID
}

func (pair Correlation) ParentFieldID() ir.FieldID { return pair.parent }
func (pair Correlation) ChildFieldID() ir.FieldID  { return pair.child }

// RelationSpec describes one traversal endpoint used by agreement cases.
type RelationSpec struct {
	id          ir.RelationID
	model       ir.ModelID
	field       ir.FieldID
	target      ir.ModelID
	cardinality ir.RelationCardinality
	correlation []Correlation
}

func (relation RelationSpec) ID() ir.RelationID                   { return relation.id }
func (relation RelationSpec) ModelID() ir.ModelID                 { return relation.model }
func (relation RelationSpec) FieldID() ir.FieldID                 { return relation.field }
func (relation RelationSpec) TargetModelID() ir.ModelID           { return relation.target }
func (relation RelationSpec) Cardinality() ir.RelationCardinality { return relation.cardinality }
func (relation RelationSpec) Correlation() []Correlation {
	return append([]Correlation(nil), relation.correlation...)
}

// Probe is one frozen predicate and one reproducible agreement obligation.
// Exactly one primary probe is present for each closed operator identity.
type Probe struct {
	name       string
	operatorID ir.OperatorID
	condition  ir.Condition
	primary    bool
	mutation   string
}

func (probe Probe) Name() string              { return probe.name }
func (probe Probe) OperatorID() ir.OperatorID { return probe.operatorID }
func (probe Probe) Condition() ir.Condition   { return probe.condition }
func (probe Probe) Primary() bool             { return probe.primary }
func (probe Probe) Mutation() string          { return probe.mutation }

// Corpus is an immutable provider-neutral agreement fixture.
type Corpus struct {
	seed      int64
	models    []ModelSpec
	fields    []FieldSpec
	relations []RelationSpec
	rows      []Row
	probes    []Probe
}

func (corpus Corpus) Seed() int64 { return corpus.seed }
func (corpus Corpus) Models() []ModelSpec {
	result := append([]ModelSpec(nil), corpus.models...)
	for index := range result {
		result[index].identityFields = append([]ir.FieldID(nil), result[index].identityFields...)
	}
	return result
}
func (corpus Corpus) Fields() []FieldSpec { return append([]FieldSpec(nil), corpus.fields...) }
func (corpus Corpus) Relations() []RelationSpec {
	result := append([]RelationSpec(nil), corpus.relations...)
	for index := range result {
		result[index].correlation = append([]Correlation(nil), result[index].correlation...)
	}
	return result
}
func (corpus Corpus) Rows() []Row {
	result := append([]Row(nil), corpus.rows...)
	for index := range result {
		result[index].cells = append([]SeedCell(nil), result[index].cells...)
	}
	return result
}
func (corpus Corpus) Probes() []Probe { return append([]Probe(nil), corpus.probes...) }

func (corpus Corpus) rowsFor(model ir.ModelID) []Row {
	rows := make([]Row, 0)
	for _, row := range corpus.rows {
		if row.model == model {
			rows = append(rows, row)
		}
	}
	return rows
}

// DerivedSeed returns stable, well-spaced seeds for bounded generated cases.
func DerivedSeed(iteration uint32) int64 {
	return CanonicalSeed + int64(iteration)*-7046029254386353131
}

// SQLResult is the exact four-query polarity protocol from the P2 provider
// agreement. UnknownCount is separate so adapters do not need to manufacture
// identities for unknown rows. SelectionStatements counts only executions of
// the identity-selection query, not the three diagnostic queries.
type SQLResult struct {
	Selected            []Identity
	UnknownCount        uint64
	IsNotTrue           []Identity
	Negated             []Identity
	SelectionStatements uint32
}

// ControlResult proves the adapter/database can observe three-valued SQL. The
// deliberately unguarded nullable comparison must yield unknowns and make
// IS NOT TRUE differ from NOT(F).
type ControlResult struct {
	UnknownCount uint64
	IsNotTrue    []Identity
	Negated      []Identity
}

// Engine is the sole integration seam required from a provider adapter. It
// does not expose or guess any renderer API; adapters compile and execute using
// their provider's real exported contract.
type Engine interface {
	Name() string
	Control(context.Context, Corpus) (ControlResult, error)
	Run(context.Context, Corpus, Probe) (SQLResult, error)
}

func sortedIdentities(values []Identity) []Identity {
	result := append([]Identity(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
