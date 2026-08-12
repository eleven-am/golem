// Package sql compiles validated policy conditions into provider-rendered,
// parameterized SQL predicate fragments. It owns traversal, relation
// correlation, alias allocation, and bind ordering; dialects own leaf syntax
// and physical value codecs.
package sql

import (
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type ErrorCode string

const (
	CodeInput      ErrorCode = "P2_SQL_INPUT"
	CodeSchema     ErrorCode = "P2_SQL_SCHEMA"
	CodeProvider   ErrorCode = "P2_SQL_PROVIDER"
	CodeCapability ErrorCode = "P2_SQL_CAPABILITY"
	CodeRender     ErrorCode = "P2_SQL_RENDER"
)

type Error struct {
	Code       ErrorCode
	ModelID    ir.ModelID
	FieldID    ir.FieldID
	OperatorID ir.OperatorID
	Detail     string
	Cause      error
}

func (err *Error) Error() string {
	return fmt.Sprintf("%s: model=%x field=%x operator=%d: %s", err.Code, err.ModelID, err.FieldID, err.OperatorID, err.Detail)
}

func (err *Error) Unwrap() error { return err.Cause }

// Fragment is an immutable predicate and its positional arguments. SQL is
// produced only by the shared walker and a closed provider dialect.
type Fragment struct {
	text                  string
	args                  []any
	policyRelationAliases []PolicyRelationAliasFact
}

func (fragment Fragment) SQL() string { return fragment.text }
func (fragment Fragment) Args() []any { return cloneArgs(fragment.args) }

// PolicyRelationAliasFact is one renderer-owned policy traversal identity.
// It deliberately retains no SQL, predicate, bind, actor, table, column, or
// provider object name. The opaque alias can only be compared with a provider
// plan alias; it cannot be extracted and reused as an identifier.
type PolicyRelationAliasFact struct {
	alias      rendererAlias
	modelID    ir.ModelID
	relationID ir.RelationID
}

type rendererAlias struct{ value string }

// PolicyAliasAllocator is a single-render ownership token. The ordinary SQL
// renderer shares one allocator across every independently compiled policy
// fragment in a statement so aliases remain globally unique even across
// nested scopes. It exposes no counter or allocated identifier.
type PolicyAliasAllocator struct {
	next uint64
}

func NewPolicyAliasAllocator() *PolicyAliasAllocator {
	return &PolicyAliasAllocator{next: 1}
}

func (allocator *PolicyAliasAllocator) allocate() string {
	if allocator == nil || allocator.next == 0 {
		return ""
	}
	alias := fmt.Sprintf("golem_p%d", allocator.next)
	allocator.next++
	return alias
}

func newPolicyRelationAliasFact(alias string, model ir.ModelID, relation ir.RelationID) PolicyRelationAliasFact {
	return PolicyRelationAliasFact{alias: rendererAlias{value: alias}, modelID: model, relationID: relation}
}

// Matches compares an untrusted provider alias with the exact renderer-owned
// token. Zero or incomplete facts never match.
func (fact PolicyRelationAliasFact) Matches(candidate string) bool {
	return fact.alias.value != "" && fact.modelID != (ir.ModelID{}) && fact.relationID != (ir.RelationID{}) && candidate == fact.alias.value
}

func (fact PolicyRelationAliasFact) ModelID() ir.ModelID       { return fact.modelID }
func (fact PolicyRelationAliasFact) RelationID() ir.RelationID { return fact.relationID }

// PolicyRelationAliases returns allocation-ordered, caller-owned facts for
// aliases allocated by this policy fragment. The outer renderer owns and
// registers the root alias separately.
func (fragment Fragment) PolicyRelationAliases() []PolicyRelationAliasFact {
	return append([]PolicyRelationAliasFact(nil), fragment.policyRelationAliases...)
}

func cloneArgs(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case []byte:
			result[index] = append([]byte(nil), typed...)
		case []string:
			result[index] = append([]string(nil), typed...)
		default:
			result[index] = value
		}
	}
	return result
}

func validArgument(value any) bool {
	switch value.(type) {
	case bool, int16, int32, int64, float32, float64, string, []byte, time.Time:
		return true
	default:
		return false
	}
}

type Model struct {
	ID        ir.ModelID
	Namespace physical.PhysicalName
	Table     physical.PhysicalName
}

type Field struct {
	Model    ir.ModelID
	ID       ir.FieldID
	Column   physical.PhysicalName
	Type     ir.TypeRef
	Nullable bool
}

type Correlation struct {
	Parent ir.FieldID
	Child  ir.FieldID
}

type Relation struct {
	Model       ir.ModelID
	Field       ir.FieldID
	ID          ir.RelationID
	Target      ir.ModelID
	Cardinality ir.RelationCardinality
	Pairs       []Correlation
}

// Resolver publishes only descriptor-derived identities and physical names.
// Implementations must reject wrong-owner field identities.
type Resolver interface {
	Providers() ir.ProviderSet
	SchemaFingerprint() [32]byte
	Model(provider ir.Provider, model ir.ModelID) (Model, bool)
	Field(provider ir.Provider, model ir.ModelID, field ir.FieldID) (Field, bool)
	Relation(model ir.ModelID, field ir.FieldID, relation ir.RelationID) (Relation, bool)
	EnumWire(enum ir.EnumID, value ir.EnumValueID) (string, bool)
	Capability(provider ir.Provider, capability ir.Capability) bool
}

// CapabilityProof is an immutable snapshot of capabilities measured for the
// active provider instance. It is separate from static schema declarations.
type CapabilityProof struct {
	provider     ir.Provider
	fingerprint  [32]byte
	capabilities map[ir.Capability]struct{}
}

func NewCapabilityProof(provider ir.Provider, fingerprint [32]byte, capabilities ...ir.Capability) (CapabilityProof, error) {
	if (provider != ir.ProviderSQLite && provider != ir.ProviderPostgreSQL) || fingerprint == ([32]byte{}) {
		return CapabilityProof{}, fmt.Errorf("policy SQL capability proof: provider and schema fingerprint are required")
	}
	proof := CapabilityProof{provider: provider, fingerprint: fingerprint, capabilities: make(map[ir.Capability]struct{}, len(capabilities))}
	for _, capability := range capabilities {
		if capability < ir.CapabilityBinaryText || capability > ir.CapabilityRelationCorrelation {
			return CapabilityProof{}, fmt.Errorf("policy SQL capability proof: unknown capability %d", capability)
		}
		proof.capabilities[capability] = struct{}{}
	}
	return proof, nil
}

func (proof CapabilityProof) Provider() ir.Provider       { return proof.provider }
func (proof CapabilityProof) SchemaFingerprint() [32]byte { return proof.fingerprint }
func (proof CapabilityProof) Has(capability ir.Capability) bool {
	_, ok := proof.capabilities[capability]
	return ok
}

type Column struct {
	Alias physical.PhysicalName
	Name  physical.PhysicalName
}

type ScalarLeaf struct {
	Column   Column
	Field    Field
	Operator ir.OperatorID
	Mode     ir.ComparisonMode
	Operand  ir.Operand
}

type ListLeaf struct {
	Column   Column
	Field    Field
	Operator ir.OperatorID
	Operand  ir.Operand
}

type JSONLeaf struct {
	Column   Column
	Field    Field
	Operator ir.OperatorID
	Mode     ir.ComparisonMode
	Path     ir.JSONPath
	Operand  ir.Operand
}

// Dialect is deliberately high-level: it cannot alter logical traversal or
// relation quantifier semantics. A dialect renders only closed leaf families,
// quotes descriptor names, and encodes typed values.
type Dialect interface {
	Provider() ir.Provider
	Quote(physical.PhysicalName) string
	Table(Model) string
	Placeholder(position int) string
	Encode(BoundValue) (any, error)
	Supports(ir.OperatorID) bool
	RenderScalar(ScalarLeaf, *Binder) (string, error)
	RenderList(ListLeaf, *Binder) (string, error)
	RenderJSON(JSONLeaf, *Binder) (string, error)
}

// BoundValue carries a typed immutable operand plus any descriptor-resolved
// enum wire values needed by physical storage. Dialects never guess persisted
// enum labels from stable identity bytes.
type BoundValue struct {
	Value     ir.Value
	Type      ir.TypeRef
	EnumWires []string
}

// Binder is the only path from a dialect leaf to statement arguments.
type Binder struct {
	dialect  Dialect
	resolver Resolver
	args     []any
}

func (binder *Binder) Value(value ir.Value, typ ir.TypeRef) (string, error) {
	bound := BoundValue{Value: value, Type: typ}
	if typ.Kind() == ir.ValueEnum {
		enum, member, ok := value.Enum()
		if !ok {
			return "", fmt.Errorf("policy SQL bind: enum type received a non-enum value")
		}
		wire, ok := binder.resolver.EnumWire(enum, member)
		if !ok {
			return "", fmt.Errorf("policy SQL bind: enum value has no descriptor wire label")
		}
		bound.EnumWires = []string{wire}
	} else if typ.Kind() == ir.ValueScalarList {
		element, _ := typ.Element()
		if element.Kind() == ir.ValueEnum {
			values, ok := value.List()
			if !ok {
				return "", fmt.Errorf("policy SQL bind: scalar-list type received a non-list value")
			}
			bound.EnumWires = make([]string, len(values))
			for index, item := range values {
				enum, member, valid := item.Enum()
				if !valid {
					return "", fmt.Errorf("policy SQL bind: enum list contains a non-enum value")
				}
				wire, found := binder.resolver.EnumWire(enum, member)
				if !found {
					return "", fmt.Errorf("policy SQL bind: enum list value has no descriptor wire label")
				}
				bound.EnumWires[index] = wire
			}
		}
	}
	encoded, err := binder.dialect.Encode(bound)
	if err != nil {
		return "", err
	}
	if !validArgument(encoded) {
		return "", fmt.Errorf("policy SQL bind: dialect returned unsupported argument type %T", encoded)
	}
	binder.args = append(binder.args, encoded)
	return binder.dialect.Placeholder(len(binder.args)), nil
}

func (binder *Binder) Text(value string) string {
	binder.args = append(binder.args, value)
	return binder.dialect.Placeholder(len(binder.args))
}

func (binder *Binder) Integer(value int64) string {
	binder.args = append(binder.args, value)
	return binder.dialect.Placeholder(len(binder.args))
}

type Request struct {
	Condition        ir.Condition
	Provider         ir.Provider
	Resolver         Resolver
	Dialect          Dialect
	Capabilities     CapabilityProof
	BoundFingerprint [32]byte
	RootAlias        physical.PhysicalName
}
