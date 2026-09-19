package nested

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func foreignKeyModeFixture(t *testing.T, mode compilerir.FieldMode) schematest.Fixture {
	return schematest.NewWithContractModes(t, schematest.ContractModes{AuthorID: []compilerir.FieldMode{mode}})
}

func inverseConnect(t *testing.T, fixture schematest.Fixture) []golem.FrozenNestedMutation {
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	return freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedConnect[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector),
	))
}

func inverseCreate(t *testing.T, fixture schematest.Fixture) []golem.FrozenNestedMutation {
	return freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, nestedPostCreate(t, fixture)),
	))
}

func buildForeignKeyMode(t *testing.T, fixture schematest.Fixture, stance mutationir.Stance, mutations []golem.FrozenNestedMutation) error {
	request := Request{Root: systemRoot(t, fixture), Mutations: mutations, Stance: stance, Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10}
	if stance == mutationir.Caller {
		request.Root, request.Policies = callerRoot(t, fixture), allowPolicies(t, fixture)
	}
	_, err := Build(request)
	return err
}

func TestNestedRelationWriteRefusesAModedForeignKeyWithTheDirectWriteReason(t *testing.T) {
	for mode, reason := range map[compilerir.FieldMode]string{
		compilerir.ModeSystem:    "system field is not caller writable",
		compilerir.ModeReadOnly:  "read-only field is not writable",
		compilerir.ModeImmutable: "immutable field is writable only during create",
		compilerir.ModeHidden:    "hidden field is not writable",
	} {
		fixture := foreignKeyModeFixture(t, mode)
		err := buildForeignKeyMode(t, fixture, mutationir.Caller, inverseConnect(t, fixture))
		if err == nil || !strings.Contains(err.Error(), "P4_NESTED_EXPOSURE") || !strings.Contains(err.Error(), reason) {
			t.Fatalf("%s foreign key connect: %v", mode, err)
		}
	}
}

func TestNestedCreateCorrelationTreatsAnImmutableForeignKeyLikeADirectCreate(t *testing.T) {
	fixture := foreignKeyModeFixture(t, compilerir.ModeImmutable)
	if err := buildForeignKeyMode(t, fixture, mutationir.Caller, inverseCreate(t, fixture)); err != nil {
		t.Fatalf("immutable foreign key at create was refused: %v", err)
	}
	fixture = foreignKeyModeFixture(t, compilerir.ModeSystem)
	if err := buildForeignKeyMode(t, fixture, mutationir.Caller, inverseCreate(t, fixture)); err == nil || !strings.Contains(err.Error(), "system field is not caller writable") {
		t.Fatalf("system foreign key at caller create: %v", err)
	}
}

func TestNestedSystemStanceMayAssignASystemForeignKeyButNotAReadOnlyOne(t *testing.T) {
	fixture := foreignKeyModeFixture(t, compilerir.ModeSystem)
	if err := buildForeignKeyMode(t, fixture, mutationir.System, inverseConnect(t, fixture)); err != nil {
		t.Fatalf("system stance connect over a system foreign key was refused: %v", err)
	}
	fixture = foreignKeyModeFixture(t, compilerir.ModeReadOnly)
	if err := buildForeignKeyMode(t, fixture, mutationir.System, inverseConnect(t, fixture)); err == nil || !strings.Contains(err.Error(), "read-only field is not writable") {
		t.Fatalf("system stance connect over a read-only foreign key: %v", err)
	}
}

func TestNestedSystemStanceMayNotAssignAHiddenForeignKey(t *testing.T) {
	fixture := foreignKeyModeFixture(t, compilerir.ModeHidden)
	if err := buildForeignKeyMode(t, fixture, mutationir.System, inverseConnect(t, fixture)); err == nil || !strings.Contains(err.Error(), "hidden field is not writable") {
		t.Fatalf("system stance connect over a hidden foreign key: %v", err)
	}
	if err := buildForeignKeyMode(t, fixture, mutationir.Caller, inverseCreate(t, fixture)); err == nil || !strings.Contains(err.Error(), "hidden field is not writable") {
		t.Fatalf("hidden foreign key at caller create: %v", err)
	}
}
