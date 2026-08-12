package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/eleven-am/golem/go/internal/compatibility"
)

func TestFreezePublicGoInventoryUsesCompatibilityAuthority(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	rootRequests := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isCompatibilitySelector(call.Fun, "BuildAPIInventory") || len(call.Args) != 2 {
			return true
		}
		request, ok := call.Args[1].(*ast.CompositeLit)
		if !ok {
			return true
		}
		fields := keyedFields(request)
		root, ok := fields["Directory"].(*ast.Ident)
		if !ok || root.Name != "root" {
			return true
		}
		rootRequests++
		patterns, ok := fields["Patterns"].(*ast.CallExpr)
		if !ok || !isCompatibilitySelector(patterns.Fun, "PublicGoAPIPatterns") || len(patterns.Args) != 0 {
			t.Error("freeze public Go inventory does not use compatibility.PublicGoAPIPatterns")
		}
		return true
	})
	if rootRequests != 1 {
		t.Fatalf("freeze public Go inventory request count=%d; want 1", rootRequests)
	}
	if !slices.Contains(compatibility.PublicGoAPIPatterns(), "./queryplan") {
		t.Fatal("authoritative public Go inventory omits ./queryplan")
	}
}

func keyedFields(literal *ast.CompositeLit) map[string]ast.Expr {
	fields := make(map[string]ast.Expr, len(literal.Elts))
	for _, element := range literal.Elts {
		keyed, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyed.Key.(*ast.Ident)
		if ok {
			fields[key.Name] = keyed.Value
		}
	}
	return fields
}

func isCompatibilitySelector(expression ast.Expr, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == "compatibility"
}
