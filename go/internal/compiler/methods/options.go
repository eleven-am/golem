package methods

import (
	"go/ast"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/keyindex"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
)

func (in *interpreter) evalOption(expression ast.Expr, scope ir.ProviderScope) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		in.errorAt("P1_METHOD_OPTION", "model options must be direct constructor/fluent calls", expression)
		return
	}
	switch in.callOperation(call) {
	case "ForProvider":
		if len(call.Args) < 2 {
			in.errorAt("P1_METHOD_PROVIDER_ARITY", "ForProvider requires a provider and at least one option", call)
			return
		}
		provider, valid := in.provider(call.Args[0])
		if !valid {
			in.errorAt("P1_METHOD_PROVIDER", "ForProvider requires the typed golem.SQLite or golem.PostgreSQL constant", call.Args[0])
			return
		}
		if scope != ir.ProviderScopePortable && scope != provider {
			in.errorAt("P1_METHOD_PROVIDER_NESTING", "nested provider scopes must agree", call)
			return
		}
		for _, option := range call.Args[1:] {
			in.evalOption(option, provider)
		}
	case "PrimaryKey", "Unique":
		if scope != ir.ProviderScopePortable {
			in.errorAt("P1_METHOD_PROVIDER_KEY", "primary and unique keys cannot be provider-scoped", call)
			return
		}
		in.evalKey(call)
	case "Check":
		in.evalCheck(call, scope)
	case "Generated":
		in.evalGenerated(call, scope)
	default:
		if index, valid := in.evalIndex(expression, scope); valid {
			in.advanced.Indexes = append(in.advanced.Indexes, index)
			return
		}
		if relation, valid := in.evalRelationOption(expression, scope); valid {
			in.relations = append(in.relations, relation)
			return
		}
		in.errorAt("P1_METHOD_OPTION_CALL", "call is not a recognized golem model-option constructor", expression)
	}
}

func (in *interpreter) evalKey(call *ast.CallExpr) {
	if len(call.Args) < 2 {
		in.errorAt("P1_METHOD_KEY_ARITY", "key declaration requires a name and at least one scalar field", call)
		return
	}
	name, valid := in.constString(call.Args[0])
	if !valid {
		in.errorAt("P1_METHOD_NAME", "physical names must be string literals or typed string constants", call.Args[0])
		return
	}
	declaration := keyindex.KeyDeclaration{PhysicalName: ir.SQLIdentifier(name), Span: in.span(call)}
	if in.callOperation(call) == "PrimaryKey" {
		declaration.Kind = ir.KeyPrimary
	} else {
		declaration.Kind = ir.KeyUnique
	}
	for _, argument := range call.Args[1:] {
		symbol, ok := in.resolveHandle(argument)
		if !ok || symbol.Kind != "field" {
			in.errorAt("P1_METHOD_KEY_FIELD", "key components must be generated scalar handles for this model", argument)
			return
		}
		declaration.Fields = append(declaration.Fields, symbol.FieldID)
	}
	in.advanced.Keys = append(in.advanced.Keys, declaration)
}

func (in *interpreter) evalCheck(call *ast.CallExpr, scope ir.ProviderScope) {
	if len(call.Args) != 2 {
		in.errorAt("P1_METHOD_CHECK_ARITY", "Check requires a name and predicate", call)
		return
	}
	name, valid := in.constString(call.Args[0])
	if !valid {
		in.errorAt("P1_METHOD_NAME", "check name must be a string literal or typed string constant", call.Args[0])
		return
	}
	predicate, valid := in.evalPredicate(call.Args[1:][0])
	if !valid {
		return
	}
	analysis, diagnostics := in.engine.ValidatePredicateUse(predicate, schemaexpr.UseCheck, scope)
	in.addDiagnostics(diagnostics, call.Args[1])
	if hasErrors(diagnostics) {
		return
	}
	in.advanced.Checks = append(in.advanced.Checks, keyindex.CheckDeclaration{PhysicalName: ir.SQLIdentifier(name), Predicate: analysis.Predicate, Provider: scope, Span: in.span(call)})
}

func (in *interpreter) evalGenerated(call *ast.CallExpr, scope ir.ProviderScope) {
	if len(call.Args) != 3 {
		in.errorAt("P1_METHOD_GENERATED_ARITY", "Generated requires target field, expression, and storage", call)
		return
	}
	handle, valid := in.resolveHandle(call.Args[0])
	if !valid || handle.Kind != "field" {
		in.errorAt("P1_METHOD_GENERATED_FIELD", "generated target must be a generated scalar handle for this model", call.Args[0])
		return
	}
	expression, valid := in.evalExpression(call.Args[1])
	if !valid {
		return
	}
	var storage ir.GeneratedStorage
	switch in.constantName(call.Args[2]) {
	case "Stored":
		storage = ir.GeneratedStored
	case "Virtual":
		storage = ir.GeneratedVirtual
	default:
		in.errorAt("P1_METHOD_GENERATED_STORAGE", "generated storage must be golem.Stored or golem.Virtual", call.Args[2])
		return
	}
	in.generated = append(in.generated, pendingGenerated{fieldID: handle.FieldID, expr: expression, storage: storage, scope: scope, span: in.span(call)})
}

func (in *interpreter) finishGenerated() {
	inputs := make([]schemaexpr.GeneratedInput, len(in.generated))
	byField := make(map[ir.FieldID]pendingGenerated, len(in.generated))
	for index, declaration := range in.generated {
		inputs[index] = schemaexpr.GeneratedInput{FieldID: declaration.fieldID, Expr: declaration.expr, Scope: declaration.scope, Span: declaration.span}
		byField[declaration.fieldID] = declaration
	}
	plan, diagnostics := in.engine.PlanGenerated(inputs)
	in.addDiagnostics(diagnostics, nil)
	if hasErrors(diagnostics) {
		return
	}
	for _, node := range plan.Nodes {
		declaration := byField[node.FieldID]
		in.advanced.Generated = append(in.advanced.Generated, keyindex.GeneratedDeclaration{FieldID: node.FieldID, Generation: ir.GeneratedColumnIR{Expr: node.Expr, Storage: declaration.storage, Provider: declaration.scope}, ExpressionProvenNonNull: !node.Expr.Nullable, Span: declaration.span})
	}
}

type indexBuilder struct {
	name      string
	keys      []ir.IndexKeyIR
	predicate *ir.SchemaPredicateIR
	span      ir.SourceSpan
}

func (in *interpreter) evalIndex(expression ast.Expr, scope ir.ProviderScope) (keyindex.IndexDeclaration, bool) {
	builder, valid := in.indexBuilder(expression, scope)
	if !valid {
		return keyindex.IndexDeclaration{}, false
	}
	return keyindex.IndexDeclaration{PhysicalName: ir.SQLIdentifier(builder.name), Method: ir.IndexBTree, Keys: builder.keys, Predicate: builder.predicate, Provider: scope, Span: builder.span}, true
}

func (in *interpreter) indexBuilder(expression ast.Expr, scope ir.ProviderScope) (indexBuilder, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		return indexBuilder{}, false
	}
	switch in.callOperation(call) {
	case "Index":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_INDEX_ARITY", "Index requires one physical name", call)
			return indexBuilder{}, true
		}
		name, valid := in.constString(call.Args[0])
		if !valid {
			in.errorAt("P1_METHOD_NAME", "index name must be a string literal or typed string constant", call.Args[0])
			return indexBuilder{}, true
		}
		return indexBuilder{name: name, span: in.span(call)}, true
	case "IndexSpec.Keys":
		builder, valid := in.indexBuilder(in.receiver(call), scope)
		if !valid {
			return indexBuilder{}, false
		}
		if len(call.Args) == 0 {
			in.errorAt("P1_METHOD_INDEX_KEYS", "Keys requires at least one index key", call)
			return builder, true
		}
		for _, argument := range call.Args {
			key, ok := in.evalIndexKey(argument, scope)
			if ok {
				builder.keys = append(builder.keys, key)
			}
		}
		builder.span = in.span(call)
		return builder, true
	case "IndexSpec.Where":
		builder, valid := in.indexBuilder(in.receiver(call), scope)
		if !valid {
			return indexBuilder{}, false
		}
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_INDEX_WHERE", "Where requires one schema predicate", call)
			return builder, true
		}
		predicate, ok := in.evalPredicate(call.Args[0])
		if !ok {
			return builder, true
		}
		analysis, diagnostics := in.engine.ValidatePredicateUse(predicate, schemaexpr.UsePartialIndex, scope)
		in.addDiagnostics(diagnostics, call.Args[0])
		if !hasErrors(diagnostics) {
			builder.predicate = &analysis.Predicate
		}
		builder.span = in.span(call)
		return builder, true
	default:
		return indexBuilder{}, false
	}
}

func (in *interpreter) evalIndexKey(expression ast.Expr, scope ir.ProviderScope) (ir.IndexKeyIR, bool) {
	direction := ir.SortAsc
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		in.errorAt("P1_METHOD_INDEX_KEY", "index key must use IndexColumn or IndexExpr", expression)
		return ir.IndexKeyIR{}, false
	}
	if in.callOperation(call) == "IndexKey.Desc" {
		if len(call.Args) != 0 {
			in.errorAt("P1_METHOD_INDEX_DESC", "Desc takes no arguments", call)
			return ir.IndexKeyIR{}, false
		}
		direction = ir.SortDesc
		call, ok = unparen(in.receiver(call)).(*ast.CallExpr)
		if !ok {
			in.errorAt("P1_METHOD_INDEX_KEY", "Desc receiver must be an index key constructor", expression)
			return ir.IndexKeyIR{}, false
		}
	}
	key := ir.IndexKeyIR{Direction: direction, Nulls: ir.NullsDefault}
	switch in.callOperation(call) {
	case "IndexColumn":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_INDEX_COLUMN", "IndexColumn requires one scalar handle", call)
			return key, false
		}
		handle, valid := in.resolveHandle(call.Args[0])
		if !valid || handle.Kind != "field" {
			in.errorAt("P1_METHOD_INDEX_COLUMN", "IndexColumn requires a generated scalar handle for this model", call.Args[0])
			return key, false
		}
		key.Column = &handle.FieldID
	case "IndexExpr":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_INDEX_EXPR", "IndexExpr requires one schema expression", call)
			return key, false
		}
		expressionIR, valid := in.evalExpression(call.Args[0])
		if !valid {
			return key, false
		}
		normalized, diagnostics := in.engine.ValidateIndexExpression(expressionIR, scope)
		in.addDiagnostics(diagnostics, call.Args[0])
		if hasErrors(diagnostics) {
			return key, false
		}
		key.Expr = &normalized
	default:
		in.errorAt("P1_METHOD_INDEX_KEY", "index key must use IndexColumn or IndexExpr", call)
		return key, false
	}
	return key, true
}

type relationBuilder struct {
	handle         modelRelationHandle
	update, delete *ir.ReferentialAction
	span           ir.SourceSpan
}
type modelRelationHandle struct {
	relationID ir.RelationID
	fieldID    ir.FieldID
}

func (in *interpreter) evalRelationOption(expression ast.Expr, scope ir.ProviderScope) (RelationOptionDeclaration, bool) {
	builder, valid := in.relationBuilder(expression)
	if !valid {
		return RelationOptionDeclaration{}, false
	}
	return RelationOptionDeclaration{ModelID: in.model.ID, RelationID: builder.handle.relationID, RelationField: builder.handle.fieldID, OnUpdate: builder.update, OnDelete: builder.delete, Provider: scope, Span: builder.span}, true
}

func (in *interpreter) relationBuilder(expression ast.Expr) (relationBuilder, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		return relationBuilder{}, false
	}
	switch in.callOperation(call) {
	case "RelationOptions":
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_RELATION_ARITY", "RelationOptions requires one to-one relation handle", call)
			return relationBuilder{}, true
		}
		handle, valid := in.resolveHandle(call.Args[0])
		if !valid || handle.Kind != "relation" {
			in.errorAt("P1_METHOD_RELATION_HANDLE", "RelationOptions requires a generated relation handle for this model", call.Args[0])
			return relationBuilder{}, true
		}
		return relationBuilder{handle: modelRelationHandle{handle.RelationID, handle.FieldID}, span: in.span(call)}, true
	case "RelationOptionSpec.OnUpdate", "RelationOptionSpec.OnDelete":
		builder, valid := in.relationBuilder(in.receiver(call))
		if !valid {
			return relationBuilder{}, false
		}
		if len(call.Args) != 1 {
			in.errorAt("P1_METHOD_RELATION_ACTION", "relation action method requires one referential-action constant", call)
			return builder, true
		}
		action, ok := in.action(call.Args[0])
		if !ok {
			in.errorAt("P1_METHOD_RELATION_ACTION", "relation action must be a typed golem referential-action constant", call.Args[0])
			return builder, true
		}
		if in.callOperation(call) == "RelationOptionSpec.OnUpdate" {
			builder.update = &action
		} else {
			builder.delete = &action
		}
		builder.span = in.span(call)
		return builder, true
	default:
		return relationBuilder{}, false
	}
}
