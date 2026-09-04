package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func systemModeCreate(t *testing.T, fixture schematest.Fixture) golem.FrozenMutationInput {
	t.Helper()
	uuid := golem.NewUUID([16]byte{8})
	return freezeCreate(t, golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[bindPost, golem.UUID](fixture.PostID), uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[bindPost, golem.UUID](fixture.AuthorID), uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedTextField[bindPost, string](fixture.PostTitle), "owned"),
	))
}

func TestBindKeepsSystemFieldWritableForBothCreateAndUpdate(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem}})
	if _, err := CreateInput(systemModeCreate(t, fixture), fixture.Registry); err != nil {
		t.Fatalf("system create rejected: %v", err)
	}
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	if _, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "owned"))), fixture.Registry); err != nil {
		t.Fatalf("system update rejected: %v", err)
	}
}

func TestBindKeepsSystemImmutableFieldWritableOnlyAtCreate(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}})
	if _, err := CreateInput(systemModeCreate(t, fixture), fixture.Registry); err != nil {
		t.Fatalf("system immutable create rejected: %v", err)
	}
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "x"))), fixture.Registry)
	assertBindCode(t, err, CodeExposure, fixture.PostTitle)
}
