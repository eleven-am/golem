package golem

import (
	"errors"
	"testing"
)

type mutationTestModel struct {
	ID    int64
	Name  string
	Bytes []byte
}

func TestMutationInputsAreModelTypedFrozenAndDetached(t *testing.T) {
	model := ModelID{1}
	nameID, bytesID := FieldID{2}, FieldID{3}
	name := GeneratedTextField[mutationTestModel, string](nameID)
	bytesField := GeneratedBytesField[mutationTestModel](bytesID)
	raw := []byte("before")

	create := GeneratedCreateInput(model,
		GeneratedCreateFieldValue(model, name, "name"),
		GeneratedCreateFieldValue(model, bytesField, raw),
	)
	raw[0] = 'x'
	frozen, err := RuntimeFreezeCreateInput(create)
	if err != nil {
		t.Fatal(err)
	}
	fields := frozen.Fields()
	if frozen.ModelID() != model || len(fields) != 2 || fields[0].Operation() != MutationFieldCreate {
		t.Fatalf("frozen create=%#v fields=%#v", frozen, fields)
	}
	first, ok := fields[1].Value()
	if !ok || string(first.([]byte)) != "before" {
		t.Fatalf("detached bytes=%q, %v", first, ok)
	}
	first.([]byte)[0] = 'z'
	second, _ := frozen.Fields()[1].Value()
	if string(second.([]byte)) != "before" {
		t.Fatalf("frozen value leaked mutable bytes: %q", second)
	}

	set := GeneratedSetFieldValue(model, name, "next")
	var _ UpdateValue[mutationTestModel] = set
	var _ UpdateManyValue[mutationTestModel] = set
	update, err := RuntimeFreezeUpdateInput(GeneratedUpdateInput[mutationTestModel](model, set))
	if err != nil || len(update.Fields()) != 1 || update.Fields()[0].Operation() != MutationFieldSet {
		t.Fatalf("frozen update=%#v err=%v", update, err)
	}
	updateMany, err := RuntimeFreezeUpdateManyInput(GeneratedUpdateManyInput[mutationTestModel](model,
		GeneratedIncrementFieldValue(model, GeneratedOrderedField[mutationTestModel, int64](FieldID{4}), int64(2)),
	))
	if err != nil || updateMany.Fields()[0].Operation() != MutationFieldIncrement {
		t.Fatalf("frozen updateMany=%#v err=%v", updateMany, err)
	}
}

func TestMutationInputFreezeRejectsZeroForeignAndDuplicateValues(t *testing.T) {
	model, other := ModelID{1}, ModelID{2}
	field := GeneratedTextField[mutationTestModel, string](FieldID{3})
	tests := []struct {
		name  string
		input CreateInput[mutationTestModel]
	}{
		{name: "zero", input: CreateInput[mutationTestModel]{}},
		{name: "foreign", input: GeneratedCreateInput(model, GeneratedCreateFieldValue(other, field, "value"))},
		{name: "duplicate", input: GeneratedCreateInput(model, GeneratedCreateFieldValue(model, field, "one"), GeneratedCreateFieldValue(model, field, "two"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RuntimeFreezeCreateInput(test.input)
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodeBadUserInput || failure.Operation != "create" {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

func TestMutationTargetPreservesSelectorAndGuardSeparately(t *testing.T) {
	model, key, idField, guardField := ModelID{1}, KeyID{2}, FieldID{3}, FieldID{4}
	selector := GeneratedUniqueSelectorValue[mutationTestModel](model, key, GeneratedSelectorComponent(idField, int64(9)))
	guard := GeneratedTextField[mutationTestModel, string](guardField).Eq("open")

	for _, target := range []MutationTarget[mutationTestModel]{selector, selector.And(guard)} {
		frozen, err := RuntimeFreezeMutationTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		metadata := frozen.Selector()
		if metadata.ModelID() != model || metadata.KeyID() != key || len(metadata.Fields()) != 1 || metadata.Fields()[0] != idField {
			t.Fatalf("selector metadata=%#v", metadata)
		}
		if frozen.SelectorPredicate().View().RootModelID() != model {
			t.Fatal("selector predicate lost root model")
		}
	}
	frozen, err := RuntimeFreezeMutationTarget(selector.And(guard))
	if err != nil {
		t.Fatal(err)
	}
	frozenGuard, ok := frozen.Guard()
	if !ok || frozenGuard.View().RootModelID() != model {
		t.Fatalf("guard=%#v ok=%v", frozenGuard, ok)
	}
}

func TestProjectionIsAdditiveReadOptionCapability(t *testing.T) {
	field := GeneratedTextField[mutationTestModel, string](FieldID{1})
	projection := Select[mutationTestModel](field)
	var _ Projection[mutationTestModel] = projection
	var _ ReadOption[mutationTestModel] = projection
	option := RuntimeProjectionReadOption(projection)
	if option == nil {
		t.Fatal("projection bridge returned nil")
	}
	if RuntimeProjectionReadOption[mutationTestModel](nil) != nil {
		t.Fatal("nil projection did not remain nil")
	}
}

func TestMutationHookFacadesAndResultsAreDetached(t *testing.T) {
	model, fieldID := ModelID{1}, FieldID{2}
	field := GeneratedTextField[mutationTestModel, string](fieldID)
	request := RuntimeCreateHookRequest(GeneratedCreateInput(model, GeneratedCreateFieldValue(model, field, "before")))
	if err := SetCreate(request, GeneratedCreateFieldCapability(model, field), "after"); err != nil {
		t.Fatal(err)
	}
	if err := SetCreate(request, GeneratedCreateFieldCapability(ModelID{9}, field), "foreign"); err == nil {
		t.Fatal("foreign generated create-field capability was accepted")
	}
	frozen, err := RuntimeFreezeCreateInput(request.Input())
	if err != nil {
		t.Fatal(err)
	}
	value, _ := frozen.Fields()[0].Value()
	if len(frozen.Fields()) != 1 || value.(string) != "after" {
		t.Fatalf("hook input=%#v", frozen.Fields())
	}
	if RuntimeBatchResult(7).Count() != 7 || RuntimeUpdateManyHookResult[mutationTestModel](8).Count() != 8 || RuntimeDeleteManyHookResult[mutationTestModel](9).Count() != 9 {
		t.Fatal("batch/hook counts drifted")
	}
	cause := errors.New("hook failed")
	failure := RuntimeAfterCommitFailure(HookCreate, model, cause)
	if failure.Operation() != HookCreate || failure.Model() != model || !errors.Is(failure.Cause(), cause) {
		t.Fatalf("after-commit failure=%#v", failure)
	}
}

func TestConflictIsStablePublicErrorCode(t *testing.T) {
	err := RuntimeReadError(CodeConflict, "upsert", ModelID{1}, FieldID{}, "mutation conflicted", errors.New("private provider cause"))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeConflict || failure.Error() != "CONFLICT: mutation conflicted" {
		t.Fatalf("conflict=%#v", err)
	}
}
