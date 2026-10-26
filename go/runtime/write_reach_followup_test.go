package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func runWriteReachScenario(t *testing.T, purpose string, policies reachPolicies, write func(context.Context, *Caller[testPrincipal, testActor], schematest.Fixture, reachHandles) error, check func(*testing.T, error, map[string]string, map[string]string)) {
	t.Helper()
	fixture := schematest.NewIndexedExactScoped(t)
	handles := newReachHandles(fixture)
	for index, open := range reachDatabases(t, purpose, fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			ctx := context.Background()
			target := open(t)
			before := postTitles(t, ctx, target, fixture, handles)
			app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, policies)
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			err = write(ctx, caller, fixture, handles)
			check(t, err, before, postTitles(t, ctx, target, fixture, handles))
		})
	}
}

func batchReachPolicies(handles reachHandles) reachPolicies {
	return reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(golem.All[testUser]())
			rules.CanUpdate(golem.All[testUser]())
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanRead(handles.title.StartsWith("allowed-"))
			rules.CanUpdate(golem.All[testPost]())
			rules.CanDelete(golem.All[testPost]())
		},
	}
}

func daveTarget(fixture schematest.Fixture) golem.MutationTarget[testUser] {
	return writeReachUserTarget(fixture, reachUsers[3].id)
}

func assertOnlyChanged(t *testing.T, err error, before, after map[string]string, changed ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("write refused: %v", err)
	}
	allowed := make(map[string]bool, len(changed))
	for _, id := range changed {
		allowed[id] = true
	}
	for id, title := range before {
		current, present := after[id]
		moved := !present || current != title
		if moved != allowed[id] {
			t.Errorf("post %s changed=%v want=%v (before=%q after=%q present=%v)", id, moved, allowed[id], title, current, present)
		}
	}
}

func TestNestedConstantBatchWritesOnlyReadableRowsAcrossProviders(t *testing.T) {
	handles := newReachHandles(schematest.NewIndexedExactScoped(t))
	t.Run("updateMany", func(t *testing.T) {
		runWriteReachScenario(t, "batch_all_update", batchReachPolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
			data := golem.GeneratedUpdateManyInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, "allowed-batch"))
			input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedUpdateMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, golem.All[testPost](), data))
			_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), input)
			return err
		}, func(t *testing.T, err error, before, after map[string]string) {
			assertOnlyChanged(t, err, before, after, reachPosts[3].id)
		})
	})
	t.Run("deleteMany", func(t *testing.T) {
		runWriteReachScenario(t, "batch_all_delete", batchReachPolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
			input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedDeleteMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, golem.All[testPost]()))
			_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), input)
			return err
		}, func(t *testing.T, err error, before, after map[string]string) {
			assertOnlyChanged(t, err, before, after, reachPosts[3].id)
		})
	})
}

func TestNestedBatchRelationFilterDoesNotProbeUnreadableRowsAcrossProviders(t *testing.T) {
	handles := newReachHandles(schematest.NewIndexedExactScoped(t))
	for name, want := range map[string][]string{"denied-public": nil, "nothing": nil, "allowed-public": {reachPosts[3].id}} {
		name, want := name, want
		t.Run(name, func(t *testing.T) {
			runWriteReachScenario(t, "batch_probe", batchReachPolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
				where := handles.author.Is(handles.written.Some(handles.title.Eq(name)))
				input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedDeleteMany[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, where))
				_, err := CallerUpdate(ctx, caller, handles.users, daveTarget(fixture), input)
				return err
			}, func(t *testing.T, err error, before, after map[string]string) {
				assertOnlyChanged(t, err, before, after, want...)
			})
		})
	}
}

func upsertWithoutCreatePolicies(handles reachHandles) reachPolicies {
	return reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(golem.All[testUser]())
			rules.CanUpdate(golem.All[testUser]())
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanRead(handles.title.StartsWith("allowed-"))
			rules.CanUpdate(golem.All[testPost]())
		},
	}
}

func assertNotGrantedAndUnchanged(t *testing.T, err error, before, after map[string]string) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != golem.ReasonNotGranted {
		t.Fatalf("upsert without a create grant err=%v", err)
	}
	assertOnlyChanged(t, nil, before, after)
}

func TestUpsertWithoutCreateGrantIsNotAnExistenceOracleAcrossProviders(t *testing.T) {
	handles := newReachHandles(schematest.NewIndexedExactScoped(t))
	missing := reachPost{id: "00000000-0000-0000-0000-000000000019", author: reachUsers[1].id, title: "missing"}
	for name, post := range map[string]reachPost{"unreadable": reachPosts[2], "missing": missing} {
		post := post
		t.Run("root-"+name, func(t *testing.T) {
			runWriteReachScenario(t, "upsert_oracle", upsertWithoutCreatePolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
				return upsertWrite(ctx, caller, fixture, handles, post)
			}, assertNotGrantedAndUnchanged)
		})
		t.Run("nested-"+name, func(t *testing.T) {
			runWriteReachScenario(t, "nested_upsert_oracle", upsertWithoutCreatePolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
				create := golem.GeneratedCreateInput[testPost](fixture.Post,
					golem.GeneratedCreateFieldValue(fixture.Post, handles.postID, writeReachUUID(post.id)),
					golem.GeneratedCreateFieldValue(fixture.Post, handles.title, "allowed-created"),
					golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[testPost, int64](fixture.PostBigInt), int64(0)),
					golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[testPost, golem.Decimal](fixture.PostDecimal), writeReachDecimal()),
				)
				update := golem.GeneratedUpdateInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, "allowed-updated"))
				input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedUpsert[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, writeReachTarget(fixture, post), create, update))
				_, err := CallerUpdate(ctx, caller, handles.users, writeReachUserTarget(fixture, post.author), input)
				return err
			}, assertNotGrantedAndUnchanged)
		})
	}
	t.Run("readable-updates", func(t *testing.T) {
		runWriteReachScenario(t, "upsert_readable", upsertWithoutCreatePolicies(handles), func(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) error {
			return upsertWrite(ctx, caller, fixture, handles, reachPosts[1])
		}, func(t *testing.T, err error, before, after map[string]string) {
			assertOnlyChanged(t, err, before, after, reachPosts[1].id)
		})
	})
}
