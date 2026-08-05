package operator

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func mustProviders(t *testing.T) ir.ProviderSet {
	t.Helper()
	set, err := ir.NewProviderSet(ir.ProviderSQLite, ir.ProviderPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	return set
}
func mustType(t *testing.T, kind ir.ValueKind, nullable bool, element *ir.TypeRef, capability ir.Capability) ir.TypeRef {
	t.Helper()
	value, err := ir.NewTypeRef(kind, nullable, 0, 0, ir.EnumID{}, element, capability)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustOne(t *testing.T, value ir.Value) ir.Operand {
	t.Helper()
	operand, err := ir.OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}

func TestRegistryIsClosedUniqueAndPortableAgreementProved(t *testing.T) {
	entries := Entries()
	if len(entries) != 41 {
		t.Fatalf("entry count = %d", len(entries))
	}
	seen := map[ir.OperatorID]bool{}
	var previous ir.OperatorID
	for index, entry := range entries {
		if entry.ID() == 0 || entry.Name() == "" || !entry.SQLIsTwoValued() {
			t.Fatalf("invalid entry %#v", entry)
		}
		if seen[entry.ID()] {
			t.Fatalf("duplicate ID %d", entry.ID())
		}
		seen[entry.ID()] = true
		if index > 0 && entry.ID() <= previous {
			t.Fatal("entries are not ID ordered")
		}
		previous = entry.ID()
		if entry.AgreementStatus() != AgreementProved || entry.AgreementProviders() != mustProviders(t) {
			t.Fatalf("%s is not agreement-proved for the portable providers", entry.Name())
		}
		if err := RequireAgreement(entry.ID(), mustProviders(t)); err != nil {
			t.Fatalf("%s failed the proved agreement gate: %v", entry.Name(), err)
		}
	}
	if _, ok := Lookup(99); ok {
		t.Fatal("unknown operator resolved")
	}
}

func TestScalarShapeAndConditionalCapabilities(t *testing.T) {
	providers := mustProviders(t)
	integer := mustType(t, ir.ValueInt64, false, nil, 0)
	number, _ := ir.SignedValue(ir.ValueInt64, 7)
	requirements, err := ValidateShape(ir.OperatorEqual, Shape{Node: ir.ConditionScalar, FieldType: integer, Operand: mustOne(t, number), Mode: ir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 0 {
		t.Fatalf("integer equality requirements = %v", requirements)
	}
	text := mustType(t, ir.ValueString, false, nil, 0)
	stringValue, _ := ir.StringValue("A")
	requirements, err = ValidateShape(ir.OperatorStartsWith, Shape{Node: ir.ConditionScalar, FieldType: text, Operand: mustOne(t, stringValue), Mode: ir.ComparisonASCIIInsensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 2 || requirements[0].Capability() != ir.CapabilityBinaryText || requirements[1].Capability() != ir.CapabilityASCIIInsensitiveText {
		t.Fatalf("text requirements = %v", requirements)
	}
	if _, err := ValidateShape(ir.OperatorEqual, Shape{Node: ir.ConditionScalar, FieldType: integer, Operand: mustOne(t, number), Mode: ir.ComparisonASCIIInsensitive, Providers: providers}); err == nil {
		t.Fatal("insensitive numeric equality accepted")
	}
}

func TestPresenceAndOperandKindsFailClosed(t *testing.T) {
	providers := mustProviders(t)
	required := mustType(t, ir.ValueInt32, false, nil, 0)
	nullable := mustType(t, ir.ValueInt32, true, nil, 0)
	shape := Shape{Node: ir.ConditionScalar, FieldType: required, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: providers}
	if _, err := ValidateShape(ir.OperatorIsNull, shape); err == nil {
		t.Fatal("presence accepted required field")
	}
	shape.FieldType = nullable
	if _, err := ValidateShape(ir.OperatorIsNull, shape); err != nil {
		t.Fatal(err)
	}
	value, _ := ir.SignedValue(ir.ValueInt32, 1)
	shape.Operand = mustOne(t, value)
	if _, err := ValidateShape(ir.OperatorIsNull, shape); err == nil {
		t.Fatal("presence accepted value operand")
	}
}

func TestListShapeChecksElementKindsAndStorageRequirement(t *testing.T) {
	providers := mustProviders(t)
	element := mustType(t, ir.ValueString, false, nil, 0)
	list := mustType(t, ir.ValueScalarList, true, &element, ir.CapabilityScalarListJSON)
	value, _ := ir.StringValue("go")
	requirements, err := ValidateShape(ir.OperatorListHas, Shape{Node: ir.ConditionList, FieldType: list, Operand: mustOne(t, value), Mode: ir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 1 || requirements[0].Capability() != ir.CapabilityScalarListJSON {
		t.Fatalf("list requirements = %v", requirements)
	}
	wrong, _ := ir.SignedValue(ir.ValueInt64, 1)
	if _, err := ValidateShape(ir.OperatorListHas, Shape{Node: ir.ConditionList, FieldType: list, Operand: mustOne(t, wrong), Mode: ir.ComparisonSensitive, Providers: providers}); err == nil {
		t.Fatal("wrong list element accepted")
	}
	entry, _ := Lookup(ir.OperatorListHasEvery)
	if entry.EmptyMeaning() != EmptyTrueWhenPresent {
		t.Fatalf("hasEvery empty meaning = %d", entry.EmptyMeaning())
	}
}

func TestJSONGuardsAndModeCells(t *testing.T) {
	providers := mustProviders(t)
	jsonType := mustType(t, ir.ValueJSON, true, nil, 0)
	boolean, err := ir.NewJSONValue(ir.JSONBoolValue(true))
	if err != nil {
		t.Fatal(err)
	}
	shape := Shape{Node: ir.ConditionJSON, FieldType: jsonType, Operand: mustOne(t, boolean), Mode: ir.ComparisonSensitive, Providers: providers}
	if _, err := ValidateShape(ir.OperatorJSONLessThan, shape); err == nil {
		t.Fatal("JSON bool ordering accepted")
	}
	stringJSON, _ := ir.JSONStringValue("a")
	wrapped, _ := ir.NewJSONValue(stringJSON)
	shape.Operand = mustOne(t, wrapped)
	if _, err := ValidateShape(ir.OperatorJSONStringContains, shape); err != nil {
		t.Fatal(err)
	}
	shape.Mode = ir.ComparisonASCIIInsensitive
	if _, err := ValidateShape(ir.OperatorJSONStringContains, shape); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateShape(ir.OperatorJSONEqual, shape); err == nil {
		t.Fatal("insensitive JSON equality accepted")
	}
	shape.Mode = ir.ComparisonSensitive
	shape.Path, _ = ir.NewJSONPath(ir.JSONIndexSegment(0))
	shape.Operand = ir.NoOperand()
	if _, err := ValidateShape(ir.OperatorJSONIsNull, shape); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("JSON column presence path error = %v", err)
	}
}

func TestRelationShapeDeclaresQuantifierAndCorrelationSemantics(t *testing.T) {
	providers := mustProviders(t)
	shape := Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: ir.RelationToMany, HasChild: true, Providers: providers}
	requirements, err := ValidateShape(ir.OperatorRelationEvery, shape)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 1 || requirements[0].Capability() != ir.CapabilityRelationCorrelation {
		t.Fatalf("relation requirements = %v", requirements)
	}
	entry, _ := Lookup(ir.OperatorRelationEvery)
	if entry.EmptyMeaning() != EmptyTrue {
		t.Fatalf("every empty meaning = %d", entry.EmptyMeaning())
	}
	shape.HasChild = false
	if _, err := ValidateShape(ir.OperatorRelationEvery, shape); err == nil {
		t.Fatal("every without child accepted")
	}
	shape.HasChild = true
	shape.Cardinality = ir.RelationToOne
	if _, err := ValidateShape(ir.OperatorRelationEvery, shape); err == nil {
		t.Fatal("every on to-one accepted")
	}
}

func TestValidateConditionRejectsWrongOperatorCellAndRequirements(t *testing.T) {
	providers := mustProviders(t)
	var model ir.ModelID
	model[15] = 1
	var field ir.FieldID
	field[15] = 1
	typ := mustType(t, ir.ValueString, false, nil, 0)
	value, _ := ir.StringValue("a")
	operand := mustOne(t, value)
	wrongCell, err := ir.NewScalar(model, field, typ, ir.OperatorRelationIs, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCondition(wrongCell, providers); err == nil {
		t.Fatal("relation operator accepted in scalar node")
	}
	requirements, err := ValidateShape(ir.OperatorEqual, Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCondition(valid, providers); err != nil {
		t.Fatal(err)
	}
	missing, err := ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCondition(missing, providers); err == nil {
		t.Fatal("missing derived requirement accepted")
	}
}

func TestNamedValidationMutations(t *testing.T) {
	providers := mustProviders(t)

	t.Run("M23_bool_never_numeric", func(t *testing.T) {
		boolean := mustType(t, ir.ValueBool, false, nil, 0)
		numeric, _ := ir.SignedValue(ir.ValueInt64, 1)
		if _, err := ValidateShape(ir.OperatorEqual, Shape{Node: ir.ConditionScalar, FieldType: boolean, Operand: mustOne(t, numeric), Mode: ir.ComparisonSensitive, Providers: providers}); err == nil {
			t.Fatal("boolean equality accepted a numeric operand")
		}
	})

	t.Run("M28_comparison_mode_rejected_on_non_text_operation", func(t *testing.T) {
		integer := mustType(t, ir.ValueInt64, false, nil, 0)
		numeric, _ := ir.SignedValue(ir.ValueInt64, 1)
		if _, err := ValidateShape(ir.OperatorEqual, Shape{Node: ir.ConditionScalar, FieldType: integer, Operand: mustOne(t, numeric), Mode: ir.ComparisonASCIIInsensitive, Providers: providers}); err == nil {
			t.Fatal("ASCII-insensitive mode accepted on numeric equality")
		}

		jsonType := mustType(t, ir.ValueJSON, false, nil, 0)
		jsonString, _ := ir.JSONStringValue("x")
		jsonValue, _ := ir.NewJSONValue(jsonString)
		if _, err := ValidateShape(ir.OperatorJSONEqual, Shape{Node: ir.ConditionJSON, FieldType: jsonType, Operand: mustOne(t, jsonValue), Mode: ir.ComparisonASCIIInsensitive, Providers: providers}); err == nil {
			t.Fatal("ASCII-insensitive mode accepted on JSON structural equality")
		}
	})
}
