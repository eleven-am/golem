package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestP5GeneratedExtensionFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	fixtureDir := filepath.Join(moduleRoot, "runtime", "testdata", "p5extensions")
	request := p5ExtensionGenerationRequest(fixtureDir, "github.com/eleven-am/golem/go/runtime/testdata/p5extensions")
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Prospective.GenerationDigest != second.Prospective.GenerationDigest {
		t.Fatalf("repeat generation digest differs: %s != %s", first.Prospective.GenerationDigest, second.Prospective.GenerationDigest)
	}
	firstArtifacts := p5ExtensionArtifactMap(first)
	secondArtifacts := p5ExtensionArtifactMap(second)
	if fmt.Sprint(sortedP5ExtensionKeys(firstArtifacts)) != fmt.Sprint(sortedP5ExtensionKeys(secondArtifacts)) {
		t.Fatalf("repeat artifact inventory differs: %v != %v", sortedP5ExtensionKeys(firstArtifacts), sortedP5ExtensionKeys(secondArtifacts))
	}
	for path, content := range firstArtifacts {
		if !bytes.Equal(content, secondArtifacts[path]) {
			t.Fatalf("repeat artifact differs: %s", path)
		}
		if !strings.HasPrefix(path, "runtime/testdata/p5extensions/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed generated fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}

func p5ExtensionFirstDifference(committed, generated []byte) string {
	committedLines := bytes.Split(committed, []byte("\n"))
	generatedLines := bytes.Split(generated, []byte("\n"))
	limit := len(committedLines)
	if len(generatedLines) < limit {
		limit = len(generatedLines)
	}
	for index := 0; index < limit; index++ {
		if !bytes.Equal(committedLines[index], generatedLines[index]) {
			return fmt.Sprintf("line %d committed=%q generated=%q", index+1, committedLines[index], generatedLines[index])
		}
	}
	return fmt.Sprintf("line count committed=%d generated=%d", len(committedLines), len(generatedLines))
}

func TestP5GeneratedExtensionFixtureBuildsFromCleanSourceThenRepeatsIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	cleanDir := t.TempDir()
	fixtureSource, err := os.ReadFile(filepath.Join(moduleRoot, "runtime", "testdata", "p5extensions", "fixture.go"))
	if err != nil {
		t.Fatal(err)
	}
	goMod := fmt.Sprintf("module example.test/p5extensions\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleRoot)
	if err := os.WriteFile(filepath.Join(cleanDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanDir, "fixture.go"), fixtureSource, 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = cleanDir
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare clean source-only module: %v\n%s", err, output)
	}
	request := p5ExtensionGenerationRequest(cleanDir, "example.test/p5extensions")
	request.Env = append(os.Environ(), "GOWORK=off")
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("first generation from source-only package failed: %v", err)
	}
	for _, artifact := range first.Prospective.Artifacts {
		path := filepath.Join(cleanDir, filepath.FromSlash(artifact.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second generation with final registry present failed: %v", err)
	}
	if first.Prospective.GenerationDigest != second.Prospective.GenerationDigest {
		t.Fatalf("clean first/second generation digest differs: %s != %s", first.Prospective.GenerationDigest, second.Prospective.GenerationDigest)
	}
	firstArtifacts := p5ExtensionArtifactMap(first)
	secondArtifacts := p5ExtensionArtifactMap(second)
	if fmt.Sprint(sortedP5ExtensionKeys(firstArtifacts)) != fmt.Sprint(sortedP5ExtensionKeys(secondArtifacts)) {
		t.Fatalf("clean first/second generation artifact inventory differs: %v != %v", sortedP5ExtensionKeys(firstArtifacts), sortedP5ExtensionKeys(secondArtifacts))
	}
	for path, content := range firstArtifacts {
		if !bytes.Equal(content, secondArtifacts[path]) {
			t.Fatalf("clean first/second generation artifact differs: %s", path)
		}
	}
}

func TestP5GeneratedSocialFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p5social")
	request := p5SocialGenerationRequest(directory)
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Prospective.GenerationDigest != second.Prospective.GenerationDigest {
		t.Fatalf("social repeat digest differs: %s != %s", first.Prospective.GenerationDigest, second.Prospective.GenerationDigest)
	}
	for path, content := range p5ExtensionArtifactMap(first) {
		if !bytes.Equal(content, p5ExtensionArtifactMap(second)[path]) {
			t.Fatalf("social repeat artifact differs: %s", path)
		}
		if !strings.HasPrefix(path, "runtime/testdata/p5social/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed social fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}

func TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p5social")
	request := p5SocialGenerationRequest(directory)
	request.Env = append([]string(nil), os.Environ()...)
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := request
	shuffled.Lowerers = []physical.Lowerer{sqliteprovider.New(), p5SocialPostgreSQLLowerer{delegate: postgresprovider.New()}}
	shuffled.Env = append([]string(nil), request.Env...)
	for left, right := 0, len(shuffled.Env)-1; left < right; left, right = left+1, right-1 {
		shuffled.Env[left], shuffled.Env[right] = shuffled.Env[right], shuffled.Env[left]
	}
	third, err := pipeline.Build(context.Background(), shuffled)
	if err != nil {
		t.Fatal(err)
	}
	baseline := p5ExtensionArtifactMap(first)
	for label, result := range map[string]pipeline.Result{"repeat": second, "shuffled": third} {
		if result.Prospective.GenerationDigest != first.Prospective.GenerationDigest {
			t.Fatalf("%s generation digest differs: %s != %s; model=%s/%s contract=%s/%s providers=%v/%v artifacts=%v/%v", label,
				result.Prospective.GenerationDigest, first.Prospective.GenerationDigest,
				result.ModelFingerprint, first.ModelFingerprint, result.ContractFingerprint, first.ContractFingerprint,
				result.Prospective.Manifest.ProviderFingerprints, first.Prospective.Manifest.ProviderFingerprints,
				sortedP5ExtensionKeys(p5ExtensionArtifactMap(result)), sortedP5ExtensionKeys(baseline))
		}
		candidate := p5ExtensionArtifactMap(result)
		if fmt.Sprint(sortedP5ExtensionKeys(candidate)) != fmt.Sprint(sortedP5ExtensionKeys(baseline)) {
			t.Fatalf("%s artifact inventory differs", label)
		}
		for path, content := range baseline {
			if !bytes.Equal(content, candidate[path]) {
				t.Fatalf("%s artifact differs: %s", label, path)
			}
		}
	}
	assertArtifactContains := func(path string, values ...string) {
		t.Helper()
		content := string(baseline[path])
		for _, value := range values {
			if !strings.Contains(content, value) {
				t.Fatalf("P6 artifact %s omitted %q", path, value)
			}
		}
	}
	assertArtifactContains("runtime/testdata/p5social/zz_golem_models.gen.go", "func (golemGeneratedPostFields) Aggregate", "func (golemGeneratedPostFields) Scope")
	assertArtifactContains("runtime/testdata/p5social/zz_golem_registry.gen.go", "func (client CallerPostClient[P]) Scoped", "func (client CallerPostClient[P]) GroupBy")
	assertArtifactContains("runtime/testdata/p5social/zz_golem_graphql.schema.graphqls", "aggregatePosts", "groupByPosts", "relationGroupByPosts")
}

func TestFreshP6SocialModuleGeneratesCompilesAndConstructsApp(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := t.TempDir()
	for _, name := range []string{"models.go", "policies.go", "schema.go"} {
		content, err := os.ReadFile(filepath.Join(moduleRoot, "runtime", "testdata", "p5social", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goMod := fmt.Sprintf("module example.test/p6social\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleRoot)
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(name string, arguments ...string) {
		t.Helper()
		command := exec.Command(name, arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(), "GOWORK=off")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
		}
	}
	run("go", "mod", "tidy")
	request := pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/p6social", PackageName: "p5social", Directory: directory},
		Lowerers:   []physical.Lowerer{postgresprovider.New(), sqliteprovider.New()},
		Env:        append(os.Environ(), "GOWORK=off"),
	}
	generated, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range generated.Prospective.Artifacts {
		absolute := filepath.Join(directory, filepath.FromSlash(artifact.Path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, artifact.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const construction = `package p5social

import (
	"context"
	"testing"
)

func TestConstructGeneratedP6AppSurface(t *testing.T) {
	app := &App[Principal]{}
	if app == nil { t.Fatal("generated App was not constructed") }
	_ = Posts.Scope()
	_ = Posts.Aggregate(Posts.AggregateSelect(Posts.CountAll()))
	if _, err := Open(context.Background(), Config[Principal]{}); err == nil {
		t.Fatal("Open accepted missing infrastructure")
	}
}
`
	if err := os.WriteFile(filepath.Join(directory, "p6_construct_test.go"), []byte(construction), 0o644); err != nil {
		t.Fatal(err)
	}
	run("go", "mod", "tidy")
	run("go", "test", "./...")
}

func TestP5ActiveGeneratedSocialFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p5socialactive")
	request := p5ActiveSocialGenerationRequest(directory)
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range p5ExtensionArtifactMap(first) {
		if !strings.HasPrefix(path, "runtime/testdata/p5socialactive/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed active social fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}

func TestP6GeneratedMetricsFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p6metrics")
	request := p6MetricsGenerationRequest(directory)
	first, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Prospective.GenerationDigest != second.Prospective.GenerationDigest {
		t.Fatalf("metrics repeat digest differs: %s != %s", first.Prospective.GenerationDigest, second.Prospective.GenerationDigest)
	}
	firstArtifacts, secondArtifacts := p5ExtensionArtifactMap(first), p5ExtensionArtifactMap(second)
	for path, content := range firstArtifacts {
		if !bytes.Equal(content, secondArtifacts[path]) {
			t.Fatalf("metrics repeat artifact differs: %s", path)
		}
		if !strings.HasPrefix(path, "runtime/testdata/p6metrics/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed metrics fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}

func p6MetricsGenerationRequest(directory string) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/p6metrics", PackageName: "p6metrics", Directory: directory},
		Lowerers:   []physical.Lowerer{p6MetricsPostgreSQLLowerer{delegate: postgresprovider.New()}, sqliteprovider.New()},
	}
}

func p5SocialGenerationRequest(directory string) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/p5social", PackageName: "p5social", Directory: directory},
		Lowerers:   []physical.Lowerer{p5SocialPostgreSQLLowerer{delegate: postgresprovider.New()}, sqliteprovider.New()},
	}
}

func p5ActiveSocialGenerationRequest(directory string) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/p5socialactive", PackageName: "p5socialactive", Directory: directory},
		Lowerers:   []physical.Lowerer{p5ActivePostgreSQLLowerer{delegate: postgresprovider.New()}, sqliteprovider.New()},
	}
}

func p5ExtensionGenerationRequest(directory, importPath string) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: importPath, PackageName: "p5extensions", Directory: directory},
		Lowerers: []physical.Lowerer{
			p5ExtensionPostgreSQLLowerer{delegate: postgresprovider.New()},
			sqliteprovider.New(),
		},
	}
}

const (
	p5ExtensionPostgreSQLNamespace       physical.PhysicalName = "golem_p5_generated_extensions"
	p5ExtensionPostgreSQLSystemNamespace physical.PhysicalName = "golem_p5_generated_extensions_system"
)

type p5ExtensionPostgreSQLLowerer struct{ delegate physical.Lowerer }

type p6MetricsPostgreSQLLowerer struct{ delegate physical.Lowerer }

func (lowerer p6MetricsPostgreSQLLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

func (lowerer p6MetricsPostgreSQLLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p6MetricsPostgreSQLNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p6MetricsPostgreSQLSystemNamespace
	return physical.Normalize(schema)
}

func (lowerer p5ExtensionPostgreSQLLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

type p5SocialPostgreSQLLowerer struct{ delegate physical.Lowerer }

type p5ActivePostgreSQLLowerer struct{ delegate physical.Lowerer }

func (lowerer p5SocialPostgreSQLLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

func (lowerer p5ActivePostgreSQLLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

func (lowerer p5ActivePostgreSQLLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p5ActivePostgreSQLNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p5ActivePostgreSQLSystemNamespace
	return physical.Normalize(schema)
}

func (lowerer p5SocialPostgreSQLLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p5SocialPostgreSQLNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p5SocialPostgreSQLSystemNamespace
	return physical.Normalize(schema)
}

func (lowerer p5ExtensionPostgreSQLLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p5ExtensionPostgreSQLNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p5ExtensionPostgreSQLSystemNamespace
	return physical.Normalize(schema)
}

func p5ExtensionArtifactMap(result pipeline.Result) map[string][]byte {
	artifacts := make(map[string][]byte, len(result.Prospective.Artifacts))
	for _, artifact := range result.Prospective.Artifacts {
		artifacts[artifact.Path] = append([]byte(nil), artifact.Content...)
	}
	return artifacts
}

func sortedP5ExtensionKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func p5ExtensionModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
	if !strings.HasSuffix(filepath.ToSlash(root), "/go") {
		t.Fatalf("unexpected module root %s", root)
	}
	return root
}
