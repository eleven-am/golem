package evaluate

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

var testProviders = ir.PortableProviders()

func TestScalarNullComplementsOrderingTextAndASCIIMode(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	integer := typeRef(t, ir.ValueInt64, true, nil, 0)
	one := signed(t, ir.ValueInt64, 1)
	two := signed(t, ir.ValueInt64, 2)

	null := record(t, model, NullField(field))
	loaded := record(t, model, valueField(t, field, two))
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, one)), null, false)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorNotEqual, ir.ComparisonSensitive, oneOperand(t, one)), null, true)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorIn, ir.ComparisonSensitive, manyOperand(t, one, two)), null, false)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorNotIn, ir.ComparisonSensitive, manyOperand(t, one, two)), null, true)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorGreaterThan, ir.ComparisonSensitive, oneOperand(t, one)), loaded, true)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorLessThan, ir.ComparisonSensitive, oneOperand(t, one)), null, false)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorIsNull, ir.ComparisonSensitive, ir.NoOperand()), null, true)
	assertEvaluate(t, scalar(t, model, field, integer, ir.OperatorIsNotNull, ir.ComparisonSensitive, ir.NoOperand()), loaded, true)

	text := typeRef(t, ir.ValueString, false, nil, 0)
	stringRow := record(t, model, valueField(t, field, stringValue(t, "AÉ-Backend")))
	assertEvaluate(t, scalar(t, model, field, text, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, oneOperand(t, stringValue(t, "aÉ-backend"))), stringRow, true)
	assertEvaluate(t, scalar(t, model, field, text, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, oneOperand(t, stringValue(t, "aé-backend"))), stringRow, false)
	assertEvaluate(t, scalar(t, model, field, text, ir.OperatorContains, ir.ComparisonSensitive, oneOperand(t, stringValue(t, "%_Back"))), stringRow, false)
	assertEvaluate(t, scalar(t, model, field, text, ir.OperatorEndsWith, ir.ComparisonSensitive, oneOperand(t, stringValue(t, "Backend"))), stringRow, true)
}

func TestExactScalarValuesAndDecimalOrdering(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	decimalType, err := ir.NewTypeRef(ir.ValueDecimal, false, 18, 6, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := ir.NewDecimalValue(12, 1)
	right, _ := ir.NewDecimalValue(119, 2)
	row := record(t, model, valueField(t, field, left))
	assertEvaluate(t, scalar(t, model, field, decimalType, ir.OperatorGreaterThan, ir.ComparisonSensitive, oneOperand(t, right)), row, true)

	bytesType := typeRef(t, ir.ValueBytes, false, nil, 0)
	bytesRow := record(t, model, valueField(t, field, ir.BytesValue([]byte{0, 1, 2})))
	assertEvaluate(t, scalar(t, model, field, bytesType, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, ir.BytesValue([]byte{0, 1, 2}))), bytesRow, true)
	assertEvaluate(t, scalar(t, model, field, bytesType, ir.OperatorNotEqual, ir.ComparisonSensitive, oneOperand(t, ir.BytesValue([]byte{0, 1}))), bytesRow, true)
}

func TestScalarListEmptyNullMalformedAndOrderedEquality(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	elementType := typeRef(t, ir.ValueString, false, nil, 0)
	listType := typeRef(t, ir.ValueScalarList, true, &elementType, ir.CapabilityScalarListJSON)
	a, b := stringValue(t, "a"), stringValue(t, "b")

	empty := record(t, model, listField(t, field))
	null := record(t, model, NullField(field))
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListHasEvery, manyOperand(t)), empty, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListHasEvery, manyOperand(t)), null, false)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListHasSome, manyOperand(t)), empty, false)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListIsEmpty, ir.FlagOperand(true)), empty, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListIsNull, ir.NoOperand()), null, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListIsNotNull, ir.NoOperand()), empty, true)

	malformed := record(t, model, listField(t, field, validElement(t, a), InvalidListElement(), validElement(t, signed(t, ir.ValueInt64, 1))))
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListHas, oneOperand(t, b)), malformed, false)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListHas, oneOperand(t, a)), malformed, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListIsEmpty, ir.FlagOperand(false)), malformed, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListEqual, oneOperand(t, listValue(t, a, b))), malformed, false)

	ordered := record(t, model, valueField(t, field, listValue(t, a, b)))
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListEqual, oneOperand(t, listValue(t, a, b))), ordered, true)
	assertEvaluate(t, list(t, model, field, listType, ir.OperatorListEqual, oneOperand(t, listValue(t, b, a))), ordered, false)
}

func TestJSONAbsentNullWrongTypeExactNumberAndPaths(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	jsonType := typeRef(t, ir.ValueJSON, true, nil, 0)
	key, _ := ir.JSONKeySegment("slot")
	path, _ := ir.NewJSONPath(key)
	emptyPath, _ := ir.NewJSONPath()

	dbNull := record(t, model, NullField(field))
	jsonNull := record(t, model, valueField(t, field, jsonValue(t, ir.JSONNullValue())))
	missingObject := record(t, model, valueField(t, field, jsonValue(t, objectValue(t, "other", ir.JSONNullValue()))))
	nullObject := record(t, model, valueField(t, field, jsonValue(t, objectValue(t, "slot", ir.JSONNullValue()))))

	for _, test := range []struct {
		name   string
		row    Record
		path   ir.JSONPath
		kind   ir.JSONNullKind
		result bool
	}{
		{"db null", dbNull, emptyPath, ir.JSONDbNull, true},
		{"document null", jsonNull, emptyPath, ir.JSONDocumentNull, true},
		{"missing path", missingObject, path, ir.JSONDbNull, true},
		{"path json null", nullObject, path, ir.JSONDocumentNull, true},
		{"any missing", missingObject, path, ir.JSONAnyNull, true},
		{"db null does not mean json null", nullObject, path, ir.JSONDbNull, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			operand, _ := ir.JSONNullOperand(test.kind)
			assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONEqual, ir.ComparisonSensitive, test.path, operand), test.row, test.result)
		})
	}

	stringObject := record(t, model, valueField(t, field, jsonValue(t, objectValue(t, "slot", jsonString(t, "10")))))
	numberOperand := jsonValue(t, jsonNumber(t, false, "1", 1))
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONNotEqual, ir.ComparisonSensitive, path, oneOperand(t, numberOperand)), stringObject, false)

	largeA := jsonNumber(t, false, "9007199254740992", 0)
	largeB := jsonNumber(t, false, "9007199254740993", 0)
	largeRow := record(t, model, valueField(t, field, jsonValue(t, largeB)))
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONGreaterThan, ir.ComparisonSensitive, emptyPath, oneOperand(t, jsonValue(t, largeA))), largeRow, true)
}

func TestJSONStringAndArrayOperations(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	jsonType := typeRef(t, ir.ValueJSON, false, nil, 0)
	path, _ := ir.NewJSONPath()
	textRow := record(t, model, valueField(t, field, jsonValue(t, jsonString(t, "AÉBackend"))))
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONStringStartsWith, ir.ComparisonASCIIInsensitive, path, oneOperand(t, jsonValue(t, jsonString(t, "aÉ")))), textRow, true)
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONStringStartsWith, ir.ComparisonASCIIInsensitive, path, oneOperand(t, jsonValue(t, jsonString(t, "aé")))), textRow, false)

	one, two := jsonNumber(t, false, "1", 0), jsonNumber(t, false, "2", 0)
	nested, _ := ir.JSONArrayValue([]ir.JSONValue{one, two})
	array, _ := ir.JSONArrayValue([]ir.JSONValue{nested, jsonString(t, "tail")})
	row := record(t, model, valueField(t, field, jsonValue(t, array)))
	innerCandidate, _ := ir.JSONArrayValue([]ir.JSONValue{one})
	contained, _ := ir.JSONArrayValue([]ir.JSONValue{innerCandidate})
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONArrayContains, ir.ComparisonSensitive, path, oneOperand(t, jsonValue(t, contained))), row, true)
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONArrayStartsWith, ir.ComparisonSensitive, path, oneOperand(t, jsonValue(t, nested))), row, true)
	assertEvaluate(t, jsonCondition(t, model, field, jsonType, ir.OperatorJSONArrayEndsWith, ir.ComparisonSensitive, path, oneOperand(t, jsonValue(t, jsonString(t, "tail")))), row, true)
}

func TestRelationLoadedEmptyVacuityComplementsAndNestedRows(t *testing.T) {
	post, user := modelID(1), modelID(2)
	author, users, name := fieldID(1), fieldID(2), fieldID(3)
	child := scalar(t, user, name, typeRef(t, ir.ValueString, false, nil, 0), ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, stringValue(t, "Ada")))

	emptyOne := record(t, post, toOne(t, author, user))
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIsNull, nil), emptyOne, true)
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIsNotNull, nil), emptyOne, false)
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIs, &child), emptyOne, false)
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIsNot, &child), emptyOne, true)

	ada := record(t, user, valueField(t, name, stringValue(t, "Ada")))
	presentOne := record(t, post, toOne(t, author, user, ada))
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIs, &child), presentOne, true)
	assertEvaluate(t, relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIsNot, &child), presentOne, false)

	emptyMany := record(t, post, toMany(t, users, user))
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationSome, &child), emptyMany, false)
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationEvery, &child), emptyMany, true)
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationNone, &child), emptyMany, true)
	loadedMany := record(t, post, toMany(t, users, user, ada))
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationSome, &child), loadedMany, true)
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationEvery, &child), loadedMany, true)
	assertEvaluate(t, relation(t, post, users, user, ir.RelationToMany, ir.OperatorRelationNone, &child), loadedMany, false)
}

func TestMissingDependenciesRefuseBeforeBooleanShortCircuit(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	falseCondition, _ := ir.NewConstant(model, false)
	leaf := scalar(t, model, field, typeRef(t, ir.ValueInt64, false, nil, 0), ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, signed(t, ir.ValueInt64, 1)))
	condition, err := ir.NewLogical(model, ir.LogicalAnd, []ir.Condition{falseCondition, leaf})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Condition(condition, record(t, model), testProviders)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeMissing || failure.FieldID != field {
		t.Fatalf("error = %v, want %s for field %x", err, CodeMissing, field)
	}
}

func TestMalformedLoadedDependencyRefusesBeforeBooleanShortCircuit(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	trueCondition, _ := ir.NewConstant(model, true)
	leaf := scalar(t, model, field, typeRef(t, ir.ValueInt64, false, nil, 0), ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, signed(t, ir.ValueInt64, 1)))
	condition, err := ir.NewLogical(model, ir.LogicalOr, []ir.Condition{trueCondition, leaf})
	if err != nil {
		t.Fatal(err)
	}
	row := record(t, model, valueField(t, field, stringValue(t, "wrong type")))
	_, err = Condition(condition, row, testProviders)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeType || failure.FieldID != field {
		t.Fatalf("error = %v, want %s for field %x", err, CodeType, field)
	}
}

func TestMissingRelationAndNestedScalarDependenciesRefuse(t *testing.T) {
	post, user := modelID(1), modelID(2)
	author, name := fieldID(1), fieldID(2)
	child := scalar(t, user, name, typeRef(t, ir.ValueString, false, nil, 0), ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, stringValue(t, "Ada")))
	condition := relation(t, post, author, user, ir.RelationToOne, ir.OperatorRelationIs, &child)

	for _, row := range []Record{
		record(t, post),
		record(t, post, toOne(t, author, user, record(t, user))),
	} {
		_, err := Condition(condition, row, testProviders)
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeMissing {
			t.Fatalf("error = %v, want %s", err, CodeMissing)
		}
	}

	// A genuinely loaded absent to-one has no child record whose scalar
	// dependencies could be missing.
	assertEvaluate(t, condition, record(t, post, toOne(t, author, user)), false)
}

func TestScalarEqualityAndMembershipComplementsProperty(t *testing.T) {
	model, field := modelID(1), fieldID(1)
	typ := typeRef(t, ir.ValueInt64, true, nil, 0)
	for subject := int64(-12); subject <= 12; subject++ {
		row := record(t, model, valueField(t, field, signed(t, ir.ValueInt64, subject)))
		for operand := int64(-12); operand <= 12; operand++ {
			value := signed(t, ir.ValueInt64, operand)
			equal := scalar(t, model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(t, value))
			notEqual := scalar(t, model, field, typ, ir.OperatorNotEqual, ir.ComparisonSensitive, oneOperand(t, value))
			gotEqual, err := Condition(equal, row, testProviders)
			if err != nil {
				t.Fatal(err)
			}
			gotNotEqual, err := Condition(notEqual, row, testProviders)
			if err != nil {
				t.Fatal(err)
			}
			if gotEqual == gotNotEqual {
				t.Fatalf("eq/ne are not complements for %d and %d", subject, operand)
			}
		}
		values := []ir.Value{signed(t, ir.ValueInt64, subject-1), signed(t, ir.ValueInt64, subject+1)}
		in := scalar(t, model, field, typ, ir.OperatorIn, ir.ComparisonSensitive, manyOperand(t, values...))
		notIn := scalar(t, model, field, typ, ir.OperatorNotIn, ir.ComparisonSensitive, manyOperand(t, values...))
		gotIn, _ := Condition(in, row, testProviders)
		gotNotIn, _ := Condition(notIn, row, testProviders)
		if gotIn == gotNotIn {
			t.Fatalf("in/notIn are not complements for %d", subject)
		}
	}
}

func TestExactJSONNumberComparisonHandlesHugeExponentsWithoutExpansion(t *testing.T) {
	tests := []struct {
		left, right ir.JSONNumberValue
		want        int
	}{
		{number(t, false, "1", 2_000_000_000), number(t, false, "9", 1_999_999_999), 1},
		{number(t, true, "1", 2_000_000_000), number(t, true, "9", 1_999_999_999), -1},
		{number(t, false, "12", 1), number(t, false, "119", 0), 1},
		{number(t, false, "1", 0), number(t, false, "1", 0), 0},
	}
	for _, test := range tests {
		got := compareJSONNumber(test.left, test.right)
		if got != test.want {
			t.Fatalf("number comparison = %d, want %d", got, test.want)
		}
	}
}

func TestEveryRegistryOperatorHasEvaluatorDispatch(t *testing.T) {
	expected := map[ir.OperatorID]bool{
		ir.OperatorEqual: false, ir.OperatorNotEqual: true, ir.OperatorIn: true, ir.OperatorNotIn: false,
		ir.OperatorLessThan: false, ir.OperatorLessThanOrEqual: false, ir.OperatorGreaterThan: true, ir.OperatorGreaterThanOrEqual: true,
		ir.OperatorContains: true, ir.OperatorStartsWith: true, ir.OperatorEndsWith: false, ir.OperatorIsNull: false, ir.OperatorIsNotNull: true,
		ir.OperatorListEqual: true, ir.OperatorListHas: true, ir.OperatorListHasEvery: true, ir.OperatorListHasSome: true,
		ir.OperatorListIsEmpty: true, ir.OperatorListIsNull: false, ir.OperatorListIsNotNull: true,
		ir.OperatorJSONIsNull: false, ir.OperatorJSONIsNotNull: true, ir.OperatorJSONEqual: true, ir.OperatorJSONNotEqual: false,
		ir.OperatorJSONLessThan: false, ir.OperatorJSONLessThanOrEqual: false, ir.OperatorJSONGreaterThan: true, ir.OperatorJSONGreaterThanOrEqual: true,
		ir.OperatorJSONStringContains: true, ir.OperatorJSONStringStartsWith: true, ir.OperatorJSONStringEndsWith: true,
		ir.OperatorJSONArrayContains: true, ir.OperatorJSONArrayStartsWith: true, ir.OperatorJSONArrayEndsWith: true,
		ir.OperatorRelationIs: true, ir.OperatorRelationIsNot: false, ir.OperatorRelationIsNull: false, ir.OperatorRelationIsNotNull: true,
		ir.OperatorRelationSome: true, ir.OperatorRelationEvery: true, ir.OperatorRelationNone: false,
	}
	seen := make(map[ir.OperatorID]bool)
	for _, entry := range operator.Entries() {
		condition, row := representative(t, entry.ID())
		got, err := Condition(condition, row, testProviders)
		if err != nil {
			t.Fatalf("operator %d (%s): %v", entry.ID(), entry.Name(), err)
		}
		if want, exists := expected[entry.ID()]; !exists || got != want {
			t.Fatalf("operator %d (%s) = %v, want %v (listed=%v)", entry.ID(), entry.Name(), got, want, exists)
		}
		seen[entry.ID()] = true
	}
	if len(seen) != 41 || len(expected) != len(seen) {
		t.Fatalf("evaluator/expected inventory = %d/%d, want 41", len(seen), len(expected))
	}
}

func representative(t *testing.T, operatorID ir.OperatorID) (ir.Condition, Record) {
	model, target, field := modelID(1), modelID(2), fieldID(1)
	switch {
	case operatorID >= ir.OperatorEqual && operatorID <= ir.OperatorIsNotNull:
		kind, nullable, value, operand := ir.ValueInt64, false, signed(t, ir.ValueInt64, 2), oneOperand(t, signed(t, ir.ValueInt64, 1))
		mode := ir.ComparisonSensitive
		if operatorID >= ir.OperatorContains && operatorID <= ir.OperatorEndsWith {
			kind, value, operand = ir.ValueString, stringValue(t, "abc"), oneOperand(t, stringValue(t, "a"))
		}
		if operatorID == ir.OperatorIn || operatorID == ir.OperatorNotIn {
			operand = manyOperand(t, value)
		}
		if operatorID == ir.OperatorIsNull || operatorID == ir.OperatorIsNotNull {
			nullable, operand = true, ir.NoOperand()
		}
		typ := typeRef(t, kind, nullable, nil, 0)
		return scalar(t, model, field, typ, operatorID, mode, operand), record(t, model, valueField(t, field, value))
	case operatorID >= ir.OperatorListEqual && operatorID <= ir.OperatorListIsNotNull:
		elementType := typeRef(t, ir.ValueString, false, nil, 0)
		typ := typeRef(t, ir.ValueScalarList, true, &elementType, ir.CapabilityScalarListJSON)
		value := stringValue(t, "a")
		operand := oneOperand(t, value)
		switch operatorID {
		case ir.OperatorListEqual:
			operand = oneOperand(t, listValue(t, value))
		case ir.OperatorListHasEvery, ir.OperatorListHasSome:
			operand = manyOperand(t, value)
		case ir.OperatorListIsEmpty:
			operand = ir.FlagOperand(false)
		case ir.OperatorListIsNull, ir.OperatorListIsNotNull:
			operand = ir.NoOperand()
		}
		return list(t, model, field, typ, operatorID, operand), record(t, model, listField(t, field, validElement(t, value)))
	case operatorID >= ir.OperatorJSONIsNull && operatorID <= ir.OperatorJSONArrayEndsWith:
		typ := typeRef(t, ir.ValueJSON, true, nil, 0)
		path, _ := ir.NewJSONPath()
		value := jsonString(t, "abc")
		operand := oneOperand(t, jsonValue(t, value))
		switch {
		case operatorID == ir.OperatorJSONIsNull || operatorID == ir.OperatorJSONIsNotNull:
			operand = ir.NoOperand()
		case operatorID >= ir.OperatorJSONLessThan && operatorID <= ir.OperatorJSONGreaterThanOrEqual:
			value = jsonNumber(t, false, "2", 0)
			operand = oneOperand(t, jsonValue(t, jsonNumber(t, false, "1", 0)))
		case operatorID >= ir.OperatorJSONArrayContains:
			candidate := jsonString(t, "a")
			value, _ = ir.JSONArrayValue([]ir.JSONValue{candidate})
			operand = oneOperand(t, jsonValue(t, candidate))
		}
		return jsonCondition(t, model, field, typ, operatorID, ir.ComparisonSensitive, path, operand), record(t, model, valueField(t, field, jsonValue(t, value)))
	default:
		child, _ := ir.NewConstant(target, true)
		cardinality, childPointer := ir.RelationToOne, &child
		rows := []Record{record(t, target)}
		if operatorID == ir.OperatorRelationIsNull || operatorID == ir.OperatorRelationIsNotNull {
			childPointer = nil
		}
		if operatorID >= ir.OperatorRelationSome {
			cardinality = ir.RelationToMany
		}
		condition := relation(t, model, field, target, cardinality, operatorID, childPointer)
		if cardinality == ir.RelationToOne {
			return condition, record(t, model, toOne(t, field, target, rows...))
		}
		return condition, record(t, model, toMany(t, field, target, rows...))
	}
}

func scalar(t *testing.T, model ir.ModelID, field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, mode ir.ComparisonMode, operand ir.Operand) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: mode, Providers: testProviders})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(model, field, typ, operatorID, mode, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func list(t *testing.T, model ir.ModelID, field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, operand ir.Operand) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionList, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: testProviders})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewList(model, field, typ, operatorID, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func jsonCondition(t *testing.T, model ir.ModelID, field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, mode ir.ComparisonMode, path ir.JSONPath, operand ir.Operand) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionJSON, FieldType: typ, Operand: operand, Mode: mode, Path: path, Providers: testProviders})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewJSON(model, field, typ, operatorID, mode, path, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func relation(t *testing.T, model ir.ModelID, field ir.FieldID, target ir.ModelID, cardinality ir.RelationCardinality, operatorID ir.OperatorID, child *ir.Condition) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: testProviders})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewRelation(model, field, relationID(1), target, cardinality, operatorID, child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func typeRef(t *testing.T, kind ir.ValueKind, nullable bool, element *ir.TypeRef, capability ir.Capability) ir.TypeRef {
	t.Helper()
	typ, err := ir.NewTypeRef(kind, nullable, 0, 0, ir.EnumID{}, element, capability)
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func record(t *testing.T, model ir.ModelID, fields ...Field) Record {
	t.Helper()
	value, err := NewRecord(model, fields...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func valueField(t *testing.T, field ir.FieldID, value ir.Value) Field {
	t.Helper()
	result, err := ValueField(field, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func listField(t *testing.T, field ir.FieldID, elements ...ListElement) Field {
	t.Helper()
	result, err := ListField(field, elements...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func validElement(t *testing.T, value ir.Value) ListElement {
	t.Helper()
	result, err := ValidListElement(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func toOne(t *testing.T, field ir.FieldID, target ir.ModelID, rows ...Record) Field {
	t.Helper()
	result, err := ToOneField(field, target, rows...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func toMany(t *testing.T, field ir.FieldID, target ir.ModelID, rows ...Record) Field {
	t.Helper()
	result, err := ToManyField(field, target, rows...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func oneOperand(t *testing.T, value ir.Value) ir.Operand {
	t.Helper()
	result, err := ir.OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func manyOperand(t *testing.T, values ...ir.Value) ir.Operand {
	t.Helper()
	result, err := ir.ManyOperand(values)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func signed(t *testing.T, kind ir.ValueKind, value int64) ir.Value {
	t.Helper()
	result, err := ir.SignedValue(kind, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func stringValue(t *testing.T, value string) ir.Value {
	t.Helper()
	result, err := ir.StringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func listValue(t *testing.T, values ...ir.Value) ir.Value {
	t.Helper()
	result, err := ir.NewListValue(values)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func jsonValue(t *testing.T, value ir.JSONValue) ir.Value {
	t.Helper()
	result, err := ir.NewJSONValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func jsonString(t *testing.T, value string) ir.JSONValue {
	t.Helper()
	result, err := ir.JSONStringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func jsonNumber(t *testing.T, negative bool, coefficient string, exponent int32) ir.JSONValue {
	t.Helper()
	number := number(t, negative, coefficient, exponent)
	result, err := ir.JSONNumberValueOf(number)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func number(t *testing.T, negative bool, coefficient string, exponent int32) ir.JSONNumberValue {
	t.Helper()
	number, err := ir.NewJSONNumber(negative, []byte(coefficient), exponent)
	if err != nil {
		t.Fatal(err)
	}
	return number
}

func objectValue(t *testing.T, key string, value ir.JSONValue) ir.JSONValue {
	t.Helper()
	member, err := ir.NewJSONMember(key, value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ir.JSONObjectValue([]ir.JSONMember{member})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertEvaluate(t *testing.T, condition ir.Condition, row Record, want bool) {
	t.Helper()
	got, err := Condition(condition, row, testProviders)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("evaluation = %v, want %v", got, want)
	}
}

func modelID(value int) (result ir.ModelID)       { result[15] = byte(value); return }
func fieldID(value int) (result ir.FieldID)       { result[15] = byte(value); return }
func relationID(value int) (result ir.RelationID) { result[15] = byte(value); return }
