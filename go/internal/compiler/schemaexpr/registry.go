// Package schemaexpr constructs and validates the closed P1 database-schema
// expression language. It is intentionally separate from authorization
// predicates.
package schemaexpr

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	Add               = "golem.schema.operator.add.v1"
	Subtract          = "golem.schema.operator.subtract.v1"
	Multiply          = "golem.schema.operator.multiply.v1"
	Divide            = "golem.schema.operator.divide.v1"
	Remainder         = "golem.schema.operator.remainder.v1"
	Concat            = "golem.schema.operator.concat.v1"
	Lower             = "golem.schema.function.lower.v1"
	Upper             = "golem.schema.function.upper.v1"
	Length            = "golem.schema.function.length.v1"
	Coalesce          = "golem.schema.function.coalesce.v1"
	Equal             = "golem.schema.predicate.equal.v1"
	NotEqual          = "golem.schema.predicate.not-equal.v1"
	Less              = "golem.schema.predicate.less.v1"
	LessEqual         = "golem.schema.predicate.less-equal.v1"
	Greater           = "golem.schema.predicate.greater.v1"
	GreaterEq         = "golem.schema.predicate.greater-equal.v1"
	IsNull            = "golem.schema.predicate.is-null.v1"
	IsNotNull         = "golem.schema.predicate.is-not-null.v1"
	In                = "golem.schema.predicate.in.v1"
	CastInt16ToInt32  = "golem.schema.cast.int16-to-int32.v1"
	CastInt16ToInt64  = "golem.schema.cast.int16-to-int64.v1"
	CastInt32ToInt64  = "golem.schema.cast.int32-to-int64.v1"
	CastInt64ToString = "golem.schema.cast.int64-to-string.v1"
)

type SymbolRole string

const (
	RoleExpression SymbolRole = "expression"
	RolePredicate  SymbolRole = "predicate"
)

type NullRule string

const (
	NullIfAny NullRule = "ifAny"
	NullIfAll NullRule = "ifAll"
	NeverNull NullRule = "never"
)

// SymbolSpec is the registration boundary for typed provider functions and
// casts. Built-in polymorphic symbols use the same registry internally.
type SymbolSpec struct {
	Ref          ir.SchemaSymbolRef
	Role         SymbolRole
	Inputs       []ir.LogicalTypeIR
	Output       ir.LogicalTypeIR
	Variadic     bool
	MinimumArity int
	NullRule     NullRule

	rule        semanticRule
	commutative bool
	associative bool
}

type semanticRule uint8

const (
	ruleFixed semanticRule = iota
	ruleNumericSame
	ruleStringSame
	ruleLength
	ruleCoalesce
	ruleComparable
	ruleOrdered
	ruleNullTest
	ruleIn
)

type Registry struct{ symbols map[string]SymbolSpec }

func NewRegistry() *Registry {
	registry := &Registry{symbols: map[string]SymbolSpec{}}
	immutable := func(identity, name string, kind ir.SchemaSymbolKind) ir.SchemaSymbolRef {
		return ir.SchemaSymbolRef{Identity: identity, Kind: kind, Name: name, Version: 1, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true}
	}
	registry.must(SymbolSpec{Ref: immutable(Add, "+", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, Variadic: true, NullRule: NullIfAny, rule: ruleNumericSame, commutative: true, associative: true})
	registry.must(SymbolSpec{Ref: immutable(Subtract, "-", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, NullRule: NullIfAny, rule: ruleNumericSame})
	registry.must(SymbolSpec{Ref: immutable(Multiply, "*", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, Variadic: true, NullRule: NullIfAny, rule: ruleNumericSame, commutative: true, associative: true})
	registry.must(SymbolSpec{Ref: immutable(Divide, "/", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, NullRule: NullIfAny, rule: ruleNumericSame})
	registry.must(SymbolSpec{Ref: immutable(Remainder, "%", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, NullRule: NullIfAny, rule: ruleNumericSame})
	registry.must(SymbolSpec{Ref: immutable(Concat, "concat", ir.SchemaSymbolOperator), Role: RoleExpression, MinimumArity: 2, Variadic: true, NullRule: NullIfAny, rule: ruleStringSame, associative: true})
	registry.must(SymbolSpec{Ref: immutable(Lower, "lower", ir.SchemaSymbolFunction), Role: RoleExpression, MinimumArity: 1, NullRule: NullIfAny, rule: ruleStringSame})
	registry.must(SymbolSpec{Ref: immutable(Upper, "upper", ir.SchemaSymbolFunction), Role: RoleExpression, MinimumArity: 1, NullRule: NullIfAny, rule: ruleStringSame})
	registry.must(SymbolSpec{Ref: immutable(Length, "length", ir.SchemaSymbolFunction), Role: RoleExpression, MinimumArity: 1, NullRule: NullIfAny, rule: ruleLength})
	registry.must(SymbolSpec{Ref: immutable(Coalesce, "coalesce", ir.SchemaSymbolFunction), Role: RoleExpression, MinimumArity: 2, Variadic: true, NullRule: NullIfAll, rule: ruleCoalesce})
	registry.must(SymbolSpec{Ref: immutable(Equal, "eq", ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 2, NullRule: NullIfAny, rule: ruleComparable, commutative: true})
	registry.must(SymbolSpec{Ref: immutable(NotEqual, "ne", ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 2, NullRule: NullIfAny, rule: ruleComparable, commutative: true})
	for _, item := range []struct{ id, name string }{{Less, "lt"}, {LessEqual, "lte"}, {Greater, "gt"}, {GreaterEq, "gte"}} {
		registry.must(SymbolSpec{Ref: immutable(item.id, item.name, ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 2, NullRule: NullIfAny, rule: ruleOrdered})
	}
	registry.must(SymbolSpec{Ref: immutable(IsNull, "is-null", ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 1, NullRule: NeverNull, rule: ruleNullTest})
	registry.must(SymbolSpec{Ref: immutable(IsNotNull, "is-not-null", ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 1, NullRule: NeverNull, rule: ruleNullTest})
	registry.must(SymbolSpec{Ref: immutable(In, "in", ir.SchemaSymbolOperator), Role: RolePredicate, MinimumArity: 2, Variadic: true, NullRule: NullIfAny, rule: ruleIn})
	for _, cast := range []struct {
		identity string
		name     string
		input    ir.LogicalTypeKind
		output   ir.LogicalTypeKind
	}{
		{CastInt16ToInt32, "int16-to-int32", ir.TypeInt16, ir.TypeInt32},
		{CastInt16ToInt64, "int16-to-int64", ir.TypeInt16, ir.TypeInt64},
		{CastInt32ToInt64, "int32-to-int64", ir.TypeInt32, ir.TypeInt64},
		{CastInt64ToString, "int64-to-string", ir.TypeInt64, ir.TypeString},
	} {
		registry.must(SymbolSpec{
			Ref: immutable(cast.identity, cast.name, ir.SchemaSymbolCast), Role: RoleExpression,
			Inputs: []ir.LogicalTypeIR{{Kind: cast.input}}, Output: ir.LogicalTypeIR{Kind: cast.output},
			MinimumArity: 1, NullRule: NullIfAny, rule: ruleFixed,
		})
	}
	return registry
}

// Register adds one fixed-signature typed function or cast. Operators and
// predicate vocabulary are closed and cannot be extended here.
func (registry *Registry) Register(spec SymbolSpec) *ir.Diagnostic {
	if registry == nil {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_REGISTRY", "schema symbol registry is nil", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.Ref.Identity == "" || spec.Ref.Name == "" || spec.Ref.Version == 0 {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_INVALID", "registered schema symbol requires identity, name, and version", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.Ref.Kind != ir.SchemaSymbolFunction && spec.Ref.Kind != ir.SchemaSymbolCast {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_KIND", "only typed functions and casts may extend the schema registry", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.Role != RoleExpression || len(spec.Inputs) == 0 || spec.Output.Kind == "" {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_SIGNATURE", "registered function/cast requires expression role and fixed input/output types", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.Variadic {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_SIGNATURE", "registered provider functions and casts use fixed typed signatures", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.NullRule != NullIfAny && spec.NullRule != NullIfAll && spec.NullRule != NeverNull {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_NULLABILITY", "registered schema symbol requires an explicit nullability rule", ir.SourceSpan{})
		return &diagnostic
	}
	if spec.Ref.Kind == ir.SchemaSymbolCast && len(spec.Inputs) != 1 {
		diagnostic := ir.NewError("P1_SCHEMA_CAST_ARITY", "registered cast requires exactly one input type", ir.SourceSpan{})
		return &diagnostic
	}
	if !validScope(spec.Ref.Provider) || !validVolatility(spec.Ref.Volatility) {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_METADATA", "registered symbol has invalid provider scope or volatility", ir.SourceSpan{})
		return &diagnostic
	}
	if _, exists := registry.symbols[spec.Ref.Identity]; exists {
		diagnostic := ir.NewError("P1_SCHEMA_SYMBOL_DUPLICATE", fmt.Sprintf("schema symbol %q is already registered", spec.Ref.Identity), ir.SourceSpan{})
		return &diagnostic
	}
	spec.rule = ruleFixed
	spec.MinimumArity = len(spec.Inputs)
	registry.symbols[spec.Ref.Identity] = spec
	return nil
}

func (registry *Registry) Symbols() []ir.SchemaSymbolRef {
	identities := make([]string, 0, len(registry.symbols))
	for identity := range registry.symbols {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	result := make([]ir.SchemaSymbolRef, len(identities))
	for index, identity := range identities {
		result[index] = registry.symbols[identity].Ref
	}
	return result
}

func (registry *Registry) must(spec SymbolSpec) { registry.symbols[spec.Ref.Identity] = spec }

func validScope(scope ir.ProviderScope) bool {
	return scope == ir.ProviderScopePortable || scope == ir.ProviderScopeSQLite || scope == ir.ProviderScopePostgreSQL
}

func validVolatility(volatility ir.SchemaVolatility) bool {
	return volatility == ir.SchemaVolatilityImmutable || volatility == ir.SchemaVolatilityStable || volatility == ir.SchemaVolatilityVolatile
}
