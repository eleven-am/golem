package bind

import (
	"encoding/hex"
	"fmt"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const listCapability = compilerir.CapabilityID("scalar-list:json-array:v1")

func bindType(logical compilerir.LogicalTypeIR, nullable bool) (policyir.TypeRef, error) {
	kind, ok := logicalKind(logical.Kind)
	if !ok {
		return policyir.TypeRef{}, fmt.Errorf("unknown logical type %q", logical.Kind)
	}
	var precision, scale uint16
	if logical.Precision != nil {
		precision = *logical.Precision
	}
	if logical.Scale != nil {
		scale = *logical.Scale
	}
	if logical.Kind == compilerir.TypeDecimal && (logical.Precision == nil || logical.Scale == nil) {
		return policyir.TypeRef{}, fmt.Errorf("decimal logical type lacks normalized precision or scale")
	}
	if (logical.Kind == compilerir.TypeTime || logical.Kind == compilerir.TypeDateTime) && logical.Precision == nil {
		return policyir.TypeRef{}, fmt.Errorf("temporal logical type lacks normalized precision")
	}
	var enum policyir.EnumID
	if logical.EnumID != nil {
		decoded, err := fixedID(string(*logical.EnumID))
		if err != nil {
			return policyir.TypeRef{}, fmt.Errorf("invalid enum identity: %w", err)
		}
		enum = policyir.EnumID(decoded)
	}
	var element *policyir.TypeRef
	if logical.Element != nil {
		value, err := bindType(*logical.Element, false)
		if err != nil {
			return policyir.TypeRef{}, fmt.Errorf("invalid scalar-list element: %w", err)
		}
		element = &value
	}
	var capability policyir.Capability
	if logical.Capability != nil {
		if *logical.Capability != listCapability {
			return policyir.TypeRef{}, fmt.Errorf("unknown logical capability %q", *logical.Capability)
		}
		capability = policyir.CapabilityScalarListJSON
	}
	if logical.Kind == compilerir.TypeScalarList && capability != policyir.CapabilityScalarListJSON {
		return policyir.TypeRef{}, fmt.Errorf("scalar-list logical type lacks capability %q", listCapability)
	}
	return policyir.NewTypeRef(kind, nullable, precision, scale, enum, element, capability)
}

func logicalKind(value compilerir.LogicalTypeKind) (policyir.ValueKind, bool) {
	values := map[compilerir.LogicalTypeKind]policyir.ValueKind{
		compilerir.TypeBool: policyir.ValueBool, compilerir.TypeInt16: policyir.ValueInt16,
		compilerir.TypeInt32: policyir.ValueInt32, compilerir.TypeInt64: policyir.ValueInt64,
		compilerir.TypeFloat32: policyir.ValueFloat32, compilerir.TypeFloat64: policyir.ValueFloat64,
		compilerir.TypeDecimal: policyir.ValueDecimal, compilerir.TypeString: policyir.ValueString,
		compilerir.TypeBytes: policyir.ValueBytes, compilerir.TypeUUID: policyir.ValueUUID,
		compilerir.TypeDate: policyir.ValueDate, compilerir.TypeTime: policyir.ValueTime,
		compilerir.TypeDateTime: policyir.ValueDateTime, compilerir.TypeEnum: policyir.ValueEnum,
		compilerir.TypeJSON: policyir.ValueJSON, compilerir.TypeScalarList: policyir.ValueScalarList,
	}
	result, ok := values[value]
	return result, ok
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("%q is not a canonical 128-bit lowercase hexadecimal ID", value)
	}
	copy(result[:], decoded)
	return result, nil
}
