package mutation

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func TestSixRootMutationShapesLowerToFrozenP4Boundary(t *testing.T) {
	model, field := modelID(1), fieldID(2)
	target := frozenTarget(t, model, field, 7)
	where := target.SelectorPredicate()
	create := &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldNull}}}
	update := &Input{Kind: UpdateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldSet, Value: "updated"}}}
	updateMany := &Input{Kind: UpdateManyInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldIncrement, Value: int64(2)}}}

	tests := []struct {
		name      string
		input     RootInput
		operation mutationir.Operation
		inputKind golem.RuntimeMutationInputKind
	}{
		{"create", RootInput{Operation: Create, Model: model, Data: create}, mutationir.Create, golem.RuntimeMutationCreateInput},
		{"update", RootInput{Operation: Update, Model: model, Target: &target, Data: update}, mutationir.Update, golem.RuntimeMutationUpdateInput},
		{"upsert", RootInput{Operation: Upsert, Model: model, Target: &target, Create: create, Update: update}, mutationir.Upsert, 0},
		{"delete", RootInput{Operation: Delete, Model: model, Target: &target}, mutationir.Delete, 0},
		{"updateMany", RootInput{Operation: UpdateMany, Model: model, Where: &where, Data: updateMany}, mutationir.UpdateMany, golem.RuntimeMutationUpdateManyInput},
		{"deleteMany", RootInput{Operation: DeleteMany, Model: model, Where: &where}, mutationir.DeleteMany, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := Lower(test.input, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if request.Operation() != test.operation || request.ModelID() != model {
				t.Fatalf("operation/model mismatch: %v %x", request.Operation(), request.ModelID())
			}
			switch test.input.Operation {
			case Create, Update, UpdateMany:
				frozen, ok := request.Input()
				if !ok || frozen.ModelID() != model || len(frozen.Fields()) != 1 {
					t.Fatalf("root input was not preserved: %#v %v", frozen, ok)
				}
			case Upsert:
				created, createOK := request.CreateInput()
				updated, updateOK := request.UpdateInput()
				if !createOK || !updateOK || created.Fields()[0].Operation() != golem.MutationFieldNull || updated.Fields()[0].Operation() != golem.MutationFieldSet {
					t.Fatalf("upsert branches mismatch: %#v %#v", created.Fields(), updated.Fields())
				}
			}
		})
	}
}

func TestAllElevenNestedOperationsLowerWithExactBranches(t *testing.T) {
	parent, child := modelID(10), modelID(11)
	scalar := fieldID(12)
	target := frozenTarget(t, child, scalar, 99)
	rootTarget := frozenTarget(t, parent, fieldID(13), 100)
	predicate := target.SelectorPredicate()
	create := &Input{Kind: CreateInput, Model: child}
	update := &Input{Kind: UpdateInput, Model: child, Scalars: []Scalar{{Field: scalar, Operation: golem.MutationFieldSet, Value: "x"}}}
	updateMany := &Input{Kind: UpdateManyInput, Model: child, Scalars: []Scalar{{Field: scalar, Operation: golem.MutationFieldSet, Value: "x"}}}
	relations := []Relation{
		relationValue(1, child, golem.MutationRelationCreate, RelationEntry{Create: create}),
		relationValue(2, child, golem.MutationRelationCreateMany, RelationEntry{Create: create}),
		relationValue(3, child, golem.MutationRelationConnect, RelationEntry{Target: &target}),
		relationValue(4, child, golem.MutationRelationConnectOrCreate, RelationEntry{Target: &target, Create: create}),
		relationValue(5, child, golem.MutationRelationDisconnect, RelationEntry{}),
		relationValue(6, child, golem.MutationRelationSet),
		relationValue(7, child, golem.MutationRelationUpdate, RelationEntry{Target: &target, Update: update}),
		relationValue(8, child, golem.MutationRelationUpdateMany, RelationEntry{Predicate: &predicate, Update: updateMany}),
		relationValue(9, child, golem.MutationRelationUpsert, RelationEntry{Target: &target, Create: create, Update: update}),
		relationValue(10, child, golem.MutationRelationDelete, RelationEntry{}),
		relationValue(11, child, golem.MutationRelationDeleteMany, RelationEntry{Predicate: &predicate}),
	}
	request, err := Lower(RootInput{Operation: Update, Model: parent, Target: &rootTarget, Data: &Input{Kind: UpdateInput, Model: parent, Relations: relations}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	frozen, ok := request.Input()
	if !ok {
		t.Fatal("nested update input is absent")
	}
	got := frozen.Relations()
	if len(got) != 11 {
		t.Fatalf("nested operation count = %d", len(got))
	}
	for index, nested := range got {
		want := golem.MutationRelationAction(index + 1)
		if nested.Action() != want || nested.ParentModelID() != parent || nested.TargetModelID() != child {
			t.Fatalf("nested %d mismatch: action=%v parent=%x target=%x", index, nested.Action(), nested.ParentModelID(), nested.TargetModelID())
		}
		branches := nested.Branches()
		wantBranches := 1
		if want == golem.MutationRelationConnectOrCreate || want == golem.MutationRelationUpsert {
			wantBranches = 2
		}
		if len(branches) != wantBranches {
			t.Fatalf("action %v branch count = %d, want %d", want, len(branches), wantBranches)
		}
	}
	assertBranches(t, got[3].Branches(), []golem.MutationRelationBranch{golem.MutationRelationConnectOrCreateConnectBranch, golem.MutationRelationConnectOrCreateCreateBranch})
	assertBranches(t, got[8].Branches(), []golem.MutationRelationBranch{golem.MutationRelationUpsertCreateBranch, golem.MutationRelationUpsertUpdateBranch})
	if _, hasTarget := got[4].Branches()[0].Target(); hasTarget {
		t.Fatal("targetless disconnect acquired a target")
	}
	if _, hasTarget := got[5].Branches()[0].Target(); hasTarget {
		t.Fatal("empty set acquired a target")
	}
}

func TestNestedListOperationsPreserveP4GroupingSemantics(t *testing.T) {
	parent, child := modelID(14), modelID(15)
	childField := fieldID(16)
	rootTarget := frozenTarget(t, parent, fieldID(17), 1)
	first := frozenTarget(t, child, childField, 2)
	second := frozenTarget(t, child, childField, 3)
	create := func(value string) *Input {
		return &Input{Kind: CreateInput, Model: child, Scalars: []Scalar{{Field: childField, Operation: golem.MutationFieldCreate, Value: value}}}
	}
	data := &Input{Kind: UpdateInput, Model: parent, Relations: []Relation{
		relationValue(12, child, golem.MutationRelationConnect, RelationEntry{Target: &first}, RelationEntry{Target: &second}),
		relationValue(13, child, golem.MutationRelationConnectOrCreate, RelationEntry{Target: &first, Create: create("a")}, RelationEntry{Target: &second, Create: create("b")}),
	}}
	request, err := Lower(RootInput{Operation: Update, Model: parent, Target: &rootTarget, Data: data}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := request.Input()
	relations := frozen.Relations()
	if len(relations) != 3 {
		t.Fatalf("relation count = %d, want grouped connect plus two connect-or-create values", len(relations))
	}
	if relations[0].Action() != golem.MutationRelationConnect || len(relations[0].Branches()) != 2 {
		t.Fatalf("connect grouping mismatch: action=%v branches=%d", relations[0].Action(), len(relations[0].Branches()))
	}
	for index := 1; index < 3; index++ {
		if relations[index].Action() != golem.MutationRelationConnectOrCreate || len(relations[index].Branches()) != 2 {
			t.Fatalf("connect-or-create %d mismatch: action=%v branches=%d", index, relations[index].Action(), len(relations[index].Branches()))
		}
	}
}

func TestExplicitCreateNullOmissionLimitsAndInvalidShapes(t *testing.T) {
	model, field := modelID(20), fieldID(21)
	target := frozenTarget(t, model, field, 1)
	request, err := Lower(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldNull}}}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := request.Input()
	if fields := frozen.Fields(); len(fields) != 1 || fields[0].Operation() != golem.MutationFieldNull {
		t.Fatalf("explicit null not preserved: %#v", fields)
	} else if _, hasValue := fields[0].Value(); hasValue {
		t.Fatal("explicit null unexpectedly has an operand")
	}
	omitted, err := Lower(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	omittedInput, _ := omitted.Input()
	if len(omittedInput.Fields()) != 0 {
		t.Fatal("omitted field became an explicit operation")
	}

	badRoots := []RootInput{
		{Operation: Create, Model: model, Target: &target, Data: &Input{Kind: CreateInput, Model: model}},
		{Operation: Update, Model: model, Data: &Input{Kind: UpdateInput, Model: model}},
		{Operation: DeleteMany, Model: model},
		{Operation: UpdateMany, Model: model, Where: ptrPredicate(target.SelectorPredicate()), Data: &Input{Kind: UpdateManyInput, Model: model}, Selection: make([]readir.Selection, 1)},
	}
	for index, input := range badRoots {
		if _, err := Lower(input, Limits{}); err == nil || !strings.Contains(err.Error(), "P5_MUTATION_ROOT") {
			t.Fatalf("bad root %d error = %v", index, err)
		}
	}

	deep := &Input{Kind: CreateInput, Model: model}
	deep.Relations = []Relation{relationValue(1, model, golem.MutationRelationCreate, RelationEntry{Create: &Input{Kind: CreateInput, Model: model}})}
	if _, err := Lower(RootInput{Operation: Create, Model: model, Data: deep}, Limits{MaxInputDepth: 1}); err == nil || !strings.Contains(err.Error(), "P5_MUTATION_LIMIT") {
		t.Fatalf("depth error = %v", err)
	}
	tooMany := relationValue(2, model, golem.MutationRelationCreate, RelationEntry{Create: &Input{Kind: CreateInput, Model: model}}, RelationEntry{Create: &Input{Kind: CreateInput, Model: model}})
	if _, err := Lower(RootInput{Operation: Update, Model: model, Target: &target, Data: &Input{Kind: UpdateInput, Model: model, Relations: []Relation{tooMany}}}, Limits{MaxListItems: 1}); err == nil || !strings.Contains(err.Error(), "list items") {
		t.Fatalf("list error = %v", err)
	}
	if _, err := Lower(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldCreate, Value: "x"}}}}, Limits{MaxInputNodes: 1}); err == nil || !strings.Contains(err.Error(), "P5_MUTATION_LIMIT") {
		t.Fatalf("node error = %v", err)
	}
}

func relationValue(seed byte, target golem.ModelID, action golem.MutationRelationAction, entries ...RelationEntry) Relation {
	return Relation{Field: fieldID(seed + 30), Relation: relationID(seed + 60), TargetModel: target, Action: action, Entries: entries}
}

func assertBranches(t *testing.T, branches []golem.FrozenNestedMutationBranch, want []golem.MutationRelationBranch) {
	t.Helper()
	if len(branches) != len(want) {
		t.Fatalf("branch count %d != %d", len(branches), len(want))
	}
	for index := range want {
		if branches[index].Branch() != want[index] {
			t.Fatalf("branch %d = %v, want %v", index, branches[index].Branch(), want[index])
		}
	}
}

func frozenTarget(t *testing.T, model golem.ModelID, field golem.FieldID, value int32) golem.FrozenMutationTarget {
	t.Helper()
	target, err := golem.RuntimeMutationTargetFromIdentity(model, keyID(90), []golem.RuntimeSelectorValue{{Field: field, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func ptrPredicate(value golem.FrozenPredicate) *golem.FrozenPredicate { return &value }

func modelID(seed byte) golem.ModelID       { return golem.ModelID(fixed(seed)) }
func fieldID(seed byte) golem.FieldID       { return golem.FieldID(fixed(seed)) }
func relationID(seed byte) golem.RelationID { return golem.RelationID(fixed(seed)) }
func keyID(seed byte) golem.KeyID           { return golem.KeyID(fixed(seed)) }
func fixed(seed byte) [16]byte {
	var value [16]byte
	for index := range value {
		value[index] = seed + byte(index)
	}
	return value
}
