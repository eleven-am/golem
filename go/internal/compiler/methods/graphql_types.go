package methods

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type graphQLRegistration struct {
	named      *types.Named
	call       *ast.CallExpr
	definition ir.GraphQLTypeDefinitionIR
}

func graphQLRegistrationKey(named *types.Named, kind ir.GraphQLTypeKind) string {
	return string(kind) + "\x00" + named.Obj().Pkg().Path() + "\x00" + named.Obj().Name()
}

func (in *interpreter) collectGraphQLTypes(declaration *ast.FuncDecl, parameter *types.Var) []ir.GraphQLTypeDefinitionIR {
	in.objectTypes = make(map[string]ir.GraphQLTypeIR)
	names := make(map[string]bool)
	var registrations []graphQLRegistration
	for _, statement := range declaration.Body.List {
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := unparen(expression.X).(*ast.CallExpr)
		if !ok {
			continue
		}
		operation := in.callOperation(call)
		if operation != "Input" && operation != "Output" {
			continue
		}
		if len(call.Args) < 2 || !in.isParameter(call.Args[0], parameter) {
			in.errorAt("P5_OBJECT_REGISTRATION", "type registration requires the schema parameter and constant GraphQL name", call)
			continue
		}
		var typeExpression ast.Expr
		switch function := unparen(call.Fun).(type) {
		case *ast.IndexExpr:
			typeExpression = function.Index
		case *ast.IndexListExpr:
			if len(function.Indices) == 1 {
				typeExpression = function.Indices[0]
			}
		}
		if typeExpression == nil {
			in.errorAt("P5_OBJECT_TYPE", "type registration requires one named Go struct", call)
			continue
		}
		named, ok := types.Unalias(in.pkg.TypesInfo.TypeOf(typeExpression)).(*types.Named)
		if !ok || named.Obj().Pkg() == nil || named.TypeArgs().Len() != 0 || named.TypeParams().Len() != 0 {
			in.errorAt("P5_OBJECT_TYPE", "type registration requires one non-generic named Go struct", call)
			continue
		}
		if _, ok := named.Underlying().(*types.Struct); !ok {
			in.errorAt("P5_OBJECT_TYPE", "type registration requires a named Go struct", call)
			continue
		}
		name, ok := in.constString(call.Args[1])
		if !ok {
			in.errorAt("P5_OBJECT_NAME", "GraphQL type name must be a constant string", call.Args[1])
			continue
		}
		kind := ir.GraphQLTypeObject
		if operation == "Input" {
			kind = ir.GraphQLTypeInputObject
		}
		key := graphQLRegistrationKey(named, kind)
		if _, exists := in.objectTypes[key]; exists || names[name] {
			in.errorAt("P5_OBJECT_DUPLICATE", fmt.Sprintf("duplicate GraphQL registration %q", name), call)
			continue
		}
		names[name] = true
		in.objectTypes[key] = ir.GraphQLTypeIR{Kind: kind, Name: name}
		registrations = append(registrations, graphQLRegistration{named: named, call: call, definition: ir.GraphQLTypeDefinitionIR{Kind: kind, Name: name, Go: ir.GoNamedTypeIR{PackagePath: named.Obj().Pkg().Path(), Name: named.Obj().Name()}}})
	}
	definitions := make([]ir.GraphQLTypeDefinitionIR, 0, len(registrations))
	for _, registration := range registrations {
		structure := registration.named.Underlying().(*types.Struct)
		definition := registration.definition
		use := graphQLCustomResult
		if definition.Kind == ir.GraphQLTypeInputObject {
			use = graphQLCustomArgument
		}
		var maxima map[string][]uint32
		if definition.Kind == ir.GraphQLTypeObject {
			maxima = in.graphQLObjectMaxima(registration.call)
		} else {
			definition.OneOf = in.graphQLInputOneOf(registration.call)
		}
		for index := 0; index < structure.NumFields(); index++ {
			field := structure.Field(index)
			if !field.Exported() || field.Embedded() {
				in.errorAt("P5_OBJECT_FIELD", fmt.Sprintf("GraphQL field %s.%s must be exported and non-embedded", definition.Name, field.Name()), registration.call)
				continue
			}
			name, valid := graphQLArgumentName(field.Name(), structure.Tag(index))
			if !valid {
				in.errorAt("P5_OBJECT_FIELD", fmt.Sprintf("GraphQL field %s.%s has an invalid tag", definition.Name, field.Name()), registration.call)
				continue
			}
			value, valid := in.graphQLTypeFromGo(field.Type(), use)
			if !valid {
				in.errorAt("P5_OBJECT_FIELD_TYPE", fmt.Sprintf("GraphQL field %s.%s has an unsupported or unregistered type", definition.Name, field.Name()), registration.call)
				continue
			}
			bounds, declared := maxima[field.Name()]
			delete(maxima, field.Name())
			depth := value.ListDepth()
			if (declared && len(bounds) != depth) || (definition.Kind == ir.GraphQLTypeObject && depth > 0 && !declared) {
				in.errorAt("P5_OBJECT_LIST_MAX", fmt.Sprintf("GraphQL field %s.%s requires exactly one maximum per output list level", definition.Name, field.Name()), registration.call)
			}
			value = value.WithListMaxima(bounds)
			_, presence := graphQLInputValueElement(field.Type(), in.config.GolemImportPath)
			definition.Fields = append(definition.Fields, ir.GraphQLObjectFieldIR{Name: name, GoName: field.Name(), Type: value, PresenceAware: presence})
		}
		unused := make([]string, 0, len(maxima))
		for field := range maxima {
			unused = append(unused, field)
		}
		sort.Strings(unused)
		for _, field := range unused {
			in.errorAt("P5_OBJECT_LIST_MAX", fmt.Sprintf("GraphQL type %s has no bounded field %s", definition.Name, field), registration.call)
		}
		definitions = append(definitions, definition)
		gate := ""
		for _, expression := range registration.call.Args[2:] {
			option, ok := unparen(expression).(*ast.CallExpr)
			if !ok || in.callOperation(option) != "ObjectRowGate" {
				continue
			}
			if gate != "" || len(option.Args) != 1 {
				in.errorAt("P5_OBJECT_GATE", "output objects may declare one direct ObjectRowGate", option)
				continue
			}
			name, valid := in.constString(option.Args[0])
			if !valid || name == "" {
				in.errorAt("P5_OBJECT_GATE", "row gate requires a constant Go field name", option)
				continue
			}
			gate = name
		}
		if gate != "" {
			for _, field := range definition.Fields {
				if field.GoName == gate {
					definition.RowGate = field.Name
					break
				}
			}
			if definition.RowGate == "" {
				in.errorAt("P5_OBJECT_GATE", fmt.Sprintf("row gate field %s.%s is absent", definition.Name, gate), registration.call)
			}
			definitions[len(definitions)-1] = definition
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	contract := in.config.Compilation.Contract
	contract.GraphQLTypes = definitions
	if err := ir.ValidateGraphQLTypeDefinitions(contract); err != nil {
		in.errorAt("P5_OBJECT_DEFINITION", err.Error(), declaration)
	}
	return definitions
}

func (in *interpreter) graphQLInputOneOf(call *ast.CallExpr) bool {
	oneOf := false
	for _, expression := range call.Args[2:] {
		option, ok := unparen(expression).(*ast.CallExpr)
		if !ok || in.callOperation(option) != "InputOneOf" || len(option.Args) != 0 {
			in.errorAt("P5_INPUT_OPTION", "input options must be direct InputOneOf declarations", expression)
			continue
		}
		if oneOf {
			in.errorAt("P5_INPUT_OPTION", "input declares InputOneOf more than once", option)
		}
		oneOf = true
	}
	return oneOf
}

func (in *interpreter) graphQLObjectMaxima(call *ast.CallExpr) map[string][]uint32 {
	result := make(map[string][]uint32)
	for _, expression := range call.Args[2:] {
		option, ok := unparen(expression).(*ast.CallExpr)
		if ok && in.callOperation(option) == "ObjectRowGate" {
			continue
		}
		if !ok || in.callOperation(option) != "ObjectFieldMaxItems" || len(option.Args) < 2 {
			in.errorAt("P5_OBJECT_LIST_MAX", "output options must be direct ObjectFieldMaxItems declarations", expression)
			continue
		}
		name, valid := in.constString(option.Args[0])
		if !valid {
			in.errorAt("P5_OBJECT_LIST_MAX", "bounded Go field name must be a constant string", option)
			continue
		}
		if _, exists := result[name]; exists {
			in.errorAt("P5_OBJECT_LIST_MAX", fmt.Sprintf("duplicate maximum for Go field %s", name), option)
			continue
		}
		values := make([]uint32, 0, len(option.Args)-1)
		for _, argument := range option.Args[1:] {
			maximum, valid := in.maxItemsConstant(argument)
			if !valid || maximum == 0 {
				in.errorAt("P5_OBJECT_LIST_MAX", "field maximum must be a positive uint32 constant", argument)
				continue
			}
			values = append(values, maximum)
		}
		result[name] = values
	}
	return result
}
