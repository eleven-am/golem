package compile

import (
	"context"
	"strings"
	"testing"
)

func TestAggregateGrantsInsideWithinCompile(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/aggregates", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	if operations := result.Compilation.Contract.TxOperations; len(operations) != 1 || operations[0].Name != "countUsers" {
		t.Fatalf("transaction operations = %#v", operations)
	}
}

func TestAggregateGrantsOutsideWithinAreRefused(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/aggregates_outside_within", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil {
		t.Fatal("aggregate grants outside Within were accepted")
	}
	refused := map[string]int{}
	for _, diagnostic := range result.Diagnostics {
		for _, verb := range []string{"CanCount", "CanAggregate"} {
			if diagnostic.Code == "P1_METHOD_TYPECHECK" && strings.Contains(diagnostic.Message, "rules."+verb+" undefined") {
				refused[verb] = int(diagnostic.Primary.StartLine)
			}
		}
	}
	if refused["CanCount"] != 28 || refused["CanAggregate"] != 29 {
		t.Fatalf("refusals = %v, diagnostics = %#v", refused, result.Diagnostics)
	}
}
