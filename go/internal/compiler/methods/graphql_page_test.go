package methods

import (
	"context"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"testing"
)

func pageInterpret(t *testing.T, fixture string) Result {
	t.Helper()
	pkg := "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/" + fixture
	compilation := extensionCompilation(pkg)
	compilation.Contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: 1}
	return Interpret(context.Background(), Config{Dir: moduleDirectory(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{{ImportPath: pkg, PackageName: fixture, Directory: fixtureDirectory(t, fixture)}}})
}

func TestInterpretPageQueryOwnsSyntheticResultAndReservedArguments(t *testing.T) {
	result := pageInterpret(t, "pagequeries")
	if len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	if len(result.GraphQLCustom) != 1 || len(result.GraphQLTypes) != 3 {
		t.Fatalf("result=%#v", result)
	}
	operation := result.GraphQLCustom[0].Operation
	if operation.Pagination == nil || operation.Pagination.DefaultTake != 25 || operation.Pagination.MaxTake != 100 || operation.Pagination.MaxCandidates != 200 || operation.Pagination.Revision != "home-v1" || operation.Pagination.Item.Name != "HomeSegment" {
		t.Fatalf("pagination=%#v", operation.Pagination)
	}
	if operation.Result.Name != "HomeFeedPage" || operation.Result.Nullable || len(operation.Arguments) != 3 {
		t.Fatalf("operation=%#v", operation)
	}
	var found bool
	for _, definition := range result.GraphQLTypes {
		if definition.Name == "HomeFeedPage" {
			found = true
			if !definition.Page || definition.Go != (ir.GoNamedTypeIR{}) || definition.Fields[0].Type.MaxItems != 100 {
				t.Fatalf("synthetic=%#v", definition)
			}
		}
	}
	if !found {
		t.Fatal("page definition absent")
	}
}

func TestInterpretPageQueryRejectsAmbiguousOrUnboundedDeclarations(t *testing.T) {
	result := pageInterpret(t, "pagequeriesinvalid")
	for _, code := range []string{"P5_PAGE_OPTION_REQUIRED", "P5_PAGE_LIMITS", "P5_PAGE_REVISION", "P5_PAGE_ARGUMENT", "P5_PAGE_SIGNATURE", "P5_PAGE_ITEM"} {
		if !containsDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s: %#v", code, result.Diagnostics)
		}
	}
	if len(result.GraphQLCustom) != 0 {
		t.Fatalf("invalid pages admitted: %#v", result.GraphQLCustom)
	}
}
