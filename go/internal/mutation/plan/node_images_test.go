package plan

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type imagePost struct{}
type imageUser struct{}

func TestDeriveNodeImagesIncludesDirectRelationAndHookSnapshotDependencies(t *testing.T) {
	fixture := schematest.New(t)
	author := golem.GeneratedToOne[imagePost, imageUser](fixture.PostAuthor, fixture.Authorship)
	name := golem.GeneratedTextField[imageUser, string](fixture.UserName)
	descriptor := golem.GeneratedModelDescriptor[imagePost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := author.Is(name.Eq("Ada")).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := mutationbind.BatchPredicate(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, condition)
	authorization, _ := mutationir.NewFieldAuthorization(policyir.FieldID(fixture.PostTitle), condition)
	hook, _ := mutationir.NewHookRequirement(mutationir.TransactionAfterHook, mutationir.HookUpdate)
	node := mutationir.NodeInput{
		Operation: mutationir.Update, Model: policyir.ModelID(fixture.Post),
		Selection: &selection, RowPostcondition: &condition,
		FieldConditions: []mutationir.FieldAuthorization{authorization}, Hooks: []mutationir.HookRequirement{hook},
	}
	request := NodeImageRequest{Registry: fixture.Registry, Node: node}
	before, after, err := DeriveNodeImages(request)
	if err != nil {
		t.Fatal(err)
	}
	wantScalar := []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}
	assertImageFields(t, before.Fields(), wantScalar...)
	assertImageFields(t, after.Fields(), wantScalar...)
	assertRelationDependency(t, before.Dependencies(), policyir.FieldID(fixture.PostAuthor), policyir.FieldID(fixture.UserName))
	assertRelationDependency(t, after.Dependencies(), policyir.FieldID(fixture.PostAuthor), policyir.FieldID(fixture.UserName))

	beforeAgain, afterAgain, err := DeriveNodeImages(request)
	if err != nil || !reflect.DeepEqual(before, beforeAgain) || !reflect.DeepEqual(after, afterAgain) {
		t.Fatalf("image derivation is not deterministic: err=%v", err)
	}
	if _, _, err := DeriveNodeImages(NodeImageRequest{Node: node}); err == nil {
		t.Fatal("missing active registry did not fail closed")
	}
}

func assertImageFields(t *testing.T, got []policyir.FieldID, want ...policyir.FieldID) {
	t.Helper()
	set := make(map[policyir.FieldID]bool, len(got))
	for _, field := range got {
		set[field] = true
	}
	for _, field := range want {
		if !set[field] {
			t.Fatalf("image fields %#v omit %x", got, field)
		}
	}
}

func assertRelationDependency(t *testing.T, dependencies []mutationir.Dependency, relation, terminal policyir.FieldID) {
	t.Helper()
	for _, dependency := range dependencies {
		path := dependency.Path()
		if len(path) == 1 && path[0].FieldID() == relation && dependency.FieldID() == terminal {
			return
		}
	}
	t.Fatalf("dependencies %#v omit relation %x terminal %x", dependencies, relation, terminal)
}
