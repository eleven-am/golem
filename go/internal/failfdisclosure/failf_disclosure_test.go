package failfdisclosure

import (
	"go/ast"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	{"events/cdc.go", "ValidateCDCAdapters", "identity.Provider", "the adapter's own declared provider identity, echoed back to the application that configured it"},
	{"events/cdc.go", "ValidateCDCAdapters", "provider", "the runtime provider identity the application configured, not provider or record content"},
	{"events/cdc.go", "ValidateCDCAdapters", "canonical", "CanonicalName is empty unless every field matched canonicalCDCIdentity within MaximumCDCIdentityBytes"},
	{"events/limits.go", "NormalizeLimits", "item.name", "a package-local table of string-literal field names"},
	{"events/nats/config.go", "normalizeConfig", "item.name", "a package-local table of string-literal field names"},
	{"internal/semantic/runtime/manager.go", "(*Manager).Query", "model", "the caller's own model identity, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "(*Manager).Query", "name", "the caller's own index name, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "(*Manager).QueryByKey", "model", "the caller's own model identity, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "(*Manager).QueryByKey", "name", "the caller's own index name, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "validateCandidates", "index.Descriptor.Name", "a compiled schema index name, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "validateCandidates", "candidates.Columns[position]", "a caller-projected column name, not provider output or indexed document text"},
	{"internal/semantic/runtime/manager.go", "validateCandidates", "column.Name", "a compiled schema column name, not provider output or indexed document text"},
}

var failfOwners = map[string]bool{
	"github.com/eleven-am/golem/go/embedding": true,
	"github.com/eleven-am/golem/go/events":    true,
}

func TestFailfDetailNeverCarriesCallerSuppliedStrings(t *testing.T) {
	root := moduleRoot(t)
	patterns := callingPatterns(t, root)
	if len(patterns) == 0 {
		t.Fatal("no package in the module calls Failf")
	}
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:  root,
	}, patterns...)
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
				function, ok := declaration.(*ast.FuncDecl)
				if !ok {
					continue
				}
				enclosing := functionName(function)
				ast.Inspect(function, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					owner, signature := failfTarget(loadedPackage.TypesInfo, call)
					if signature == nil {
						return true
					}
					calls++
					owners[owner] = true
					inspectDetail(t, loadedPackage.TypesInfo, call, signature, name, enclosing, allowed, used)
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
		if known && value.Value != nil {
			continue
		}
		if known && !carriesText(value.Type) {
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

func carriesText(typ types.Type) bool {
	return carriesTextWithin(typ, map[types.Type]bool{})
}

func carriesTextWithin(typ types.Type, seen map[types.Type]bool) bool {
	if typ == nil {
		return true
	}
	if seen[typ] {
		return false
	}
	seen[typ] = true
	switch underlying := typ.Underlying().(type) {
	case *types.Basic:
		return underlying.Info()&types.IsString != 0
	case *types.Interface:
		return true
	case *types.Slice:
		return elementCarriesText(underlying.Elem(), seen)
	case *types.Array:
		return elementCarriesText(underlying.Elem(), seen)
	case *types.Map:
		return carriesTextWithin(underlying.Key(), seen) || carriesTextWithin(underlying.Elem(), seen)
	case *types.Struct:
		for field := 0; field < underlying.NumFields(); field++ {
			if carriesTextWithin(underlying.Field(field).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Pointer:
		return carriesTextWithin(underlying.Elem(), seen)
	default:
		return false
	}
}

func elementCarriesText(element types.Type, seen map[types.Type]bool) bool {
	if basic, ok := element.Underlying().(*types.Basic); ok {
		if basic.Kind() == types.Byte || basic.Kind() == types.Rune {
			return true
		}
	}
	return carriesTextWithin(element, seen)
}

func describe(typ types.Type) string {
	if typ == nil {
		return "expression of unknown type"
	}
	return typ.String()
}

func failfTarget(info *types.Info, call *ast.CallExpr) (string, *types.Signature) {
	var identifier *ast.Ident
	switch target := call.Fun.(type) {
	case *ast.Ident:
		identifier = target
	case *ast.SelectorExpr:
		identifier = target.Sel
	default:
		return "", nil
	}
	if identifier.Name != "Failf" {
		return "", nil
	}
	function, ok := info.Uses[identifier].(*types.Func)
	if !ok || function.Pkg() == nil || !failfOwners[function.Pkg().Path()] {
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

func callingPatterns(t *testing.T, root string) []string {
	t.Helper()
	found := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), "Failf(") {
			return nil
		}
		found["./"+filepath.ToSlash(mustRelative(root, filepath.Dir(path)))] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	patterns := make([]string, 0, len(found))
	for pattern := range found {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return patterns
}

func mustRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return relative
}
