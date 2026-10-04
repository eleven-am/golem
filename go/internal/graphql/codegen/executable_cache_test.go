package codegen

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestExecutableCacheRunsGQLGenOnceAndStampsEveryDigest(t *testing.T) {
	first := socialEmitRequest(t)
	first.GenerationDigest = "provisional-digest"
	second := first
	second.GenerationDigest = "final-digest"
	second.GeneratorVersion = "other-generator"
	second.TemplateABIVersion = "other-template"

	cache := &ExecutableCache{}
	cachedFirst, cachedSecond := first, second
	cachedFirst.Executables, cachedSecond.Executables = cache, cache
	assertEmitMatchesUncached(t, cachedFirst, first)
	assertEmitMatchesUncached(t, cachedSecond, second)
	if got := cache.Generations(); got != 1 {
		t.Fatalf("gqlgen generations = %d, want one shared by both digests", got)
	}
}

func TestExecutableCacheRegeneratesWhenTheSDLChangesBetweenEmits(t *testing.T) {
	request := socialEmitRequest(t)
	cache := &ExecutableCache{}
	cached := request
	cached.Executables = cache
	if _, err := Emit(cached); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.SDL += "\n# changed between emits\n"
	cachedChanged := changed
	cachedChanged.Executables = cache
	assertEmitMatchesUncached(t, cachedChanged, changed)
	if got := cache.Generations(); got != 2 {
		t.Fatalf("gqlgen generations = %d, want a second run for the changed SDL", got)
	}
}

func TestExecutableKeyCoversEveryGQLGenInputAndIgnoresTheStamp(t *testing.T) {
	tree := t.TempDir()
	writeTreeFile(t, tree, "go.mod", "module example.test/framework\n")
	writeTreeFile(t, tree, "graphql/object.go", "package graphql\n\ntype PreparedObject struct{}\n")
	moduleDir := t.TempDir()
	base := Request{SDL: "type Query { ok: String }", GraphQLImportPath: "example.test/framework/graphql", Env: []string{"GOWORK=off"}, GenerationDigest: "one", GeneratorVersion: "one", TemplateABIVersion: "one"}
	module := privateModule{source: []byte("module golem.invalid/gqlgentmp\n"), directory: tree}
	key := func(request Request, moduleDir string, module privateModule) [32]byte {
		t.Helper()
		value, err := executableKey(request, moduleDir, module)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := key(base, moduleDir, module)

	stamped := base
	stamped.GenerationDigest, stamped.GeneratorVersion, stamped.TemplateABIVersion = "two", "two", "two"
	if key(stamped, moduleDir, module) != original {
		t.Fatal("generation stamp changed the gqlgen cache key")
	}
	writeTreeFile(t, tree, "graphql/object_test.go", "package graphql\n")
	writeTreeFile(t, tree, "graphql/testdata/fixture.go", "package fixture\n")
	writeTreeFile(t, tree, "nested/go.mod", "module example.test/nested\n")
	writeTreeFile(t, tree, "nested/nested.go", "package nested\n")
	writeTreeFile(t, tree, "golemgqlgentmp123/zz.go", "package golemgqlgen\n")
	if key(base, moduleDir, module) != original {
		t.Fatal("files gqlgen never loads changed the gqlgen cache key")
	}

	requestChanges := []struct {
		name   string
		change func(*Request, *string, *privateModule)
	}{
		{"sdl", func(request *Request, _ *string, _ *privateModule) { request.SDL += "\n" }},
		{"graphql import", func(request *Request, _ *string, _ *privateModule) {
			request.GraphQLImportPath = "example.test/fork/graphql"
		}},
		{"request environment", func(request *Request, _ *string, _ *privateModule) { request.Env = []string{"GOWORK=/tmp/go.work"} }},
		{"module directory", func(_ *Request, directory *string, _ *privateModule) { *directory = t.TempDir() }},
		{"private module", func(_ *Request, _ *string, module *privateModule) {
			module.source = []byte("module golem.invalid/gqlgentmp\n\nrequire example.test/framework v1.0.0\n")
		}},
		{"source directory", func(_ *Request, _ *string, module *privateModule) { module.directory = t.TempDir() }},
	}
	for _, test := range requestChanges {
		request, directory, changed := base, moduleDir, module
		test.change(&request, &directory, &changed)
		if key(request, directory, changed) == original {
			t.Fatalf("changing the %s did not change the gqlgen cache key", test.name)
		}
	}

	environmentChanges := []struct {
		name   string
		change func()
	}{
		{"process environment", func() { t.Setenv("GOLEM_EXECUTABLE_CACHE_PROBE", "changed") }},
		{"edited source", func() {
			writeTreeFile(t, tree, "graphql/object.go", "package graphql\n\ntype PreparedObject struct{ Field string }\n")
		}},
		{"added source", func() { writeTreeFile(t, tree, "golem/golem.go", "package golem\n") }},
		{"module requirements", func() { writeTreeFile(t, tree, "go.sum", "example.test/dependency v1.0.0 h1:changed\n") }},
		{"renamed source", func() {
			if err := os.Rename(filepath.Join(tree, "golem", "golem.go"), filepath.Join(tree, "golem", "renamed.go")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range environmentChanges {
		before := key(base, moduleDir, module)
		test.change()
		if key(base, moduleDir, module) == before {
			t.Fatalf("changing the %s did not change the gqlgen cache key", test.name)
		}
	}
}

func socialEmitRequest(t *testing.T) Request {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		PackageName: "social", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		GenerationDigest: "generation-digest", GeneratorVersion: "generator-version", TemplateABIVersion: "template-abi",
	}
	for _, model := range compiled.Compilation.Model.Models {
		request.MutationModels = append(request.MutationModels, MutationModel{PackagePath: model.Go.PackagePath, GoName: model.Go.Name})
	}
	return request
}

func assertEmitMatchesUncached(t *testing.T, cached, uncached Request) {
	t.Helper()
	if uncached.Executables != nil {
		t.Fatal("reference emit must not use an executable cache")
	}
	got, err := Emit(cached)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Emit(uncached)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Source, want.Source) || got.SDLFingerprint != want.SDLFingerprint || len(got.Files) != len(want.Files) {
		t.Fatal("cached GraphQL adapter differs from an uncached emit")
	}
	for index := range want.Files {
		if got.Files[index].Filename != want.Files[index].Filename || !bytes.Equal(got.Files[index].Source, want.Files[index].Source) {
			t.Fatalf("cached gqlgen file %s differs from an uncached emit", want.Files[index].Filename)
		}
		if !bytes.Contains(got.Files[index].Source, []byte("generation "+uncached.GenerationDigest+"; generator "+uncached.GeneratorVersion+"; template "+uncached.TemplateABIVersion+".")) {
			t.Fatalf("cached gqlgen file %s carries another request's stamp", want.Files[index].Filename)
		}
	}
}

func writeTreeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
