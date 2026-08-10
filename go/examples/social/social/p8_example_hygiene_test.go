package social_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone(t *testing.T) {
	root := socialExampleRoot(t)
	assertNoReplaceDirective(t, filepath.Join(root, "go.mod"))

	ordinary := map[string]bool{
		"FindUnique": true, "FindFirst": true, "FindMany": true, "Count": true,
		"Create": true, "Update": true, "Upsert": true, "Delete": true,
		"UpdateMany": true, "DeleteMany": true, "Aggregate": true,
		"GroupBy": true, "RelationGroupBy": true,
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.Contains(filepath.ToSlash(path), "/golemgqlgen/") || strings.HasPrefix(entry.Name(), "zz_golem_") {
			return walkErr
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if strings.Contains(value, "/internal/") {
				t.Errorf("public example imports internal package %q from %s", value, path)
			}
		}
		parsed, err = parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && ordinary[function.Name.Name] {
				t.Errorf("handwritten ordinary backend method %s in %s", function.Name.Name, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP8ExampleGeneratedAndReviewedInventoryIsCheckedIn(t *testing.T) {
	root := socialExampleRoot(t)
	for _, relative := range []string{
		".golem/generated-manifest.json",
		".golem/generated/model.snapshot.json",
		".golem/generated/contract.metadata.json",
		".golem/generated/sqlite.physical.snapshot.json",
		".golem/generated/postgresql.physical.snapshot.json",
		"migrations/sqlite/manifest.json",
		"migrations/sqlite/0001_initial.sql",
		"migrations/postgresql/manifest.json",
		"migrations/postgresql/0001_initial.sql",
		"social/zz_golem_models.gen.go",
		"social/zz_golem_registry.gen.go",
		"social/zz_golem_graphql.gen.go",
		"social/zz_golem_events.gen.go",
		"social/zz_golem_graphql.schema.graphqls",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || info.IsDir() || info.Size() == 0 {
			t.Errorf("required generated/reviewed artifact %s is missing or empty", relative)
		}
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), ".golem/generation.lock") {
		t.Error("runtime generation lock must be excluded from the checked-in example inventory")
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".golem", "generated-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "generation.lock") {
		t.Error("runtime generation lock must not be published in the generated manifest")
	}
}

func socialExampleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate example source")
	}
	return filepath.Dir(filepath.Dir(filename))
}

func assertNoReplaceDirective(t *testing.T, filename string) {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 0 && fields[0] == "replace" {
			t.Fatalf("external-style example contains a replace directive: %s", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}
