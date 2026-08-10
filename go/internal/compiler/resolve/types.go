package resolve

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const golemPackagePath = "github.com/eleven-am/golem/go/golem"

func (r *baseResolver) resolveGoType(raw ir.RawGoTypeRef) (ir.LogicalTypeIR, bool, ir.FieldKind, []string, []ir.Diagnostic) {
	switch raw.Kind {
	case ir.RawGoTypePointer:
		if len(raw.Args) != 1 {
			return typeFailure(raw, "pointer type must contain one argument")
		}
		typ, nullable, kind, enumValues, diagnostics := r.resolveGoType(raw.Args[0])
		if nullable {
			diagnostics = append(diagnostics, errorf("P1_TYPE_NESTED_NULLABLE", raw.Span, "nested nullable wrappers are not accepted"))
		}
		return typ, true, kind, enumValues, diagnostics

	case ir.RawGoTypeSlice:
		if len(raw.Args) == 1 && raw.Args[0].Kind == ir.RawGoTypeBuiltin && raw.Args[0].GoName == "byte" {
			return ir.LogicalTypeIR{Kind: ir.TypeBytes}, false, ir.FieldScalar, nil, nil
		}
		return typeFailure(raw, "only []byte is a built-in persisted slice; use golem.List[T] for scalar lists")

	case ir.RawGoTypeBuiltin:
		kind, ok := map[string]ir.LogicalTypeKind{
			"bool": ir.TypeBool, "int16": ir.TypeInt16, "int32": ir.TypeInt32, "int64": ir.TypeInt64,
			"float32": ir.TypeFloat32, "float64": ir.TypeFloat64, "string": ir.TypeString,
		}[raw.GoName]
		if !ok {
			return typeFailure(raw, fmt.Sprintf("Go built-in %s is not a portable logical scalar", raw.GoName))
		}
		return ir.LogicalTypeIR{Kind: kind}, false, ir.FieldScalar, nil, nil

	case ir.RawGoTypeNamed:
		if enum, ok := r.enums[typeKey(raw.PackagePath, raw.GoName)]; ok {
			id := enum.id
			return ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &id}, false, ir.FieldEnum, enum.values, nil
		}
		kind, ok := namedScalar(raw.PackagePath, raw.GoName)
		if !ok {
			return typeFailure(raw, fmt.Sprintf("named Go type %s.%s is not a registered scalar or enum", raw.PackagePath, raw.GoName))
		}
		return ir.LogicalTypeIR{Kind: kind}, false, ir.FieldScalar, nil, nil

	case ir.RawGoTypeInstantiation:
		if raw.PackagePath != golemPackagePath || len(raw.Args) != 1 {
			return typeFailure(raw, fmt.Sprintf("generic type %s.%s is not a registered scalar wrapper", raw.PackagePath, raw.GoName))
		}
		switch raw.GoName {
		case "Null":
			typ, nullable, kind, enumValues, diagnostics := r.resolveGoType(raw.Args[0])
			if nullable {
				diagnostics = append(diagnostics, errorf("P1_TYPE_NESTED_NULLABLE", raw.Span, "nested nullable wrappers are not accepted"))
			}
			return typ, true, kind, enumValues, diagnostics
		case "JSON":
			witness := canonicalGoType(raw.Args[0])
			return ir.LogicalTypeIR{Kind: ir.TypeJSON, JSONSchemaID: &witness}, false, ir.FieldScalar, nil, nil
		case "List":
			element, nullable, elementKind, enumValues, diagnostics := r.resolveGoType(raw.Args[0])
			if nullable {
				diagnostics = append(diagnostics, errorf("P1_LIST_NULLABLE_ELEMENT", raw.Args[0].Span, "scalar-list elements cannot be nullable"))
			}
			if elementKind == ir.FieldScalarList {
				diagnostics = append(diagnostics, errorf("P1_LIST_NESTED", raw.Args[0].Span, "nested scalar lists are not accepted"))
			}
			return ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &element}, false, ir.FieldScalarList, enumValues, diagnostics
		default:
			return typeFailure(raw, fmt.Sprintf("golem.%s is not a registered scalar wrapper", raw.GoName))
		}
	default:
		return typeFailure(raw, fmt.Sprintf("unknown raw Go type kind %q", raw.Kind))
	}
}

func namedScalar(packagePath, name string) (ir.LogicalTypeKind, bool) {
	if packagePath == "time" && name == "Time" {
		return ir.TypeDateTime, true
	}
	if packagePath != golemPackagePath {
		return "", false
	}
	kind, ok := map[string]ir.LogicalTypeKind{
		"UUID": ir.TypeUUID, "Decimal": ir.TypeDecimal, "Date": ir.TypeDate, "Time": ir.TypeTime,
	}[name]
	return kind, ok
}

func typeFailure(raw ir.RawGoTypeRef, message string) (ir.LogicalTypeIR, bool, ir.FieldKind, []string, []ir.Diagnostic) {
	return ir.LogicalTypeIR{}, false, ir.FieldScalar, nil, []ir.Diagnostic{errorf("P1_TYPE_GO_UNSUPPORTED", raw.Span, "%s", message)}
}

func canonicalGoType(raw ir.RawGoTypeRef) string {
	switch raw.Kind {
	case ir.RawGoTypeBuiltin:
		return raw.GoName
	case ir.RawGoTypeNamed:
		return raw.PackagePath + "." + raw.GoName
	case ir.RawGoTypePointer:
		if len(raw.Args) == 1 {
			return "*" + canonicalGoType(raw.Args[0])
		}
	case ir.RawGoTypeSlice:
		if len(raw.Args) == 1 {
			return "[]" + canonicalGoType(raw.Args[0])
		}
	case ir.RawGoTypeInstantiation:
		arguments := make([]string, len(raw.Args))
		for index := range raw.Args {
			arguments[index] = canonicalGoType(raw.Args[index])
		}
		return raw.PackagePath + "." + raw.GoName + "[" + strings.Join(arguments, ",") + "]"
	}
	return "<invalid>"
}

func applyTypeOverride(logical ir.LogicalTypeIR, value string, span ir.SourceSpan) (ir.LogicalTypeIR, []ir.Diagnostic) {
	name, parameters, ok := parseTypeOverride(value)
	if !ok {
		return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_SYNTAX", span, "invalid logical type override %q", value)}
	}
	expectKind := func(kind ir.LogicalTypeKind) []ir.Diagnostic {
		if logical.Kind != kind {
			return []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_MISMATCH", span, "%s override is incompatible with Go-derived %s", name, logical.Kind)}
		}
		return nil
	}
	switch name {
	case "varchar", "string":
		if diagnostics := expectKind(ir.TypeString); len(diagnostics) != 0 {
			return logical, diagnostics
		}
		if len(parameters) != 1 {
			return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_ARITY", span, "%s requires one maximum length", name)}
		}
		logical.MaxLength = uint32Pointer(parameters[0])
	case "bytes":
		if diagnostics := expectKind(ir.TypeBytes); len(diagnostics) != 0 {
			return logical, diagnostics
		}
		if len(parameters) != 1 {
			return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_ARITY", span, "bytes requires one maximum length")}
		}
		logical.MaxLength = uint32Pointer(parameters[0])
	case "decimal":
		if diagnostics := expectKind(ir.TypeDecimal); len(diagnostics) != 0 {
			return logical, diagnostics
		}
		if len(parameters) != 2 || parameters[0] > 65535 || parameters[1] > 65535 {
			return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_ARITY", span, "decimal requires precision and scale")}
		}
		logical.Precision, logical.Scale = uint16Pointer(uint16(parameters[0])), uint16Pointer(uint16(parameters[1]))
	case "time", "datetime":
		expected := ir.TypeTime
		if name == "datetime" {
			expected = ir.TypeDateTime
		}
		if diagnostics := expectKind(expected); len(diagnostics) != 0 {
			return logical, diagnostics
		}
		if len(parameters) != 1 || parameters[0] > 65535 {
			return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_ARITY", span, "%s requires one precision", name)}
		}
		logical.Precision = uint16Pointer(uint16(parameters[0]))
	case "json":
		if diagnostics := expectKind(ir.TypeJSON); len(diagnostics) != 0 {
			return logical, diagnostics
		}
		if len(parameters) != 0 {
			return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_ARITY", span, "json accepts no parameters")}
		}
	default:
		return logical, []ir.Diagnostic{errorf("P1_TYPE_OVERRIDE_UNKNOWN", span, "unknown logical type override %q", name)}
	}
	return logical, nil
}

func parseTypeOverride(value string) (string, []uint32, bool) {
	open := strings.IndexByte(value, '(')
	if open < 0 {
		if value == "json" {
			return value, nil, true
		}
		return "", nil, false
	}
	if !strings.HasSuffix(value, ")") || open == 0 {
		return "", nil, false
	}
	name := value[:open]
	rawParameters := strings.Split(value[open+1:len(value)-1], ",")
	parameters := make([]uint32, 0, len(rawParameters))
	for _, raw := range rawParameters {
		number, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
		if err != nil {
			return "", nil, false
		}
		parameters = append(parameters, uint32(number))
	}
	return name, parameters, true
}

func uint16Pointer(value uint16) *uint16 { return &value }
func uint32Pointer(value uint32) *uint32 { return &value }
