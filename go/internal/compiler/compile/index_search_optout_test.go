package compile

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/clientmanifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	registrycodegen "github.com/eleven-am/golem/go/internal/codegen/registry"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestGraphQLIndexSearchOptOutArtifactsKeepTypedGoSearchOnly(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/indexsearchoptout", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	document, err := graphqlschema.Build(*result.Compilation)
	if err != nil {
		t.Fatalf("emit GraphQL schema: %v", err)
	}
	for _, forbidden := range []string{"textSearchDocumentsByContent", "searchDocumentsByMeaning", "similarDocumentsByMeaning"} {
		if strings.Contains(document.SDL, forbidden) {
			t.Fatalf("disabled index root %q leaked into SDL:\n%s", forbidden, document.SDL)
		}
	}
	client, err := clientmanifest.Build(*result.Compilation, result.ContractFingerprint, document.SDL)
	if err != nil {
		t.Fatalf("emit client manifest: %v", err)
	}
	for _, forbidden := range []string{"textSearchDocumentsByContent", "searchDocumentsByMeaning", "similarDocumentsByMeaning"} {
		if _, leaked := client.Roots[forbidden]; leaked {
			t.Fatalf("disabled index root %q leaked into client manifest: %#v", forbidden, client.Roots)
		}
	}

	packagePath := result.Compilation.Model.Models[0].Go.PackagePath
	app := modelcodegen.PackageSpec{ImportPath: packagePath, PackageName: "indexsearchoptout", Directory: t.TempDir()}
	registry, err := registrycodegen.EmitSurface(registrycodegen.SurfaceRequest{
		AppPackage:    app,
		ModelPackages: []modelcodegen.PackageSpec{app},
		Actor:         result.Compilation.Model.Schema.Actor,
		Model:         result.Compilation.Model,
		Contract:      result.Compilation.Contract,
	})
	if err != nil {
		t.Fatalf("emit typed Go search surface: %v", err)
	}
	source := string(registry.Source)
	for _, required := range []string{"TextSearchContent(", "SearchMeaning(", "SimilarMeaning("} {
		if !strings.Contains(source, required) {
			t.Fatalf("typed Go search surface is missing %q:\n%s", required, source)
		}
	}
}
