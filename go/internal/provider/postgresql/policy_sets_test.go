package postgresql

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestPolicyPostgreSQLTwoThousandUUIDSetUsesOneTypedArrayParameter(t *testing.T) {
	resolver := newPolicyTestResolver("policy_sets")
	field := resolver.fields[policyFieldKey{policyModel, policyText}]
	var err error
	field.Type, err = policyir.NewTypeRef(policyir.ValueUUID, true, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	resolver.fields[policyFieldKey{policyModel, policyText}] = field
	values := make([]policyir.Value, 2000)
	for i := range values {
		var id [16]byte
		binary.BigEndian.PutUint32(id[12:], uint32(i+1))
		values[i] = policyir.UUIDValue(id)
	}
	operand, err := policyir.ManyOperand(values)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []policyir.OperatorID{policyir.OperatorIn, policyir.OperatorNotIn} {
		fragment := compilePolicyTest(t, resolver, policyScalar(t, resolver, operation, policyir.ComparisonSensitive, operand))
		if len(fragment.Args()) != 1 {
			t.Fatalf("2,000 IDs expanded to %d binds; want one typed set", len(fragment.Args()))
		}
		if !strings.Contains(fragment.SQL(), "= ANY (ARRAY(SELECT golem_set.value::uuid") || !strings.Contains(fragment.SQL(), "pg_catalog.jsonb_array_elements_text($1::jsonb)") {
			t.Fatalf("set does not lower to an exact typed array: %s", fragment.SQL())
		}
		var ids []string
		if err := json.Unmarshal([]byte(fragment.Args()[0].(string)), &ids); err != nil || len(ids) != 2000 || ids[0] != "00000000-0000-0000-0000-000000000001" {
			t.Fatalf("UUID set codec: count=%d err=%v", len(ids), err)
		}
	}
}
