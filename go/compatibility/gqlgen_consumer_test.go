package compatibility

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Frames pins these versions in its real consumer module. This gate exercises
// MVS for the generator itself as well as the generated server; a CLI compiled
// in Golem's root module would silently test a different gqlgen API. It is a
// small consumer compatibility gate, not a port of the Frames application.
func TestFramesGQLGenConsumerMVS(t *testing.T) {
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
	checkVersions := func() {
		t.Helper()
		output := runOfflineConsumerGo(t, dir, "list", "-mod=mod", "-m", "-f", "{{.Path}} {{.Version}}", "github.com/99designs/gqlgen", "github.com/vektah/gqlparser/v2")
		if strings.TrimSpace(output) != "github.com/99designs/gqlgen v0.17.94\ngithub.com/vektah/gqlparser/v2 v2.5.36" {
			t.Fatalf("consumer selected unexpected generator dependencies: %s", output)
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
