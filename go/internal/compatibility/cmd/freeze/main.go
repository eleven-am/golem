// Command freeze writes reviewed compatibility inventories. It is deliberately
// explicit and is never invoked by ordinary tests or generation.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eleven-am/golem/go/internal/compatibility"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail("working-directory")
	}
	if filepath.Base(root) != "go" {
		fail("module-root")
	}
	testdata := filepath.Join(root, "internal", "compatibility", "testdata")
	if err := os.MkdirAll(testdata, 0o755); err != nil {
		fail("testdata-directory")
	}
	ctx := context.Background()
	public, err := compatibility.BuildAPIInventory(ctx, compatibility.APIRequest{
		Directory: root,
		Patterns: []string{
			"./embedding", "./events", "./golem", "./golemtest", "./graphql", "./observe",
			"./provider", "./provider/postgresql", "./provider/sqlite", "./runtime",
		},
	})
	if err != nil {
		fail("public-api-build")
	}
	publicBytes, err := compatibility.EncodeAPIInventory(public)
	if err != nil {
		fail("public-api-encode")
	}
	example := filepath.Join(root, "examples", "social")
	workspace, cleanup, err := compatibilityWorkspace(root, example)
	if err != nil {
		fail("workspace")
	}
	defer cleanup()
	generated, err := compatibility.BuildAPIInventory(ctx, compatibility.APIRequest{
		Directory: example,
		Patterns:  []string{"./social", "./social/golemgqlgen"},
		Env:       append(os.Environ(), "GOWORK="+workspace),
		IncludeFile: func(path string) bool {
			return strings.HasPrefix(filepath.Base(path), "zz_golem_")
		},
	})
	if err != nil {
		fail("generated-api-build")
	}
	generatedBytes, err := compatibility.EncodeAPIInventory(generated)
	if err != nil {
		fail("generated-api-encode")
	}
	sdl, err := os.ReadFile(filepath.Join(example, "social", "zz_golem_graphql.schema.graphqls"))
	if err != nil {
		fail("graphql-read")
	}
	graph, err := compatibility.BuildGraphQLInventory(sdl)
	if err != nil {
		fail("graphql-build")
	}
	graphBytes, err := compatibility.EncodeGraphQLInventory(graph)
	if err != nil {
		fail("graphql-encode")
	}
	for path, content := range map[string][]byte{
		"public-go-api.json":    publicBytes,
		"generated-go-abi.json": generatedBytes,
		"graphql-abi.json":      graphBytes,
	} {
		if err := os.WriteFile(filepath.Join(testdata, path), content, 0o644); err != nil {
			fail("write")
		}
		fmt.Printf("%s %s\n", path, compatibility.Digest(content))
	}
	manifestBytes, err := compatibility.Encode(compatibility.DevelopmentManifest())
	if err != nil {
		fail("manifest-encode")
	}
	manifestDirectory := filepath.Join(root, "compatibility")
	if err := os.MkdirAll(manifestDirectory, 0o755); err != nil {
		fail("manifest-directory")
	}
	if err := os.WriteFile(filepath.Join(manifestDirectory, "manifest.json"), manifestBytes, 0o644); err != nil {
		fail("manifest-write")
	}
	fmt.Printf("compatibility/manifest.json %s\n", compatibility.Digest(manifestBytes))
}

func compatibilityWorkspace(root, example string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "golem-compatibility-work-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	workspace := filepath.Join(directory, "go.work")
	commands := [][]string{
		{"work", "init", root, example},
		{"work", "edit", "-replace=github.com/eleven-am/golem/go@v0.0.0=" + root},
	}
	for index, arguments := range commands {
		command := exec.Command("go", arguments...)
		command.Dir = directory
		work := "GOWORK=" + workspace
		if index == 0 {
			work = "GOWORK=off"
		}
		command.Env = append(os.Environ(), work)
		if err := command.Run(); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return workspace, cleanup, nil
}

func fail(stage string) {
	_, _ = fmt.Fprintf(os.Stderr, "compatibility corpus generation failed: %s\n", stage)
	os.Exit(1)
}
