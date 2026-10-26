package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/methods"
	graphqlcodegen "github.com/eleven-am/golem/go/internal/graphql/codegen"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const kernelModeSchema = `package app

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type Principal struct{ ID string }

type Note struct {
	_      struct{} ` + "`golem:\"model;id=kernelmode.Note;table=notes\"`" + `
	ID     int64    ` + "`db:\"id\" golem:\"id=kernelmode.Note.ID;pk\"`" + `
	Owner  string   ` + "`db:\"owner\"`" + `
	Body   string   ` + "`db:\"body\"`" + `
}

func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(
		golem.GraphQL[Note](golem.GraphQLHookOwned(Notes.Owner)),
		golem.Subscriptions[Note](),
	)
}

func (Note) DefinePolicy(rules *golem.Rules[Note], principal Principal) {
	rules.CanRead(Notes.Owner.Eq(principal.ID))
	rules.CanCreate(Notes.Owner.Eq(principal.ID))
}

func (Note) BeforeCreate(ctx context.Context, request *NoteCreateRequest) error {
	return golem.SetCreate(request, Notes.Owner, golem.ActorFrom[Principal](ctx).ID)
}

type ArchiveArgs struct {
	Owner string ` + "`golem:\"graphql=owner\"`" + `
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "kernel_mode")
	DECLARATION
	golem.Actor[Principal](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "archiveNotes", ArchiveNotes)
}

func ArchiveNotes(context.Context, *Caller[Principal], ArchiveArgs) (int32, error) {
	return 0, nil
}
`

type kernelModeFixture struct {
	root string
}

func newKernelModeFixture(t *testing.T) kernelModeFixture {
	t.Helper()
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf("module example.test/kernelmode\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "app/zz_golem_registry.gen.go", "package app\ntype Caller[P any] struct{}\n")
	fixture := kernelModeFixture{root: root}
	fixture.setMode(t, false)
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare module: %v\n%s", err, output)
	}
	return fixture
}

func (fixture kernelModeFixture) setMode(t *testing.T, kernel bool) {
	t.Helper()
	declaration := ""
	if kernel {
		declaration = "golem.WithoutGraphQL(schema)"
	}
	writePipelineAcceptanceFile(t, fixture.root, "app/schema.go", strings.Replace(kernelModeSchema, "DECLARATION", declaration, 1))
}

func (fixture kernelModeFixture) request(cache *graphqlcodegen.ExecutableCache) Request {
	return Request{
		Compile:            compile.Config{Dir: filepath.Join(fixture.root, "app"), Pattern: ".", Root: "DefineSchema"},
		AppPackage:         modelcodegen.PackageSpec{ImportPath: "example.test/kernelmode/app", PackageName: "app", Directory: filepath.Join(fixture.root, "app")},
		Lowerers:           []physical.Lowerer{sqliteprovider.New()},
		Env:                []string{"GOWORK=off"},
		GraphQLExecutables: cache,
	}
}

func TestKernelModeRunsNoGQLGenAndEmitsNoGraphQLArtifacts(t *testing.T) {
	fixture := newKernelModeFixture(t)
	fixture.setMode(t, true)
	cache := &graphqlcodegen.ExecutableCache{}
	request := fixture.request(cache)
	result, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactGraphQLGo || artifact.Kind == manifest.ArtifactGraphQLSDL {
			t.Fatalf("kernel mode emitted GraphQL artifact %s (%s)", artifact.Path, artifact.Kind)
		}
		if bytes.Contains(artifact.Content, []byte("GraphQLConfig")) {
			t.Fatalf("kernel artifact %s references the GraphQL adapter", artifact.Path)
		}
	}
	readOnly := request
	readOnly.ReadOnlyDiagnostics = true
	if _, err := Build(context.Background(), readOnly); err != nil {
		t.Fatal(err)
	}
	if got := cache.Generations(); got != 0 {
		t.Fatalf("kernel-mode generations = %d, want 0", got)
	}

	fixture.setMode(t, false)
	graphqlCache := &graphqlcodegen.ExecutableCache{}
	graphqlResult, err := Build(context.Background(), fixture.request(graphqlCache))
	if err != nil {
		t.Fatal(err)
	}
	if got := graphqlCache.Generations(); got != 1 {
		t.Fatalf("GraphQL-mode generations = %d, want 1", got)
	}
	kinds := map[manifest.ArtifactKind]int{}
	for _, artifact := range graphqlResult.Prospective.Artifacts {
		kinds[artifact.Kind]++
	}
	if kinds[manifest.ArtifactGraphQLGo] != 4 || kinds[manifest.ArtifactGraphQLSDL] != 1 {
		t.Fatalf("GraphQL-mode artifact kinds = %v", kinds)
	}
}

func TestKernelArtifactsAreByteIdenticalAcrossModesWithAFixedDigest(t *testing.T) {
	fixture := newKernelModeFixture(t)
	ctx := context.Background()
	graphqlAnalysis, err := Analyze(ctx, fixture.request(nil))
	if err != nil {
		t.Fatal(err)
	}
	fixture.setMode(t, true)
	kernelAnalysis, err := Analyze(ctx, fixture.request(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !graphqlAnalysis.graphql || kernelAnalysis.graphql {
		t.Fatalf("analysis modes graphql=%v kernel=%v", graphqlAnalysis.graphql, kernelAnalysis.graphql)
	}
	if kernelAnalysis.request.GraphQLExecutables != nil {
		t.Fatal("kernel-mode analysis allocated a GraphQL executable cache")
	}
	if graphqlAnalysis.modelFingerprint != kernelAnalysis.modelFingerprint || graphqlAnalysis.contractFingerprint != kernelAnalysis.contractFingerprint {
		t.Fatalf("fingerprints differ across modes: model %s/%s contract %s/%s", graphqlAnalysis.modelFingerprint, kernelAnalysis.modelFingerprint, graphqlAnalysis.contractFingerprint, kernelAnalysis.contractFingerprint)
	}
	if len(graphqlAnalysis.providers) != 1 || len(kernelAnalysis.providers) != 1 {
		t.Fatalf("providers = %d / %d", len(graphqlAnalysis.providers), len(kernelAnalysis.providers))
	}
	for index := range graphqlAnalysis.providers {
		left, right := graphqlAnalysis.providers[index], kernelAnalysis.providers[index]
		if left.Fingerprint != right.Fingerprint || left.SystemFingerprint != right.SystemFingerprint {
			t.Fatalf("provider fingerprints differ across modes: %#v / %#v", left, right)
		}
	}
	history := p8InitialSQLiteHistory(t, graphqlAnalysis.modelFingerprint, graphqlAnalysis.providers[0].Schema)
	graphqlDocuments, err := reviewedMigrationDocuments([]ReviewedMigration{history}, graphqlAnalysis.providers, graphqlAnalysis.modelFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	kernelDocuments, err := reviewedMigrationDocuments([]ReviewedMigration{history}, kernelAnalysis.providers, kernelAnalysis.modelFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for provider, document := range graphqlDocuments {
		if !bytes.Equal(document, kernelDocuments[provider]) {
			t.Fatalf("reviewed migration documents differ for %s", provider)
		}
	}

	digest := strings.Repeat("ab", 32)
	emit := func(analysis *Analysis) []manifest.Artifact {
		t.Helper()
		artifacts, err := emitArtifacts(analysis.compiled, analysis.request, analysis.compilation, analysis.entries, analysis.providers, graphqlDocuments, analysis.modelFingerprint, analysis.contractFingerprint, digest, analysis.generatorVersion, analysis.templateABI, analysis.graphql)
		if err != nil {
			t.Fatal(err)
		}
		return artifacts
	}
	graphqlArtifacts := emit(graphqlAnalysis)
	kernelArtifacts := emit(kernelAnalysis)
	var expected []manifest.Artifact
	removed := 0
	for _, artifact := range graphqlArtifacts {
		if artifact.Kind == manifest.ArtifactGraphQLGo || artifact.Kind == manifest.ArtifactGraphQLSDL {
			removed++
			continue
		}
		expected = append(expected, artifact)
	}
	if removed != 5 {
		t.Fatalf("GraphQL mode emitted %d GraphQL artifacts, want 5", removed)
	}
	if len(kernelArtifacts) != len(expected) {
		t.Fatalf("kernel artifacts = %d, want %d", len(kernelArtifacts), len(expected))
	}
	for index := range expected {
		left, right := expected[index], kernelArtifacts[index]
		if left.Path != right.Path || left.Kind != right.Kind || left.GeneratedHeader != right.GeneratedHeader || !bytes.Equal(left.Content, right.Content) {
			t.Fatalf("artifact %d differs across modes: %s (%s) / %s (%s)", index, left.Path, left.Kind, right.Path, right.Kind)
		}
	}
}

func TestKernelModeRejectsApplicationCodeThatReferencesTheGraphQLAdapter(t *testing.T) {
	fixture := newKernelModeFixture(t)
	fixture.setMode(t, true)
	writePipelineAcceptanceFile(t, fixture.root, "app/server.go", "package app\n\nfunc Serve(application *App[Principal]) error {\n\t_, err := application.GraphQL(GraphQLConfig[Principal]{})\n\treturn err\n}\n")
	_, err := Analyze(context.Background(), fixture.request(nil))
	var diagnostics *DiagnosticsError
	if !errors.As(err, &diagnostics) {
		t.Fatalf("error = %v", err)
	}
	for _, diagnostic := range diagnostics.Diagnostics {
		if diagnostic.Code == methods.TypeCheckCode && strings.Contains(diagnostic.Message, "GraphQL") && diagnostic.Primary.RelativeFile == "app/server.go" {
			return
		}
	}
	t.Fatalf("diagnostics = %#v", diagnostics.Diagnostics)
}
