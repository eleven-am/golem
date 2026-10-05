package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func forEachSocialProvider(t *testing.T, run func(*testing.T, socialMutationFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, newSocialMutationFixture(t, golem.ModelID{}, nil))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			run(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func reopenSocialMutation(t testing.TB, fixture socialMutationFixture, policies []golem.PolicyBinding[graphMutationActor], hooks []golem.HookBinding[graphMutationActor]) socialMutationFixture {
	t.Helper()
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), policies, hooks))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[graphMutationPrincipal, graphMutationActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func socialPolicies(fixture socialMutationFixture, user func(*golem.Rules[socialMutationUser]), post func(*golem.Rules[socialMutationPost])) []golem.PolicyBinding[graphMutationActor] {
	userPolicy := allowSocialMutationPolicy[socialMutationUser](fixture.schema.User, golem.ModelID{})
	if user != nil {
		userPolicy = golem.GeneratedPolicyBinding[graphMutationActor, socialMutationUser](fixture.schema.User, func(graphMutationActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[socialMutationUser]()
			user(rules)
			return rules.Freeze(fixture.schema.User)
		})
	}
	postPolicy := allowSocialMutationPolicy[socialMutationPost](fixture.schema.Post, golem.ModelID{})
	if post != nil {
		postPolicy = golem.GeneratedPolicyBinding[graphMutationActor, socialMutationPost](fixture.schema.Post, func(graphMutationActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[socialMutationPost]()
			post(rules)
			return rules.Freeze(fixture.schema.Post)
		})
	}
	return []golem.PolicyBinding[graphMutationActor]{
		userPolicy, postPolicy,
		allowSocialMutationPolicy[socialMutationComment](fixture.schema.Comment, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationFriendship](fixture.schema.Friendship, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationTag](fixture.schema.Tag, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationPostTag](fixture.schema.PostTag, golem.ModelID{}),
	}
}

func (fixture socialMutationFixture) friendshipTarget(user, friend byte) golem.MutationTarget[socialMutationFriendship] {
	return golem.GeneratedUniqueSelectorValue[socialMutationFriendship](fixture.schema.Friendship, fixture.schema.FriendshipKey,
		golem.GeneratedSelectorComponent(fixture.schema.FriendshipUserID, golem.UUID{15: user}),
		golem.GeneratedSelectorComponent(fixture.schema.FriendshipFriendID, golem.UUID{15: friend}))
}

func (fixture socialMutationFixture) connectFriendshipUser(user byte) golem.NestedValue[socialMutationFriendship] {
	return golem.GeneratedNestedConnect[socialMutationFriendship, socialMutationUser](fixture.schema.Friendship, fixture.schema.FriendshipUser, fixture.schema.FriendshipOrigin, fixture.schema.User, fixture.userTarget(user))
}

func (fixture socialMutationFixture) connectFriendshipFriend(friend byte) golem.NestedValue[socialMutationFriendship] {
	return golem.GeneratedNestedConnect[socialMutationFriendship, socialMutationUser](fixture.schema.Friendship, fixture.schema.FriendshipFriend, fixture.schema.FriendshipDestination, fixture.schema.User, fixture.userTarget(friend))
}

func (fixture socialMutationFixture) friendshipUpdate(friend byte) golem.UpdateInput[socialMutationFriendship] {
	return golem.GeneratedUpdateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipFriend(friend))
}

func (fixture socialMutationFixture) seedUsers(t testing.TB, ids ...byte) {
	t.Helper()
	for _, id := range ids {
		if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, fixture.userCreate(id, "seed")); err != nil {
			t.Fatal(err)
		}
	}
}

func socialFriendships(t testing.TB, fixture socialMutationFixture) []string {
	t.Helper()
	var rows []string
	query := `SELECT "user_id" || ':' || "friend_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Friendship) + ` ORDER BY 1`
	if err := fixture.app.database.Select(&rows, query); err != nil {
		t.Fatal(err)
	}
	return rows
}

func socialFriendshipKey(user, friend byte) string {
	return mutationResultUUIDText(user) + ":" + mutationResultUUIDText(friend)
}

func socialCount(t testing.TB, fixture socialMutationFixture, model golem.ModelID) int {
	t.Helper()
	var count int
	if err := fixture.app.database.Get(&count, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, model)); err != nil {
		t.Fatal(err)
	}
	return count
}

func socialPostAuthor(t testing.TB, fixture socialMutationFixture, post byte) string {
	t.Helper()
	var author string
	query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
	if err := fixture.app.database.Get(&author, query, mutationResultUUIDText(post)); err != nil {
		t.Fatal(err)
	}
	return author
}

func assertSocialNotFound(t testing.TB, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("cause %T: %v", cause, cause)
		}
		t.Fatalf("error=%v failure=%#v; want %s", err, failure, golem.CodeNotFound)
	}
}

func assertConnectOrCreateTargetIdentityRefusal(t *testing.T, err error) {
	t.Helper()
	assertTargetIdentityRefusal(t, err)
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Message != "connectOrCreate create input does not set the target selector" {
		t.Fatalf("connectOrCreate public error=%#v; want the connectOrCreate target-selector message", failure)
	}
}

func TestRootUpsertForeignKeySelectorIsSatisfiedOnlyByAMatchingConnect(t *testing.T) {
	forEachSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1, 2, 3)
		create := func(user, friend byte) golem.CreateInput[socialMutationFriendship] {
			return golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipUser(user), fixture.connectFriendshipFriend(friend))
		}
		if _, err := SystemUpsert(ctx, fixture.app.System(), fixture.friendshipDescriptor, fixture.friendshipTarget(1, 2), create(1, 2), fixture.friendshipUpdate(2)); err != nil {
			t.Fatalf("connect-satisfied upsert was refused: %v", err)
		}
		if got := socialFriendships(t, fixture); len(got) != 1 || got[0] != socialFriendshipKey(1, 2) {
			t.Fatalf("friendships=%v", got)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, mismatched := range []golem.CreateInput[socialMutationFriendship]{create(1, 3), create(3, 2)} {
			_, err := SystemUpsert(ctx, fixture.app.System(), fixture.friendshipDescriptor, fixture.friendshipTarget(1, 2), mismatched, fixture.friendshipUpdate(2))
			assertTargetIdentityRefusal(t, err)
			_, err = CallerUpsert(ctx, caller, fixture.friendshipDescriptor, fixture.friendshipTarget(1, 2), mismatched, fixture.friendshipUpdate(2))
			assertTargetIdentityRefusal(t, err)
		}
		if got := socialFriendships(t, fixture); len(got) != 1 || got[0] != socialFriendshipKey(1, 2) {
			t.Fatalf("refused upserts changed friendships to %v", got)
		}
	})
}

func TestNestedUpsertForeignKeySelectorUsesTheKnownParentAndAMatchingConnect(t *testing.T) {
	forEachSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1, 2, 3)
		nested := func(user, friend, connected byte) golem.UpdateInput[socialMutationUser] {
			create := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipFriend(connected))
			return golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
				golem.GeneratedNestedUpsert[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship,
					fixture.friendshipTarget(user, friend), create, fixture.friendshipUpdate(friend)))
		}
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userTarget(1), nested(1, 2, 2)); err != nil {
			t.Fatalf("parent-and-connect-satisfied nested upsert was refused: %v", err)
		}
		if got := socialFriendships(t, fixture); len(got) != 1 || got[0] != socialFriendshipKey(1, 2) {
			t.Fatalf("friendships=%v", got)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for name, input := range map[string]golem.UpdateInput[socialMutationUser]{
			"connect differs on the update branch": nested(1, 2, 3),
			"parent differs":                       nested(3, 2, 2),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userTarget(1), input)
				assertNestedTargetIdentityRefusal(t, err)
				_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(1), input)
				assertNestedTargetIdentityRefusal(t, err)
			})
		}
		if got := socialFriendships(t, fixture); len(got) != 1 || got[0] != socialFriendshipKey(1, 2) {
			t.Fatalf("refused nested upserts changed friendships to %v", got)
		}
	})
}

func TestConnectOrCreateRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	forEachSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 2, 4)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(54, 2, "coc")); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		coc := func(target byte) golem.NestedValue[socialMutationPost] {
			return golem.GeneratedNestedConnectOrCreate[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(target), fixture.userCreate(9, "mismatched"))
		}
		for name, target := range map[string]byte{"absent target": 99, "existing target": 4} {
			t.Run(name, func(t *testing.T) {
				update := golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post, coc(target))
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postTarget(54), update)
				assertConnectOrCreateTargetIdentityRefusal(t, err)
				_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(54), update)
				assertConnectOrCreateTargetIdentityRefusal(t, err)
				create := golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 55}),
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "coc-create"),
					coc(target))
				_, err = CallerCreate(ctx, caller, fixture.postDescriptor, create)
				assertConnectOrCreateTargetIdentityRefusal(t, err)
			})
		}
		if got := socialCount(t, fixture, fixture.schema.User); got != 2 {
			t.Fatalf("refused connectOrCreate left %d users, want 2", got)
		}
		if got := socialCount(t, fixture, fixture.schema.Post); got != 1 {
			t.Fatalf("refused connectOrCreate left %d posts, want 1", got)
		}
		if author := socialPostAuthor(t, fixture, 54); author != mutationResultUUIDText(2) {
			t.Fatalf("refused connectOrCreate relinked post 54 to %s", author)
		}
		matched := golem.GeneratedNestedConnectOrCreate[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(9), fixture.userCreate(9, "matched"))
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(54), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post, matched)); err != nil {
			t.Fatalf("matching connectOrCreate was refused: %v", err)
		}
		if author := socialPostAuthor(t, fixture, 54); author != mutationResultUUIDText(9) {
			t.Fatalf("matching connectOrCreate linked post 54 to %s", author)
		}
	})
}

func TestConnectOrCreateReportsAnUnreadableExistingTargetAsNotFound(t *testing.T) {
	forEachSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(2, "visible")); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(7, "hidden")); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(54, 2, "visible")); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(57, 7, "hidden")); err != nil {
			t.Fatal(err)
		}
		fixture = reopenSocialMutation(t, fixture, socialPolicies(fixture,
			func(rules *golem.Rules[socialMutationUser]) {
				rules.CanRead(fixture.userName.Eq("visible"))
				rules.CanCreate(golem.All[socialMutationUser]())
				rules.CanUpdate(fixture.userName.Eq("visible"))
			},
			func(rules *golem.Rules[socialMutationPost]) {
				rules.CanRead(fixture.postTitle.Eq("visible"))
				rules.CanCreate(golem.All[socialMutationPost]())
				rules.CanUpdate(fixture.postTitle.Eq("visible"))
			},
		), nil)
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		source := golem.GeneratedNestedConnectOrCreate[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(7), fixture.userCreate(7, "visible"))
		_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(54), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post, source))
		assertSocialNotFound(t, err)
		created := golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 58}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "visible"),
			source)
		_, err = CallerCreate(ctx, caller, fixture.postDescriptor, created)
		assertSocialNotFound(t, err)
		inverse := golem.GeneratedNestedConnectOrCreate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(57), fixture.postCreate(57, "visible"))
		_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(2), golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User, inverse))
		assertSocialNotFound(t, err)
		if author := socialPostAuthor(t, fixture, 54); author != mutationResultUUIDText(2) {
			t.Fatalf("invisible connectOrCreate relinked post 54 to %s", author)
		}
		if author := socialPostAuthor(t, fixture, 57); author != mutationResultUUIDText(7) {
			t.Fatalf("invisible connectOrCreate relinked post 57 to %s", author)
		}
		if users, posts := socialCount(t, fixture, fixture.schema.User), socialCount(t, fixture, fixture.schema.Post); users != 2 || posts != 2 {
			t.Fatalf("invisible connectOrCreate left users=%d posts=%d", users, posts)
		}
	})
}
