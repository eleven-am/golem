package graphql

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type generatedTestCaller struct {
	rows  []golem.RuntimeModelRow
	err   error
	calls int
}

func (caller *generatedTestCaller) ExecuteFrozenRead(_ context.Context, _ golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	caller.calls++
	return caller.rows, caller.err
}

func TestGeneratedExecutorCreatesOneCallerAndExecutesAllQueryRoots(t *testing.T) {
	compilation, bundle := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	post := generatedTestContract(t, compilation.Contract, "Post")
	model := generatedTestModel(t, compilation.Model, post.ModelID)
	title := generatedTestField(t, model, "Title")
	row, err := golem.RuntimeModelReadRow(generatedTestModelID(t, model.ID), golem.RuntimePresentReadCell(generatedTestFieldID(t, title.ID), "visible", nil))
	if err != nil {
		t.Fatal(err)
	}
	caller := &generatedTestCaller{rows: []golem.RuntimeModelRow{row}}
	begins, reports := 0, 0
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			begins++
			return caller, nil
		},
		ReportInternalError: func(context.Context, error) { reports++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 7, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError:  func(context.Context, error) { reports++ },
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 7, Request{Query: `query { first: ` + post.Roots.FindMany + `(take: 1) { title } second: ` + post.Roots.FindMany + `(take: 2) { title } }`})
	if len(response.Errors) != 0 || begins != 1 || caller.calls != 2 || reports != 0 {
		t.Fatalf("response=%#v begins=%d calls=%d reports=%d", response, begins, caller.calls, reports)
	}
	data := response.Data.(map[string]any)
	for _, name := range []string{"first", "second"} {
		items := data[name].([]any)
		if len(items) != 1 || items[0].(map[string]any)["title"] != "visible" {
			t.Fatalf("%s=%#v", name, data[name])
		}
	}
	if server.ContractFingerprint() != bundle.Contract().Fingerprint() {
		t.Fatal("server contract fingerprint drifted")
	}
}

func TestGeneratedExecutorMapsInvisibleUniqueToNullableRoot(t *testing.T) {
	compilation, bundle := generatedTestCompilation(t)
	document, _ := graphqlschema.Build(compilation)
	post := generatedTestContract(t, compilation.Contract, "Post")
	model := generatedTestModel(t, compilation.Model, post.ModelID)
	var selector compilerir.SelectorContractIR
	for _, candidate := range post.Selectors {
		if len(candidate.Fields) == 1 {
			selector = candidate
			break
		}
	}
	if selector.KeyID == "" {
		t.Fatal("Post has no scalar unique selector")
	}
	field := generatedTestFieldByID(t, model, selector.Fields[0])
	whereValue := `"missing"`
	if field.Scalar.Type.Kind == compilerir.TypeUUID {
		whereValue = `"00000000-0000-0000-0000-000000000001"`
	}
	caller := &generatedTestCaller{err: golem.RuntimeReadError(golem.CodeNotFound, "findUnique", generatedTestModelID(t, model.ID), golem.FieldID{}, "record not found", nil)}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{Bundle: bundle, BeginCaller: func(context.Context, int) (CallerExecution, error) { return caller, nil }, ReportInternalError: func(context.Context, error) {}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	query := `query { ` + post.Roots.FindOne + `(where: { ` + selector.Name + `: ` + whereValue + ` }) { title } }`
	response := server.Execute(context.Background(), 1, Request{Query: query})
	if len(response.Errors) != 0 || response.Data.(map[string]any)[post.Roots.FindOne] != nil || caller.calls != 1 {
		t.Fatalf("response=%#v calls=%d", response, caller.calls)
	}
}

func generatedTestCompilation(t *testing.T) (compilerir.CompilationIR, golem.SchemaBundle) {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../internal/compiler/compile/testdata/social", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	compilation := *result.Compilation
	modelBytes, _ := compilerir.CanonicalModel(compilation.Model)
	contractBytes, _ := compilerir.CanonicalContract(compilation.Contract)
	modelFingerprint, _ := compilerir.ModelFingerprint(compilation.Model)
	contractFingerprint, _ := compilerir.ContractFingerprint(compilation.Contract)
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), generatedTestDigest(t, modelFingerprint), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), generatedTestDigest(t, contractFingerprint), contractBytes)
	return compilation, golem.GeneratedSchemaBundle(golem.SchemaDigest{1}, "test", "test", modelDocument, contractDocument)
}

func generatedTestDigest(t *testing.T, value compilerir.Fingerprint) (result golem.SchemaDigest) {
	t.Helper()
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(result) {
		t.Fatalf("fingerprint=%q err=%v", value, err)
	}
	copy(result[:], decoded)
	return result
}

func generatedTestContract(t *testing.T, contract compilerir.ContractIR, name string) compilerir.ModelContractIR {
	t.Helper()
	for _, model := range contract.Models {
		if model.GraphQLName == name {
			return model
		}
	}
	t.Fatalf("missing contract %s", name)
	return compilerir.ModelContractIR{}
}

func generatedTestModel(t *testing.T, model compilerir.ModelIR, id compilerir.ModelID) compilerir.ModelDeclIR {
	t.Helper()
	for _, value := range model.Models {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("missing model %s", id)
	return compilerir.ModelDeclIR{}
}

func generatedTestField(t *testing.T, model compilerir.ModelDeclIR, goName string) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.GoName == goName {
			return field
		}
	}
	t.Fatalf("missing field %s.%s", model.Go.Name, goName)
	return compilerir.FieldIR{}
}

func generatedTestFieldByID(t *testing.T, model compilerir.ModelDeclIR, id compilerir.FieldID) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.ID == id {
			return field
		}
	}
	t.Fatalf("missing field %s.%s", model.Go.Name, id)
	return compilerir.FieldIR{}
}

func generatedTestModelID(t *testing.T, value compilerir.ModelID) (result golem.ModelID) {
	t.Helper()
	generatedTestFixedID(t, string(value), result[:])
	return result
}
func generatedTestFieldID(t *testing.T, value compilerir.FieldID) (result golem.FieldID) {
	t.Helper()
	generatedTestFixedID(t, string(value), result[:])
	return result
}
func generatedTestFixedID(t *testing.T, value string, destination []byte) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(destination) {
		t.Fatalf("identity=%q err=%v", value, err)
	}
	copy(destination, decoded)
}
