package nested

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func nestedSystemTitleUpdate(t *testing.T, fixture schematest.Fixture) []golem.FrozenNestedMutation {
	t.Helper()
	update := golem.GeneratedUpdateInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "updated"))
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	input := golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedUpdate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, update),
	)
	frozen, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return frozen.Relations()
}

func TestNestedCallerWriteRefusesSystemField(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem}})
	mutations := nestedSystemTitleUpdate(t, fixture)
	_, err := Build(Request{Root: callerRoot(t, fixture), Mutations: mutations, Stance: mutationir.Caller, Registry: fixture.Registry, Policies: allowPolicies(t, fixture), MaxDepth: 5, MaxRows: 10})
	if err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("nested caller write reached a system field: %v", err)
	}
}

func TestNestedSystemWriteKeepsSystemField(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem}})
	mutations := nestedSystemTitleUpdate(t, fixture)
	if _, err := Build(Request{Root: systemRoot(t, fixture), Mutations: mutations, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10}); err != nil {
		t.Fatalf("nested system write was refused a system field: %v", err)
	}
}
