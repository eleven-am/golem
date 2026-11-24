package compatibility

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGQLGenConsumerMVS(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	module := read(filepath.Join("testdata", "framesgqlgen", "go.mod"))
	module = append(module, []byte(fmt.Sprintf("\nreplace github.com/eleven-am/golem/go => %q\n", moduleRoot))...)
	write("go.mod", module)
	// Preserve the source checkout and Frames. Only the two direct version
	// checksums are retained from Frames; other dependencies use Golem's sums
	// and the existing local cache. Missing cache entries fail offline.
	sums := append(read(filepath.Join(moduleRoot, "go.sum")), '\n')
	sums = append(sums, read(filepath.Join("testdata", "framesgqlgen", "go.sum"))...)
	write("go.sum", sums)
	// Reuse the actual nested DTO/input fixture, including enums, oneOf,
	// InputValue, computed fields and ordinary stored model fields.
	write("app/schema.go", read(filepath.Join(moduleRoot, "internal", "compiler", "compile", "testdata", "objecttypes", "schema.go")))
	expectedVersions := selectedGeneratorVersions(t, moduleRoot, "readonly")
	checkVersions := func() {
		t.Helper()
		versions := selectedGeneratorVersions(t, dir, "mod")
		if versions != expectedVersions {
			t.Fatalf("consumer selected generator dependencies: %+v; want current Golem requirements: %+v", versions, expectedVersions)
		}
	}
	checkVersions()
	for _, arguments := range [][]string{
		{"inspect", "--schema", "./app"},
		{"migration", "new", "--name", "initial", "--schema", "./app"},
		{"generate", "--schema", "./app", "--app-out", "./app"},
		{"check", "--schema", "./app", "--app-out", "./app"},
	} {
		command := append([]string{"run", "-mod=mod", "github.com/eleven-am/golem/go/cmd/golem"}, arguments...)
		runOfflineConsumerGo(t, dir, command...)
		if arguments[0] == "generate" {
			// First generation introduces the executable server's imports.
			// Follow the documented consumer workflow before readonly check.
			runOfflineConsumerGo(t, dir, "mod", "tidy")
			checkVersions()
		}
	}
	// Compile every emitted binding and the actual gqlgen executable server
	// using the same consumer dependency graph, without a live database.
	runOfflineConsumerGo(t, dir, "test", "-mod=mod", "-p=1", "-count=1", "-timeout=30s", "./...")
	checkVersions()
}

type generatorDependencyVersions struct {
	GQLGen    string
	GQLParser string
}

func selectedGeneratorVersions(t *testing.T, directory, moduleMode string) generatorDependencyVersions {
	t.Helper()
	output := runOfflineConsumerGo(t, directory, "list", "-mod="+moduleMode, "-m", "-f", "{{.Path}} {{.Version}}", "all")
	var versions generatorDependencyVersions
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "github.com/99designs/gqlgen":
			versions.GQLGen = fields[1]
		case "github.com/vektah/gqlparser/v2":
			versions.GQLParser = fields[1]
		}
	}
	if versions.GQLGen == "" || versions.GQLParser == "" {
		t.Fatalf("complete module graph omits generator dependencies: %+v", versions)
	}
	return versions
}
