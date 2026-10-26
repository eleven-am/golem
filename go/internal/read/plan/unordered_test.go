package plan

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func TestUnorderedRequestSkipsThePrimaryKeyTieBreaker(t *testing.T) {
	fixture := schematest.New(t)
	name, err := readir.NewScalarSelection(policyir.FieldID(fixture.UserName))
	if err != nil {
		t.Fatal(err)
	}
	plans := map[bool]Plan{}
	for _, unordered := range []bool{false, true} {
		request, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.User), Projection: readir.ProjectionSelect, Selection: []readir.Selection{name}, Unordered: unordered})
		if err != nil {
			t.Fatal(err)
		}
		plans[unordered], err = System(request, fixture.Registry, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
	}
	ordered := plans[false].OrderBy()
	if len(ordered) != 1 || ordered[0].FieldID() != policyir.FieldID(fixture.UserID) {
		t.Fatalf("an ordered findMany has orders %#v, want the primary-key tie-breaker", ordered)
	}
	if orders := plans[true].OrderBy(); len(orders) != 0 {
		t.Fatalf("an unordered findMany has orders %#v", orders)
	}
	for _, field := range plans[true].Fields() {
		if field.FieldID() == policyir.FieldID(fixture.UserID) {
			t.Fatal("an unordered findMany injected the primary key as a private order dependency")
		}
	}
}
