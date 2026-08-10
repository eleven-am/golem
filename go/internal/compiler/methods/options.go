package methods

import (
	"go/ast"
	"go/constant"
	"regexp"

	analyticscontract "github.com/eleven-am/golem/go/internal/analytics/contract"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/keyindex"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

var semanticIndexNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func (in *interpreter) evalAnalytics(call *ast.CallExpr) {
	if in.analytics != nil && in.analytics.Enabled {
		in.errorAt("P6_ANALYTICS_MODEL_DUPLICATE", "Analytics may be declared only once in GolemModel", call)
		return
	}
	if in.analytics == nil {
		in.analytics = &analyticscontract.ModelPatch{ModelID: in.model.ID, Span: in.span(call)}
	}
	in.analytics.Enabled = true
	seen := map[string]bool{}
	for _, expression := range call.Args {
		option, ok := unparen(expression).(*ast.CallExpr)
		if !ok {
			in.errorAt("P6_ANALYTICS_OPTION", "Analytics options must be direct constructor calls", expression)
			continue
		}
		operation := in.callOperation(option)
		if seen[operation] {
			in.errorAt("P6_ANALYTICS_OPTION_DUPLICATE", operation+" may be declared only once", option)
			continue
		}
		seen[operation] = true
		switch operation {
		case "AnalyticsDimensions", "AnalyticsMeasures":
			values := make([]ir.FieldID, 0, len(option.Args))
			for _, argument := range option.Args {
				symbol, valid := in.resolveHandle(argument)
				if !valid || symbol.Kind != "field" {
					in.errorAt("P6_ANALYTICS_FIELD", operation+" accepts only scalar handles of this model", argument)
					continue
				}
				values = append(values, symbol.FieldID)
			}
			if operation == "AnalyticsDimensions" {
				in.analytics.Dimensions = &values
			} else {
				in.analytics.Measures = &values
			}
		case "AnalyticsRelationDimensions":
			for _, argument := range option.Args {
				dimension, valid := in.relationDimension(argument)
				if valid {
					in.analytics.RelationDimensions = append(in.analytics.RelationDimensions, dimension)
				}
			}
		case "AnalyticsLimits":
			if len(option.Args) != 2 {
				in.errorAt("P6_ANALYTICS_LIMIT_ARITY", "AnalyticsLimits requires GraphQL and intermediate-group limits", option)
				continue
			}
			graphqlLimit, left := in.uint32Constant(option.Args[0])
			relationLimit, right := in.uint32Constant(option.Args[1])
			if !left || !right {
				in.errorAt("P6_ANALYTICS_LIMIT_VALUE", "analytics limits must be positive uint32 constants", option)
				continue
			}
			in.analytics.GraphQLMaxGroups, in.analytics.RelationMaxIntermediateGroups = &graphqlLimit, &relationLimit
		default:
			in.errorAt("P6_ANALYTICS_OPTION", "call is not a recognized Analytics option constructor", expression)
		}
	}
}

func (in *interpreter) relationDimension(expression ast.Expr) (ir.RelationDimensionContractIR, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok || in.callOperation(call) != "NamedRelationDimension" || len(call.Args) != 2 {
		in.errorAt("P6_RELATION_DIMENSION_DECLARATION", "relation dimensions require NamedRelationDimension(name, path)", expression)
		return ir.RelationDimensionContractIR{}, false
	}
	name, valid := in.constString(call.Args[0])
	if !valid {
		in.errorAt("P6_RELATION_DIMENSION_NAME", "relation dimension names must be constant strings", call.Args[0])
		return ir.RelationDimensionContractIR{}, false
	}
	path, terminal, start, valid := in.relationDimensionPath(call.Args[1])
	if !valid || start != in.model.ID {
		in.errorAt("P6_RELATION_DIMENSION_PATH", "relation dimension path must start at this model and contain only forward to-one hops", call.Args[1])
		return ir.RelationDimensionContractIR{}, false
	}
	return ir.RelationDimensionContractIR{Name: name, Path: path, TerminalField: terminal}, true
}

func (in *interpreter) relationDimensionPath(expression ast.Expr) ([]ir.RelationID, ir.FieldID, ir.ModelID, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		return nil, "", "", false
	}
	switch in.callOperation(call) {
	case "DimensionField":
		if len(call.Args) != 1 {
			return nil, "", "", false
		}
		symbol, valid := in.resolveAnyHandle(call.Args[0])
		return []ir.RelationID{}, symbol.FieldID, symbol.ModelID, valid && symbol.Kind == "field"
	case "Via":
		if len(call.Args) != 2 {
			return nil, "", "", false
		}
		relationSymbol, valid := in.resolveAnyHandle(call.Args[0])
		if !valid || relationSymbol.Kind != "relation" {
			return nil, "", "", false
		}
		tail, terminal, tailStart, valid := in.relationDimensionPath(call.Args[1])
		if !valid {
			return nil, "", "", false
		}
		for _, relation := range in.config.Compilation.Model.Relations {
			if relation.ID == relationSymbol.RelationID && relation.SourceModel == relationSymbol.ModelID && relation.TargetModel == tailStart {
				return append([]ir.RelationID{relation.ID}, tail...), terminal, relation.SourceModel, true
			}
		}
	}
	return nil, "", "", false
}

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
	case "GraphQL":
		if scope != ir.ProviderScopePortable {
			in.errorAt("P5_GRAPHQL_PROVIDER_SCOPE", "GraphQL contract options cannot be provider-scoped", call)
			return
		}
		in.evalGraphQL(call)
	case "Subscriptions":
		if scope != ir.ProviderScopePortable || len(call.Args) != 0 {
			in.errorAt("P7_SUBSCRIPTIONS_DECLARATION", "Subscriptions takes no arguments and cannot be provider-scoped", call)
			return
		}
		if in.graphql == nil {
			in.graphql = &graphqlcontract.ModelPatch{ModelID: in.model.ID, Span: in.span(call)}
		}
		if in.graphql.Subscriptions != nil {
			in.errorAt("P7_SUBSCRIPTIONS_DUPLICATE", "Subscriptions may be declared only once in GolemModel", call)
			return
		}
		enabled := true
		in.graphql.Subscriptions = &enabled
	case "Analytics":
		if scope != ir.ProviderScopePortable {
			in.errorAt("P6_ANALYTICS_PROVIDER_SCOPE", "Analytics cannot be provider-scoped", call)
			return
		}
		in.evalAnalytics(call)
	case "ScopedReads":
		if scope != ir.ProviderScopePortable || len(call.Args) != 0 {
			in.errorAt("P6_SCOPED_READS_DECLARATION", "ScopedReads takes no arguments and cannot be provider-scoped", call)
			return
		}
		if in.analytics == nil {
			in.analytics = &analyticscontract.ModelPatch{ModelID: in.model.ID, Span: in.span(call)}
		}
		in.analytics.ScopedReads = true
	case "SemanticIndex":
		if scope != ir.ProviderScopePortable {
			in.errorAt("P9_SEMANTIC_INDEX_PROVIDER_SCOPE", "SemanticIndex is portable and cannot be provider-scoped", call)
			return
		}
		in.evalSemanticIndex(call)
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

func (in *interpreter) evalSemanticIndex(call *ast.CallExpr) {
	if len(call.Args) < 3 {
		in.errorAt("P9_SEMANTIC_INDEX_ARITY", "SemanticIndex requires a name, embedding space, and at least one text field", call)
		return
	}
	name, valid := in.constString(call.Args[0])
	if !valid || !semanticIndexNamePattern.MatchString(name) {
		in.errorAt("P9_SEMANTIC_INDEX_NAME", "semantic index name must be a constant matching [a-z][a-z0-9_-]{0,62}", call.Args[0])
		return
	}
	if in.semanticIndexes[name] {
		in.errorAt("P9_SEMANTIC_INDEX_DUPLICATE", "semantic index names must be unique within a model", call.Args[0])
		return
	}
	spaceName, valid := in.constString(call.Args[1])
	if !valid {
		in.errorAt("P9_SEMANTIC_INDEX_SPACE", "SemanticIndex requires a constant embedding-space name", call.Args[1])
		return
	}
	dimensionsByProvider := map[ir.Provider]uint16{}
	for _, extension := range in.config.Compilation.Model.Extensions {
		if extension.Kind != semanticcontract.SpaceKind || extension.Version != semanticcontract.Version {
			continue
		}
		space, err := semanticcontract.DecodeSpace(extension.Payload)
		if err == nil && space.Name == spaceName {
			dimensionsByProvider[extension.Provider] = space.Dimensions
		}
	}
	var dimensions uint16
	for _, provider := range in.config.Compilation.Model.Providers {
		value, exists := dimensionsByProvider[provider]
		if !exists {
			in.errorAt("P9_SEMANTIC_INDEX_SPACE_UNKNOWN", "SemanticIndex references an undeclared embedding space", call.Args[1])
			return
		}
		if dimensions == 0 {
			dimensions = value
		} else if dimensions != value {
			in.errorAt("P9_SEMANTIC_INDEX_SPACE_DIVERGENT", "embedding-space dimensions must agree across providers", call.Args[1])
			return
		}
	}
	fields := make([]string, 0, len(call.Args)-2)
	seenFields := map[ir.FieldID]bool{}
	for _, argument := range call.Args[2:] {
		symbol, resolved := in.resolveHandle(argument)
		if !resolved || symbol.Kind != "field" || symbol.ModelID != in.model.ID {
			in.errorAt("P9_SEMANTIC_INDEX_FIELD", "SemanticIndex accepts only text field handles of this model", argument)
			continue
		}
		if seenFields[symbol.FieldID] {
			in.errorAt("P9_SEMANTIC_INDEX_FIELD_DUPLICATE", "SemanticIndex lists a field more than once", argument)
			continue
		}
		field, exists := semanticField(in.model, symbol.FieldID)
		if !exists || field.Scalar == nil || field.Scalar.Type.Kind != ir.TypeString {
			in.errorAt("P9_SEMANTIC_INDEX_FIELD_TYPE", "SemanticIndex fields must have logical type String", argument)
			continue
		}
		seenFields[symbol.FieldID] = true
		fields = append(fields, string(symbol.FieldID))
	}
	if len(fields) != len(call.Args)-2 {
		return
	}
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: name, Space: spaceName, Dimensions: dimensions, Fields: fields, Metric: "cosine"})
	if err != nil {
		in.errorAt("P9_SEMANTIC_INDEX_ENCODE", err.Error(), call)
		return
	}
	for _, provider := range in.config.Compilation.Model.Providers {
		canonical := ir.OwnedIdentity(string(in.model.ID), semanticcontract.IndexKind+"\x00"+string(provider)+"\x00"+name)
		identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, canonical, in.span(call))
		if diagnostic != nil {
			in.diagnostics = append(in.diagnostics, *diagnostic)
			continue
		}
		in.extensions = append(in.extensions, ir.ProviderExtensionIR{
			ID:       ir.ExtensionIDFrom(identity),
			Provider: provider,
			Version:  semanticcontract.Version,
			Owner:    ir.ObjectID(in.model.ID),
			Kind:     semanticcontract.IndexKind,
			Payload:  payload,
		})
	}
	in.semanticIndexes[name] = true
	in.semanticNodes = append(in.semanticNodes, call)
}

func (in *interpreter) finishSemantic() {
	// Primary keys authored as struct tags are linked after method
	// interpretation. Semantic identity validation therefore belongs to the
	// completed compilation, where both tag- and method-authored keys exist.
}

func semanticIdentityType(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeString, ir.TypeUUID, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64:
		return true
	default:
		return false
	}
}

func semanticField(model ir.ModelDeclIR, fieldID ir.FieldID) (ir.FieldIR, bool) {
	for _, field := range model.Fields {
		if field.ID == fieldID {
			return field, true
		}
	}
	return ir.FieldIR{}, false
}

func (in *interpreter) evalGraphQL(call *ast.CallExpr) {
	if in.graphqlDeclared {
		in.errorAt("P5_GRAPHQL_MODEL_DUPLICATE", "GraphQL may be declared only once in GolemModel", call)
		return
	}
	in.graphqlDeclared = true
	patch := graphqlcontract.ModelPatch{ModelID: in.model.ID, Span: in.span(call)}
	if in.graphql != nil {
		patch.Subscriptions = in.graphql.Subscriptions
	}
	seen := map[string]bool{}
	for _, expression := range call.Args {
		option, ok := unparen(expression).(*ast.CallExpr)
		if !ok {
			in.errorAt("P5_GRAPHQL_OPTION", "GraphQL options must be direct constructor calls", expression)
			continue
		}
		operation := in.callOperation(option)
		if seen[operation] {
			in.errorAt("P5_GRAPHQL_OPTION_DUPLICATE", operation+" may be declared only once", option)
			continue
		}
		seen[operation] = true
		switch operation {
		case "GraphQLOperations":
			values := make([]ir.Operation, 0, len(option.Args))
			for _, argument := range option.Args {
				value, valid := graphqlOperation(in.constantName(argument))
				if !valid {
					in.errorAt("P5_GRAPHQL_OPERATION", "GraphQLOperations accepts only typed golem GraphQL operation constants", argument)
					continue
				}
				values = append(values, value)
			}
			patch.Operations = &values
		case "GraphQLPlural":
			if len(option.Args) != 1 {
				in.errorAt("P5_GRAPHQL_PLURAL_ARITY", "GraphQLPlural requires one constant string", option)
				continue
			}
			value, valid := in.constString(option.Args[0])
			if !valid {
				in.errorAt("P5_GRAPHQL_PLURAL", "GraphQLPlural requires one constant string", option.Args[0])
				continue
			}
			patch.Plural = &value
		case "GraphQLRoots":
			if len(option.Args) != 1 {
				in.errorAt("P5_GRAPHQL_ROOTS_ARITY", "GraphQLRoots requires one GraphQLRootNames literal", option)
				continue
			}
			roots, valid := in.graphqlRoots(option.Args[0])
			if valid {
				patch.Roots = &roots
			}
		case "GraphQLPageSizes":
			if len(option.Args) != 2 {
				in.errorAt("P5_GRAPHQL_PAGE_ARITY", "GraphQLPageSizes requires default and maximum sizes", option)
				continue
			}
			defaultSize, left := in.uint32Constant(option.Args[0])
			maximumSize, right := in.uint32Constant(option.Args[1])
			if !left || !right {
				in.errorAt("P5_GRAPHQL_PAGE_VALUE", "GraphQL page sizes must be positive uint32 integer constants", option)
				continue
			}
			patch.DefaultPage, patch.MaximumPage = &defaultSize, &maximumSize
		case "GraphQLHidden":
			if len(option.Args) != 0 {
				in.errorAt("P5_GRAPHQL_HIDDEN_ARITY", "GraphQLHidden takes no arguments", option)
				continue
			}
			patch.Hidden = true
		case "GraphQLHookOwned":
			if len(option.Args) == 0 {
				in.errorAt("P8_GRAPHQL_HOOK_OWNED_ARITY", "GraphQLHookOwned requires at least one generated scalar field handle", option)
				continue
			}
			seenFields := map[ir.FieldID]bool{}
			for _, argument := range option.Args {
				symbol, valid := in.resolveHandle(argument)
				if !valid || symbol.Kind != "field" || symbol.ModelID != in.model.ID {
					in.errorAt("P8_GRAPHQL_HOOK_OWNED_FIELD", "GraphQLHookOwned accepts only scalar field handles of this model", argument)
					continue
				}
				if seenFields[symbol.FieldID] {
					in.errorAt("P8_GRAPHQL_HOOK_OWNED_DUPLICATE", "GraphQLHookOwned lists a field more than once", argument)
					continue
				}
				seenFields[symbol.FieldID] = true
				patch.HookOwnedCreateFields = append(patch.HookOwnedCreateFields, symbol.FieldID)
			}
		default:
			in.errorAt("P5_GRAPHQL_OPTION", "call is not a recognized GraphQL option constructor", expression)
		}
	}
	in.graphql = &patch
}

func graphqlOperation(name string) (ir.Operation, bool) {
	values := map[string]ir.Operation{
		"GraphQLFindOne": ir.OperationFindOne, "GraphQLFindMany": ir.OperationFindMany,
		"GraphQLCreate": ir.OperationCreate, "GraphQLUpdate": ir.OperationUpdate,
		"GraphQLUpsert": ir.OperationUpsert, "GraphQLDelete": ir.OperationDelete,
		"GraphQLUpdateMany": ir.OperationUpdateMany, "GraphQLDeleteMany": ir.OperationDeleteMany,
		"GraphQLAggregate": ir.OperationAggregate, "GraphQLGroupBy": ir.OperationGroupBy,
		"GraphQLRelationGroupBy": ir.OperationRelationGroupBy,
	}
	value, ok := values[name]
	return value, ok
}

func (in *interpreter) uint32Constant(expression ast.Expr) (uint32, bool) {
	value := in.pkg.TypesInfo.Types[expression].Value
	if value == nil || value.Kind() != constant.Int {
		return 0, false
	}
	integer, exact := constant.Uint64Val(value)
	return uint32(integer), exact && integer > 0 && integer <= uint64(^uint32(0))
}

func (in *interpreter) graphqlRoots(expression ast.Expr) (ir.GraphQLRootNamesIR, bool) {
	literal, ok := unparen(expression).(*ast.CompositeLit)
	if !ok {
		in.errorAt("P5_GRAPHQL_ROOTS_LITERAL", "GraphQLRoots requires a direct golem.GraphQLRootNames struct literal", expression)
		return ir.GraphQLRootNamesIR{}, false
	}
	var result ir.GraphQLRootNamesIR
	destinations := map[string]*string{
		"FindOne": &result.FindOne, "FindMany": &result.FindMany, "Create": &result.Create,
		"Update": &result.Update, "Upsert": &result.Upsert, "Delete": &result.Delete,
		"UpdateMany": &result.UpdateMany, "DeleteMany": &result.DeleteMany,
		"Aggregate": &result.Aggregate, "GroupBy": &result.GroupBy,
		"RelationGroupBy": &result.RelationGroupBy,
		"Events":          &result.Events,
	}
	seen := map[string]bool{}
	for _, element := range literal.Elts {
		item, ok := element.(*ast.KeyValueExpr)
		if !ok {
			in.errorAt("P5_GRAPHQL_ROOTS_KEYED", "GraphQLRootNames must use keyed fields", element)
			return result, false
		}
		key, ok := item.Key.(*ast.Ident)
		if !ok || destinations[key.Name] == nil {
			in.errorAt("P5_GRAPHQL_ROOTS_FIELD", "GraphQLRootNames contains an unknown field", item.Key)
			return result, false
		}
		if seen[key.Name] {
			in.errorAt("P5_GRAPHQL_ROOTS_DUPLICATE", key.Name+" is assigned more than once", item.Key)
			return result, false
		}
		seen[key.Name] = true
		value, valid := in.constString(item.Value)
		if !valid {
			in.errorAt("P5_GRAPHQL_ROOTS_VALUE", "GraphQL root names must be constant strings", item.Value)
			return result, false
		}
		*destinations[key.Name] = value
	}
	return result, true
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
