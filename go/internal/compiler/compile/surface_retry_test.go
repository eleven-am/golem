package compile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/methods"
)

func recordInterpretations(t *testing.T) *[]methods.Config {
	t.Helper()
	var calls []methods.Config
	original := interpretMethods
	interpretMethods = func(ctx context.Context, config methods.Config) methods.Result {
		calls = append(calls, config)
		return original(ctx, config)
	}
	t.Cleanup(func() { interpretMethods = original })
	return &calls
}

func TestSourceThatTypeChecksAgainstTheBootstrapIsLoadedOnce(t *testing.T) {
	calls := recordInterpretations(t)
	result := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	if len(*calls) != 1 {
		t.Fatalf("source that type-checks on the first pass was loaded %d times", len(*calls))
	}
	if first := (*calls)[0]; !first.TolerateTypeErrors || len(first.BuildFlags) != 0 {
		t.Fatalf("first pass ran with tolerance=%t build flags=%v", first.TolerateTypeErrors, first.BuildFlags)
	}
}

func writeSurfaceRetryFixture(t *testing.T, declarations string) string {
	t.Helper()
	golemModule, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/surfaceretry\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n",
		"schema.go": `package surfaceretry

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{ ID int64 }

type Note struct {
	_  struct{} ` + "`golem:\"model;table=notes\"`" + `
	ID int64 ` + "`db:\"id\" golem:\"pk\"`" + `
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "surface_retry")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}

func Load(ctx context.Context, system System[string], id int64) error {
	_, err := system.Notes.FindUnique(ctx, Notes.ByID.Value(id))
	return err
}
` + declarations,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func TestSourceUsingASymbolOnlyTheCompleteSurfaceDeclaresIsLoadedTwice(t *testing.T) {
	directory := writeSurfaceRetryFixture(t, "")
	calls := recordInterpretations(t)
	result := Compile(context.Background(), Config{Dir: directory, Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	if len(*calls) != 2 {
		t.Fatalf("source needing the complete surface was loaded %d times, want 2", len(*calls))
	}
	if retry := (*calls)[1]; retry.TolerateTypeErrors || len(retry.BuildFlags) == 0 {
		t.Fatalf("retry ran with tolerance=%t build flags=%v", retry.TolerateTypeErrors, retry.BuildFlags)
	}
}

func TestADeclarationErrorThatPreventsTheSurfaceRetryIsReportedInsteadOfGeneratedSymbols(t *testing.T) {
	directory := writeSurfaceRetryFixture(t, `
func (*Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(golem.ScopedReads[Note]())
}
`)
	result := Compile(context.Background(), Config{Dir: directory, Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil {
		t.Fatal("an invalid declaration compiled")
	}
	codes := make([]string, len(result.Diagnostics))
	for index, diagnostic := range result.Diagnostics {
		codes[index] = diagnostic.Code
	}
	if len(codes) != 1 || codes[0] != "P1_METHOD_RECEIVER" {
		t.Fatalf("diagnostics = %v, want only the declaration error: %#v", codes, result.Diagnostics)
	}
}
