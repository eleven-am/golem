package operation

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
)

var (
	hydrationModel = golem.ModelID{1}
	hydrationField = golem.FieldID{2}
)

func hydrationRow(t *testing.T, key int64) golem.RuntimeModelRow {
	t.Helper()
	return identifiedRowForTest(t, hydrationModel, hydrationField, key)
}

func hydrationOf(typ compilerir.GraphQLTypeIR, distinct bool) *Hydration {
	return &Hydration{typ: typ, publicModel: hydrationModel, distinct: distinct, requested: map[golem.RuntimeSemanticIdentityToken]struct{}{}}
}

func listOf(element compilerir.GraphQLTypeIR) compilerir.GraphQLTypeIR {
	return compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &element}
}

var modelType = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}

func hydratedByKey(t *testing.T, keys ...int64) HydratedRows {
	t.Helper()
	rows := make(HydratedRows, len(keys))
	for _, key := range keys {
		row := hydrationRow(t, key)
		rows[golem.RuntimeModelRowIdentity(row).Token()] = row
	}
	return rows
}

func keysOf(t *testing.T, value any) []int64 {
	t.Helper()
	var keys []int64
	for _, item := range value.([]any) {
		cell := golem.RuntimeTransportField(item.(golem.RuntimeModelRow), hydrationField)
		raw, _ := cell.Get()
		keys = append(keys, raw.(int64))
	}
	return keys
}

func TestHydrationCollectsEachIdentityOnceInFirstSeenOrder(t *testing.T) {
	hydration := hydrationOf(listOf(modelType), false)
	if err := hydration.Collect([]any{hydrationRow(t, 3), hydrationRow(t, 1), hydrationRow(t, 3)}); err != nil {
		t.Fatal(err)
	}
	if err := hydration.Collect([]any{hydrationRow(t, 1), hydrationRow(t, 2)}); err != nil {
		t.Fatal(err)
	}
	if hydration.Len() != 3 {
		t.Fatalf("identities = %d, want 3", hydration.Len())
	}
	var order []int64
	for _, identity := range hydration.identities {
		order = append(order, identity.Values()[0].(int64))
	}
	if !reflect.DeepEqual(order, []int64{3, 1, 2}) {
		t.Fatalf("identity order = %v", order)
	}
}

func TestHydrationOfRankedResultsRejectsDuplicateIdentities(t *testing.T) {
	hydration := hydrationOf(listOf(modelType), true)
	if err := hydration.Collect([]any{hydrationRow(t, 1), hydrationRow(t, 1)}); err == nil {
		t.Fatal("a ranked result repeated an identity")
	}
}

func TestHydrationRefusesValuesItCannotReadAgain(t *testing.T) {
	handBuilt, err := golem.RuntimeModelReadRow(hydrationModel, golem.RuntimePresentReadCell(hydrationField, int64(1), nil))
	if err != nil {
		t.Fatal(err)
	}
	foreign := identifiedRowForTest(t, golem.ModelID{9}, hydrationField, int64(1))
	for name, value := range map[string]any{
		"row without identity": []any{handBuilt},
		"row of another model": []any{foreign},
		"not a row":            []any{"x"},
		"not a list":           handBuilt,
		"list item is a list":  []any{[]any{hydrationRow(t, 1)}},
	} {
		if err := hydrationOf(listOf(modelType), false).Collect(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := hydrationOf(modelType, false).Collect([]any{hydrationRow(t, 1)}); err == nil {
		t.Error("a list was accepted for a single model")
	}
}

func TestHydrationIndexRefusesRowsItDidNotRequest(t *testing.T) {
	hydration := hydrationOf(listOf(modelType), false)
	if err := hydration.Collect([]any{hydrationRow(t, 1)}); err != nil {
		t.Fatal(err)
	}
	handBuilt, err := golem.RuntimeModelReadRow(hydrationModel)
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range map[string][]golem.RuntimeModelRow{
		"unrequested identity": {hydrationRow(t, 2)},
		"repeated identity":    {hydrationRow(t, 1), hydrationRow(t, 1)},
		"row without identity": {handBuilt},
		"row of another model": {identifiedRowForTest(t, golem.ModelID{9}, hydrationField, int64(1))},
	} {
		if _, err := hydration.Index(rows); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	indexed, err := hydration.Index([]golem.RuntimeModelRow{hydrationRow(t, 1)})
	if err != nil || len(indexed) != 1 {
		t.Fatalf("indexed = %v err=%v", indexed, err)
	}
}

func TestHydrationRestoreKeepsOrderDuplicatesAndShapeAndOmitsAbsentRows(t *testing.T) {
	value := []any{hydrationRow(t, 3), hydrationRow(t, 1), hydrationRow(t, 3), hydrationRow(t, 2)}
	hydration := hydrationOf(listOf(modelType), false)
	restored, absent, err := hydration.Restore(value, hydratedByKey(t, 1, 3))
	if err != nil || absent != 1 || !reflect.DeepEqual(keysOf(t, restored), []int64{3, 1, 3}) {
		t.Fatalf("restored=%v absent=%d err=%v", restored, absent, err)
	}

	nested := hydrationOf(listOf(listOf(modelType)), false)
	grouped, absent, err := nested.Restore([]any{[]any{hydrationRow(t, 1), hydrationRow(t, 2)}, []any{hydrationRow(t, 3)}, []any{hydrationRow(t, 2)}}, hydratedByKey(t, 1, 3))
	if err != nil || absent != 2 {
		t.Fatalf("grouped absent=%d err=%v", absent, err)
	}
	shape := []int{}
	for _, group := range grouped.([]any) {
		shape = append(shape, len(group.([]any)))
	}
	if !reflect.DeepEqual(shape, []int{1, 1, 0}) {
		t.Fatalf("nested shape = %v", shape)
	}

	nullable := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post", Nullable: true}
	items := hydrationOf(listOf(nullable), false)
	kept, absent, err := items.Restore([]any{nil, hydrationRow(t, 1)}, hydratedByKey(t, 1))
	if err != nil || absent != 0 || len(kept.([]any)) != 2 || kept.([]any)[0] != nil {
		t.Fatalf("nullable list item kept=%v absent=%d err=%v", kept, absent, err)
	}
}

func TestHydrationSingleResultsAreNullOrMissingAccordingToNullability(t *testing.T) {
	nullable := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post", Nullable: true}
	for name, test := range map[string]struct {
		typ     compilerir.GraphQLTypeIR
		missing bool
	}{
		"nullable":      {typ: nullable},
		"non-null":      {typ: modelType, missing: true},
		"list":          {typ: listOf(modelType)},
		"nullable list": {typ: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Nullable: true, Element: &modelType}},
	} {
		hydration := hydrationOf(test.typ, false)
		var value any = hydrationRow(t, 1)
		if test.typ.Kind == compilerir.GraphQLTypeList {
			value = []any{value}
		}
		restored, absent, err := hydration.Restore(value, HydratedRows{})
		if err != nil || absent != 1 || hydration.AbsentIsMissing(absent) != test.missing {
			t.Fatalf("%s: restored=%v absent=%d missing=%t err=%v", name, restored, absent, hydration.AbsentIsMissing(absent), err)
		}
		if test.typ.Kind != compilerir.GraphQLTypeList && restored != nil {
			t.Fatalf("%s: an absent single result must restore to nil, got %v", name, restored)
		}
	}
	if hydrationOf(modelType, false).AbsentIsMissing(0) {
		t.Fatal("nothing absent cannot be missing")
	}
}

func TestHydrationChunkNeverExceedsThePageLimits(t *testing.T) {
	compilation := semanticSocial(t)
	post := contractNamed(t, compilation.Contract, "Post")
	publicModel := golemModelID(post.ModelID)
	field, _ := publicFieldID(modelByIDForTest(t, compilation.Model, post.ModelID).PrimaryKey.Fields[0])
	collect := func(compiler *Compiler) *Hydration {
		hydration, err := compiler.newHydration(listOf(compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}), post.ModelID, nil, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		var rows []any
		for key := int64(1); key <= 5; key++ {
			rows = append(rows, identifiedRowForTest(t, publicModel, field, semanticIdentityValue(t, generatedFieldByIDForTest(t, modelByIDForTest(t, compilation.Model, post.ModelID), modelByIDForTest(t, compilation.Model, post.ModelID).PrimaryKey.Fields[0]), byte(key))))
		}
		if err := hydration.Collect(rows); err != nil {
			t.Fatal(err)
		}
		return hydration
	}
	unlimited, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(unlimited).Chunk(); got != 5 {
		t.Fatalf("unlimited chunk = %d, want every identity", got)
	}
	paged, err := New(compilation, Limits{Bind: graphqlbind.Limits{MaxPageSize: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(paged).Chunk(); got != 2 {
		t.Fatalf("runtime page limit chunk = %d, want 2", got)
	}
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].ModelID == post.ModelID {
			compilation.Contract.Models[index].Limits.MaxTake = 1
		}
	}
	taken, err := New(compilation, Limits{Bind: graphqlbind.Limits{MaxPageSize: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(taken).Chunk(); got != 1 {
		t.Fatalf("model max take chunk = %d, want 1", got)
	}
}
