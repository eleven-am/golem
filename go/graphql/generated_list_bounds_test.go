package graphql

import (
	"context"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestServerStartsWhenAnAggregateOnlyModelHasAComputedModelList(t *testing.T) {
	compilation, _ := generatedTestCompilation(t)
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	const extension = compilerir.ExtensionID("00000000000000000000000000000abc")
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		contract.Subscriptions = false
		contract.Operations = nil
		if contract.GraphQLName != "Post" {
			continue
		}
		var title compilerir.FieldID
		for _, field := range contract.Fields {
			if field.GraphQLName == "title" {
				title = field.FieldID
			}
		}
		if title == "" {
			t.Fatal("Post title field is absent")
		}
		contract.Operations = []compilerir.Operation{compilerir.OperationAggregate}
		contract.Aggregation = &compilerir.AggregationContractIR{
			Enabled: true, Dimensions: []compilerir.FieldID{title}, DimensionsExplicit: true,
			Measures: []compilerir.FieldID{title}, MeasuresExplicit: true,
			GraphQLMaxGroups: 100, RelationMaxIntermediateGroups: 10_000,
		}
		contract.Computed = []compilerir.ComputedFieldContractIR{{
			ExtensionID: extension, Name: "related",
			Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, MaxItems: 7, Element: &compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}},
		}}
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BindComputed(string(extension), func(context.Context, ComputedRequest) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:              generatedTestBundle(t, compilation),
		ComputedBindings:    []ComputedBinding{binding},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return nil, nil },
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 0, false },
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
