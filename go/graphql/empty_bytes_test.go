package graphql

import (
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func assertGraphQLEmptyBytes(t *testing.T, path string, value any) {
	t.Helper()
	data, ok := value.([]byte)
	if !ok || data == nil || len(data) != 0 {
		t.Fatalf("%s bytes=%#v; want a non-nil empty slice", path, value)
	}
}

func TestEmptyBytesStayEmptyThroughCustomComputedAndGeneratedResults(t *testing.T) {
	custom := cloneCustomArguments([]CustomArgument{{name: "payload", value: []byte{}}, {name: "list", value: []any{[]byte{}}}})
	assertGraphQLEmptyBytes(t, "custom argument", custom[0].value)
	assertGraphQLEmptyBytes(t, "custom list argument", custom[1].value.([]any)[0])
	computed := cloneComputedArguments([]ComputedArgument{{Name: "payload", Value: []byte{}}, {Name: "list", Value: []any{[]byte{}}}})
	assertGraphQLEmptyBytes(t, "computed argument", computed[0].Value)
	assertGraphQLEmptyBytes(t, "computed list argument", computed[1].Value.([]any)[0])
	result, err := normalizeGeneratedCustomResult[struct{}](compilerir.CompilationIR{}, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Bytes"}, []byte{}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertGraphQLEmptyBytes(t, "generated custom result", result)
}
