package runtime

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func membershipTestApp() *App[struct{}, struct{}] {
	return &App[struct{}, struct{}]{}
}

func membershipSetOperation(t *testing.T, field policyir.FieldID, kind policyir.ValueKind, value policyir.Value) mutationir.ScalarOperation {
	t.Helper()
	typ, err := policyir.NewTypeRef(kind, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := mutationir.NewSet(field, typ, value)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestMembershipHookInputCarriesPublicGoValues(t *testing.T) {
	field := policyir.FieldID{15: 2}
	operation := membershipSetOperation(t, field, policyir.ValueUUID, policyir.UUIDValue([16]byte{15: 7}))
	input, err := membershipFrozenInput(membershipTestApp(), policyir.ModelID{15: 1}, []mutationir.ScalarOperation{operation})
	if err != nil {
		t.Fatal(err)
	}
	fields := input.Fields()
	value, present := fields[0].Value()
	if len(fields) != 1 || !present || value != (golem.UUID{15: 7}) {
		t.Fatalf("membership hook field = %T(%#v) present=%v", value, value, present)
	}
}

func TestMembershipHookInputRefusesValuesWithoutAPublicForm(t *testing.T) {
	field := policyir.FieldID{15: 2}
	enum, err := policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{2})
	if err != nil {
		t.Fatal(err)
	}
	enumType, err := policyir.NewTypeRef(policyir.ValueEnum, false, 0, 0, policyir.EnumID{1}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := mutationir.NewSet(field, enumType, enum)
	if err != nil {
		t.Fatal(err)
	}
	_, err = membershipFrozenInput(membershipTestApp(), policyir.ModelID{15: 1}, []mutationir.ScalarOperation{operation})
	if err == nil || !strings.Contains(err.Error(), "membership assignment value") {
		t.Fatalf("enum without an active wire value error=%v", err)
	}
}
