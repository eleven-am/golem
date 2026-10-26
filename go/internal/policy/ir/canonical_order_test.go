package ir_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func orderModel(value byte) ir.ModelID {
	var id ir.ModelID
	id[15] = value
	return id
}

func orderField(value byte) ir.FieldID {
	var id ir.FieldID
	id[15] = value
	return id
}

func orderLeaf(tb testing.TB, model ir.ModelID, field byte, value int64) ir.Condition {
	tb.Helper()
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		tb.Fatal(err)
	}
	signed, err := ir.SignedValue(ir.ValueInt64, value)
	if err != nil {
		tb.Fatal(err)
	}
	operand, err := ir.OneOperand(signed)
	if err != nil {
		tb.Fatal(err)
	}
	leaf, err := ir.NewScalar(model, orderField(field), typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return leaf
}

func orderLogical(tb testing.TB, model ir.ModelID, op ir.LogicalOperator, children ...ir.Condition) ir.Condition {
	tb.Helper()
	result, err := ir.NewLogical(model, op, children)
	if err != nil {
		tb.Fatal(err)
	}
	return result
}

func orderJSON(tb testing.TB, model ir.ModelID) ir.Condition {
	tb.Helper()
	typ, err := ir.NewTypeRef(ir.ValueJSON, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		tb.Fatal(err)
	}
	number, err := ir.NewJSONNumber(false, []byte("42"), 0)
	if err != nil {
		tb.Fatal(err)
	}
	numberJSON, err := ir.JSONNumberValueOf(number)
	if err != nil {
		tb.Fatal(err)
	}
	wrapped, err := ir.NewJSONValue(numberJSON)
	if err != nil {
		tb.Fatal(err)
	}
	operand, err := ir.OneOperand(wrapped)
	if err != nil {
		tb.Fatal(err)
	}
	key, err := ir.JSONKeySegment("count")
	if err != nil {
		tb.Fatal(err)
	}
	path, err := ir.NewJSONPath(key)
	if err != nil {
		tb.Fatal(err)
	}
	requirements, err := operator.ValidateShape(ir.OperatorJSONEqual, operator.Shape{Node: ir.ConditionJSON, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Path: path, Providers: ir.PortableProviders()})
	if err != nil {
		tb.Fatal(err)
	}
	condition, err := ir.NewJSON(model, orderField(9), typ, ir.OperatorJSONEqual, ir.ComparisonSensitive, path, operand, requirements)
	if err != nil {
		tb.Fatal(err)
	}
	return condition
}

func orderRelation(tb testing.TB, model, target ir.ModelID, child ir.Condition) ir.Condition {
	tb.Helper()
	requirements, err := operator.ValidateShape(ir.OperatorRelationIs, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: ir.RelationToOne, HasChild: true, Providers: ir.PortableProviders()})
	if err != nil {
		tb.Fatal(err)
	}
	var relation ir.RelationID
	relation[15] = 7
	condition, err := ir.NewRelation(model, orderField(8), relation, target, ir.RelationToOne, ir.OperatorRelationIs, &child, requirements)
	if err != nil {
		tb.Fatal(err)
	}
	return condition
}

func encodedOrder(tb testing.TB, condition ir.Condition) []byte {
	tb.Helper()
	encoded, err := ir.CanonicalCondition(condition)
	if err != nil {
		tb.Fatal(err)
	}
	return encoded
}

func TestCanonicalKeyCompareMatchesEncodedBytesAcrossModelsSubtreesAndNegation(t *testing.T) {
	first, second := orderModel(1), orderModel(2)
	a, b := orderLeaf(t, first, 1, 1), orderLeaf(t, first, 1, 2)
	c, d := orderLeaf(t, first, 2, 3), orderLeaf(t, first, 2, 4)
	shared := orderLogical(t, first, ir.LogicalOr, a, b)
	candidates := map[string]ir.Condition{
		"a":                  a,
		"b":                  b,
		"c":                  c,
		"d":                  d,
		"and(or(a,b),c)":     orderLogical(t, first, ir.LogicalAnd, shared, c),
		"and(or(a,b),d)":     orderLogical(t, first, ir.LogicalAnd, shared, d),
		"not(a)":             orderLogical(t, first, ir.LogicalNot, a),
		"not(b)":             orderLogical(t, first, ir.LogicalNot, b),
		"not(or(a,b))":       orderLogical(t, first, ir.LogicalNot, shared),
		"second.a":           orderLeaf(t, second, 1, 1),
		"second.and(a,b)":    orderLogical(t, second, ir.LogicalAnd, orderLeaf(t, second, 1, 1), orderLeaf(t, second, 1, 2)),
		"second.not(a)":      orderLogical(t, second, ir.LogicalNot, orderLeaf(t, second, 1, 1)),
		"relation(second.a)": orderRelation(t, first, second, orderLeaf(t, second, 1, 1)),
		"json":               orderJSON(t, first),
	}
	keys := make(map[string]ir.CanonicalKey, len(candidates))
	for name, condition := range candidates {
		key, err := ir.CanonicalKeyOf(condition)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		keys[name] = key
	}
	for leftName, left := range candidates {
		for rightName, right := range candidates {
			want := sign(bytes.Compare(encodedOrder(t, left), encodedOrder(t, right)))
			if got := sign(keys[leftName].Compare(keys[rightName])); got != want {
				t.Errorf("Compare(%s, %s)=%d, encoded byte order=%d", leftName, rightName, got, want)
			}
		}
	}
	if keys["and(or(a,b),c)"].Compare(keys["and(or(a,b),d)"]) == 0 {
		t.Fatal("a difference after a completed shared subtree compared equal")
	}
}

const representativeConditionSHA256 = "b18012f93c82236c84eec0495cfae1c6d71eec1c12d1c04fecc46ab9ba3a7e8a"

func TestCanonicalEncodingLayoutIsPinned(t *testing.T) {
	root, target := orderModel(1), orderModel(2)
	tree := orderLogical(t, root, ir.LogicalAnd,
		orderLogical(t, root, ir.LogicalOr, orderLeaf(t, root, 1, 1), orderJSON(t, root)),
		orderLogical(t, root, ir.LogicalNot, orderRelation(t, root, target, orderLeaf(t, target, 3, 5))),
	)
	digest := sha256.Sum256(encodedOrder(t, tree))
	if got := hex.EncodeToString(digest[:]); got != representativeConditionSHA256 {
		t.Fatalf("canonical encoding layout changed: sha256=%s", got)
	}
}
