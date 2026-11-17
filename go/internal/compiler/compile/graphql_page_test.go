package compile

import (
	"context"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"testing"
)

func TestCompilePageQueryThroughGeneratedCallerBootstrap(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/pagequeries", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	contract := result.Compilation.Contract
	if err := ir.ValidateGraphQLTypeDefinitions(contract); err != nil {
		t.Fatal(err)
	}
	if contract.RequiredClientManifestVersion() != 3 || len(contract.CustomOperations) != 1 {
		t.Fatalf("contract=%#v", contract)
	}
	page := contract.CustomOperations[0]
	if page.Pagination == nil || page.Pagination.Item.Name != "HomeSegment" || page.Result.Name != "HomeFeedPage" {
		t.Fatalf("page=%#v", page)
	}
	if _, err := ir.CanonicalContract(contract); err != nil {
		t.Fatal(err)
	}
}
