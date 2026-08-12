package p8mutation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

const isolatedMutationExecutionEnv = "GOLEM_RUN_P8_ISOLATED_MUTATIONS"
const globalCatalogTestExecutionEnv = "GOLEM_RUN_P8_GLOBAL_CATALOG_TESTS"

func isolatedMutationExecutionEnabled() bool {
	return os.Getenv(isolatedMutationExecutionEnv) == "1"
}

func globalCatalogTestExecutionEnabled() bool {
	return os.Getenv(globalCatalogTestExecutionEnv) == "1"
}

func TestP8EveryCatalogSandboxExecutionIsExplicitlyEnabled(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "catalog_") || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !containsRunnerCall(function.Body) {
				continue
			}
			if !runnerGuardDominates(function.Body.List) {
				t.Fatalf("%s %s executes a sandbox without an exact leading mutation-mode early-return guard", name, function.Name.Name)
			}
		}
	}
}

func TestP8MutationExecutionGuardRequiresDominatingExactEarlyReturn(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		want       bool
	}{
		{"isolated", `if !isolatedMutationExecutionEnabled() { return }; _ = Runner{Repository: repository}`, true},
		{"global", `if !globalCatalogTestExecutionEnabled() { return }; _ = Runner{Repository: repository}`, true},
		{"legacy", `if os.Getenv("GOLEM_RUN_EXACT_MUTATIONS") != "1" { return }; _ = Runner{Repository: repository}`, true},
		{"combined", `if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_EXACT_MUTATIONS") != "1" { return }; _ = Runner{Repository: repository}`, true},
		{"comment", `/* if !isolatedMutationExecutionEnabled() { return } */; _ = Runner{Repository: repository}`, false},
		{"non-returning", `if !isolatedMutationExecutionEnabled() { t.Log("disabled") }; _ = Runner{Repository: repository}`, false},
		{"inverted", `if isolatedMutationExecutionEnabled() { return }; _ = Runner{Repository: repository}`, false},
		{"after", `_ = Runner{Repository: repository}; if !isolatedMutationExecutionEnabled() { return }`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\nfunc gate() {"+testCase.body+"}", 0)
			if err != nil {
				t.Fatal(err)
			}
			function := parsed.Decls[0].(*ast.FuncDecl)
			if got := runnerGuardDominates(function.Body.List); got != testCase.want {
				t.Fatalf("runnerGuardDominates=%v want=%v", got, testCase.want)
			}
		})
	}
}

func runnerGuardDominates(statements []ast.Stmt) bool {
	for _, statement := range statements {
		if containsRunnerCall(statement) {
			return false
		}
		conditional, ok := statement.(*ast.IfStmt)
		if !ok || conditional.Init != nil || conditional.Else != nil || len(conditional.Body.List) != 1 {
			continue
		}
		if _, ok := conditional.Body.List[0].(*ast.ReturnStmt); !ok {
			continue
		}
		if exactMutationGuard(conditional.Cond) {
			return true
		}
	}
	return false
}

func exactMutationGuard(expression ast.Expr) bool {
	if unary, ok := expression.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		return zeroArgumentCallNamed(unary.X, "isolatedMutationExecutionEnabled") || zeroArgumentCallNamed(unary.X, "globalCatalogTestExecutionEnabled")
	}
	binary, ok := expression.(*ast.BinaryExpr)
	if ok && binary.Op == token.LAND {
		return exactMutationGuard(binary.X) && exactMutationGuard(binary.Y)
	}
	if !ok || binary.Op != token.NEQ {
		return false
	}
	call, ok := binary.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	packageName, packageOK := selector.X.(*ast.Ident)
	literal, literalOK := binary.Y.(*ast.BasicLit)
	argument, argumentOK := call.Args[0].(*ast.BasicLit)
	return ok && packageOK && packageName.Name == "os" && selector.Sel.Name == "Getenv" && argumentOK && argument.Kind == token.STRING &&
		strings.HasPrefix(argument.Value, `"GOLEM_RUN_`) && literalOK && literal.Kind == token.STRING && literal.Value == `"1"`
}

func zeroArgumentCallNamed(expression ast.Expr, name string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	identifier, ok := call.Fun.(*ast.Ident)
	return ok && identifier.Name == name
}

func containsRunnerCall(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		literal, ok := candidate.(*ast.CompositeLit)
		if !ok {
			return !found
		}
		identifier, ok := literal.Type.(*ast.Ident)
		if ok && identifier.Name == "Runner" {
			found = true
			return false
		}
		return !found
	})
	return found
}
