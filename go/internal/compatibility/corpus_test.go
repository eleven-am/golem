package compatibility

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestP8PublicGoAPIDiffGate(t *testing.T) {
	root := compatibilityModuleRoot(t)
	frozenBytes := readFrozenCorpus(t, "public-go-api.json", PublicGoAPICorpusSHA256)
	frozen, err := ParseAPIInventory(frozenBytes)
	if err != nil {
		t.Fatal(err)
	}
	current, err := BuildAPIInventory(context.Background(), APIRequest{
		Directory: root,
		Patterns: []string{
			"./events", "./golem", "./graphql", "./observe", "./provider",
			"./provider/postgresql", "./provider/sqlite", "./runtime",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentBytes, err := EncodeAPIInventory(current)
	if err != nil {
		t.Fatal(err)
	}
	if change := CompareAPI(frozen, current); change != LayerUnchanged || string(currentBytes) != string(frozenBytes) {
		t.Fatalf("public Go API corpus changed: classification=%s", change)
	}
}

func TestP8GeneratedAndGraphQLCompatibilityGate(t *testing.T) {
	root := compatibilityModuleRoot(t)
	example := filepath.Join(root, "examples", "social")
	workspace := compatibilityTestWorkspace(t, root, example)
	frozenGeneratedBytes := readFrozenCorpus(t, "generated-go-abi.json", GeneratedGoABICorpusSHA256)
	frozenGenerated, err := ParseAPIInventory(frozenGeneratedBytes)
	if err != nil {
		t.Fatal(err)
	}
	currentGenerated, err := BuildAPIInventory(context.Background(), APIRequest{
		Directory: example,
		Patterns:  []string{"./social", "./social/golemgqlgen"},
		Env:       append(os.Environ(), "GOWORK="+workspace),
		IncludeFile: func(path string) bool {
			return strings.HasPrefix(filepath.Base(path), "zz_golem_")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentGeneratedBytes, err := EncodeAPIInventory(currentGenerated)
	if err != nil {
		t.Fatal(err)
	}
	if change := CompareAPI(frozenGenerated, currentGenerated); change != LayerUnchanged || string(currentGeneratedBytes) != string(frozenGeneratedBytes) {
		t.Fatalf("generated Go ABI corpus changed: classification=%s", change)
	}

	frozenGraphQLBytes := readFrozenCorpus(t, "graphql-abi.json", GraphQLABICorpusSHA256)
	frozenGraphQL, err := BuildGraphQLInventory(mustRead(t, filepath.Join(example, "social", "zz_golem_graphql.schema.graphqls")))
	if err != nil {
		t.Fatal(err)
	}
	currentGraphQLBytes, err := EncodeGraphQLInventory(frozenGraphQL)
	if err != nil {
		t.Fatal(err)
	}
	parsedFrozenGraphQL := parseFrozenGraphQLInventory(t, frozenGraphQLBytes)
	if change := CompareGraphQL(parsedFrozenGraphQL, frozenGraphQL); change != LayerUnchanged || string(currentGraphQLBytes) != string(frozenGraphQLBytes) {
		t.Fatalf("generated GraphQL ABI corpus changed: classification=%s", change)
	}
}

func readFrozenCorpus(t *testing.T, name, expectedDigest string) []byte {
	t.Helper()
	encoded := mustRead(t, filepath.Join(compatibilityModuleRoot(t), "internal", "compatibility", "testdata", name))
	if actual := Digest(encoded); actual != expectedDigest {
		t.Fatalf("frozen corpus %s digest mismatch", name)
	}
	return encoded
}

func parseFrozenGraphQLInventory(t *testing.T, encoded []byte) GraphQLInventory {
	t.Helper()
	value, err := ParseGraphQLInventory(encoded)
	if err != nil {
		t.Fatalf("frozen GraphQL inventory is invalid: %v", err)
	}
	return value
}

func compatibilityModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate compatibility package")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filename)))
}

func compatibilityTestWorkspace(t *testing.T, root, example string) string {
	t.Helper()
	directory := t.TempDir()
	workspace := filepath.Join(directory, "go.work")
	commands := []struct {
		env  string
		args []string
	}{
		{env: "GOWORK=off", args: []string{"work", "init", root, example}},
		{env: "GOWORK=" + workspace, args: []string{"work", "edit", "-replace=github.com/eleven-am/golem/go@v0.0.0=" + root}},
	}
	for _, value := range commands {
		command := exec.Command("go", value.args...)
		command.Dir = directory
		command.Env = append(os.Environ(), value.env)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create compatibility workspace: %v (%s)", err, strings.TrimSpace(string(output)))
		}
	}
	return workspace
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
