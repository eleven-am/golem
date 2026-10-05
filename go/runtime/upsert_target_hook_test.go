package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestHookRewriteOfAStaticallyMatchingCreateIsRefusedAfterTheWrite(t *testing.T) {
	var rewrites int
	var rewrittenAuthor *golem.UUID
	hooks := func(schema schematest.GraphFixture) []golem.HookBinding[graphMutationActor] {
		postID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.PostID)
		postTitle := golem.GeneratedTextField[graphMutationPost, string](schema.PostTitle)
		authorID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationPost]) error {
				rewrites++
				values := []golem.CreateValue[graphMutationPost]{
					golem.GeneratedCreateFieldValue(schema.Post, postID, golem.UUID{15: 99}),
					golem.GeneratedCreateFieldValue(schema.Post, postTitle, "rewritten"),
				}
				if rewrittenAuthor != nil {
					values = append(values, golem.GeneratedCreateFieldValue(schema.Post, authorID, *rewrittenAuthor))
				}
				request.ReplaceInput(golem.GeneratedCreateInput(schema.Post, values...))
				return nil
			}),
		}
	}
	runRuntimeDefaultGraphProfiles(t, hooks, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		owner := byte(62)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserCreate(fixture, &owner, "owner")); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		postTarget := golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 63}))
		postCreate := golem.GeneratedCreateInput(fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 63}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "matching"))
		postUpdate := golem.GeneratedUpdateInput(fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "unused"))
		t.Run("root upsert", func(t *testing.T) {
			rewrites = 0
			author := golem.UUID{15: owner}
			rewrittenAuthor = &author
			defer func() { rewrittenAuthor = nil }()
			create := golem.GeneratedCreateInput(fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 63}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID), author),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "matching"))
			_, err := CallerUpsert(ctx, caller, fixture.postDescriptor, postTarget, create, postUpdate)
			assertTargetIdentityRefusal(t, err)
			if rewrites != 1 {
				t.Fatalf("the rewriting create hook ran %d times, want 1", rewrites)
			}
			if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 0 {
				t.Fatalf("hook-rewritten root upsert left %d posts", got)
			}
		})
		for name, input := range map[string]golem.UpdateInput[graphMutationUser]{
			"upsert": golem.GeneratedUpdateInput(fixture.schema.User,
				golem.GeneratedNestedUpsert[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget, postCreate, postUpdate)),
			"connectOrCreate": golem.GeneratedUpdateInput(fixture.schema.User,
				golem.GeneratedNestedConnectOrCreate[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget, postCreate)),
		} {
			t.Run(name, func(t *testing.T) {
				rewrites = 0
				_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, graphUserTarget(fixture, owner), input)
				assertTargetIdentityRefusal(t, err)
				if rewrites != 1 {
					t.Fatalf("the rewriting create hook ran %d times, want 1", rewrites)
				}
				if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 0 {
					t.Fatalf("hook-rewritten create left %d posts", got)
				}
			})
		}
	})
}
