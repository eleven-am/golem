package schemaexpr

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestEquivalentFormsHaveOneCanonicalIdentity(t *testing.T) {
	engine := testEngine()
	a := mustField(t, engine, "a")
	b := mustField(t, engine, "b")
	c := mustField(t, engine, "c")
	ab := mustExpression(t, engine, Add, a, b)
	ba := mustExpression(t, engine, Add, b, a)
	if ab.CanonicalIdentity != ba.CanonicalIdentity || !reflect.DeepEqual(ab.Operands, ba.Operands) {
		t.Fatalf("commutative add did not canonicalize:\n%#v\n%#v", ab, ba)
	}
	nested := mustExpression(t, engine, Add, mustExpression(t, engine, Add, c, a), b)
	flat := mustExpression(t, engine, Add, b, c, a)
	if nested.CanonicalIdentity != flat.CanonicalIdentity || len(nested.Operands) != 3 {
		t.Fatalf("associative add did not flatten: %#v / %#v", nested, flat)
	}
	eq := mustPredicate(t, engine, Equal, a, b)
	null := mustPredicate(t, engine, IsNull, b)
	left := mustAnd(t, engine, eq, null)
	right := mustAnd(t, engine, null, eq, eq)
	if left.CanonicalIdentity != right.CanonicalIdentity || len(right.Children) != 2 {
		t.Fatalf("logical canonicalization differs: %#v / %#v", left, right)
	}
	leftBytes, diagnostics := CanonicalPredicate(engine, left)
	assertNoDiagnostics(t, diagnostics)
	rightBytes, diagnostics := CanonicalPredicate(engine, right)
	assertNoDiagnostics(t, diagnostics)
	if string(leftBytes) != string(rightBytes) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", leftBytes, rightBytes)
	}
}

func TestOperandTypesCastsNullabilityAndReferences(t *testing.T) {
	engine := testEngine()
	a := mustField(t, engine, "a")
	b := mustField(t, engine, "b")
	s := mustField(t, engine, "s")
	if _, diagnostics := engine.Expression(Add, a, s); !hasCode(diagnostics, "P1_SCHEMA_OPERAND_TYPE") {
		t.Fatalf("mixed arithmetic diagnostics = %#v", diagnostics)
	}
	if _, diagnostics := engine.Expression("unknown", a); !hasCode(diagnostics, "P1_SCHEMA_SYMBOL_UNKNOWN") {
		t.Fatalf("unknown symbol diagnostics = %#v", diagnostics)
	}
	add := mustExpression(t, engine, Add, a, b, a)
	if !add.Nullable || !reflect.DeepEqual(add.ReferencedFields, []ir.FieldID{"a", "b"}) {
		t.Fatalf("add metadata = %#v", add)
	}
	coalesce := mustExpression(t, engine, Coalesce, b, a)
	if coalesce.Nullable {
		t.Fatalf("coalesce with required fallback is nullable: %#v", coalesce)
	}
	eq := mustPredicate(t, engine, Equal, a, b)
	if !eq.Nullable {
		t.Fatalf("comparison should preserve SQL unknown possibility: %#v", eq)
	}
	isNull := mustPredicate(t, engine, IsNull, b)
	if isNull.Nullable {
		t.Fatalf("IS NULL is never nullable: %#v", isNull)
	}

	castID := "test.cast.int64-string.v1"
	diagnostic := engine.registry.Register(SymbolSpec{
		Ref:  ir.SchemaSymbolRef{Identity: castID, Kind: ir.SchemaSymbolCast, Name: "int64_to_string", Version: 1, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true},
		Role: RoleExpression, Inputs: []ir.LogicalTypeIR{{Kind: ir.TypeInt64}}, Output: ir.LogicalTypeIR{Kind: ir.TypeString}, NullRule: NullIfAny,
	})
	if diagnostic != nil {
		t.Fatal(diagnostic)
	}
	cast := mustExpression(t, engine, castID, a)
	if cast.Kind != ir.SchemaExprCast || cast.ResultType.Kind != ir.TypeString {
		t.Fatalf("cast = %#v", cast)
	}
	if _, diagnostics := engine.Expression(castID, s); !hasCode(diagnostics, "P1_SCHEMA_OPERAND_TYPE") {
		t.Fatalf("invalid cast diagnostics = %#v", diagnostics)
	}
}

func TestBuiltInCastRegistry(t *testing.T) {
	engine := New(ir.ModelDeclIR{ID: "model", Fields: []ir.FieldIR{
		{ID: "small", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt16}}},
		{ID: "wide", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
	}}, NewRegistry())
	small := mustField(t, engine, "small")
	cast := mustExpression(t, engine, CastInt16ToInt64, small)
	if cast.Kind != ir.SchemaExprCast || cast.ResultType.Kind != ir.TypeInt64 || cast.Symbol == nil || cast.Symbol.Identity != CastInt16ToInt64 {
		t.Fatalf("built-in cast = %#v", cast)
	}
	wide := mustField(t, engine, "wide")
	if _, diagnostics := engine.Expression(CastInt16ToInt64, wide); !hasCode(diagnostics, "P1_SCHEMA_OPERAND_TYPE") {
		t.Fatalf("invalid built-in cast diagnostics = %#v", diagnostics)
	}
}

func TestProviderScopeVolatilityAndUseValidation(t *testing.T) {
	registry := NewRegistry()
	registerUnary(t, registry, "test.pg", ir.ProviderScopePostgreSQL, ir.SchemaVolatilityImmutable, true)
	registerUnary(t, registry, "test.sqlite", ir.ProviderScopeSQLite, ir.SchemaVolatilityImmutable, true)
	registerUnary(t, registry, "test.stable", ir.ProviderScopePortable, ir.SchemaVolatilityStable, true)
	registerUnary(t, registry, "test.nondeterministic", ir.ProviderScopePortable, ir.SchemaVolatilityImmutable, false)
	engine := New(testModel(), registry)
	a := mustField(t, engine, "a")
	pg := mustExpression(t, engine, "test.pg", a)
	sqlite := mustExpression(t, engine, "test.sqlite", a)
	if pg.Provider != ir.ProviderScopePostgreSQL {
		t.Fatalf("provider scope = %q", pg.Provider)
	}
	if _, diagnostics := engine.Expression(Add, pg, sqlite); !hasCode(diagnostics, "P1_SCHEMA_PROVIDER_MISMATCH") {
		t.Fatalf("provider mismatch diagnostics = %#v", diagnostics)
	}
	stable := mustExpression(t, engine, "test.stable", a)
	if _, diagnostics := engine.ValidateIndexExpression(stable, ir.ProviderScopePortable); !hasCode(diagnostics, "P1_SCHEMA_NOT_IMMUTABLE") {
		t.Fatalf("stable index diagnostics = %#v", diagnostics)
	}
	nondeterministic := mustExpression(t, engine, "test.nondeterministic", a)
	predicate := mustPredicate(t, engine, Equal, nondeterministic, a)
	if _, diagnostics := engine.ValidatePredicateUse(predicate, UsePartialIndex, ir.ProviderScopePortable); !hasCode(diagnostics, "P1_SCHEMA_NOT_IMMUTABLE") {
		t.Fatalf("nondeterministic partial predicate diagnostics = %#v", diagnostics)
	}
	pgPredicate := mustPredicate(t, engine, Equal, pg, a)
	if _, diagnostics := engine.ValidatePredicateUse(pgPredicate, UseCheck, ir.ProviderScopeSQLite); !hasCode(diagnostics, "P1_SCHEMA_PROVIDER_MISMATCH") {
		t.Fatalf("declared provider mismatch diagnostics = %#v", diagnostics)
	}
}

func TestOwnRowValidationMetadataAndCheckSemantics(t *testing.T) {
	engine := testEngine()
	if _, diagnostics := engine.Field("other-model-field"); !hasCode(diagnostics, "P1_SCHEMA_FIELD_OWN_ROW") {
		t.Fatalf("cross-model field diagnostics = %#v", diagnostics)
	}
	a := mustField(t, engine, "a")
	b := mustField(t, engine, "b")
	predicate := mustPredicate(t, engine, Greater, a, b)
	analysis, diagnostics := engine.ValidatePredicateUse(predicate, UseCheck, ir.ProviderScopePortable)
	assertNoDiagnostics(t, diagnostics)
	if analysis.Check == nil || !analysis.Check.ThreeValued || !analysis.Check.UnknownPassesCheck || !analysis.Predicate.Nullable {
		t.Fatalf("check semantics = %#v", analysis)
	}
	tampered := predicate
	tampered.ReferencedFields = []ir.FieldID{"wrong"}
	if _, diagnostics := engine.ValidatePredicate(tampered); !hasCode(diagnostics, "P1_SCHEMA_PREDICATE_METADATA") {
		t.Fatalf("tampered metadata diagnostics = %#v", diagnostics)
	}
	forbidden := ir.SchemaExprIR{Kind: ir.SchemaExprKind("subquery")}
	if _, diagnostics := engine.ValidateExpression(forbidden); !hasCode(diagnostics, "P1_SCHEMA_EXPR_KIND") {
		t.Fatalf("forbidden expression diagnostics = %#v", diagnostics)
	}
}

func TestGeneratedDependencyPlanAndCyclesAreDeterministic(t *testing.T) {
	engine := testEngine()
	a := mustField(t, engine, "a")
	g1 := mustField(t, engine, "g1")
	g2 := mustField(t, engine, "g2")
	g1Expr := mustExpression(t, engine, Add, a, a)
	g2Expr := mustExpression(t, engine, Add, g1, a)
	left, diagnostics := engine.PlanGenerated([]GeneratedInput{{FieldID: "g2", Expr: g2Expr, Scope: ir.ProviderScopePortable}, {FieldID: "g1", Expr: g1Expr, Scope: ir.ProviderScopePortable}})
	assertNoDiagnostics(t, diagnostics)
	right, diagnostics := engine.PlanGenerated([]GeneratedInput{{FieldID: "g1", Expr: g1Expr, Scope: ir.ProviderScopePortable}, {FieldID: "g2", Expr: g2Expr, Scope: ir.ProviderScopePortable}})
	assertNoDiagnostics(t, diagnostics)
	if !reflect.DeepEqual(left, right) || len(left.Nodes) != 2 || left.Nodes[0].FieldID != "g1" || !reflect.DeepEqual(left.Nodes[1].DependsOn, []ir.FieldID{"g1"}) {
		t.Fatalf("generated plan is not deterministic/topological: %#v / %#v", left, right)
	}
	cycleG1 := mustExpression(t, engine, Add, g2, a)
	cycleG2 := mustExpression(t, engine, Add, g1, a)
	_, diagnostics = engine.PlanGenerated([]GeneratedInput{{FieldID: "g1", Expr: cycleG1, Scope: ir.ProviderScopePortable}, {FieldID: "g2", Expr: cycleG2, Scope: ir.ProviderScopePortable}})
	if !hasCode(diagnostics, "P1_GENERATED_CYCLE") {
		t.Fatalf("cycle diagnostics = %#v", diagnostics)
	}
}

func testEngine() *Engine { return New(testModel(), NewRegistry()) }

func testModel() ir.ModelDeclIR {
	field := func(id ir.FieldID, kind ir.LogicalTypeKind, nullable bool) ir.FieldIR {
		return ir.FieldIR{ID: id, GoName: string(id), Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: kind}, Nullable: nullable}}
	}
	return ir.ModelDeclIR{ID: "model", Fields: []ir.FieldIR{field("a", ir.TypeInt64, false), field("b", ir.TypeInt64, true), field("c", ir.TypeInt64, false), field("s", ir.TypeString, false), field("g1", ir.TypeInt64, false), field("g2", ir.TypeInt64, false)}}
}

func registerUnary(t *testing.T, registry *Registry, identity string, scope ir.ProviderScope, volatility ir.SchemaVolatility, deterministic bool) {
	t.Helper()
	diagnostic := registry.Register(SymbolSpec{Ref: ir.SchemaSymbolRef{Identity: identity, Kind: ir.SchemaSymbolFunction, Name: identity, Version: 1, Provider: scope, Volatility: volatility, Deterministic: deterministic}, Role: RoleExpression, Inputs: []ir.LogicalTypeIR{{Kind: ir.TypeInt64}}, Output: ir.LogicalTypeIR{Kind: ir.TypeInt64}, NullRule: NullIfAny})
	if diagnostic != nil {
		t.Fatal(diagnostic)
	}
}

func mustField(t *testing.T, engine *Engine, id ir.FieldID) ir.SchemaExprIR {
	t.Helper()
	value, diagnostics := engine.Field(id)
	assertNoDiagnostics(t, diagnostics)
	return value
}

func mustExpression(t *testing.T, engine *Engine, identity string, operands ...ir.SchemaExprIR) ir.SchemaExprIR {
	t.Helper()
	value, diagnostics := engine.Expression(identity, operands...)
	assertNoDiagnostics(t, diagnostics)
	return value
}

func mustPredicate(t *testing.T, engine *Engine, identity string, operands ...ir.SchemaExprIR) ir.SchemaPredicateIR {
	t.Helper()
	value, diagnostics := engine.Predicate(identity, operands...)
	assertNoDiagnostics(t, diagnostics)
	return value
}

func mustAnd(t *testing.T, engine *Engine, children ...ir.SchemaPredicateIR) ir.SchemaPredicateIR {
	t.Helper()
	value, diagnostics := engine.And(children...)
	assertNoDiagnostics(t, diagnostics)
	return value
}

func assertNoDiagnostics(t *testing.T, diagnostics []ir.Diagnostic) {
	t.Helper()
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

func hasCode(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
