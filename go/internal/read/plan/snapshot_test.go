package plan

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func snapshotRequest(t *testing.T, model golem.ModelID, identity golem.FieldID) readir.Request {
	t.Helper()
	selection, err := readir.NewScalarSelection(policyir.FieldID(identity))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readir.NewRequest(readir.RequestInput{
		Operation: readir.FindMany, Model: policyir.ModelID(model), Selection: []readir.Selection{selection}, Projection: readir.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func snapshotTree(t *testing.T, fixture schematest.Fixture, model golem.ModelID, policy policyir.Policy) dependency.Tree {
	t.Helper()
	row, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(model))
	if err != nil {
		t.Fatal(err)
	}
	collected, err := dependency.Collect(row)
	if err != nil {
		t.Fatal(err)
	}
	return collected.Dependencies()
}

func TestSystemSnapshotLoadsScalarsAndToOneHopsButNeverToMany(t *testing.T) {
	fixture := schematest.New(t)
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)

	postRules := golem.NewRules[planPost]()
	postRules.CanRead(golem.And(postTitle.Eq("t"), author.Is(userName.Eq("owner"))))
	postTree := snapshotTree(t, fixture, fixture.Post, bindPlanPolicy(t, fixture, fixture.Post, postRules))
	planned, err := SystemSnapshot(snapshotRequest(t, fixture.Post, fixture.PostID), fixture.Registry, DefaultLimits(), postTree)
	if err != nil {
		t.Fatal(err)
	}
	if !planned.IsSystem() {
		t.Fatal("snapshot plan is not a system plan")
	}
	loaded := map[policyir.FieldID]bool{}
	for _, field := range planned.Fields() {
		loaded[field.FieldID()] = true
	}
	for _, want := range []golem.FieldID{fixture.PostID, fixture.PostTitle, fixture.AuthorID} {
		if !loaded[policyir.FieldID(want)] {
			t.Fatalf("snapshot did not load field %x: %#v", want, planned.Fields())
		}
	}
	hydrations := planned.Hydrations()
	if len(hydrations) != 1 || hydrations[0].FieldID() != policyir.FieldID(fixture.PostAuthor) || hydrations[0].ToMany() {
		t.Fatalf("snapshot hydrations = %#v, want the one to-one author hop", hydrations)
	}
	child := hydrations[0].Child()
	childLoaded := false
	for _, field := range child.Fields() {
		childLoaded = childLoaded || field.FieldID() == policyir.FieldID(fixture.UserName)
	}
	if !childLoaded {
		t.Fatalf("author hop did not load the name the condition reads: %#v", child.Fields())
	}

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(posts.Some(postTitle.Eq("t")))
	userTree := snapshotTree(t, fixture, fixture.User, bindPlanPolicy(t, fixture, fixture.User, userRules))
	userPlan, err := SystemSnapshot(snapshotRequest(t, fixture.User, fixture.UserID), fixture.Registry, DefaultLimits(), userTree)
	if err != nil {
		t.Fatal(err)
	}
	if len(userPlan.Hydrations()) != 0 {
		t.Fatalf("a to-many dependency was hydrated: %#v", userPlan.Hydrations())
	}
}

func TestSystemSnapshotRefusesEveryInvalidInput(t *testing.T) {
	fixture := schematest.New(t)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	rules := golem.NewRules[planPost]()
	rules.CanRead(postTitle.Eq("t"))
	postTree := snapshotTree(t, fixture, fixture.Post, bindPlanPolicy(t, fixture, fixture.Post, rules))

	if _, err := SystemSnapshot(snapshotRequest(t, fixture.User, fixture.UserID), fixture.Registry, DefaultLimits(), postTree); err == nil {
		t.Fatal("a dependency tree rooted at another model was accepted")
	}
	absent := snapshotRequest(t, golem.ModelID{1}, fixture.UserID)
	if _, err := SystemSnapshot(absent, fixture.Registry, DefaultLimits(), postTree); err == nil {
		t.Fatal("a request for an unknown model was accepted")
	}
	limits := DefaultLimits()
	limits.MaxSelected = 1
	_, err := SystemSnapshot(snapshotRequest(t, fixture.Post, fixture.PostID), fixture.Registry, limits, postTree)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("over-limit snapshot error = %v, want %s", err, CodeLimit)
	}
}
