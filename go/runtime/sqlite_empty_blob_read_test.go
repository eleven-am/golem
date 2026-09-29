package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type emptyBlobReader struct {
	fixture mutationResultFixture
	users   golem.ModelDescriptor[mutationResultUser]
	posts   golem.ModelDescriptor[mutationResultPost]
	bytes   golem.BytesField[mutationResultPost]
	author  golem.EqualField[mutationResultPost, golem.UUID]
	title   golem.TextField[mutationResultPost, string]
	list    golem.ListField[mutationResultPost, string]
	postID  golem.EqualField[mutationResultPost, golem.UUID]
	written golem.ToMany[mutationResultUser, mutationResultPost]
}

func newEmptyBlobReader(t *testing.T, profile exactValueProfile) emptyBlobReader {
	t.Helper()
	return newConfiguredEmptyBlobReader(t, profile, nil)
}

func newConfiguredEmptyBlobReader(t *testing.T, profile exactValueProfile, configure func(*Config[mutationResultPrincipal, mutationResultActor])) emptyBlobReader {
	t.Helper()
	schema := profile.fixture
	provider := golem.SQLite
	if profile.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	posts := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostBytes, schema.PostList, schema.AuthorID, schema.PostTitle, schema.PostID}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	users := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	fixture := mutationResultFixtureForSchemaConfigured(t, profile.database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		config.Descriptors = descriptors
		if configure != nil {
			configure(config)
		}
	})
	return emptyBlobReader{
		fixture: fixture, users: users, posts: posts,
		bytes:   golem.GeneratedBytesField[mutationResultPost](schema.PostBytes),
		author:  golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID),
		title:   golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle),
		list:    golem.GeneratedListField[mutationResultPost, string](schema.PostList),
		postID:  golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID),
		written: golem.GeneratedToMany[mutationResultUser, mutationResultPost](schema.UserPosts, schema.Authorship, schema.Post),
	}
}

func (reader emptyBlobReader) projection() golem.Projection[mutationResultPost] {
	return golem.Select[mutationResultPost](reader.bytes, reader.list, reader.author, reader.title)
}

func (reader emptyBlobReader) assertRow(t *testing.T, path string, row golem.Row[mutationResultPost], title string) {
	t.Helper()
	data, dataPresent := golem.Value(row, reader.bytes).Get()
	author, authorPresent := golem.Value(row, reader.author).Get()
	got, titlePresent := golem.Value(row, reader.title).Get()
	list, listPresent := golem.Value(row, reader.list).Get()
	if !dataPresent || data == nil || len(data) != 0 || !authorPresent || author != (golem.UUID{15: 201}) || !titlePresent || got != title || !listPresent || len(list) != 1 || list[0] != "after" {
		t.Fatalf("%s: bytes=%#v present=%t author=%v present=%t title=%q present=%t list=%v present=%t; want empty bytes, author 201, %q and [after]", path, data, dataPresent, author, authorPresent, got, titlePresent, list, listPresent, title)
	}
}

func (reader emptyBlobReader) selector(id byte) golem.UniqueSelectorValue[mutationResultPost] {
	schema := reader.fixture.schema
	return golem.GeneratedUniqueSelectorValue[mutationResultPost](schema.Post, schema.PostKey, golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: id}))
}

func TestEmptyBytesFollowedByOtherColumnsReadCorrectlyOnEveryPath(t *testing.T) {
	runExactValueProfiles(t, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		profile.seed(t)
		profile.storeEmptyBytes(t, 202)
		reader := newEmptyBlobReader(t, profile)
		system := reader.fixture.app.System()

		unique, err := SystemFindUnique(ctx, system, reader.posts, reader.selector(202), golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil {
			t.Fatalf("find unique: %v", err)
		}
		reader.assertRow(t, "find unique", unique, "exact")

		many, err := SystemFindMany(ctx, system, reader.posts, golem.OrderBy(reader.postID.Asc()), golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil || len(many) != 1 {
			t.Fatalf("find many rows=%d err=%v", len(many), err)
		}
		reader.assertRow(t, "find many", many[0], "exact")

		filtered, err := SystemFindMany(ctx, system, reader.posts, golem.Where(reader.bytes.Eq([]byte{})), golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil || len(filtered) != 1 {
			t.Fatalf("find many filtered on empty bytes rows=%d err=%v", len(filtered), err)
		}
		reader.assertRow(t, "find many filtered on empty bytes", filtered[0], "exact")

		distinct, err := SystemFindMany(ctx, system, reader.posts, golem.Distinct[mutationResultPost](reader.author), golem.OrderBy(reader.postID.Asc()), golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil || len(distinct) != 1 {
			t.Fatalf("find many distinct rows=%d err=%v", len(distinct), err)
		}
		reader.assertRow(t, "find many distinct", distinct[0], "exact")

		for _, strategy := range []struct {
			name string
			ctx  context.Context
		}{
			{"planned", ctx},
			{"batched", context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)},
			{"correlated-oracle", context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadCorrelatedOracle)},
		} {
			users, err := SystemFindMany(strategy.ctx, system, reader.users, golem.Select[mutationResultUser](reader.written.Args(golem.OrderBy(reader.postID.Asc()), reader.projection())))
			if err != nil || len(users) != 1 {
				t.Fatalf("%s relation load users=%d err=%v", strategy.name, len(users), err)
			}
			children, present := golem.Many(users[0], reader.written).Get()
			if !present || len(children) != 1 {
				t.Fatalf("%s relation load children=%d present=%t", strategy.name, len(children), present)
			}
			reader.assertRow(t, strategy.name+" relation load", children[0], "exact")
		}

		zero, err := golem.ParseDecimal("0")
		if err != nil {
			t.Fatal(err)
		}
		post := profile.fixture.Post
		created, err := SystemCreate(ctx, system, reader.posts, golem.GeneratedCreateInput[mutationResultPost](post,
			golem.GeneratedCreateFieldValue(post, reader.postID, golem.UUID{15: 203}),
			golem.GeneratedCreateFieldValue(post, reader.author, golem.UUID{15: 201}),
			golem.GeneratedCreateFieldValue(post, reader.title, "created"),
			golem.GeneratedCreateFieldValue(post, golem.GeneratedEqualField[mutationResultPost, int64](profile.fixture.PostBigInt), int64(0)),
			golem.GeneratedCreateFieldValue(post, golem.GeneratedEqualField[mutationResultPost, golem.Decimal](profile.fixture.PostDecimal), zero),
			golem.GeneratedCreateFieldValue(post, reader.bytes, []byte{}),
			golem.GeneratedCreateFieldValue(post, reader.list, []string{"after"}),
		), reader.projection())
		if err != nil {
			t.Fatalf("create returning: %v", err)
		}
		reader.assertRow(t, "create returning", created, "created")

		updated, err := SystemUpdate(ctx, system, reader.posts, reader.selector(202), golem.GeneratedUpdateInput[mutationResultPost](profile.fixture.Post, golem.GeneratedSetFieldValue(profile.fixture.Post, reader.title, "renamed")), reader.projection())
		if err != nil {
			t.Fatalf("update returning: %v", err)
		}
		reader.assertRow(t, "update returning", updated, "renamed")

		deleted, err := SystemDelete(ctx, system, reader.posts, reader.selector(202), reader.projection())
		if err != nil {
			t.Fatalf("delete returning: %v", err)
		}
		reader.assertRow(t, "delete returning", deleted, "renamed")
	})
}

func (profile exactValueProfile) storeEmptyBytes(t *testing.T, id byte) {
	t.Helper()
	empty, instant := "x''", "1700000000000000"
	if profile.provider == policyir.ProviderPostgreSQL {
		empty, instant = `'\x'::bytea`, "TIMESTAMPTZ '2023-11-14 22:13:20+00'"
	}
	if _, err := profile.database.ExecContext(context.Background(), `UPDATE `+profile.prefix+`"posts" SET "bytes_value"=`+empty+`,"json_value"='{"after":true}',"list_value"='["after"]',"datetime_value"=`+instant+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
}

func (profile exactValueProfile) facts(t *testing.T) int {
	t.Helper()
	var count int
	if err := profile.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
		t.Fatal(err)
	}
	return count
}

func (profile exactValueProfile) titles(t *testing.T) map[string]string {
	t.Helper()
	var rows []struct {
		ID    string `db:"id"`
		Title string `db:"title"`
	}
	if err := profile.database.SelectContext(context.Background(), &rows, `SELECT "id","title" FROM `+profile.prefix+`"posts" ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.ID] = row.Title
	}
	return result
}

func TestEmptyBytesFollowedByOtherColumnsSurviveEveryMutationImage(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewSubscribedMutationExactValues, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		profile.seed(t)
		profile.storeEmptyBytes(t, 202)
		reader := newEmptyBlobReader(t, profile)
		system := reader.fixture.app.System()
		post := profile.fixture.Post
		before := profile.facts(t)

		updated, err := SystemUpdate(ctx, system, reader.posts, reader.selector(202), golem.GeneratedUpdateInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "scalar")), reader.projection())
		if err != nil {
			t.Fatalf("scalar update with a captured image: %v", err)
		}
		reader.assertRow(t, "scalar update", updated, "scalar")

		count, err := SystemUpdateMany(ctx, system, reader.posts, reader.author.Eq(golem.UUID{15: 201}), golem.GeneratedUpdateManyInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "batch")))
		if err != nil || count != 1 {
			t.Fatalf("batch update with captured images count=%d err=%v", count, err)
		}
		if titles := profile.titles(t); titles[mutationResultUUIDText(202)] != "batch" {
			t.Fatalf("batch update titles=%v", titles)
		}

		if _, err := profile.database.ExecContext(ctx, `INSERT INTO `+profile.prefix+`"users" ("id","name") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`)`, mutationResultUUIDText(204), "second"); err != nil {
			t.Fatal(err)
		}
		schema := profile.fixture
		secondUser := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 204}))
		if _, err := SystemUpdate(ctx, system, reader.users, secondUser, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, reader.selector(202)))); err != nil {
			t.Fatalf("nested connect returning the related image: %v", err)
		}
		if _, err := SystemUpdate(ctx, system, reader.users, secondUser, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, reader.selector(202),
				golem.GeneratedUpdateInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "nested"))))); err != nil {
			t.Fatalf("nested update: %v", err)
		}
		if titles := profile.titles(t); titles[mutationResultUUIDText(202)] != "nested" {
			t.Fatalf("nested update titles=%v", titles)
		}
		deleted, err := SystemDeleteMany(ctx, system, reader.posts, reader.author.Eq(golem.UUID{15: 204}))
		if err != nil || deleted != 1 {
			t.Fatalf("batch delete with captured images count=%d err=%v", deleted, err)
		}
		if titles := profile.titles(t); len(titles) != 0 {
			t.Fatalf("batch delete left titles=%v", titles)
		}
		if got := profile.facts(t) - before; got != 5 {
			t.Fatalf("facts recorded=%d want=5", got)
		}
	})
}
