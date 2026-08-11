package golemtest

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const packageImportPath = "github.com/eleven-am/golem/go/golemtest"

var permittedImports = map[string]bool{
	"encoding/hex": true,
	"errors":       true,
	"fmt":          true,
	"slices":       true,
	"sort":         true,
	"strconv":      true,

	"github.com/eleven-am/golem/go/golem": true,

	"github.com/eleven-am/golem/go/internal/policy/bind":       true,
	"github.com/eleven-am/golem/go/internal/policy/classify":   true,
	"github.com/eleven-am/golem/go/internal/policy/dependency": true,
	"github.com/eleven-am/golem/go/internal/policy/imply":      true,
	"github.com/eleven-am/golem/go/internal/policy/ir":         true,
	"github.com/eleven-am/golem/go/internal/policy/normalize":  true,
	"github.com/eleven-am/golem/go/internal/policy/resolve":    true,
	"github.com/eleven-am/golem/go/internal/policy/schema":     true,
}

var forbiddenMethods = map[string]map[string]bool{
	"golem.FrozenRuleView":      nil,
	"golem.FrozenConditionView": nil,
	"golem.FrozenOperandView":   nil,
	"golem.FrozenValueView":     nil,
	"golem.FrozenJSONPathView":  nil,
	"golem.FrozenRelationRef":   nil,
	"ir.Rule":                   nil,
	"ir.Operand":                nil,
	"ir.Value":                  nil,
	"ir.JSONPath":               nil,
	"ir.JSONPathSegment":        nil,
	"ir.Requirement":            nil,

	"golem.FrozenPolicyView":    {"Rules": true},
	"golem.FrozenPredicateView": {"Root": true},
	"ir.Policy":                 {"Rules": true},
	"ir.Condition": {
		"Kind": true, "Logical": true, "Field": true, "FieldType": true,
		"Operator": true, "Mode": true, "Operand": true, "Path": true,
		"Relation": true, "RelationOwnRequirements": true, "Requirements": true,
	},
}

var forbiddenConstantTypes = map[string]bool{
	"golem.FrozenEffect":         true,
	"golem.FrozenOperator":       true,
	"golem.FrozenConditionKind":  true,
	"golem.FrozenComparisonMode": true,
	"golem.FrozenValueKind":      true,
	"golem.FrozenOperandKind":    true,
	"ir.Effect":                  true,
	"ir.ConditionKind":           true,
	"ir.OperatorID":              true,
	"ir.LogicalOperator":         true,
	"ir.ComparisonMode":          true,
	"ir.ValueKind":               true,
	"ir.OperandKind":             true,
}

var delegationRequirements = []struct {
	symbol   string
	delegate string
}{
	{"RowConstraint", "github.com/eleven-am/golem/go/internal/policy/resolve"},
	{"RowConstraint", "github.com/eleven-am/golem/go/internal/policy/bind"},
	{"Equivalent", "github.com/eleven-am/golem/go/internal/policy/imply"},
	{"Implies", "github.com/eleven-am/golem/go/internal/policy/imply"},
	{"ClassifyReadFields", "github.com/eleven-am/golem/go/internal/policy/classify"},
	{"ClassifyReadFieldsWithReach", "github.com/eleven-am/golem/go/internal/policy/classify"},
	{"Dependencies", "github.com/eleven-am/golem/go/internal/policy/dependency"},
	{"CanonicalBytes", "github.com/eleven-am/golem/go/internal/policy/bind"},
	{"View", "github.com/eleven-am/golem/go/internal/policy/bind"},
}

func packageDirectory(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve package directory")
	}
	return filepath.Dir(file)
}

func loadPackage(t *testing.T) *packages.Package {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{
		Dir: packageDirectory(t),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
	}, ".")
	if err != nil {
		t.Fatalf("load package: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(loaded))
	}
	if len(loaded[0].Errors) != 0 {
		t.Fatalf("package has %d load errors, first: %v", len(loaded[0].Errors), loaded[0].Errors[0])
	}
	if loaded[0].PkgPath != packageImportPath {
		t.Fatalf("loaded %q, want %q", loaded[0].PkgPath, packageImportPath)
	}
	return loaded[0]
}

func productionFiles(t *testing.T, pkg *packages.Package) []*ast.File {
	t.Helper()
	files := make([]*ast.File, 0, len(pkg.Syntax))
	for _, syntax := range pkg.Syntax {
		name := filepath.Base(pkg.Fset.Position(syntax.Pos()).Filename)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, syntax)
	}
	if len(files) == 0 {
		t.Fatal("package declares no non-test source files")
	}
	return files
}

func qualifiedTypeName(named *types.Named) string {
	object := named.Obj()
	if object == nil || object.Pkg() == nil {
		return ""
	}
	return object.Pkg().Name() + "." + object.Name()
}

func receiverTypeName(signature *types.Signature) string {
	receiver := signature.Recv()
	if receiver == nil {
		return ""
	}
	kind := receiver.Type()
	for {
		switch value := kind.(type) {
		case *types.Pointer:
			kind = value.Elem()
		case *types.Named:
			return qualifiedTypeName(value)
		default:
			return ""
		}
	}
}

func TestPolicyTestKitSourceInventoryContainsNoForkedPolicyKernel(t *testing.T) {
	directory := packageDirectory(t)
	pkg := loadPackage(t)
	files := productionFiles(t, pkg)

	t.Run("NoNestedPackages", func(t *testing.T) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			nested, err := os.ReadDir(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range nested {
				if strings.HasSuffix(candidate.Name(), ".go") {
					t.Fatalf("nested package %q may host a private policy fork", entry.Name())
				}
			}
		}
	})

	t.Run("ImportsAreRestrictedToTheProductionKernel", func(t *testing.T) {
		for _, file := range files {
			name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
			for _, spec := range file.Imports {
				path := strings.Trim(spec.Path.Value, `"`)
				if !permittedImports[path] {
					t.Errorf("%s imports %q which is outside the adapter allowlist", name, path)
				}
			}
		}
	})

	t.Run("NoPackageLevelMutableState", func(t *testing.T) {
		for _, file := range files {
			name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
			for _, declaration := range file.Decls {
				generic, ok := declaration.(*ast.GenDecl)
				if !ok {
					continue
				}
				if generic.Tok.String() == "var" {
					t.Errorf("%s declares package-level var state, which permits a cross-call policy cache", name)
				}
			}
		}
	})

	t.Run("NoConcurrencyOrIO", func(t *testing.T) {
		for _, file := range files {
			name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
			ast.Inspect(file, func(node ast.Node) bool {
				switch node.(type) {
				case *ast.GoStmt:
					t.Errorf("%s starts a goroutine", name)
				case *ast.SelectStmt:
					t.Errorf("%s blocks on a channel", name)
				case *ast.ChanType:
					t.Errorf("%s declares a channel", name)
				}
				return true
			})
		}
	})

	t.Run("NoRuleOrPredicateInterpretationVocabulary", func(t *testing.T) {
		for _, file := range files {
			name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				object := pkg.TypesInfo.Uses[ident]
				if object == nil {
					return true
				}
				switch value := object.(type) {
				case *types.Func:
					signature, ok := value.Type().(*types.Signature)
					if !ok {
						return true
					}
					owner := receiverTypeName(signature)
					methods, forbidden := forbiddenMethods[owner]
					if !forbidden {
						return true
					}
					if methods == nil || methods[value.Name()] {
						t.Errorf("%s calls %s.%s; interpreting frozen rules or predicates here forks the policy kernel", name, owner, value.Name())
					}
				case *types.Const:
					named, ok := value.Type().(*types.Named)
					if !ok {
						return true
					}
					if forbiddenConstantTypes[qualifiedTypeName(named)] {
						t.Errorf("%s uses the %s constant %s; deciding effects or operators here forks the policy kernel", name, qualifiedTypeName(named), value.Name())
					}
				}
				return true
			})
		}
	})

	t.Run("AnalysisSurfacesMustDelegateToTheProductionKernel", func(t *testing.T) {
		declared := map[string]bool{}
		for _, file := range files {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if ok {
					declared[function.Name.Name] = true
				}
			}
		}
		imported := map[string]bool{}
		for _, file := range files {
			for _, spec := range file.Imports {
				imported[strings.Trim(spec.Path.Value, `"`)] = true
			}
		}
		for _, requirement := range delegationRequirements {
			if declared[requirement.symbol] && !imported[requirement.delegate] {
				t.Errorf("the package declares %s without importing %s; that surface must adapt the production kernel, not reimplement it", requirement.symbol, requirement.delegate)
			}
		}
	})
}

func TestPolicyTestKitPublicAPIContainsNoInternalTypesOrAuthority(t *testing.T) {
	pkg := loadPackage(t)
	scope := pkg.Types.Scope()

	t.Run("NoInternalTypeInAnyExportedSignature", func(t *testing.T) {
		for _, name := range scope.Names() {
			object := scope.Lookup(name)
			if !object.Exported() {
				continue
			}
			assertNoInternalType(t, name, object.Type())
			named, ok := object.Type().(*types.Named)
			if !ok {
				continue
			}
			for index := 0; index < named.NumMethods(); index++ {
				method := named.Method(index)
				if !method.Exported() {
					continue
				}
				assertNoInternalType(t, name+"."+method.Name(), method.Type())
			}
			structure, ok := named.Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for index := 0; index < structure.NumFields(); index++ {
				field := structure.Field(index)
				if !field.Exported() {
					continue
				}
				assertNoInternalType(t, name+"."+field.Name(), field.Type())
			}
		}
	})

	t.Run("ExportedSurfaceIsTheDeclaredInventory", func(t *testing.T) {
		expected := []string{
			"CodeOf",
			"Constraint",
			"Equivalent",
			"ErrorCode",
			"ErrorGenerationMismatch",
			"ErrorInvalidInput",
			"ErrorPolicyAnalysis",
			"ErrorPolicyFactory",
			"Implies",
			"Kit",
			"Model",
			"ModelPolicy",
			"New",
			"PolicySet",
		}
		actual := make([]string, 0, len(expected))
		for _, name := range scope.Names() {
			if scope.Lookup(name).Exported() {
				actual = append(actual, name)
			}
		}
		sort.Strings(actual)
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("exported surface is [%s], declared inventory is [%s]", strings.Join(actual, ","), strings.Join(expected, ","))
		}
	})

	t.Run("NoExportedMutableStateOrIdentityConstructor", func(t *testing.T) {
		for _, name := range scope.Names() {
			object := scope.Lookup(name)
			if !object.Exported() {
				continue
			}
			if _, mutable := object.(*types.Var); mutable {
				t.Errorf("%s is exported package state", name)
			}
			function, ok := object.(*types.Func)
			if !ok {
				continue
			}
			signature, ok := function.Type().(*types.Signature)
			if !ok {
				continue
			}
			results := signature.Results()
			for index := 0; index < results.Len(); index++ {
				assertNoManufacturedIdentity(t, name, results.At(index).Type())
			}
		}
	})
}

func assertNoManufacturedIdentity(t *testing.T, symbol string, kind types.Type) {
	t.Helper()
	named, ok := kind.(*types.Named)
	if !ok {
		return
	}
	switch qualifiedTypeName(named) {
	case "golem.ModelID", "golem.FieldID", "golem.RelationID", "golem.KeyID", "golem.SchemaDigest":
		t.Errorf("%s returns a bare %s, which manufactures a schema identity", symbol, qualifiedTypeName(named))
	case "golem.FrozenPolicy", "golem.Rules":
		t.Errorf("%s returns %s, which grants policy authorship authority", symbol, qualifiedTypeName(named))
	}
}

func assertNoInternalType(t *testing.T, symbol string, kind types.Type) {
	t.Helper()
	seen := map[types.Type]bool{}
	var walk func(types.Type)
	walk = func(current types.Type) {
		if current == nil || seen[current] {
			return
		}
		seen[current] = true
		switch value := current.(type) {
		case *types.Named:
			object := value.Obj()
			if object != nil && object.Pkg() != nil && strings.Contains(object.Pkg().Path(), "/internal/") {
				t.Errorf("exported signature %s contains %s", symbol, object.Pkg().Path()+"."+object.Name())
			}
			for index := 0; index < value.TypeArgs().Len(); index++ {
				walk(value.TypeArgs().At(index))
			}
			if _, isStruct := value.Underlying().(*types.Struct); !isStruct {
				walk(value.Underlying())
			}
			for index := 0; index < value.NumMethods(); index++ {
				if value.Method(index).Exported() {
					walk(value.Method(index).Type())
				}
			}
		case *types.Signature:
			walk(value.Params())
			walk(value.Results())
		case *types.Tuple:
			for index := 0; index < value.Len(); index++ {
				walk(value.At(index).Type())
			}
		case *types.Pointer:
			walk(value.Elem())
		case *types.Slice:
			walk(value.Elem())
		case *types.Array:
			walk(value.Elem())
		case *types.Map:
			walk(value.Key())
			walk(value.Elem())
		case *types.Chan:
			walk(value.Elem())
		case *types.Interface:
			for index := 0; index < value.NumMethods(); index++ {
				walk(value.Method(index).Type())
			}
		case *types.Struct:
			for index := 0; index < value.NumFields(); index++ {
				if value.Field(index).Exported() {
					walk(value.Field(index).Type())
				}
			}
		}
	}
	walk(kind)
}
