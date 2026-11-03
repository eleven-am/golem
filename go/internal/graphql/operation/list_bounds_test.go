package operation

import (
	"fmt"
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
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
	compilation := social(t)
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{
		{Operation: compilerir.CustomOperationQuery, Name: "me", Result: modelList(3)},
		{Operation: compilerir.CustomOperationMutation, Name: "submitAll", Result: modelList(2, 5)},
		{Operation: compilerir.CustomOperationQuery, Name: "labels", Result: scalarList},
		{Operation: compilerir.CustomOperationQuery, Name: "one", Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}},
	}
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "Post" {
			compilation.Contract.Models[index].Computed = []compilerir.ComputedFieldContractIR{{Name: "related", Result: modelList(7)}, {Name: "tags", Result: scalarList}}
		}
	}
	compiler := &Compiler{compilation: compilation}
	want := map[string]int{"Query.me": 3, "Mutation.submitAll": 10, "Post.related": 7}
	got, err := compiler.ListBounds()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	for key, bound := range want {
		if got[key] != bound {
			t.Fatalf("bounds = %v, want %v", got, want)
		}
	}
}

func TestListBoundsExcludeComputedFieldsOfPrunedTypes(t *testing.T) {
	compilation := p6AnalyticsSocial(t, social(t))
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		contract.Subscriptions = false
		contract.Operations = nil
		if contract.GraphQLName == "Post" {
			contract.Operations = []compilerir.Operation{compilerir.OperationAggregate}
			contract.Computed = []compilerir.ComputedFieldContractIR{{Name: "related", Result: modelList(7)}}
		}
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	if schema.Types["Post"] != nil {
		t.Fatal("fixture no longer prunes the Post object type")
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := compiler.ListBounds()
	if err != nil {
		t.Fatal(err)
	}
	for key := range bounds {
		typeName, fieldName, _ := strings.Cut(key, ".")
		if definition := schema.Types[typeName]; definition == nil || definition.Fields.ForName(fieldName) == nil {
			t.Fatalf("bound %s names a field absent from the generated SDL", key)
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
			bounds, err := compiler.ListBounds()
			if err != nil {
				t.Fatal(err)
			}
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
	bounds, err := compiler.ListBounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range compilation.Contract.CustomOperations {
		if _, present := bounds["Query."+operation.Name]; present {
			t.Fatalf("search operation %s has a list bound beside its take default", operation.Name)
		}
	}
}

func TestAdvertisedTakeDefaultIsTheTakeBindApplies(t *testing.T) {
	compilation := social(t)
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		compilation.Contract.Models[index].Limits.DefaultPageSize = uint32(10 * (index + 1))
		compilation.Contract.Models[index].Limits.MaxPageSize = 1000
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	pk := func(contract compilerir.ModelContractIR) string {
		model := modelByIDForTest(t, compilation.Model, contract.ModelID)
		return fieldContractByIDForTest(t, contract, model.PrimaryKey.Fields[0]).GraphQLName
	}
	byName := map[string]compilerir.ModelContractIR{}
	for _, contract := range compilation.Contract.Models {
		byName[contract.GraphQLName] = contract
	}
	advertised := func(field *ast.FieldDefinition) (int64, bool) {
		take := field.Arguments.ForName("take")
		if take == nil || take.DefaultValue == nil || field.Type.Elem == nil {
			return 0, false
		}
		value, err := take.DefaultValue.Value(nil)
		if err != nil {
			t.Fatal(err)
		}
		return value.(int64), true
	}
	checked := 0
	for _, root := range schema.Types["Query"].Fields {
		want, ok := advertised(root)
		target, listsModel := byName[root.Type.Name()]
		if !ok || !listsModel {
			continue
		}
		query, errs := gqlparser.LoadQuery(schema, fmt.Sprintf("query { %s { %s } }", root.Name, pk(target)))
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		result, err := compiler.Compile(query, query.Operations[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		if take, present := result.Reads[0].Request.Take(); !present || int64(take) != want {
			t.Fatalf("%s advertises take %d but bind applies %d/%v", root.Name, want, take, present)
		}
		checked++
		for _, relation := range schema.Types[root.Type.Name()].Fields {
			want, ok := advertised(relation)
			child, listsModel := byName[relation.Type.Name()]
			if !ok || !listsModel {
				continue
			}
			query, errs := gqlparser.LoadQuery(schema, fmt.Sprintf("query { %s { %s %s { %s } } }", root.Name, pk(target), relation.Name, pk(child)))
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			result, err := compiler.Compile(query, query.Operations[0], nil)
			if err != nil {
				t.Fatal(err)
			}
			applied := int64(-1)
			for _, selection := range result.Reads[0].Frozen.Selection() {
				if request, nested := selection.Request(); nested {
					if take, present := request.Take(); present {
						applied = int64(take)
					}
				}
			}
			if applied != want {
				t.Fatalf("%s.%s advertises take %d but bind applies %d", root.Type.Name(), relation.Name, want, applied)
			}
			checked++
		}
	}
	if checked < 4 {
		t.Fatalf("only %d advertised defaults were compared", checked)
	}
}
