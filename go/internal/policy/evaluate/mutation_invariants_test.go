package evaluate

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// These tests own evaluator-side mutations whose provider witnesses live in
// the SQLite/PostgreSQL agreement suites.  Keeping the cases named after the
// contract makes a failed mutation identify the semantic invariant, while the
// assertions remain actual truth-table checks rather than an inventory map.
func TestNamedEvaluatorMutationInvariants(t *testing.T) {
	model, field := modelID(1), fieldID(1)

	t.Run("M12_non_ascii_uppercase_is_not_folded", func(t *testing.T) {
		typ := typeRef(t, ir.ValueString, false, nil, 0)
		row := record(t, model, valueField(t, field, stringValue(t, "AÉ")))
		assertEvaluate(t, scalar(t, model, field, typ, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, oneOperand(t, stringValue(t, "aÉ"))), row, true)
		assertEvaluate(t, scalar(t, model, field, typ, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, oneOperand(t, stringValue(t, "aé"))), row, false)
	})

	t.Run("M19_not_array_is_nor", func(t *testing.T) {
		truth, _ := ir.NewConstant(model, true)
		falsehood, _ := ir.NewConstant(model, false)
		or, _ := ir.NewLogical(model, ir.LogicalOr, []ir.Condition{truth, falsehood})
		nor, _ := ir.NewLogical(model, ir.LogicalNot, []ir.Condition{or})
		assertEvaluate(t, nor, record(t, model), false)

		notTruth, _ := ir.NewLogical(model, ir.LogicalNot, []ir.Condition{truth})
		notFalsehood, _ := ir.NewLogical(model, ir.LogicalNot, []ir.Condition{falsehood})
		wrongPerBranch, _ := ir.NewLogical(model, ir.LogicalOr, []ir.Condition{notTruth, notFalsehood})
		assertEvaluate(t, wrongPerBranch, record(t, model), true)
	})

	t.Run("M20_empty_combinator_constants", func(t *testing.T) {
		all, _ := ir.NewConstant(model, true)
		none, _ := ir.NewConstant(model, false)
		assertEvaluate(t, all, record(t, model), true)
		assertEvaluate(t, none, record(t, model), false)
	})

	t.Run("M21_adjacent_integer_above_2pow53", func(t *testing.T) {
		typ := typeRef(t, ir.ValueInt64, false, nil, 0)
		row := record(t, model, valueField(t, field, signed(t, ir.ValueInt64, 9_007_199_254_740_993)))
		assertEvaluate(t, scalar(t, model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, signed(t, ir.ValueInt64, 9_007_199_254_740_992))), row, false)
	})

	t.Run("M22_oracle_exact_decode_no_float64", func(t *testing.T) {
		jsonType := typeRef(t, ir.ValueJSON, false, nil, 0)
		path, _ := ir.NewJSONPath()
		actual := jsonValue(t, jsonNumber(t, false, "9007199254740993", 0))
		wanted := jsonValue(t, jsonNumber(t, false, "9007199254740992", 0))
		row := record(t, model, valueField(t, field, actual))
		assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONEqual, ir.ComparisonSensitive, path, oneOperand(t, wanted)), row, false)
	})

	t.Run("M23_bool_never_numeric", func(t *testing.T) {
		if valuesEqual(ir.BoolValue(true), signed(t, ir.ValueInt64, 1)) {
			t.Fatal("boolean true compared equal to numeric one")
		}
		if valuesEqual(ir.BoolValue(false), signed(t, ir.ValueInt64, 0)) {
			t.Fatal("boolean false compared equal to numeric zero")
		}
	})

	t.Run("M24_astral_vs_private_use_utf8_order", func(t *testing.T) {
		typ := typeRef(t, ir.ValueString, false, nil, 0)
		privateUse := "\uE000"
		astral := "\U00010000"
		row := record(t, model, valueField(t, field, stringValue(t, privateUse)))
		assertEvaluate(t, scalar(t, model, field, typ, ir.OperatorLessThan, ir.ComparisonSensitive, oneOperand(t, stringValue(t, astral))), row, true)
	})

	t.Run("M25_null_never_orders", func(t *testing.T) {
		typ := typeRef(t, ir.ValueInt64, true, nil, 0)
		row := record(t, model, NullField(field))
		value := oneOperand(t, signed(t, ir.ValueInt64, 1))
		for _, operatorID := range []ir.OperatorID{ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual} {
			assertEvaluate(t, scalar(t, model, field, typ, operatorID, ir.ComparisonSensitive, value), row, false)
		}
	})

	t.Run("M26_json_wrong_type_never_matches", func(t *testing.T) {
		jsonType := typeRef(t, ir.ValueJSON, false, nil, 0)
		path, _ := ir.NewJSONPath()
		numberRow := record(t, model, valueField(t, field, jsonValue(t, jsonNumber(t, false, "1", 0))))
		stringRow := record(t, model, valueField(t, field, jsonValue(t, jsonString(t, "x"))))
		stringOperand := oneOperand(t, jsonValue(t, jsonString(t, "x")))
		numberOperand := oneOperand(t, jsonValue(t, jsonNumber(t, false, "1", 0)))
		arrayOperand := oneOperand(t, jsonValue(t, jsonString(t, "x")))
		for _, operatorID := range []ir.OperatorID{ir.OperatorJSONEqual, ir.OperatorJSONNotEqual} {
			assertEvaluate(t, jsonCondition(t, model, field, jsonType, operatorID, ir.ComparisonSensitive, path, stringOperand), numberRow, false)
		}
		for _, operatorID := range []ir.OperatorID{ir.OperatorJSONLessThan, ir.OperatorJSONLessThanOrEqual, ir.OperatorJSONGreaterThan, ir.OperatorJSONGreaterThanOrEqual} {
			assertEvaluate(t, jsonCondition(t, model, field, jsonType, operatorID, ir.ComparisonSensitive, path, numberOperand), stringRow, false)
		}
		for _, operatorID := range []ir.OperatorID{ir.OperatorJSONStringContains, ir.OperatorJSONStringStartsWith, ir.OperatorJSONStringEndsWith} {
			assertEvaluate(t, jsonCondition(t, model, field, jsonType, operatorID, ir.ComparisonSensitive, path, stringOperand), numberRow, false)
		}
		for _, operatorID := range []ir.OperatorID{ir.OperatorJSONArrayContains, ir.OperatorJSONArrayStartsWith, ir.OperatorJSONArrayEndsWith} {
			assertEvaluate(t, jsonCondition(t, model, field, jsonType, operatorID, ir.ComparisonSensitive, path, arrayOperand), stringRow, false)
		}
	})
}
