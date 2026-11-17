package methods

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// A template is a closed predicate expression, not an executable policy
// factory. The only runtime inputs are direct fields of its named argument.
func (in *interpreter) evalScopeTemplate(call *ast.CallExpr) {
	in.evalPolicyTemplate(call, false)
}

func (in *interpreter) evalFactTemplate(call *ast.CallExpr) {
	in.evalPolicyTemplate(call, true)
}

func (in *interpreter) evalPolicyTemplate(call *ast.CallExpr, data bool) {
	arity := 2
	if data {
		arity = 3
	}
	if len(call.Args) != arity {
		in.errorAt("P05_SCOPE_TEMPLATE", "ScopeTemplate requires a name and a predicate function literal", call)
		return
	}
	name, valid := in.constString(call.Args[0])
	scopeName := ""
	if data {
		var scopeValid bool
		scopeName, scopeValid = in.constString(call.Args[1])
		valid = valid && scopeValid
	}
	function, literal := unparen(call.Args[arity-1]).(*ast.FuncLit)
	if !valid || !literal || function.Body == nil || len(function.Body.List) != 1 {
		in.errorAt("P05_SCOPE_TEMPLATE", "ScopeTemplate requires a constant name and a function containing one return expression", call)
		return
	}
	returned, valid := function.Body.List[0].(*ast.ReturnStmt)
	if !valid || len(returned.Results) != 1 {
		in.errorAt("P05_SCOPE_TEMPLATE", "scope functions must contain exactly one return expression", function)
		return
	}
	signature, _ := in.pkg.TypesInfo.TypeOf(function).(*types.Signature)
	if signature == nil || signature.Params().Len() != 1 || signature.Results().Len() != 1 || signature.Variadic() {
		in.errorAt("P05_SCOPE_ARGUMENTS", "scope functions require one named struct argument and one predicate result", function)
		return
	}
	argument := signature.Params().At(0)
	named, _ := types.Unalias(argument.Type()).(*types.Named)
	var structure *types.Struct
	if named != nil {
		structure, _ = named.Underlying().(*types.Struct)
	}
	if structure == nil || named.Obj().Pkg() == nil || named.TypeArgs().Len() != 0 || argument.Name() == "_" || argument.Name() == "" {
		in.errorAt("P05_SCOPE_ARGUMENTS", "scope argument must be a named, non-generic struct value", function)
		return
	}
	if !in.scopePredicateType(signature.Results().At(0).Type(), in.model.ID) {
		in.errorAt("P05_SCOPE_RESULT", "scope function must return a predicate of its owning model", function)
		return
	}
	parser := scopeParser{in: in, argument: argument, structure: structure, parameters: map[string]ir.ScopeParameterIR{}, data: data}
	expression, valid := parser.expression(returned.Results[0], in.model.ID, 0)
	if !valid {
		return
	}
	parameters := make([]ir.ScopeParameterIR, 0, len(parser.parameters))
	for _, parameter := range parser.parameters {
		parameters = append(parameters, parameter)
	}
	sort.Slice(parameters, func(i, j int) bool { return parameters[i].Name < parameters[j].Name })
	arguments := ir.GoNamedTypeIR{PackagePath: named.Obj().Pkg().Path(), Name: named.Obj().Name()}
	if data {
		in.factTemplates = append(in.factTemplates, scopecontract.FactDeclaration{ModelID: in.model.ID, ScopeName: scopeName, Span: in.span(call), Template: ir.FactTemplateIR{Name: name, Arguments: arguments, Parameters: parameters, Expression: expression}})
	} else {
		in.scopeTemplates = append(in.scopeTemplates, scopecontract.Declaration{ModelID: in.model.ID, Span: in.span(call), Template: ir.ScopeTemplateIR{Name: name, Arguments: arguments, Parameters: parameters, Expression: expression}})
	}
}

type scopeParser struct {
	in         *interpreter
	argument   *types.Var
	structure  *types.Struct
	parameters map[string]ir.ScopeParameterIR
	nodes      int
	data       bool
}

type scopeSymbolKey struct{ pkg, namespace, member string }
type scopeFieldKey struct {
	model ir.ModelID
	field ir.FieldID
}

// This interpretation-wide index contains only immutable schema metadata. It
// is built lazily, so schemas without scope templates retain their old cost.
type scopeIndex struct {
	model     ir.ModelIR
	manifest  modelcodegen.Manifest
	models    map[ir.ModelID]ir.GoNamedTypeIR
	fields    map[scopeFieldKey]ir.FieldIR
	relations map[ir.RelationID]ir.RelationIR
	symbols   map[scopeSymbolKey]modelcodegen.Symbol
}

func (index *scopeIndex) prepare() {
	if index.models != nil {
		return
	}
	index.models, index.fields = map[ir.ModelID]ir.GoNamedTypeIR{}, map[scopeFieldKey]ir.FieldIR{}
	index.relations, index.symbols = map[ir.RelationID]ir.RelationIR{}, map[scopeSymbolKey]modelcodegen.Symbol{}
	for _, model := range index.model.Models {
		index.models[model.ID] = model.Go
		for _, field := range model.Fields {
			index.fields[scopeFieldKey{model.ID, field.ID}] = field
		}
	}
	for _, relation := range index.model.Relations {
		index.relations[relation.ID] = relation
	}
	for _, symbol := range index.manifest.Symbols {
		if symbol.Kind == modelcodegen.SymbolField || symbol.Kind == modelcodegen.SymbolRelation {
			index.symbols[scopeSymbolKey{symbol.PackagePath, symbol.Namespace, symbol.Name}] = symbol
		}
	}
}

func (in *interpreter) scopePredicateType(value types.Type, model ir.ModelID) bool {
	named, _ := types.Unalias(value).(*types.Named)
	if named == nil || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != in.config.GolemImportPath || named.Origin().Obj().Name() != "Predicate" || named.TypeArgs().Len() != 1 {
		return false
	}
	root, _ := types.Unalias(named.TypeArgs().At(0)).(*types.Named)
	if root == nil || root.Obj().Pkg() == nil {
		return false
	}
	in.scopeIndex.prepare()
	candidate, present := in.scopeIndex.models[model]
	return present && root.Obj().Pkg().Path() == candidate.PackagePath && root.Obj().Name() == candidate.Name
}

func (parser *scopeParser) reject(expression ast.Expr, detail string) (ir.ScopeExpressionIR, bool) {
	parser.in.errorAt("P05_SCOPE_EXPRESSION", detail, expression)
	return ir.ScopeExpressionIR{}, false
}

func (parser *scopeParser) expression(expression ast.Expr, model ir.ModelID, depth int) (ir.ScopeExpressionIR, bool) {
	parser.nodes++
	if parser.nodes > scopecontract.MaxNodes || depth > scopecontract.MaxDepth {
		return parser.reject(expression, "scope template expression exceeds its node or depth limit")
	}
	in := parser.in
	call, ok := unparen(expression).(*ast.CallExpr)
	if !ok || !in.scopePredicateType(in.pkg.TypesInfo.TypeOf(expression), model) {
		return parser.reject(expression, "scope templates accept only typed predicate constructor calls")
	}
	function := in.callObject(call)
	if function == nil || function.Origin().Pkg() == nil || function.Origin().Pkg().Path() != in.config.GolemImportPath {
		return parser.reject(expression, "scope templates cannot call application functions or capture predicate values")
	}
	operation := function.Name()
	result := ir.ScopeExpressionIR{ModelID: model, Operator: strings.ToLower(operation)}
	signature, _ := function.Type().(*types.Signature)
	method := signature != nil && signature.Recv() != nil
	switch operation {
	case "All", "None":
		if method && operation == "None" && parser.data {
			return parser.relation(call, model, "none", depth)
		}
		if method || len(call.Args) != 0 {
			return parser.reject(expression, "scope constants take no arguments")
		}
		result.Operator = "true"
		if operation == "None" {
			result.Operator = "false"
		}
		return result, true
	case "And", "Or", "Not":
		if call.Ellipsis.IsValid() {
			return parser.reject(expression, "scope logical children must be statically declared")
		}
		receiverCount := 0
		if method {
			receiverCount = 1
		}
		if len(call.Args) > scopecontract.MaxNodes-parser.nodes-receiverCount {
			return parser.reject(expression, "scope logical children exceed the remaining node budget")
		}
		children := make([]ast.Expr, 0, len(call.Args)+receiverCount)
		if method {
			children = append(children, in.receiver(call))
		}
		children = append(children, call.Args...)
		if operation == "Not" && len(children) != 1 {
			return parser.reject(expression, "scope Not requires one predicate")
		}
		if len(children) == 0 {
			result.Operator = "true"
			if operation == "Or" {
				result.Operator = "false"
			}
		}
		for _, child := range children {
			value, valid := parser.expression(child, model, depth+1)
			if !valid {
				return ir.ScopeExpressionIR{}, false
			}
			result.Children = append(result.Children, value)
		}
		return result, true
	case "Eq", "In", "Ne", "NotIn", "LT", "LTE", "GT", "GTE":
		set := operation == "In" || operation == "NotIn"
		if !parser.data && operation != "Eq" && operation != "In" {
			return parser.reject(expression, "authority scalar operator is not supported")
		}
		if !method || !set && len(call.Args) != 1 {
			return parser.reject(expression, "scope scalar comparison has invalid arity")
		}
		handle, valid := in.resolveScopeHandle(in.receiver(call))
		if !valid || handle.Kind != modelcodegen.SymbolField || handle.ModelID != model {
			return parser.reject(expression, "scope scalar must name a generated field at its exact model root")
		}
		field, valid := in.scopeScalar(model, handle.FieldID)
		if !valid || field.Scalar == nil {
			return parser.reject(expression, "scope comparison requires a stored scalar field")
		}
		result.FieldID = handle.FieldID
		many := set && call.Ellipsis.IsValid()
		if len(call.Args) == 1 {
			if name, parameter := parser.parameter(call.Args[0]); parameter {
				if set && !many {
					return parser.reject(expression, "scope In parameters must expand a slice argument field")
				}
				result.Parameter = name
				value := ir.ScopeParameterIR{Name: name, Type: field.Scalar.Type, Many: many}
				if previous, present := parser.parameters[name]; present {
					// Complete type/cardinality consistency is checked by the
					// normalized contract owner, including logical type details.
					if previous.Many != value.Many {
						return parser.reject(expression, "scope parameter mixes scalar and set operands")
					}
				} else {
					parser.parameters[name] = value
				}
				return result, true
			}
		}
		if call.Ellipsis.IsValid() || len(call.Args) > scopecontract.MaxLiteralValues {
			return parser.reject(expression, "scope sets require bounded constants or one direct slice parameter")
		}
		for _, argument := range call.Args {
			literal, valid := in.literal(argument, field.Scalar.Type)
			if !valid || literal.Literal == nil {
				return ir.ScopeExpressionIR{}, false
			}
			result.Literals = append(result.Literals, *literal.Literal)
		}
		return result, true
	case "Is", "Some", "IsNot", "Every", "IsNull", "IsNotNull":
		if !method {
			return parser.reject(expression, "template relationship/presence requires a method")
		}
		return parser.relation(call, model, strings.ToLower(operation), depth)
	default:
		return parser.reject(expression, fmt.Sprintf("scope operator %s is not a supported positive-domain expression", operation))
	}
}

func (parser *scopeParser) relation(call *ast.CallExpr, model ir.ModelID, operation string, depth int) (ir.ScopeExpressionIR, bool) {
	if !parser.data && operation != "is" && operation != "some" {
		return parser.reject(call, "authority requires positive Is/Some witnesses")
	}
	presence := operation == "isnull" || operation == "isnotnull"
	arity := 1
	if presence {
		arity = 0
	}
	if len(call.Args) != arity || call.Ellipsis.IsValid() {
		return parser.reject(call, "template relation/presence has invalid operand arity")
	}
	in := parser.in
	handle, valid := in.resolveScopeHandle(in.receiver(call))
	if !valid || handle.ModelID != model {
		return parser.reject(call, "template relationship must name its exact generated model handle")
	}
	field, found := in.scopeScalar(model, handle.FieldID)
	result := ir.ScopeExpressionIR{ModelID: model, Operator: operation, FieldID: handle.FieldID}
	if presence && handle.Kind == modelcodegen.SymbolField && found && field.Scalar != nil {
		return result, true
	}
	if !found || handle.Kind != modelcodegen.SymbolRelation || field.Relation == nil {
		return parser.reject(call, "template relationship endpoint is absent")
	}
	in.scopeIndex.prepare()
	relation := in.scopeIndex.relations[handle.RelationID]
	target := relation.TargetModel
	if field.Relation.Role == ir.RelationInverse {
		target = relation.SourceModel
	}
	result.RelationID, result.TargetModelID = handle.RelationID, target
	if !presence {
		child, valid := parser.expression(call.Args[0], target, depth+1)
		if !valid {
			return ir.ScopeExpressionIR{}, false
		}
		result.Children = []ir.ScopeExpressionIR{child}
	}
	return result, true
}

func (parser *scopeParser) parameter(expression ast.Expr) (string, bool) {
	selector, ok := unparen(expression).(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	owner, ok := unparen(selector.X).(*ast.Ident)
	if !ok || parser.in.pkg.TypesInfo.Uses[owner] != parser.argument {
		return "", false
	}
	selection := parser.in.pkg.TypesInfo.Selections[selector]
	if selection == nil || len(selection.Index()) != 1 {
		return "", false
	}
	field := parser.structure.Field(selection.Index()[0])
	return field.Name(), field.Exported() && !field.Embedded()
}

func (in *interpreter) scopeScalar(model ir.ModelID, id ir.FieldID) (ir.FieldIR, bool) {
	in.scopeIndex.prepare()
	field, present := in.scopeIndex.fields[scopeFieldKey{model, id}]
	return field, present
}

func (in *interpreter) resolveScopeHandle(expression ast.Expr) (modelcodegen.Symbol, bool) {
	if symbol, found := in.resolveAnyHandle(expression); found {
		return symbol, true
	}
	selector, ok := unparen(expression).(*ast.SelectorExpr)
	if !ok {
		return modelcodegen.Symbol{}, false
	}
	namespace, ok := unparen(selector.X).(*ast.SelectorExpr)
	if !ok {
		return modelcodegen.Symbol{}, false
	}
	importName, ok := unparen(namespace.X).(*ast.Ident)
	if !ok {
		return modelcodegen.Symbol{}, false
	}
	imported, _ := in.pkg.TypesInfo.Uses[importName].(*types.PkgName)
	if imported == nil {
		return modelcodegen.Symbol{}, false
	}
	selection := in.pkg.TypesInfo.Selections[selector]
	variable, _ := in.pkg.TypesInfo.Uses[namespace.Sel].(*types.Var)
	if selection == nil || variable == nil {
		return modelcodegen.Symbol{}, false
	}
	// Match imported generated members by exact Go object plus manifest ID;
	// a lookalike application struct or namespace is not a field capability.
	if variable.Pkg() != imported.Imported() || selection.Obj().Pkg() != imported.Imported() || imported.Imported().Scope().Lookup(variable.Name()) != variable {
		return modelcodegen.Symbol{}, false
	}
	in.scopeIndex.prepare()
	symbol, present := in.scopeIndex.symbols[scopeSymbolKey{imported.Imported().Path(), variable.Name(), selection.Obj().Name()}]
	return symbol, present
}
