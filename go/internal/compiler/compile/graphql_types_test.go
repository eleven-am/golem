package compile

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestCompileExplicitGraphQLObjectsThroughGeneratedCallerBootstrap(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/objecttypes", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	contract := result.Compilation.Contract
	definitions := map[string]bool{"HomeFeedPage": true, "HomeInput": true, "HomeSegment": true, "InputView": true, "PageRange": true, "PatchChild": true, "PatchInput": true, "PlaybackTarget": true, "TransportMetadata": true}
	for _, definition := range contract.GraphQLTypes {
		if !definitions[definition.Name] {
			t.Fatalf("unexpected or repeated named type %q", definition.Name)
		}
		delete(definitions, definition.Name)
	}
	if len(definitions) != 0 {
		t.Fatalf("registered types were omitted: %v", definitions)
	}
	operations := map[string]bool{"choosePlayback": true, "homeFeed": true, "homeFeedList": true, "inputState": true, "transportMetadata": true}
	var operation ir.CustomOperationContractIR
	for _, candidate := range contract.CustomOperations {
		if !operations[candidate.Name] {
			t.Fatalf("unexpected or repeated operation %q", candidate.Name)
		}
		delete(operations, candidate.Name)
		if candidate.Name == "homeFeed" {
			operation = candidate
		}
	}
	if len(operations) != 0 {
		t.Fatalf("registered operations were omitted: %v", operations)
	}
	if operation.Result.Kind != ir.GraphQLTypeObject || operation.Result.Name != "HomeFeedPage" || operation.Arguments[0].Type.Kind != ir.GraphQLTypeInputObject {
		t.Fatalf("operation=%#v", operation)
	}
	if _, err := ir.CanonicalContract(contract); err != nil {
		t.Fatal(err)
	}
}
