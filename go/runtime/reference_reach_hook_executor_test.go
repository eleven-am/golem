package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type referenceHookTarget struct{}

type referenceHookRequest struct {
	operation string
	author    golem.UUID
}

func TestHookExecutorForeignKeyWritesReportUnreadableTargetsAsMissingAcrossProviders(t *testing.T) {
	var mu sync.Mutex
	var fixture mutationResultFixture
	outcomes := map[string]string{}
	configure := func(schema schematest.Fixture, config *Config[mutationResultPrincipal, mutationResultActor]) {
		userName := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		authorID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		postID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID)
		users := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultUser]()
			rules.CanRead(userName.Eq("alice"))
			rules.CanCreate(golem.All[mutationResultUser]())
			rules.CanUpdate(golem.All[mutationResultUser]())
			return rules.Freeze(schema.User)
		})
		posts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultPost]()
			rules.CanRead(golem.All[mutationResultPost]())
			rules.CanCreate(golem.All[mutationResultPost]())
			rules.CanUpdate(golem.All[mutationResultPost]())
			return rules.Freeze(schema.Post)
		})
		after := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			request, ok := hookContext.Value(referenceHookTarget{}).(referenceHookRequest)
			if !ok {
				return nil
			}
			hookContext = context.WithValue(hookContext, referenceHookTarget{}, nil)
			var count int64
			var err error
			if request.operation == "create" {
				_, err = golem.HookCreateRow(hookContext, result.Executor(), fixture.postDescriptor, fixture.createPost(request.author[15]+100, request.author, "hook-created"))
			} else {
				count, err = golem.HookUpdateManyRows(hookContext, result.Executor(), fixture.postDescriptor, postID.Eq(golem.UUID{15: 90}),
					golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, authorID, request.author)))
			}
			mu.Lock()
			outcomes[request.operation+" "+request.author.String()] = referenceHookOutcome(count, err)
			mu.Unlock()
			return err
		})
		bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{users, posts}, []golem.HookBinding[mutationResultActor]{after}))
		if err != nil {
			t.Fatal(err)
		}
		config.Bindings = bindings
	}
	runConfiguredRelationDeleteProviderProfiles(t, "hook_reference", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, configure, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture = profile.fixture
		mu.Lock()
		clear(outcomes)
		mu.Unlock()
		caller := mustMutationResultCaller(t, fixture)
		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(90, golem.UUID{15: 1}, "anchor")); err != nil {
			t.Fatal(err)
		}
		bob, missing := golem.UUID{15: 2}, golem.UUID{15: 250}
		next := byte(91)
		for _, operation := range []string{"create", "update many"} {
			for _, author := range []golem.UUID{bob, missing} {
				request := referenceHookRequest{operation: operation, author: author}
				if _, err := CallerCreate(context.WithValue(ctx, referenceHookTarget{}, request), caller, fixture.postDescriptor, fixture.createPost(next, golem.UUID{15: 1}, "outer")); err == nil {
					t.Errorf("hook executor %s to %s committed", operation, author)
				}
				next++
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, operation := range []string{"create", "update many"} {
			unreadable, absent := outcomes[operation+" "+bob.String()], outcomes[operation+" "+missing.String()]
			if unreadable == "" || unreadable != absent || unreadable[:2] == "ok" {
				t.Errorf("hook executor %s unreadable=%q missing=%q", operation, unreadable, absent)
			}
		}
		var linked int
		query := `SELECT COUNT(*) FROM ` + profile.posts + ` WHERE "author_id"=` + profile.placeholder(1)
		if err := fixture.app.database.GetContext(ctx, &linked, query, mutationResultUUIDText(2)); err != nil || linked != 0 {
			t.Fatalf("hook executor linked posts to an unreadable user: count=%d err=%v", linked, err)
		}
	})
}

func referenceHookOutcome(count int64, err error) string {
	var failure *golem.Error
	if errors.As(err, &failure) {
		return fmt.Sprintf("%s: %s", failure.Code, failure.Message)
	}
	if err != nil {
		return "untyped: " + err.Error()
	}
	return fmt.Sprintf("ok count=%d", count)
}
