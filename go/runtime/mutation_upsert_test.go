package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestRootUpsertTruthfullyCreatesThenUpdatesWithProjection(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	system := fixture.app.System()
	target := fixture.target(70)
	projection := golem.Select[mutationResultPost](fixture.postID, fixture.title)

	created, err := SystemUpsert(ctx, system, fixture.postDescriptor, target,
		fixture.createPost(70, golem.UUID{15: 1}, "created"), fixture.updateTitle("updated"), projection)
	if err != nil {
		t.Fatal(err)
	}
	if title, selected := golem.Value(created, fixture.title).Get(); !selected || title != "created" {
		t.Fatalf("created title=%q selected=%t", title, selected)
	}

	updated, err := SystemUpsert(ctx, system, fixture.postDescriptor, target,
		fixture.createPost(70, golem.UUID{15: 1}, "wrong-branch"), fixture.updateTitle("updated"), projection)
	if err != nil {
		t.Fatal(err)
	}
	if title, selected := golem.Value(updated, fixture.title).Get(); !selected || title != "updated" {
		t.Fatalf("updated title=%q selected=%t", title, selected)
	}
	assertUpsertTitle(t, fixture, 70, "updated")
	assertUpsertGuardEmpty(t, fixture)
	if system.executor.invalidationEpoch() != 2 {
		t.Fatalf("upsert invalidation epoch=%d want=2", system.executor.invalidationEpoch())
	}
	var factCount int
	if err := fixture.app.database.GetContext(ctx, &factCount, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factCount != 2 {
		t.Fatalf("subscribed upsert facts=%d err=%v", factCount, err)
	}
}

func TestSQLiteRootUpsertExecutesOnlySelectedNestedBranch(t *testing.T) {
	schemaFixture := schematest.NewSubscribedGraph(t)
	fixture := newGraphMutationFixture(t, schemaFixture, golem.ModelID{})
	assertRootUpsertSelectedNestedBranch(t, fixture)
}

func TestPostgreSQLRootUpsertExecutesOnlySelectedNestedBranch(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil)
			assertRootUpsertSelectedNestedBranch(t, fixture)
		})
	}
}

func TestCallerUpsertDoesNotApplyUnselectedNestedActionDenial(t *testing.T) {
	schemaFixture := schematest.NewSubscribedGraph(t)
	fixture := newGraphMutationFixture(t, schemaFixture, schemaFixture.Post)
	userID := golem.UUID{15: 210}
	plainCreate := golem.GeneratedCreateInput(schemaFixture.User,
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, userID),
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, "existing"),
	)
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, plainCreate); err != nil {
		t.Fatal(err)
	}
	target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schemaFixture.User, schemaFixture.UserKey, golem.GeneratedSelectorComponent(schemaFixture.UserID, userID))
	deniedCreate := fixture.deepCreate(210, 211, 212)
	selectedUpdate := golem.GeneratedUpdateInput(schemaFixture.User, golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "selected-update"))
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpsert(context.Background(), caller, fixture.userDescriptor, target, deniedCreate, selectedUpdate); err != nil {
		t.Fatalf("unselected nested create denial blocked truthful update: %v", err)
	}
	var name string
	query := fixture.app.database.Rebind(`SELECT "name" FROM ` + nestedAcceptanceTable(fixture.app, schemaFixture.User) + ` WHERE "id" = ?`)
	if err := fixture.app.database.Get(&name, query, mutationResultUUIDText(210)); err != nil || name != "selected-update" {
		t.Fatalf("selected update name=%q err=%v", name, err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 0, 0, 2)
}

func TestCallerRootUpsertSelectedNestedBranchHooksAndFactsAreExact(t *testing.T) {
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	type counts struct {
		userCreateBefore, userCreateAfter, userCreateCommit atomic.Int64
		userUpdateBefore, userUpdateAfter, userUpdateCommit atomic.Int64
		postCreateBefore, postCreateAfter, postCreateCommit atomic.Int64
	}
	var calls counts
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			calls.userCreateBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			calls.userCreateAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			calls.userCreateCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookRequest[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[graphMutationUser]) error {
			calls.userUpdateBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookResult[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[graphMutationUser]) error {
			calls.userUpdateAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookResult[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[graphMutationUser]) error {
			calls.userUpdateCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
			calls.postCreateBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			calls.postCreateAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			calls.postCreateCommit.Add(1)
			return nil
		}),
	}
	fixture := newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks)
	target := func(id byte) golem.UniqueSelectorValue[graphMutationUser] {
		return golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: id}))
	}
	post := func(id byte, title string) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, title),
		)
	}
	user := func(userID, postID byte, name string) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput(schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: userID}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, name),
			golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post(postID, name+"-post")),
		)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	plainUpdate := golem.GeneratedUpdateInput(schema.User, golem.GeneratedSetFieldValue(schema.User, fixture.userName, "unused-update"))
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target(220), user(220, 221, "selected-create"), plainUpdate); err != nil {
		t.Fatal(err)
	}
	if got := [9]int64{calls.userCreateBefore.Load(), calls.userCreateAfter.Load(), calls.userCreateCommit.Load(), calls.userUpdateBefore.Load(), calls.userUpdateAfter.Load(), calls.userUpdateCommit.Load(), calls.postCreateBefore.Load(), calls.postCreateAfter.Load(), calls.postCreateCommit.Load()}; got != [9]int64{1, 1, 1, 0, 0, 0, 1, 1, 1} {
		t.Fatalf("create-selected hook counts=%v", got)
	}
	assertExactNestedUpsertFacts(t, fixture, []policyir.ModelID{policyir.ModelID(schema.User), policyir.ModelID(schema.Post)}, []mutationir.FactAction{mutationir.FactCreated, mutationir.FactCreated})
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	nestedUpdate := golem.GeneratedUpdateInput(schema.User,
		golem.GeneratedSetFieldValue(schema.User, fixture.userName, "selected-update"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post(222, "selected-update-post")),
	)
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target(220), user(223, 224, "unselected-create"), nestedUpdate); err != nil {
		t.Fatal(err)
	}
	if got := [9]int64{calls.userCreateBefore.Load(), calls.userCreateAfter.Load(), calls.userCreateCommit.Load(), calls.userUpdateBefore.Load(), calls.userUpdateAfter.Load(), calls.userUpdateCommit.Load(), calls.postCreateBefore.Load(), calls.postCreateAfter.Load(), calls.postCreateCommit.Load()}; got != [9]int64{1, 1, 1, 1, 1, 1, 2, 2, 2} {
		t.Fatalf("update-selected hook counts=%v", got)
	}
	assertExactNestedUpsertFacts(t, fixture, []policyir.ModelID{policyir.ModelID(schema.User), policyir.ModelID(schema.Post)}, []mutationir.FactAction{mutationir.FactUpdated, mutationir.FactCreated})
}

func TestPostgreSQLCCallerRootUpsertSelectedNestedBranchHooksAndFactsAreExact(t *testing.T) {
	var profile postgresAcceptanceProfile
	for _, candidate := range postgresAcceptanceProfiles() {
		if candidate.name == "c" {
			profile = candidate
			break
		}
	}
	if profile.dsn == "" {
		t.Skip(profile.env + " is not configured")
	}
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	var userCreate, userUpdate, postCreate [3]atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			userCreate[0].Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			userCreate[1].Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			userCreate[2].Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookRequest[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[graphMutationUser]) error {
			userUpdate[0].Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookResult[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[graphMutationUser]) error {
			userUpdate[1].Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookResult[graphMutationUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[graphMutationUser]) error {
			userUpdate[2].Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
			postCreate[0].Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			postCreate[1].Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			postCreate[2].Add(1)
			return nil
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	target := func(id byte) golem.UniqueSelectorValue[graphMutationUser] {
		return golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: id}))
	}
	post := func(id byte) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, fmt.Sprintf("pg-exact-%d", id)),
		)
	}
	create := func(userID, postID byte) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput(schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: userID}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "pg-selected-create"),
			golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post(postID)),
		)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target(230), create(230, 231), golem.GeneratedUpdateInput(schema.User, golem.GeneratedSetFieldValue(schema.User, fixture.userName, "unused"))); err != nil {
		t.Fatal(err)
	}
	if got := [3]int64{userCreate[0].Load(), userCreate[1].Load(), userCreate[2].Load()}; got != [3]int64{1, 1, 1} {
		t.Fatalf("PG create root hooks=%v", got)
	}
	if got := [3]int64{postCreate[0].Load(), postCreate[1].Load(), postCreate[2].Load()}; got != [3]int64{1, 1, 1} {
		t.Fatalf("PG create child hooks=%v", got)
	}
	assertExactNestedUpsertFacts(t, fixture, []policyir.ModelID{policyir.ModelID(schema.User), policyir.ModelID(schema.Post)}, []mutationir.FactAction{mutationir.FactCreated, mutationir.FactCreated})
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	update := golem.GeneratedUpdateInput(schema.User,
		golem.GeneratedSetFieldValue(schema.User, fixture.userName, "pg-selected-update"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post(232)),
	)
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target(230), create(233, 234), update); err != nil {
		t.Fatal(err)
	}
	if got := [3]int64{userUpdate[0].Load(), userUpdate[1].Load(), userUpdate[2].Load()}; got != [3]int64{1, 1, 1} {
		t.Fatalf("PG update root hooks=%v", got)
	}
	if got := [3]int64{postCreate[0].Load(), postCreate[1].Load(), postCreate[2].Load()}; got != [3]int64{2, 2, 2} {
		t.Fatalf("PG update child hooks=%v", got)
	}
	if got := [3]int64{userCreate[0].Load(), userCreate[1].Load(), userCreate[2].Load()}; got != [3]int64{1, 1, 1} {
		t.Fatalf("PG unselected create hooks changed=%v", got)
	}
	assertExactNestedUpsertFacts(t, fixture, []policyir.ModelID{policyir.ModelID(schema.User), policyir.ModelID(schema.Post)}, []mutationir.FactAction{mutationir.FactUpdated, mutationir.FactCreated})
}

func assertExactNestedUpsertFacts(t testing.TB, fixture graphMutationFixture, models []policyir.ModelID, actions []mutationir.FactAction) {
	t.Helper()
	type storedFact struct {
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	var rows []storedFact
	if err := fixture.app.database.Select(&rows, `SELECT "transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(models) || len(models) != len(actions) {
		t.Fatalf("nested upsert facts=%d models=%d actions=%d", len(rows), len(models), len(actions))
	}
	var causation mutationfact.CausationID
	for index, stored := range rows {
		envelope, err := decodeCurrentMutationFactMetadata(fixture.schema.Registry, models[index], stored.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Ordinal != int64(index+1) || envelope.TransactionOrdinal() != uint32(index+1) || envelope.ModelID() != models[index] || envelope.Action() != actions[index] {
			t.Fatalf("fact[%d] storedOrdinal=%d envelopeOrdinal=%d model=%x action=%d", index, stored.Ordinal, envelope.TransactionOrdinal(), envelope.ModelID(), envelope.Action())
		}
		if index == 0 {
			causation = envelope.CausationID()
		} else if envelope.CausationID() != causation {
			t.Fatalf("fact[%d] changed causation", index)
		}
	}
}

func assertRootUpsertSelectedNestedBranch(t *testing.T, fixture graphMutationFixture) {
	t.Helper()
	schemaFixture := fixture.schema
	target := func(id byte) golem.UniqueSelectorValue[graphMutationUser] {
		return golem.GeneratedUniqueSelectorValue[graphMutationUser](schemaFixture.User, schemaFixture.UserKey, golem.GeneratedSelectorComponent(schemaFixture.UserID, golem.UUID{15: id}))
	}
	plainUpdate := golem.GeneratedUpdateInput(schemaFixture.User, golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "updated-user"))
	if _, err := SystemUpsert(context.Background(), fixture.app.System(), fixture.userDescriptor, target(201), fixture.deepCreate(201, 202, 203), plainUpdate); err != nil {
		t.Fatal(err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 1, 1, 3)
	postInput := golem.GeneratedCreateInput(schemaFixture.Post,
		golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postID, golem.UUID{15: 204}),
		golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postTitle, "update-branch-post"),
	)
	nestedUpdate := golem.GeneratedUpdateInput(schemaFixture.User,
		golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "updated-user"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, postInput),
	)
	if _, err := SystemUpsert(context.Background(), fixture.app.System(), fixture.userDescriptor, target(201), fixture.deepCreate(205, 206, 207), nestedUpdate); err != nil {
		t.Fatal(err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 2, 1, 5)
}

func TestRootUpsertGuardHiddenExistingExhaustsAsStableConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(71, golem.UUID{15: 1}, "private")); err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 71}))
	target := selector.And(fixture.title.Eq("not-private"))
	_, err := SystemUpsert(ctx, fixture.app.System(), fixture.postDescriptor, target,
		fixture.createPost(71, golem.UUID{15: 1}, "create-branch"), fixture.updateTitle("update-branch"))
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeConflict || failure.Error() != "CONFLICT: mutation conflicted" {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	assertUpsertTitle(t, fixture, 71, "private")
	assertUpsertGuardEmpty(t, fixture)
}

func TestApplicationTransactionClosureIsNeverReplayed(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(72, golem.UUID{15: 1}, "existing")); err != nil {
			t.Fatal(err)
		}
		selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 72}))
		hidden := selector.And(fixture.postID.Eq(golem.UUID{15: 99}))
		callbacks := 0
		err = CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			callbacks++
			_, upsertErr := CallerTxUpsert(ctx, transaction, fixture.postDescriptor, hidden,
				fixture.createPost(72, golem.UUID{15: 1}, "create"), fixture.updateTitle("update"))
			var conflict *golem.Error
			if !errors.As(upsertErr, &conflict) || conflict.Code != golem.CodeConflict {
				return upsertErr
			}
			// The failed operation rolled back only its savepoint; the outer
			// transaction remains usable and commits this independent row.
			_, createErr := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(73, golem.UUID{15: 1}, "after-conflict"))
			return createErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if callbacks != 1 {
			t.Fatalf("outer callback replayed %d times", callbacks)
		}
		for _, row := range []struct {
			id   byte
			want string
		}{{72, "existing"}, {73, "after-conflict"}} {
			var title string
			query := `SELECT "title" FROM ` + profile.posts + ` WHERE "id" = ` + profile.placeholder(1)
			if err := fixture.app.database.GetContext(ctx, &title, query, mutationResultUUIDText(row.id)); err != nil || title != row.want {
				t.Fatalf("id=%d title=%q want=%q err=%v", row.id, title, row.want, err)
			}
		}
		if profile.provider == golem.SQLite {
			var guards int
			if err := fixture.app.database.GetContext(ctx, &guards, `SELECT COUNT(*) FROM `+profile.guard); err != nil || guards != 0 {
				t.Fatalf("guard rows=%d err=%v", guards, err)
			}
		}
	})
}

func TestUpsertHiddenExistingNeverFallsThroughToUnauthorizedUpdate(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, fixture mutationResultFixture, posts, guards string, placeholder func(int) string) {
		t.Helper()
		bindings := conditionalUpsertBindings(t, fixture)
		provider := golem.SQLite
		if fixture.app.provider == policyir.ProviderPostgreSQL {
			provider = golem.PostgreSQL
		}
		app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
			DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle,
			Bindings: bindings, Descriptors: fixture.app.descriptors,
			ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
				return mutationResultActor{}, nil
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		caller, err := app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, app.System(), fixture.postDescriptor, fixture.createPost(74, golem.UUID{15: 2}, "hidden")); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, app.System(), fixture.postDescriptor, fixture.createPost(75, golem.UUID{15: 1}, "reachable")); err != nil {
			t.Fatal(err)
		}

		_, err = CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(74),
			fixture.createPost(74, golem.UUID{15: 1}, "create"), fixture.updateTitle("must-not-update"))
		assertPublicUpsertCode(t, err, golem.CodeConflict)
		assertUpsertStoredTitle(t, fixture, posts, placeholder, 74, "hidden")

		_, err = CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(76),
			fixture.createPost(76, golem.UUID{15: 1}, "denied-create"), fixture.updateTitle("irrelevant"))
		assertPublicUpsertCode(t, err, golem.CodeForbidden)
		var missing int
		if err := fixture.app.database.GetContext(ctx, &missing, `SELECT count(*) FROM `+posts+` WHERE "id" = `+placeholder(1), mutationResultUUIDText(76)); err != nil || missing != 0 {
			t.Fatalf("denied missing branch rows=%d err=%v", missing, err)
		}

		if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(75),
			fixture.createPost(75, golem.UUID{15: 1}, "wrong"), fixture.updateTitle("authorized-update")); err != nil {
			t.Fatal(err)
		}
		assertUpsertStoredTitle(t, fixture, posts, placeholder, 75, "authorized-update")
		if provider == golem.SQLite {
			var guardCount int
			if err := fixture.app.database.GetContext(ctx, &guardCount, `SELECT count(*) FROM `+guards); err != nil || guardCount != 0 {
				t.Fatalf("selector guard rows=%d err=%v", guardCount, err)
			}
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		run(t, newMutationResultFixture(t), `"posts"`, `"_golem_upsert_guard"`, func(int) string { return "?" })
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for hidden-existing upsert evidence", profile.env)
			}
			fixture, applicationNamespace, systemNamespace := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			run(t, fixture, oracleQualified(applicationNamespace, "posts"), oracleQualified(systemNamespace, "_golem_upsert_guard"), func(index int) string { return "$" + fmt.Sprint(index) })
		})
	}
}

func assertUpsertStoredTitle(t *testing.T, fixture mutationResultFixture, posts string, placeholder func(int) string, id byte, want string) {
	t.Helper()
	var title string
	if err := fixture.app.database.GetContext(context.Background(), &title, `SELECT "title" FROM `+posts+` WHERE "id" = `+placeholder(1), mutationResultUUIDText(id)); err != nil || title != want {
		t.Fatalf("id=%d title=%q want=%q err=%v", id, title, want, err)
	}
}

func TestConcurrentSystemCreatorsSerializeToOneTruthfulRow(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	fixture.app.database.SetMaxOpenConns(4)
	start := make(chan struct{})
	errorsByWorker := make([]error, 2)
	var wait sync.WaitGroup
	for worker := range 2 {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			_, errorsByWorker[worker] = SystemUpsert(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(77),
				fixture.createPost(77, golem.UUID{15: 1}, "created"), fixture.updateTitle("updated"))
		}(worker)
	}
	close(start)
	wait.Wait()
	for worker, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT count(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(77)); err != nil || count != 1 {
		t.Fatalf("serialized row count=%d err=%v", count, err)
	}
	assertUpsertTitle(t, fixture, 77, "updated")
	assertUpsertGuardEmpty(t, fixture)
}

func conditionalUpsertBindings(t *testing.T, fixture mutationResultFixture) golem.ApplicationBindings[mutationResultActor] {
	t.Helper()
	allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	conditionalPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.None[mutationResultPost]())
		rules.CanUpdate(fixture.authorID.Eq(golem.UUID{15: 1}))
		return rules.Freeze(fixture.schema.Post)
	})
	pack := golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, conditionalPosts}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), pack)
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}

func assertPublicUpsertCode(t *testing.T, err error, code golem.ErrorCode) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error=%v failure=%#v want=%s", err, failure, code)
	}
}

func assertUpsertTitle(t *testing.T, fixture mutationResultFixture, id byte, want string) {
	t.Helper()
	var title string
	if err := fixture.app.database.GetContext(context.Background(), &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(id)); err != nil || title != want {
		t.Fatalf("id=%d title=%q want=%q err=%v", id, title, want, err)
	}
}

func assertUpsertGuardEmpty(t *testing.T, fixture mutationResultFixture) {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, `SELECT count(*) FROM "_golem_upsert_guard"`); err != nil || count != 0 {
		t.Fatalf("guard rows=%d err=%v", count, err)
	}
}
