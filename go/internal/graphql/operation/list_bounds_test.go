package operation

import (
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
)

func modelList(maxima ...uint32) compilerir.GraphQLTypeIR {
	result := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}
	for index := len(maxima) - 1; index >= 0; index-- {
		inner := result
		result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, MaxItems: maxima[index], Element: &inner}
	}
	return result
}

func TestListBoundsDeclareEveryCompositeListWithoutATakeDefault(t *testing.T) {
	scalarList := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, MaxItems: 4, Element: &compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}}
	compiler := &Compiler{compilation: compilerir.CompilationIR{Contract: compilerir.ContractIR{
		CustomOperations: []compilerir.CustomOperationContractIR{
			{Operation: compilerir.CustomOperationQuery, Name: "me", Result: modelList(3)},
			{Operation: compilerir.CustomOperationMutation, Name: "submitAll", Result: modelList(2, 5)},
			{Operation: compilerir.CustomOperationQuery, Name: "labels", Result: scalarList},
			{Operation: compilerir.CustomOperationQuery, Name: "one", Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}},
		},
		Models: []compilerir.ModelContractIR{
			{GraphQLName: "Author", Exposed: true, Computed: []compilerir.ComputedFieldContractIR{{Name: "related", Result: modelList(7)}, {Name: "tags", Result: scalarList}}},
			{GraphQLName: "Hidden", Exposed: false, Computed: []compilerir.ComputedFieldContractIR{{Name: "related", Result: modelList(9)}}},
		},
	}}}
	want := map[string]int{"Query.me": 3, "Mutation.submitAll": 10, "Author.related": 7}
	got := compiler.ListBounds()
	if len(got) != len(want) {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	for key, bound := range want {
		if got[key] != bound {
			t.Fatalf("bounds = %v, want %v", got, want)
		}
	}
}

func TestListBoundsCoverGroupRootsAtTheirGroupMaximum(t *testing.T) {
	compilation := p6AnalyticsSocial(t, social(t))
	contract := contractNamed(t, compilation.Contract, "Post")
	for _, testCase := range []struct {
		name      string
		maxGroups int
		want      int
	}{
		{"runtime maximum below the model maximum", 40, 40},
		{"model maximum below the runtime maximum", 500, 100},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			compiler, err := New(compilation, Limits{MaxGroups: testCase.maxGroups})
			if err != nil {
				t.Fatal(err)
			}
			bounds := compiler.ListBounds()
			if bounds["Query."+contract.Roots.GroupBy] != testCase.want {
				t.Fatalf("groupBy bound = %v, want %d", bounds, testCase.want)
			}
			if _, present := bounds["Query."+contract.Roots.Aggregate]; present {
				t.Fatalf("aggregate root has a list bound: %v", bounds)
			}
		})
	}
}

func TestListBoundsLeaveSearchOperationsToTheirSDLDefault(t *testing.T) {
	compilation := semanticSocial(t)
	compiler, err := New(compilation, Limits{Bind: graphqlbind.Limits{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(compilation.Contract.CustomOperations) == 0 {
		t.Fatal("fixture has no generated search operations")
	}
	for _, operation := range compilation.Contract.CustomOperations {
		if _, present := compiler.ListBounds()["Query."+operation.Name]; present {
			t.Fatalf("search operation %s has a list bound beside its take default", operation.Name)
		}
	}
}
