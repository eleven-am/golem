package graphql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

type customTypedCaller struct{ marker string }
type otherCustomCaller struct{}

func (*customTypedCaller) GolemGraphQLCallerCapability() {}
func (*otherCustomCaller) GolemGraphQLCallerCapability() {}

type customSearchArgs struct{ Where golem.FrozenPredicate }
type customImportArgs struct {
	Metadata golem.JSON[any]
	Data     golem.RuntimeCustomMutationInput
	Patch    golem.RuntimeCustomMutationInput
}

func TestGeneratedCustomQueryAndMutationUseCallerTypesAndExactSchema(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../internal/compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	search := customContract(t, compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, compilation, compilerir.CustomOperationMutation, "importUsers")
	user := generatedTestContract(t, compilation.Contract, "User")
	model := generatedTestModelID(t, generatedTestModel(t, compilation.Model, user.ModelID).ID)
	predicate, err := golem.RuntimeFreezePredicate(model, golem.RuntimePredicateNode{Kind: golem.FrozenConditionConstant, Truth: true, Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}})
	if err != nil {
		t.Fatal(err)
	}
	create, err := golem.RuntimeMutationInputFromValues(golem.RuntimeMutationCreateInput, model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := golem.RuntimeMutationInputFromValues(golem.RuntimeMutationUpdateManyInput, model, []golem.RuntimeMutationFieldValue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	createArgument, err := golem.RuntimeCustomMutationInputValue(golem.RuntimeMutationCreateInput, create)
	if err != nil {
		t.Fatal(err)
	}
	patchArgument, err := golem.RuntimeCustomMutationInputValue(golem.RuntimeMutationUpdateManyInput, patch)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"exact":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeModelReadRow(model)
	if err != nil {
		t.Fatal(err)
	}
	caller := &customTypedCaller{marker: "caller-only"}
	queryCalls, mutationCalls := 0, 0
	query, err := BindCustomQuery[*customTypedCaller, customSearchArgs, []golem.RuntimeModelRow](customSpec(search),
		func(arguments []CustomArgument) (customSearchArgs, error) {
			where, ok := CustomArgumentValue[golem.FrozenPredicate](arguments, "where")
			if !ok {
				return customSearchArgs{}, errCustomTest("where is not exact")
			}
			return customSearchArgs{Where: where}, nil
		},
		func(_ context.Context, got *customTypedCaller, args customSearchArgs) ([]golem.RuntimeModelRow, error) {
			queryCalls++
			if got != caller || got.marker != "caller-only" || args.Where.View().RootModelID() != model {
				return nil, errCustomTest("query caller or predicate mismatch")
			}
			return []golem.RuntimeModelRow{row}, nil
		},
		func(rows []golem.RuntimeModelRow) (any, error) {
			result := make([]any, len(rows))
			for index, value := range rows {
				result[index] = value
			}
			return result, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := BindCustomMutation[*customTypedCaller, customImportArgs, golem.RuntimeModelRow](customSpec(importUsers),
		func(arguments []CustomArgument) (customImportArgs, error) {
			metadata, metadataOK := CustomArgumentValue[golem.JSON[any]](arguments, "metadata")
			data, dataOK := CustomArgumentValue[golem.RuntimeCustomMutationInput](arguments, "data")
			patch, patchOK := CustomArgumentValue[golem.RuntimeCustomMutationInput](arguments, "patch")
			if !metadataOK || !dataOK || !patchOK {
				return customImportArgs{}, errCustomTest("mutation arguments are not exact")
			}
			return customImportArgs{Metadata: metadata, Data: data, Patch: patch}, nil
		},
		func(_ context.Context, got *customTypedCaller, args customImportArgs) (golem.RuntimeModelRow, error) {
			mutationCalls++
			if got != caller || args.Data.Frozen().ModelID() != model || args.Patch.Frozen().ModelID() != model || string(args.Metadata.Bytes()) != `{"exact":9007199254740993}` {
				return golem.RuntimeModelRow{}, errCustomTest("mutation caller or exact arguments mismatch")
			}
			return row, nil
		}, func(value golem.RuntimeModelRow) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewCustomRegistry(bundleForCompilation(t, compilation), query, mutation)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.ForCaller(caller)
	if err != nil {
		t.Fatal(err)
	}
	queryResult, err := execution.Execute(context.Background(), CustomQuery, "searchUsers", map[string]any{"where": predicate})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := queryResult.Value().([]any)
	if !ok || len(rows) != 1 || rows[0].(golem.RuntimeModelRow).ModelID() != model || queryCalls != 1 {
		t.Fatalf("custom query result=%#v calls=%d", queryResult.Value(), queryCalls)
	}
	mutationResult, err := execution.Execute(context.Background(), CustomMutation, "importUsers", map[string]any{"metadata": metadata, "data": createArgument, "patch": patchArgument})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := mutationResult.Value().(golem.RuntimeModelRow); !ok || value.ModelID() != model || mutationCalls != 1 {
		t.Fatalf("custom mutation result=%#v calls=%d", mutationResult.Value(), mutationCalls)
	}
	wrong, _ := registry.ForCaller(&otherCustomCaller{})
	if _, err := wrong.Execute(context.Background(), CustomQuery, "searchUsers", map[string]any{"where": predicate}); err == nil || queryCalls != 1 {
		t.Fatalf("wrong caller entered typed resolver: err=%v calls=%d", err, queryCalls)
	}
}

func customContract(t *testing.T, compilation compilerir.CompilationIR, kind compilerir.CustomOperationKind, name string) compilerir.CustomOperationContractIR {
	t.Helper()
	for _, operation := range compilation.Contract.CustomOperations {
		if operation.Operation == kind && operation.Name == name {
			return operation
		}
	}
	t.Fatalf("missing custom %s %s", kind, name)
	return compilerir.CustomOperationContractIR{}
}

func customSpec(operation compilerir.CustomOperationContractIR) CustomBindingSpec {
	return CustomBindingSpec{ExtensionID: string(operation.ExtensionID), ResolverPackage: operation.Resolver.PackagePath, ResolverName: operation.Resolver.Name}
}

type errCustomTest string

func (err errCustomTest) Error() string { return string(err) }
