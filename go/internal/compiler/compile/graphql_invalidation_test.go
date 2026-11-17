package compile

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/clientmanifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	registrycodegen "github.com/eleven-am/golem/go/internal/codegen/registry"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestCompileInvalidationBindersResolveTemplatesAndPublishClosedSubscriptions(t *testing.T) {
	compiled := Compile(context.Background(), Config{Dir: "testdata/invalidations", Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", compiled.Diagnostics)
	}
	contract := compiled.Compilation.Contract
	if err := ir.ValidateGraphQLInvalidations(contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.CustomOperations) != 3 {
		t.Fatalf("operations=%#v", contract.CustomOperations)
	}
	for _, operation := range contract.CustomOperations {
		if operation.Invalidation == nil || len(operation.Invalidation.Dependencies) != 1 {
			t.Fatalf("invalidation=%#v", operation)
		}
		dependency := operation.Invalidation.Dependencies[0]
		if dependency.ID != ir.CustomInvalidationDependencyID(operation.ExtensionID, dependency.Name) || dependency.Binder.Actor == nil || *dependency.Binder.Actor != compiled.Compilation.Model.Schema.Actor {
			t.Fatalf("binder=%#v", dependency)
		}
		if operation.Name == "factFeed" && (dependency.ScopeTemplateID == "" || len(dependency.FactTemplateIDs) != 1) {
			t.Fatalf("unresolved templates=%#v", dependency)
		}
		args := operation.InvalidationArguments()
		if len(args) != 1 || operation.Pagination != nil && args[0].Name != "seed" || operation.Pagination == nil && args[0].Name != "take" {
			t.Fatalf("ordinary arguments=%#v", args)
		}
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	for _, signature := range []string{"scalarFeedChanges(take: Int): Boolean!", "factFeedChanges(take: Int): Boolean!", "pageFeedChanges(seed: Int!): Boolean!"} {
		if !strings.Contains(document.SDL, signature) {
			t.Fatalf("missing %q in SDL\n%s", signature, document.SDL)
		}
	}
	manifest, err := clientmanifest.Build(*compiled.Compilation, compiled.ContractFingerprint, document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 3 || !reflect.DeepEqual(manifest.RequiredCapabilities, []string{"data-incarnation-v1", "operation-invalidation-v1"}) {
		t.Fatalf("manifest=%#v", manifest)
	}
	for _, name := range []string{"scalarFeed", "factFeed", "pageFeed"} {
		root := manifest.Roots[name]
		if root.Invalidation == nil || root.Invalidation.Kind != "operation" || root.Invalidation.SubscriptionField != name+"Changes" {
			t.Fatalf("root=%#v", root)
		}
	}
	app := modelcodegen.PackageSpec{ImportPath: compiled.Compilation.Model.Schema.PackagePath, PackageName: "invalidations"}
	provider, err := sqlite.New().Lower(context.Background(), compiled.Compilation.Model, physical.LowerOptions{Namespace: "main"})
	if err != nil {
		t.Fatal(err)
	}
	providerFingerprint, err := physical.PhysicalFingerprint(provider)
	if err != nil {
		t.Fatal(err)
	}
	systemFingerprint, err := physical.SystemFingerprint(provider.Provider, provider.System)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := registrycodegen.Emit(registrycodegen.Request{
		AppPackage: app, ModelPackages: []modelcodegen.PackageSpec{app}, Actor: compiled.Compilation.Model.Schema.Actor,
		GenerationDigest: strings.Repeat("c", 64), GeneratorVersion: "test", TemplateABIVersion: "test",
		Schema: registrycodegen.SchemaInput{Model: compiled.Compilation.Model, Contract: contract, ModelFingerprint: compiled.ModelFingerprint, ContractFingerprint: compiled.ContractFingerprint,
			Providers: []registrycodegen.ProviderInput{{Schema: provider, Fingerprint: ir.Fingerprint(providerFingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String())}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"BindGeneratedInvalidationRows(GolemGeneratedSchemaBundle()", "BindGeneratedInvalidationFacts(GolemGeneratedSchemaBundle()", "GeneratedApplicationInvalidations(bindings,", "BinderName: \"BindFacts\"", "GolemGeneratedUserDescriptor, BindFacts", "bindings, err = golem.GeneratedApplicationPageQueries(bindings,"} {
		if !strings.Contains(string(registry.Source), required) {
			t.Fatalf("missing registered binding %q\n%s", required, registry.Source)
		}
	}
}
