package clientmanifest

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/codegen/surface"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func compileMutationScopeFixture(t *testing.T, directory string) (ir.CompilationIR, ir.Fingerprint, string) {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: directory, Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("compile %s: %#v", directory, compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	var app modelcodegen.PackageSpec
	for _, spec := range compiled.Packages {
		if spec.ImportPath == compilation.Model.Schema.PackagePath {
			app = spec
		}
	}
	bootstrap, err := surface.Emit(surface.Request{Compilation: compilation, Packages: compiled.Packages, AppPackage: app})
	if err != nil {
		t.Fatal(err)
	}
	// Generation discovers attached hooks after schema compilation. Exercise
	// that production boundary instead of inventing hooks from resolver text.
	discovered := bindings.DiscoverAndEmit(context.Background(), bindings.DiscoveryRequest{
		Dir: directory, ModulePath: compiled.ModulePath, Compilation: compilation,
		Packages: compiled.Packages, ModelBootstrap: bootstrap,
	})
	if len(discovered.Diagnostics) != 0 {
		t.Fatalf("discover %s: %#v", directory, discovered.Diagnostics)
	}
	compilation.Contract.Methods = append(compilation.Contract.Methods, discovered.Methods...)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	return compilation, compiled.ContractFingerprint, document.SDL
}

func TestCompiledGeneratedMutationScopesCoverNestedWritesFKActionsHooksAndPrivateModels(t *testing.T) {
	compilation, fingerprint, sdl := compileMutationScopeFixture(t, "testdata/mutationscope")
	manifest, err := Build(compilation, fingerprint, sdl)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range compilation.Contract.Models {
		if !model.Exposed {
			continue
		}
		want := &MutationScope{Kind: MutationScopeModels, Models: []string{model.GraphQLName}}
		switch model.GraphQLName {
		case "Team", "Invite":
			want.Models = []string{"Invite", "Team"}
		case "Hooked", "Public":
			want = &MutationScope{Kind: MutationScopeUnscoped}
		}
		for _, name := range []string{model.Roots.Create, model.Roots.Update, model.Roots.Upsert, model.Roots.Delete, model.Roots.UpdateMany, model.Roots.DeleteMany} {
			if name == "" {
				continue
			}
			root := manifest.Roots[name]
			if !reflect.DeepEqual(root.MutationScope, want) {
				t.Errorf("%s mutationScope=%#v, want %#v", name, root.MutationScope, want)
			}
			if !reflect.DeepEqual(root.Models, []string{model.GraphQLName}) {
				t.Errorf("%s response models changed: %v", name, root.Models)
			}
		}
		for _, name := range []string{model.Roots.FindOne, model.Roots.FindMany} {
			if name != "" && manifest.Roots[name].MutationScope != nil {
				t.Errorf("read %s gained mutation scope", name)
			}
		}
	}
	if manifest.FormatVersion != 0 || len(manifest.RequiredCapabilities) != 0 {
		t.Fatalf("optional hint changed protocol requirements: %#v", manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PrivateTrail") {
		t.Fatal("private model name leaked into manifest")
	}
	assertMutationScopeWireChoices(t, encoded)
}

func TestCompiledCustomNestedMutationNeverUsesItsResponseModelsAsWriteScope(t *testing.T) {
	compilation, fingerprint, sdl := compileMutationScopeFixture(t, "../../../runtime/testdata/p10operations")
	manifest, err := Build(compilation, fingerprint, sdl)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"submitNested", "submitInvite", "submitInvites", "inviteMember", "createInvite", "createTeam"} {
		root := manifest.Roots[name]
		if root.MutationScope == nil || root.MutationScope.Kind != MutationScopeUnscoped || len(root.MutationScope.Models) != 0 {
			t.Errorf("%s must be unscoped: %#v", name, root)
		}
	}
	for _, name := range []string{"submitNested", "submitInvite", "submitInvites", "searchInvites"} {
		if !reflect.DeepEqual(manifest.Roots[name].Models, []string{"Invite"}) {
			t.Errorf("%s response models=%v", name, manifest.Roots[name].Models)
		}
	}
	if manifest.Roots["searchInvites"].MutationScope != nil || !reflect.DeepEqual(manifest.Models["Team"].Computed["newestInvite"], []string{"Invite"}) {
		t.Fatalf("read/enrichment metadata changed: %#v", manifest)
	}
}

func assertMutationScopeWireChoices(t *testing.T, encoded []byte) {
	t.Helper()
	var wire struct {
		Roots  map[string]map[string]json.RawMessage `json:"roots"`
		Models map[string]json.RawMessage            `json:"models"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for name, root := range wire.Roots {
		data, ok := root["mutationScope"]
		if !ok {
			continue
		}
		var scope map[string]json.RawMessage
		if err := json.Unmarshal(data, &scope); err != nil {
			t.Fatal(err)
		}
		switch string(scope["kind"]) {
		case `"unscoped"`:
			if len(scope) != 1 {
				t.Errorf("%s unscoped wire=%s", name, data)
			}
		case `"models"`:
			var models []string
			if err := json.Unmarshal(scope["models"], &models); err != nil {
				t.Fatal(err)
			}
			if len(scope) != 2 || len(models) == 0 {
				t.Errorf("%s models wire=%s", name, data)
			}
			if !sort.StringsAreSorted(models) {
				t.Errorf("%s scope models are unsorted: %v", name, models)
			}
			for index, model := range models {
				if _, known := wire.Models[model]; !known || index > 0 && models[index-1] == model {
					t.Errorf("%s scope has unknown or duplicate model: %v", name, models)
				}
			}
		default:
			t.Errorf("%s unknown scope wire=%s", name, data)
		}
	}
}

func TestMutationScopeIsAnOptionalHintAcrossExistingManifestFormats(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		compilation := shapeCompilation()
		sdl := shapeSDL
		if version >= 2 {
			compilation.Contract.GraphQLTypes = []ir.GraphQLTypeDefinitionIR{{
				Kind: ir.GraphQLTypeObject, Name: "Transport", Go: ir.GoNamedTypeIR{PackagePath: "example.test/transport", Name: "Transport"},
				Fields: []ir.GraphQLObjectFieldIR{{Name: "value", GoName: "Value", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}},
			}}
			sdl += "\ntype Transport { value: String! }\n"
		}
		if version == 3 {
			compilation.Contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: 1}
		}
		manifest, err := Build(compilation, "contract", sdl)
		if err != nil {
			t.Fatal(err)
		}
		wireVersion := manifest.FormatVersion
		if wireVersion == 0 {
			wireVersion = 1
		}
		if wireVersion != version || !reflect.DeepEqual(manifest.RequiredCapabilities, compilation.Contract.RequiredClientCapabilities()) {
			t.Fatalf("format %d protocol requirements changed: %#v", version, manifest)
		}
		if scope := manifest.Roots["createPost"].MutationScope; !reflect.DeepEqual(scope, &MutationScope{Kind: MutationScopeModels, Models: []string{"Post"}}) {
			t.Fatalf("format %d lost scope: %#v", version, scope)
		}
		if manifest.Roots["posts"].MutationScope != nil || !reflect.DeepEqual(manifest.Roots["posts"].Models, []string{"Post"}) {
			t.Fatalf("format %d changed read metadata", version)
		}
	}
	// Old serialized manifests can omit the optional hint without acquiring a
	// false model bound. Clients interpret that absence as unscoped.
	var old Root
	if err := json.Unmarshal([]byte(`{"kind":"generated","models":["Post"],"shape":"row","nullable":false}`), &old); err != nil || old.MutationScope != nil {
		t.Fatalf("old optional scope=%#v, error=%v", old, err)
	}
}

func TestUnknownMutationReachIsUnscoped(t *testing.T) {
	for _, name := range []string{"unknown relation endpoint", "missing relation edge", "missing relation metadata", "unknown hook owner", "missing hook owner", "through private model"} {
		t.Run(name, func(t *testing.T) {
			compilation := shapeCompilation()
			unknown := ir.ModelID("unknown")
			switch name {
			case "unknown relation endpoint":
				compilation.Model.Relations = []ir.RelationIR{{ID: "edge", SourceModel: "post", TargetModel: unknown}}
			case "missing relation edge":
				compilation.Model.Models[0].Fields = []ir.FieldIR{{Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: "missing"}}}
			case "missing relation metadata":
				compilation.Model.Models[0].Fields = []ir.FieldIR{{Kind: ir.FieldRelation}}
			case "unknown hook owner":
				compilation.Contract.Methods = []ir.AttachedMethodIR{{Kind: "hook", ModelID: &unknown}}
			case "missing hook owner":
				compilation.Contract.Methods = []ir.AttachedMethodIR{{Kind: "hook"}}
			case "through private model":
				compilation.Model.Relations = []ir.RelationIR{{ID: "edge", SourceModel: "post", TargetModel: "post", Through: &ir.ThroughRelationIR{ModelID: unknown}}}
			}
			if name == "missing relation edge" || name == "missing relation metadata" {
				// Build rejects malformed relation fields before publishing a manifest.
				// Pin the conservative reach decision at its owner independently.
				models := map[ir.ModelID]ir.ModelDeclIR{}
				for _, model := range compilation.Model.Models {
					models[model.ID] = model
				}
				contracts := map[ir.ModelID]ir.ModelContractIR{}
				for _, contract := range compilation.Contract.Models {
					contracts[contract.ModelID] = contract
				}
				scopes := generatedMutationScopes(compilation, models, contracts, nil)
				if scope, ok := scopes["post"]; !ok || scope.Kind != MutationScopeUnscoped || len(scope.Models) != 0 {
					t.Fatalf("scope=%#v, present=%v", scope, ok)
				}
				if _, err := Build(compilation, "contract", shapeSDL); err == nil {
					t.Fatal("malformed relation field was accepted by the manifest builder")
				}
				return
			}
			manifest, err := Build(compilation, "contract", shapeSDL)
			if err != nil {
				t.Fatal(err)
			}
			if scope := manifest.Roots["createPost"].MutationScope; scope == nil || scope.Kind != MutationScopeUnscoped || len(scope.Models) != 0 {
				t.Fatalf("scope=%#v", scope)
			}
		})
	}
}
