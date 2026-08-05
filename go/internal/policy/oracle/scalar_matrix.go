package oracle

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

const scalarMatrixTable = "scalar_matrix"

type scalarMatrix struct {
	model  ModelSpec
	fields []FieldSpec
	rows   []Row
	probes []Probe
}

type scalarKindCase struct {
	name string
	kind ir.ValueKind
	typ  ir.TypeRef
	low  ir.Value
	high ir.Value
}

type scalarFieldCase struct {
	kindCase scalarKindCase
	field    FieldSpec
}

var (
	scalarMatrixModelID = modelID(7)
	matrixEnumID        = enumID(1)
	matrixEnumLowID     = enumValueID(1)
	matrixEnumHighID    = enumValueID(2)
)

func scalarMatrixFixture() scalarMatrix {
	kinds := scalarKindCases()
	fieldCases := make([]scalarFieldCase, 0, len(kinds)*2)
	fields := make([]FieldSpec, 0, len(kinds)*2)
	for kindIndex, kindCase := range kinds {
		for nullIndex, nullable := range []bool{false, true} {
			typ := scalarType(kindCase.typ, nullable)
			suffix := "required"
			if nullable {
				suffix = "nullable"
			}
			field := FieldSpec{
				id:       fieldID(7, byte(kindIndex*2+nullIndex+1)),
				model:    scalarMatrixModelID,
				name:     strings.ToUpper(kindCase.name[:1]) + kindCase.name[1:] + strings.ToUpper(suffix[:1]) + suffix[1:],
				column:   kindCase.name + "_" + suffix,
				typeRef:  typ,
				nullable: nullable,
			}
			fields = append(fields, field)
			fieldCases = append(fieldCases, scalarFieldCase{kindCase: kindCase, field: field})
		}
	}

	rows := scalarMatrixRows(fieldCases)
	probes := scalarMatrixProbes(fieldCases)
	return scalarMatrix{
		model:  ModelSpec{id: scalarMatrixModelID, name: "ScalarMatrix", table: scalarMatrixTable, identityFields: []ir.FieldID{fields[0].id}},
		fields: fields,
		rows:   rows,
		probes: probes,
	}
}

func scalarKindCases() []scalarKindCase {
	int16Low := must(ir.SignedValue(ir.ValueInt16, -12))
	int16High := must(ir.SignedValue(ir.ValueInt16, 23))
	int32Low := must(ir.SignedValue(ir.ValueInt32, -12_345))
	int32High := must(ir.SignedValue(ir.ValueInt32, 23_456))
	int64Low := must(ir.SignedValue(ir.ValueInt64, -9_007_199_254_740_993))
	int64High := must(ir.SignedValue(ir.ValueInt64, 9_007_199_254_740_993))
	float32Low := must(ir.Float32Value(-1.25))
	float32High := must(ir.Float32Value(2.5))
	float64Low := must(ir.Float64Value(-1.125))
	float64High := must(ir.Float64Value(2.25))
	decimalLow := must(ir.NewDecimalValue(-12345, 4))
	decimalHigh := must(ir.NewDecimalValue(23456, 4))
	dateLow := must(ir.NewDateValue(2024, 2, 29))
	dateHigh := must(ir.NewDateValue(2026, 8, 5))
	timeLow := must(ir.NewTimeValue(1_000_001))
	timeHigh := must(ir.NewTimeValue(86_399_999_999))
	datetimeLow := must(ir.NewDateTimeValue(1, 999_999_000))
	datetimeHigh := must(ir.NewDateTimeValue(1_800_000_000, 123_456_000))
	enumLow := must(ir.NewEnumValue(matrixEnumID, matrixEnumLowID))
	enumHigh := must(ir.NewEnumValue(matrixEnumID, matrixEnumHighID))
	return []scalarKindCase{
		{name: "bool", kind: ir.ValueBool, typ: scalarTypeRef(ir.ValueBool, 0, 0, ir.EnumID{}), low: ir.BoolValue(false), high: ir.BoolValue(true)},
		{name: "int16", kind: ir.ValueInt16, typ: scalarTypeRef(ir.ValueInt16, 0, 0, ir.EnumID{}), low: int16Low, high: int16High},
		{name: "int32", kind: ir.ValueInt32, typ: scalarTypeRef(ir.ValueInt32, 0, 0, ir.EnumID{}), low: int32Low, high: int32High},
		{name: "int64", kind: ir.ValueInt64, typ: scalarTypeRef(ir.ValueInt64, 0, 0, ir.EnumID{}), low: int64Low, high: int64High},
		{name: "float32", kind: ir.ValueFloat32, typ: scalarTypeRef(ir.ValueFloat32, 0, 0, ir.EnumID{}), low: float32Low, high: float32High},
		{name: "float64", kind: ir.ValueFloat64, typ: scalarTypeRef(ir.ValueFloat64, 0, 0, ir.EnumID{}), low: float64Low, high: float64High},
		{name: "decimal", kind: ir.ValueDecimal, typ: scalarTypeRef(ir.ValueDecimal, 18, 4, ir.EnumID{}), low: decimalLow, high: decimalHigh},
		// UTF-8 binary order deliberately places the private-use BMP rune before
		// the astral rune. This is the M24 cross-provider ordering witness.
		{name: "string", kind: ir.ValueString, typ: scalarTypeRef(ir.ValueString, 0, 0, ir.EnumID{}), low: stringValue("\ue000"), high: stringValue("😀")},
		{name: "bytes", kind: ir.ValueBytes, typ: scalarTypeRef(ir.ValueBytes, 0, 0, ir.EnumID{}), low: ir.BytesValue([]byte{0x00, 0x7f}), high: ir.BytesValue([]byte{0x00, 0x80})},
		{name: "uuid", kind: ir.ValueUUID, typ: scalarTypeRef(ir.ValueUUID, 0, 0, ir.EnumID{}), low: ir.UUIDValue(uuid(41)), high: ir.UUIDValue(uuid(42))},
		{name: "date", kind: ir.ValueDate, typ: scalarTypeRef(ir.ValueDate, 0, 0, ir.EnumID{}), low: dateLow, high: dateHigh},
		{name: "time", kind: ir.ValueTime, typ: scalarTypeRef(ir.ValueTime, 6, 0, ir.EnumID{}), low: timeLow, high: timeHigh},
		{name: "datetime", kind: ir.ValueDateTime, typ: scalarTypeRef(ir.ValueDateTime, 6, 0, ir.EnumID{}), low: datetimeLow, high: datetimeHigh},
		{name: "enum", kind: ir.ValueEnum, typ: scalarTypeRef(ir.ValueEnum, 0, 0, matrixEnumID), low: enumLow, high: enumHigh},
	}
}

func scalarTypeRef(kind ir.ValueKind, precision, scale uint16, enum ir.EnumID) ir.TypeRef {
	return must(ir.NewTypeRef(kind, false, precision, scale, enum, nil, 0))
}

func scalarType(base ir.TypeRef, nullable bool) ir.TypeRef {
	enum, _ := base.EnumID()
	return must(ir.NewTypeRef(base.Kind(), nullable, base.Precision(), base.Scale(), enum, nil, base.Capability()))
}

func scalarMatrixRows(fields []scalarFieldCase) []Row {
	makeRow := func(identity Identity, high, nullableNull bool) Row {
		evaluatorFields := make([]evaluate.Field, 0, len(fields))
		cells := make([]SeedCell, 0, len(fields))
		for _, fieldCase := range fields {
			if nullableNull && fieldCase.field.nullable {
				evaluatorFields = append(evaluatorFields, evaluate.NullField(fieldCase.field.id))
				cells = append(cells, NullCell(fieldCase.field.id))
				continue
			}
			value := fieldCase.kindCase.low
			if high {
				value = fieldCase.kindCase.high
			}
			evaluatorFields = append(evaluatorFields, mustValueField(fieldCase.field.id, value))
			cells = append(cells, mustValueCell(fieldCase.field.id, value))
		}
		return seedRow(identity, scalarMatrixModelID, mustRecord(scalarMatrixModelID, evaluatorFields...), cells...)
	}
	return []Row{
		makeRow("scalar:low", false, false),
		makeRow("scalar:high", true, false),
		makeRow("scalar:null", false, true),
	}
}

func scalarMatrixProbes(fields []scalarFieldCase) []Probe {
	entries := operator.Entries()
	probes := make([]Probe, 0, 240)
	for _, fieldCase := range fields {
		for _, entry := range entries {
			if entry.NodeKind() != ir.ConditionScalar || !entry.AcceptsFieldKind(fieldCase.kindCase.kind) {
				continue
			}
			if entry.Nullability() == operator.NullabilityNullable && !fieldCase.field.nullable {
				continue
			}
			operand := scalarMatrixOperand(entry.ID(), fieldCase.kindCase.low, fieldCase.kindCase.high)
			condition := scalarCondition(scalarMatrixModelID, fieldCase.field.id, fieldCase.field.typeRef, entry.ID(), ir.ComparisonSensitive, operand)
			probes = append(probes, variantProbe(
				fmt.Sprintf("matrix/%s/%s/%s", fieldCase.kindCase.name, matrixNullabilityName(fieldCase.field.nullable), entry.Name()),
				entry.ID(),
				condition,
			))
		}
	}
	for _, fieldCase := range fields {
		if fieldCase.kindCase.kind == ir.ValueString && !fieldCase.field.nullable {
			condition := scalarCondition(scalarMatrixModelID, fieldCase.field.id, fieldCase.field.typeRef, ir.OperatorLessThan, ir.ComparisonSensitive, oneOperand(fieldCase.kindCase.high))
			probes = append(probes, variantProbe("scalar/astral-private-use-order", ir.OperatorLessThan, condition))
			break
		}
	}
	// Mode is a separate ABI axis. Keep an explicit ASCII-insensitive set for
	// every string operator that accepts it, in both nullable forms.
	for _, fieldCase := range fields {
		if fieldCase.kindCase.kind != ir.ValueString {
			continue
		}
		for _, entry := range entries {
			if entry.NodeKind() != ir.ConditionScalar || !entry.AcceptsFieldKind(ir.ValueString) || !entry.AcceptsMode(ir.ComparisonASCIIInsensitive) {
				continue
			}
			if entry.Nullability() == operator.NullabilityNullable && !fieldCase.field.nullable {
				continue
			}
			operand := scalarMatrixOperand(entry.ID(), fieldCase.kindCase.low, fieldCase.kindCase.high)
			condition := scalarCondition(scalarMatrixModelID, fieldCase.field.id, fieldCase.field.typeRef, entry.ID(), ir.ComparisonASCIIInsensitive, operand)
			probes = append(probes, variantProbe(
				fmt.Sprintf("matrix/string/%s/%s/ascii-insensitive", matrixNullabilityName(fieldCase.field.nullable), entry.Name()),
				entry.ID(),
				condition,
			))
		}
	}
	return probes
}

func scalarMatrixOperand(operatorID ir.OperatorID, low, high ir.Value) ir.Operand {
	switch operatorID {
	case ir.OperatorIsNull, ir.OperatorIsNotNull:
		return ir.NoOperand()
	case ir.OperatorIn, ir.OperatorNotIn:
		return manyOperand(high)
	case ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual:
		return oneOperand(low)
	default:
		return oneOperand(high)
	}
}

func matrixNullabilityName(nullable bool) string {
	if nullable {
		return "nullable"
	}
	return "required"
}

func enumID(value byte) (result ir.EnumID) {
	result[0], result[15] = 0x54, value
	return result
}

func enumValueID(value byte) (result ir.EnumValueID) {
	result[0], result[15] = 0x55, value
	return result
}

func scalarMatrixEnumWire(enum ir.EnumID, value ir.EnumValueID) (string, bool) {
	if enum != matrixEnumID {
		return "", false
	}
	switch value {
	case matrixEnumLowID:
		return "alpha", true
	case matrixEnumHighID:
		return "omega", true
	default:
		return "", false
	}
}
