package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func (profile exactValueProfile) storeBytes(t *testing.T, id byte, value []byte) {
	t.Helper()
	literal := "x'" + hexUpper(value) + "'"
	if profile.provider == policyir.ProviderPostgreSQL {
		literal = `'\x` + hexUpper(value) + `'::bytea`
	}
	if _, err := profile.database.ExecContext(context.Background(), `UPDATE `+profile.prefix+`"posts" SET "bytes_value"=`+literal+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
}

func hexUpper(value []byte) string {
	const digits = "0123456789ABCDEF"
	result := make([]byte, 0, len(value)*2)
	for _, item := range value {
		result = append(result, digits[item>>4], digits[item&15])
	}
	return string(result)
}

func (profile exactValueProfile) assertStoredEmptyBytes(t *testing.T, path string, id byte) {
	t.Helper()
	var stored struct {
		Null   bool  `db:"is_null"`
		Length int64 `db:"size"`
	}
	query := `SELECT "bytes_value" IS NULL AS "is_null", COALESCE(length("bytes_value"),-1) AS "size" FROM ` + profile.prefix + `"posts" WHERE "id"=` + profile.placeholder(1)
	if err := profile.database.GetContext(context.Background(), &stored, query, mutationResultUUIDText(id)); err != nil || stored.Null || stored.Length != 0 {
		t.Fatalf("%s stored bytes=%+v err=%v; want a non-NULL zero-length value", path, stored, err)
	}
}

func TestEmptyBytesAreWrittenAsEmptyOnEveryWritePath(t *testing.T) {
	runExactValueProfiles(t, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		profile.seed(t)
		reader := newEmptyBlobReader(t, profile)
		system := reader.fixture.app.System()
		schema := profile.fixture
		post := schema.Post
		empty := golem.GeneratedSetFieldValue(post, reader.bytes, []byte{})

		profile.storeBytes(t, 202, []byte{1})
		if count, err := SystemUpdateMany(ctx, system, reader.posts, reader.postID.Eq(golem.UUID{15: 202}), golem.GeneratedUpdateManyInput[mutationResultPost](post, empty)); err != nil || count != 1 {
			t.Fatalf("batch update count=%d err=%v", count, err)
		}
		profile.assertStoredEmptyBytes(t, "batch update", 202)

		if count, err := SystemUpdateMany(ctx, system, reader.posts, reader.bytes.Eq([]byte{}), golem.GeneratedUpdateManyInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "batch-matched"))); err != nil || count != 1 {
			t.Fatalf("batch update filtered on empty bytes count=%d err=%v", count, err)
		}

		profile.storeBytes(t, 202, []byte{1})
		zero, err := golem.ParseDecimal("0")
		if err != nil {
			t.Fatal(err)
		}
		createFor := func(id byte) golem.CreateInput[mutationResultPost] {
			return golem.GeneratedCreateInput[mutationResultPost](post,
				golem.GeneratedCreateFieldValue(post, reader.postID, golem.UUID{15: id}),
				golem.GeneratedCreateFieldValue(post, reader.author, golem.UUID{15: 201}),
				golem.GeneratedCreateFieldValue(post, reader.title, "upserted"),
				golem.GeneratedCreateFieldValue(post, golem.GeneratedEqualField[mutationResultPost, int64](schema.PostBigInt), int64(0)),
				golem.GeneratedCreateFieldValue(post, golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal), zero),
				golem.GeneratedCreateFieldValue(post, reader.bytes, []byte{}),
			)
		}
		update := golem.GeneratedUpdateInput[mutationResultPost](post, empty)
		if _, err := SystemUpsert(ctx, system, reader.posts, reader.selector(205), createFor(205), update); err != nil {
			t.Fatalf("upsert create: %v", err)
		}
		profile.assertStoredEmptyBytes(t, "upsert create", 205)
		if _, err := SystemUpsert(ctx, system, reader.posts, reader.selector(202), createFor(202), update); err != nil {
			t.Fatalf("upsert update: %v", err)
		}
		profile.assertStoredEmptyBytes(t, "upsert update", 202)

		profile.storeBytes(t, 202, []byte{1})
		profile.storeBytes(t, 205, []byte{1})
		author := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 201}))
		if _, err := SystemUpdate(ctx, system, reader.users, author, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, post, reader.selector(202), update))); err != nil {
			t.Fatalf("nested update: %v", err)
		}
		profile.assertStoredEmptyBytes(t, "nested update", 202)
		if _, err := SystemUpdate(ctx, system, reader.users, author, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, post, reader.postID.Eq(golem.UUID{15: 205}),
				golem.GeneratedUpdateManyInput[mutationResultPost](post, empty)))); err != nil {
			t.Fatalf("nested update many: %v", err)
		}
		profile.assertStoredEmptyBytes(t, "nested update many", 205)
		if _, err := SystemUpdate(ctx, system, reader.users, author, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
			golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, post, reader.bytes.Eq([]byte{}),
				golem.GeneratedUpdateManyInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "nested-matched"))))); err != nil {
			t.Fatalf("nested update many filtered on empty bytes: %v", err)
		}
		if titles := profile.titles(t); titles[mutationResultUUIDText(202)] != "nested-matched" || titles[mutationResultUUIDText(205)] != "nested-matched" {
			t.Fatalf("nested update many filtered on empty bytes titles=%v", titles)
		}
		if _, err := SystemUpsert(ctx, system, reader.posts, reader.selector(202).And(reader.bytes.Eq([]byte{})), createFor(202), golem.GeneratedUpdateInput[mutationResultPost](post, golem.GeneratedSetFieldValue(post, reader.title, "guarded"))); err != nil {
			t.Fatalf("upsert guarded by empty bytes: %v", err)
		}
		if titles := profile.titles(t); titles[mutationResultUUIDText(202)] != "guarded" {
			t.Fatalf("upsert guarded by empty bytes titles=%v", titles)
		}
	})
}
