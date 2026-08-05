// Package scalar owns provider-neutral logical scalar and default validation.
// It does not inspect Go packages or choose physical database storage.
package scalar

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	DefaultDecimalPrecision  uint16 = 18
	DefaultDecimalScale      uint16 = 2
	DefaultTemporalPrecision uint16 = 6
)

// Nullability is the resolved logical database nullability. Go source wrapper
// recognition belongs to the source-discovery layer; this package validates
// the provider-neutral result.
type Nullability uint8

const (
	Required Nullability = iota
	Nullable
)

func ValidateNullability(value Nullability, primaryKeyComponent bool, span ir.SourceSpan) []ir.Diagnostic {
	if value != Required && value != Nullable {
		return []ir.Diagnostic{ir.NewError(
			"P1_NULLABILITY_UNKNOWN",
			fmt.Sprintf("unknown logical nullability value %d", value),
			span,
		)}
	}
	if value == Nullable && primaryKeyComponent {
		return []ir.Diagnostic{ir.NewError(
			"P1_PRIMARY_KEY_NULLABLE",
			"primary-key fields must be non-null",
			span,
		)}
	}
	return nil
}

var registeredKinds = map[ir.LogicalTypeKind]struct{}{
	ir.TypeBool: {}, ir.TypeInt16: {}, ir.TypeInt32: {}, ir.TypeInt64: {},
	ir.TypeFloat32: {}, ir.TypeFloat64: {}, ir.TypeDecimal: {}, ir.TypeString: {},
	ir.TypeBytes: {}, ir.TypeUUID: {}, ir.TypeDate: {}, ir.TypeTime: {},
	ir.TypeDateTime: {}, ir.TypeJSON: {}, ir.TypeEnum: {}, ir.TypeScalarList: {},
}

func NormalizeType(input ir.LogicalTypeIR, span ir.SourceSpan) (ir.LogicalTypeIR, []ir.Diagnostic) {
	output := input
	if _, ok := registeredKinds[input.Kind]; !ok {
		return output, []ir.Diagnostic{ir.NewError(
			"P1_TYPE_UNKNOWN",
			fmt.Sprintf("unknown logical scalar kind %q", input.Kind),
			span,
		)}
	}

	diagnostics := validateParameterPlacement(input, span)
	switch input.Kind {
	case ir.TypeDecimal:
		if output.Precision == nil {
			output.Precision = uint16Pointer(DefaultDecimalPrecision)
		}
		if output.Scale == nil {
			output.Scale = uint16Pointer(DefaultDecimalScale)
		}
		if *output.Precision < 1 || *output.Precision > 18 {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_DECIMAL_PRECISION",
				fmt.Sprintf("portable Decimal precision must be between 1 and 18, received %d", *output.Precision),
				span,
			))
		}
		if *output.Scale > *output.Precision {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_DECIMAL_SCALE",
				fmt.Sprintf("Decimal scale %d exceeds precision %d", *output.Scale, *output.Precision),
				span,
			))
		}
	case ir.TypeTime, ir.TypeDateTime:
		if output.Precision == nil {
			output.Precision = uint16Pointer(DefaultTemporalPrecision)
		}
		if *output.Precision > 6 {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_TIME_PRECISION",
				fmt.Sprintf("%s precision must be between 0 and 6, received %d", input.Kind, *output.Precision),
				span,
			))
		}
	case ir.TypeString, ir.TypeBytes:
		if output.MaxLength != nil && *output.MaxLength == 0 {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_TYPE_LENGTH",
				fmt.Sprintf("%s maximum length must be positive", input.Kind),
				span,
			))
		}
	case ir.TypeEnum:
		if output.EnumID == nil || *output.EnumID == "" {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_ENUM_ID_REQUIRED",
				"logical enum type requires a resolved EnumID",
				span,
			))
		}
	case ir.TypeScalarList:
		if output.Element == nil {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_LIST_ELEMENT_REQUIRED",
				"scalar list requires an element logical type",
				span,
			))
			break
		}
		element, elementDiagnostics := NormalizeType(*output.Element, span)
		output.Element = &element
		diagnostics = append(diagnostics, elementDiagnostics...)
		if !portableListElement(element.Kind) {
			diagnostics = append(diagnostics, ir.NewError(
				"P1_LIST_ELEMENT_UNSUPPORTED",
				fmt.Sprintf("%s is not a portable scalar-list element kind", element.Kind),
				span,
			))
		}
		capability := ir.CapabilityID("scalar-list:json-array:v1")
		output.Capability = &capability
	}
	return output, diagnostics
}

// ValidateIdentityDefault applies the field-level constraints that cannot be
// decided while parsing the default token alone.
func ValidateIdentityDefault(logicalType ir.LogicalTypeIR, nullable, primaryKeyComponent bool, primaryKeyArity int, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	if logicalType.Kind != ir.TypeInt16 && logicalType.Kind != ir.TypeInt32 && logicalType.Kind != ir.TypeInt64 {
		diagnostics = append(diagnostics, ir.NewError("P1_DEFAULT_IDENTITY_TYPE", "identity default requires a signed integer field", span))
	}
	if nullable || !primaryKeyComponent || primaryKeyArity != 1 {
		diagnostics = append(diagnostics, ir.NewError("P1_DEFAULT_IDENTITY_KEY", "identity default requires a non-null, single-column primary key", span))
	}
	return diagnostics
}

// ValidateGeneratedNullability enforces that a Go field can represent NULL
// whenever the closed expression analysis cannot prove a generated value is
// non-null.
func ValidateGeneratedNullability(value Nullability, expressionProvenNonNull bool, span ir.SourceSpan) []ir.Diagnostic {
	if !expressionProvenNonNull && value != Nullable {
		return []ir.Diagnostic{ir.NewError(
			"P1_GENERATED_NULLABILITY",
			"generated expression with unproven nullability requires a nullable field",
			span,
		)}
	}
	return nil
}

// ValidateCompositeNullability enforces the portable v1 all-nullable or
// all-required rule for composite foreign-key components.
func ValidateCompositeNullability(values []Nullability, span ir.SourceSpan) []ir.Diagnostic {
	if len(values) < 2 {
		return nil
	}
	first := values[0]
	for _, value := range values {
		if value != Required && value != Nullable {
			return []ir.Diagnostic{ir.NewError("P1_NULLABILITY_UNKNOWN", fmt.Sprintf("unknown logical nullability value %d", value), span)}
		}
		if value != first {
			return []ir.Diagnostic{ir.NewError(
				"P1_COMPOSITE_NULLABILITY_MIXED",
				"portable composite foreign keys must be either all nullable or all non-null",
				span,
			)}
		}
	}
	return nil
}

func validateParameterPlacement(input ir.LogicalTypeIR, span ir.SourceSpan) []ir.Diagnostic {
	var invalid []string
	if input.Precision != nil && input.Kind != ir.TypeDecimal && input.Kind != ir.TypeTime && input.Kind != ir.TypeDateTime {
		invalid = append(invalid, "precision")
	}
	if input.Scale != nil && input.Kind != ir.TypeDecimal {
		invalid = append(invalid, "scale")
	}
	if input.MaxLength != nil && input.Kind != ir.TypeString && input.Kind != ir.TypeBytes {
		invalid = append(invalid, "maximum length")
	}
	if input.EnumID != nil && input.Kind != ir.TypeEnum {
		invalid = append(invalid, "enum ID")
	}
	if input.Element != nil && input.Kind != ir.TypeScalarList {
		invalid = append(invalid, "element type")
	}
	if input.JSONSchemaID != nil && input.Kind != ir.TypeJSON {
		invalid = append(invalid, "JSON schema ID")
	}
	if len(invalid) == 0 {
		return nil
	}
	return []ir.Diagnostic{ir.NewError(
		"P1_TYPE_PARAMETER_INVALID",
		fmt.Sprintf("%s does not accept %s", input.Kind, joinParameterNames(invalid)),
		span,
	)}
}

func joinParameterNames(parameters []string) string {
	if len(parameters) == 1 {
		return parameters[0]
	}
	result := ""
	for index, parameter := range parameters {
		if index > 0 {
			result += ", "
		}
		result += parameter
	}
	return result
}

func ValidateEnum(enum ir.EnumIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	if enum.ID == "" {
		diagnostics = append(diagnostics, ir.NewError("P1_ENUM_ID_REQUIRED", "enum requires a stable ID", span))
	}
	if len(enum.Values) == 0 {
		diagnostics = append(diagnostics, ir.NewError("P1_ENUM_EMPTY", fmt.Sprintf("enum %s declares no values", enum.LogicalName), span))
		return diagnostics
	}
	wires := make(map[string]struct{}, len(enum.Values))
	ids := make(map[ir.EnumValueID]struct{}, len(enum.Values))
	for _, value := range enum.Values {
		if value.WireValue == "" {
			diagnostics = append(diagnostics, ir.NewError("P1_ENUM_VALUE_EMPTY", fmt.Sprintf("enum %s has an empty wire value", enum.LogicalName), span))
		}
		if _, exists := wires[value.WireValue]; exists {
			diagnostics = append(diagnostics, ir.NewError("P1_ENUM_VALUE_DUPLICATE", fmt.Sprintf("enum %s repeats wire value %q", enum.LogicalName, value.WireValue), span))
		}
		wires[value.WireValue] = struct{}{}
		if value.ID == "" {
			diagnostics = append(diagnostics, ir.NewError("P1_ENUM_VALUE_ID_REQUIRED", fmt.Sprintf("enum value %s.%s requires a stable ID", enum.LogicalName, value.GoName), span))
		} else if _, exists := ids[value.ID]; exists {
			diagnostics = append(diagnostics, ir.NewError("P1_ENUM_VALUE_ID_DUPLICATE", fmt.Sprintf("enum %s repeats value ID %s", enum.LogicalName, value.ID), span))
		}
		ids[value.ID] = struct{}{}
	}
	return diagnostics
}

func portableListElement(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeBool, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64,
		ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal, ir.TypeString,
		ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeDateTime, ir.TypeEnum:
		return true
	default:
		return false
	}
}

func uint16Pointer(value uint16) *uint16 { return &value }
