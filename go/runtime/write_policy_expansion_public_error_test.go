package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestWritePolicyExpansionCycleAndDepthArePolicyInvalidAcrossProviders(t *testing.T) {
	handles := newReachHandles(schematest.NewIndexedFullExactScopedNullableAuthor(t))
	cyclic := handles.author.Is(handles.written.Some(handles.title.Eq("x")))
	deep := handles.title.Eq("x")
	for range 4 {
		deep = handles.author.Is(handles.written.Some(deep))
	}
	cyclicRead := func(rules *golem.Rules[testPost]) {
		rules.CanRead(handles.author.Is(handles.userName.StartsWith("a")))
	}
	allUsers := func(rules *golem.Rules[testUser]) {
		rules.CanRead(golem.All[testUser]())
		rules.CanUpdate(golem.All[testUser]())
	}
	allPosts := func(rules *golem.Rules[testPost]) {
		rules.CanRead(golem.All[testPost]())
		rules.CanUpdate(golem.All[testPost]())
		rules.CanDelete(golem.All[testPost]())
	}
	postTarget := func(fixture schematest.Fixture, id string) golem.UniqueSelectorValue[testPost] {
		return golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, writeReachUUID(id)))
	}
	retitle := func(fixture schematest.Fixture) golem.UpdateInput[testPost] {
		return golem.GeneratedUpdateInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, "retitled"))
	}
	retitleMany := func(fixture schematest.Fixture) golem.UpdateManyInput[testPost] {
		return golem.GeneratedUpdateManyInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, "retitled"))
	}
	updateDave := func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, nested golem.UpdateValue[testUser]) error {
		_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), golem.GeneratedUpdateInput[testUser](fixture.User, nested))
		return err
	}
	type write func(context.Context, *Caller[testPrincipal, testActor], schematest.Fixture) error
	tests := []struct {
		name     string
		policies reachPolicies
		write    write
	}{
		{
			name:     "root update with cyclic update policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(cyclic) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				_, err := CallerUpdate(ctx, caller, handles.posts, postTarget(fixture, reachPosts[0].id), retitle(fixture))
				return err
			},
		},
		{
			name:     "root delete with cyclic delete policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanDelete(cyclic) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				_, err := CallerDelete(ctx, caller, handles.posts, postTarget(fixture, reachPosts[0].id))
				return err
			},
		},
		{
			name:     "root deleteMany with cyclic delete policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanDelete(cyclic) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				_, err := CallerDeleteMany(ctx, caller, handles.posts, handles.title.StartsWith("allowed-"))
				return err
			},
		},
		{
			name:     "root updateMany with cyclic update policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(cyclic) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				_, err := CallerUpdateMany(ctx, caller, handles.posts, handles.title.StartsWith("allowed-"), retitleMany(fixture))
				return err
			},
		},
		{
			name:     "root delete with too-deep delete policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { rules.CanRead(golem.All[testPost]()); rules.CanDelete(deep) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				_, err := CallerDelete(ctx, caller, handles.posts, postTarget(fixture, reachPosts[0].id))
				return err
			},
		},
		{
			name:     "nested updateMany with cyclic filter",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(golem.All[testPost]()) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedUpdateMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, cyclic, retitleMany(fixture)))
			},
		},
		{
			name:     "nested deleteMany with too-deep filter",
			policies: reachPolicies{user: allUsers, post: allPosts},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedDeleteMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, deep))
			},
		},
		{
			name:     "nested update with cyclic target guard",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(golem.All[testPost]()) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedUpdate[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, postTarget(fixture, reachPosts[3].id).And(cyclic), retitle(fixture)))
			},
		},
		{
			name:     "nested delete with cyclic target guard",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanDelete(golem.All[testPost]()) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedDelete[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, postTarget(fixture, reachPosts[3].id).And(cyclic)))
			},
		},
		{
			name:     "nested set with cyclic target guard",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(golem.All[testPost]()) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedSet[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, postTarget(fixture, reachPosts[3].id).And(cyclic)))
			},
		},
		{
			name:     "nested update with cyclic update policy",
			policies: reachPolicies{user: allUsers, post: func(rules *golem.Rules[testPost]) { cyclicRead(rules); rules.CanUpdate(cyclic) }},
			write: func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture) error {
				return updateDave(ctx, caller, fixture, golem.GeneratedNestedUpdate[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, postTarget(fixture, reachPosts[3].id), retitle(fixture)))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runWriteReachScenario(t, "policy_expansion_error", test.policies, func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, _ reachHandles) error {
				return test.write(ctx, caller, fixture)
			}, func(t *testing.T, err error, before, after map[string]string) {
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != golem.ReasonPolicyInvalid {
					t.Fatalf("policy expansion refusal err=%v failure=%#v", err, failure)
				}
				if !errors.Is(err, golem.CodeForbidden) || !errors.Is(err, golem.ReasonPolicyInvalid) {
					t.Fatalf("errors.Is mismatch: %v", err)
				}
				if len(before) != len(after) {
					t.Fatalf("refused write changed row count before=%v after=%v", before, after)
				}
				for id, title := range before {
					if after[id] != title {
						t.Fatalf("refused write changed post %s before=%q after=%q", id, title, after[id])
					}
				}
			})
		})
	}
}
