package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func forEachUniqueUserNameSocialProvider(t *testing.T, run func(*testing.T, socialMutationFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		schemaFixture := schematest.NewSubscribedSocialMutationUniqueUserName(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "social-unique-name.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, openSocialMutationFixture(t, database, golem.SQLite, schemaFixture, golem.ModelID{}, nil))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			suffix := time.Now().UnixNano()
			namespace := physical.PhysicalName(fmt.Sprintf("golem_social_name_%s_%d_%d", profile.name, os.Getpid(), suffix))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_social_name_sys_%s_%d_%d", profile.name, os.Getpid(), suffix))
			schemaFixture := schematest.NewSubscribedSocialMutationUniqueUserNamePostgreSQLNamespaces(t, namespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, openSocialMutationFixture(t, database, golem.PostgreSQL, schemaFixture, golem.ModelID{}, nil))
		})
	}
}

func (fixture socialMutationFixture) tagIDTarget(id byte) golem.MutationTarget[socialMutationTag] {
	return golem.GeneratedUniqueSelectorValue[socialMutationTag](fixture.schema.Tag, fixture.schema.TagKey,
		golem.GeneratedSelectorComponent(fixture.schema.TagID, golem.UUID{15: id}))
}

func (fixture socialMutationFixture) postTagTarget(post byte, tag string) golem.MutationTarget[socialMutationPostTag] {
	return golem.GeneratedUniqueSelectorValue[socialMutationPostTag](fixture.schema.PostTag, fixture.schema.PostTagKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostTagPostID, golem.UUID{15: post}),
		golem.GeneratedSelectorComponent(fixture.schema.PostTagTagName, tag))
}

func TestNestedUpsertUnderAParentSelectedByAKeyItsForeignKeyDoesNotReference(t *testing.T) {
	forEachSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		system := fixture.app.System()
		fixture.seedUsers(t, 1)
		for _, post := range []byte{10, 11} {
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.postRootCreate(post, 1, "p")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := SystemCreate(ctx, system, fixture.tagDescriptor, fixture.tagCreate(30, "deep-tag")); err != nil {
			t.Fatal(err)
		}
		postTag := func(post byte) golem.CreateInput[socialMutationPostTag] {
			return golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag, golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagPostID, golem.UUID{15: post}))
		}
		connectPost := golem.GeneratedUpdateInput[socialMutationPostTag](fixture.schema.PostTag,
			golem.GeneratedNestedConnect[socialMutationPostTag, socialMutationPost](fixture.schema.PostTag, fixture.schema.PostTagPost, fixture.schema.PostTagPostRelation, fixture.schema.Post, fixture.postTarget(10)))
		upsert := func(target golem.MutationTarget[socialMutationPostTag], create golem.CreateInput[socialMutationPostTag]) golem.UpdateInput[socialMutationTag] {
			return golem.GeneratedUpdateInput[socialMutationTag](fixture.schema.Tag,
				golem.GeneratedNestedUpsert[socialMutationTag, socialMutationPostTag](fixture.schema.Tag, fixture.schema.TagPostTags, fixture.schema.PostTagTagRelation, fixture.schema.PostTag, target, create, connectPost))
		}
		connectOrCreate := func(target golem.MutationTarget[socialMutationPostTag], create golem.CreateInput[socialMutationPostTag]) golem.UpdateInput[socialMutationTag] {
			return golem.GeneratedUpdateInput[socialMutationTag](fixture.schema.Tag,
				golem.GeneratedNestedConnectOrCreate[socialMutationTag, socialMutationPostTag](fixture.schema.Tag, fixture.schema.TagPostTags, fixture.schema.PostTagTagRelation, fixture.schema.PostTag, target, create))
		}
		if _, err := SystemUpdate(ctx, system, fixture.tagDescriptor, fixture.tagIDTarget(30), upsert(fixture.postTagTarget(10, "deep-tag"), postTag(10))); err != nil {
			t.Fatalf("upsert under a tag selected by id: %v", err)
		}
		if _, err := SystemUpdate(ctx, system, fixture.tagDescriptor, fixture.tagIDTarget(30), connectOrCreate(fixture.postTagTarget(11, "deep-tag"), postTag(11))); err != nil {
			t.Fatalf("connectOrCreate under a tag selected by id: %v", err)
		}
		if got := socialCount(t, fixture, fixture.schema.PostTag); got != 2 {
			t.Fatalf("post tags=%d want 2", got)
		}
		_, err := SystemUpdate(ctx, system, fixture.tagDescriptor, fixture.tagIDTarget(30), upsert(fixture.postTagTarget(10, "other-tag"), postTag(10)))
		assertNestedTargetIdentityRefusal(t, err)
		_, err = SystemUpdate(ctx, system, fixture.tagDescriptor, fixture.tagIDTarget(30), connectOrCreate(fixture.postTagTarget(11, "other-tag"), postTag(11)))
		assertConnectOrCreateTargetIdentityRefusal(t, err)
		if got := socialCount(t, fixture, fixture.schema.PostTag); got != 2 {
			t.Fatalf("refused writes changed post tags to %d", got)
		}
	})
}

func TestNestedUpsertUnderAParentSelectedByANonPrimaryUniqueKey(t *testing.T) {
	forEachUniqueUserNameSocialProvider(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		system := fixture.app.System()
		for _, user := range []struct {
			id   byte
			name string
		}{{1, "alice"}, {2, "bob"}, {3, "carol"}} {
			if _, err := SystemCreate(ctx, system, fixture.userDescriptor, fixture.userCreate(user.id, user.name)); err != nil {
				t.Fatal(err)
			}
		}
		byName := golem.GeneratedUniqueSelectorValue[socialMutationUser](fixture.schema.User, fixture.schema.UserNameKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserName, "alice"))
		create := func(friend byte) golem.CreateInput[socialMutationFriendship] {
			return golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipFriend(friend))
		}
		upsert := func(user, friend, connected byte) golem.UpdateInput[socialMutationUser] {
			return golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
				golem.GeneratedNestedUpsert[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship,
					fixture.friendshipTarget(user, friend), create(connected), fixture.friendshipUpdate(friend)))
		}
		connectOrCreate := func(user, friend, connected byte) golem.UpdateInput[socialMutationUser] {
			return golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
				golem.GeneratedNestedConnectOrCreate[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship,
					fixture.friendshipTarget(user, friend), create(connected)))
		}
		if _, err := SystemUpdate(ctx, system, fixture.userDescriptor, byName, upsert(1, 2, 2)); err != nil {
			t.Fatalf("upsert under a user selected by name: %v", err)
		}
		if _, err := SystemUpdate(ctx, system, fixture.userDescriptor, byName, connectOrCreate(1, 3, 3)); err != nil {
			t.Fatalf("connectOrCreate under a user selected by name: %v", err)
		}
		if got := socialFriendships(t, fixture); len(got) != 2 {
			t.Fatalf("friendships=%v want 2", got)
		}
		_, err := SystemUpdate(ctx, system, fixture.userDescriptor, byName, upsert(2, 3, 3))
		assertNestedTargetIdentityRefusal(t, err)
		_, err = SystemUpdate(ctx, system, fixture.userDescriptor, byName, connectOrCreate(1, 2, 3))
		assertConnectOrCreateTargetIdentityRefusal(t, err)
		if got := socialFriendships(t, fixture); len(got) != 2 {
			t.Fatalf("refused writes changed friendships to %v", got)
		}
	})
}
