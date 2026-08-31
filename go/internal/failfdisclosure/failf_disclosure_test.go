package failfdisclosure

import (
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

type exception struct {
	file     string
	function string
	argument string
	reason   string
}

var declaredExceptions = []exception{
	{"events/cdc.go", "ValidateCDCIdentity", "detail", "cdcIdentityDefect builds the detail from package-local field-name literals, bounds, and the canonical identity pattern"},
	{"events/cdc.go", "ValidateCDCAdapters", "detail", "cdcIdentityDefect builds the detail from package-local field-name literals, bounds, and the canonical identity pattern"},
	{"events/limits.go", "NormalizeLimits", "item.name", "a package-local table of string-literal field names"},
	{"events/nats/config.go", "normalizeConfig", "item.name", "a package-local table of string-literal field names"},
}

var failfOwners = map[string]bool{
	"github.com/eleven-am/golem/go/embedding": true,
	"github.com/eleven-am/golem/go/events":    true,
}

func TestFailfDetailNeverCarriesCallerSuppliedStrings(t *testing.T) {
	root := moduleRoot(t)
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:  root,
	}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(loaded) > 0 {
		t.Fatal("the scanned packages did not type-check")
	}

	used := make(map[exception]bool, len(declaredExceptions))
	allowed := make(map[exception]bool, len(declaredExceptions))
	for _, entry := range declaredExceptions {
		allowed[exception{file: entry.file, function: entry.function, argument: entry.argument}] = true
	}

	calls := 0
	owners := make(map[string]bool)
	for _, loadedPackage := range loaded {
		for _, file := range loadedPackage.Syntax {
			name := relativePath(root, loadedPackage.Fset.Position(file.Pos()).Filename)
			for _, declaration := range file.Decls {
				enclosing := "<package>"
				if function, ok := declaration.(*ast.FuncDecl); ok {
					enclosing = functionName(function)
				}
				direct := make(map[*ast.Ident]bool)
				ast.Inspect(declaration, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					identifier, owner, signature := failfTarget(loadedPackage.TypesInfo, call)
					if signature == nil {
						return true
					}
					direct[identifier] = true
					calls++
					owners[owner] = true
					inspectDetail(t, loadedPackage.TypesInfo, call, signature, name, enclosing, allowed, used)
					return true
				})
				ast.Inspect(declaration, func(node ast.Node) bool {
					identifier, ok := node.(*ast.Ident)
					if !ok || direct[identifier] {
						return true
					}
					if owner, _ := failfReference(loadedPackage.TypesInfo, identifier); owner != "" {
						t.Errorf("%s: %s takes %s.Failf as a function value; the disclosure guard only permits direct calls", name, enclosing, owner)
					}
					return true
				})
			}
		}
	}
	if calls == 0 {
		t.Fatal("no Failf call sites were scanned")
	}
	for owner := range failfOwners {
		if !owners[owner] {
			t.Errorf("no Failf call site was scanned in %s", owner)
		}
	}
	for _, entry := range declaredExceptions {
		key := exception{file: entry.file, function: entry.function, argument: entry.argument}
		if !used[key] {
			t.Errorf("stale exception: %s %s no longer passes %s to Failf; it was allowed because %s", entry.file, entry.function, entry.argument, entry.reason)
		}
	}
}

func inspectDetail(t *testing.T, info *types.Info, call *ast.CallExpr, signature *types.Signature, file, enclosing string, allowed, used map[exception]bool) {
	first := signature.Params().Len() - 1
	if call.Ellipsis.IsValid() {
		t.Errorf("%s: %s spreads a slice into the Failf detail; the guard cannot see what it carries", file, enclosing)
		return
	}
	if first < 1 || len(call.Args) < first {
		return
	}
	format := call.Args[first-1]
	if value := info.Types[format]; value.Value == nil {
		t.Errorf("%s: %s builds the Failf format from a non-constant expression %q", file, enclosing, types.ExprString(format))
	}
	for _, argument := range call.Args[first:] {
		value, known := info.Types[argument]
		text := types.ExprString(argument)
		if known && safeDetailArgument(value) {
			continue
		}
		key := exception{file: file, function: enclosing, argument: text}
		if allowed[key] {
			used[key] = true
			continue
		}
		t.Errorf("%s: %s passes non-constant %s %q into the printed Failf detail; the detail may only name fields, bounds, and lengths", file, enclosing, describe(value.Type), text)
	}
}

func safeDetailArgument(value types.TypeAndValue) bool {
	if basic, ok := value.Type.(*types.Basic); ok {
		return basic.Info()&types.IsString == 0 || value.Value != nil
	}
	named, ok := value.Type.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Duration"
}

func TestSafeDetailArgumentUsesAClosedTypeSet(t *testing.T) {
	if !safeDetailArgument(types.TypeAndValue{Type: types.Typ[types.Int]}) {
		t.Fatal("a plain integer was rejected")
	}
	if !safeDetailArgument(types.TypeAndValue{Type: types.Typ[types.UntypedString], Value: constant.MakeString("field")}) {
		t.Fatal("a plain string constant was rejected")
	}
	if safeDetailArgument(types.TypeAndValue{Type: types.Typ[types.String]}) {
		t.Fatal("a dynamic string was accepted")
	}
	pkg := types.NewPackage("example.com/private", "private")
	named := types.NewNamed(types.NewTypeName(0, pkg, "SecretNumber", nil), types.Typ[types.Int], nil)
	if safeDetailArgument(types.TypeAndValue{Type: named, Value: constant.MakeInt64(1)}) {
		t.Fatal("a typed constant with a customisable formatter was accepted")
	}
	timePackage := types.NewPackage("time", "time")
	duration := types.NewNamed(types.NewTypeName(0, timePackage, "Duration", nil), types.Typ[types.Int64], nil)
	if !safeDetailArgument(types.TypeAndValue{Type: duration}) {
		t.Fatal("time.Duration was rejected")
	}
}

func describe(typ types.Type) string {
	if typ == nil {
		return "expression of unknown type"
	}
	return typ.String()
}

func failfTarget(info *types.Info, call *ast.CallExpr) (*ast.Ident, string, *types.Signature) {
	var identifier *ast.Ident
	switch target := call.Fun.(type) {
	case *ast.Ident:
		identifier = target
	case *ast.SelectorExpr:
		identifier = target.Sel
	default:
		return nil, "", nil
	}
	owner, signature := failfReference(info, identifier)
	if signature == nil {
		return nil, "", nil
	}
	return identifier, owner, signature
}

func failfReference(info *types.Info, identifier *ast.Ident) (string, *types.Signature) {
	function, ok := info.Uses[identifier].(*types.Func)
	if !ok || function.Name() != "Failf" || function.Pkg() == nil || !failfOwners[function.Pkg().Path()] {
		return "", nil
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || !signature.Variadic() {
		return "", nil
	}
	return function.Pkg().Path(), signature
}

func functionName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	return "(" + types.ExprString(function.Recv.List[0].Type) + ")." + function.Name.Name
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}
