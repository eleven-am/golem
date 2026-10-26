package methods

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type TxOperationDeclaration struct {
	Operation ir.TxOperationContractIR
	Span      ir.SourceSpan
}

func interpretOperations(config Config, loaded loaded) ([]TxOperationDeclaration, []ir.Diagnostic) {
	packagePath := config.Compilation.Model.Schema.PackagePath
	if packagePath == "" {
		return nil, nil
	}
	pkg := loaded.packages[packagePath]
	if pkg == nil {
		return nil, []ir.Diagnostic{ir.NewError("P11_OPERATION_PACKAGE", fmt.Sprintf("schema package %q was not loaded", packagePath), ir.SourceSpan{})}
	}
	var declarations []*ast.FuncDecl
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "DefineOperations" {
				declarations = append(declarations, function)
			}
		}
	}
	if len(declarations) == 0 {
		return nil, nil
	}
	vocab, diagnostics := buildVocabulary(pkg, config.GolemImportPath)
	entry := &interpreter{config: config, pkg: pkg, vocab: vocab}
	if len(declarations) != 1 {
		for _, declaration := range declarations {
			entry.errorAt("P11_OPERATION_DEFINE_DUPLICATE", "schema package has multiple DefineOperations declarations", declaration)
		}
		return nil, append(diagnostics, entry.diagnostics...)
	}
	declaration := declarations[0]
	function, _ := pkg.TypesInfo.Defs[declaration.Name].(*types.Func)
	var signature *types.Signature
	if function != nil {
		signature, _ = function.Type().(*types.Signature)
	}
	if signature == nil || signature.Params().Len() != 1 || signature.Results().Len() != 0 || !isOperationsPointer(signature.Params().At(0).Type(), config.GolemImportPath) {
		entry.errorAt("P11_OPERATION_DEFINE_SIGNATURE", "schema DefineOperations must have signature func(*golem.Operations)", declaration.Type)
		return nil, append(diagnostics, entry.diagnostics...)
	}
	if declaration.Body == nil {
		entry.errorAt("P11_OPERATION_DEFINE_BODY", "schema DefineOperations requires a direct declaration body", declaration)
		return nil, append(diagnostics, entry.diagnostics...)
	}
	parameter := signature.Params().At(0)
	names := map[string]bool{}
	functions := map[*types.Func]bool{}
	var result []TxOperationDeclaration
	for _, statement := range declaration.Body.List {
		expressionStatement, ok := statement.(*ast.ExprStmt)
		if !ok {
			entry.errorAt("P11_OPERATION_DEFINE_STATEMENT", "schema DefineOperations permits only direct golem.TxOperation calls", statement)
			continue
		}
		call, ok := unparen(expressionStatement.X).(*ast.CallExpr)
		if !ok || entry.callOperation(call) != "TxOperation" {
			entry.errorAt("P11_OPERATION_DEFINE_CALL", "schema DefineOperations accepts only direct golem.TxOperation calls", expressionStatement)
			continue
		}
		if operation, valid := entry.evalTxOperation(call, parameter, names, functions); valid {
			result = append(result, operation)
		}
	}
	diagnostics = append(diagnostics, entry.diagnostics...)
	if hasErrors(entry.diagnostics) {
		return nil, diagnostics
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation.Name < result[j].Operation.Name })
	return result, diagnostics
}

func (in *interpreter) evalTxOperation(call *ast.CallExpr, parameter *types.Var, names map[string]bool, functions map[*types.Func]bool) (TxOperationDeclaration, bool) {
	if len(call.Args) != 3 || !in.isParameter(call.Args[0], parameter) {
		in.errorAt("P11_OPERATION_ARITY", "transaction operation requires the operations destination, name, and function", call)
		return TxOperationDeclaration{}, false
	}
	name, valid := in.constString(call.Args[1])
	if !valid {
		in.errorAt("P11_OPERATION_NAME", "transaction operation name must be a constant string", call.Args[1])
		return TxOperationDeclaration{}, false
	}
	if !token.IsIdentifier(name) {
		in.errorAt("P11_OPERATION_NAME", fmt.Sprintf("transaction operation name %q must be a Go identifier", name), call.Args[1])
		return TxOperationDeclaration{}, false
	}
	var function *types.Func
	if identifier, ok := unparen(call.Args[2]).(*ast.Ident); ok {
		function, _ = in.pkg.TypesInfo.Uses[identifier].(*types.Func)
	}
	signature, _ := in.pkg.TypesInfo.TypeOf(call.Args[2]).(*types.Signature)
	if function == nil || signature == nil || function.Pkg() == nil || function.Pkg().Path() != in.pkg.PkgPath || function.Parent() != function.Pkg().Scope() {
		in.errorAt("P11_OPERATION_FUNCTION", "transaction operation must name a package-level function of the schema package", call.Args[2])
		return TxOperationDeclaration{}, false
	}
	if !validTxOperationCallable(signature, in.pkg.PkgPath) {
		in.errorAt("P11_OPERATION_SIGNATURE", "transaction operation must be func(context.Context, *CallerTx[Principal], Args) (Result, error)", call.Args[2])
		return TxOperationDeclaration{}, false
	}
	if names[name] {
		in.errorAt("P11_OPERATION_DUPLICATE", fmt.Sprintf("transaction operation %q is declared more than once", name), call.Args[1])
		return TxOperationDeclaration{}, false
	}
	if functions[function] {
		in.errorAt("P11_OPERATION_DUPLICATE", fmt.Sprintf("function %s is declared as more than one transaction operation", function.Name()), call.Args[2])
		return TxOperationDeclaration{}, false
	}
	names[name] = true
	functions[function] = true
	identity, diagnostic := in.config.IDRegistry.Register(ir.ObjectExtension, ir.OwnedIdentity(string(in.config.Compilation.Model.Schema.ID), "tx-operation\x00"+name), in.span(call))
	if diagnostic != nil {
		in.diagnostics = append(in.diagnostics, *diagnostic)
		return TxOperationDeclaration{}, false
	}
	return TxOperationDeclaration{Span: in.span(call), Operation: ir.TxOperationContractIR{
		ExtensionID: ir.ExtensionIDFrom(identity), Name: name,
		Operation: ir.AttachedMethodIR{PackagePath: function.Pkg().Path(), Name: function.Name(), Kind: "txoperation"},
	}}, true
}

func validTxOperationCallable(signature *types.Signature, schemaPackage string) bool {
	return signature != nil && signature.Params().Len() == 3 && signature.Results().Len() == 2 && !signature.Variadic() && isContext(signature.Params().At(0).Type()) && isCallerTxPointer(signature.Params().At(1).Type(), schemaPackage) && isError(signature.Results().At(1).Type())
}

func isCallerTxPointer(value types.Type, schemaPackage string) bool {
	pointer, _ := value.(*types.Pointer)
	named, _ := pointerElem(pointer).(*types.Named)
	return named != nil && named.Origin().Obj().Pkg() != nil && named.Origin().Obj().Pkg().Path() == schemaPackage && named.Origin().Obj().Name() == "CallerTx" && named.TypeArgs().Len() == 1
}

func isOperationsPointer(value types.Type, golemPath string) bool {
	pointer, _ := value.(*types.Pointer)
	named, _ := pointerElem(pointer).(*types.Named)
	return named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == golemPath && named.Obj().Name() == "Operations"
}
