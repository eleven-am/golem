package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestNestedFilterExpansionCycleIsPolicyInvalidAcrossProviders(t *testing.T) {
	handles := newReachHandles(schematest.NewIndexedFullExactScopedNullableAuthor(t))
	policies := reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(golem.All[testUser]())
			rules.CanUpdate(golem.All[testUser]())
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanRead(handles.author.Is(handles.userName.StartsWith("a")))
			rules.CanDelete(golem.All[testPost]())
		},
	}
	runWriteReachScenario(t, "cycle_filter", policies, func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
		where := handles.author.Is(handles.written.Some(handles.title.Eq("x")))
		input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedDeleteMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, where))
		_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), input)
		return err
	}, func(t *testing.T, err error, before, after map[string]string) {
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != golem.ReasonPolicyInvalid {
			t.Fatalf("cyclic nested filter err=%v failure=%#v", err, failure)
		}
		assertOnlyChanged(t, nil, before, after)
	})
}

func TestNestedBatchUnderFieldScopedReadWritesReadableRowsAcrossProviders(t *testing.T) {
	fixture := schematest.NewIndexedFullExactScopedNullableAuthor(t)
	handles := newReachHandles(fixture)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	policies := reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(golem.All[testUser]())
			rules.CanUpdate(golem.All[testUser]())
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanReadFields(author.Eq(writeReachUUID(reachUsers[3].id)).And(handles.title.StartsWith("allowed-")), handles.postID, handles.title)
			rules.CanDelete(golem.All[testPost]())
		},
	}
	for name, where := range map[string]golem.Predicate[testPost]{"all": golem.All[testPost](), "title": handles.title.EndsWith("public")} {
		where := where
		t.Run(name, func(t *testing.T) {
			runWriteReachScenario(t, "fields_batch", policies, func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
				input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedDeleteMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, where))
				_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), input)
				return err
			}, func(t *testing.T, err error, before, after map[string]string) {
				assertOnlyChanged(t, err, before, after, reachPosts[3].id)
			})
		})
	}
}
