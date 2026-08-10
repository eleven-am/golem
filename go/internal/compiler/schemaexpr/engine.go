package schemaexpr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
)

type Engine struct {
	model    ir.ModelDeclIR
	fields   map[ir.FieldID]ir.FieldIR
	registry *Registry
}

func New(model ir.ModelDeclIR, registry *Registry) *Engine {
	if registry == nil {
		registry = NewRegistry()
	}
	fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	return &Engine{model: model, fields: fields, registry: registry}
}

func (engine *Engine) Field(fieldID ir.FieldID) (ir.SchemaExprIR, []ir.Diagnostic) {
	field, exists := engine.fields[fieldID]
	if !exists || field.Scalar == nil {
		return ir.SchemaExprIR{}, []ir.Diagnostic{schemaError("P1_SCHEMA_FIELD_OWN_ROW", fmt.Sprintf("field %s is not a scalar field of model %s", fieldID, engine.model.ID))}
	}
	expression := ir.SchemaExprIR{Kind: ir.SchemaExprField, ResultType: field.Scalar.Type, Nullable: field.Scalar.Nullable, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, Field: fieldIDPointer(fieldID), ReferencedFields: []ir.FieldID{fieldID}}
	expression.CanonicalIdentity = expressionIdentity(expression)
	return expression, nil
}

func (engine *Engine) Literal(logicalType ir.LogicalTypeIR, literal ir.TypedLiteralIR) (ir.SchemaExprIR, []ir.Diagnostic) {
	normalized, diagnostics := scalar.NormalizeType(logicalType, ir.SourceSpan{})
	if !literalMatches(normalized, literal.Kind) {
		diagnostics = append(diagnostics, schemaError("P1_SCHEMA_LITERAL_TYPE", fmt.Sprintf("literal kind %s does not match logical type %s", literal.Kind, normalized.Kind)))
	}
	expression := ir.SchemaExprIR{Kind: ir.SchemaExprLiteral, ResultType: normalized, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, Literal: literalPointer(literal)}
	expression.CanonicalIdentity = expressionIdentity(expression)
	ir.SortDiagnostics(diagnostics)
	return expression, diagnostics
}

func (engine *Engine) Expression(identity string, operands ...ir.SchemaExprIR) (ir.SchemaExprIR, []ir.Diagnostic) {
	spec, exists := engine.registry.symbols[identity]
	if !exists || spec.Role != RoleExpression {
		return ir.SchemaExprIR{}, []ir.Diagnostic{schemaError("P1_SCHEMA_SYMBOL_UNKNOWN", fmt.Sprintf("unknown schema expression symbol %q", identity))}
	}
	return engine.buildExpression(spec, operands)
}

func (engine *Engine) Predicate(identity string, operands ...ir.SchemaExprIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	spec, exists := engine.registry.symbols[identity]
	if !exists || spec.Role != RolePredicate {
		return ir.SchemaPredicateIR{}, []ir.Diagnostic{schemaError("P1_SCHEMA_SYMBOL_UNKNOWN", fmt.Sprintf("unknown schema predicate symbol %q", identity))}
	}
	return engine.buildPredicate(spec, operands)
}

func (engine *Engine) Constant(value bool) ir.SchemaPredicateIR {
	predicate := ir.SchemaPredicateIR{Kind: ir.SchemaPredicateConstant, ResultType: ir.LogicalTypeIR{Kind: ir.TypeBool}, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, Constant: boolPointer(value)}
	predicate.CanonicalIdentity = predicateIdentity(predicate)
	return predicate
}

func (engine *Engine) And(children ...ir.SchemaPredicateIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	return engine.logical(ir.SchemaPredicateAnd, children)
}

func (engine *Engine) Or(children ...ir.SchemaPredicateIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	return engine.logical(ir.SchemaPredicateOr, children)
}

func (engine *Engine) Not(child ir.SchemaPredicateIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	return engine.logical(ir.SchemaPredicateNot, []ir.SchemaPredicateIR{child})
}

func (engine *Engine) buildExpression(spec SymbolSpec, operands []ir.SchemaExprIR) (ir.SchemaExprIR, []ir.Diagnostic) {
	operands = normalizeExpressionOperands(spec, operands)
	resultType, diagnostics := inferExpressionType(spec, operands)
	diagnostics = append(diagnostics, validateArity(spec, len(operands))...)
	scope, scopeDiagnostics := operandScope(spec.Ref.Provider, operands)
	diagnostics = append(diagnostics, scopeDiagnostics...)
	volatility, deterministic := expressionBehavior(spec.Ref, operands)
	expression := ir.SchemaExprIR{Kind: expressionKind(spec.Ref.Kind), ResultType: resultType, Nullable: nullableExpressions(spec.NullRule, operands), Provider: scope, Volatility: volatility, Deterministic: deterministic, Symbol: symbolPointer(spec.Ref), Operands: operands, ReferencedFields: expressionReferences(operands)}
	expression.CanonicalIdentity = expressionIdentity(expression)
	ir.SortDiagnostics(diagnostics)
	return expression, diagnostics
}

func (engine *Engine) buildPredicate(spec SymbolSpec, operands []ir.SchemaExprIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	operands = normalizePredicateOperands(spec, operands)
	diagnostics := validateArity(spec, len(operands))
	diagnostics = append(diagnostics, validatePredicateTypes(spec, operands)...)
	scope, scopeDiagnostics := operandScope(spec.Ref.Provider, operands)
	diagnostics = append(diagnostics, scopeDiagnostics...)
	volatility, deterministic := expressionBehavior(spec.Ref, operands)
	predicate := ir.SchemaPredicateIR{Kind: ir.SchemaPredicateOperator, ResultType: ir.LogicalTypeIR{Kind: ir.TypeBool}, Nullable: nullableExpressions(spec.NullRule, operands), Provider: scope, Volatility: volatility, Deterministic: deterministic, Symbol: symbolPointer(spec.Ref), ExpressionOperands: operands, ReferencedFields: expressionReferences(operands)}
	predicate.CanonicalIdentity = predicateIdentity(predicate)
	ir.SortDiagnostics(diagnostics)
	return predicate, diagnostics
}

func (engine *Engine) logical(kind ir.SchemaPredicateKind, children []ir.SchemaPredicateIR) (ir.SchemaPredicateIR, []ir.Diagnostic) {
	minimum := 2
	if kind == ir.SchemaPredicateNot {
		minimum = 1
	}
	if kind == ir.SchemaPredicateAnd || kind == ir.SchemaPredicateOr {
		children = flattenChildren(kind, children)
		sort.Slice(children, func(i, j int) bool { return children[i].CanonicalIdentity < children[j].CanonicalIdentity })
		children = dedupePredicates(children)
	}
	var diagnostics []ir.Diagnostic
	if len(children) != minimum && kind == ir.SchemaPredicateNot || len(children) < minimum && kind != ir.SchemaPredicateNot {
		diagnostics = append(diagnostics, schemaError("P1_SCHEMA_PREDICATE_ARITY", fmt.Sprintf("%s predicate has invalid arity %d", kind, len(children))))
	}
	scope := ir.ProviderScopePortable
	volatility := ir.SchemaVolatilityImmutable
	deterministic := true
	nullable := false
	refs := []ir.FieldID{}
	for _, child := range children {
		var scopeDiagnostics []ir.Diagnostic
		scope, scopeDiagnostics = intersectScope(scope, child.Provider)
		diagnostics = append(diagnostics, scopeDiagnostics...)
		volatility = maxVolatility(volatility, child.Volatility)
		deterministic = deterministic && child.Deterministic
		nullable = nullable || child.Nullable
		refs = append(refs, child.ReferencedFields...)
	}
	predicate := ir.SchemaPredicateIR{Kind: kind, ResultType: ir.LogicalTypeIR{Kind: ir.TypeBool}, Nullable: nullable, Provider: scope, Volatility: volatility, Deterministic: deterministic, Children: children, ReferencedFields: canonicalFields(refs)}
	predicate.CanonicalIdentity = predicateIdentity(predicate)
	ir.SortDiagnostics(diagnostics)
	return predicate, diagnostics
}

func inferExpressionType(spec SymbolSpec, operands []ir.SchemaExprIR) (ir.LogicalTypeIR, []ir.Diagnostic) {
	if len(operands) == 0 {
		return spec.Output, nil
	}
	same := func(allowed func(ir.LogicalTypeKind) bool) (ir.LogicalTypeIR, []ir.Diagnostic) {
		first := operands[0].ResultType
		if !allowed(first.Kind) {
			return first, []ir.Diagnostic{schemaError("P1_SCHEMA_OPERAND_TYPE", fmt.Sprintf("%s does not accept %s", spec.Ref.Name, first.Kind))}
		}
		for _, operand := range operands[1:] {
			if !reflect.DeepEqual(first, operand.ResultType) {
				return first, []ir.Diagnostic{schemaError("P1_SCHEMA_OPERAND_TYPE", fmt.Sprintf("%s operands must have identical logical types", spec.Ref.Name))}
			}
		}
		return first, nil
	}
	switch spec.rule {
	case ruleNumericSame:
		return same(isNumeric)
	case ruleStringSame:
		return same(func(kind ir.LogicalTypeKind) bool { return kind == ir.TypeString })
	case ruleLength:
		if len(operands) != 1 || operands[0].ResultType.Kind != ir.TypeString && operands[0].ResultType.Kind != ir.TypeBytes {
			return ir.LogicalTypeIR{Kind: ir.TypeInt64}, []ir.Diagnostic{schemaError("P1_SCHEMA_OPERAND_TYPE", "length accepts one String or Bytes operand")}
		}
		return ir.LogicalTypeIR{Kind: ir.TypeInt64}, nil
	case ruleCoalesce:
		return same(func(ir.LogicalTypeKind) bool { return true })
	case ruleFixed:
		var diagnostics []ir.Diagnostic
		if len(operands) == len(spec.Inputs) {
			for index := range operands {
				if !reflect.DeepEqual(operands[index].ResultType, spec.Inputs[index]) {
					diagnostics = append(diagnostics, schemaError("P1_SCHEMA_OPERAND_TYPE", fmt.Sprintf("%s operand %d has type %s, want %s", spec.Ref.Name, index, operands[index].ResultType.Kind, spec.Inputs[index].Kind)))
				}
			}
		}
		return spec.Output, diagnostics
	default:
		return spec.Output, []ir.Diagnostic{schemaError("P1_SCHEMA_SYMBOL_ROLE", "predicate symbol used as a value expression")}
	}
}

func validatePredicateTypes(spec SymbolSpec, operands []ir.SchemaExprIR) []ir.Diagnostic {
	if len(operands) == 0 {
		return nil
	}
	if spec.rule == ruleNullTest {
		return nil
	}
	first := operands[0].ResultType
	for _, operand := range operands[1:] {
		if !reflect.DeepEqual(first, operand.ResultType) {
			return []ir.Diagnostic{schemaError("P1_SCHEMA_PREDICATE_TYPE", fmt.Sprintf("%s operands must have identical logical types", spec.Ref.Name))}
		}
	}
	if spec.rule == ruleOrdered && !isOrdered(first.Kind) || spec.rule == ruleComparable && !isComparable(first.Kind) || spec.rule == ruleIn && !isComparable(first.Kind) {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_PREDICATE_TYPE", fmt.Sprintf("%s does not accept %s", spec.Ref.Name, first.Kind))}
	}
	return nil
}

func validateArity(spec SymbolSpec, count int) []ir.Diagnostic {
	valid := count == spec.MinimumArity
	if spec.Variadic {
		valid = count >= spec.MinimumArity
	}
	if !valid {
		return []ir.Diagnostic{schemaError("P1_SCHEMA_ARITY", fmt.Sprintf("%s received %d operands", spec.Ref.Name, count))}
	}
	return nil
}

func expressionKind(kind ir.SchemaSymbolKind) ir.SchemaExprKind {
	switch kind {
	case ir.SchemaSymbolOperator:
		return ir.SchemaExprOperator
	case ir.SchemaSymbolCast:
		return ir.SchemaExprCast
	default:
		return ir.SchemaExprFunction
	}
}

func expressionIdentity(expression ir.SchemaExprIR) string {
	parts := []any{expression.Kind, expression.ResultType, expression.Nullable}
	if expression.Symbol != nil {
		parts = append(parts, *expression.Symbol)
	}
	if expression.Field != nil {
		parts = append(parts, *expression.Field)
	}
	if expression.Literal != nil {
		parts = append(parts, *expression.Literal)
	}
	for _, operand := range expression.Operands {
		parts = append(parts, operand.CanonicalIdentity)
	}
	return canonicalHash("expression", parts)
}

func predicateIdentity(predicate ir.SchemaPredicateIR) string {
	parts := []any{predicate.Kind, predicate.Nullable}
	if predicate.Symbol != nil {
		parts = append(parts, *predicate.Symbol)
	}
	if predicate.Constant != nil {
		parts = append(parts, *predicate.Constant)
	}
	for _, operand := range predicate.ExpressionOperands {
		parts = append(parts, operand.CanonicalIdentity)
	}
	for _, child := range predicate.Children {
		parts = append(parts, child.CanonicalIdentity)
	}
	return canonicalHash("predicate", parts)
}

func canonicalHash(domain string, parts []any) string {
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(append([]byte("golem:schema-"+domain+":v1\x00"), encoded...))
	return hex.EncodeToString(sum[:])
}

func normalizeExpressionOperands(spec SymbolSpec, operands []ir.SchemaExprIR) []ir.SchemaExprIR {
	result := append([]ir.SchemaExprIR(nil), operands...)
	if spec.associative {
		flat := make([]ir.SchemaExprIR, 0, len(result))
		for _, operand := range result {
			if operand.Symbol != nil && operand.Symbol.Identity == spec.Ref.Identity {
				flat = append(flat, operand.Operands...)
			} else {
				flat = append(flat, operand)
			}
		}
		result = flat
	}
	if spec.commutative {
		sort.Slice(result, func(i, j int) bool { return result[i].CanonicalIdentity < result[j].CanonicalIdentity })
	}
	return result
}

func normalizePredicateOperands(spec SymbolSpec, operands []ir.SchemaExprIR) []ir.SchemaExprIR {
	result := append([]ir.SchemaExprIR(nil), operands...)
	if spec.commutative {
		sort.Slice(result, func(i, j int) bool { return result[i].CanonicalIdentity < result[j].CanonicalIdentity })
	}
	if spec.rule == ruleIn && len(result) > 2 {
		tail := append([]ir.SchemaExprIR(nil), result[1:]...)
		sort.Slice(tail, func(i, j int) bool { return tail[i].CanonicalIdentity < tail[j].CanonicalIdentity })
		tail = dedupeExpressions(tail)
		result = append([]ir.SchemaExprIR{result[0]}, tail...)
	}
	return result
}

func literalMatches(logical ir.LogicalTypeIR, literal ir.LiteralKind) bool {
	matching := map[ir.LogicalTypeKind]ir.LiteralKind{ir.TypeBool: ir.LiteralBool, ir.TypeInt16: ir.LiteralInteger, ir.TypeInt32: ir.LiteralInteger, ir.TypeInt64: ir.LiteralInteger, ir.TypeFloat32: ir.LiteralFloat, ir.TypeFloat64: ir.LiteralFloat, ir.TypeDecimal: ir.LiteralDecimal, ir.TypeString: ir.LiteralString, ir.TypeBytes: ir.LiteralBytes, ir.TypeUUID: ir.LiteralUUID, ir.TypeDate: ir.LiteralDate, ir.TypeTime: ir.LiteralTime, ir.TypeDateTime: ir.LiteralDateTime, ir.TypeJSON: ir.LiteralJSON, ir.TypeEnum: ir.LiteralEnum, ir.TypeScalarList: ir.LiteralList}
	return matching[logical.Kind] == literal
}

func isNumeric(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal:
		return true
	default:
		return false
	}
}

func isComparable(kind ir.LogicalTypeKind) bool {
	return kind != ir.TypeJSON && kind != ir.TypeScalarList
}

func isOrdered(kind ir.LogicalTypeKind) bool {
	return isNumeric(kind) || kind == ir.TypeString || kind == ir.TypeEnum || kind == ir.TypeDate || kind == ir.TypeTime || kind == ir.TypeDateTime
}

func nullableExpressions(rule NullRule, operands []ir.SchemaExprIR) bool {
	if rule == NeverNull {
		return false
	}
	if rule == NullIfAll {
		if len(operands) == 0 {
			return false
		}
		for _, operand := range operands {
			if !operand.Nullable {
				return false
			}
		}
		return true
	}
	for _, operand := range operands {
		if operand.Nullable {
			return true
		}
	}
	return false
}

func expressionBehavior(ref ir.SchemaSymbolRef, operands []ir.SchemaExprIR) (ir.SchemaVolatility, bool) {
	volatility := ref.Volatility
	deterministic := ref.Deterministic
	for _, operand := range operands {
		volatility = maxVolatility(volatility, operand.Volatility)
		deterministic = deterministic && operand.Deterministic
	}
	return volatility, deterministic
}

func operandScope(initial ir.ProviderScope, operands []ir.SchemaExprIR) (ir.ProviderScope, []ir.Diagnostic) {
	scope := initial
	var diagnostics []ir.Diagnostic
	for _, operand := range operands {
		var current []ir.Diagnostic
		scope, current = intersectScope(scope, operand.Provider)
		diagnostics = append(diagnostics, current...)
	}
	return scope, diagnostics
}

func intersectScope(left, right ir.ProviderScope) (ir.ProviderScope, []ir.Diagnostic) {
	if !validScope(left) || !validScope(right) {
		return "", []ir.Diagnostic{schemaError("P1_SCHEMA_PROVIDER_SCOPE", fmt.Sprintf("invalid provider scope intersection %q and %q", left, right))}
	}
	if left == ir.ProviderScopePortable {
		return right, nil
	}
	if right == ir.ProviderScopePortable || left == right {
		return left, nil
	}
	return "", []ir.Diagnostic{schemaError("P1_SCHEMA_PROVIDER_MISMATCH", fmt.Sprintf("provider scopes %s and %s do not intersect", left, right))}
}

func maxVolatility(left, right ir.SchemaVolatility) ir.SchemaVolatility {
	rank := map[ir.SchemaVolatility]int{ir.SchemaVolatilityImmutable: 0, ir.SchemaVolatilityStable: 1, ir.SchemaVolatilityVolatile: 2}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func expressionReferences(operands []ir.SchemaExprIR) []ir.FieldID {
	var fields []ir.FieldID
	for _, operand := range operands {
		fields = append(fields, operand.ReferencedFields...)
	}
	return canonicalFields(fields)
}

func canonicalFields(fields []ir.FieldID) []ir.FieldID {
	result := append([]ir.FieldID(nil), fields...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) == 0 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func flattenChildren(kind ir.SchemaPredicateKind, children []ir.SchemaPredicateIR) []ir.SchemaPredicateIR {
	var result []ir.SchemaPredicateIR
	for _, child := range children {
		if child.Kind == kind {
			result = append(result, child.Children...)
		} else {
			result = append(result, child)
		}
	}
	return result
}

func dedupeExpressions(items []ir.SchemaExprIR) []ir.SchemaExprIR {
	if len(items) == 0 {
		return items
	}
	result := items[:1]
	for _, item := range items[1:] {
		if item.CanonicalIdentity != result[len(result)-1].CanonicalIdentity {
			result = append(result, item)
		}
	}
	return result
}

func dedupePredicates(items []ir.SchemaPredicateIR) []ir.SchemaPredicateIR {
	if len(items) == 0 {
		return items
	}
	result := items[:1]
	for _, item := range items[1:] {
		if item.CanonicalIdentity != result[len(result)-1].CanonicalIdentity {
			result = append(result, item)
		}
	}
	return result
}

func fieldIDPointer(value ir.FieldID) *ir.FieldID                { return &value }
func literalPointer(value ir.TypedLiteralIR) *ir.TypedLiteralIR  { return &value }
func symbolPointer(value ir.SchemaSymbolRef) *ir.SchemaSymbolRef { return &value }
func boolPointer(value bool) *bool                               { return &value }

func schemaError(code, message string) ir.Diagnostic {
	return ir.NewError(code, message, ir.SourceSpan{})
}
