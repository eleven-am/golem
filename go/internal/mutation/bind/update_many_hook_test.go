package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestUpdateManyInputFromHookMarksOnlyTheNamedSystemField(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{AuthorID: []compilerir.FieldMode{compilerir.ModeSystem}})
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	author := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.AuthorID)
	frozen := freezeUpdateMany(t, golem.GeneratedUpdateManyInput(fixture.Post,
		golem.GeneratedSetFieldValue(fixture.Post, title, "hooked"),
		golem.GeneratedSetFieldValue(fixture.Post, author, golem.NewUUID([16]byte{2})),
	))
	bound, err := UpdateManyInputFromHook(frozen, fixture.Registry, []golem.FieldID{fixture.AuthorID})
	if err != nil {
		t.Fatalf("hook-authored update-many system field rejected: %v", err)
	}
	if bound.Kind() != InputUpdateMany {
		t.Fatalf("kind=%v, want update-many", bound.Kind())
	}
	marked := map[policyir.FieldID]bool{}
	for _, operation := range bound.Operations() {
		marked[operation.FieldID()] = operation.HookAuthored()
	}
	if len(marked) != 2 || !marked[policyir.FieldID(fixture.AuthorID)] || marked[policyir.FieldID(fixture.PostTitle)] {
		t.Fatalf("hook-authored marks=%v, want only the system author field", marked)
	}
}

func TestUpdateManyInputFromHookRefusesNonSystemAndImmutableFields(t *testing.T) {
	plain := schematest.NewWithContractModes(t, schematest.ContractModes{})
	title := golem.GeneratedTextField[bindPost, string](plain.PostTitle)
	frozen := freezeUpdateMany(t, golem.GeneratedUpdateManyInput(plain.Post, golem.GeneratedSetFieldValue(plain.Post, title, "hooked")))
	_, err := UpdateManyInputFromHook(frozen, plain.Registry, []golem.FieldID{plain.PostTitle})
	assertBindCode(t, err, CodeExposure, plain.PostTitle)

	immutable := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}})
	immutableTitle := golem.GeneratedTextField[bindPost, string](immutable.PostTitle)
	frozen = freezeUpdateMany(t, golem.GeneratedUpdateManyInput(immutable.Post, golem.GeneratedSetFieldValue(immutable.Post, immutableTitle, "hooked")))
	_, err = UpdateManyInputFromHook(frozen, immutable.Registry, []golem.FieldID{immutable.PostTitle})
	assertBindCode(t, err, CodeExposure, immutable.PostTitle)
}
