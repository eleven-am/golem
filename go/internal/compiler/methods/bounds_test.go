package methods

import (
	"context"
	"reflect"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
)

const boundsPackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/extensionbounds"

func interpretBounds(t *testing.T) Result {
	t.Helper()
	return Interpret(context.Background(), Config{
		Dir: moduleDirectory(t), Compilation: extensionCompilation(boundsPackage),
		Packages: []modelcodegen.PackageSpec{{ImportPath: boundsPackage, PackageName: "extensionbounds", Directory: fixtureDirectory(t, "extensionbounds")}},
	})
}

func TestInterpretListMaximaRejectsEveryUndeclaredOrMalformedBound(t *testing.T) {
	result := interpretBounds(t)
	cases := []struct{ code, mention string }{
		{"P5_GRAPHQL_LIST_MAX_REQUIRED", "custom query missing result list level 1"},
		{"P5_GRAPHQL_LIST_MAX_REQUIRED", "custom mutation mutatingMissing result list level 1"},
		{"P5_GRAPHQL_LIST_MAX_ARITY", "custom query wrongArity declares 1 maximum item counts for a result with 2 list levels"},
		{"P5_GRAPHQL_LIST_MAX_ZERO", "maximum item count must be at least 1"},
		{"P5_GRAPHQL_LIST_MAX_SCALAR", "custom query scalarMax result is not a list"},
		{"P5_GRAPHQL_LIST_MAX_SCALAR", "custom query bytesMax result is not a list"},
		{"P5_GRAPHQL_LIST_MAX_CONSTANT", "maximum item count must be a uint32 constant"},
		{"P5_CUSTOM_OPTION_DUPLICATE", "more than once"},
		{"P5_CUSTOM_OPTION", "must be direct golem.MaxItems calls"},
	}
	for _, expected := range cases {
		found := false
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Code == expected.code && strings.Contains(diagnostic.Message, expected.mention) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s mentioning %q in %#v", expected.code, expected.mention, result.Diagnostics)
		}
	}
	zeroLines := map[uint32]bool{}
	constantLines := map[uint32]bool{}
	for _, diagnostic := range result.Diagnostics {
		switch diagnostic.Code {
		case "P5_GRAPHQL_LIST_MAX_ZERO":
			zeroLines[diagnostic.Primary.StartLine] = true
		case "P5_GRAPHQL_LIST_MAX_CONSTANT":
			constantLines[diagnostic.Primary.StartLine] = true
		}
	}
	if len(zeroLines) != 3 {
		t.Errorf("zero maxima reported on %d distinct lines, want 3 (computed GraphQLList, MaxItems(0), MaxItems(4, 0)): %v", len(zeroLines), zeroLines)
	}
	if len(constantLines) != 2 {
		t.Errorf("non-constant maxima reported on %d distinct lines, want 2 (GraphQLList, MaxItems): %v", len(constantLines), constantLines)
	}
}

func TestInterpretListMaximaAcceptsDeclaredBounds(t *testing.T) {
	result := interpretBounds(t)
	maxima := map[string][]uint32{}
	for _, declaration := range result.GraphQLCustom {
		maxima[declaration.Operation.Name] = declaration.Operation.Result.ListMaxima()
	}
	want := map[string][]uint32{"okSingle": {1}, "okNested": {10, 5}, "mutatingOk": {2}}
	if !reflect.DeepEqual(maxima, want) {
		t.Fatalf("accepted custom operations = %v, want exactly %v", maxima, want)
	}
	computed := map[string][]uint32{}
	for _, declaration := range result.GraphQLComputed {
		computed[declaration.Field.Name] = declaration.Field.Result.ListMaxima()
	}
	wantComputed := map[string][]uint32{"okList": {10}, "okNestedList": {7, 3}}
	if !reflect.DeepEqual(computed, wantComputed) {
		t.Fatalf("accepted computed fields = %v, want exactly %v", computed, wantComputed)
	}
	for _, declaration := range result.GraphQLComputed {
		if declaration.Field.Name == "okList" && declaration.Field.Result.Nullable {
			t.Fatal("NonNull must keep the declared maximum and clear nullability")
		}
	}
}
