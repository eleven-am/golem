package compile

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestKernelModeRejectsGraphQLOnlyExtensionsAtTheirDeclarations(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/kernel_extensions", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil {
		t.Fatal("kernel-mode compilation with GraphQL-only extensions must not be accepted")
	}
	var lines []int
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "P5_GRAPHQL_DISABLED_EXTENSION" {
			t.Fatalf("unexpected diagnostic %#v", diagnostic)
		}
		if diagnostic.Primary.RelativeFile != "internal/compiler/compile/testdata/kernel_extensions/schema.go" {
			t.Fatalf("diagnostic span = %#v", diagnostic.Primary)
		}
		lines = append(lines, int(diagnostic.Primary.StartLine))
	}
	sort.Ints(lines)
	if want := []int{37, 38, 39, 55}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("diagnostic lines = %v, want %v (all %#v)", lines, want, result.Diagnostics)
	}
}

func TestKernelModeKeepsMutationsAndReportsTheMode(t *testing.T) {
	kernel := Compile(context.Background(), Config{Dir: "testdata/kernel_mutation", Pattern: ".", Root: "DefineSchema"})
	if len(kernel.Diagnostics) != 0 || kernel.Compilation == nil {
		t.Fatalf("kernel diagnostics = %#v", kernel.Diagnostics)
	}
	if kernel.GraphQL {
		t.Fatal("WithoutGraphQL compilation reported GraphQL enabled")
	}
	operations := kernel.Compilation.Contract.CustomOperations
	if len(operations) != 1 || operations[0].Name != "renameUsers" || operations[0].Operation != ir.CustomOperationMutation {
		t.Fatalf("kernel custom operations = %#v", operations)
	}
	graphql := Compile(context.Background(), Config{Dir: "testdata/graphql_extensions", Pattern: ".", Root: "DefineSchema"})
	if len(graphql.Diagnostics) != 0 || !graphql.GraphQL {
		t.Fatalf("schema without the declaration must report GraphQL enabled: %#v", graphql.Diagnostics)
	}
}
