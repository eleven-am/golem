package schemaexpr

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func (engine *Engine) ValidateExpression(input ir.SchemaExprIR) (ir.SchemaExprIR, []ir.Diagnostic) {
	var normalized ir.SchemaExprIR
	var diagnostics []ir.Diagnostic
	switch input.Kind {
	case ir.SchemaExprField:
		if input.Field == nil || input.Symbol != nil || input.Literal != nil || len(input.Operands) != 0 {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_EXPR_SHAPE", "field expression has invalid payload"))
			break
		}
		normalized, diagnostics = engine.Field(*input.Field)
	case ir.SchemaExprLiteral:
		if input.Literal == nil || input.Symbol != nil || input.Field != nil || len(input.Operands) != 0 {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_EXPR_SHAPE", "literal expression has invalid payload"))
			break
		}
		normalized, diagnostics = engine.Literal(input.ResultType, *input.Literal)
	case ir.SchemaExprOperator, ir.SchemaExprFunction, ir.SchemaExprCast:
		if input.Symbol == nil || input.Field != nil || input.Literal != nil {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_EXPR_SHAPE", "compound expression has invalid payload"))
			break
		}
		operands := make([]ir.SchemaExprIR, len(input.Operands))
		for index, operand := range input.Operands {
			var nested []ir.Diagnostic
			operands[index], nested = engine.ValidateExpression(operand)
			diagnostics = append(diagnostics, nested...)
		}
		if !hasErrors(diagnostics) {
			var buildDiagnostics []ir.Diagnostic
			normalized, buildDiagnostics = engine.Expression(input.Symbol.Identity, operands...)
			diagnostics = append(diagnostics, buildDiagnostics...)
			diagnostics = append(diagnostics, engine.validateSymbol(*input.Symbol, RoleExpression)...)
		}
	default:
		diagnostics = append(diagnostics, schemaError("P1_SCHEMA_EXPR_KIND", fmt.Sprintf("unknown or forbidden schema expression kind %q", input.Kind)))
	}
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, compareExpressionMetadata(input, normalized)...)
	}
	ir.SortDiagnostics(diagnostics)
	return normalized, diagnostics
}

func (engine *Engine) ValidatePredicate(input ir.SchemaPredicateIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	var normalized ir.SchemaPredicateIR
	var diagnostics []ir.Diagnostic
	switch input.Kind {
	case ir.SchemaPredicateConstant:
		if input.Constant == nil || input.Symbol != nil || len(input.ExpressionOperands) != 0 || len(input.Children) != 0 {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_SHAPE", "constant predicate has invalid payload"))
			break
		}
		normalized = engine.Constant(*input.Constant)
	case ir.SchemaPredicateOperator:
		if input.Symbol == nil || input.Constant != nil || len(input.Children) != 0 {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_SHAPE", "operator predicate has invalid payload"))
			break
		}
		operands := make([]ir.SchemaExprIR, len(input.ExpressionOperands))
		for index, operand := range input.ExpressionOperands {
			var nested []ir.Diagnostic
			operands[index], nested = engine.ValidateExpression(operand)
			diagnostics = append(diagnostics, nested...)
		}
		if !hasErrors(diagnostics) {
			var buildDiagnostics []ir.Diagnostic
			normalized, buildDiagnostics = engine.Predicate(input.Symbol.Identity, operands...)
			diagnostics = append(diagnostics, buildDiagnostics...)
			diagnostics = append(diagnostics, engine.validateSymbol(*input.Symbol, RolePredicate)...)
		}
	case ir.SchemaPredicateAnd, ir.SchemaPredicateOr, ir.SchemaPredicateNot:
		if input.Symbol != nil || input.Constant != nil || len(input.ExpressionOperands) != 0 {
			diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_SHAPE", "logical predicate has invalid payload"))
			break
		}
		children := make([]ir.SchemaPredicateIR, len(input.Children))
		for index, child := range input.Children {
			var nested []ir.Diagnostic
			children[index], nested = engine.ValidatePredicate(child)
			diagnostics = append(diagnostics, nested...)
		}
		if !hasErrors(diagnostics) {
			switch input.Kind {
			case ir.SchemaPredicateAnd:
				normalized, diagnostics = engine.And(children...)
			case ir.SchemaPredicateOr:
				normalized, diagnostics = engine.Or(children...)
			case ir.SchemaPredicateNot:
				if len(children) == 1 {
					normalized, diagnostics = engine.Not(children[0])
				} else {
					diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_ARITY", "not requires one child"))
				}
			}
		}
	default:
		diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_KIND", fmt.Sprintf("unknown or forbidden schema predicate kind %q", input.Kind)))
	}
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, comparePredicateMetadata(input, normalized)...)
	}
	ir.SortDiagnostics(diagnostics)
	return normalized, diagnostics
}

func (engine *Engine) validateSymbol(ref ir.SchemaSymbolRef, role SymbolRole) []ir.Diagnostic {
	spec, exists := engine.registry.symbols[ref.Identity]
	if !exists {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_SYMBOL_UNKNOWN", fmt.Sprintf("unknown schema symbol %q", ref.Identity))}
	}
	if spec.Role != role || !reflect.DeepEqual(spec.Ref, ref) {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_SYMBOL_MISMATCH", fmt.Sprintf("schema symbol metadata for %q does not match its registry entry", ref.Identity))}
	}
	return nil
}

func compareExpressionMetadata(input, normalized ir.SchemaExprIR) []ir.Diagnostic {
	if input.CanonicalIdentity != normalized.CanonicalIdentity || !reflect.DeepEqual(input.ResultType, normalized.ResultType) || input.Nullable != normalized.Nullable || input.Provider != normalized.Provider || input.Volatility != normalized.Volatility || input.Deterministic != normalized.Deterministic || !reflect.DeepEqual(input.ReferencedFields, normalized.ReferencedFields) {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_EXPR_METADATA", "expression metadata does not agree with its semantic tree")}
	}
	return nil
}

func comparePredicateMetadata(input, normalized ir.SchemaPredicateIR) []ir.Diagnostic {
	if input.CanonicalIdentity != normalized.CanonicalIdentity || !reflect.DeepEqual(input.ResultType, normalized.ResultType) || input.Nullable != normalized.Nullable || input.Provider != normalized.Provider || input.Volatility != normalized.Volatility || input.Deterministic != normalized.Deterministic || !reflect.DeepEqual(input.ReferencedFields, normalized.ReferencedFields) {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_PREDICATE_METADATA", "predicate metadata does not agree with its semantic tree")}
	}
	return nil
}

func CanonicalPredicate(engine *Engine, input ir.SchemaPredicateIR) ([]byte, []ir.Diagnostic) {
	normalized, diagnostics := engine.ValidatePredicate(input)
	if hasErrors(diagnostics) {
		return nil, diagnostics
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, []ir.Diagnostic{schemaError("P1_SCHEMA_CANONICAL_ENCODING", err.Error())}
	}
	return encoded, diagnostics
}

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}
