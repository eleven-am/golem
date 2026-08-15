package golem

import (
	"strings"
	"testing"
)

func TestExactNumbersCanonicalEnvelope(t *testing.T) {
	for input, want := range map[string]string{
		"0": "0", "-0": "0", "00042": "42", "-00042": "-42",
	} {
		got, err := ParseExactInteger(input)
		if err != nil || got.String() != want {
			t.Fatalf("integer %q = %q, %v; want %q", input, got.String(), err, want)
		}
	}
	for input, want := range map[string]string{
		"0": "0", "-0.000": "0", "00042.1200": "42.12", "-00042.1200": "-42.12",
	} {
		got, err := ParseExactDecimal(input)
		if err != nil || got.String() != want {
			t.Fatalf("decimal %q = %q, %v; want %q", input, got.String(), err, want)
		}
	}
	if _, err := ParseExactInteger(strings.Repeat("9", maxExactDigits+1)); err == nil {
		t.Fatal("oversized integer accepted")
	}
	if _, err := ParseExactDecimal("1." + strings.Repeat("0", 18) + "1"); err == nil {
		t.Fatal("overscale decimal accepted")
	}
	large := MustParseExactDecimal(strings.Repeat("9", 30) + ".25")
	if _, ok := large.Decimal(); ok {
		t.Fatal("out-of-storage-envelope exact decimal converted to Decimal")
	}
}

func TestTextMeasureHavingFunctionsFreezeExactModeAndOperator(t *testing.T) {
	model, field := ModelID{1}, FieldID{2}
	measure := GeneratedMeasure[struct{}, string, string](model, GeneratedOrderedField[struct{}, string](field), AggregateMinimum)
	dimension := GeneratedDimension[struct{}](model, GeneratedOrderedField[struct{}, string](field), false)
	tests := []struct {
		name, operator string
		mode           FrozenComparisonMode
		predicate      GroupPredicate[struct{}]
	}{
		{"contains sensitive", "contains", FrozenComparisonSensitive, TextMeasureContains(measure, "A", DefaultComparison())},
		{"starts insensitive", "startsWith", FrozenComparisonASCIIInsensitive, TextMeasureStartsWith(measure, "A", ASCIIInsensitive())},
		{"ends insensitive", "endsWith", FrozenComparisonASCIIInsensitive, TextMeasureEndsWith(measure, "A", ASCIIInsensitive())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := GeneratedGroupBy(model, GeneratedGroupDimensions[struct{}](dimension), GeneratedGroupHaving(test.predicate))
			frozen, err := RuntimeFreezeGroupRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			having, ok := frozen.Having()
			if !ok || having.Operator != test.operator || having.Mode != test.mode || having.Value != "A" {
				t.Fatalf("having=%#v", having)
			}
		})
	}
	invalid := TextMeasureContains(measure, "A", nil)
	if _, err := RuntimeFreezeGroupRequest(GeneratedGroupBy(model, GeneratedGroupDimensions[struct{}](dimension), GeneratedGroupHaving(invalid))); err == nil {
		t.Fatal("nil text comparison mode was accepted")
	}
}

func TestFrozenRequestRejectsForeignDuplicateAndMalformedTerms(t *testing.T) {
	model := ModelID{1}
	foreign := ModelID{2}
	field := FieldID{3}
	dimension := Dimension[struct{}, int64]{identity: aggregateIdentity{model: model, field: field}}
	measure := Measure[struct{}, ExactInteger]{identity: aggregateIdentity{model: model, field: field, operator: AggregateSum}}
	foreignMeasure := Measure[struct{}, ExactInteger]{identity: aggregateIdentity{model: foreign, field: field, operator: AggregateSum}}

	if _, err := RuntimeFreezeAggregateRequest(GeneratedAggregate(model, GeneratedAggregateSelect(measure, measure))); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_DUPLICATE") {
		t.Fatalf("duplicate measure error = %v", err)
	}
	if _, err := RuntimeFreezeAggregateRequest(GeneratedAggregate(model, GeneratedAggregateSelect(foreignMeasure))); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_MODEL") {
		t.Fatalf("foreign measure error = %v", err)
	}
	request := GeneratedGroupBy(model,
		GeneratedGroupDimensions(dimension),
		GeneratedGroupOrderBy(dimension.Asc(), dimension.Desc()),
	)
	if _, err := RuntimeFreezeGroupRequest(request); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_ORDER") {
		t.Fatalf("duplicate order error = %v", err)
	}
	otherDimension := Dimension[struct{}, int64]{identity: aggregateIdentity{model: model, field: FieldID{4}}}
	ungrouped := GeneratedGroupBy(model, GeneratedGroupDimensions(dimension), GeneratedGroupHaving(otherDimension.Eq(1)))
	if _, err := RuntimeFreezeGroupRequest(ungrouped); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_HAVING: dimension is not grouped") {
		t.Fatalf("ungrouped having dimension error = %v", err)
	}
	zero := AggregateRequest[struct{}]{model: model, measures: []aggregateIdentity{{model: model, operator: AggregateSum}}}
	if _, err := RuntimeFreezeAggregateRequest(zero); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_FIELD") {
		t.Fatalf("zero field error = %v", err)
	}
}

func TestFrozenRequestClonesMutableValuesAndRejectsForeignSchemaNodes(t *testing.T) {
	model := ModelID{1}
	field := FieldID{2}
	dimension := Dimension[struct{}, int64]{identity: aggregateIdentity{model: model, field: field}}
	measure := Measure[struct{}, ExactInteger]{identity: aggregateIdentity{model: model, field: field, operator: AggregateSum}}
	predicate := AndGroup(measure.GT(NewExactInteger(10)), dimension.Eq(7))
	request := GeneratedGroupBy(model, GeneratedGroupDimensions(dimension), GeneratedGroupHaving(predicate))
	frozen, err := RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	predicate.children[0] = measure.GT(NewExactInteger(999))
	having, ok := frozen.Having()
	if !ok || len(having.Children) != 2 || having.Children[0].Value.(ExactInteger).String() != "10" {
		t.Fatalf("frozen having was mutated: %#v", having)
	}
	having.Children[0].Value = NewExactInteger(500)
	again, _ := frozen.Having()
	if again.Children[0].Value.(ExactInteger).String() != "10" {
		t.Fatalf("Having returned internal tree: %#v", again)
	}
	relationPath := []RelationID{{4}}
	relationDimension := GeneratedRelationDimension[struct{}, string](model, "authorName", relationPath, FieldID{5}, true)
	relationRequest := GeneratedRelationGroupBy(model, GeneratedRelationGroupDimensions[struct{}](relationDimension))
	frozenRelation, err := RuntimeFreezeRelationGroupRequest(relationRequest)
	if err != nil {
		t.Fatal(err)
	}
	relationPath[0] = RelationID{9}
	dimensions := frozenRelation.Dimensions()
	if len(dimensions) != 1 || len(dimensions[0].RelationPath) != 1 || dimensions[0].RelationPath[0] != (RelationID{4}) {
		t.Fatalf("frozen relation path was mutated: %#v", dimensions)
	}
	dimensions[0].RelationPath[0] = RelationID{8}
	if frozenRelation.Dimensions()[0].RelationPath[0] != (RelationID{4}) {
		t.Fatal("Dimensions returned mutable relation-path storage")
	}
	foreign := ModelID{9}
	foreignMeasure := Measure[struct{}, ExactInteger]{identity: aggregateIdentity{model: foreign, field: field, operator: AggregateSum}}
	foreignRequest := GeneratedGroupBy(model, GeneratedGroupDimensions(dimension), GeneratedGroupHaving(foreignMeasure.GT(NewExactInteger(1))))
	if _, err := RuntimeFreezeGroupRequest(foreignRequest); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_MODEL") {
		t.Fatalf("foreign having term error = %v", err)
	}
}

func TestRelationRequestRejectsRelatedMeasuresAndForeignPaths(t *testing.T) {
	model := ModelID{1}
	forgedMeasure := Measure[struct{}, int64]{identity: aggregateIdentity{
		model:    model,
		field:    FieldID{2},
		operator: AggregateCountField,
		relation: "authorName",
	}}
	if _, err := RuntimeFreezeAggregateRequest(GeneratedAggregate(model, GeneratedAggregateSelect(forgedMeasure))); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_RELATION: measures over related models are not supported") {
		t.Fatalf("forged relation measure error = %v", err)
	}
	for _, test := range []struct {
		name string
		path []RelationID
	}{
		{name: "empty path"},
		{name: "zero path identity", path: []RelationID{{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forgedDimension := GeneratedRelationDimension[struct{}, string](model, "authorName", test.path, FieldID{3}, true)
			request := GeneratedRelationGroupBy(model, GeneratedRelationGroupDimensions[struct{}](forgedDimension))
			if _, err := RuntimeFreezeRelationGroupRequest(request); err == nil || !strings.Contains(err.Error(), "P6_ANALYTICS_RELATION") {
				t.Fatalf("forged relation path error = %v", err)
			}
		})
	}
}
