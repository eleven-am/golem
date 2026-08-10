package golem

import "testing"

type p6FuzzModel struct{}

// FuzzP6FrozenAnalyticsRequestRejectionAndClone checks both closed-shape
// rejection and the copy boundary returned by FrozenAnalyticsRequest getters.
func FuzzP6FrozenAnalyticsRequestRejectionAndClone(f *testing.F) {
	f.Add(uint8(0), 1, 0)
	f.Add(uint8(1), 0, 0)
	f.Add(uint8(2), -5, -1)
	f.Fuzz(func(t *testing.T, shape uint8, take, skip int) {
		model := ModelID{1}
		field := GeneratedOrderedField[p6FuzzModel, int64](FieldID{2})
		dimension := GeneratedDimension[p6FuzzModel](model, field, true)
		measure := GeneratedMeasure[p6FuzzModel, int64, ExactInteger](model, field, AggregateSum)
		options := []GroupOption[p6FuzzModel]{GeneratedGroupDimensions[p6FuzzModel](dimension)}
		switch shape % 4 {
		case 0:
			options = append(options, GeneratedGroupMeasures[p6FuzzModel](measure), GeneratedGroupHaving(measure.GT(NewExactInteger(1))))
		case 1:
			options = append(options, GeneratedGroupDimensions[p6FuzzModel](dimension))
		case 2:
			options = append(options, GeneratedGroupHaving(GeneratedDimension[p6FuzzModel](ModelID{9}, field, true).Eq(1)))
		case 3:
			options = append(options, GeneratedGroupOrderBy(dimension.Asc(), dimension.Desc()))
		}
		options = append(options, GeneratedGroupTake[p6FuzzModel](take), GeneratedGroupSkip[p6FuzzModel](skip))
		frozen, err := RuntimeFreezeGroupRequest(GeneratedGroupBy(model, options...))
		validShape := shape%4 == 0 && take != 0 && skip >= 0
		if !validShape {
			if err == nil {
				t.Fatalf("invalid analytics shape was accepted: shape=%d take=%d skip=%d", shape%4, take, skip)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid analytics shape was rejected: %v", err)
		}
		dimensions := frozen.Dimensions()
		if len(dimensions) != 1 {
			t.Fatalf("dimensions=%d", len(dimensions))
		}
		dimensions[0].RelationPath = append(dimensions[0].RelationPath, RelationID{7})
		if again := frozen.Dimensions(); len(again) != 1 || len(again[0].RelationPath) != 0 {
			t.Fatalf("Dimensions exposed mutable frozen storage: %#v", again)
		}
		having, present := frozen.Having()
		if !present {
			t.Fatal("valid request lost having")
		}
		having.Value = NewExactInteger(99)
		again, _ := frozen.Having()
		if value, ok := again.Value.(ExactInteger); !ok || value.Cmp(NewExactInteger(1)) != 0 {
			t.Fatalf("Having exposed mutable frozen storage: %#v", again)
		}
	})
}

// FuzzP6FrozenScopedRequestRejectionAndClone validates opaque scope ownership,
// paging shape, and clone-on-read for frozen predicates and slices.
func FuzzP6FrozenScopedRequestRejectionAndClone(f *testing.F) {
	f.Add(uint8(0), 1, 0, "seed")
	f.Add(uint8(1), 1, 0, "foreign")
	f.Add(uint8(2), 0, -1, "invalid")
	f.Fuzz(func(t *testing.T, shape uint8, take, skip int, value string) {
		if len(value) > 256 {
			t.Skip()
		}
		model := ModelID{3}
		root := GeneratedScope[p6FuzzModel](model)
		field := GeneratedScopedTextField(root, GeneratedTextField[p6FuzzModel, string](FieldID{4}))
		selected := ScopedSelection(field)
		query := From(root).Where(field.Eq(value)).Select(selected).Take(take).Skip(skip)
		if shape%3 == 1 {
			foreignRoot := GeneratedScope[p6FuzzModel](model)
			foreign := GeneratedScopedTextField(foreignRoot, GeneratedTextField[p6FuzzModel, string](FieldID{4}))
			query = From(root).Select(foreign)
		}
		frozen, err := RuntimeFreezeScopedQuery(query)
		validShape := shape%3 != 1 && take != 0 && skip >= 0
		if !validShape {
			if err == nil {
				t.Fatalf("invalid scoped shape was accepted: shape=%d take=%d skip=%d", shape%3, take, skip)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid scoped shape was rejected: %v", err)
		}
		selections := frozen.Selections()
		selections[0].Field = FieldID{9}
		if again := frozen.Selections(); len(again) != 1 || again[0].Field != (FieldID{4}) {
			t.Fatalf("Selections exposed mutable frozen storage: %#v", again)
		}
		where, present := frozen.Where()
		if !present || len(where.Values) != 1 {
			t.Fatalf("where=%#v present=%t", where, present)
		}
		where.Values[0] = "mutated"
		again, _ := frozen.Where()
		if got, ok := again.Values[0].(string); !ok || got != value {
			t.Fatalf("Where exposed mutable frozen storage: %#v", again)
		}
	})
}
