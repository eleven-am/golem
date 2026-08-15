package schema_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func TestHistoricalRegistryLoadsExactReleasedSocialV1V4PhysicalV1BundleOnly(t *testing.T) {
	bundle := frozenP7GeneratedSchemaBundle(t)
	if bundle.Model().FormatVersion() != 1 || bundle.Contract().FormatVersion() != 4 {
		t.Fatalf("released social fixture versions = model %d contract %d", bundle.Model().FormatVersion(), bundle.Contract().FormatVersion())
	}
	for _, provider := range bundle.Providers() {
		if provider.Schema().FormatVersion() != 1 || provider.Schema().CanonicalVersion() != 1 {
			t.Fatalf("released social provider %s versions = %d/%d", provider.Provider(), provider.Schema().FormatVersion(), provider.Schema().CanonicalVersion())
		}
	}
	registry, err := schema.NewHistorical(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if registry.GenerationDigest() != bundle.GenerationDigest() || len(registry.Providers()) != 2 {
		t.Fatalf("historical registry = generation %s providers %v", registry.GenerationDigest(), registry.Providers())
	}
	if _, err := schema.New(bundle); err == nil {
		t.Fatal("active registry accepted released historical social bundle")
	}

	providers := bundle.Providers()
	first := providers[0]
	firstSchema := first.Schema()
	wrongFingerprint := firstSchema.Fingerprint()
	wrongFingerprint[0] ^= 0xff
	providers[0] = golem.GeneratedProviderSchemaDocument(first.Provider(), first.SystemFingerprint(), golem.GeneratedSchemaDocument(1, 1, wrongFingerprint, firstSchema.Bytes()))
	wrongBundle := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
	if _, err := schema.NewHistorical(wrongBundle); err == nil || !strings.Contains(err.Error(), "historical physical fingerprint mismatch") {
		t.Fatalf("historical registry accepted wrong original physical-v1 fingerprint: %v", err)
	}

	providers = bundle.Providers()
	first = providers[0]
	wrongSystemFingerprint := first.SystemFingerprint()
	wrongSystemFingerprint[0] ^= 0xff
	providers[0] = golem.GeneratedProviderSchemaDocument(first.Provider(), wrongSystemFingerprint, first.Schema())
	wrongBundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
	if _, err := schema.NewHistorical(wrongBundle); err == nil || !strings.Contains(err.Error(), "historical system fingerprint mismatch") {
		t.Fatalf("historical registry accepted wrong original physical-v1 system fingerprint: %v", err)
	}

	for _, version := range []uint32{2, 3, 4} {
		providers = bundle.Providers()
		first = providers[0]
		firstSchema = first.Schema()
		providers[0] = golem.GeneratedProviderSchemaDocument(first.Provider(), first.SystemFingerprint(), golem.GeneratedSchemaDocument(version, version, firstSchema.Fingerprint(), firstSchema.Bytes()))
		forged := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
		if _, err := schema.NewHistorical(forged); err == nil {
			t.Fatalf("historical registry accepted physical-v1 bytes relabelled as %d", version)
		}
	}
}

func frozenP7GeneratedSchemaBundle(t *testing.T) golem.SchemaBundle {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate historical acceptance test")
	}
	path := filepath.Join(filepath.Dir(source), "testdata", "p7", "zz_golem_registry.gen.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse exact frozen P7 registry: %v", err)
	}
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions[function.Name.Name] = function
		}
	}
	bundleCall := frozenP7ReturnCall(t, functions["GolemGeneratedSchemaBundle"])
	if len(bundleCall.Args) != 7 {
		t.Fatalf("exact frozen P7 bundle argument inventory = %d", len(bundleCall.Args))
	}
	generation := frozenP7Digest(t, frozenP7ReturnExpression(t, functions["golemGeneratedGenerationDigest"]))
	providers := make([]golem.ProviderSchemaDocument, 0, 2)
	for _, expression := range bundleCall.Args[5:] {
		call := frozenP7Call(t, expression, "GeneratedProviderSchemaDocument", 3)
		provider := frozenP7Provider(t, call.Args[0])
		providers = append(providers, golem.GeneratedProviderSchemaDocument(provider, frozenP7Digest(t, call.Args[1]), frozenP7Document(t, call.Args[2])))
	}
	return golem.GeneratedSchemaBundle(generation, frozenP7String(t, bundleCall.Args[1]), frozenP7String(t, bundleCall.Args[2]), frozenP7Document(t, bundleCall.Args[3]), frozenP7Document(t, bundleCall.Args[4]), providers...)
}

func frozenP7Provider(t *testing.T, expression ast.Expr) golem.Provider {
	t.Helper()
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("exact frozen P7 provider shape changed: %T", expression)
	}
	switch selector.Sel.Name {
	case "SQLite":
		return golem.SQLite
	case "PostgreSQL":
		return golem.PostgreSQL
	default:
		t.Fatalf("unknown exact frozen P7 provider %s", selector.Sel.Name)
		return ""
	}
}

func frozenP7Document(t *testing.T, expression ast.Expr) golem.SchemaDocument {
	t.Helper()
	call := frozenP7Call(t, expression, "GeneratedSchemaDocument", 4)
	format := uint32(frozenP7Integer(t, call.Args[0]))
	canonical := uint32(frozenP7Integer(t, call.Args[1]))
	bytesCall := frozenP7Call(t, call.Args[3], "", 1)
	return golem.GeneratedSchemaDocument(format, canonical, frozenP7Digest(t, call.Args[2]), []byte(frozenP7String(t, bytesCall.Args[0])))
}

func frozenP7ReturnExpression(t *testing.T, function *ast.FuncDecl) ast.Expr {
	t.Helper()
	if function == nil || function.Body == nil || len(function.Body.List) != 1 {
		t.Fatal("exact frozen P7 function shape changed")
	}
	statement, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(statement.Results) != 1 {
		t.Fatal("exact frozen P7 return shape changed")
	}
	return statement.Results[0]
}

func frozenP7ReturnCall(t *testing.T, function *ast.FuncDecl) *ast.CallExpr {
	t.Helper()
	return frozenP7Call(t, frozenP7ReturnExpression(t, function), "GeneratedSchemaBundle", 7)
}

func frozenP7Call(t *testing.T, expression ast.Expr, name string, arguments int) *ast.CallExpr {
	t.Helper()
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != arguments {
		t.Fatalf("exact frozen P7 call shape changed: %T/%d", expression, arguments)
	}
	if name != "" {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != name {
			t.Fatalf("exact frozen P7 call is not %s", name)
		}
	}
	return call
}

func frozenP7Digest(t *testing.T, expression ast.Expr) golem.SchemaDigest {
	t.Helper()
	composite, ok := expression.(*ast.CompositeLit)
	if !ok || len(composite.Elts) != len(golem.SchemaDigest{}) {
		t.Fatalf("exact frozen P7 digest shape changed: %T", expression)
	}
	var result golem.SchemaDigest
	for index, element := range composite.Elts {
		result[index] = byte(frozenP7Integer(t, element))
	}
	return result
}

func frozenP7String(t *testing.T, expression ast.Expr) string {
	t.Helper()
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		t.Fatalf("exact frozen P7 string shape changed: %T", expression)
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		t.Fatalf("decode exact frozen P7 string: %v", err)
	}
	return value
}

func frozenP7Integer(t *testing.T, expression ast.Expr) uint64 {
	t.Helper()
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		t.Fatalf("exact frozen P7 integer shape changed: %T", expression)
	}
	value, err := strconv.ParseUint(literal.Value, 0, 64)
	if err != nil {
		t.Fatalf("decode exact frozen P7 integer: %v", err)
	}
	return value
}
