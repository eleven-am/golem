package runtime

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestCallerNestedNonCreateDenialsRollBackEveryFamilyAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		fixture := reopenMutationResultWithPostWriteDenials(t, profile.fixture)
		assertCallerNestedNonCreateDenials(t, fixture)
	})
}

func assertCallerNestedNonCreateDenials(t *testing.T, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	for _, row := range []struct {
		id     byte
		author byte
		title  string
	}{
		{60, 1, "update-before"}, {61, 1, "delete-before"}, {62, 2, "membership-before"},
		{63, 1, "batch-update-a"}, {64, 1, "batch-update-b"},
		{65, 1, "batch-delete-a"}, {66, 1, "batch-delete-b"},
	} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: row.author}, row.title)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	caller := mustMutationResultCaller(t, fixture)
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
		return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	userUpdate := func(name string, nested golem.NestedUpdateValue[mutationResultUser]) golem.UpdateInput[mutationResultUser] {
		return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name), nested)
	}
	tests := []struct {
		name  string
		input golem.UpdateInput[mutationResultUser]
		code  golem.ErrorCode
	}{
		{"update", userUpdate("denied-update", golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(60), fixture.updateTitle("must-not-update"))), golem.CodeNotFound},
		{"delete", userUpdate("denied-delete", golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(61))), golem.CodeNotFound},
		{"membership", userUpdate("denied-membership", golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(62))), golem.CodeNotFound},
		{"updateMany", userUpdate("denied-update-many", golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 63}, golem.UUID{15: 64}), fixture.updateManyTitle("must-not-batch-update"))), golem.CodeForbidden},
		{"deleteMany", userUpdate("denied-delete-many", golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 65}, golem.UUID{15: 66}))), golem.CodeForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget, test.input)
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("nested denial failure=%#v err=%v", failure, err)
			}
			assertNestedDenialState(t, fixture)
		})
	}
}

func assertNestedDenialState(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	users, posts := nestedAcceptanceTable(fixture.app, fixture.schema.User), nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var name string
	query := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &name, query, mutationResultUUIDText(1)); err != nil || name != "alice" {
		t.Fatalf("denied root name=%q err=%v", name, err)
	}
	for _, row := range []struct {
		id     byte
		author byte
		title  string
	}{
		{60, 1, "update-before"}, {61, 1, "delete-before"}, {62, 2, "membership-before"},
		{63, 1, "batch-update-a"}, {64, 1, "batch-update-b"}, {65, 1, "batch-delete-a"}, {66, 1, "batch-delete-b"},
	} {
		var author, title string
		query = fixture.app.database.Rebind(`SELECT "author_id","title" FROM ` + posts + ` WHERE "id"=?`)
		if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(row.id)).Scan(&author, &title); err != nil || author != mutationResultUUIDText(row.author) || title != row.title {
			t.Fatalf("denied post %d author=%q title=%q err=%v", row.id, author, title, err)
		}
	}
	var facts int
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != 0 {
		t.Fatalf("denied nested facts=%d err=%v", facts, err)
	}
}

func TestCallerPolicyInvisibleExactDisconnectReturnsNotFoundAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "invisible_disconnect", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		fixture := reopenMutationResultWithPostWriteDenials(t, profile.fixture)
		ctx := context.Background()
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(67, golem.UUID{15: 1}, "disconnect-before")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
		caller := mustMutationResultCaller(t, fixture)
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		post := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 67}))
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "must-roll-back"),
			golem.GeneratedNestedDisconnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post))
		_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, input)
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
			t.Fatalf("policy-invisible disconnect failure=%#v err=%v", failure, err)
		}
		var name, author string
		users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
		posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
		if err := fixture.app.database.GetContext(ctx, &name, fixture.app.database.Rebind(`SELECT "name" FROM `+users+` WHERE "id"=?`), mutationResultUUIDText(1)); err != nil || name != "alice" {
			t.Fatalf("policy-invisible disconnect root name=%q err=%v", name, err)
		}
		if err := fixture.app.database.GetContext(ctx, &author, fixture.app.database.Rebind(`SELECT "author_id" FROM `+posts+` WHERE "id"=?`), mutationResultUUIDText(67)); err != nil || author != mutationResultUUIDText(1) {
			t.Fatalf("policy-invisible disconnect author=%q err=%v", author, err)
		}
		var facts int
		if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != 0 {
			t.Fatalf("policy-invisible disconnect facts=%d err=%v", facts, err)
		}
	})
}

func TestCallerSourceExactConnectAuthorizesSelectedTargetUpdateReachAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "src_connect_auth", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, base := context.Background(), profile.fixture
		for _, id := range []byte{240, 241, 242} {
			if _, err := SystemCreate(ctx, base.app.System(), base.postDescriptor, base.createPost(id, golem.UUID{15: 1}, "before")); err != nil {
				t.Fatal(err)
			}
		}
		for index, test := range []struct {
			name   string
			reach  sourceTargetUpdateReach
			allows bool
		}{{"allowed", sourceTargetUpdateAll, true}, {"absent", sourceTargetUpdateAbsent, false}, {"conditional-invisible", sourceTargetUpdateAlice, false}} {
			t.Run(test.name, func(t *testing.T) {
				fixture := reopenMutationResultWithSourceTargetUpdateReach(t, base, test.reach)
				if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
					t.Fatal(err)
				}
				id := byte(240 + index)
				bob := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
					golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
				input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
					golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
					golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, bob))
				_, err := CallerUpdate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.target(id), input)
				if test.allows {
					if err != nil {
						t.Fatalf("authorized source Connect: %v", err)
					}
				} else {
					var failure *golem.Error
					if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
						t.Fatalf("source Connect target denial=%#v err=%v", failure, err)
					}
				}
				wantTitle, wantAuthor := "before", mutationResultUUIDText(1)
				if test.allows {
					wantTitle, wantAuthor = "after", mutationResultUUIDText(2)
				}
				assertSourceMembershipPostState(t, fixture, id, wantTitle, sql.NullString{String: wantAuthor, Valid: true}, test.allows)
			})
		}
	})
}

func TestCallerSourceCurrentDisconnectAuthorizesSelectedTargetUpdateReachAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "src_disconnect_auth", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, base := context.Background(), profile.fixture
		for _, id := range []byte{243, 244, 245} {
			if _, err := SystemCreate(ctx, base.app.System(), base.postDescriptor, base.createPost(id, golem.UUID{15: 2}, "before")); err != nil {
				t.Fatal(err)
			}
		}
		for index, test := range []struct {
			name   string
			reach  sourceTargetUpdateReach
			allows bool
		}{{"allowed", sourceTargetUpdateAll, true}, {"absent", sourceTargetUpdateAbsent, false}, {"conditional-invisible", sourceTargetUpdateAlice, false}} {
			t.Run(test.name, func(t *testing.T) {
				fixture := reopenMutationResultWithSourceTargetUpdateReach(t, base, test.reach)
				if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
					t.Fatal(err)
				}
				id := byte(243 + index)
				input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
					golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
					golem.GeneratedNestedDisconnectOne[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User))
				_, err := CallerUpdate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.target(id), input)
				if test.allows {
					if err != nil {
						t.Fatalf("authorized source Disconnect: %v", err)
					}
				} else {
					var failure *golem.Error
					if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
						t.Fatalf("source Disconnect target denial=%#v err=%v", failure, err)
					}
				}
				wantTitle, wantAuthor := "before", sql.NullString{String: mutationResultUUIDText(2), Valid: true}
				if test.allows {
					wantTitle, wantAuthor = "after", sql.NullString{}
				}
				assertSourceMembershipPostState(t, fixture, id, wantTitle, wantAuthor, test.allows)
			})
		}
	})
}

type sourceTargetUpdateReach uint8

const (
	sourceTargetUpdateAbsent sourceTargetUpdateReach = iota
	sourceTargetUpdateAll
	sourceTargetUpdateAlice
)

func reopenMutationResultWithSourceTargetUpdateReach(t testing.TB, fixture mutationResultFixture, reach sourceTargetUpdateReach) mutationResultFixture {
	t.Helper()
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		switch reach {
		case sourceTargetUpdateAll:
			rules.CanUpdate(golem.All[mutationResultUser]())
		case sourceTargetUpdateAlice:
			rules.CanUpdate(fixture.userName.Eq("alice"))
		}
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func assertSourceMembershipPostState(t testing.TB, fixture mutationResultFixture, id byte, wantTitle string, wantAuthor sql.NullString, expectFacts bool) {
	t.Helper()
	ctx := context.Background()
	var title string
	var author sql.NullString
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	query := fixture.app.database.Rebind(`SELECT "title","author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(id)).Scan(&title, &author); err != nil || title != wantTitle || author != wantAuthor {
		t.Fatalf("source membership post id=%d title=%q author=%#v err=%v; want title=%q author=%#v", id, title, author, err, wantTitle, wantAuthor)
	}
	var facts int
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	if expectFacts && facts == 0 || !expectFacts && facts != 0 {
		t.Fatalf("source membership facts=%d expectFacts=%t", facts, expectFacts)
	}
}

func reopenMutationResultWithPostWriteDenials(t testing.TB, fixture mutationResultFixture) mutationResultFixture {
	t.Helper()
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func TestCallerNestedNonCreateFactsRetainForwardOrdinalsAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		assertCallerNestedNonCreateFacts(t, profile.fixture)
	})
}

func TestCallerNestedAuthorizedEmptyBatchIsNoOpAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		fixture := profile.fixture
		ctx := context.Background()
		caller := mustMutationResultCaller(t, fixture)
		target := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		update := func(name string, nested golem.NestedUpdateValue[mutationResultUser]) golem.UpdateInput[mutationResultUser] {
			return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
				golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name), nested)
		}
		missing := fixture.postID.Eq(golem.UUID{15: 250})
		updateMany := golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing, fixture.updateManyTitle("never-written"))
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, update("empty-update-many", updateMany)); err != nil {
			t.Fatalf("authorized empty nested updateMany: %v cause=%v", err, errors.Unwrap(err))
		}
		deleteMany := golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing)
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, update("empty-delete-many", deleteMany)); err != nil {
			t.Fatalf("authorized empty nested deleteMany: %v", err)
		}
		var name string
		users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
		query := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(ctx, &name, query, mutationResultUUIDText(1)); err != nil || name != "empty-delete-many" {
			t.Fatalf("authorized empty nested batch root=%q err=%v", name, err)
		}
		var matching int
		posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
		query = fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "title"=?`)
		if err := fixture.app.database.GetContext(ctx, &matching, query, "never-written"); err != nil || matching != 0 {
			t.Fatalf("authorized empty nested batch child writes=%d err=%v", matching, err)
		}
	})
}

func TestCallerAndSystemRelationOnlyRootUpdateAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		fixture := profile.fixture
		ctx := context.Background()
		for _, id := range []byte{70, 71} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 2}, "relation-only")); err != nil {
				t.Fatal(err)
			}
		}
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		relationOnly := func() golem.UpdateInput[mutationResultPost] {
			return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user))
		}
		caller := mustMutationResultCaller(t, fixture)
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(70), relationOnly()); err != nil {
			chain := make([]string, 0, 4)
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				chain = append(chain, cause.Error())
			}
			t.Fatalf("caller relation-only root update: %q", chain)
		}
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(71), relationOnly()); err != nil {
			t.Fatalf("system relation-only root update: %v", err)
		}
		posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
		for _, id := range []byte{70, 71} {
			var author string
			query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + posts + ` WHERE "id"=?`)
			if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(id)); err != nil || author != mutationResultUUIDText(1) {
				t.Fatalf("relation-only post %d author=%q err=%v", id, author, err)
			}
		}
	})
}

func assertCallerNestedNonCreateFacts(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	caller := mustMutationResultCaller(t, fixture)
	for _, row := range []struct{ id, author byte }{{101, 2}, {102, 2}, {103, 2}, {104, 1}, {105, 1}, {106, 1}, {107, 1}, {108, 1}, {109, 1}} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: row.author}, "fact-before")); err != nil {
			t.Fatal(err)
		}
	}
	userThree := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 3}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "charlie"))
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, userThree); err != nil {
		t.Fatal(err)
	}
	userTarget := func(id byte) golem.MutationTarget[mutationResultUser] {
		return golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
	}
	postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
		return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	userUpdate := func(name string, nested golem.NestedUpdateValue[mutationResultUser]) golem.UpdateInput[mutationResultUser] {
		return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name), nested)
	}
	clear := func() {
		t.Helper()
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
	}
	clear()
	connect := golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(101))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), userUpdate("membership-fact", connect)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactUpdated, 101)

	clear()
	set := golem.GeneratedNestedSet[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(102), postTarget(103))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(3), userUpdate("set-fact", set)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactUpdated, 102, 103)

	clear()
	update := golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(104), fixture.updateTitle("nested-updated"))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), userUpdate("update-fact", update)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactUpdated, 104)

	clear()
	deleteOne := golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(105))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), userUpdate("delete-fact", deleteOne)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactDeleted, 105)

	clear()
	updateMany := golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 106}, golem.UUID{15: 107}), fixture.updateManyTitle("nested-many-updated"))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), userUpdate("update-many-fact", updateMany)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactUpdated, 106, 107)

	clear()
	deleteMany := golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 108}, golem.UUID{15: 109}))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), userUpdate("delete-many-fact", deleteMany)); err != nil {
		t.Fatal(err)
	}
	assertPostFactSequence(t, fixture, mutationir.FactDeleted, 108, 109)
}

func assertPostFactSequence(t testing.TB, fixture mutationResultFixture, action mutationir.FactAction, ids ...byte) {
	t.Helper()
	type row struct {
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	var rows []row
	if err := fixture.app.database.Select(&rows, `SELECT "transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(ids) {
		t.Fatalf("fact rows=%d want=%d", len(rows), len(ids))
	}
	var causation mutationfact.CausationID
	for index, stored := range rows {
		envelope, err := decodeCurrentMutationFactMetadata(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), stored.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Ordinal != int64(index+1) || envelope.TransactionOrdinal() != uint32(index+1) || envelope.ModelID() != policyir.ModelID(fixture.schema.Post) || envelope.Action() != action {
			t.Fatalf("fact[%d] ordinal=%d envelopeOrdinal=%d model=%x action=%d", index, stored.Ordinal, envelope.TransactionOrdinal(), envelope.ModelID(), envelope.Action())
		}
		if index == 0 {
			causation = envelope.CausationID()
		} else if envelope.CausationID() != causation {
			t.Fatalf("fact[%d] causation changed", index)
		}
		before, beforeOK := envelope.BeforeIdentity()
		if !beforeOK {
			t.Fatalf("fact[%d] has no before identity", index)
		}
		assertMutationUUIDIdentity(t, before, golem.UUID{15: ids[index]})
		after, afterOK := envelope.AfterIdentity()
		if action == mutationir.FactUpdated {
			if !afterOK {
				t.Fatalf("updated fact[%d] has no after identity", index)
			}
			assertMutationUUIDIdentity(t, after, golem.UUID{15: ids[index]})
		} else if afterOK {
			t.Fatalf("deleted fact[%d] unexpectedly has after identity", index)
		}
	}
}
