package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type fakeLowerer struct {
	manifest physical.ProviderManifest
	fail     error
}

func (f fakeLowerer) Manifest() physical.ProviderManifest { return f.manifest }
func (f fakeLowerer) Lower(ctx context.Context, _ ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	if err := ctx.Err(); err != nil {
		return physical.PhysicalSchema{}, err
	}
	if f.fail != nil {
		return physical.PhysicalSchema{}, f.fail
	}
	namespace := options.Namespace
	if namespace == "" {
		if f.manifest.Provider == ir.SQLite {
			namespace = "main"
		} else {
			namespace = "public"
		}
	}
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: f.manifest, Namespace: physical.Namespace{Name: namespace},
	}, nil
}

func TestBuildCleanCheckoutAcrossActorModelAndApplicationPackages(t *testing.T) {
	request := multipackageRequest(t)
	// A sparse provider override is valid; SQLite intentionally uses its
	// lowerer's default while PostgreSQL receives an explicit namespace.
	request.LowerOptions = []ProviderOptions{{Provider: ir.PostgreSQL, Options: physical.LowerOptions{Namespace: "public"}}}
	result, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Prospective.GenerationDigest == "" || len(result.Providers) != 2 || len(result.Bindings) != 3 {
		t.Fatalf("incomplete pipeline result: %#v", result)
	}
	if result.ModelFingerprint == "" || result.ContractFingerprint == "" || result.ContractFingerprint == compileFixture(t, request).ContractFingerprint {
		t.Fatal("typed binding inventory did not change only the final contract fingerprint")
	}
	if result.ModelFingerprint != compileFixture(t, request).ModelFingerprint {
		t.Fatal("typed binding inventory changed the persisted model fingerprint")
	}
	if len(result.Prospective.Manifest.ProviderFingerprints) != len(result.Providers) {
		t.Fatal("generation manifest provider fingerprint inventory is incomplete")
	}
	for index, fingerprint := range result.Prospective.Manifest.ProviderFingerprints {
		if fingerprint.Provider != result.Providers[index].Provider.Provider || fingerprint.Fingerprint != result.Providers[index].Fingerprint || fingerprint.SystemFingerprint != result.Providers[index].SystemFingerprint || fingerprint.SystemFingerprint == "" {
			t.Fatalf("generation manifest provider fingerprint %d=%#v", index, fingerprint)
		}
	}
	var registrySource string
	var graphqlGo, graphqlSDL bool
	for _, artifact := range result.Prospective.Artifacts {
		if isGoArtifact(artifact.Kind) {
			for _, marker := range []string{result.Prospective.GenerationDigest, manifest.GeneratorVersion, manifest.TemplateABIVersion} {
				if !bytes.Contains(artifact.Content, []byte(marker)) {
					t.Fatalf("Go artifact %s lacks generation identity %q", artifact.Path, marker)
				}
			}
		}
		if artifact.Kind == manifest.ArtifactModelGo && !bytes.Contains(artifact.Content, []byte("GeneratedStampedPackageDescriptors")) {
			t.Fatalf("final model artifact %s is not generation-stamped", artifact.Path)
		}
		if artifact.Kind == manifest.ArtifactBindingsGo && !bytes.Contains(artifact.Content, []byte("GeneratedStampedPackageBindings")) {
			t.Fatalf("final binding artifact %s is not generation-stamped", artifact.Path)
		}
		if artifact.Kind == manifest.ArtifactMetadata || artifact.Kind == manifest.ArtifactSnapshot || artifact.Kind == manifest.ArtifactProvider {
			if firstLine(artifact.Content) != metadataHeader || !json.Valid(artifact.Content) || !bytes.Contains(artifact.Content, []byte(result.Prospective.GenerationDigest)) {
				t.Fatalf("metadata artifact %s is not safely owned and stamped", artifact.Path)
			}
			if artifact.Kind == manifest.ArtifactProvider {
				var envelope generatedEnvelope
				if err := json.Unmarshal(artifact.Content, &envelope); err != nil {
					t.Fatal(err)
				}
				var matched bool
				for _, provider := range result.Providers {
					if provider.Provider.Provider == envelope.Provider {
						matched = envelope.PhysicalFingerprint == provider.Fingerprint && envelope.SystemFingerprint == provider.SystemFingerprint && envelope.SystemFingerprint != ""
					}
				}
				if !matched {
					t.Fatalf("provider artifact %s does not bind both physical and system fingerprints", artifact.Path)
				}
			}
		}
		if artifact.Kind == manifest.ArtifactRegistryGo {
			registrySource = string(artifact.Content)
		}
		if artifact.Kind == manifest.ArtifactGraphQLGo {
			graphqlGo = strings.HasSuffix(artifact.Path, "/zz_golem_graphql.gen.go") && bytes.Contains(artifact.Content, []byte("func (app *App[P]) GraphQL")) && bytes.Contains(artifact.Content, []byte("NewGeneratedExecutor")) && bytes.Contains(artifact.Content, []byte("NewExecutableSchema")) && bytes.Contains(artifact.Content, []byte("ExecutableSchema: golemGeneratedExecutable")) && bytes.Contains(artifact.Content, []byte("app.ForPrincipal")) && bytes.Contains(artifact.Content, []byte("NewCallerMutationExecution")) && bytes.Contains(artifact.Content, []byte("CallerMutationModel")) && bytes.Contains(artifact.Content, []byte(string(result.ContractFingerprint)))
		}
		if artifact.Kind == manifest.ArtifactGraphQLSDL {
			graphqlSDL = strings.HasSuffix(artifact.Path, "/zz_golem_graphql.schema.graphqls") && firstLine(artifact.Content) == graphqlGeneratedHeader && bytes.Contains(artifact.Content, []byte("type Query"))
		}
	}
	if !graphqlGo || !graphqlSDL {
		t.Fatalf("canonical GraphQL artifacts missing or incomplete: go=%v sdl=%v", graphqlGo, graphqlSDL)
	}
	modelBytes, _ := ir.CanonicalModel(result.Compilation.Model)
	contractBytes, _ := ir.CanonicalContract(result.Compilation.Contract)
	for _, payload := range [][]byte{modelBytes, contractBytes} {
		if !strings.Contains(registrySource, strconv.Quote(string(payload))) {
			t.Fatal("application registry does not embed the exact canonical logical schema payload")
		}
	}
	for _, provider := range result.Providers {
		payload, encodeErr := physical.CanonicalEncode(provider.Schema)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if !strings.Contains(registrySource, strconv.Quote(string(payload))) {
			t.Fatalf("application registry does not embed exact %s physical schema payload", provider.Provider.Provider)
		}
	}
	if !strings.Contains(registrySource, "func GolemGeneratedSchemaBundle() golem.SchemaBundle") || strings.Contains(registrySource, "internal/compiler/ir") || strings.Contains(registrySource, "internal/physical") {
		t.Fatal("application schema bundle accessor is missing or leaks internal schema packages")
	}
	if matches, _ := filepath.Glob(filepath.Join(testdata(t, "multipkg"), "models", "zz_golem_*.go")); len(matches) != 0 {
		t.Fatalf("in-memory build wrote model files: %v", matches)
	}
}

func TestLowerersAreAnAvailableRegistryForSingleProviderSchemas(t *testing.T) {
	model := ir.ModelIR{Providers: []ir.Provider{ir.SQLite}}
	result, err := lowerProviders(context.Background(), model, defaultLowerers(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Provider.Provider != ir.SQLite {
		t.Fatalf("single-provider lowering = %#v", result)
	}
	if _, err := lowerProviders(context.Background(), model, defaultLowerers(), []ProviderOptions{{Provider: ir.PostgreSQL}}); err == nil || !strings.Contains(err.Error(), "undeclared provider") {
		t.Fatalf("lower options for an undeclared registered provider succeeded: %v", err)
	}
	if _, err := lowerProviders(context.Background(), model, []physical.Lowerer{defaultLowerers()[1]}, nil); err == nil || !strings.Contains(err.Error(), "no lowerer") {
		t.Fatalf("missing declared lowerer succeeded: %v", err)
	}
	if _, err := lowerProviders(context.Background(), model, []physical.Lowerer{defaultLowerers()[0], defaultLowerers()[0]}, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate lowerer succeeded: %v", err)
	}
}

func TestBuildSupportsSamePackageApplicationRegistry(t *testing.T) {
	directory := testdata(t, "samepkg")
	request := Request{
		Compile:    compile.Config{Dir: moduleRoot(t), Pattern: "./internal/generate/pipeline/testdata/samepkg", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/samepkg", PackageName: "samepkg", Directory: directory},
		Lowerers:   defaultLowerers(),
	}
	result, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactRegistryGo {
			found = bytes.Contains(artifact.Content, []byte("GolemGeneratedBindings()"))
		}
	}
	if !found {
		t.Fatal("same-package registry did not call its local package binding accessor")
	}
}

func TestBuildCompilesEveryCurrentlyEnabledPolicyHandleFamily(t *testing.T) {
	directory := testdata(t, "capabilities")
	request := Request{
		Compile:    compile.Config{Dir: moduleRoot(t), Pattern: "./internal/generate/pipeline/testdata/capabilities", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/capabilities", PackageName: "capabilities", Directory: directory},
		Lowerers:   []physical.Lowerer{sqliteprovider.New(), postgresqlprovider.New()},
	}
	result, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	coveredProviders := map[ir.Provider]bool{}
	for _, provider := range result.Providers {
		coveredProviders[provider.Provider.Provider] = true
	}
	if len(result.Providers) != 2 || !coveredProviders[ir.SQLite] || !coveredProviders[ir.PostgreSQL] {
		t.Fatalf("all-family provider coverage = %v", coveredProviders)
	}
	var modelSource string
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactModelGo {
			modelSource += string(artifact.Content)
		}
	}
	for _, handle := range []string{
		"golem.EqualField[Post, bool]",
		"golem.EqualField[Post, Status]",
		"golem.OrderedField[Post, int64]",
		"golem.ModeTextField[Post, string]",
		"golem.BytesField[Post]",
		"golem.ListField[Post, string]",
		"golem.ModeJSONField[Post]",
		"golem.NullableEqualField[Post, bool]",
		"golem.NullableOrderedField[Post, int64]",
		"golem.NullableModeTextField[Post, string]",
		"golem.NullableBytesField[Post]",
		"golem.NullableModeJSONField[Post]",
		"golem.ToOne[Post, User]",
		"golem.ToMany[User, Post]",
	} {
		if !strings.Contains(modelSource, handle) {
			t.Errorf("full-pipeline model source missing %q", handle)
		}
	}
}

func TestBuildInvalidBindingAndLoweringFailureReturnNoPartialResult(t *testing.T) {
	t.Run("binding", func(t *testing.T) {
		request := invalidRequest(t)
		result, err := Build(context.Background(), request)
		var diagnostics *DiagnosticsError
		if !errors.As(err, &diagnostics) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		found := false
		for _, diagnostic := range diagnostics.Diagnostics {
			found = found || diagnostic.Code == "P1_BINDING_POLICY_SIGNATURE"
		}
		if !found {
			t.Fatalf("binding diagnostics = %#v", diagnostics.Diagnostics)
		}
	})
	t.Run("lowerer", func(t *testing.T) {
		request := multipackageRequest(t)
		request.Lowerers[1] = fakeLowerer{manifest: physical.PostgreSQLManifest(), fail: errors.New("injected lowering failure")}
		result, err := Build(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "injected lowering failure") || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	})
}

func TestGeneratedGraphQLArtifactsAreByteIdenticalAcrossShuffledInputAndRepeatedGeneration(t *testing.T) {
	firstRequest := multipackageRequest(t)
	firstRequest.LowerOptions = []ProviderOptions{{Provider: ir.SQLite}, {Provider: ir.PostgreSQL}}
	first, err := Build(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := multipackageRequest(t)
	secondRequest.Lowerers[0], secondRequest.Lowerers[1] = secondRequest.Lowerers[1], secondRequest.Lowerers[0]
	secondRequest.LowerOptions = []ProviderOptions{{Provider: ir.PostgreSQL}, {Provider: ir.SQLite}}
	second, err := Build(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertManifestResultsEqual(t, first.Prospective, second.Prospective)

	shuffledArtifacts := append([]manifest.Artifact(nil), first.Prospective.Artifacts...)
	sort.Slice(shuffledArtifacts, func(i, j int) bool { return shuffledArtifacts[i].Path > shuffledArtifacts[j].Path })
	shuffledPackages := append([]modelcodegen.PackageSpec(nil), compileFixture(t, firstRequest).Packages...)
	sort.Slice(shuffledPackages, func(i, j int) bool { return shuffledPackages[i].ImportPath > shuffledPackages[j].ImportPath })
	if !reflect.DeepEqual(modelPackageSpecs(first.Compilation, shuffledPackages), modelPackageSpecs(first.Compilation, compileFixture(t, firstRequest).Packages)) {
		t.Fatal("package canonicalization depends on input order")
	}
	rebuilt, err := buildManifest(first.ModelFingerprint, first.ContractFingerprint, first.Providers, shuffledArtifacts, manifest.GeneratorVersion, manifest.TemplateABIVersion)
	if err != nil {
		t.Fatal(err)
	}
	assertManifestResultsEqual(t, first.Prospective, rebuilt)
}

func TestProspectiveCompileFailureAborts(t *testing.T) {
	request := multipackageRequest(t)
	request.AppPackage.PackageName = "wrongpackage"
	result, err := Build(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "prospective generated graph does not compile") || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestProspectiveExcludesOnlyHeaderVerifiedStaleManifestFiles(t *testing.T) {
	directory := testdata(t, "stale")
	request := Request{
		Compile:    compile.Config{Dir: moduleRoot(t), Pattern: "./internal/generate/pipeline/testdata/stale", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/stale", PackageName: "stale", Directory: directory},
		Lowerers:   defaultLowerers(),
		PreviousManifest: &manifest.Manifest{Artifacts: []manifest.Entry{{
			Path: "internal/generate/pipeline/testdata/stale/zz_old_golem.gen.go", Kind: manifest.ArtifactModelGo, GeneratedHeader: manifest.GeneratedHeader,
		}}},
	}
	if _, err := Build(context.Background(), request); err != nil {
		t.Fatalf("verified stale file was not excluded: %v", err)
	}
	request.PreviousManifest.Artifacts = append(request.PreviousManifest.Artifacts, manifest.Entry{
		Path: "internal/generate/pipeline/testdata/stale/unowned.go", Kind: manifest.ArtifactModelGo, GeneratedHeader: manifest.GeneratedHeader,
	})
	result, err := Build(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "exact ownership header") || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func multipackageRequest(t *testing.T) Request {
	t.Helper()
	base := testdata(t, "multipkg")
	return Request{
		Compile:    compile.Config{Dir: moduleRoot(t), Pattern: "./internal/generate/pipeline/testdata/multipkg/schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/multipkg/app", PackageName: "app", Directory: filepath.Join(base, "app")},
		Lowerers:   defaultLowerers(),
	}
}

func invalidRequest(t *testing.T) Request {
	t.Helper()
	base := testdata(t, "invalid")
	return Request{
		Compile:    compile.Config{Dir: moduleRoot(t), Pattern: "./internal/generate/pipeline/testdata/invalid/schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/invalid/app", PackageName: "app", Directory: filepath.Join(base, "app")},
		Lowerers:   defaultLowerers(),
	}
}

func defaultLowerers() []physical.Lowerer {
	return []physical.Lowerer{fakeLowerer{manifest: physical.SQLiteManifest()}, fakeLowerer{manifest: physical.PostgreSQLManifest()}}
}

func compileFixture(t *testing.T, request Request) compile.Result {
	t.Helper()
	result := compile.Compile(context.Background(), request.Compile)
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("fixture compile = %#v", result.Diagnostics)
	}
	return result
}

func assertManifestResultsEqual(t *testing.T, left, right manifest.Result) {
	t.Helper()
	if !bytes.Equal(left.Bytes, right.Bytes) || len(left.Artifacts) != len(right.Artifacts) {
		t.Fatal("prospective manifests differ")
	}
	for index := range left.Artifacts {
		if left.Artifacts[index].Path != right.Artifacts[index].Path || !bytes.Equal(left.Artifacts[index].Content, right.Artifacts[index].Content) {
			t.Fatalf("artifact %d differs: %s / %s", index, left.Artifacts[index].Path, right.Artifacts[index].Path)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal", "generate", "pipeline", "testdata", name)
}
