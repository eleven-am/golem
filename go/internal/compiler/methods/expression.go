package methods

import (
	"go/ast"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
)

func (in *interpreter) evalExpression(expression ast.Expr) (ir.SchemaExprIR, bool) {
	if handle, valid := in.resolveHandle(expression); valid && handle.Kind == "field" {
		result, diagnostics := in.engine.Field(handle.FieldID)
		in.addDiagnostics(diagnostics, expression)
		return result, !hasErrors(diagnostics)
	}
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		logical, valid := in.logicalTypeOf(expression)
		if !valid {
			in.errorAt("P1_METHOD_EXPRESSION", "expression is not a generated handle, constant, or recognized schema call", expression)
			return ir.SchemaExprIR{}, false
		}
		return in.literal(expression, logical)
	}
	// Expr is the one schema-expression bridge shared by every generated scalar
	// handle family. Recognize it from the manifest-backed receiver instead of
	// coupling interpretation to one concrete public handle type: P2 narrows the
	// policy method set per logical field capability, while every scalar remains a
	// valid schema value.
	if handle, valid := in.resolveHandle(in.receiver(call)); valid && handle.Kind == "field" {
		if function := in.callObject(call); function != nil && function.Name() == "Expr" {
			if len(call.Args) != 0 {
				in.errorAt("P1_METHOD_EXPR_ARITY", "Expr takes no arguments", call)
				return ir.SchemaExprIR{}, false
			}
			return in.evalExpression(in.receiver(call))
		}
	}
	operation := in.callOperation(call)
	switch operation {
	case "SchemaValueOf":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_EXPR_ARITY", "SchemaValueOf requires one constant", call)
			return ir.SchemaExprIR{}, false
		}
		logical, valid := in.logicalTypeOf(call)
		if !valid {
			in.errorAt("P1_METHOD_EXPR_TYPE", "cannot determine SchemaValueOf result type", call)
			return ir.SchemaExprIR{}, false
		}
		return in.literal(call.Args[0], logical)
	case "Lower", "Upper", "Length":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_EXPR_ARITY", operation+" requires one operand", call)
			return ir.SchemaExprIR{}, false
		}
		operand, valid := in.evalExpression(call.Args[0])
		if !valid {
			return ir.SchemaExprIR{}, false
		}
		identity := map[string]string{"Lower": schemaexpr.Lower, "Upper": schemaexpr.Upper, "Length": schemaexpr.Length}[operation]
		result, diagnostics := in.engine.Expression(identity, operand)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "Coalesce":
		operands := make([]ir.SchemaExprIR, 0, len(call.Args))
		for _, argument := range call.Args {
			operand, valid := in.evalExpression(argument)
			if !valid {
				return ir.SchemaExprIR{}, false
			}
			operands = append(operands, operand)
		}
		result, diagnostics := in.engine.Expression(schemaexpr.Coalesce, operands...)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "SchemaExpr.Add", "SchemaExpr.Sub", "SchemaExpr.Mul", "SchemaExpr.Div", "SchemaExpr.Mod":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_EXPR_ARITY", "schema arithmetic method requires one operand", call)
			return ir.SchemaExprIR{}, false
		}
		left, valid := in.evalExpression(in.receiver(call))
		if !valid {
			return ir.SchemaExprIR{}, false
		}
		right, valid := in.evalExpression(call.Args[0])
		if !valid {
			return ir.SchemaExprIR{}, false
		}
		identity := map[string]string{"SchemaExpr.Add": schemaexpr.Add, "SchemaExpr.Sub": schemaexpr.Subtract, "SchemaExpr.Mul": schemaexpr.Multiply, "SchemaExpr.Div": schemaexpr.Divide, "SchemaExpr.Mod": schemaexpr.Remainder}[operation]
		result, diagnostics := in.engine.Expression(identity, left, right)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "Cast":
		if len(call.Args) != 2 {
			in.errorAt("P1_METHOD_EXPR_ARITY", "Cast requires one operand and one registered cast", call)
			return ir.SchemaExprIR{}, false
		}
		identity := map[string]string{
			"Int16ToInt32":  schemaexpr.CastInt16ToInt32,
			"Int16ToInt64":  schemaexpr.CastInt16ToInt64,
			"Int32ToInt64":  schemaexpr.CastInt32ToInt64,
			"Int64ToString": schemaexpr.CastInt64ToString,
		}[in.constantName(call.Args[1])]
		if identity == "" {
			in.errorAt("P1_METHOD_CAST_IDENTITY", "Cast requires an exact registered golem cast object", call.Args[1])
			return ir.SchemaExprIR{}, false
		}
		operand, valid := in.evalExpression(call.Args[0])
		if !valid {
			return ir.SchemaExprIR{}, false
		}
		result, diagnostics := in.engine.Expression(identity, operand)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	default:
		in.errorAt("P1_METHOD_EXPRESSION_CALL", "arbitrary function and method calls are forbidden in schema expressions", call)
		return ir.SchemaExprIR{}, false
	}
}

func (in *interpreter) evalPredicate(expression ast.Expr) (ir.SchemaPredicateIR, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		in.errorAt("P1_METHOD_PREDICATE", "predicate must be a recognized closed schema call", expression)
		return ir.SchemaPredicateIR{}, false
	}
	operation := in.callOperation(call)
	switch operation {
	case "SchemaExpr.Eq", "SchemaExpr.Ne", "SchemaExpr.LT", "SchemaExpr.LTE", "SchemaExpr.GT", "SchemaExpr.GTE":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_PREDICATE_ARITY", "comparison requires one constant", call)
			return ir.SchemaPredicateIR{}, false
		}
		left, valid := in.evalExpression(in.receiver(call))
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		right, valid := in.literal(call.Args[0], left.ResultType)
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		identity := map[string]string{"SchemaExpr.Eq": schemaexpr.Equal, "SchemaExpr.Ne": schemaexpr.NotEqual, "SchemaExpr.LT": schemaexpr.Less, "SchemaExpr.LTE": schemaexpr.LessEqual, "SchemaExpr.GT": schemaexpr.Greater, "SchemaExpr.GTE": schemaexpr.GreaterEq}[operation]
		result, diagnostics := in.engine.Predicate(identity, left, right)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "SchemaExpr.IsNull", "SchemaExpr.IsNotNull":
		if len(call.Args) != 0 {
			in.errorAt("P1_METHOD_PREDICATE_ARITY", "null test takes no arguments", call)
			return ir.SchemaPredicateIR{}, false
		}
		operand, valid := in.evalExpression(in.receiver(call))
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		identity := schemaexpr.IsNull
		if operation == "SchemaExpr.IsNotNull" {
			identity = schemaexpr.IsNotNull
		}
		result, diagnostics := in.engine.Predicate(identity, operand)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "SchemaPredicate.And", "SchemaPredicate.Or":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_PREDICATE_ARITY", "logical method requires one predicate", call)
			return ir.SchemaPredicateIR{}, false
		}
		left, valid := in.evalPredicate(in.receiver(call))
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		right, valid := in.evalPredicate(call.Args[0])
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		var result ir.SchemaPredicateIR
		var diagnostics []ir.Diagnostic
		if operation == "SchemaPredicate.And" {
			result, diagnostics = in.engine.And(left, right)
		} else {
			result, diagnostics = in.engine.Or(left, right)
		}
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	case "SchemaPredicate.Not":
		if len(call.Args) != 0 {
			in.errorAt("P1_METHOD_PREDICATE_ARITY", "Not takes no arguments", call)
			return ir.SchemaPredicateIR{}, false
		}
		child, valid := in.evalPredicate(in.receiver(call))
		if !valid {
			return ir.SchemaPredicateIR{}, false
		}
		result, diagnostics := in.engine.Not(child)
		in.addDiagnostics(diagnostics, call)
		return result, !hasErrors(diagnostics)
	default:
		in.errorAt("P1_METHOD_PREDICATE_CALL", "arbitrary function and method calls are forbidden in schema predicates", call)
		return ir.SchemaPredicateIR{}, false
	}
}
