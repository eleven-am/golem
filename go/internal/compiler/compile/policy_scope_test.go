package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestCompilePolicyScopeTemplateAndDeriveCaptureProjection(t *testing.T) {
	calls := recordInterpretations(t)
	result := Compile(context.Background(), Config{Dir: "testdata/policyscopes", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("scope template diagnostics: %+v", result.Diagnostics)
	}
	if len(*calls) != 2 || (*calls)[1].TolerateTypeErrors {
		t.Fatalf("generated scope usage did not complete the clean surface retry: %d interpretations", len(*calls))
	}
	compilation := *result.Compilation
	var template ir.ScopeTemplateIR
	for _, model := range compilation.Contract.Models {
		if len(model.ScopeTemplates) != 0 {
			template = model.ScopeTemplates[0]
		}
	}
	if template.ID == "" || template.Name != "libraries" || len(template.Parameters) != 1 || template.Parameters[0].Name != "StorageIDs" || !template.Parameters[0].Many || template.Fingerprint == "" {
		t.Fatalf("scope template lost its typed inventory: %+v", template)
	}
	projection, err := scopecontract.Projection(compilation.Model, template)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[ir.ModelID]ir.ModelDeclIR{}
	for _, model := range compilation.Model.Models {
		byID[model.ID] = model
	}
	want := map[string][]string{
		"Media": {"ID"}, "Video": {"ID", "MediaID", "StorageID"}, "StorageObject": {"ID", "VideoID", "StorageID", "State"},
	}
	for _, item := range projection {
		model := byID[item.ModelID]
		fields := map[string]bool{}
		for _, field := range model.Fields {
			for _, selected := range item.Fields {
				if field.ID == selected {
					fields[field.GoName] = true
				}
			}
		}
		for _, name := range want[model.Go.Name] {
			if !fields[name] {
				t.Fatalf("capture projection for %s omitted %s: %+v", model.Go.Name, name, item)
			}
		}
		delete(want, model.Go.Name)
	}
	if len(want) != 0 {
		t.Fatalf("capture omitted participating models: %v", want)
	}
	if err := scopecontract.Validate(compilation.Model, compilation.Contract); err != nil {
		t.Fatal(err)
	}
}

func TestCompilePolicyScopeUsesImportedGeneratedHandles(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/policyscopecross", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("imported scope diagnostics: %+v", result.Diagnostics)
	}
	for _, model := range result.Compilation.Contract.Models {
		for _, template := range model.ScopeTemplates {
			projection, err := scopecontract.Projection(result.Compilation.Model, template)
			if err != nil || len(projection) != 2 || len(projection[1].Path) != 1 {
				t.Fatalf("imported endpoint lost its exact projection: %+v err=%v", projection, err)
			}
			return
		}
	}
	t.Fatal("imported scope template was omitted")
}

func TestCompilePolicyScopeRejectsExecutableOrUncapturedDomains(t *testing.T) {
	for _, test := range []struct{ name, from, to string }{
		{"negative-related", "Medias.Videos.Some(", "Medias.Videos.None("},
		{"captured-slice", "Videos.StorageID.In(args.StorageIDs...)", "Videos.StorageID.In(capturedIDs...)"},
		{"unobservable-target", "golem.Subscriptions[StorageObject]()", "golem.ScopedReads[StorageObject]()"},
		{"helper-call", "return Medias.Videos.Some(", "return identityScope(Medias.Videos.Some("},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile("testdata/policyscopes/schema.go")
			if err != nil {
				t.Fatal(err)
			}
			source := string(payload)
			if !strings.Contains(source, test.from) {
				t.Fatal("test mutation did not match its source fixture")
			}
			source = strings.Replace(source, test.from, test.to, 1)
			if test.name == "captured-slice" {
				source += "\nvar capturedIDs []golem.UUID\n"
			}
			if test.name == "helper-call" {
				// Keep this source well-typed so only the static template
				// restriction, not a Go syntax error, rejects the helper.
				source = strings.Replace(source, "\n\t\t\t))", "\n\t\t\t)))", 1)
				source += "\nfunc identityScope(value golem.Predicate[Media]) golem.Predicate[Media] { return value }\n"
			}
			directory := writePolicyScopeFixture(t, source)
			result := Compile(context.Background(), Config{Dir: directory, Pattern: "."})
			if result.Compilation != nil || len(result.Diagnostics) == 0 {
				t.Fatal("unsupported or unobservable policy domain compiled")
			}
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == "P1_METHOD_TYPECHECK" {
					t.Fatalf("fixture failed Go type checking instead of its domain rule: %+v", result.Diagnostics)
				}
			}
		})
	}
}

func writePolicyScopeFixture(t *testing.T, source string) string {
	t.Helper()
	module, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for path, payload := range map[string]string{
		"schema.go": source,
		"go.mod":    "module example.com/policyscopes\n\ngo 1.26.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(module) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, path), []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}
