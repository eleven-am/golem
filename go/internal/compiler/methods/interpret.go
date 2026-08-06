package methods

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"math"
	"sort"
	"strconv"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/keyindex"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	"golang.org/x/tools/go/packages"
)

const defaultGolemPath = "github.com/eleven-am/golem/go/golem"

type vocabulary struct {
	functions map[*types.Func]string
	methods   map[*types.Func]string
	constants map[types.Object]string
}

type interpreter struct {
	config      Config
	pkg         *packages.Package
	model       ir.ModelDeclIR
	engine      *schemaexpr.Engine
	vocab       vocabulary
	handles     map[*types.Var]handleBinding
	advanced    keyindex.AdvancedModelDeclarations
	relations   []RelationOptionDeclaration
	generated   []pendingGenerated
	diagnostics []ir.Diagnostic
	graphql     *graphqlcontract.ModelPatch
	computed    []graphqlextension.ComputedDeclaration
}

type handleBinding struct {
	symbol    modelcodegen.Symbol
	namespace *types.Var
}

type pendingGenerated struct {
	fieldID ir.FieldID
	expr    ir.SchemaExprIR
	storage ir.GeneratedStorage
	scope   ir.ProviderScope
	span    ir.SourceSpan
}

func interpret(ctx context.Context, config Config) Result {
	if config.GolemImportPath == "" {
		config.GolemImportPath = defaultGolemPath
	}
	if len(config.Compilation.Model.Models) == 0 {
		return Result{}
	}
	if config.IDRegistry == nil {
		config.IDRegistry = ir.NewIDRegistry()
	}
	loaded, diagnostics := loadTyped(ctx, config)
	result := Result{Diagnostics: diagnostics}
	if hasErrors(diagnostics) {
		return result
	}
	models := append([]ir.ModelDeclIR(nil), config.Compilation.Model.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	for _, model := range models {
		pkg := loaded.packages[model.Go.PackagePath]
		if pkg == nil {
			result.Diagnostics = append(result.Diagnostics, ir.NewError("P1_METHOD_PACKAGE_MISSING", fmt.Sprintf("registered model package %q was not loaded", model.Go.PackagePath), ir.SourceSpan{}))
			continue
		}
		vocab, vocabDiagnostics := buildVocabulary(pkg, config.GolemImportPath)
		result.Diagnostics = append(result.Diagnostics, vocabDiagnostics...)
		handles, handleDiagnostics := buildHandles(pkg, loaded.overlay.Manifest, model.Go.PackagePath)
		result.Diagnostics = append(result.Diagnostics, handleDiagnostics...)
		entry := &interpreter{
			config: config, pkg: pkg, model: model,
			engine: schemaexpr.New(model, config.Registry), vocab: vocab, handles: handles,
			advanced: keyindex.AdvancedModelDeclarations{ModelID: model.ID},
		}
		entry.interpretModel()
		entry.interpretGraphQLModel()
		entry.finishGenerated()
		result.Advanced = append(result.Advanced, entry.advanced)
		result.RelationOptions = append(result.RelationOptions, entry.relations...)
		if entry.graphql != nil {
			result.GraphQLModels = append(result.GraphQLModels, *entry.graphql)
		}
		result.GraphQLComputed = append(result.GraphQLComputed, entry.computed...)
		result.Diagnostics = append(result.Diagnostics, entry.diagnostics...)
	}
	custom, customDiagnostics := interpretGraphQLSchema(config, loaded)
	result.GraphQLCustom = append(result.GraphQLCustom, custom...)
	result.Diagnostics = append(result.Diagnostics, customDiagnostics...)
	sort.Slice(result.Advanced, func(i, j int) bool { return result.Advanced[i].ModelID < result.Advanced[j].ModelID })
	sort.Slice(result.RelationOptions, func(i, j int) bool {
		if result.RelationOptions[i].ModelID != result.RelationOptions[j].ModelID {
			return result.RelationOptions[i].ModelID < result.RelationOptions[j].ModelID
		}
		if result.RelationOptions[i].RelationID != result.RelationOptions[j].RelationID {
			return result.RelationOptions[i].RelationID < result.RelationOptions[j].RelationID
		}
		return result.RelationOptions[i].Span.StartLine < result.RelationOptions[j].Span.StartLine
	})
	graphqlcontract.SortPatches(result.GraphQLModels)
	graphqlextension.SortDeclarations(result.GraphQLComputed, result.GraphQLCustom)
	ir.SortDiagnostics(result.Diagnostics)
	return result
}

func buildVocabulary(pkg *packages.Package, golemPath string) (vocabulary, []ir.Diagnostic) {
	result := vocabulary{functions: map[*types.Func]string{}, methods: map[*types.Func]string{}, constants: map[types.Object]string{}}
	golemPkg := pkg.Imports[golemPath]
	if golemPkg == nil || golemPkg.Types == nil {
		return result, []ir.Diagnostic{ir.NewError("P1_METHOD_GOLEM_IMPORT", fmt.Sprintf("package %q does not import the declaration package %q", pkg.PkgPath, golemPath), ir.SourceSpan{})}
	}
	scope := golemPkg.Types.Scope()
	for _, name := range []string{"DefineModel", "PrimaryKey", "Unique", "Index", "IndexColumn", "IndexExpr", "Check", "Generated", "RelationOptions", "ForProvider", "SchemaValueOf", "Lower", "Upper", "Length", "Coalesce", "Cast", "GraphQL", "GraphQLOperations", "GraphQLPlural", "GraphQLRoots", "GraphQLPageSizes", "GraphQLHidden", "ComputedField", "BatchedComputedField", "BatchedComputedFieldWithCacheKey", "Requires", "Query", "Mutation", "GraphQLBoolean", "GraphQLInt", "GraphQLFloat", "GraphQLString", "GraphQLBigInt", "GraphQLDecimal", "GraphQLUUID", "GraphQLDate", "GraphQLTime", "GraphQLDateTime", "GraphQLBytes", "GraphQLJSON", "GraphQLObject", "GraphQLEnum", "GraphQLList"} {
		if fn, ok := scope.Lookup(name).(*types.Func); ok {
			result.functions[fn] = name
		}
	}
	for typeName, names := range map[string][]string{
		"SchemaExpr":         {"Eq", "Ne", "LT", "LTE", "GT", "GTE", "IsNull", "IsNotNull", "Add", "Sub", "Mul", "Div", "Mod"},
		"SchemaPredicate":    {"Or", "And", "Not"},
		"IndexKey":           {"Desc"},
		"IndexSpec":          {"Keys", "Where"},
		"RelationOptionSpec": {"OnUpdate", "OnDelete"},
		"GraphQLType":        {"NonNull"},
	} {
		object, _ := scope.Lookup(typeName).(*types.TypeName)
		if object == nil {
			continue
		}
		named, _ := object.Type().(*types.Named)
		if named == nil {
			continue
		}
		wanted := map[string]bool{}
		for _, name := range names {
			wanted[name] = true
		}
		for index := 0; index < named.NumMethods(); index++ {
			method := named.Method(index)
			if wanted[method.Name()] {
				result.methods[method] = typeName + "." + method.Name()
			}
		}
	}
	for _, name := range []string{"SQLite", "PostgreSQL", "Stored", "Virtual", "NoAction", "Restrict", "Cascade", "SetNull", "SetDefault", "Int16ToInt32", "Int16ToInt64", "Int32ToInt64", "Int64ToString", "GraphQLFindOne", "GraphQLFindMany", "GraphQLCreate", "GraphQLUpdate", "GraphQLUpsert", "GraphQLDelete", "GraphQLUpdateMany", "GraphQLDeleteMany"} {
		if object := scope.Lookup(name); object != nil {
			result.constants[object] = name
		}
	}
	return result, nil
}

func buildHandles(pkg *packages.Package, manifest modelcodegen.Manifest, packagePath string) (map[*types.Var]handleBinding, []ir.Diagnostic) {
	result := map[*types.Var]handleBinding{}
	var diagnostics []ir.Diagnostic
	for _, symbol := range manifest.Symbols {
		if symbol.PackagePath != packagePath || symbol.Namespace == "" || symbol.Kind != modelcodegen.SymbolField && symbol.Kind != modelcodegen.SymbolRelation {
			continue
		}
		namespace, _ := pkg.Types.Scope().Lookup(symbol.Namespace).(*types.Var)
		if namespace == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_METHOD_MANIFEST_NAMESPACE", fmt.Sprintf("generated namespace %s.%s is missing", packagePath, symbol.Namespace), ir.SourceSpan{}))
			continue
		}
		typeValue := namespace.Type()
		if named, ok := typeValue.(*types.Named); ok {
			typeValue = named.Underlying()
		}
		structure, _ := typeValue.(*types.Struct)
		if structure == nil {
			continue
		}
		for index := 0; index < structure.NumFields(); index++ {
			field := structure.Field(index)
			if field.Name() == symbol.Name {
				result[field] = handleBinding{symbol: symbol, namespace: namespace}
				break
			}
		}
	}
	return result, diagnostics
}

func (in *interpreter) interpretModel() {
	object := in.pkg.Types.Scope().Lookup(in.model.Go.Name)
	typeName, _ := object.(*types.TypeName)
	named, _ := typeNameType(typeName).(*types.Named)
	if named == nil {
		in.errorAt("P1_METHOD_MODEL_TYPE", fmt.Sprintf("registered model %s.%s is not a defined Go type", in.model.Go.PackagePath, in.model.Go.Name), nil)
		return
	}
	type candidate struct {
		declaration *ast.FuncDecl
		function    *types.Func
	}
	var candidates []candidate
	for _, file := range in.pkg.Syntax {
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "GolemModel" || fn.Recv == nil {
				continue
			}
			function, _ := in.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
			if function == nil {
				continue
			}
			signature, _ := function.Type().(*types.Signature)
			if signature == nil || signature.Recv() == nil {
				continue
			}
			receiver := signature.Recv().Type()
			if pointer, ok := receiver.(*types.Pointer); ok {
				receiver = pointer.Elem()
			}
			receiverNamed, _ := receiver.(*types.Named)
			if receiverNamed != nil && receiverNamed.Obj() == named.Obj() {
				candidates = append(candidates, candidate{fn, function})
			}
		}
	}
	if len(candidates) == 0 {
		return
	}
	if len(candidates) != 1 {
		for _, item := range candidates {
			in.errorAt("P1_METHOD_DUPLICATE", fmt.Sprintf("model %s has multiple GolemModel declarations", in.model.Go.Name), item.declaration)
		}
		return
	}
	item := candidates[0]
	signature := item.function.Type().(*types.Signature)
	valid := true
	if _, pointer := signature.Recv().Type().(*types.Pointer); pointer {
		in.errorAt("P1_METHOD_RECEIVER", "GolemModel requires an exact value receiver", item.declaration.Recv)
		valid = false
	}
	if signature.Params().Len() != 0 || signature.Results().Len() != 1 || !isModelSpecFor(signature.Results(), named, in.config.GolemImportPath) {
		in.errorAt("P1_METHOD_SIGNATURE", fmt.Sprintf("GolemModel must have signature func (%s) GolemModel() golem.ModelSpec[%s]", in.model.Go.Name, in.model.Go.Name), item.declaration.Type)
		valid = false
	}
	if !valid {
		return
	}
	if item.declaration.Body == nil || len(item.declaration.Body.List) != 1 {
		in.errorAt("P1_METHOD_BODY", "GolemModel body must contain exactly one return statement", item.declaration.Body)
		return
	}
	returnStatement, ok := item.declaration.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		in.errorAt("P1_METHOD_BODY", "GolemModel body must contain exactly one returned declaration expression", item.declaration.Body)
		return
	}
	call, ok := unparen(returnStatement.Results[0]).(*ast.CallExpr)
	if !ok || in.callOperation(call) != "DefineModel" {
		in.errorAt("P1_METHOD_ROOT", "GolemModel must return a direct call to golem.DefineModel", returnStatement.Results[0])
		return
	}
	for _, argument := range call.Args {
		in.evalOption(argument, ir.ProviderScopePortable)
	}
}

func typeNameType(name *types.TypeName) types.Type {
	if name == nil {
		return nil
	}
	return name.Type()
}

func isModelSpecFor(results *types.Tuple, model *types.Named, golemPath string) bool {
	if results == nil || results.Len() != 1 {
		return false
	}
	named, ok := results.At(0).Type().(*types.Named)
	if !ok || named.Origin().Obj().Pkg() == nil || named.Origin().Obj().Pkg().Path() != golemPath || named.Origin().Obj().Name() != "ModelSpec" || named.TypeArgs().Len() != 1 {
		return false
	}
	return types.Identical(named.TypeArgs().At(0), model)
}

func (in *interpreter) callObject(call *ast.CallExpr) *types.Func {
	var expression ast.Expr = call.Fun
	for {
		switch typed := expression.(type) {
		case *ast.IndexExpr:
			expression = typed.X
		case *ast.IndexListExpr:
			expression = typed.X
		case *ast.ParenExpr:
			expression = typed.X
		default:
			switch typed := expression.(type) {
			case *ast.Ident:
				function, _ := in.pkg.TypesInfo.Uses[typed].(*types.Func)
				return function
			case *ast.SelectorExpr:
				if selection := in.pkg.TypesInfo.Selections[typed]; selection != nil {
					function, _ := selection.Obj().(*types.Func)
					return function
				}
				function, _ := in.pkg.TypesInfo.Uses[typed.Sel].(*types.Func)
				return function
			}
			return nil
		}
	}
}

func (in *interpreter) callOperation(call *ast.CallExpr) string {
	function := in.callObject(call)
	if function == nil {
		return ""
	}
	function = function.Origin()
	if operation := in.vocab.functions[function]; operation != "" {
		return operation
	}
	return in.vocab.methods[function]
}

func (in *interpreter) receiver(call *ast.CallExpr) ast.Expr {
	expression := call.Fun
	for {
		switch typed := expression.(type) {
		case *ast.IndexExpr:
			expression = typed.X
		case *ast.IndexListExpr:
			expression = typed.X
		case *ast.ParenExpr:
			expression = typed.X
		default:
			selector, _ := expression.(*ast.SelectorExpr)
			if selector != nil {
				return selector.X
			}
			return nil
		}
	}
}

func (in *interpreter) resolveHandle(expression ast.Expr) (modelcodegen.Symbol, bool) {
	selector, ok := unparen(expression).(*ast.SelectorExpr)
	if !ok {
		return modelcodegen.Symbol{}, false
	}
	selection := in.pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return modelcodegen.Symbol{}, false
	}
	field, _ := selection.Obj().(*types.Var)
	binding, ok := in.handles[field]
	if !ok {
		return modelcodegen.Symbol{}, false
	}
	namespace, ok := unparen(selector.X).(*ast.Ident)
	if !ok || in.pkg.TypesInfo.Uses[namespace] != binding.namespace || binding.symbol.ModelID != in.model.ID {
		return modelcodegen.Symbol{}, false
	}
	return binding.symbol, true
}

func (in *interpreter) span(node ast.Node) ir.SourceSpan {
	if node == nil {
		return ir.SourceSpan{ModulePath: in.config.ModulePath}
	}
	start, end := in.pkg.Fset.Position(node.Pos()), in.pkg.Fset.Position(node.End())
	return sourceSpan(start.Filename, start.Line, start.Column, end.Line, end.Column, in.pkg, in.config)
}

func (in *interpreter) errorAt(code, message string, node ast.Node) {
	in.diagnostics = append(in.diagnostics, ir.NewError(code, message, in.span(node)))
}

func (in *interpreter) addDiagnostics(items []ir.Diagnostic, node ast.Node) {
	span := in.span(node)
	for index := range items {
		if items[index].Primary.RelativeFile == "" {
			items[index].Primary = span
		}
	}
	in.diagnostics = append(in.diagnostics, items...)
}

func unparen(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}

func (in *interpreter) constString(expression ast.Expr) (string, bool) {
	value := in.pkg.TypesInfo.Types[expression].Value
	if value == nil || value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(value), true
}

func (in *interpreter) constantName(expression ast.Expr) string {
	switch typed := unparen(expression).(type) {
	case *ast.Ident:
		return in.vocab.constants[in.pkg.TypesInfo.Uses[typed]]
	case *ast.SelectorExpr:
		if in.pkg.TypesInfo.Selections[typed] != nil {
			return ""
		}
		return in.vocab.constants[in.pkg.TypesInfo.Uses[typed.Sel]]
	}
	return ""
}

func (in *interpreter) provider(expression ast.Expr) (ir.ProviderScope, bool) {
	switch in.constantName(expression) {
	case "SQLite":
		return ir.ProviderScopeSQLite, true
	case "PostgreSQL":
		return ir.ProviderScopePostgreSQL, true
	default:
		return "", false
	}
}

func (in *interpreter) action(expression ast.Expr) (ir.ReferentialAction, bool) {
	values := map[string]ir.ReferentialAction{"NoAction": ir.ActionNoAction, "Restrict": ir.ActionRestrict, "Cascade": ir.ActionCascade, "SetNull": ir.ActionSetNull, "SetDefault": ir.ActionSetDefault}
	value, ok := values[in.constantName(expression)]
	return value, ok
}

func (in *interpreter) literal(expression ast.Expr, logical ir.LogicalTypeIR) (ir.SchemaExprIR, bool) {
	value := in.pkg.TypesInfo.Types[expression].Value
	if value == nil {
		in.errorAt("P1_METHOD_LITERAL", "schema expressions accept only literals or typed constants", expression)
		return ir.SchemaExprIR{}, false
	}
	var literal ir.TypedLiteralIR
	switch logical.Kind {
	case ir.TypeBool:
		if value.Kind() != constant.Bool {
			break
		}
		literal = ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: strconv.FormatBool(constant.BoolVal(value))}
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			break
		}
		literal = ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: integer.ExactString()}
	case ir.TypeFloat32, ir.TypeFloat64:
		float, exact := constant.Float64Val(constant.ToFloat(value))
		if !exact && (math.IsInf(float, 0) || math.IsNaN(float)) {
			break
		}
		bits := 64
		if logical.Kind == ir.TypeFloat32 {
			bits = 32
		}
		literal = ir.TypedLiteralIR{Kind: ir.LiteralFloat, Canonical: strconv.FormatFloat(float, 'g', -1, bits)}
	case ir.TypeString:
		if value.Kind() != constant.String {
			break
		}
		literal = ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: constant.StringVal(value)}
	case ir.TypeEnum:
		if value.Kind() != constant.String {
			break
		}
		literal = ir.TypedLiteralIR{Kind: ir.LiteralEnum, Canonical: constant.StringVal(value)}
	}
	if literal.Kind == "" {
		in.errorAt("P1_METHOD_LITERAL_TYPE", fmt.Sprintf("constant cannot represent schema type %s", logical.Kind), expression)
		return ir.SchemaExprIR{}, false
	}
	result, diagnostics := in.engine.Literal(logical, literal)
	in.addDiagnostics(diagnostics, expression)
	return result, !hasErrors(diagnostics)
}

func (in *interpreter) logicalTypeOf(expression ast.Expr) (ir.LogicalTypeIR, bool) {
	typeValue := in.pkg.TypesInfo.TypeOf(expression)
	named, ok := typeValue.(*types.Named)
	if ok && named.Origin().Obj().Pkg() != nil && named.Origin().Obj().Pkg().Path() == in.config.GolemImportPath && named.Origin().Obj().Name() == "SchemaExpr" && named.TypeArgs().Len() == 2 {
		typeValue = named.TypeArgs().At(1)
	}
	return in.logicalType(typeValue)
}

func (in *interpreter) logicalType(typeValue types.Type) (ir.LogicalTypeIR, bool) {
	if named, ok := typeValue.(*types.Named); ok {
		for _, enum := range in.config.Compilation.Model.Enums {
			if named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == enum.Go.PackagePath && named.Obj().Name() == enum.Go.Name {
				id := enum.ID
				return ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &id}, true
			}
		}
	}
	basic, ok := typeValue.Underlying().(*types.Basic)
	if !ok {
		return ir.LogicalTypeIR{}, false
	}
	kinds := map[types.BasicKind]ir.LogicalTypeKind{types.Bool: ir.TypeBool, types.Int16: ir.TypeInt16, types.Int32: ir.TypeInt32, types.Int64: ir.TypeInt64, types.Float32: ir.TypeFloat32, types.Float64: ir.TypeFloat64, types.String: ir.TypeString}
	kind, ok := kinds[basic.Kind()]
	return ir.LogicalTypeIR{Kind: kind}, ok
}
