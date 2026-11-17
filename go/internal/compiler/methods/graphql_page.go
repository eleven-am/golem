package methods

import (
	"go/ast"
	"go/types"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

func (in *interpreter) pageTypeArgument(value types.Type, name string) (types.Type, bool) {
	named, ok := types.Unalias(value).(*types.Named)
	if !ok || named.Origin().Obj().Pkg() == nil || named.Origin().Obj().Pkg().Path() != in.config.GolemImportPath || named.Origin().Obj().Name() != name || named.TypeArgs().Len() != 1 {
		return nil, false
	}
	return named.TypeArgs().At(0), true
}

func (in *interpreter) evalPageOperation(call *ast.CallExpr, parameter *types.Var, definitions []ir.GraphQLTypeDefinitionIR) (graphqlextension.CustomOperationDeclaration, ir.GraphQLTypeDefinitionIR, bool) {
	var zero graphqlextension.CustomOperationDeclaration
	var empty ir.GraphQLTypeDefinitionIR
	fail := func(code, message string) (graphqlextension.CustomOperationDeclaration, ir.GraphQLTypeDefinitionIR, bool) {
		in.errorAt(code, message, call)
		return zero, empty, false
	}
	if in.config.Compilation.Contract.DataIncarnation == nil {
		return fail("P12_PAGE_DATA_INCARNATION", "PageQuery requires golem.WithDataIncarnation in DefineSchema")
	}
	if len(call.Args) < 3 || !in.isParameter(call.Args[0], parameter) {
		return fail("P5_PAGE_DECLARATION", "PageQuery requires the schema parameter, constant name, resolver, PageLimits and PageRevision")
	}
	name, valid := in.constString(call.Args[1])
	if !valid || name == "" {
		return fail("P5_PAGE_NAME", "page name must be a nonempty constant string")
	}
	callable, signature, valid := in.callable(call.Args[2], "customquery")
	if !valid {
		return zero, empty, false
	}
	if callable.Receiver != (ir.GoNamedTypeIR{}) || callable.PackagePath != in.pkg.PkgPath || !validCustomCallable(signature, in.pkg.PkgPath) {
		return fail("P5_PAGE_SIGNATURE", "page resolver must be a schema package function func(context.Context, *Caller[Principal], golem.PageRequest[Args]) (golem.PageBatch[Item], error)")
	}
	args, validArgs := in.pageTypeArgument(signature.Params().At(2).Type(), "PageRequest")
	item, validItem := in.pageTypeArgument(signature.Results().At(0).Type(), "PageBatch")
	if !validArgs || !validItem {
		return fail("P5_PAGE_SIGNATURE", "page resolver requires golem.PageRequest[Args] and golem.PageBatch[Item]")
	}
	arguments, valid := in.graphQLArguments(args, true, call.Args[2])
	if !valid {
		return zero, empty, false
	}
	for _, argument := range arguments {
		if argument.Name == "take" || argument.Name == "after" {
			return fail("P5_PAGE_ARGUMENT", "page arguments reserve take and after")
		}
		if !ir.IsPageArgumentType(argument.Type) {
			return fail("P5_PAGE_ARGUMENT", "page arguments permit only scalar, enum, list and registered input-object values")
		}
	}
	if err := ir.ValidatePageArguments(arguments, definitions); err != nil {
		return fail("P5_PAGE_ARGUMENT", err.Error())
	}
	itemType, valid := in.graphQLTypeFromGo(item, graphQLCustomResult)
	if !valid || itemType.Nullable || itemType.Kind == ir.GraphQLTypeList {
		return fail("P5_PAGE_ITEM", "page items must be non-null scalar, enum, registered output object or model rows")
	}
	pagination := &ir.CustomPaginationIR{Item: itemType}
	limits, revision := false, false
	for _, expression := range call.Args[3:] {
		option, ok := unparen(expression).(*ast.CallExpr)
		if !ok {
			return fail("P5_PAGE_OPTION", "page options must be direct PageLimits and PageRevision calls")
		}
		switch in.callOperation(option) {
		case "InvalidateRows", "InvalidateFacts":
			continue
		case "PageLimits":
			if limits || len(option.Args) != 3 {
				return fail("P5_PAGE_LIMITS", "PageLimits must occur exactly once with defaultTake, maxTake and maxCandidates")
			}
			values := []*uint32{&pagination.DefaultTake, &pagination.MaxTake, &pagination.MaxCandidates}
			for index, target := range values {
				value, constant := in.maxItemsConstant(option.Args[index])
				if !constant {
					return fail("P5_PAGE_LIMITS", "page limits must be uint32 constants")
				}
				*target = value
			}
			limits = true
		case "PageRevision":
			if revision || len(option.Args) != 1 {
				return fail("P5_PAGE_REVISION", "PageRevision must occur exactly once with a nonempty constant string")
			}
			value, constant := in.constString(option.Args[0])
			if !constant || strings.TrimSpace(value) == "" {
				return fail("P5_PAGE_REVISION", "page revision must be a nonempty constant string")
			}
			pagination.Revision, revision = value, true
		default:
			return fail("P5_PAGE_OPTION", "page options must be direct PageLimits and PageRevision calls")
		}
	}
	if !limits || !revision {
		return fail("P5_PAGE_OPTION_REQUIRED", "PageQuery requires both PageLimits and PageRevision")
	}
	if err := pagination.Validate(); err != nil {
		return fail("P5_PAGE_LIMITS", err.Error())
	}
	identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, ir.OwnedIdentity(string(in.config.Compilation.Model.Schema.ID), "graphql-custom\x00query\x00"+name), in.span(call))
	if diagnostic != nil {
		in.diagnostics = append(in.diagnostics, *diagnostic)
		return zero, empty, false
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	pageName := string(runes) + "Page"
	definition := ir.GraphQLPageDefinition(pageName, *pagination)
	arguments = append(arguments,
		ir.GraphQLArgumentContractIR{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
		ir.GraphQLArgumentContractIR{Name: "after", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: true}},
	)
	operation := ir.CustomOperationContractIR{
		ExtensionID: ir.ExtensionIDFrom(identity), Operation: ir.CustomOperationQuery, Name: name, Arguments: arguments,
		Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: pageName}, Resolver: callable, Capability: ir.CustomOperationCallerOnly, Pagination: pagination,
	}
	pending, valid := in.customInvalidations(call.Args[3:], &operation, args, definitions)
	if !valid {
		return zero, empty, false
	}
	return graphqlextension.CustomOperationDeclaration{Span: in.span(call), Operation: operation, InvalidationTemplates: pending}, definition, true
}
