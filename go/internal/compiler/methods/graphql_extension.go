package methods

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

func (in *interpreter) interpretGraphQLModel() {
	object, _ := in.pkg.Types.Scope().Lookup(in.model.Go.Name).(*types.TypeName)
	model, _ := typeNameType(object).(*types.Named)
	if model == nil {
		return
	}
	type candidate struct {
		declaration *ast.FuncDecl
		function    *types.Func
	}
	var candidates []candidate
	for _, file := range in.pkg.Syntax {
		for _, declaration := range file.Decls {
			functionDecl, ok := declaration.(*ast.FuncDecl)
			if !ok || functionDecl.Name.Name != "DefineGraphQL" || functionDecl.Recv == nil {
				continue
			}
			function, _ := in.pkg.TypesInfo.Defs[functionDecl.Name].(*types.Func)
			signature, _ := function.Type().(*types.Signature)
			if signature == nil || signature.Recv() == nil || !sameNamedReceiver(signature.Recv().Type(), model) {
				continue
			}
			candidates = append(candidates, candidate{declaration: functionDecl, function: function})
		}
	}
	if len(candidates) == 0 {
		return
	}
	if len(candidates) != 1 {
		for _, candidate := range candidates {
			in.errorAt("P5_GRAPHQL_DEFINE_DUPLICATE", fmt.Sprintf("model %s has multiple DefineGraphQL declarations", in.model.Go.Name), candidate.declaration)
		}
		return
	}
	declaration := candidates[0].declaration
	signature := candidates[0].function.Type().(*types.Signature)
	if _, pointer := signature.Recv().Type().(*types.Pointer); pointer || signature.Params().Len() != 1 || signature.Results().Len() != 0 || !isGraphQLModelPointer(signature.Params().At(0).Type(), model, in.config.GolemImportPath) {
		in.errorAt("P5_GRAPHQL_DEFINE_SIGNATURE", fmt.Sprintf("DefineGraphQL must have signature func (%s) DefineGraphQL(*golem.GraphQLModel[%s])", in.model.Go.Name, in.model.Go.Name), declaration.Type)
		return
	}
	if declaration.Body == nil {
		in.errorAt("P5_GRAPHQL_DEFINE_BODY", "DefineGraphQL requires a direct declaration body", declaration)
		return
	}
	parameter := signature.Params().At(0)
	for _, statement := range declaration.Body.List {
		expressionStatement, ok := statement.(*ast.ExprStmt)
		if !ok {
			in.errorAt("P5_GRAPHQL_DEFINE_STATEMENT", "DefineGraphQL permits only direct computed-field declaration calls", statement)
			continue
		}
		call, ok := unparen(expressionStatement.X).(*ast.CallExpr)
		if !ok {
			in.errorAt("P5_GRAPHQL_DEFINE_STATEMENT", "DefineGraphQL permits only direct computed-field declaration calls", expressionStatement)
			continue
		}
		switch in.callOperation(call) {
		case "ComputedField":
			in.evalComputedField(call, parameter, model, false, false)
		case "BatchedComputedField":
			in.evalComputedField(call, parameter, model, true, false)
		case "BatchedComputedFieldWithCacheKey":
			in.evalComputedField(call, parameter, model, true, true)
		default:
			in.errorAt("P5_GRAPHQL_DEFINE_CALL", "DefineGraphQL accepts only direct golem computed-field declarations", call)
		}
	}
}

func interpretGraphQLSchema(config Config, loaded loaded) ([]graphqlextension.CustomOperationDeclaration, []ir.Diagnostic) {
	packagePath := config.Compilation.Model.Schema.PackagePath
	if packagePath == "" {
		return nil, nil
	}
	pkg := loaded.packages[packagePath]
	if pkg == nil {
		return nil, []ir.Diagnostic{ir.NewError("P5_GRAPHQL_SCHEMA_PACKAGE", fmt.Sprintf("schema package %q was not loaded", packagePath), ir.SourceSpan{})}
	}
	vocab, diagnostics := buildVocabulary(pkg, config.GolemImportPath)
	entry := &interpreter{config: config, pkg: pkg, vocab: vocab}
	var declarations []*ast.FuncDecl
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "DefineGraphQL" {
				declarations = append(declarations, function)
			}
		}
	}
	if len(declarations) == 0 {
		return nil, diagnostics
	}
	if len(declarations) != 1 {
		for _, declaration := range declarations {
			entry.errorAt("P5_CUSTOM_DEFINE_DUPLICATE", "schema package has multiple DefineGraphQL declarations", declaration)
		}
		return nil, append(diagnostics, entry.diagnostics...)
	}
	declaration := declarations[0]
	function, _ := pkg.TypesInfo.Defs[declaration.Name].(*types.Func)
	signature, _ := function.Type().(*types.Signature)
	if signature == nil || signature.Params().Len() != 1 || signature.Results().Len() != 0 || !isGraphQLSchemaPointer(signature.Params().At(0).Type(), config.GolemImportPath) {
		entry.errorAt("P5_CUSTOM_DEFINE_SIGNATURE", "schema DefineGraphQL must have signature func(*golem.GraphQLSchema)", declaration.Type)
		return nil, append(diagnostics, entry.diagnostics...)
	}
	if declaration.Body == nil {
		entry.errorAt("P5_CUSTOM_DEFINE_BODY", "schema DefineGraphQL requires a direct declaration body", declaration)
		return nil, append(diagnostics, entry.diagnostics...)
	}
	parameter := signature.Params().At(0)
	var result []graphqlextension.CustomOperationDeclaration
	for _, statement := range declaration.Body.List {
		expressionStatement, ok := statement.(*ast.ExprStmt)
		if !ok {
			entry.errorAt("P5_CUSTOM_DEFINE_STATEMENT", "schema DefineGraphQL permits only direct Query or Mutation calls", statement)
			continue
		}
		call, ok := unparen(expressionStatement.X).(*ast.CallExpr)
		if !ok {
			entry.errorAt("P5_CUSTOM_DEFINE_STATEMENT", "schema DefineGraphQL permits only direct Query or Mutation calls", expressionStatement)
			continue
		}
		kind := ir.CustomOperationKind("")
		switch entry.callOperation(call) {
		case "Query":
			kind = ir.CustomOperationQuery
		case "Mutation":
			kind = ir.CustomOperationMutation
		default:
			entry.errorAt("P5_CUSTOM_DEFINE_CALL", "schema DefineGraphQL accepts only direct golem.Query or golem.Mutation calls", call)
			continue
		}
		if operation, valid := entry.evalCustomOperation(call, parameter, kind); valid {
			result = append(result, operation)
		}
	}
	diagnostics = append(diagnostics, entry.diagnostics...)
	return result, diagnostics
}

func (in *interpreter) evalComputedField(call *ast.CallExpr, parameter *types.Var, model *types.Named, batched, withCacheKey bool) {
	minimum := 4
	if batched {
		minimum = 6
		if withCacheKey {
			minimum = 7
		}
	}
	if len(call.Args) < minimum || !in.isParameter(call.Args[0], parameter) {
		in.errorAt("P5_COMPUTED_ARITY", "computed declaration has an invalid destination or argument count", call)
		return
	}
	name, valid := in.constString(call.Args[1])
	if !valid {
		in.errorAt("P5_COMPUTED_NAME", "computed field name must be a constant string", call.Args[1])
		return
	}
	resultType, resultWitness, valid := in.graphQLResultType(call.Args[2])
	if !valid {
		return
	}
	resolverIndex := 3
	var callable ir.AttachedMethodIR
	var signature *types.Signature
	var arguments []ir.GraphQLArgumentContractIR
	var dependencies []ir.FieldID
	var batch *ir.ComputedBatchContractIR
	if !batched {
		callable, signature, valid = in.callable(call.Args[resolverIndex], "computed")
		if !valid {
			return
		}
		if !validComputedCallable(signature, model, resultWitness, in.config.GolemImportPath) {
			in.errorAt("P5_COMPUTED_SIGNATURE", "computed resolver must be func(context.Context, golem.Row[Model], Args) (Result, error)", call.Args[resolverIndex])
			return
		}
		arguments, valid = in.graphQLArguments(signature.Params().At(2).Type(), false, call.Args[resolverIndex])
		if !valid {
			return
		}
		if callable.Receiver != in.model.Go {
			in.errorAt("P5_COMPUTED_RECEIVER", "computed resolver must be an exact method on the extended model", call.Args[resolverIndex])
			return
		}
		dependencies = in.computedDependencies(call.Args[4:])
	} else {
		keySymbol, keyValid := in.resolveHandle(call.Args[3])
		if !keyValid || keySymbol.Kind != modelcodegen.SymbolField {
			in.errorAt("P5_COMPUTED_BATCH_KEY", "batch key must be a generated scalar handle for this model", call.Args[3])
			return
		}
		callable, signature, valid = in.callable(call.Args[4], "computedBatch")
		if !valid {
			return
		}
		keyType := in.modelFieldType(model, keySymbol.Name)
		if keyType == nil || !validBatchCallable(signature, keyType, resultWitness, in.config.GolemImportPath) {
			in.errorAt("P5_COMPUTED_BATCH_SIGNATURE", "batch loader must be func(context.Context, []Key, Args) (map[Key]Result, error)", call.Args[4])
			return
		}
		arguments, valid = in.graphQLArguments(signature.Params().At(2).Type(), false, call.Args[4])
		if !valid {
			return
		}
		limitIndex := 5
		var cacheKey *ir.AttachedMethodIR
		if withCacheKey {
			codec, codecSignature, codecValid := in.callable(call.Args[5], "computedCacheKey")
			if !codecValid || !validCacheKeyCallable(codecSignature, keyType) {
				in.errorAt("P5_COMPUTED_CACHE_KEY_SIGNATURE", "cache-key codec must be func(Key) (string, error)", call.Args[5])
				return
			}
			cacheKey = &codec
			limitIndex = 6
		}
		limit, limitValid := in.uint32Constant(call.Args[limitIndex])
		if !limitValid {
			in.errorAt("P5_COMPUTED_BATCH_LIMIT", "maximum batch size must be a positive uint32 constant", call.Args[limitIndex])
			return
		}
		batch = &ir.ComputedBatchContractIR{KeyField: keySymbol.FieldID, Loader: callable, CacheKey: cacheKey, MaxBatchSize: limit}
		resolverIndex = 4
		dependencies = in.computedDependencies(call.Args[limitIndex+1:])
	}
	identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, ir.OwnedIdentity(string(in.model.ID), "graphql-computed\x00"+name), in.span(call))
	if diagnostic != nil {
		in.diagnostics = append(in.diagnostics, *diagnostic)
		return
	}
	modelID := in.model.ID
	callable.ModelID = &modelID
	if batch != nil {
		batch.Loader.ModelID = &modelID
	}
	in.computed = append(in.computed, graphqlextension.ComputedDeclaration{ModelID: modelID, Span: in.span(call), Field: ir.ComputedFieldContractIR{
		ExtensionID: ir.ExtensionIDFrom(identity), Name: name, Result: resultType, Arguments: arguments,
		Requires: dependencies, Resolver: callable, Batch: batch,
	}})
}

func (in *interpreter) evalCustomOperation(call *ast.CallExpr, parameter *types.Var, kind ir.CustomOperationKind) (graphqlextension.CustomOperationDeclaration, bool) {
	if len(call.Args) != 3 || !in.isParameter(call.Args[0], parameter) {
		in.errorAt("P5_CUSTOM_ARITY", "custom operation requires the schema destination, name, and resolver", call)
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	name, valid := in.constString(call.Args[1])
	if !valid {
		in.errorAt("P5_CUSTOM_NAME", "custom operation name must be a constant string", call.Args[1])
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	callable, signature, valid := in.callable(call.Args[2], "custom"+string(kind))
	if !valid {
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	if callable.Receiver != (ir.GoNamedTypeIR{}) || callable.PackagePath != in.pkg.PkgPath {
		in.errorAt("P5_CUSTOM_RESOLVER", "custom operation resolver must be a package function in the schema package", call.Args[2])
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	if !validCustomCallable(signature, in.pkg.PkgPath) {
		in.errorAt("P5_CUSTOM_SIGNATURE", "custom resolver must be func(context.Context, *Caller[Principal], Args) (Result, error)", call.Args[2])
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	arguments, valid := in.graphQLArguments(signature.Params().At(2).Type(), true, call.Args[2])
	if !valid {
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	result, valid := in.graphQLTypeFromGo(signature.Results().At(0).Type(), graphQLCustomResult)
	if !valid {
		in.errorAt("P5_CUSTOM_RESULT_TYPE", "custom resolver result is not a registered scalar, enum, model row, or list", call.Args[2])
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, ir.OwnedIdentity(string(in.config.Compilation.Model.Schema.ID), "graphql-custom\x00"+string(kind)+"\x00"+name), in.span(call))
	if diagnostic != nil {
		in.diagnostics = append(in.diagnostics, *diagnostic)
		return graphqlextension.CustomOperationDeclaration{}, false
	}
	return graphqlextension.CustomOperationDeclaration{Span: in.span(call), Operation: ir.CustomOperationContractIR{
		ExtensionID: ir.ExtensionIDFrom(identity), Operation: kind, Name: name, Arguments: arguments, Result: result,
		Resolver: callable, Capability: ir.CustomOperationCallerOnly,
	}}, true
}

func (in *interpreter) computedDependencies(expressions []ast.Expr) []ir.FieldID {
	var result []ir.FieldID
	for _, expression := range expressions {
		call, ok := unparen(expression).(*ast.CallExpr)
		if !ok || in.callOperation(call) != "Requires" || len(call.Args) == 0 {
			in.errorAt("P5_COMPUTED_OPTION", "computed options must be direct non-empty golem.Requires calls", expression)
			continue
		}
		for _, argument := range call.Args {
			symbol, valid := in.resolveHandle(argument)
			if !valid || symbol.Kind != modelcodegen.SymbolField && symbol.Kind != modelcodegen.SymbolRelation {
				in.errorAt("P5_COMPUTED_DEPENDENCY", "computed dependencies must be generated fields or relations for this model", argument)
				continue
			}
			result = append(result, symbol.FieldID)
		}
	}
	return result
}

func (in *interpreter) graphQLResultType(expression ast.Expr) (ir.GraphQLTypeIR, types.Type, bool) {
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok {
		in.errorAt("P5_COMPUTED_RESULT_TYPE", "computed result type must be a direct closed GraphQL type constructor", expression)
		return ir.GraphQLTypeIR{}, nil, false
	}
	if in.callOperation(call) == "GraphQLType.NonNull" {
		if len(call.Args) != 0 {
			in.errorAt("P5_COMPUTED_RESULT_TYPE", "GraphQL NonNull takes no arguments", call)
			return ir.GraphQLTypeIR{}, nil, false
		}
		value, witness, valid := in.graphQLResultType(in.receiver(call))
		if valid {
			value.Nullable = false
		}
		return value, witness, valid
	}
	operation := in.callOperation(call)
	if operation == "GraphQLList" {
		if len(call.Args) != 1 {
			in.errorAt("P5_COMPUTED_RESULT_TYPE", "GraphQLList requires one closed element type", call)
			return ir.GraphQLTypeIR{}, nil, false
		}
		element, _, valid := in.graphQLResultType(call.Args[0])
		witness := graphQLTypeWitness(in.pkg.TypesInfo.TypeOf(expression), in.config.GolemImportPath)
		if !valid || witness == nil {
			return ir.GraphQLTypeIR{}, nil, false
		}
		return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Nullable: true, Element: &element}, witness, true
	}
	scalars := map[string]string{
		"GraphQLBoolean": "Boolean", "GraphQLInt": "Int", "GraphQLFloat": "Float", "GraphQLString": "String",
		"GraphQLBigInt": "BigInt", "GraphQLDecimal": "Decimal", "GraphQLUUID": "UUID", "GraphQLDate": "Date",
		"GraphQLTime": "Time", "GraphQLDateTime": "DateTime", "GraphQLBytes": "Bytes", "GraphQLJSON": "JSON",
	}
	witness := graphQLTypeWitness(in.pkg.TypesInfo.TypeOf(expression), in.config.GolemImportPath)
	if name := scalars[operation]; name != "" && len(call.Args) == 0 && witness != nil {
		return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: name, Nullable: true}, witness, true
	}
	if (operation == "GraphQLObject" || operation == "GraphQLEnum") && len(call.Args) == 0 && witness != nil {
		value, valid := in.graphQLTypeFromGo(witness, graphQLCustomResult)
		if valid && (operation != "GraphQLObject" || value.Kind == ir.GraphQLTypeModel) && (operation != "GraphQLEnum" || value.Kind == ir.GraphQLTypeEnum) {
			value.Nullable = true
			return value, witness, true
		}
	}
	in.errorAt("P5_COMPUTED_RESULT_TYPE", "computed result type is not a recognized closed GraphQL constructor", expression)
	return ir.GraphQLTypeIR{}, nil, false
}

func graphQLTypeWitness(value types.Type, golemPath string) types.Type {
	named, _ := value.(*types.Named)
	if named == nil || named.Origin().Obj().Pkg() == nil || named.Origin().Obj().Pkg().Path() != golemPath || named.Origin().Obj().Name() != "GraphQLType" || named.TypeArgs().Len() != 1 {
		return nil
	}
	return named.TypeArgs().At(0)
}

type graphQLGoTypeUse uint8

const (
	graphQLComputedArgument graphQLGoTypeUse = iota
	graphQLCustomArgument
	graphQLCustomResult
)

func (in *interpreter) graphQLArguments(value types.Type, custom bool, node ast.Node) ([]ir.GraphQLArgumentContractIR, bool) {
	named, _ := value.(*types.Named)
	structure, _ := value.Underlying().(*types.Struct)
	if named == nil || structure == nil {
		in.errorAt("P5_EXTENSION_ARGUMENT_STRUCT", "resolver arguments must be a named struct", node)
		return nil, false
	}
	use := graphQLComputedArgument
	if custom {
		use = graphQLCustomArgument
	}
	result := make([]ir.GraphQLArgumentContractIR, 0, structure.NumFields())
	seen := map[string]bool{}
	valid := true
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		if !field.Exported() || field.Embedded() {
			in.errorAt("P5_EXTENSION_ARGUMENT_FIELD", fmt.Sprintf("argument struct field %s must be exported and non-embedded", field.Name()), node)
			valid = false
			continue
		}
		name, tagValid := graphQLArgumentName(field.Name(), structure.Tag(index))
		if !tagValid {
			in.errorAt("P5_EXTENSION_ARGUMENT_TAG", fmt.Sprintf("argument struct field %s has an invalid golem GraphQL tag", field.Name()), node)
			valid = false
			continue
		}
		if seen[name] {
			in.errorAt("P5_EXTENSION_ARGUMENT_DUPLICATE", fmt.Sprintf("argument struct maps multiple fields to %q", name), node)
			valid = false
			continue
		}
		seen[name] = true
		fieldType, typeValid := in.graphQLTypeFromGo(field.Type(), use)
		if !typeValid {
			in.errorAt("P5_EXTENSION_ARGUMENT_TYPE", fmt.Sprintf("argument %s has an unrecognized GraphQL type", name), node)
			valid = false
			continue
		}
		result = append(result, ir.GraphQLArgumentContractIR{Name: name, Type: fieldType})
	}
	return result, valid
}

func graphQLArgumentName(goName, rawTag string) (string, bool) {
	name := graphqlcontract.LowerCamel(goName)
	value, exists := reflect.StructTag(rawTag).Lookup("golem")
	if !exists || value == "" {
		return name, true
	}
	seen := false
	for _, part := range strings.Split(value, ",") {
		if strings.HasPrefix(part, "graphql=") && !seen && len(part) > len("graphql=") {
			name, seen = strings.TrimPrefix(part, "graphql="), true
			continue
		}
		return "", false
	}
	return name, seen
}

func (in *interpreter) graphQLTypeFromGo(value types.Type, use graphQLGoTypeUse) (ir.GraphQLTypeIR, bool) {
	value = types.Unalias(value)
	nullable := false
	if pointer, ok := value.(*types.Pointer); ok {
		nullable, value = true, types.Unalias(pointer.Elem())
	}
	if slice, ok := value.(*types.Slice); ok {
		if basic, bytes := slice.Elem().Underlying().(*types.Basic); bytes && basic.Kind() == types.Byte {
			return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Bytes", Nullable: nullable}, true
		}
		element, valid := in.graphQLTypeFromGo(slice.Elem(), use)
		if !valid {
			return ir.GraphQLTypeIR{}, false
		}
		return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Nullable: nullable, Element: &element}, true
	}
	if basic, ok := value.(*types.Basic); ok {
		name := map[types.BasicKind]string{types.Bool: "Boolean", types.Int16: "Int", types.Int32: "Int", types.Int64: "BigInt", types.Float32: "Float", types.Float64: "Float", types.String: "String"}[basic.Kind()]
		if name != "" {
			return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: name, Nullable: nullable}, true
		}
	}
	named, _ := value.(*types.Named)
	if named == nil || named.Obj().Pkg() == nil {
		return ir.GraphQLTypeIR{}, false
	}
	packagePath, name := named.Obj().Pkg().Path(), named.Obj().Name()
	if packagePath == "time" && name == "Time" {
		return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "DateTime", Nullable: nullable}, true
	}
	if packagePath == in.config.GolemImportPath {
		scalar := map[string]string{"UUID": "UUID", "Decimal": "Decimal", "Date": "Date", "Time": "Time", "JSON": "JSON"}[named.Origin().Obj().Name()]
		if scalar != "" {
			return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: scalar, Nullable: nullable}, true
		}
		origin := named.Origin().Obj().Name()
		if named.TypeArgs().Len() == 1 {
			modelName, modelValid := in.graphQLModelName(named.TypeArgs().At(0))
			switch origin {
			case "Row":
				if modelValid && use == graphQLCustomResult {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: modelName, Nullable: nullable}, true
				}
			case "Predicate":
				if modelValid && use == graphQLCustomArgument {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypePredicate, Name: modelName, Nullable: nullable}, true
				}
			case "UniqueSelectorValue", "MutationTarget":
				if modelValid && use == graphQLCustomArgument {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeSelector, Name: modelName, Nullable: nullable}, true
				}
			case "CreateInput":
				if modelValid && use == graphQLCustomArgument {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeCreateInput, Name: modelName, Nullable: nullable}, true
				}
			case "UpdateInput":
				if modelValid && use == graphQLCustomArgument {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeUpdateInput, Name: modelName, Nullable: nullable}, true
				}
			case "UpdateManyInput":
				if modelValid && use == graphQLCustomArgument {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeUpdateManyInput, Name: modelName, Nullable: nullable}, true
				}
			}
		}
	}
	for _, enum := range in.config.Compilation.Model.Enums {
		if enum.Go.PackagePath == packagePath && enum.Go.Name == name {
			for _, contract := range in.config.Compilation.Contract.Enums {
				if contract.EnumID == enum.ID {
					return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeEnum, Name: contract.GraphQLName, Nullable: nullable}, true
				}
			}
		}
	}
	if use == graphQLCustomArgument {
		for _, model := range in.config.Compilation.Model.Models {
			if model.Go.PackagePath != packagePath {
				continue
			}
			modelName := in.graphQLModelContractName(model.ID)
			switch name {
			case model.Go.Name + "CreateInput":
				return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeCreateInput, Name: modelName, Nullable: nullable}, modelName != ""
			case model.Go.Name + "UpdateInput":
				return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeUpdateInput, Name: modelName, Nullable: nullable}, modelName != ""
			case model.Go.Name + "UpdateManyInput":
				return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeUpdateManyInput, Name: modelName, Nullable: nullable}, modelName != ""
			}
		}
	}
	return ir.GraphQLTypeIR{}, false
}

func (in *interpreter) graphQLModelContractName(modelID ir.ModelID) string {
	for _, contract := range in.config.Compilation.Contract.Models {
		if contract.ModelID == modelID && contract.Exposed {
			return contract.GraphQLName
		}
	}
	return ""
}

func (in *interpreter) graphQLModelName(value types.Type) (string, bool) {
	named, _ := value.(*types.Named)
	if named == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	for _, model := range in.config.Compilation.Model.Models {
		if model.Go.PackagePath == named.Obj().Pkg().Path() && model.Go.Name == named.Obj().Name() {
			if name := in.graphQLModelContractName(model.ID); name != "" {
				return name, true
			}
		}
	}
	return "", false
}

func (in *interpreter) callable(expression ast.Expr, kind string) (ir.AttachedMethodIR, *types.Signature, bool) {
	expression = unparen(expression)
	var function *types.Func
	var receiver ir.GoNamedTypeIR
	switch typed := expression.(type) {
	case *ast.Ident:
		function, _ = in.pkg.TypesInfo.Uses[typed].(*types.Func)
	case *ast.SelectorExpr:
		selection := in.pkg.TypesInfo.Selections[typed]
		if selection != nil {
			function, _ = selection.Obj().(*types.Func)
			if function != nil {
				if signature, _ := function.Type().(*types.Signature); signature != nil && signature.Recv() != nil {
					receiver = goNamedType(signature.Recv().Type())
				}
			}
		}
	}
	signature, _ := in.pkg.TypesInfo.TypeOf(expression).(*types.Signature)
	if function == nil || signature == nil || function.Pkg() == nil {
		in.errorAt("P5_EXTENSION_RESOLVER", "resolver must be a direct named function or method value", expression)
		return ir.AttachedMethodIR{}, nil, false
	}
	return ir.AttachedMethodIR{PackagePath: function.Pkg().Path(), Receiver: receiver, Name: function.Name(), Kind: kind}, signature, true
}

func (in *interpreter) isParameter(expression ast.Expr, parameter *types.Var) bool {
	identifier, ok := unparen(expression).(*ast.Ident)
	return ok && in.pkg.TypesInfo.Uses[identifier] == parameter
}

func (in *interpreter) modelFieldType(model *types.Named, name string) types.Type {
	structure, _ := model.Underlying().(*types.Struct)
	if structure == nil {
		return nil
	}
	for index := 0; index < structure.NumFields(); index++ {
		if structure.Field(index).Name() == name {
			return structure.Field(index).Type()
		}
	}
	return nil
}

func sameNamedReceiver(value types.Type, expected *types.Named) bool {
	if pointer, ok := value.(*types.Pointer); ok {
		value = pointer.Elem()
	}
	named, _ := value.(*types.Named)
	return named != nil && named.Obj() == expected.Obj()
}

func goNamedType(value types.Type) ir.GoNamedTypeIR {
	if pointer, ok := value.(*types.Pointer); ok {
		value = pointer.Elem()
	}
	named, _ := value.(*types.Named)
	if named == nil || named.Obj().Pkg() == nil {
		return ir.GoNamedTypeIR{}
	}
	return ir.GoNamedTypeIR{PackagePath: named.Obj().Pkg().Path(), Name: named.Obj().Name()}
}

func isGraphQLModelPointer(value types.Type, model *types.Named, golemPath string) bool {
	pointer, _ := value.(*types.Pointer)
	named, _ := pointerElem(pointer).(*types.Named)
	return named != nil && named.Origin().Obj().Pkg() != nil && named.Origin().Obj().Pkg().Path() == golemPath && named.Origin().Obj().Name() == "GraphQLModel" && named.TypeArgs().Len() == 1 && types.Identical(named.TypeArgs().At(0), model)
}

func isGraphQLSchemaPointer(value types.Type, golemPath string) bool {
	pointer, _ := value.(*types.Pointer)
	named, _ := pointerElem(pointer).(*types.Named)
	return named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == golemPath && named.Obj().Name() == "GraphQLSchema"
}

func pointerElem(pointer *types.Pointer) types.Type {
	if pointer == nil {
		return nil
	}
	return pointer.Elem()
}

func validComputedCallable(signature *types.Signature, model *types.Named, result types.Type, golemPath string) bool {
	return signature != nil && signature.Params().Len() == 3 && signature.Results().Len() == 2 && isContext(signature.Params().At(0).Type()) && isRowOf(signature.Params().At(1).Type(), model, golemPath) && types.Identical(signature.Results().At(0).Type(), result) && isError(signature.Results().At(1).Type())
}

func validBatchCallable(signature *types.Signature, key, result types.Type, golemPath string) bool {
	if signature == nil || signature.Params().Len() != 3 || signature.Results().Len() != 2 || !isContext(signature.Params().At(0).Type()) || !isError(signature.Results().At(1).Type()) {
		return false
	}
	keys, _ := signature.Params().At(1).Type().(*types.Slice)
	values, _ := signature.Results().At(0).Type().(*types.Map)
	return keys != nil && values != nil && types.Identical(keys.Elem(), key) && types.Identical(values.Key(), key) && types.Identical(values.Elem(), result)
}

func validCacheKeyCallable(signature *types.Signature, key types.Type) bool {
	return signature != nil && signature.Params().Len() == 1 && signature.Results().Len() == 2 && types.Identical(signature.Params().At(0).Type(), key) && isBuiltin(signature.Results().At(0).Type(), types.String) && isError(signature.Results().At(1).Type())
}

func validCustomCallable(signature *types.Signature, schemaPackage string) bool {
	return signature != nil && signature.Params().Len() == 3 && signature.Results().Len() == 2 && isContext(signature.Params().At(0).Type()) && isCallerPointer(signature.Params().At(1).Type(), schemaPackage) && isError(signature.Results().At(1).Type())
}

func isContext(value types.Type) bool {
	named, _ := value.(*types.Named)
	return named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}

func isRowOf(value types.Type, model *types.Named, golemPath string) bool {
	named, _ := value.(*types.Named)
	return named != nil && named.Origin().Obj().Pkg() != nil && named.Origin().Obj().Pkg().Path() == golemPath && named.Origin().Obj().Name() == "Row" && named.TypeArgs().Len() == 1 && types.Identical(named.TypeArgs().At(0), model)
}

func isCallerPointer(value types.Type, schemaPackage string) bool {
	pointer, _ := value.(*types.Pointer)
	named, _ := pointerElem(pointer).(*types.Named)
	return named != nil && named.Origin().Obj().Pkg() != nil && named.Origin().Obj().Pkg().Path() == schemaPackage && named.Origin().Obj().Name() == "Caller" && named.TypeArgs().Len() == 1
}

func isError(value types.Type) bool {
	return types.Identical(value, types.Universe.Lookup("error").Type())
}

func isBuiltin(value types.Type, kind types.BasicKind) bool {
	return types.Identical(value, types.Typ[kind])
}
