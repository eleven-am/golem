package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestPostgreSQLDeleteManyDeletesTheMatchingRowsItHolds(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "batch_rule", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		if profile.provider != golem.PostgreSQL {
			t.Skip("the enumeration fault seam exists only where rows are locked, on PostgreSQL")
		}
		ctx, fixture := context.Background(), profile.fixture
		seedCascadeUser(t, fixture, 3, "carol")
		for _, post := range []struct {
			id     byte
			author byte
			title  string
		}{{10, 2, "doomed"}, {11, 2, "doomed"}, {12, 2, "doomed"}, {13, 3, "kept"}, {14, 3, "kept"}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, post.title)); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		caller := mustMutationResultCaller(t, fixture)
		database := fixture.app.database
		racing := atEnumeration(ctx, 1, func(ctx context.Context) error {
			if _, err := database.ExecContext(ctx, `DELETE FROM `+profile.posts+` WHERE "id" = $1`, mutationResultUUIDText(11)); err != nil {
				return err
			}
			_, err := database.ExecContext(ctx, `UPDATE `+profile.posts+` SET "author_id" = $2 WHERE "id" = $1`, mutationResultUUIDText(13), mutationResultUUIDText(2))
			return err
		})
		count, err := CallerDeleteMany(racing, caller, fixture.postDescriptor, fixture.authorID.Eq(golem.UUID{15: 2}))
		if err != nil || count != 2 {
			t.Fatalf("deleteMany racing a delete and a newly matching row count=%d err=%v cause=%v; want the 2 rows it held", count, err, errors.Unwrap(err))
		}
		assertCascadeFacts(t, profile, []cascadeFact{{"deleted", 10}, {"deleted", 12}})
		assertCascadePosts(t, profile, map[byte]string{13: mutationResultUUIDText(2), 14: mutationResultUUIDText(3)})

		for _, id := range []byte{30, 31, 32} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "cascaded")); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		dependentGone := atEnumeration(ctx, 2, func(ctx context.Context) error {
			_, err := database.ExecContext(ctx, `DELETE FROM `+profile.posts+` WHERE "id" = $1`, mutationResultUUIDText(31))
			return err
		})
		if count, err := CallerDeleteMany(dependentGone, caller, fixture.userDescriptor, fixture.userID.Eq(golem.UUID{15: 1})); err != nil || count != 1 {
			t.Fatalf("deleteMany whose cascaded dependent vanished count=%d err=%v", count, err)
		}
		assertCascadeFacts(t, profile, []cascadeFact{{"deleted", 30}, {"deleted", 32}})
		assertCascadePosts(t, profile, map[byte]string{13: mutationResultUUIDText(2), 14: mutationResultUUIDText(3)})
	})
}

func TestPostgreSQLNestedDeleteManyDeletesTheMatchingRowsItHolds(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil)
			system := fixture.app.System()
			if _, err := SystemCreate(ctx, system, fixture.userDescriptor, fixture.userCreate(1, "owner")); err != nil {
				t.Fatal(err)
			}
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.postRootCreate(10, 1, "post")); err != nil {
				t.Fatal(err)
			}
			for index, name := range []string{"a", "b", "c", "d", "e"} {
				if _, err := SystemCreate(ctx, system, fixture.tagDescriptor, fixture.tagCreate(byte(30+index), name)); err != nil {
					t.Fatal(err)
				}
				if name == "d" {
					continue
				}
				link := golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
					golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagPostID, golem.UUID{15: 10}),
					golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, name))
				if _, err := SystemCreate(ctx, system, fixture.postTagDescriptor, link); err != nil {
					t.Fatal(err)
				}
			}
			outbox := nestedAcceptanceOutbox(fixture.app)
			if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+outbox); err != nil {
				t.Fatal(err)
			}
			postTags := nestedAcceptanceTable(fixture.app, fixture.schema.PostTag)
			database := fixture.app.database
			racing := atEnumeration(ctx, 2, func(ctx context.Context) error {
				if _, err := database.ExecContext(ctx, `DELETE FROM `+postTags+` WHERE "tag_name" = 'b'`); err != nil {
					return err
				}
				_, err := database.ExecContext(ctx, `UPDATE `+postTags+` SET "tag_name" = 'd' WHERE "tag_name" = 'e'`)
				return err
			})
			caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CallerUpdate(racing, caller, fixture.postDescriptor, fixture.postTarget(10), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
				golem.GeneratedNestedDeleteMany[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag,
					fixture.postTagTagName.In("a", "b", "c", "d")))); err != nil {
				t.Fatalf("nested deleteMany racing a delete and a newly matching row: %v cause=%v", err, errors.Unwrap(err))
			}
			var remaining []string
			if err := database.SelectContext(ctx, &remaining, `SELECT "tag_name" FROM `+postTags); err != nil || strings.Join(remaining, ",") != "d" {
				t.Fatalf("post tags remaining=%v err=%v; want only the row that matched after enumeration", remaining, err)
			}
			var stored []struct {
				Model  string `db:"model_id"`
				Action string `db:"action"`
				Before []byte `db:"before_identity"`
			}
			if err := database.SelectContext(ctx, &stored, `SELECT "model_id","action","before_identity" FROM `+outbox); err != nil {
				t.Fatal(err)
			}
			var deleted []string
			for _, row := range stored {
				if !strings.EqualFold(row.Model, hex.EncodeToString(fixture.schema.PostTag[:])) {
					continue
				}
				if row.Action != "deleted" {
					t.Fatalf("post tag fact action=%s", row.Action)
				}
				identity, err := mutationfact.DecodeIdentity(row.Before)
				if err != nil {
					t.Fatal(err)
				}
				for _, component := range identity.Components() {
					value, _ := component.PolicyValue()
					if name, ok := value.Text(); ok {
						deleted = append(deleted, name)
					}
				}
			}
			sort.Strings(deleted)
			if strings.Join(deleted, ",") != "a,c" {
				t.Fatalf("post tag delete facts=%v; want exactly the rows this write deleted", deleted)
			}
		})
	}
}

func TestPostgreSQLSingleTargetReplacedBeforeLockingIsMissing(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil)
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tagDescriptor, fixture.tagCreate(30, "a")); err != nil {
				t.Fatal(err)
			}
			tags := nestedAcceptanceTable(fixture.app, fixture.schema.Tag)
			database := fixture.app.database
			replaced := atEnumeration(ctx, 1, func(ctx context.Context) error {
				if _, err := database.ExecContext(ctx, `DELETE FROM `+tags+` WHERE "name" = 'a'`); err != nil {
					return err
				}
				_, err := database.ExecContext(ctx, `INSERT INTO `+tags+` ("id", "name") VALUES ($1, 'a')`, mutationResultUUIDText(39))
				return err
			})
			caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = CallerDelete(replaced, caller, fixture.tagDescriptor, fixture.tagNameTarget("a"))
			assertPublicNotFound(t, err)
			var names []string
			if err := database.SelectContext(ctx, &names, `SELECT "name" FROM `+tags); err != nil || strings.Join(names, ",") != "a" {
				t.Fatalf("tags=%v err=%v; the row that replaced the target must be untouched", names, err)
			}
		})
	}
}
