package plan

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func TestPolicyOnlyHydrationNarrowsItsRowCapThroughTheSameOwnerAsTheCallerPlan(t *testing.T) {
	fixture := schematest.NewWithMaxTake(t, 0, 2)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(golem.All[planUser]())
	userRules.CannotReadFields(golem.All[planUser](), userName)
	userRules.CanReadFields(posts.Some(author.Is(userName.Eq("owner"))), userName)
	frozenUser, err := userRules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	userPolicy, err := policybind.Policy(frozenUser, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	userPolicy, err = normalize.Policy(userPolicy)
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{
		policyir.ModelID(fixture.User): userPolicy,
		policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post),
	}
	frozen, err := golem.FreezeFindMany(descriptor, golem.Select[planUser](userName, posts.Select(postTitle)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(request, fixture.Registry, policies, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Hydrations()) != 1 {
		t.Fatalf("private hydrations = %d, want the policy-only Post traversal", len(planned.Hydrations()))
	}
	hydration := planned.Hydrations()[0].Child()
	if take, ok := hydration.Take(); !ok || take != 3 || hydration.ResultLimit() != 2 {
		t.Fatalf("policy-only hydration take=%d present=%t resultLimit=%d, want the model contract cap of 2 fetched as 3", take, ok, hydration.ResultLimit())
	}
	if take, ok := planned.Relations()[0].Child().Take(); !ok || take != 3 {
		t.Fatalf("caller relation take=%d present=%t, want the same owner to apply the same model cap", take, ok)
	}
	if readir.NarrowCap(0, 2) != 2 || readir.NarrowCap(5, 2) != 2 || readir.NarrowCap(2, 5) != 2 || readir.NarrowCap(2, 0) != 2 {
		t.Fatal("the row-cap owner no longer treats zero as unbounded")
	}
}
