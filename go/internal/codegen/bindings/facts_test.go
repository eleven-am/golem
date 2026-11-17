package bindings

import (
	"context"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
)

func TestDiscoverAndEmitPolicyFactsUseOrdinaryGoFactories(t *testing.T) {
	const pkg = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/facts"
	compilation := bindingCompilation(validPackage, true)
	compilation.Model.Schema.Actor.PackagePath = pkg
	for index := range compilation.Model.Models {
		compilation.Model.Models[index].Go.PackagePath = pkg
	}
	spec := modelcodegen.PackageSpec{ImportPath: pkg, PackageName: "facts", Directory: fixtureDir(t, "facts")}
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	result := DiscoverAndEmit(context.Background(), DiscoveryRequest{Dir: moduleDir(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}, ModelBootstrap: bootstrap, GenerationDigest: strings.Repeat("a", 64)})
	if len(result.Diagnostics) != 0 || len(result.Methods) != 2 || len(result.Files) != 1 {
		t.Fatalf("fact policy discovery failed: methods=%d files=%d diagnostics=%+v", len(result.Methods), len(result.Files), result.Diagnostics)
	}
	compileOverlay(t, pkg, append(modelFiles(bootstrap), result.Files...))
}
