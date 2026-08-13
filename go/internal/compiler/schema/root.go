package schema

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"strconv"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/load"
)

const golemImportPath = "github.com/eleven-am/golem/go/golem"

var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var embeddingSpaceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func (c *compiler) extractRoot(pkg *load.Package) *ir.RawSchemaDecl {
	type candidate struct {
		file *ast.File
		decl *ast.FuncDecl
	}
	var candidates []candidate
	for _, file := range pkg.Files {
		for _, declaration := range file.AST.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.Name == c.rootName && function.Recv == nil {
				candidates = append(candidates, candidate{file: file.AST, decl: function})
			}
		}
	}
	if len(candidates) == 0 {
		c.diagnostics = append(c.diagnostics, Diagnostic{Code: "P1_SCHEMA_ROOT_MISSING", Severity: SeverityError, Message: fmt.Sprintf("package %s does not declare func %s", pkg.ImportPath, c.rootName)})
		return nil
	}
	if len(candidates) > 1 {
		for _, item := range candidates {
			c.error(pkg, "P1_SCHEMA_ROOT_MULTIPLE", fmt.Sprintf("multiple functions named %s", c.rootName), item.decl)
		}
		return nil
	}

	item := candidates[0]
	function := item.decl
	aliases := importAliases(item.file)
	golemAlias := ""
	for alias, path := range aliases {
		if path == golemImportPath {
			golemAlias = alias
			break
		}
	}
	if golemAlias == "" {
		c.error(pkg, "P1_SCHEMA_GOLEM_IMPORT", fmt.Sprintf("%s must import %s", c.rootName, golemImportPath), function)
		return nil
	}
	parameter := validateRootSignature(c, pkg, function, golemAlias)
	if parameter == "" {
		return nil
	}

	root := &ir.RawSchemaDecl{
		PackagePath:   pkg.ImportPath,
		FunctionName:  c.rootName,
		ParameterName: parameter,
		Span:          sourceSpan(pkg, function.Pos(), function.End()),
	}
	if function.Body == nil {
		c.error(pkg, "P1_SCHEMA_ROOT_BODY", "schema root must have a body", function)
		return root
	}

	seenName := false
	seenActor := false
	seenProviders := false
	embeddingSpaces := map[string]bool{}
	models := make(map[string]token.Pos)
	for _, statement := range function.Body.List {
		if _, ok := statement.(*ast.EmptyStmt); ok {
			continue
		}
		expressionStatement, ok := statement.(*ast.ExprStmt)
		if !ok {
			c.error(pkg, "P1_SCHEMA_BODY_STATEMENT", "schema root permits only direct registered declaration calls", statement)
			continue
		}
		call, ok := expressionStatement.X.(*ast.CallExpr)
		if !ok {
			c.error(pkg, "P1_SCHEMA_BODY_STATEMENT", "schema root permits only direct registered declaration calls", expressionStatement)
			continue
		}

		name, typeArgument, valid := declarationCall(call, golemAlias)
		if !valid {
			c.error(pkg, "P1_SCHEMA_BODY_CALL", "call is not a registered golem schema declaration", call)
			continue
		}
		switch name {
		case "SchemaName":
			if typeArgument != nil || len(call.Args) != 2 || !isIdent(call.Args[0], parameter) {
				c.error(pkg, "P1_SCHEMA_NAME_CALL", "SchemaName requires (schema, string literal)", call)
				continue
			}
			value, ok := stringLiteral(call.Args[1])
			if !ok || !schemaNamePattern.MatchString(value) {
				c.error(pkg, "P1_SCHEMA_NAME_LITERAL", "SchemaName requires a literal matching [a-z][a-z0-9_]{0,62}", call.Args[1])
				continue
			}
			if seenName {
				c.error(pkg, "P1_SCHEMA_NAME_DUPLICATE", "SchemaName may be declared once", call)
				continue
			}
			seenName = true
			root.SchemaName = value
			root.SchemaNameSpan = sourceSpan(pkg, call.Args[1].Pos(), call.Args[1].End())

		case "Actor":
			if typeArgument == nil || len(call.Args) != 1 || !isIdent(call.Args[0], parameter) {
				c.error(pkg, "P1_SCHEMA_ACTOR_CALL", "Actor requires one named type argument and the schema parameter", call)
				continue
			}
			actor, ok := namedTypeRef(pkg, aliases, typeArgument)
			if !ok {
				c.error(pkg, "P1_SCHEMA_ACTOR_TYPE", "Actor type argument must be a named type", typeArgument)
				continue
			}
			if seenActor {
				c.error(pkg, "P1_SCHEMA_ACTOR_DUPLICATE", "schema must register exactly one actor", call)
				continue
			}
			seenActor = true
			root.Actor = &actor

		case "Model":
			if typeArgument == nil || len(call.Args) != 1 || !isIdent(call.Args[0], parameter) {
				c.error(pkg, "P1_SCHEMA_MODEL_CALL", "Model requires one named type argument and the schema parameter", call)
				continue
			}
			model, ok := namedTypeRef(pkg, aliases, typeArgument)
			if !ok {
				c.error(pkg, "P1_SCHEMA_MODEL_TYPE", "Model type argument must be a named type", typeArgument)
				continue
			}
			key := model.PackagePath + "." + model.GoName
			if _, duplicate := models[key]; duplicate {
				c.error(pkg, "P1_SCHEMA_MODEL_DUPLICATE", fmt.Sprintf("model %s is registered more than once", key), call)
				continue
			}
			models[key] = call.Pos()
			root.Models = append(root.Models, ir.RawModelRef{
				PackagePath: model.PackagePath, GoName: model.GoName,
				Ordinal: uint32(len(root.Models)), Span: model.Span,
			})

		case "Providers":
			if typeArgument != nil || len(call.Args) < 2 || !isIdent(call.Args[0], parameter) {
				c.error(pkg, "P1_SCHEMA_PROVIDERS_CALL", "Providers requires the schema parameter and at least one typed provider constant", call)
				continue
			}
			if seenProviders {
				c.error(pkg, "P1_SCHEMA_PROVIDERS_DUPLICATE", "Providers may be declared once", call)
				continue
			}
			seenProviders = true
			seen := map[ir.Provider]bool{}
			for _, expression := range call.Args[1:] {
				provider, ok := providerConstant(expression, golemAlias)
				if !ok {
					c.error(pkg, "P1_SCHEMA_PROVIDER_CONSTANT", "provider must be golem.SQLite or golem.PostgreSQL", expression)
					continue
				}
				if seen[provider] {
					c.error(pkg, "P1_SCHEMA_PROVIDER_DUPLICATE", fmt.Sprintf("provider %s is duplicated", provider), expression)
					continue
				}
				seen[provider] = true
				root.Providers = append(root.Providers, ir.RawProviderRef{Provider: provider, Ordinal: uint32(len(root.Providers)), Span: sourceSpan(pkg, expression.Pos(), expression.End())})
			}

		case "EmbeddingSpace":
			if typeArgument != nil || len(call.Args) != 3 || !isIdent(call.Args[0], parameter) {
				c.error(pkg, "P9_EMBEDDING_SPACE_CALL", "EmbeddingSpace requires (schema, name, dimensions)", call)
				continue
			}
			name, nameOK := stringLiteral(call.Args[1])
			dimensions, dimensionOK := uint16Literal(call.Args[2])
			if !nameOK || !embeddingSpaceNamePattern.MatchString(name) {
				c.error(pkg, "P9_EMBEDDING_SPACE_NAME", "embedding space name must match [a-z][a-z0-9_-]{0,62}", call.Args[1])
				continue
			}
			if !dimensionOK || dimensions < 1 || dimensions > 2000 {
				c.error(pkg, "P9_EMBEDDING_SPACE_DIMENSIONS", "embedding space dimensions must be a literal between 1 and 2000", call.Args[2])
				continue
			}
			if embeddingSpaces[name] {
				c.error(pkg, "P9_EMBEDDING_SPACE_DUPLICATE", "embedding space names must be unique", call)
				continue
			}
			embeddingSpaces[name] = true
			root.EmbeddingSpaces = append(root.EmbeddingSpaces, ir.RawEmbeddingSpace{Name: name, Dimensions: dimensions, Span: sourceSpan(pkg, call.Pos(), call.End())})

		default:
			c.error(pkg, "P1_SCHEMA_BODY_CALL", fmt.Sprintf("golem.%s is not permitted in the schema root", name), call)
		}
	}
	if !seenName {
		c.error(pkg, "P1_SCHEMA_NAME_MISSING", "schema root must call golem.SchemaName exactly once", function)
	}
	if !seenActor {
		c.error(pkg, "P1_SCHEMA_ACTOR_MISSING", "schema root must register exactly one actor", function)
	}
	if len(root.Models) == 0 {
		c.error(pkg, "P1_SCHEMA_MODEL_MISSING", "schema root must register at least one model", function)
	}
	return root
}

func validateRootSignature(c *compiler, pkg *load.Package, function *ast.FuncDecl, golemAlias string) string {
	if function.Type.TypeParams != nil || function.Type.Results != nil && len(function.Type.Results.List) != 0 || function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		c.error(pkg, "P1_SCHEMA_ROOT_SIGNATURE", "schema root signature must be func DefineSchema(schema *golem.Schema)", function.Type)
		return ""
	}
	field := function.Type.Params.List[0]
	if len(field.Names) != 1 || field.Names[0].Name == "_" {
		c.error(pkg, "P1_SCHEMA_ROOT_SIGNATURE", "schema root parameter must have one non-blank name", field)
		return ""
	}
	pointer, ok := field.Type.(*ast.StarExpr)
	if !ok {
		c.error(pkg, "P1_SCHEMA_ROOT_SIGNATURE", "schema root parameter must be *golem.Schema", field.Type)
		return ""
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Schema" || !isIdent(selector.X, golemAlias) {
		c.error(pkg, "P1_SCHEMA_ROOT_SIGNATURE", "schema root parameter must be *golem.Schema", field.Type)
		return ""
	}
	return field.Names[0].Name
}

func declarationCall(call *ast.CallExpr, golemAlias string) (string, ast.Expr, bool) {
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		if isIdent(function.X, golemAlias) {
			return function.Sel.Name, nil, true
		}
	case *ast.IndexExpr:
		selector, ok := function.X.(*ast.SelectorExpr)
		if ok && isIdent(selector.X, golemAlias) {
			return selector.Sel.Name, function.Index, true
		}
	}
	return "", nil, false
}

func namedTypeRef(pkg *load.Package, aliases map[string]string, expression ast.Expr) (ir.RawNamedTypeRef, bool) {
	ref := ir.RawNamedTypeRef{Span: sourceSpan(pkg, expression.Pos(), expression.End())}
	switch value := expression.(type) {
	case *ast.Ident:
		ref.PackagePath, ref.GoName = pkg.ImportPath, value.Name
	case *ast.SelectorExpr:
		qualifier, ok := value.X.(*ast.Ident)
		if !ok || aliases[qualifier.Name] == "" {
			return ir.RawNamedTypeRef{}, false
		}
		ref.PackagePath, ref.GoName = aliases[qualifier.Name], value.Sel.Name
	default:
		return ir.RawNamedTypeRef{}, false
	}
	if !ast.IsExported(ref.GoName) {
		return ir.RawNamedTypeRef{}, false
	}
	return ref, true
}

func providerConstant(expression ast.Expr, golemAlias string) (ir.Provider, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !isIdent(selector.X, golemAlias) {
		return "", false
	}
	switch selector.Sel.Name {
	case "SQLite":
		return ir.SQLite, true
	case "PostgreSQL":
		return ir.PostgreSQL, true
	default:
		return "", false
	}
}

func isIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func uint16Literal(expression ast.Expr) (uint16, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseUint(literal.Value, 10, 16)
	return uint16(value), err == nil
}

func unquoteString(value string) string {
	result, _ := strconv.Unquote(value)
	return result
}
