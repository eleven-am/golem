package scalar

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestNormalizeTypeAppliesPortableDefaults(t *testing.T) {
	decimal, diagnostics := NormalizeType(ir.LogicalTypeIR{Kind: ir.TypeDecimal}, ir.SourceSpan{})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected decimal diagnostics: %#v", diagnostics)
	}
	if *decimal.Precision != 18 || *decimal.Scale != 2 {
		t.Fatalf("unexpected decimal defaults: %#v", decimal)
	}

	dateTime, diagnostics := NormalizeType(ir.LogicalTypeIR{Kind: ir.TypeDateTime}, ir.SourceSpan{})
	if len(diagnostics) != 0 || *dateTime.Precision != 6 {
		t.Fatalf("unexpected DateTime normalization: %#v %#v", dateTime, diagnostics)
	}
}

func TestNormalizeTypeRejectsInvalidParameters(t *testing.T) {
	p19, s2, p2, s3 := uint16(19), uint16(2), uint16(2), uint16(3)
	cases := []ir.LogicalTypeIR{
		{Kind: ir.TypeDecimal, Precision: &p19, Scale: &s2},
		{Kind: ir.TypeDecimal, Precision: &p2, Scale: &s3},
		{Kind: ir.TypeBool, Precision: &p2},
		{Kind: ir.TypeString, MaxLength: uint32Pointer(0)},
		{Kind: ir.TypeEnum},
		{Kind: ir.TypeString, Scale: &s2},
		{Kind: ir.TypeDateTime, MaxLength: uint32Pointer(4)},
	}
	for _, testCase := range cases {
		if _, diagnostics := NormalizeType(testCase, ir.SourceSpan{}); len(diagnostics) == 0 {
			t.Fatalf("expected diagnostics for %#v", testCase)
		}
	}
}

func TestNormalizeScalarList(t *testing.T) {
	enumID := ir.EnumID("enum-1")
	typ, diagnostics := NormalizeType(ir.LogicalTypeIR{
		Kind: ir.TypeScalarList,
		Element: &ir.LogicalTypeIR{
			Kind:   ir.TypeEnum,
			EnumID: &enumID,
		},
	}, ir.SourceSpan{})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	if typ.Capability == nil || *typ.Capability != "scalar-list:json-array:v1" {
		t.Fatalf("missing scalar-list capability: %#v", typ)
	}

	_, diagnostics = NormalizeType(ir.LogicalTypeIR{
		Kind:    ir.TypeScalarList,
		Element: &ir.LogicalTypeIR{Kind: ir.TypeJSON},
	}, ir.SourceSpan{})
	if len(diagnostics) == 0 {
		t.Fatal("expected JSON list element to be rejected")
	}
}

func TestValidateEnumAndNullability(t *testing.T) {
	diagnostics := ValidateEnum(ir.EnumIR{
		ID:          "status",
		LogicalName: "Status",
		Values: []ir.EnumValueIR{
			{ID: "open", WireValue: "open"},
			{ID: "closed", WireValue: "open"},
		},
	}, ir.SourceSpan{})
	if len(diagnostics) != 1 || diagnostics[0].Code != "P1_ENUM_VALUE_DUPLICATE" {
		t.Fatalf("unexpected enum diagnostics: %#v", diagnostics)
	}
	if diagnostics := ValidateNullability(Nullable, true, ir.SourceSpan{}); len(diagnostics) != 1 || diagnostics[0].Code != "P1_PRIMARY_KEY_NULLABLE" {
		t.Fatalf("unexpected nullability diagnostics: %#v", diagnostics)
	}
	if diagnostics := ValidateNullability(Required, true, ir.SourceSpan{}); len(diagnostics) != 0 {
		t.Fatalf("required primary key should be valid: %#v", diagnostics)
	}
	if diagnostics := ValidateGeneratedNullability(Required, false, ir.SourceSpan{}); len(diagnostics) != 1 {
		t.Fatalf("unproven generated nullability was accepted: %#v", diagnostics)
	}
	if diagnostics := ValidateGeneratedNullability(Nullable, false, ir.SourceSpan{}); len(diagnostics) != 0 {
		t.Fatalf("nullable generated field was rejected: %#v", diagnostics)
	}
	if diagnostics := ValidateCompositeNullability([]Nullability{Required, Nullable}, ir.SourceSpan{}); len(diagnostics) != 1 {
		t.Fatalf("mixed composite nullability was accepted: %#v", diagnostics)
	}
}

func TestValidateIdentityDefault(t *testing.T) {
	if diagnostics := ValidateIdentityDefault(ir.LogicalTypeIR{Kind: ir.TypeInt64}, false, true, 1, ir.SourceSpan{}); len(diagnostics) != 0 {
		t.Fatalf("valid identity rejected: %#v", diagnostics)
	}
	if diagnostics := ValidateIdentityDefault(ir.LogicalTypeIR{Kind: ir.TypeInt64}, false, true, 2, ir.SourceSpan{}); len(diagnostics) != 1 {
		t.Fatalf("composite identity accepted: %#v", diagnostics)
	}
}

func uint32Pointer(value uint32) *uint32 { return &value }
