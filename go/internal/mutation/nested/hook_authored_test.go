package nested

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func systemTitleFixture(t *testing.T) schematest.Fixture {
	return schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeSystem}})
}

func entryHookAuthoredRequest(t *testing.T, fixture schematest.Fixture, mutations []golem.FrozenNestedMutation, authored ...golem.FieldID) Request {
	return Request{Root: callerRoot(t, fixture), Mutations: mutations, Stance: mutationir.Caller, Registry: fixture.Registry, Policies: allowPolicies(t, fixture), MaxDepth: 5, MaxRows: 10, EntryHookAuthored: authored}
}

func assertEntryTitleHookAuthored(t *testing.T, result Result, fixture schematest.Fixture, operation mutationir.Operation) {
	t.Helper()
	found := false
	for _, node := range result.Graph().Nodes() {
		if node.ModelID() != policyir.ModelID(fixture.Post) || node.Operation() != operation {
			continue
		}
		for _, scalar := range node.ScalarOperations() {
			if scalar.FieldID() == policyir.FieldID(fixture.PostTitle) {
				found = true
				if !scalar.HookAuthored() {
					t.Fatal("entry system field was bound without hook authorship")
				}
			}
		}
		for _, authorization := range node.FieldAuthorizations() {
			if authorization.FieldID() == policyir.FieldID(fixture.PostTitle) {
				t.Fatal("hook-authored system field was given a caller field authorization")
			}
		}
	}
	if !found {
		t.Fatal("entry node carrying the hook-authored field is absent")
	}
}

func TestNestedEntryHookAuthoredSystemFieldBindsOnUpdate(t *testing.T) {
	fixture := systemTitleFixture(t)
	result, err := Build(entryHookAuthoredRequest(t, fixture, nestedSystemTitleUpdate(t, fixture), fixture.PostTitle))
	if err != nil {
		t.Fatalf("hook-authored entry system field was refused: %v", err)
	}
	assertEntryTitleHookAuthored(t, result, fixture, mutationir.Update)
}

func TestNestedEntryHookAuthoredSystemFieldBindsOnCreate(t *testing.T) {
	fixture := systemTitleFixture(t)
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, nestedPostCreate(t, fixture)),
	))
	result, err := Build(entryHookAuthoredRequest(t, fixture, mutations, fixture.PostTitle))
	if err != nil {
		t.Fatalf("hook-authored entry system field was refused: %v", err)
	}
	assertEntryTitleHookAuthored(t, result, fixture, mutationir.Create)
}

func TestNestedEntryHookAuthoredRequiresExactlyOneSelectedEntry(t *testing.T) {
	fixture := systemTitleFixture(t)
	_, err := Build(entryHookAuthoredRequest(t, fixture, allNestedMutations(t, fixture), fixture.PostTitle))
	if err == nil || !strings.Contains(err.Error(), "one selected create, update, or update-many entry") {
		t.Fatalf("hook authorship was accepted without one selected entry: %v", err)
	}
}

func TestNestedEntryHookAuthoredDoesNotReachDescendants(t *testing.T) {
	fixture := systemTitleFixture(t)
	title := golem.GeneratedTextField[nestedPost, string](fixture.PostTitle)
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	grandchild := golem.GeneratedUpdateInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "descendant"))
	author := golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedUpdate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, grandchild),
	)
	entry := golem.GeneratedUpdateInput[nestedPost](fixture.Post,
		golem.GeneratedSetFieldValue(fixture.Post, title, "entry"),
		golem.GeneratedNestedUpdate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil, author),
	)
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedUpdate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, entry),
	))
	_, err := Build(entryHookAuthoredRequest(t, fixture, mutations, fixture.PostTitle))
	if err == nil || !strings.Contains(err.Error(), "system field is not caller writable") {
		t.Fatalf("entry hook authorship reached a descendant's system field: %v", err)
	}
}

func TestNestedEntryHookAuthoredSystemFieldBindsOnUpdateMany(t *testing.T) {
	fixture := systemTitleFixture(t)
	updateMany := golem.GeneratedUpdateManyInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "many"))
	predicate := golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID).Eq(golem.NewUUID([16]byte{3}))
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedUpdateMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, predicate, updateMany),
	))
	result, err := Build(entryHookAuthoredRequest(t, fixture, mutations, fixture.PostTitle))
	if err != nil {
		t.Fatalf("hook-authored entry system field was refused: %v", err)
	}
	assertEntryTitleHookAuthored(t, result, fixture, mutationir.UpdateMany)
}
