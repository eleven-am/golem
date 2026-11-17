package methods

import (
	"context"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestInvalidationDeclarationsRejectMutationMismatchedBindersAndUncanonicalArguments(t *testing.T) {
	fixture := "invalidationsinvalid"
	pkg := "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/" + fixture
	compilation := extensionCompilation(pkg)
	compilation.Model.Schema.Actor = ir.GoNamedTypeIR{PackagePath: pkg, Name: "Actor"}
	result := Interpret(context.Background(), Config{Dir: moduleDirectory(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{{ImportPath: pkg, PackageName: fixture, Directory: fixtureDirectory(t, fixture)}}})
	for _, code := range []string{"P07_INVALIDATION_DECLARATION", "P07_INVALIDATION_ARGUMENT"} {
		if !containsDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s: %#v", code, result.Diagnostics)
		}
	}
	if len(result.GraphQLCustom) != 0 {
		t.Fatalf("invalid declarations survived: %#v", result.GraphQLCustom)
	}
}
