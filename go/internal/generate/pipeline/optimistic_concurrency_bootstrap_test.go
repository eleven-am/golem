package pipeline

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
)

func TestOptimisticConcurrencyDeclarationDiscoveryAndFinalExactRegistryBothCompile(t *testing.T) {
	directory := filepath.Join(moduleRoot(t), "internal", "compiler", "compile", "testdata", "concurrency")
	request := Request{
		Compile: compile.Config{
			Dir:     moduleRoot(t),
			Pattern: "./internal/compiler/compile/testdata/concurrency",
			Root:    "DefineSchema",
		},
		AppPackage: modelcodegen.PackageSpec{
			ImportPath:  "github.com/eleven-am/golem/go/internal/compiler/compile/testdata/concurrency",
			PackageName: "concurrencyfixture",
			Directory:   directory,
		},
		Lowerers: defaultLowerers(),
	}
	result, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var registrySource []byte
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactRegistryGo {
			registrySource = artifact.Content
		}
	}
	if len(registrySource) == 0 || !bytes.Contains(registrySource, []byte("expected golem.ExistingVersion")) || bytes.Contains(registrySource, []byte("mutation ...any")) {
		t.Fatalf("prospective registry is not the exact versioned ABI:\n%s", registrySource)
	}
}
