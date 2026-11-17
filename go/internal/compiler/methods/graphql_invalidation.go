package methods

import (
	"go/ast"
	"go/types"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
)

func (in *interpreter) customInvalidations(options []ast.Expr, operation *ir.CustomOperationContractIR, args types.Type, definitions []ir.GraphQLTypeDefinitionIR) (map[ir.ExtensionID]graphqlextension.InvalidationTemplateDeclaration, bool) {
	var pending map[ir.ExtensionID]graphqlextension.InvalidationTemplateDeclaration
	valid := true
	names := map[string]bool{}
	for _, expression := range options {
		call, direct := unparen(expression).(*ast.CallExpr)
		if !direct {
			continue
		}
		kind := in.callOperation(call)
		if kind != "InvalidateRows" && kind != "InvalidateFacts" {
			continue
		}
		fail := func(detail string) { in.errorAt("P07_INVALIDATION_DECLARATION", detail, expression); valid = false }
		if operation.Operation != ir.CustomOperationQuery {
			fail("invalidation is supported only on Query and PageQuery")
			continue
		}
		minimum, binderIndex := 3, 1
		if kind == "InvalidateFacts" {
			minimum, binderIndex = 4, 2
		}
		if len(call.Args) < minimum || call.Ellipsis.IsValid() {
			fail("invalidation requires a constant name, direct binder and nonempty explicit member inventory")
			continue
		}
		name, constant := in.constString(call.Args[0])
		if !constant || name == "" || strings.TrimSpace(name) != name || len(name) > 256 || names[name] {
			fail("invalidation names must be unique nonempty constants of at most 256 bytes")
			continue
		}
		names[name] = true
		var modelExpression ast.Expr
		switch fn := unparen(call.Fun).(type) {
		case *ast.IndexExpr:
			modelExpression = fn.Index
		case *ast.IndexListExpr:
			if len(fn.Indices) == 1 {
				modelExpression = fn.Indices[0]
			}
		}
		if modelExpression == nil {
			fail("invalidation requires one explicit registered model type")
			continue
		}
		modelType, named := types.Unalias(in.pkg.TypesInfo.TypeOf(modelExpression)).(*types.Named)
		if !named || modelType.Obj().Pkg() == nil || modelType.TypeArgs().Len() != 0 {
			fail("invalidation source must be a registered non-generic model")
			continue
		}
		var modelID ir.ModelID
		for _, model := range in.config.Compilation.Model.Models {
			if model.Go == goNamedType(modelType) {
				modelID = model.ID
			}
		}
		if modelID == "" {
			fail("invalidation source model is not registered")
			continue
		}
		binder, signature, callable := in.callable(call.Args[binderIndex], "invalidation")
		if !callable {
			valid = false
			continue
		}
		if binder.Receiver != (ir.GoNamedTypeIR{}) || binder.PackagePath != in.pkg.PkgPath || signature.Variadic() || signature.TypeParams().Len() != 0 || signature.Params().Len() != 3 || signature.Results().Len() != 2 || !isContext(signature.Params().At(0).Type()) || !isError(signature.Results().At(1).Type()) || !types.Identical(signature.Params().At(2).Type(), args) {
			fail("invalidation binder must be a non-generic schema-package function func(context.Context, Actor, Args) (Predicate[Model] or BoundFactScope[Model], error)")
			continue
		}
		actor, actorNamed := types.Unalias(signature.Params().At(1).Type()).(*types.Named)
		if !actorNamed || goNamedType(actor) != in.config.Compilation.Model.Schema.Actor {
			fail("invalidation binder must receive the exact schema actor by value")
			continue
		}
		expected := "Predicate"
		if kind == "InvalidateFacts" {
			expected = "BoundFactScope"
		}
		resultModel, correct := in.pageTypeArgument(signature.Results().At(0).Type(), expected)
		if !correct || !types.Identical(resultModel, modelType) {
			fail("invalidation binder result must match the declared source model and observation kind")
			continue
		}
		actorIdentity := in.config.Compilation.Model.Schema.Actor
		binder.Actor = &actorIdentity
		identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, ir.OwnedIdentity(string(operation.ExtensionID), "graphql-invalidation\x00"+name), in.span(call))
		if diagnostic != nil {
			in.diagnostics = append(in.diagnostics, *diagnostic)
			valid = false
			continue
		}
		dependency := ir.CustomInvalidationDependencyIR{ID: ir.ExtensionIDFrom(identity), Name: name, ModelID: modelID, Binder: binder}
		if len(call.Args)-binderIndex-1 > invalidationcontract.MaxMembers {
			fail("invalidation member inventory exceeds 64")
			continue
		}
		if kind == "InvalidateRows" {
			dependency.Kind = ir.CustomInvalidationReadableRows
			seen := map[ir.FieldID]bool{}
			for _, member := range call.Args[2:] {
				symbol, found := in.invalidationField(member)
				if !found || symbol.Kind != modelcodegen.SymbolField || symbol.ModelID != modelID || seen[symbol.FieldID] {
					fail("invalidation watch members must be distinct generated scalar fields of its source model")
					continue
				}
				seen[symbol.FieldID] = true
				dependency.Fields = append(dependency.Fields, symbol.FieldID)
			}
		} else {
			dependency.Kind = ir.CustomInvalidationPolicyFacts
			scope, constant := in.constString(call.Args[1])
			if !constant || scope == "" {
				fail("invalidation scope template must be a nonempty constant name")
				continue
			}
			references := graphqlextension.InvalidationTemplateDeclaration{Scope: scope}
			seen := map[string]bool{}
			for _, member := range call.Args[3:] {
				fact, constant := in.constString(member)
				if !constant || fact == "" || seen[fact] {
					fail("invalidation facts must be distinct nonempty constant template names")
					continue
				}
				seen[fact] = true
				references.Facts = append(references.Facts, fact)
			}
			if pending == nil {
				pending = map[ir.ExtensionID]graphqlextension.InvalidationTemplateDeclaration{}
			}
			pending[dependency.ID] = references
		}
		if operation.Invalidation == nil {
			operation.Invalidation = &ir.CustomInvalidationIR{SubscriptionField: operation.Name + "Changes"}
		}
		operation.Invalidation.Dependencies = append(operation.Invalidation.Dependencies, dependency)
		if len(operation.Invalidation.Dependencies) > invalidationcontract.MaxDependencies {
			fail("invalidation dependency inventory exceeds 32")
		}
	}
	if operation.Invalidation != nil {
		if err := ir.ValidatePageArguments(operation.InvalidationArguments(), definitions); err != nil {
			in.errorAt("P07_INVALIDATION_ARGUMENT", "invalidation arguments require canonical scalar, enum, list or registered inputs: "+err.Error(), nil)
			valid = false
		}
	}
	return pending, valid
}

func (in *interpreter) invalidationField(expression ast.Expr) (modelcodegen.Symbol, bool) {
	selector, ok := unparen(expression).(*ast.SelectorExpr)
	if !ok || in.scopeIndex == nil {
		return modelcodegen.Symbol{}, false
	}
	selection := in.pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return modelcodegen.Symbol{}, false
	}
	var namespace types.Object
	switch expr := unparen(selector.X).(type) {
	case *ast.Ident:
		namespace = in.pkg.TypesInfo.Uses[expr]
	case *ast.SelectorExpr:
		namespace = in.pkg.TypesInfo.Uses[expr.Sel]
	}
	if namespace == nil || namespace.Pkg() == nil {
		return modelcodegen.Symbol{}, false
	}
	in.scopeIndex.prepare()
	symbol, present := in.scopeIndex.symbols[scopeSymbolKey{namespace.Pkg().Path(), namespace.Name(), selection.Obj().Name()}]
	return symbol, present
}
