package mutation

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestMapBinderLowersUniqueAndCompoundSelectorsDeterministically(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	update := &Input{Kind: UpdateInput, Model: golem.ModelID(mustFixed(t, ids.user)), Scalars: []Scalar{{Field: golem.FieldID(mustFixed(t, ids.name)), Operation: golem.MutationFieldSet, Value: "updated"}}}

	single, err := binder.Lower(MapRootInput{Operation: Update, Model: ids.user, Where: map[string]any{"id": int32(7)}, Data: update})
	if err != nil {
		t.Fatal(err)
	}
	target, ok := single.Target()
	if !ok || target.Selector().KeyID() != golem.KeyID(mustFixed(t, ids.userPK)) || len(target.Selector().Fields()) != 1 {
		t.Fatalf("single selector mismatch: %#v %v", target.Selector(), ok)
	}
	firstCanonical := target.SelectorPredicate().CanonicalBytes()
	again, err := binder.Lower(MapRootInput{Operation: Update, Model: ids.user, Where: map[string]any{"id": int32(7)}, Data: update})
	if err != nil {
		t.Fatal(err)
	}
	againTarget, _ := again.Target()
	if !bytes.Equal(firstCanonical, againTarget.SelectorPredicate().CanonicalBytes()) {
		t.Fatal("identical selector maps produced different frozen predicates")
	}

	compound, err := binder.Lower(MapRootInput{Operation: Upsert, Model: ids.user, Where: map[string]any{"tenantHandle": map[string]any{"handle": "roy", "tenantID": int32(4)}}, Create: &Input{Kind: CreateInput, Model: update.Model}, Update: update})
	if err != nil {
		t.Fatal(err)
	}
	compoundTarget, _ := compound.Target()
	fields := compoundTarget.Selector().Fields()
	if compoundTarget.Selector().KeyID() != golem.KeyID(mustFixed(t, ids.userTenantHandle)) || len(fields) != 2 || fields[0] != golem.FieldID(mustFixed(t, ids.tenantID)) || fields[1] != golem.FieldID(mustFixed(t, ids.handle)) {
		t.Fatalf("compound selector order/identity mismatch: key=%x fields=%x", compoundTarget.Selector().KeyID(), fields)
	}
}

func TestMapBinderLowersLogicalAndRelationPredicatesToFrozenP4Where(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]any{
		"AND": []any{
			map[string]any{"name": map[string]any{"contains": "go"}},
			map[string]any{"posts": map[string]any{"some": map[string]any{"title": map[string]any{"equals": "hello"}}}},
		},
	}
	root := MapRootInput{Operation: UpdateMany, Model: ids.user, Where: where, Data: &Input{Kind: UpdateManyInput, Model: golem.ModelID(mustFixed(t, ids.user)), Scalars: []Scalar{{Field: golem.FieldID(mustFixed(t, ids.name)), Operation: golem.MutationFieldSet, Value: "x"}}}}
	request, err := binder.Lower(root)
	if err != nil {
		t.Fatal(err)
	}
	frozen, ok := request.Where()
	if !ok || frozen.View().RootModelID() != golem.ModelID(mustFixed(t, ids.user)) {
		t.Fatalf("where root mismatch: %v %x", ok, frozen.View().RootModelID())
	}
	if !containsRelation(frozen.View().Root()) {
		t.Fatalf("frozen predicate lost relation traversal: %#v", frozen.View().Root())
	}
	again, err := binder.Lower(root)
	if err != nil {
		t.Fatal(err)
	}
	againWhere, _ := again.Where()
	if !bytes.Equal(frozen.CanonicalBytes(), againWhere.CanonicalBytes()) {
		t.Fatal("logical/relation map lowering is nondeterministic")
	}
}

func TestMapBinderPreservesExactBigIntAndDecimalOperands(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := binder.Lower(MapRootInput{
		Operation: DeleteMany,
		Model:     ids.user,
		Where: map[string]any{
			"big":    map[string]any{"equals": "9007199254740993"},
			"amount": map[string]any{"equals": "123.45"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, ok := request.Where()
	if !ok {
		t.Fatal("deleteMany lost its frozen where predicate")
	}
	big := findFrozenField(frozen.View().Root(), golem.FieldID(mustFixed(t, ids.big)))
	if big == nil {
		t.Fatal("frozen predicate lost BigInt field")
	}
	bigValue, ok := big.Operand().One()
	if value, width, exact := bigValue.Signed(); !ok || !exact || width != 64 || value != 9007199254740993 {
		t.Fatalf("BigInt operand = (%d, %d, %v), want exact int64", value, width, exact)
	}
	amount := findFrozenField(frozen.View().Root(), golem.FieldID(mustFixed(t, ids.amount)))
	if amount == nil {
		t.Fatal("frozen predicate lost Decimal field")
	}
	amountValue, ok := amount.Operand().One()
	if !ok {
		t.Fatal("Decimal operand is not singular")
	}
	decimal, exact := amountValue.Decimal()
	if !exact || decimal.Coefficient() != 12345 || decimal.Scale() != 2 {
		t.Fatalf("Decimal operand = (%d, %d, %v), want normalized 12345 scale 2", decimal.Coefficient(), decimal.Scale(), exact)
	}
}

func TestMapBinderPreservesJSONRootAndColumnNullDistinction(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	columnNull, err := binder.Lower(MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"meta": map[string]any{"isNull": true}}})
	if err != nil {
		t.Fatal(err)
	}
	columnWhere, _ := columnNull.Where()
	column := findFrozenField(columnWhere.View().Root(), golem.FieldID(mustFixed(t, ids.meta)))
	if column == nil || column.Operator() != golem.FrozenOperatorIsNull || column.Path().Present() || column.Operand().Kind() != golem.FrozenOperandNone {
		t.Fatalf("column null shape was not preserved: %#v", column)
	}
	documentNull, err := binder.Lower(MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"meta": map[string]any{"equals": nil}}})
	if err != nil {
		t.Fatal(err)
	}
	documentWhere, _ := documentNull.Where()
	document := findFrozenField(documentWhere.View().Root(), golem.FieldID(mustFixed(t, ids.meta)))
	if document == nil || document.Operator() != golem.FrozenOperatorJSONEq || !document.Path().Present() {
		t.Fatalf("document null root shape was not preserved: %#v", document)
	}
	if nullKind, ok := document.Operand().JSONNull(); !ok || nullKind != golem.FrozenJSONDocumentNull {
		t.Fatalf("document null operand = %v/%v", nullKind, ok)
	}
}

func TestMapBinderBindsPresenceAwareRawInputsAndCompleteNestedVocabulary(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	omitted, err := binder.LowerValues(Create, ids.user, map[string]any{"data": map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	omittedInput, _ := omitted.Input()
	if len(omittedInput.Fields()) != 0 {
		t.Fatal("omitted create field became an authored operation")
	}
	explicit, err := binder.LowerValues(Create, ids.user, map[string]any{"data": map[string]any{"name": nil}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	explicitInput, _ := explicit.Input()
	if fields := explicitInput.Fields(); len(fields) != 1 || fields[0].Operation() != golem.MutationFieldNull {
		t.Fatalf("explicit create null = %#v", fields)
	}

	selector := func(id int32) map[string]any { return map[string]any{"id": id} }
	createPost := func(id int32, title string) map[string]any { return map[string]any{"id": id, "title": title} }
	updatePost := func(title string) map[string]any { return map[string]any{"title": map[string]any{"set": title}} }
	relation := map[string]any{
		"create":          []any{createPost(1, "create")},
		"createMany":      []any{createPost(2, "create-many")},
		"connect":         []any{selector(3)},
		"connectOrCreate": []any{map[string]any{"where": selector(4), "create": createPost(4, "coc")}},
		"disconnect":      []any{selector(5)},
		"set":             []any{selector(6)},
		"update":          []any{map[string]any{"where": selector(7), "data": updatePost("update")}},
		"updateMany":      []any{map[string]any{"where": map[string]any{"title": map[string]any{"contains": "old"}}, "data": updatePost("many")}},
		"upsert":          []any{map[string]any{"where": selector(8), "create": createPost(8, "upsert"), "update": updatePost("upserted")}},
		"delete":          []any{selector(9)},
		"deleteMany":      []any{map[string]any{"title": map[string]any{"equals": "gone"}}},
	}
	request, err := binder.LowerValues(Update, ids.user, map[string]any{
		"where": selector(20),
		"data":  map[string]any{"name": map[string]any{"setNull": true}, "posts": relation},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := request.Input()
	if got := input.Relations(); len(got) != 11 {
		t.Fatalf("raw nested vocabulary lowered to %d operations, want 11", len(got))
	}
	for index, nested := range input.Relations() {
		if nested.Action() != golem.MutationRelationAction(index+1) {
			t.Fatalf("nested action %d = %d", index, nested.Action())
		}
	}
}

func TestMapBinderUsesRelationCardinalityToDiscriminateUpdateAndUpsertShapes(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	selector := map[string]any{"id": int32(20)}
	request, err := binder.LowerValues(Update, ids.post, map[string]any{
		"where": selector,
		"data": map[string]any{"author": map[string]any{
			"update": map[string]any{"name": map[string]any{"set": "updated"}},
			"upsert": map[string]any{
				"create": map[string]any{"id": int32(2), "tenantID": int32(1), "handle": "new", "big": "2", "amount": "1", "meta": nil},
				"update": map[string]any{"name": map[string]any{"set": "upserted"}},
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input, ok := request.Input()
	if !ok {
		t.Fatal("to-one relation input is absent")
	}
	relations := input.Relations()
	if len(relations) != 2 {
		t.Fatalf("to-one relation operations = %d, want 2", len(relations))
	}
	for _, relation := range relations {
		for _, branch := range relation.Branches() {
			if _, targeted := branch.Target(); targeted {
				t.Fatalf("to-one action %d incorrectly acquired a selector", relation.Action())
			}
		}
	}
}

func TestMapBinderRefusesMalformedRawMutationBeforeRuntimeBoundary(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{MaxInputDepth: 2, MaxInputNodes: 8, MaxListItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	tests := []map[string]any{
		{"data": map[string]any{"missing": "x"}},
		{"data": map[string]any{"name": map[string]any{"set": "a", "setNull": true}}, "where": map[string]any{"id": int32(1)}},
		{"data": map[string]any{"name": map[string]any{"setNull": false}}, "where": map[string]any{"id": int32(1)}},
		{"data": map[string]any{"posts": map[string]any{"connect": []any{map[string]any{"id": int32(1)}, map[string]any{"id": int32(2)}}}}, "where": map[string]any{"id": int32(1)}},
	}
	operations := []RootOperation{Create, Update, Update, Update}
	for index, arguments := range tests {
		if _, err := binder.LowerValues(operations[index], ids.user, arguments, nil); err == nil {
			t.Fatalf("malformed raw mutation %d was accepted", index)
		}
	}
}

func TestMapBinderPreservesExplicitCreateNullAndRejectsBadMapsAndLimits(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{MaxInputDepth: 2, MaxInputNodes: 8, MaxListItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	model, name := golem.ModelID(mustFixed(t, ids.user)), golem.FieldID(mustFixed(t, ids.name))
	created, err := binder.Lower(MapRootInput{Operation: Create, Model: ids.user, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: name, Operation: golem.MutationFieldNull}}}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := created.Input()
	fields := input.Fields()
	if len(fields) != 1 || fields[0].Operation() != golem.MutationFieldNull {
		t.Fatalf("explicit create null was lost: %#v", fields)
	}
	if _, present := fields[0].Value(); present {
		t.Fatal("explicit create null acquired a value")
	}

	update := &Input{Kind: UpdateInput, Model: model, Scalars: []Scalar{{Field: name, Operation: golem.MutationFieldSet, Value: "x"}}}
	tests := []struct {
		name  string
		input MapRootInput
		want  string
	}{
		{"null selector", MapRootInput{Operation: Update, Model: ids.user, Where: nil, Data: update}, "explicit null"},
		{"unknown selector", MapRootInput{Operation: Update, Model: ids.user, Where: map[string]any{"missing": int32(1)}, Data: update}, "unknown"},
		{"unknown compound field", MapRootInput{Operation: Update, Model: ids.user, Where: map[string]any{"tenantHandle": map[string]any{"tenantID": int32(1), "wrong": "x"}}, Data: update}, "required"},
		{"unknown where field", MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"missing": map[string]any{"equals": "x"}}}, "unknown"},
		{"unknown operator", MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"name": map[string]any{"wat": "x"}}}, "unknown operator"},
		{"depth", MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"AND": []any{map[string]any{"AND": []any{map[string]any{"name": map[string]any{"equals": "x"}}}}}}}, "depth"},
		{"list", MapRootInput{Operation: DeleteMany, Model: ids.user, Where: map[string]any{"OR": []any{map[string]any{"name": map[string]any{"equals": "a"}}, map[string]any{"name": map[string]any{"equals": "b"}}, map[string]any{"name": map[string]any{"equals": "c"}}}}}, "list item"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := binder.Lower(test.input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func containsRelation(node golem.FrozenConditionView) bool {
	if node.Kind() == golem.FrozenConditionRelation {
		return true
	}
	for _, child := range node.Children() {
		if containsRelation(child) {
			return true
		}
	}
	return false
}

func findFrozenField(node golem.FrozenConditionView, field golem.FieldID) golem.FrozenConditionView {
	if current, ok := node.FieldID(); ok && current == field {
		return node
	}
	for _, child := range node.Children() {
		if found := findFrozenField(child, field); found != nil {
			return found
		}
	}
	return nil
}

type mutationIDs struct {
	user, post                                           compilerir.ModelID
	id, tenantID, handle, name, big, amount, meta, posts compilerir.FieldID
	postID, title, author                                compilerir.FieldID
	userPK, userTenantHandle, postPK                     compilerir.KeyID
	relation                                             compilerir.RelationID
}

func mutationCompilation() (compilerir.CompilationIR, mutationIDs) {
	id := func(seed byte) string { return hexID(seed) }
	ids := mutationIDs{
		user: compilerir.ModelID(id(1)), post: compilerir.ModelID(id(2)), id: compilerir.FieldID(id(3)), tenantID: compilerir.FieldID(id(4)),
		handle: compilerir.FieldID(id(5)), name: compilerir.FieldID(id(6)), big: compilerir.FieldID(id(7)), amount: compilerir.FieldID(id(8)), meta: compilerir.FieldID(id(9)), posts: compilerir.FieldID(id(10)), postID: compilerir.FieldID(id(11)), title: compilerir.FieldID(id(12)), author: compilerir.FieldID(id(13)),
		userPK: compilerir.KeyID(id(14)), userTenantHandle: compilerir.KeyID(id(15)), postPK: compilerir.KeyID(id(16)), relation: compilerir.RelationID(id(17)),
	}
	stringType := compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
	intType := compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}
	bigType := compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}
	jsonType := compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}
	precision, scale := uint16(18), uint16(6)
	decimalType := compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Precision: &precision, Scale: &scale}
	userFields := []compilerir.FieldIR{
		scalarField(ids.id, "ID", intType, false), scalarField(ids.tenantID, "TenantID", intType, false),
		scalarField(ids.handle, "Handle", stringType, false), scalarField(ids.name, "Name", stringType, true), scalarField(ids.big, "Big", bigType, false), scalarField(ids.amount, "Amount", decimalType, false), scalarField(ids.meta, "Meta", jsonType, true),
		{ID: ids.posts, GoName: "Posts", Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: ids.relation, Role: compilerir.RelationInverse, Kind: compilerir.RelationHasMany}},
	}
	postFields := []compilerir.FieldIR{
		scalarField(ids.postID, "ID", intType, false), scalarField(ids.title, "Title", stringType, false),
		{ID: ids.author, GoName: "Author", Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: ids.relation, Role: compilerir.RelationSource, Kind: compilerir.RelationBelongsTo}},
	}
	visible := func(field compilerir.FieldID, name string) compilerir.FieldContractIR {
		return compilerir.FieldContractIR{FieldID: field, GraphQLName: name, Modes: []compilerir.FieldMode{compilerir.ModeVisible}}
	}
	return compilerir.CompilationIR{
		Model: compilerir.ModelIR{Providers: []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL}, Models: []compilerir.ModelDeclIR{
			{ID: ids.user, Go: compilerir.GoNamedTypeIR{Name: "User"}, Fields: userFields, PrimaryKey: &compilerir.KeyIR{ID: ids.userPK, Kind: compilerir.KeyPrimary, Fields: []compilerir.FieldID{ids.id}}, Uniques: []compilerir.KeyIR{{ID: ids.userTenantHandle, Kind: compilerir.KeyUnique, Fields: []compilerir.FieldID{ids.tenantID, ids.handle}}}},
			{ID: ids.post, Go: compilerir.GoNamedTypeIR{Name: "Post"}, Fields: postFields, PrimaryKey: &compilerir.KeyIR{ID: ids.postPK, Kind: compilerir.KeyPrimary, Fields: []compilerir.FieldID{ids.postID}}},
		}, Relations: []compilerir.RelationIR{{ID: ids.relation, SourceModel: ids.post, TargetModel: ids.user, SourceField: ids.author, InverseField: ptrFieldID(ids.posts), Cardinality: compilerir.RelationMany}}},
		Contract: compilerir.ContractIR{Models: []compilerir.ModelContractIR{
			{ModelID: ids.user, GraphQLName: "User", Exposed: true, Fields: []compilerir.FieldContractIR{visible(ids.id, "id"), visible(ids.tenantID, "tenantID"), visible(ids.handle, "handle"), visible(ids.name, "name"), visible(ids.big, "big"), visible(ids.amount, "amount"), visible(ids.meta, "meta"), visible(ids.posts, "posts")}, Selectors: []compilerir.SelectorContractIR{{KeyID: ids.userPK, Kind: compilerir.KeyPrimary, Name: "id", Fields: []compilerir.FieldID{ids.id}}, {KeyID: ids.userTenantHandle, Kind: compilerir.KeyUnique, Name: "tenantHandle", Fields: []compilerir.FieldID{ids.tenantID, ids.handle}}}},
			{ModelID: ids.post, GraphQLName: "Post", Exposed: true, Fields: []compilerir.FieldContractIR{visible(ids.postID, "id"), visible(ids.title, "title"), visible(ids.author, "author")}, Selectors: []compilerir.SelectorContractIR{{KeyID: ids.postPK, Kind: compilerir.KeyPrimary, Name: "id", Fields: []compilerir.FieldID{ids.postID}}}},
		}},
	}, ids
}

func scalarField(id compilerir.FieldID, name string, typ compilerir.LogicalTypeIR, nullable bool) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Type: typ, Nullable: nullable}}
}

func ptrFieldID(value compilerir.FieldID) *compilerir.FieldID { return &value }

func hexID(seed byte) string {
	value := fixed(seed)
	const digits = "0123456789abcdef"
	result := make([]byte, 32)
	for index, item := range value {
		result[index*2], result[index*2+1] = digits[item>>4], digits[item&15]
	}
	return string(result)
}

func mustFixed[T ~string](t *testing.T, value T) [16]byte {
	t.Helper()
	parsed, err := fixedID(string(value))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
