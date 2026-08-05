package schemaexpr

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type PredicateUse string

const (
	UseCheck        PredicateUse = "check"
	UsePartialIndex PredicateUse = "partialIndex"
)

// CheckSemantics explicitly preserves SQL CHECK three-valued behavior. It is
// not authorization/policy truth semantics.
type CheckSemantics struct {
	ThreeValued        bool
	UnknownPassesCheck bool
}

type PredicateAnalysis struct {
	Predicate ir.SchemaPredicateIR
	Provider  ir.ProviderScope
	Check     *CheckSemantics
}

func (engine *Engine) ValidateIndexExpression(input ir.SchemaExprIR, declaredScope ir.ProviderScope) (ir.SchemaExprIR, []ir.Diagnostic) {
	expression, diagnostics := engine.ValidateExpression(input)
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, requireImmutable(expression.Volatility, expression.Deterministic, "index expression")...)
		_, scopeDiagnostics := intersectScope(declaredScope, expression.Provider)
		diagnostics = append(diagnostics, scopeDiagnostics...)
	}
	ir.SortDiagnostics(diagnostics)
	return expression, diagnostics
}

func (engine *Engine) ValidatePredicateUse(input ir.SchemaPredicateIR, use PredicateUse, declaredScope ir.ProviderScope) (PredicateAnalysis, []ir.Diagnostic) {
	predicate, diagnostics := engine.ValidatePredicate(input)
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, requireImmutable(predicate.Volatility, predicate.Deterministic, string(use))...)
		_, scopeDiagnostics := intersectScope(declaredScope, predicate.Provider)
		diagnostics = append(diagnostics, scopeDiagnostics...)
	}
	analysis := PredicateAnalysis{Predicate: predicate, Provider: predicate.Provider}
	switch use {
	case UseCheck:
		analysis.Check = &CheckSemantics{ThreeValued: true, UnknownPassesCheck: true}
	case UsePartialIndex:
	default:
		diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_USE", fmt.Sprintf("unknown predicate use %q", use)))
	}
	ir.SortDiagnostics(diagnostics)
	return analysis, diagnostics
}

type GeneratedInput struct {
	FieldID ir.FieldID
	Expr    ir.SchemaExprIR
	Scope   ir.ProviderScope
	Span    ir.SourceSpan
}

type GeneratedNode struct {
	FieldID   ir.FieldID
	Expr      ir.SchemaExprIR
	Provider  ir.ProviderScope
	DependsOn []ir.FieldID
}

type GeneratedPlan struct{ Nodes []GeneratedNode }

func (engine *Engine) ValidateGenerated(input GeneratedInput) (GeneratedNode, []ir.Diagnostic) {
	var diagnostics []ir.Diagnostic
	target, exists := engine.fields[input.FieldID]
	if !exists || target.Scalar == nil {
		diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_TARGET", fmt.Sprintf("generated target %s is not a scalar field of model %s", input.FieldID, engine.model.ID), input.Span))
		return GeneratedNode{}, diagnostics
	}
	if target.Scalar.Default != nil || target.Scalar.Updated || target.Scalar.Generation != nil {
		diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_TARGET_STATE", "generated target cannot also have a default, updated behavior, or another generation expression", input.Span))
	}
	if engine.model.PrimaryKey != nil {
		for _, fieldID := range engine.model.PrimaryKey.Fields {
			if fieldID == input.FieldID {
				diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_PRIMARY_KEY", "portable generated columns cannot be primary-key components", input.Span))
				break
			}
		}
	}
	expression, expressionDiagnostics := engine.ValidateExpression(input.Expr)
	diagnostics = append(diagnostics, expressionDiagnostics...)
	if !hasErrors(expressionDiagnostics) {
		if !reflectLogicalType(target.Scalar.Type, expression.ResultType) {
			diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_RESULT_TYPE", fmt.Sprintf("generated expression type %s does not match target type %s", expression.ResultType.Kind, target.Scalar.Type.Kind), input.Span))
		}
		if expression.Nullable && !target.Scalar.Nullable {
			diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_NULLABILITY", "nullable generated expression requires a nullable target field", input.Span))
		}
		diagnostics = append(diagnostics, requireImmutable(expression.Volatility, expression.Deterministic, "generated column")...)
		_, scopeDiagnostics := intersectScope(input.Scope, expression.Provider)
		diagnostics = append(diagnostics, scopeDiagnostics...)
	}
	ir.SortDiagnostics(diagnostics)
	return GeneratedNode{FieldID: input.FieldID, Expr: expression, Provider: expression.Provider}, diagnostics
}

func (engine *Engine) PlanGenerated(inputs []GeneratedInput) (GeneratedPlan, []ir.Diagnostic) {
	orderedInputs := append([]GeneratedInput(nil), inputs...)
	sort.Slice(orderedInputs, func(i, j int) bool { return orderedInputs[i].FieldID < orderedInputs[j].FieldID })
	nodes := make(map[ir.FieldID]GeneratedNode, len(inputs))
	spans := make(map[ir.FieldID]ir.SourceSpan, len(inputs))
	var diagnostics []ir.Diagnostic
	for _, input := range orderedInputs {
		if _, duplicate := nodes[input.FieldID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_DUPLICATE", fmt.Sprintf("field %s has multiple generated declarations", input.FieldID), input.Span))
			continue
		}
		node, nodeDiagnostics := engine.ValidateGenerated(input)
		diagnostics = append(diagnostics, nodeDiagnostics...)
		nodes[input.FieldID], spans[input.FieldID] = node, input.Span
	}
	generated := make(map[ir.FieldID]bool, len(nodes))
	for fieldID := range nodes {
		generated[fieldID] = true
	}
	indegree := make(map[ir.FieldID]int, len(nodes))
	dependents := make(map[ir.FieldID][]ir.FieldID, len(nodes))
	for fieldID, node := range nodes {
		for _, dependency := range node.Expr.ReferencedFields {
			if !generated[dependency] {
				continue
			}
			node.DependsOn = append(node.DependsOn, dependency)
			indegree[fieldID]++
			dependents[dependency] = append(dependents[dependency], fieldID)
		}
		node.DependsOn = canonicalFields(node.DependsOn)
		nodes[fieldID] = node
	}
	for fieldID := range dependents {
		sort.Slice(dependents[fieldID], func(i, j int) bool { return dependents[fieldID][i] < dependents[fieldID][j] })
	}
	ready := make([]ir.FieldID, 0)
	for fieldID := range nodes {
		if indegree[fieldID] == 0 {
			ready = append(ready, fieldID)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	plan := GeneratedPlan{}
	for len(ready) != 0 {
		fieldID := ready[0]
		ready = ready[1:]
		plan.Nodes = append(plan.Nodes, nodes[fieldID])
		for _, dependent := range dependents[fieldID] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
			}
		}
	}
	if len(plan.Nodes) != len(nodes) {
		cycle := make([]ir.FieldID, 0)
		for fieldID := range nodes {
			if indegree[fieldID] > 0 {
				cycle = append(cycle, fieldID)
			}
		}
		sort.Slice(cycle, func(i, j int) bool { return cycle[i] < cycle[j] })
		span := ir.SourceSpan{}
		if len(cycle) != 0 {
			span = spans[cycle[0]]
		}
		diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_CYCLE", fmt.Sprintf("generated columns contain a dependency cycle involving %v", cycle), span))
	}
	ir.SortDiagnostics(diagnostics)
	return plan, diagnostics
}

func requireImmutable(volatility ir.SchemaVolatility, deterministic bool, use string) []ir.Diagnostic {
	if volatility != ir.SchemaVolatilityImmutable || !deterministic {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_NOT_IMMUTABLE", fmt.Sprintf("%s requires deterministic immutable expressions", use))}
	}
	return nil
}

func reflectLogicalType(left, right ir.LogicalTypeIR) bool {
	return reflect.DeepEqual(left, right)
}
