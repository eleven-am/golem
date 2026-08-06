package custom

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestCustomOperationCannotRequestSystemDBTxRawSQLOrUnknownType(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	base := *compiled.Compilation
	if len(base.Contract.CustomOperations) == 0 {
		t.Fatal("fixture has no custom operations")
	}
	tests := []struct {
		name   string
		mutate func(*compilerir.CustomOperationContractIR)
	}{
		{"system", func(value *compilerir.CustomOperationContractIR) {
			value.Capability = compilerir.CustomOperationCapability("system")
		}},
		{"db", func(value *compilerir.CustomOperationContractIR) {
			value.Capability = compilerir.CustomOperationCapability("db")
		}},
		{"tx", func(value *compilerir.CustomOperationContractIR) {
			value.Capability = compilerir.CustomOperationCapability("tx")
		}},
		{"raw sql", func(value *compilerir.CustomOperationContractIR) {
			value.Result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "RawSQL"}
		}},
		{"unknown type", func(value *compilerir.CustomOperationContractIR) {
			value.Arguments[0].Type = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Unknown"}
		}},
		{"forged resolver kind", func(value *compilerir.CustomOperationContractIR) { value.Resolver.Kind = "customSystem" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compilation := base
			compilation.Contract.CustomOperations = make([]compilerir.CustomOperationContractIR, len(base.Contract.CustomOperations))
			for index, operation := range base.Contract.CustomOperations {
				compilation.Contract.CustomOperations[index] = cloneOperation(operation)
			}
			test.mutate(&compilation.Contract.CustomOperations[0])
			if _, err := New(compilation); err == nil {
				t.Fatal("unsafe custom contract was accepted")
			}
		})
	}
}
