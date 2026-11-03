package custom

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
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

func TestCustomRegistryRejectsVersionedUpdateManyInputNestedInList(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	if len(compilation.Contract.CustomOperations) == 0 {
		t.Fatal("fixture has no custom operations")
	}
	var modelName string
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		if !contract.Exposed {
			continue
		}
		version := compilerir.FieldID("f0000000000000000000000000000091")
		contract.OptimisticConcurrency = &version
		modelName = contract.GraphQLName
		break
	}
	if modelName == "" {
		t.Fatal("fixture has no exposed model")
	}
	element := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeUpdateManyInput, Name: modelName}
	compilation.Contract.CustomOperations[0].Arguments = []compilerir.GraphQLArgumentContractIR{{Name: "data", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &element}}}
	if _, err := New(compilation); err == nil {
		t.Fatal("versioned update-many custom argument was accepted")
	}
}

func TestFinishRefusesAListAboveItsDeclaredMaximumBeforeReadingItsItems(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	var search compilerir.CustomOperationContractIR
	for _, operation := range compilation.Contract.CustomOperations {
		if operation.Name == "searchUsers" {
			search = cloneOperation(operation)
		}
	}
	if search.Result.MaxItems != 10 {
		t.Fatalf("fixture searchUsers maximum = %d, want the declared 10", search.Result.MaxItems)
	}
	bulk := cloneOperation(search)
	bulk.ExtensionID, bulk.Operation, bulk.Name = "bulk-users", compilerir.CustomOperationMutation, "bulkUsers"
	bulk.Resolver.Kind = "custom" + string(compilerir.CustomOperationMutation)
	bulk.Result.MaxItems = 3
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{search, bulk}
	registry, err := New(compilation)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		operation               compilerir.CustomOperationContractIR
		items                   int
		operationLabel, message string
	}{
		{search, 11, "customQuery", "custom query searchUsers returned more than 10 items"},
		{bulk, 4, "customMutation", "custom mutation bulkUsers returned more than 3 items"},
	} {
		_, err := registry.Finish(Request{operation: test.operation}, make([]any, test.items))
		var public *golem.Error
		if !errors.As(err, &public) {
			t.Fatalf("%s: error = %T %v", test.operation.Name, err, err)
		}
		if public.Code != golem.CodeLimitExceeded || public.Limit != golem.LimitListItems || public.LimitMax != int(test.operation.Result.MaxItems) || public.Operation != test.operationLabel || public.Message != test.message {
			t.Fatalf("%s: public error = %#v", test.operation.Name, public)
		}
	}

	_, err = registry.Finish(Request{operation: search}, make([]any, 10))
	var public *golem.Error
	if err == nil || errors.As(err, &public) {
		t.Fatalf("a list of exactly the maximum must pass the bound and then fail item validation, got %v", err)
	}
	if result, err := registry.Finish(Request{operation: search}, []any{}); err != nil || len(result.Value().([]any)) != 0 {
		t.Fatalf("an empty list must be accepted: %v", err)
	}
}
