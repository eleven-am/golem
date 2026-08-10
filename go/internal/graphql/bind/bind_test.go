package bind

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestGraphQLSelectorOrderCursorDistinctAndPagingLowerToExactP3Request(t *testing.T) {
	compilation := socialCompilation(t)
	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	post, contract := modelNamed(t, compilation, "Post")
	selector := contract.Selectors[0]
	selectorName := selector.Name
	selectorField := fieldContract(t, contract, selector.Fields[0]).GraphQLName
	id := "00000000-0000-0000-0000-000000000001"
	selectorValue := any(id)
	if len(selector.Fields) > 1 {
		compound := map[string]any{}
		for _, field := range selector.Fields {
			compound[fieldContract(t, contract, field).GraphQLName] = id
		}
		selectorValue = compound
	}
	postID := scalarSelection(t, selector.Fields[0])
	request, err := binder.Query(QueryInput{
		Operation: readir.FindMany,
		Model:     post.ID,
		Arguments: map[string]any{
			"where":    map[string]any{"title": map[string]any{"contains": "go"}},
			"orderBy":  []any{map[string]any{"createdAt": "desc"}, map[string]any{selectorField: "asc"}},
			"cursor":   map[string]any{selectorName: selectorValue},
			"distinct": []any{selectorField},
			"skip":     2,
			"take":     -5,
		},
		Selections: []readir.Selection{postID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation() != readir.FindMany || request.ProjectionMode() != readir.ProjectionSelect {
		t.Fatalf("request operation/projection = %v/%v", request.Operation(), request.ProjectionMode())
	}
	if value, ok := request.Take(); !ok || value != -5 {
		t.Fatalf("take = %d/%v", value, ok)
	}
	if value, ok := request.Skip(); !ok || value != 2 {
		t.Fatalf("skip = %d/%v", value, ok)
	}
	if len(request.OrderBy()) != 2 || request.OrderBy()[0].Direction() != readir.Descending || len(request.Distinct()) != 1 {
		t.Fatalf("order/distinct = %#v/%#v", request.OrderBy(), request.Distinct())
	}
	if _, ok := request.Cursor(); !ok {
		t.Fatal("cursor was not lowered")
	}
	if where, ok := request.Where(); !ok || where.Kind() != policyir.ConditionScalar {
		t.Fatalf("where = %#v/%v", where, ok)
	}

	unique, err := binder.Query(QueryInput{Operation: readir.FindUnique, Model: post.ID, Arguments: map[string]any{"where": map[string]any{selectorName: selectorValue}}, Selections: []readir.Selection{postID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := unique.Selector(); !ok {
		t.Fatal("find-one selector metadata was not lowered")
	}
}

func TestGraphQLDefaultPageAndExplicitReversePageRespectStricterLimitsWithoutHiddenTruncation(t *testing.T) {
	compilation := socialCompilation(t)
	post, contract := modelNamed(t, compilation, "Post")
	selection := scalarSelection(t, contract.Selectors[0].Fields[0])

	// The SDL advertises ContractIR's default. A stricter runtime maximum must
	// refuse that visible default, never replace it with an undisclosed value.
	stricter, err := New(compilation, Limits{MaxPageSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stricter.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}}); err == nil {
		t.Fatal("SDL default above the runtime maximum was silently truncated")
	}

	binder, err := New(compilation, Limits{MaxPageSize: int(contract.Limits.DefaultPageSize)})
	if err != nil {
		t.Fatal(err)
	}
	request, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}})
	if err != nil {
		t.Fatal(err)
	}
	if take, ok := request.Take(); !ok || take != int(contract.Limits.DefaultPageSize) {
		t.Fatalf("lowered SDL default take=%d/%v want=%d", take, ok, contract.Limits.DefaultPageSize)
	}
	if _, err := stricter.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}, Arguments: map[string]any{"take": 4}}); err == nil {
		t.Fatal("explicit take above the runtime limit was accepted")
	}
	if _, err := stricter.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}, Arguments: map[string]any{"take": -4}}); err == nil {
		t.Fatal("explicit reverse take above the runtime limit was accepted")
	}
	reverse, err := stricter.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}, Arguments: map[string]any{"take": -3}})
	if err != nil {
		t.Fatal(err)
	}
	if take, ok := reverse.Take(); !ok || take != -3 {
		t.Fatalf("reverse take=%d/%v", take, ok)
	}
}

func TestGraphQLRejectsZeroMultipleForgedAndHiddenSelectors(t *testing.T) {
	compilation := socialCompilation(t)
	post, contract := modelNamed(t, compilation, "Post")
	selector := contract.Selectors[0]
	field := fieldContract(t, contract, selector.Fields[0])
	modelField := modelField(t, post, selector.Fields[0])
	value := selectorTestValue(modelField.Scalar.Type)
	selectionField := field
	for _, candidate := range contract.Fields {
		if candidate.FieldID != field.FieldID {
			selectionField = candidate
			break
		}
	}
	selection := scalarSelection(t, selectionField.FieldID)
	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bad := []map[string]any{
		{},
		{selector.Name: value, "forged": value},
		{"forged": value},
	}
	for index, where := range bad {
		if _, err := binder.Query(QueryInput{Operation: readir.FindUnique, Model: post.ID, Arguments: map[string]any{"where": where}, Selections: []readir.Selection{selection}}); err == nil {
			t.Fatalf("invalid selector %d was accepted", index)
		}
	}

	hidden := compilation
	hidden.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for contractIndex := range hidden.Contract.Models {
		if hidden.Contract.Models[contractIndex].ModelID != post.ID {
			continue
		}
		hidden.Contract.Models[contractIndex].Fields = append([]compilerir.FieldContractIR(nil), hidden.Contract.Models[contractIndex].Fields...)
		for fieldIndex := range hidden.Contract.Models[contractIndex].Fields {
			if hidden.Contract.Models[contractIndex].Fields[fieldIndex].FieldID == field.FieldID {
				hidden.Contract.Models[contractIndex].Fields[fieldIndex].Modes = []compilerir.FieldMode{compilerir.ModeHidden}
			}
		}
	}
	hiddenBinder, err := New(hidden, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hiddenBinder.Query(QueryInput{Operation: readir.FindUnique, Model: post.ID, Arguments: map[string]any{"where": map[string]any{selector.Name: value}}, Selections: []readir.Selection{selection}}); err == nil {
		t.Fatal("selector with hidden component was accepted")
	}
}

func selectorTestValue(logical compilerir.LogicalTypeIR) any {
	switch logical.Kind {
	case compilerir.TypeUUID:
		return "00000000-0000-0000-0000-000000000001"
	case compilerir.TypeInt16, compilerir.TypeInt32:
		return 1
	case compilerir.TypeInt64:
		return "1"
	default:
		return "value"
	}
}

func TestGraphQLWhereBinderCoversEveryAcceptedP2OperatorAndRelationDepth(t *testing.T) {
	compilation := socialCompilation(t)
	postModelIndex, postContractIndex := -1, -1
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "Post" {
			postContractIndex = index
			break
		}
	}
	if postContractIndex >= 0 {
		for index := range compilation.Model.Models {
			if compilation.Model.Models[index].ID == compilation.Contract.Models[postContractIndex].ModelID {
				postModelIndex = index
				break
			}
		}
	}
	if postModelIndex < 0 || postContractIndex < 0 {
		t.Fatal("Post fixture is absent")
	}
	stringID := compilerir.FieldID("f0000000000000000000000000000001")
	listID := compilerir.FieldID("f0000000000000000000000000000002")
	jsonID := compilerir.FieldID("f0000000000000000000000000000003")
	listCapability := compilerir.CapabilityID("scalar-list:json-array:v1")
	stringType := compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
	compilation.Model.Models[postModelIndex].Fields = append(compilation.Model.Models[postModelIndex].Fields,
		compilerir.FieldIR{ID: stringID, GoName: "Subtitle", LogicalName: "subtitle", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "subtitle", Type: stringType, Nullable: true}},
		compilerir.FieldIR{ID: listID, GoName: "Labels", LogicalName: "labels", Kind: compilerir.FieldScalarList, Scalar: &compilerir.ScalarFieldIR{Column: "labels", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &stringType, Capability: &listCapability}, Nullable: true}},
		compilerir.FieldIR{ID: jsonID, GoName: "Metadata", LogicalName: "metadata", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "metadata", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}, Nullable: true}},
	)
	compilation.Contract.Models[postContractIndex].Fields = append(compilation.Contract.Models[postContractIndex].Fields,
		compilerir.FieldContractIR{FieldID: stringID, GraphQLName: "subtitle", Modes: []compilerir.FieldMode{compilerir.ModeVisible}},
		compilerir.FieldContractIR{FieldID: listID, GraphQLName: "labels", Modes: []compilerir.FieldMode{compilerir.ModeVisible}},
		compilerir.FieldContractIR{FieldID: jsonID, GraphQLName: "metadata", Modes: []compilerir.FieldMode{compilerir.ModeVisible}},
	)
	binder, err := New(compilation, Limits{MaxInputDepth: 8, MaxInputNodes: 64, MaxListItems: 32})
	if err != nil {
		t.Fatal(err)
	}
	post, contract := modelNamed(t, compilation, "Post")
	selection := scalarSelection(t, contract.Selectors[0].Fields[0])
	request, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}, Arguments: map[string]any{
		"where": map[string]any{
			"AND": []any{
				map[string]any{"subtitle": map[string]any{
					"equals": "a", "not": "b", "in": []any{"a", "b"}, "notIn": []any{"c"},
					"lt": "z", "lte": "z", "gt": "a", "gte": "a",
					"contains": "x", "startsWith": "a", "endsWith": "z", "mode": "insensitive",
				}},
				map[string]any{"subtitle": map[string]any{"isNull": false}},
				map[string]any{"subtitle": map[string]any{"isNull": true}},
				map[string]any{"labels": map[string]any{
					"equals": []any{"a", "b"}, "has": "a", "hasEvery": []any{"a", "b"}, "hasSome": []any{"c"}, "isEmpty": false, "isNull": false,
				}},
				map[string]any{"labels": map[string]any{"isNull": true}},
				map[string]any{"metadata": map[string]any{"isNull": true}},
				map[string]any{"metadata": map[string]any{"isNull": false}},
				map[string]any{"metadata": map[string]any{
					"path": []any{"profile"}, "equals": map[string]any{"enabled": true}, "not": "blocked",
					"lt": 10, "lte": 10, "gt": 1, "gte": 1,
					"stringContains": "x", "stringStartsWith": "a", "stringEndsWith": "z",
					"arrayContains": []any{1}, "arrayStartsWith": []any{1}, "arrayEndsWith": []any{2},
				}},
				map[string]any{"author": map[string]any{"is": map[string]any{"all": true}}},
				map[string]any{"author": map[string]any{"isNot": map[string]any{"all": true}}},
				map[string]any{"author": map[string]any{"is": nil}},
				map[string]any{"author": map[string]any{"isNot": nil}},
				map[string]any{"comments": map[string]any{"some": map[string]any{"all": true}, "every": map[string]any{"all": true}, "none": map[string]any{"all": true}}},
				map[string]any{"OR": []any{map[string]any{"title": map[string]any{"contains": "graph"}}, map[string]any{"title": map[string]any{"startsWith": "go"}}}},
				map[string]any{"NOT": []any{map[string]any{"title": map[string]any{"equals": "private"}}}},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	where, ok := request.Where()
	if !ok || where.Kind() != policyir.ConditionLogical {
		t.Fatalf("where kind = %v/%v", where.Kind(), ok)
	}
	seen := map[policyir.OperatorID]bool{}
	logical := map[policyir.LogicalOperator]bool{}
	if err := walkCondition(where, func(condition policyir.Condition) error {
		if operatorID, present := condition.Operator(); present {
			seen[operatorID] = true
		}
		if operatorID, _, present := condition.Logical(); present {
			logical[operatorID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected := []policyir.OperatorID{
		policyir.OperatorEqual, policyir.OperatorNotEqual, policyir.OperatorIn, policyir.OperatorNotIn,
		policyir.OperatorLessThan, policyir.OperatorLessThanOrEqual, policyir.OperatorGreaterThan, policyir.OperatorGreaterThanOrEqual,
		policyir.OperatorContains, policyir.OperatorStartsWith, policyir.OperatorEndsWith, policyir.OperatorIsNull, policyir.OperatorIsNotNull,
		policyir.OperatorListEqual, policyir.OperatorListHas, policyir.OperatorListHasEvery, policyir.OperatorListHasSome, policyir.OperatorListIsEmpty, policyir.OperatorListIsNull, policyir.OperatorListIsNotNull,
		policyir.OperatorJSONIsNull, policyir.OperatorJSONIsNotNull, policyir.OperatorJSONEqual, policyir.OperatorJSONNotEqual,
		policyir.OperatorJSONLessThan, policyir.OperatorJSONLessThanOrEqual, policyir.OperatorJSONGreaterThan, policyir.OperatorJSONGreaterThanOrEqual,
		policyir.OperatorJSONStringContains, policyir.OperatorJSONStringStartsWith, policyir.OperatorJSONStringEndsWith,
		policyir.OperatorJSONArrayContains, policyir.OperatorJSONArrayStartsWith, policyir.OperatorJSONArrayEndsWith,
		policyir.OperatorRelationIs, policyir.OperatorRelationIsNot, policyir.OperatorRelationIsNull, policyir.OperatorRelationIsNotNull,
		policyir.OperatorRelationSome, policyir.OperatorRelationEvery, policyir.OperatorRelationNone,
	}
	for _, operatorID := range expected {
		if !seen[operatorID] {
			t.Errorf("P2 operator %d was not produced", operatorID)
		}
	}
	if len(seen) != len(expected) {
		t.Errorf("operator inventory = %v, want exactly %v", seen, expected)
	}
	for _, operatorID := range []policyir.LogicalOperator{policyir.LogicalAnd, policyir.LogicalOr, policyir.LogicalNot} {
		if !logical[operatorID] {
			t.Errorf("logical operator %d was not produced", operatorID)
		}
	}

	bad := []map[string]any{
		{"where": map[string]any{}},
		{"where": map[string]any{"all": true, "title": map[string]any{"equals": "x"}}},
		{"where": map[string]any{"unknown": map[string]any{"equals": "x"}}},
		{"where": map[string]any{"title": map[string]any{}}},
	}
	for index, args := range bad {
		if _, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: post.ID, Selections: []readir.Selection{selection}, Arguments: args}); err == nil {
			t.Fatalf("bad input %d was accepted", index)
		}
	}
}

func TestSelectionArgumentsUseBinderForNestedRelationAndCount(t *testing.T) {
	compilation := socialCompilation(t)
	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	_, contract := modelNamed(t, compilation, "Post")
	queryText := `query Feed {
  ` + contract.Roots.FindMany + `(where: { title: { contains: "go" } }, orderBy: [{ createdAt: desc }], take: 5) {
    id
    comments(where: { body: { contains: "useful" } }, take: 2) { id body }
    _count { comments(where: { body: { not: "spam" } }) }
  }
}`
	query, errors := gqlparser.LoadQuery(schema, queryText)
	if len(errors) != 0 {
		t.Fatalf("query errors = %v\n%s", errors, document.SDL)
	}
	root := query.Operations.ForName("Feed").SelectionSet[0].(*ast.Field)
	result, err := selectset.Compile(selectset.Request{
		Compilation: compilation,
		Model:       contract.ModelID,
		Selections:  root.SelectionSet,
		Fragments:   query.Fragments,
		Child: func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
			return binder.Child(target, field, selections, nil)
		},
		Count: func(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
			return binder.Count(target, field, nil)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	args, err := argumentValues(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: contract.ModelID, Arguments: args, Selections: result.Selections})
	if err != nil {
		t.Fatal(err)
	}
	if take, ok := request.Take(); !ok || take != 5 || len(request.Selection()) != 3 {
		t.Fatalf("root request take/selections = %d/%v/%d", take, ok, len(request.Selection()))
	}
	for _, selection := range request.Selection() {
		if selection.Kind() == readir.SelectRelation {
			child, _ := selection.Request()
			if take, ok := child.Take(); !ok || take != 2 {
				t.Fatalf("relation take = %d/%v", take, ok)
			}
		}
		if selection.Kind() == readir.SelectRelationCount {
			child, _ := selection.Request()
			if _, ok := child.Where(); !ok {
				t.Fatal("count where was not lowered")
			}
		}
	}
}

type errFoundRelation struct{}

func (errFoundRelation) Error() string { return "found relation" }

func walkCondition(condition policyir.Condition, visit func(policyir.Condition) error) error {
	if err := visit(condition); err != nil {
		return err
	}
	if _, children, ok := condition.Logical(); ok {
		for _, child := range children {
			if err := walkCondition(child, visit); err != nil {
				return err
			}
		}
	}
	if _, _, _, _, child, ok := condition.Relation(); ok && child != nil {
		return walkCondition(*child, visit)
	}
	return nil
}

func socialCompilation(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	return *result.Compilation
}

func modelNamed(t *testing.T, compilation compilerir.CompilationIR, name string) (compilerir.ModelDeclIR, compilerir.ModelContractIR) {
	t.Helper()
	var contract compilerir.ModelContractIR
	for _, value := range compilation.Contract.Models {
		if value.GraphQLName == name {
			contract = value
			break
		}
	}
	for _, model := range compilation.Model.Models {
		if model.ID == contract.ModelID {
			return model, contract
		}
	}
	t.Fatalf("missing model %s", name)
	return compilerir.ModelDeclIR{}, compilerir.ModelContractIR{}
}

func fieldContract(t *testing.T, contract compilerir.ModelContractIR, id compilerir.FieldID) compilerir.FieldContractIR {
	t.Helper()
	for _, field := range contract.Fields {
		if field.FieldID == id {
			return field
		}
	}
	t.Fatalf("missing contract field %s", id)
	return compilerir.FieldContractIR{}
}

func modelField(t *testing.T, model compilerir.ModelDeclIR, id compilerir.FieldID) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.ID == id {
			return field
		}
	}
	t.Fatalf("missing model field %s", id)
	return compilerir.FieldIR{}
}

func scalarSelection(t *testing.T, id compilerir.FieldID) readir.Selection {
	t.Helper()
	fieldID, err := policyFieldID(id)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := readir.NewScalarSelection(fieldID)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}
