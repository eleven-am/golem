package scoped

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestScopedStringOperandRejectsNUL(t *testing.T) {
	typ, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scopedPolicyValue("nul\x00byte", typ, nil, golem.FrozenScopedExpression{}); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("scoped string operand with NUL error=%v; want a NUL value-domain refusal", err)
	}
	if _, err := scopedPolicyValue("nul byte", typ, nil, golem.FrozenScopedExpression{}); err != nil {
		t.Fatal(err)
	}
}
