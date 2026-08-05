package golem

import (
	"bytes"
	"errors"
	"math"
	"sync"
	"testing"
)

type freezeModel struct{}
type freezeRelated struct{}
type freezeLabel string

var (
	freezeModelID       = ModelID{0x11}
	freezeRelatedID     = ModelID{0x12}
	freezeFieldID       = FieldID{0x21}
	freezeSecondFieldID = FieldID{0x22}
	freezeRelationField = FieldID{0x23}
	freezeRelationID    = RelationID{0x31}
	freezeDescriptor    = GeneratedModelDescriptor[freezeModel](freezeModelID, GeneratedDescriptorShape(nil, nil, nil, nil))
)

func TestPredicateFreezeBuildsTypedImmutableView(t *testing.T) {
	field := GeneratedOrderedField[freezeModel, int64](freezeFieldID)
	predicate := field.GTE(7).And(field.In(7, 8), field.Eq(0).Not())
	frozen, err := predicate.Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	view := frozen.View()
	if view.Version() != 1 || view.RootModelID() != freezeModelID || len(view.CanonicalBytes()) == 0 {
		t.Fatalf("unexpected frozen predicate header: version=%d model=%x bytes=%d", view.Version(), view.RootModelID(), len(view.CanonicalBytes()))
	}
	root := view.Root()
	if root.Kind() != FrozenConditionLogical || root.Operator() != FrozenOperatorAnd || root.Mode() != 0 || len(root.Children()) != 3 {
		t.Fatalf("unexpected logical root: kind=%d operator=%d mode=%d children=%d", root.Kind(), root.Operator(), root.Mode(), len(root.Children()))
	}
	first := root.Children()[0]
	if fieldID, ok := first.FieldID(); !ok || fieldID != freezeFieldID || first.Operator() != FrozenOperatorGTE || first.Mode() != FrozenComparisonSensitive {
		t.Fatalf("unexpected first leaf: field=%x ok=%t operator=%d mode=%d", fieldID, ok, first.Operator(), first.Mode())
	}
	one, ok := first.Operand().One()
	if !ok {
		t.Fatal("ordered comparison did not expose one operand")
	}
	if value, width, ok := one.Signed(); !ok || value != 7 || width != 64 {
		t.Fatalf("signed operand=(%d,%d,%t)", value, width, ok)
	}

	canonical := view.CanonicalBytes()
	canonical[0] ^= 0xff
	if bytes.Equal(canonical, view.CanonicalBytes()) {
		t.Fatal("canonical bytes getter exposed mutable storage")
	}
}

func TestPredicateFreezeCopiesMutableInputsAtConstruction(t *testing.T) {
	bytesField := GeneratedBytesField[freezeModel](freezeFieldID)
	first := []byte("first")
	second := []byte("second")
	values := [][]byte{first, second}
	predicate := bytesField.In(values...)
	first[0] = 'X'
	second[0] = 'Y'
	values[0] = []byte("replacement")

	frozen, err := predicate.Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	viewValues := frozen.View().Root().Operand().Many()
	if len(viewValues) != 2 {
		t.Fatalf("operand count=%d", len(viewValues))
	}
	gotFirst, _ := viewValues[0].Bytes()
	gotSecond, _ := viewValues[1].Bytes()
	if string(gotFirst) != "first" || string(gotSecond) != "second" {
		t.Fatalf("frozen bytes changed through caller inputs: %q, %q", gotFirst, gotSecond)
	}
	gotFirst[0] = 'Z'
	again, _ := frozen.View().Root().Operand().Many()[0].Bytes()
	if string(again) != "first" {
		t.Fatalf("value getter exposed mutable bytes: %q", again)
	}
}

func TestListAndNullableJSONFreezeKeepDistinctNodeAndOperandShapes(t *testing.T) {
	listField := GeneratedListField[freezeModel, string](freezeFieldID)
	values := List[string]{"first", "second"}
	listPredicate := listField.Eq(values)
	values[0] = "changed"
	listFrozen, err := listPredicate.Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	listRoot := listFrozen.View().Root()
	if listRoot.Kind() != FrozenConditionList || listRoot.Operator() != FrozenOperatorListEq || listRoot.Mode() != 0 || listRoot.Operand().Kind() != FrozenOperandMany {
		t.Fatalf("unexpected list node: kind=%d operator=%d mode=%d operand=%d", listRoot.Kind(), listRoot.Operator(), listRoot.Mode(), listRoot.Operand().Kind())
	}
	first, _ := listRoot.Operand().Many()[0].String()
	if first != "first" {
		t.Fatalf("list operand retained caller storage: %q", first)
	}

	jsonField := GeneratedNullableOpaqueField[freezeModel, JSON[any]](freezeSecondFieldID)
	jsonFrozen, err := jsonField.IsNull().Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	jsonRoot := jsonFrozen.View().Root()
	if jsonRoot.Kind() != FrozenConditionJSON || jsonRoot.Operator() != FrozenOperatorIsNull || jsonRoot.Mode() != FrozenComparisonSensitive || jsonRoot.Path().Present() {
		t.Fatalf("unexpected JSON presence node: kind=%d operator=%d mode=%d path=%t", jsonRoot.Kind(), jsonRoot.Operator(), jsonRoot.Mode(), jsonRoot.Path().Present())
	}
}

func TestFreezeNormalizesSignedFloatZeroAndRejectsInvalidUTF8(t *testing.T) {
	field := GeneratedOrderedField[freezeModel, float64](freezeFieldID)
	negative, err := field.Eq(math.Copysign(0, -1)).Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := field.Eq(0).Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(negative.CanonicalBytes(), positive.CanonicalBytes()) {
		t.Fatal("negative and positive float zero did not normalize identically")
	}
	text := GeneratedTextField[freezeModel, string](freezeSecondFieldID)
	_, err = text.Eq(string([]byte{0xff})).Freeze(freezeDescriptor)
	var failure *FreezeError
	if !errors.As(err, &failure) || failure.Code != FreezeInvalidValue {
		t.Fatalf("invalid UTF-8 error=%#v", err)
	}
}

func TestPredicateFreezeNormalizesOnlySafeLogicalShapes(t *testing.T) {
	field := GeneratedEqualField[freezeModel, bool](freezeFieldID)
	tests := []struct {
		name  string
		value Predicate[freezeModel]
		truth bool
	}{
		{"empty and", And[freezeModel](), true},
		{"empty or", Or[freezeModel](), false},
		{"not all", Not(All[freezeModel]()), false},
		{"absorbing and", field.Eq(true).And(None[freezeModel]()), false},
		{"absorbing or", field.Eq(false).Or(All[freezeModel]()), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, err := test.value.Freeze(freezeDescriptor)
			if err != nil {
				t.Fatal(err)
			}
			root := frozen.View().Root()
			truth, ok := root.Operand().Flag()
			if root.Kind() != FrozenConditionConstant || root.Mode() != 0 || !ok || truth != test.truth {
				t.Fatalf("normalized root kind=%d mode=%d truth=(%t,%t)", root.Kind(), root.Mode(), truth, ok)
			}
		})
	}
	leaf, err := And(field.Eq(true)).Freeze(freezeDescriptor)
	if err != nil || leaf.View().Root().Kind() != FrozenConditionScalar {
		t.Fatalf("single-child And was not collapsed: kind=%d err=%v", leaf.View().Root().Kind(), err)
	}
}

func TestPredicateFreezeRejectsMalformedAndNonFiniteValues(t *testing.T) {
	zeroDescriptor := GeneratedModelDescriptor[freezeModel](ModelID{}, GeneratedDescriptorShape(nil, nil, nil, nil))
	tests := []struct {
		name   string
		freeze func() error
		code   FreezeErrorCode
	}{
		{"zero model", func() error { _, err := All[freezeModel]().Freeze(zeroDescriptor); return err }, FreezeInvalidModel},
		{"zero predicate", func() error { _, err := (Predicate[freezeModel]{}).Freeze(freezeDescriptor); return err }, FreezeInvalidPredicate},
		{"zero field", func() error {
			_, err := GeneratedOrderedField[freezeModel, float64](FieldID{}).Eq(1).Freeze(freezeDescriptor)
			return err
		}, FreezeInvalidField},
		{"nan", func() error {
			_, err := GeneratedOrderedField[freezeModel, float64](freezeFieldID).Eq(math.NaN()).Freeze(freezeDescriptor)
			return err
		}, FreezeInvalidValue},
		{"infinity", func() error {
			_, err := GeneratedOrderedField[freezeModel, float64](freezeFieldID).LT(math.Inf(1)).Freeze(freezeDescriptor)
			return err
		}, FreezeInvalidValue},
		{"zero relation", func() error {
			_, err := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, RelationID{}).Is(All[freezeRelated]()).Freeze(freezeDescriptor)
			return err
		}, FreezeInvalidRelation},
		{"zero relation child", func() error {
			_, err := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, freezeRelationID).Is(Predicate[freezeRelated]{}).Freeze(freezeDescriptor)
			return err
		}, FreezeInvalidPredicate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.freeze()
			var failure *FreezeError
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("freeze error=%#v; want code %s", err, test.code)
			}
		})
	}
}

func TestRelationFreezePreservesEndpointAndNestedPredicate(t *testing.T) {
	relation := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, freezeRelationID)
	relatedField := GeneratedEqualField[freezeRelated, freezeLabel](freezeSecondFieldID)
	frozen, err := relation.Is(relatedField.Eq("visible")).Freeze(freezeDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	root := frozen.View().Root()
	reference, ok := root.Relation()
	if !ok || reference.FieldID() != freezeRelationField || reference.RelationID() != freezeRelationID {
		t.Fatalf("relation reference=%#v ok=%t", reference, ok)
	}
	if root.Operator() != FrozenOperatorRelationIs || len(root.Children()) != 1 || root.Children()[0].Operator() != FrozenOperatorEq {
		t.Fatalf("unexpected relation tree: operator=%d children=%d", root.Operator(), len(root.Children()))
	}
	if root.Mode() != 0 {
		t.Fatalf("relation unexpectedly carries comparison mode %d", root.Mode())
	}
}

func TestRulesFreezePreservesOrderEffectsFieldsAndSnapshotIsolation(t *testing.T) {
	rules := NewRules[freezeModel]()
	firstField := GeneratedEqualField[freezeModel, bool](freezeFieldID)
	secondField := GeneratedTextField[freezeModel, string](freezeSecondFieldID)
	rules.CanRead(All[freezeModel]())
	rules.CannotReadFields(All[freezeModel](), firstField, secondField, firstField)
	rules.CanUpdate(firstField.Eq(true))

	frozen, err := rules.Freeze(freezeModelID)
	if err != nil {
		t.Fatal(err)
	}
	rules.CannotDelete(All[freezeModel]())
	view := frozen.View()
	if view.ModelID() != freezeModelID || len(view.Rules()) != 3 {
		t.Fatalf("frozen policy model=%x rules=%d", view.ModelID(), len(view.Rules()))
	}
	wantActions := []FrozenAction{FrozenActionRead, FrozenActionRead, FrozenActionUpdate}
	wantEffects := []FrozenEffect{FrozenEffectGrant, FrozenEffectDeny, FrozenEffectGrant}
	for index, rule := range view.Rules() {
		if rule.Position() != uint32(index) || rule.Action() != wantActions[index] || rule.Effect() != wantEffects[index] || rule.ModelID() != freezeModelID {
			t.Fatalf("rule %d metadata=(position=%d action=%d effect=%d model=%x)", index, rule.Position(), rule.Action(), rule.Effect(), rule.ModelID())
		}
	}
	if !view.Rules()[0].IsUnconditional() {
		t.Fatal("root All rule was not frozen as unconditional")
	}
	if condition, ok := view.Rules()[0].Condition(); ok || condition != nil {
		t.Fatal("unconditional rule retained a condition")
	}
	fields, modelWide := view.Rules()[1].Fields()
	if modelWide || len(fields) != 2 || fields[0] != freezeFieldID || fields[1] != freezeSecondFieldID {
		t.Fatalf("field rule fields=%x modelWide=%t", fields, modelWide)
	}
	fields[0] = FieldID{0xff}
	again, _ := view.Rules()[1].Fields()
	if again[0] != freezeFieldID {
		t.Fatal("field getter exposed mutable policy storage")
	}
	if _, modelWide := view.Rules()[2].Fields(); !modelWide {
		t.Fatal("model rule was not reported model-wide")
	}
	second, err := rules.Freeze(freezeModelID)
	if err != nil || len(second.View().Rules()) != 4 || len(frozen.View().Rules()) != 3 {
		t.Fatalf("builder mutation leaked across snapshots: first=%d second=%d err=%v", len(frozen.View().Rules()), len(second.View().Rules()), err)
	}
}

func TestRulesFreezeReportsRulePositionAndRejectsZeroFieldIdentity(t *testing.T) {
	rules := NewRules[freezeModel]()
	rules.CanRead(All[freezeModel]())
	rules.CanUpdate(Predicate[freezeModel]{})
	_, err := rules.Freeze(freezeModelID)
	var failure *FreezeError
	if !errors.As(err, &failure) || failure.Code != FreezeInvalidPredicate || !failure.HasRule || failure.RulePosition != 1 {
		t.Fatalf("zero predicate rule error=%#v", err)
	}

	fieldRules := NewRules[freezeModel]()
	fieldRules.CanReadFields(All[freezeModel](), GeneratedEqualField[freezeModel, bool](FieldID{}))
	_, err = fieldRules.Freeze(freezeModelID)
	if !errors.As(err, &failure) || failure.Code != FreezeInvalidField || !failure.HasRule || failure.RulePosition != 0 {
		t.Fatalf("zero field rule error=%#v", err)
	}
}

func TestRulesFreezeIsDeterministicAndConcurrentViewsAreRaceFree(t *testing.T) {
	rules := NewRules[freezeModel]()
	field := GeneratedBytesField[freezeModel](freezeFieldID)
	rules.CanRead(field.Eq([]byte("value")))
	first, err := rules.Freeze(freezeModelID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rules.Freeze(freezeModelID)
	if err != nil || !bytes.Equal(first.CanonicalBytes(), second.CanonicalBytes()) {
		t.Fatalf("repeat freeze differs: err=%v", err)
	}

	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				view := first.View()
				_ = view.CanonicalBytes()
				values, _ := view.Rules()[0].Condition()
				operand, _ := values.Root().Operand().One()
				data, _ := operand.Bytes()
				if string(data) != "value" {
					t.Errorf("concurrent view read=%q", data)
					return
				}
			}
		}()
	}
	wait.Wait()
}
