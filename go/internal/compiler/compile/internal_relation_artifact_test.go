package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/clientmanifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const internalRelationFixture = "../../../runtime/testdata/internalrelation"

func compileInternalRelationFixture(t *testing.T) Result {
	t.Helper()
	result := Compile(context.Background(), Config{Dir: internalRelationFixture, Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	return result
}

func TestInternalRelationIsAbsentFromEveryGraphQLTypeAndKeepsItsForeignKey(t *testing.T) {
	result := compileInternalRelationFixture(t)
	document, err := graphqlschema.Build(*result.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "internalrelation.graphqls", Input: document.SDL})
	if err != nil {
		t.Fatalf("invalid SDL: %v", err)
	}
	checked := map[string]bool{}
	for name, definition := range parsed.Types {
		if !strings.HasPrefix(name, "Child") {
			continue
		}
		checked[name] = true
		if definition.Fields.ForName("parent") != nil {
			t.Errorf("%s still carries parent", name)
		}
		for _, value := range definition.EnumValues {
			if strings.EqualFold(value.Name, "parent") {
				t.Errorf("%s enum still lists %s", name, value.Name)
			}
		}
	}
	for _, required := range []string{"Child", "ChildWhereInput", "ChildOrderByInput", "ChildCreateInput", "ChildUpdateInput"} {
		if !checked[required] {
			t.Errorf("GraphQL type %s was not generated", required)
		}
	}
	if len(checked) < 6 {
		t.Errorf("only %d Child types inspected, want the event payload among them", len(checked))
	}
	create := parsed.Types["ChildCreateInput"]
	if create == nil || create.Fields.ForName("parentID") == nil || create.Fields.ForName("parentID").Type.String() != "UUID!" {
		t.Errorf("ChildCreateInput lost its parentID foreign key: %#v", create)
	}
	if parent := parsed.Types["Parent"]; parent == nil || parent.Fields.ForName("children") == nil {
		t.Error("Parent.children must stay visible: internal is field-local")
	}
}

func TestInternalRelationIsAbsentFromTheClientManifestAndItsCounterpartHasNoInverse(t *testing.T) {
	result := compileInternalRelationFixture(t)
	document, err := graphqlschema.Build(*result.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	client, err := clientmanifest.Build(*result.Compilation, result.ContractFingerprint, document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	child, ok := client.Models["Child"]
	if !ok {
		t.Fatalf("client manifest has no Child model: %#v", client.Models)
	}
	if _, leaked := child.Relations["parent"]; leaked {
		t.Fatalf("internal relation leaked into client manifest: %#v", child.Relations)
	}
	children, ok := client.Models["Parent"].Relations["children"]
	if !ok {
		t.Fatalf("Parent.children missing from client manifest: %#v", client.Models["Parent"].Relations)
	}
	if children.Inverse != nil {
		t.Fatalf("Parent.children inverse = %q, want none", *children.Inverse)
	}
}

func TestInternalRelationKeepsItsGeneratedGoHandles(t *testing.T) {
	result := compileInternalRelationFixture(t)
	packagePath := result.Compilation.Model.Models[0].Go.PackagePath
	generated, err := modelcodegen.Emit(modelcodegen.Request{
		Compilation: *result.Compilation,
		Packages:    []modelcodegen.PackageSpec{{ImportPath: packagePath, PackageName: "internalrelation", Directory: t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var source string
	for _, file := range generated.Files {
		source += string(file.Source)
	}
	for _, required := range []string{"golemGeneratedChildParentMutationRelation", "GeneratedToOne[Child, Parent]"} {
		if !strings.Contains(source, required) {
			t.Errorf("generated Go relation handles are missing %q", required)
		}
	}
}

func TestRelationModesKeepPrecedenceAndAddInternal(t *testing.T) {
	for tags, want := range map[string][]ir.FieldMode{
		"":                            {ir.ModeVisible},
		"hidden":                      {ir.ModeHidden},
		"readonly":                    {ir.ModeReadOnly},
		"immutable":                   {ir.ModeImmutable},
		"readonly;immutable":          {ir.ModeReadOnly},
		"internal":                    {ir.ModeInternal},
		"immutable;internal":          {ir.ModeImmutable, ir.ModeInternal},
		"readonly;immutable;internal": {ir.ModeReadOnly, ir.ModeInternal},
	} {
		var field ir.RawFieldDecl
		for _, name := range strings.Split(tags, ";") {
			if name != "" {
				field.GolemAttrs = append(field.GolemAttrs, ir.RawAttribute{Name: name})
			}
		}
		got := relationModes(field)
		if len(got) != len(want) {
			t.Errorf("relationModes(%q) = %v, want %v", tags, got, want)
			continue
		}
		for index := range want {
			if got[index] != want[index] {
				t.Errorf("relationModes(%q) = %v, want %v", tags, got, want)
			}
		}
	}
}

func TestInternalRelationForeignKeyThatIsInternalAndRequiredNeedsASystemOwner(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "internalrelationowner", "models.go"))
	if err != nil {
		t.Fatal(err)
	}
	refused := Compile(context.Background(), Config{Dir: "testdata/internalrelationowner", Pattern: ".", Root: "DefineSchema"})
	if len(refused.Diagnostics) != 0 || refused.Compilation == nil {
		t.Fatalf("diagnostics = %#v", refused.Diagnostics)
	}
	if !hasInternalClientRequired(graphqlcontract.ValidateInternalFields(*refused.Compilation)) {
		t.Fatal("internal required foreign key on a create root was not refused")
	}

	directory, err := os.MkdirTemp("testdata", "internalrelationowner-system-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	cleared := strings.Replace(string(source), `golem:"internal"`, `golem:"internal;system"`, 1)
	if err := os.WriteFile(filepath.Join(directory, "models.go"), []byte(cleared), 0o600); err != nil {
		t.Fatal(err)
	}
	accepted := Compile(context.Background(), Config{Dir: directory, Pattern: ".", Root: "DefineSchema"})
	if len(accepted.Diagnostics) != 0 || accepted.Compilation == nil {
		t.Fatalf("diagnostics = %#v", accepted.Diagnostics)
	}
	if diagnostics := graphqlcontract.ValidateInternalFields(*accepted.Compilation); len(diagnostics) != 0 {
		t.Fatalf("system foreign key still refused: %#v", diagnostics)
	}
}

func hasInternalClientRequired(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "P8_GRAPHQL_INTERNAL_CLIENT_REQUIRED" {
			return true
		}
	}
	return false
}
