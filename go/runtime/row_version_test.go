package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func rowVersionRestrictFixtures(t *testing.T, action compilerir.ReferentialAction, operation func(*testing.T, mutationProviderAcceptanceFixture)) {
	t.Helper()
	runRelationDeleteProviderProfiles(t, "row_version", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedRowVersionedOnDelete(t, action)
	}, schematest.NewSubscribedRowVersionedOnDeletePostgreSQLNamespaces(action), operation)
}

func postVersions(t *testing.T, profile mutationProviderAcceptanceFixture) map[byte]int64 {
	t.Helper()
	rows, err := profile.fixture.app.database.Query(`SELECT "id", "golem_version" FROM ` + profile.posts)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[byte]int64{}
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			t.Fatal(err)
		}
		versions[postIDByte(t, id)] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return versions
}

func postIDByte(t *testing.T, id string) byte {
	t.Helper()
	for last := 0; last < 256; last++ {
		if mutationResultUUIDText(byte(last)) == id {
			return byte(last)
		}
	}
	t.Fatalf("post id %q is outside the test range", id)
	return 0
}

func requireRise(t *testing.T, label string, before, after int64) {
	t.Helper()
	if after <= before {
		t.Fatalf("%s: version %d does not exceed the prior version %d", label, after, before)
	}
}

func TestRowVersionRisesOnEveryWriteRendererAcrossProviders(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		alice := golem.UUID{15: 1}
		created, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(10, alice, "created"))
		if err != nil {
			t.Fatal(err)
		}
		_ = created
		version := postVersions(t, profile)[10]
		requireRise(t, "create", 0, version)

		if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(10), fixture.updateTitle("updated")); err != nil {
			t.Fatal(err)
		}
		next := postVersions(t, profile)[10]
		requireRise(t, "single update", version, next)
		version = next

		many := golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "batched"))
		if count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), many); err != nil || count != 1 {
			t.Fatalf("update-many count=%d err=%v", count, err)
		}
		next = postVersions(t, profile)[10]
		requireRise(t, "update-many", version, next)
		version = next

		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(11, alice, "second")); err != nil {
			t.Fatal(err)
		}
		other := postVersions(t, profile)[11]
		requireRise(t, "second create", version, other)

		if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(10)); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(10, alice, "recreated")); err != nil {
			t.Fatal(err)
		}
		next = postVersions(t, profile)[10]
		requireRise(t, "recreate after delete", version, next)
		version = next

		if count, err := SystemDeleteMany(ctx, system, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10})); err != nil || count != 1 {
			t.Fatalf("delete-many count=%d err=%v", count, err)
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(10, alice, "recreated again")); err != nil {
			t.Fatal(err)
		}
		requireRise(t, "recreate after delete-many", version, postVersions(t, profile)[10])
	})
}

func TestRowVersionRisesOnNestedUpdateAcrossProviders(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		alice := golem.UUID{15: 1}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(10, alice, "created")); err != nil {
			t.Fatal(err)
		}
		before := postVersions(t, profile)[10]
		schema := fixture.schema
		nested := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, fixture.target(10), fixture.updateTitle("nested")))
		if _, err := SystemUpdate(ctx, system, fixture.userDescriptor, cascadeUserTarget(fixture, 1), nested); err != nil {
			t.Fatal(err)
		}
		requireRise(t, "nested update", before, postVersions(t, profile)[10])
	})
}

func TestRowVersionRisesOnRelationOnlyUpdateAcrossProviders(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(10, golem.UUID{15: 1}, "created")); err != nil {
			t.Fatal(err)
		}
		schema := fixture.schema
		disconnect := golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
			golem.GeneratedNestedDisconnectOne[mutationResultPost, mutationResultUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User))
		if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(10), disconnect); err != nil {
			t.Fatal(err)
		}
		before := postVersions(t, profile)[10]
		if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(10), disconnect); err != nil {
			t.Fatal(err)
		}
		requireRise(t, "relation-only update", before, postVersions(t, profile)[10])
	})
}

func TestRowVersionRisesOnSetNullCascadeAcrossProviders(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionSetNull, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		bob := golem.UUID{15: 2}
		for _, id := range []byte{20, 21} {
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(id, bob, "set-null")); err != nil {
				t.Fatal(err)
			}
		}
		before := postVersions(t, profile)
		if _, err := SystemDelete(ctx, system, fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
			t.Fatal(err)
		}
		after := postVersions(t, profile)
		for _, id := range []byte{20, 21} {
			requireRise(t, "set-null cascade", before[id], after[id])
		}
	})
}

func TestRowVersionIsNotWritableThroughAnyWritePathAcrossProviders(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		alice := golem.UUID{15: 1}
		schema := fixture.schema
		versionColumn := golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostRowVersion)
		forged := golem.GeneratedCreateInput[mutationResultPost](schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 30}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.authorID, alice),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.title, "forged"),
			golem.GeneratedCreateFieldValue(schema.Post, versionColumn, int64(999)))
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, forged); err == nil {
			t.Fatal("create accepted an authored row version")
		}
		if _, ok := postVersions(t, profile)[30]; ok {
			t.Fatal("a refused create left a row behind")
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(31, alice, "honest")); err != nil {
			t.Fatal(err)
		}
		before := postVersions(t, profile)[31]
		update := golem.GeneratedUpdateInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, versionColumn, int64(999)))
		if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(31), update); err == nil {
			t.Fatal("update accepted an authored row version")
		}
		many := golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, versionColumn, int64(999)))
		if _, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 31}), many); err == nil {
			t.Fatal("update-many accepted an authored row version")
		}
		if after := postVersions(t, profile)[31]; after != before {
			t.Fatalf("refused writes changed the version %d -> %d", before, after)
		}
	})
}

func txPostVersion(t *testing.T, ctx context.Context, tx *SystemTx[mutationResultPrincipal, mutationResultActor], fixture mutationResultFixture, id byte) int64 {
	t.Helper()
	version := golem.GeneratedOrderedField[mutationResultPost, int64](fixture.schema.PostRowVersion)
	selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	row, err := SystemTxFindUnique(ctx, tx, fixture.postDescriptor, selector, golem.RuntimeProjectionReadOption(golem.Select[mutationResultPost](version)))
	if err != nil {
		t.Fatal(err)
	}
	value, present := golem.Value(row, version).Get()
	if !present {
		t.Fatal("the row version was not selected")
	}
	return value
}

func TestSQLiteVersionPerStatement(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		if profile.provider != golem.SQLite {
			t.Skip("SQLite counter semantics")
		}
		ctx, fixture := context.Background(), profile.fixture
		alice := golem.UUID{15: 1}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(50, alice, "first")); err != nil {
			t.Fatal(err)
		}
		var deleted, recreated int64
		err := SystemTransaction(ctx, fixture.app.System(), func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(50), fixture.updateTitle("changed")); err != nil {
				return err
			}
			deleted = txPostVersion(t, ctx, tx, fixture, 50)
			if _, err := SystemTxDelete(ctx, tx, fixture.postDescriptor, fixture.target(50)); err != nil {
				return err
			}
			if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(50, alice, "recreated")); err != nil {
				return err
			}
			recreated = txPostVersion(t, ctx, tx, fixture, 50)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		requireRise(t, "recreated against deleted in one transaction", deleted, recreated)
		requireRise(t, "committed recreated row", deleted, postVersions(t, profile)[50])
	})
}
