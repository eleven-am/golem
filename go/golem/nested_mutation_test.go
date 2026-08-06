package golem

import "testing"

type nestedParent struct{}
type nestedChild struct{}

func TestNestedMutationVocabularyFreezesExplicitBranchesAndDetachesInputs(t *testing.T) {
	parent, child := ModelID{1}, ModelID{2}
	field, relation := FieldID{3}, RelationID{4}
	childID, childBytes := FieldID{5}, FieldID{6}
	key := KeyID{7}
	idColumn := GeneratedEqualField[nestedChild, int64](childID)
	bytesColumn := GeneratedBytesField[nestedChild](childBytes)
	selector := GeneratedUniqueSelectorValue[nestedChild](child, key, GeneratedSelectorComponent(childID, int64(9)))
	raw := []byte("before")
	create := GeneratedCreateInput(child,
		GeneratedCreateFieldValue(child, idColumn, int64(10)),
		GeneratedCreateFieldValue(child, bytesColumn, raw),
	)
	update := GeneratedUpdateInput(child, GeneratedSetFieldValue(child, bytesColumn, []byte("after")))
	updateMany := GeneratedUpdateManyInput(child, GeneratedSetFieldValue(child, bytesColumn, []byte("many")))
	predicate := idColumn.Eq(9)

	input := GeneratedUpdateInput[nestedParent](parent,
		GeneratedNestedCreate[nestedParent, nestedChild](parent, field, relation, child, create),
		GeneratedNestedCreateMany[nestedParent, nestedChild](parent, field, relation, child, create, create),
		GeneratedNestedConnect[nestedParent, nestedChild](parent, field, relation, child, selector),
		GeneratedNestedConnectOrCreate[nestedParent, nestedChild](parent, field, relation, child, selector, create),
		GeneratedNestedDisconnect[nestedParent, nestedChild](parent, field, relation, child, selector),
		GeneratedNestedSet[nestedParent, nestedChild](parent, field, relation, child, selector),
		GeneratedNestedUpdate[nestedParent, nestedChild](parent, field, relation, child, selector, update),
		GeneratedNestedUpdateMany[nestedParent, nestedChild](parent, field, relation, child, predicate, updateMany),
		GeneratedNestedUpsert[nestedParent, nestedChild](parent, field, relation, child, selector, create, update),
		GeneratedNestedDelete[nestedParent, nestedChild](parent, field, relation, child, selector),
		GeneratedNestedDeleteMany[nestedParent, nestedChild](parent, field, relation, child, predicate),
	)
	raw[0] = 'x'
	frozen, err := RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	relations := frozen.Relations()
	if len(frozen.Fields()) != 0 || len(relations) != 11 {
		t.Fatalf("fields=%d relations=%d", len(frozen.Fields()), len(relations))
	}
	for index, nested := range relations {
		want := MutationRelationAction(index + 1)
		if nested.ParentModelID() != parent || nested.TargetModelID() != child || nested.FieldID() != field || nested.RelationID() != relation || nested.Action() != want {
			t.Fatalf("nested[%d]=%#v want action=%d", index, nested, want)
		}
	}

	connectOrCreate := relations[3].Branches()
	if len(connectOrCreate) != 2 || connectOrCreate[0].Branch() != MutationRelationConnectOrCreateConnectBranch || connectOrCreate[0].Action() != MutationRelationConnect || connectOrCreate[1].Branch() != MutationRelationConnectOrCreateCreateBranch || connectOrCreate[1].Action() != MutationRelationCreate {
		t.Fatalf("connect-or-create branches=%#v", connectOrCreate)
	}
	upsert := relations[8].Branches()
	if len(upsert) != 2 || upsert[0].Branch() != MutationRelationUpsertCreateBranch || upsert[1].Branch() != MutationRelationUpsertUpdateBranch {
		t.Fatalf("upsert branches=%#v", upsert)
	}
	if _, ok := upsert[1].Target(); !ok {
		t.Fatal("upsert update branch lost its typed target")
	}
	if _, ok := relations[7].Branches()[0].Predicate(); !ok {
		t.Fatal("update-many branch lost its typed predicate")
	}

	created, ok := connectOrCreate[1].Input()
	if !ok {
		t.Fatal("connect-or-create create branch lost its typed child input")
	}
	value, _ := created.Fields()[1].Value()
	if string(value.([]byte)) != "before" {
		t.Fatalf("child bytes=%q", value)
	}
	value.([]byte)[0] = 'z'
	again, _ := frozen.Relations()[3].Branches()[1].Input()
	againValue, _ := again.Fields()[1].Value()
	if string(againValue.([]byte)) != "before" {
		t.Fatalf("nested input accessor leaked bytes: %q", againValue)
	}

	branches := relations[1].Branches()
	branches[0] = FrozenNestedMutationBranch{}
	if frozen.Relations()[1].Branches()[0].ModelID() != child {
		t.Fatal("nested branch accessor leaked slice storage")
	}
}

func TestNestedCreateAndUpdateCapabilitiesRemainDistinct(t *testing.T) {
	parent, child := ModelID{1}, ModelID{2}
	field, relation := FieldID{3}, RelationID{4}
	create := GeneratedCreateInput[nestedChild](child)
	shared := GeneratedNestedCreate[nestedParent, nestedChild](parent, field, relation, child, create)
	var _ NestedCreateValue[nestedParent] = shared
	var _ NestedUpdateValue[nestedParent] = shared
	updateOnly := GeneratedNestedDisconnectOne[nestedParent, nestedChild](parent, field, relation, child)
	var _ NestedUpdateValue[nestedParent] = updateOnly

	frozen, err := RuntimeFreezeCreateInput(GeneratedCreateInput[nestedParent](parent, shared))
	if err != nil || len(frozen.Relations()) != 1 || frozen.Relations()[0].Action() != MutationRelationCreate {
		t.Fatalf("create relation=%#v err=%v", frozen.Relations(), err)
	}
	if _, err := RuntimeFreezeUpdateInput(GeneratedUpdateInput[nestedParent](parent, updateOnly)); err != nil {
		t.Fatal(err)
	}
	childField := GeneratedEqualField[nestedChild, int64](FieldID{5})
	childUpdate := GeneratedUpdateInput[nestedChild](child, GeneratedSetFieldValue(child, childField, int64(1)))
	targetless := GeneratedNestedUpdate[nestedParent, nestedChild](parent, field, relation, child, nil, childUpdate)
	frozenUpdate, err := RuntimeFreezeUpdateInput(GeneratedUpdateInput[nestedParent](parent, targetless))
	if err != nil {
		t.Fatalf("generated to-one targetless update did not freeze: %v", err)
	}
	if _, present := frozenUpdate.Relations()[0].Branches()[0].Target(); present {
		t.Fatal("generated to-one targetless update unexpectedly retained a selector")
	}
}

func TestToManyNestedRowOperationsMayRepeatAsExplicitNodes(t *testing.T) {
	parent, child := ModelID{1}, ModelID{2}
	field, relation, childID := FieldID{3}, RelationID{4}, FieldID{5}
	selector := GeneratedUniqueSelectorValue[nestedChild](child, KeyID{6}, GeneratedSelectorComponent(childID, int64(1)))
	create := GeneratedCreateInput[nestedChild](child)
	first := GeneratedNestedConnectOrCreate[nestedParent, nestedChild](parent, field, relation, child, selector, create)
	second := GeneratedNestedConnectOrCreate[nestedParent, nestedChild](parent, field, relation, child, selector, create)
	frozen, err := RuntimeFreezeUpdateInput(GeneratedUpdateInput[nestedParent](parent, first, second))
	if err != nil {
		t.Fatal(err)
	}
	if relations := frozen.Relations(); len(relations) != 2 || relations[0].Action() != MutationRelationConnectOrCreate || relations[1].Action() != MutationRelationConnectOrCreate {
		t.Fatalf("repeated explicit nodes=%#v", relations)
	}
}

func TestNestedFreezeRejectsZeroGeneratedIdentityAndNilRequiredTarget(t *testing.T) {
	parent, child := ModelID{1}, ModelID{2}
	field, relation := FieldID{3}, RelationID{4}
	badIdentity := GeneratedNestedCreate[nestedParent, nestedChild](parent, field, RelationID{}, child, GeneratedCreateInput[nestedChild](child))
	if _, err := RuntimeFreezeCreateInput(GeneratedCreateInput[nestedParent](parent, badIdentity)); err == nil {
		t.Fatal("zero nested relation identity accepted")
	}
	nilConnect := GeneratedNestedConnect[nestedParent, nestedChild](parent, field, relation, child, nil)
	if _, err := RuntimeFreezeCreateInput(GeneratedCreateInput[nestedParent](parent, nilConnect)); err == nil {
		t.Fatal("connect with a nil required target accepted")
	}
}
