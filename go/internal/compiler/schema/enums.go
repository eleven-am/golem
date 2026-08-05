package schema

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/load"
)

type enumConstant struct {
	goName string
	wire   string
	span   ir.SourceSpan
}

func (c *compiler) extractEnums(pkg *load.Package) {
	constants := enumConstants(pkg)
	for _, file := range pkg.Files {
		aliases := importAliases(file.AST)
		golemAlias := ""
		for alias, path := range aliases {
			if path == golemImportPath {
				golemAlias = alias
				break
			}
		}
		for _, declaration := range file.AST.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Name.Name != "GolemEnum" || method.Recv == nil || len(method.Recv.List) != 1 {
				continue
			}
			enumName := receiverTypeName(method.Recv.List[0].Type)
			if enumName == "" {
				c.error(pkg, "P1_ENUM_RECEIVER", "GolemEnum receiver must be a named type", method.Recv.List[0])
				continue
			}
			typeSpec := findTypeSpec(pkg, enumName)
			if typeSpec == nil || typeSpec.Assign.IsValid() || typeSpec.TypeParams != nil || !isIdent(typeSpec.Type, "string") {
				c.error(pkg, "P1_ENUM_TYPE", fmt.Sprintf("enum %s must be a non-generic named string type", enumName), method)
				continue
			}
			if golemAlias == "" {
				c.error(pkg, "P1_ENUM_GOLEM_IMPORT", "GolemEnum method must import the golem declaration package", method)
				continue
			}
			valueNames, ok := enumMethodValues(method, golemAlias)
			if !ok {
				c.error(pkg, "P1_ENUM_METHOD_BODY", "GolemEnum must return golem.DefineEnum with direct golem.EnumValue constant calls", method)
				continue
			}
			raw := ir.RawEnumDecl{
				PackagePath: pkg.ImportPath, GoName: enumName, Underlying: "string",
				Method: ir.RawMethodRef{ReceiverPackage: pkg.ImportPath, ReceiverGoName: enumName, Name: "GolemEnum", Span: sourceSpan(pkg, method.Pos(), method.End())},
				Span:   sourceSpan(pkg, typeSpec.Pos(), typeSpec.End()),
			}
			seen := map[string]bool{}
			for _, valueName := range valueNames {
				constant, exists := constants[enumName+"\x00"+valueName]
				if !exists {
					c.error(pkg, "P1_ENUM_VALUE_CONSTANT", fmt.Sprintf("%s is not a typed string constant of %s", valueName, enumName), method)
					continue
				}
				if seen[valueName] {
					c.error(pkg, "P1_ENUM_VALUE_DUPLICATE", fmt.Sprintf("enum constant %s is listed more than once", valueName), method)
					continue
				}
				seen[valueName] = true
				raw.Values = append(raw.Values, ir.RawEnumValue{GoName: valueName, WireValue: constant.wire, Ordinal: uint32(len(raw.Values)), Span: constant.span})
			}
			c.enums = append(c.enums, raw)
		}
	}
}

func enumConstants(pkg *load.Package) map[string]enumConstant {
	result := map[string]enumConstant{}
	for _, file := range pkg.Files {
		for _, declaration := range file.AST.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, rawSpec := range general.Specs {
				spec, ok := rawSpec.(*ast.ValueSpec)
				if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
					continue
				}
				typeName, ok := spec.Type.(*ast.Ident)
				if !ok {
					continue
				}
				wire, ok := stringLiteral(spec.Values[0])
				if !ok {
					continue
				}
				name := spec.Names[0].Name
				result[typeName.Name+"\x00"+name] = enumConstant{goName: name, wire: wire, span: sourceSpan(pkg, spec.Pos(), spec.End())}
			}
		}
	}
	return result
}

func enumMethodValues(method *ast.FuncDecl, golemAlias string) ([]string, bool) {
	if method.Body == nil || len(method.Body.List) != 1 {
		return nil, false
	}
	returnStatement, ok := method.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		return nil, false
	}
	defineCall, ok := returnStatement.Results[0].(*ast.CallExpr)
	if !ok || !isPackageSelector(defineCall.Fun, golemAlias, "DefineEnum") {
		return nil, false
	}
	values := make([]string, 0, len(defineCall.Args))
	for _, argument := range defineCall.Args {
		valueCall, ok := argument.(*ast.CallExpr)
		if !ok || !isPackageSelector(valueCall.Fun, golemAlias, "EnumValue") || len(valueCall.Args) != 1 {
			return nil, false
		}
		identifier, ok := valueCall.Args[0].(*ast.Ident)
		if !ok {
			return nil, false
		}
		values = append(values, identifier.Name)
	}
	return values, true
}

func isPackageSelector(expression ast.Expr, packageName, selectorName string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == selectorName && isIdent(selector.X, packageName)
}

func findTypeSpec(pkg *load.Package, name string) *ast.TypeSpec {
	for _, file := range pkg.Files {
		for _, declaration := range file.AST.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if ok && typeSpec.Name.Name == name {
					return typeSpec
				}
			}
		}
	}
	return nil
}
