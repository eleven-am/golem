package scalar

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestResolveDefaultOwnershipAndTypes(t *testing.T) {
	cases := []struct {
		name     string
		typ      ir.LogicalTypeIR
		token    string
		kind     ir.DefaultKind
		producer ir.DefaultProducer
	}{
		{"uuid", ir.LogicalTypeIR{Kind: ir.TypeUUID}, "uuid", ir.DefaultUUID, ir.ProducerApplication},
		{"now", ir.LogicalTypeIR{Kind: ir.TypeDateTime}, "now", ir.DefaultNow, ir.ProducerApplication},
		{"identity", ir.LogicalTypeIR{Kind: ir.TypeInt64}, "identity", ir.DefaultIdentity, ir.ProducerDatabase},
		{"literal", ir.LogicalTypeIR{Kind: ir.TypeBool}, "true", ir.DefaultLiteral, ir.ProducerDatabase},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, diagnostics := ResolveDefault(testCase.typ, testCase.token, DefaultContext{})
			if len(diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %#v", diagnostics)
			}
			if resolved.Kind != testCase.kind || resolved.Producer != testCase.producer {
				t.Fatalf("unexpected default: %#v", resolved)
			}
		})
	}

	if resolved, diagnostics := ResolveDefault(ir.LogicalTypeIR{Kind: ir.TypeString}, "identity", DefaultContext{}); resolved != nil || len(diagnostics) != 1 {
		t.Fatalf("invalid identity default accepted: %#v %#v", resolved, diagnostics)
	}
}

func TestResolveTypedLiterals(t *testing.T) {
	maxThree := uint32(3)
	tests := []struct {
		name      string
		typ       ir.LogicalTypeIR
		token     string
		context   DefaultContext
		canonical string
	}{
		{"decimal", ir.LogicalTypeIR{Kind: ir.TypeDecimal}, "001.2", DefaultContext{}, "1.20"},
		{"string runes", ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &maxThree}, `"aé"`, DefaultContext{}, "aé"},
		{"bytes", ir.LogicalTypeIR{Kind: ir.TypeBytes}, `"abc"`, DefaultContext{}, "YWJj"},
		{"uuid", ir.LogicalTypeIR{Kind: ir.TypeUUID}, `"550e8400-e29b-41d4-a716-446655440000"`, DefaultContext{}, "550e8400-e29b-41d4-a716-446655440000"},
		{"nil uuid", ir.LogicalTypeIR{Kind: ir.TypeUUID}, `"00000000-0000-0000-0000-000000000000"`, DefaultContext{}, "00000000-0000-0000-0000-000000000000"},
		{"date", ir.LogicalTypeIR{Kind: ir.TypeDate}, `"2026-08-05"`, DefaultContext{}, "2026-08-05"},
		{"datetime utc", ir.LogicalTypeIR{Kind: ir.TypeDateTime}, `"2026-08-05T12:00:00+02:00"`, DefaultContext{}, "2026-08-05T10:00:00.000000Z"},
		{"json", ir.LogicalTypeIR{Kind: ir.TypeJSON}, `"{\"b\":1.0,\"a\":2}"`, DefaultContext{}, `{"a":2,"b":1}`},
		{"enum", ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: enumIDPointer("status")}, "open", DefaultContext{EnumValues: []string{"open", "closed"}}, "open"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, diagnostics := ResolveDefault(testCase.typ, testCase.token, testCase.context)
			if len(diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %#v", diagnostics)
			}
			if resolved.Literal == nil || resolved.Literal.Canonical != testCase.canonical {
				t.Fatalf("got %#v, want canonical %q", resolved, testCase.canonical)
			}
		})
	}
}

func TestResolveScalarListLiteral(t *testing.T) {
	resolved, diagnostics := ResolveDefault(ir.LogicalTypeIR{
		Kind:    ir.TypeScalarList,
		Element: &ir.LogicalTypeIR{Kind: ir.TypeInt32},
	}, `"[2,1.0]"`, DefaultContext{})
	if len(diagnostics) != 0 || resolved.Literal.Canonical != "[2,1]" {
		t.Fatalf("unexpected list default: %#v %#v", resolved, diagnostics)
	}

	resolved, diagnostics = ResolveDefault(ir.LogicalTypeIR{
		Kind:    ir.TypeScalarList,
		Element: &ir.LogicalTypeIR{Kind: ir.TypeInt32},
	}, `"[null]"`, DefaultContext{})
	if resolved != nil || len(diagnostics) != 1 {
		t.Fatalf("nullable list element accepted: %#v %#v", resolved, diagnostics)
	}
}

func TestProviderDefaultAndUpdatedValidation(t *testing.T) {
	defaultValue, diagnostics := ProviderDefault(ir.ProviderSymbolRef{
		Provider: ir.PostgreSQL,
		Kind:     "default",
		Name:     "clock",
		Version:  1,
	}, ir.SourceSpan{})
	if len(diagnostics) != 0 || defaultValue.Producer != ir.ProducerProvider {
		t.Fatalf("unexpected provider default: %#v %#v", defaultValue, diagnostics)
	}

	now, diagnostics := ResolveDefault(ir.LogicalTypeIR{Kind: ir.TypeDateTime}, "now", DefaultContext{})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := ValidateUpdated(ir.LogicalTypeIR{Kind: ir.TypeDateTime}, false, now, ir.SourceSpan{}); len(diagnostics) != 0 {
		t.Fatalf("valid updated field rejected: %#v", diagnostics)
	}
	if diagnostics := ValidateUpdated(ir.LogicalTypeIR{Kind: ir.TypeDateTime}, false, nil, ir.SourceSpan{}); len(diagnostics) != 0 {
		t.Fatalf("default=now is conventional, not required: %#v", diagnostics)
	}
	if diagnostics := ValidateUpdated(ir.LogicalTypeIR{Kind: ir.TypeString}, true, nil, ir.SourceSpan{}); len(diagnostics) != 1 {
		t.Fatalf("expected type diagnostic: %#v", diagnostics)
	}
}

func TestUUIDStringDefaultRequiresEnoughDeclaredLength(t *testing.T) {
	short := uint32(35)
	if value, diagnostics := ResolveDefault(ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &short}, "uuid", DefaultContext{}); value != nil || len(diagnostics) != 1 || diagnostics[0].Code != "P1_DEFAULT_UUID_LENGTH" {
		t.Fatalf("short uuid string default = %#v diagnostics=%#v", value, diagnostics)
	}
	exact := uint32(36)
	if value, diagnostics := ResolveDefault(ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &exact}, "uuid", DefaultContext{}); value == nil || len(diagnostics) != 0 {
		t.Fatalf("exact uuid string default = %#v diagnostics=%#v", value, diagnostics)
	}
}

func enumIDPointer(value ir.EnumID) *ir.EnumID { return &value }
