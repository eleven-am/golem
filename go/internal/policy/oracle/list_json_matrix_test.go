package oracle

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

const (
	scalarListMatrixTable = "scalar_list_matrix"
	jsonMatrixTable       = "json_matrix"
)

type listJSONMatrix struct {
	model  ModelSpec
	fields []FieldSpec
	rows   []Row
	probes []Probe
}

type listFieldCase struct {
	kindCase scalarKindCase
	field    FieldSpec
}

func scalarListMatrixFixture() listJSONMatrix {
	model := modelID(8)
	kinds := scalarKindCases()
	fields := make([]FieldSpec, 0, 26)
	fieldCases := make([]listFieldCase, 0, 26)
	fieldIndex := byte(1)
	for _, kindCase := range kinds {
		if kindCase.kind == ir.ValueBytes {
			continue
		}
		element := scalarType(kindCase.typ, false)
		for _, nullable := range []bool{false, true} {
			suffix := matrixNullabilityName(nullable)
			typ := must(ir.NewTypeRef(ir.ValueScalarList, nullable, 0, 0, ir.EnumID{}, &element, ir.CapabilityScalarListJSON))
			field := FieldSpec{id: fieldID(8, fieldIndex), model: model, name: kindCase.name + "_" + suffix, column: kindCase.name + "_" + suffix, typeRef: typ, nullable: nullable}
			fieldIndex++
			fields = append(fields, field)
			fieldCases = append(fieldCases, listFieldCase{kindCase: kindCase, field: field})
		}
	}
	rows := listMatrixRows(model, fieldCases)
	probes := listMatrixProbes(model, fieldCases)
	return listJSONMatrix{model: ModelSpec{id: model, name: "ScalarListMatrix", table: scalarListMatrixTable, identityFields: []ir.FieldID{fields[0].id}}, fields: fields, rows: rows, probes: probes}
}

func listMatrixRows(model ir.ModelID, fields []listFieldCase) []Row {
	makeRow := func(identity Identity, high, nullableNull bool) Row {
		evaluatorFields := make([]evaluate.Field, 0, len(fields))
		cells := make([]SeedCell, 0, len(fields))
		for _, fieldCase := range fields {
			if nullableNull && fieldCase.field.nullable {
				evaluatorFields = append(evaluatorFields, evaluate.NullField(fieldCase.field.id))
				cells = append(cells, NullCell(fieldCase.field.id))
				continue
			}
			if nullableNull {
				evaluatorFields = append(evaluatorFields, mustListField(fieldCase.field.id))
				cells = append(cells, mustValueCell(fieldCase.field.id, listValue()))
				continue
			}
			value := fieldCase.kindCase.low
			if high {
				value = fieldCase.kindCase.high
			}
			evaluatorFields = append(evaluatorFields, mustListField(fieldCase.field.id, value))
			cells = append(cells, mustValueCell(fieldCase.field.id, listValue(value)))
		}
		return seedRow(identity, model, mustRecord(model, evaluatorFields...), cells...)
	}
	return []Row{makeRow("list:low", false, false), makeRow("list:high", true, false), makeRow("list:null", false, true)}
}

func listMatrixProbes(model ir.ModelID, fields []listFieldCase) []Probe {
	entries := operator.Entries()
	probes := make([]Probe, 0, 180)
	for _, fieldCase := range fields {
		for _, entry := range entries {
			if entry.NodeKind() != ir.ConditionList || !entry.AcceptsFieldKind(ir.ValueScalarList) {
				continue
			}
			if entry.ID() != ir.OperatorListIsNull && entry.ID() != ir.OperatorListIsNotNull && !entry.AcceptsElementKind(fieldCase.kindCase.kind) {
				continue
			}
			if entry.Nullability() == operator.NullabilityNullable && !fieldCase.field.nullable {
				continue
			}
			var operand ir.Operand
			switch entry.ID() {
			case ir.OperatorListEqual:
				operand = oneOperand(listValue(fieldCase.kindCase.high))
			case ir.OperatorListHas:
				operand = oneOperand(fieldCase.kindCase.high)
			case ir.OperatorListHasEvery, ir.OperatorListHasSome:
				operand = manyOperand(fieldCase.kindCase.high)
			case ir.OperatorListIsEmpty:
				operand = ir.FlagOperand(false)
			default:
				operand = ir.NoOperand()
			}
			condition := listCondition(model, fieldCase.field.id, fieldCase.field.typeRef, entry.ID(), operand)
			probes = append(probes, variantProbe(fmt.Sprintf("list-matrix/%s/%s/%s", fieldCase.kindCase.name, matrixNullabilityName(fieldCase.field.nullable), entry.Name()), entry.ID(), condition))
		}
	}
	return probes
}

type jsonOperandCase struct {
	name  string
	low   ir.JSONValue
	high  ir.JSONValue
	path  ir.JSONPath
	array ir.JSONPath
}

func jsonMatrixFixture() listJSONMatrix {
	model := modelID(9)
	typRequired := mustType(ir.ValueJSON, false, nil, 0)
	typNullable := mustType(ir.ValueJSON, true, nil, 0)
	fields := []FieldSpec{
		{id: fieldID(9, 1), model: model, name: "DocumentRequired", column: "document_required", typeRef: typRequired, nullable: false},
		{id: fieldID(9, 2), model: model, name: "DocumentNullable", column: "document_nullable", typeRef: typNullable, nullable: true},
	}
	cases := jsonOperandCases()
	lowDocument := jsonMatrixDocument(cases, false)
	highDocument := jsonMatrixDocument(cases, true)
	rows := []Row{
		jsonMatrixRow(model, "json:low", fields, lowDocument, lowDocument, false),
		jsonMatrixRow(model, "json:high", fields, highDocument, highDocument, false),
		jsonMatrixRow(model, "json:null", fields, lowDocument, ir.JSONValue{}, true),
	}
	probes := jsonMatrixProbes(model, fields, cases)
	return listJSONMatrix{model: ModelSpec{id: model, name: "JSONMatrix", table: jsonMatrixTable, identityFields: []ir.FieldID{fields[0].id}}, fields: fields, rows: rows, probes: probes}
}

func jsonOperandCases() []jsonOperandCase {
	lowNumber := jsonNumber("1")
	highNumber := jsonNumber("2")
	lowString, highString := jsonString("\ue000"), jsonString("😀")
	lowArray, highArray := jsonArray(jsonNumber("1")), jsonArray(jsonNumber("2"))
	lowObject := jsonObject(jsonMember("v", jsonNumber("1")))
	highObject := jsonObject(jsonMember("v", jsonNumber("2")))
	return []jsonOperandCase{
		{name: "null", low: ir.JSONNullValue(), high: ir.JSONNullValue(), path: jsonPathKey("null"), array: jsonPathKey("array_null")},
		{name: "bool", low: ir.JSONBoolValue(false), high: ir.JSONBoolValue(true), path: jsonPathKey("bool"), array: jsonPathKey("array_bool")},
		{name: "number", low: lowNumber, high: highNumber, path: jsonPathKey("number"), array: jsonPathKey("array_number")},
		{name: "string", low: lowString, high: highString, path: jsonPathKey("string"), array: jsonPathKey("array_string")},
		{name: "array", low: lowArray, high: highArray, path: jsonPathKey("array"), array: jsonPathKey("array_array")},
		{name: "object", low: lowObject, high: highObject, path: jsonPathKey("object"), array: jsonPathKey("array_object")},
	}
}

func jsonMatrixDocument(cases []jsonOperandCase, high bool) ir.JSONValue {
	members := make([]ir.JSONMember, 0, len(cases)*2)
	for _, operandCase := range cases {
		value := operandCase.low
		if high {
			value = operandCase.high
		}
		members = append(members, jsonMember(operandCase.name, value), jsonMember("array_"+operandCase.name, jsonArray(value)))
	}
	return jsonObject(members...)
}

func jsonMatrixRow(model ir.ModelID, identity Identity, fields []FieldSpec, required, nullable ir.JSONValue, nullableNull bool) Row {
	requiredValue := jsonValue(required)
	evaluatorFields := []evaluate.Field{mustValueField(fields[0].id, requiredValue)}
	cells := []SeedCell{mustValueCell(fields[0].id, requiredValue)}
	if nullableNull {
		evaluatorFields = append(evaluatorFields, evaluate.NullField(fields[1].id))
		cells = append(cells, NullCell(fields[1].id))
	} else {
		nullableValue := jsonValue(nullable)
		evaluatorFields = append(evaluatorFields, mustValueField(fields[1].id, nullableValue))
		cells = append(cells, mustValueCell(fields[1].id, nullableValue))
	}
	return seedRow(identity, model, mustRecord(model, evaluatorFields...), cells...)
}

func jsonMatrixProbes(model ir.ModelID, fields []FieldSpec, cases []jsonOperandCase) []Probe {
	probes := make([]Probe, 0, 100)
	entries := operator.Entries()
	for _, field := range fields {
		for _, entry := range entries {
			if entry.NodeKind() != ir.ConditionJSON {
				continue
			}
			if entry.Nullability() == operator.NullabilityNullable && !field.nullable {
				continue
			}
			switch entry.ID() {
			case ir.OperatorJSONIsNull, ir.OperatorJSONIsNotNull:
				condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), ir.ComparisonSensitive, jsonPath(), ir.NoOperand())
				probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s", matrixNullabilityName(field.nullable), entry.Name()), entry.ID(), condition))
			case ir.OperatorJSONEqual, ir.OperatorJSONNotEqual:
				for _, operandCase := range cases {
					condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), ir.ComparisonSensitive, operandCase.path, oneOperand(jsonValue(operandCase.high)))
					probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s/%s", matrixNullabilityName(field.nullable), entry.Name(), operandCase.name), entry.ID(), condition))
				}
				for _, sentinel := range []struct {
					name string
					kind ir.JSONNullKind
					path ir.JSONPath
				}{{"db-null", ir.JSONDbNull, jsonPathKey("absent")}, {"document-null", ir.JSONDocumentNull, jsonPathKey("null")}, {"any-null", ir.JSONAnyNull, jsonPathKey("absent")}} {
					condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), ir.ComparisonSensitive, sentinel.path, jsonNullOperand(sentinel.kind))
					probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s/%s", matrixNullabilityName(field.nullable), entry.Name(), sentinel.name), entry.ID(), condition))
				}
			case ir.OperatorJSONLessThan, ir.OperatorJSONLessThanOrEqual, ir.OperatorJSONGreaterThan, ir.OperatorJSONGreaterThanOrEqual:
				for _, operandCase := range cases {
					if operandCase.name != "number" && operandCase.name != "string" {
						continue
					}
					operand := operandCase.high
					if entry.ID() == ir.OperatorJSONGreaterThan || entry.ID() == ir.OperatorJSONGreaterThanOrEqual {
						operand = operandCase.low
					}
					condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), ir.ComparisonSensitive, operandCase.path, oneOperand(jsonValue(operand)))
					probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s/%s", matrixNullabilityName(field.nullable), entry.Name(), operandCase.name), entry.ID(), condition))
				}
			case ir.OperatorJSONStringContains, ir.OperatorJSONStringStartsWith, ir.OperatorJSONStringEndsWith:
				stringCase := cases[3]
				for _, mode := range []ir.ComparisonMode{ir.ComparisonSensitive, ir.ComparisonASCIIInsensitive} {
					condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), mode, stringCase.path, oneOperand(jsonValue(stringCase.high)))
					probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s/string/mode-%d", matrixNullabilityName(field.nullable), entry.Name(), mode), entry.ID(), condition))
				}
			case ir.OperatorJSONArrayContains, ir.OperatorJSONArrayStartsWith, ir.OperatorJSONArrayEndsWith:
				for _, operandCase := range cases {
					condition := jsonCondition(model, field.id, field.typeRef, entry.ID(), ir.ComparisonSensitive, operandCase.array, oneOperand(jsonValue(operandCase.high)))
					probes = append(probes, variantProbe(fmt.Sprintf("json-matrix/%s/%s/%s", matrixNullabilityName(field.nullable), entry.Name(), operandCase.name), entry.ID(), condition))
				}
			}
		}
	}
	return probes
}
