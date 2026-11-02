package runtime

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type dateTimeGuardFixture struct {
	app    *App[mutationResultPrincipal, mutationResultActor]
	schema schematest.Fixture
	users  golem.ModelDescriptor[mutationResultUser]
	posts  golem.ModelDescriptor[mutationResultPost]
	postID golem.EqualField[mutationResultPost, golem.UUID]
	author golem.EqualField[mutationResultPost, golem.UUID]
	title  golem.TextField[mutationResultPost, string]
	at     golem.OrderedField[mutationResultPost, time.Time]
}

func newDateTimeGuardFixture(t *testing.T, profile exactValueProfile) dateTimeGuardFixture {
	t.Helper()
	schema := profile.fixture
	provider := golem.SQLite
	if profile.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID, schema.PostAt)
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	posts := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostAt, schema.AuthorID, schema.PostTitle, schema.PostID}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	users := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	opened := mutationResultFixtureForSchemaConfigured(t, profile.database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		config.Descriptors = descriptors
	})
	return dateTimeGuardFixture{
		app: opened.app, schema: schema, users: users, posts: posts,
		postID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID),
		author: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID),
		title:  golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle),
		at:     golem.GeneratedOrderedField[mutationResultPost, time.Time](schema.PostAt),
	}
}

func (fixture dateTimeGuardFixture) selector(id byte, at time.Time) golem.UniqueSelectorValue[mutationResultPost] {
	return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}),
		golem.GeneratedSelectorComponent(fixture.schema.PostAt, at))
}

func (fixture dateTimeGuardFixture) createInput(id byte, at time.Time, title string) golem.CreateInput[mutationResultPost] {
	post := fixture.schema.Post
	return golem.GeneratedCreateInput[mutationResultPost](post,
		golem.GeneratedCreateFieldValue(post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(post, fixture.author, golem.UUID{15: 201}),
		golem.GeneratedCreateFieldValue(post, fixture.title, title),
		golem.GeneratedCreateFieldValue(post, fixture.at, at),
	)
}

func (fixture dateTimeGuardFixture) retitle(title string) golem.UpdateInput[mutationResultPost] {
	return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func (fixture dateTimeGuardFixture) ids(t *testing.T, options ...golem.ReadOption[mutationResultPost]) []byte {
	t.Helper()
	rows, err := SystemFindMany(context.Background(), fixture.app.System(), fixture.posts, options...)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 0, len(rows))
	for _, row := range rows {
		id, present := golem.Value(row, fixture.postID).Get()
		if !present {
			t.Fatal("post id is absent")
		}
		result = append(result, id[15])
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func assertPrecisionRefusal(t *testing.T, path string, err error, field golem.FieldID) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Reason != golem.ReasonValuePrecision || failure.Field != field {
		t.Fatalf("%s: err=%v; want BAD_USER_INPUT VALUE_PRECISION on the datetime field", path, err)
	}
}

func TestMutationTargetGuardRewritesOffGridDateTimeWhileSelectorRefuses(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewDateTimeKeyed, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		fixture := newDateTimeGuardFixture(t, profile)
		system := fixture.app.System()
		schema := fixture.schema
		if _, err := SystemCreate(ctx, system, fixture.users, golem.GeneratedCreateInput[mutationResultUser](schema.User,
			golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID), golem.UUID{15: 201}),
			golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedTextField[mutationResultUser, string](schema.UserName), "guard-author"),
		)); err != nil {
			t.Fatal(err)
		}
		floor := time.Date(2026, 10, 25, 12, 0, 0, 123456000, time.UTC)
		offGrid := floor.Add(time.Nanosecond)
		stamps := []time.Time{floor.Add(-time.Microsecond), floor, floor.Add(time.Microsecond)}
		guards := map[string]golem.Predicate[mutationResultPost]{
			"lt":  fixture.at.LT(offGrid),
			"lte": fixture.at.LTE(offGrid),
			"gt":  fixture.at.GT(offGrid),
			"gte": fixture.at.GTE(offGrid),
			"eq":  fixture.at.Eq(offGrid),
			"ne":  fixture.at.Ne(offGrid),
		}
		names := make([]string, 0, len(guards))
		for name := range guards {
			names = append(names, name)
		}
		sort.Strings(names)
		author := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 201}))
		operations := []struct {
			name    string
			miss    golem.ErrorCode
			deletes bool
			run     func(id byte, at time.Time, guard golem.Predicate[mutationResultPost]) error
		}{
			{"update", golem.CodeNotFound, false, func(id byte, at time.Time, guard golem.Predicate[mutationResultPost]) error {
				_, err := SystemUpdate(ctx, system, fixture.posts, fixture.selector(id, at).And(guard), fixture.retitle("touched"))
				return err
			}},
			{"delete", golem.CodeNotFound, true, func(id byte, at time.Time, guard golem.Predicate[mutationResultPost]) error {
				_, err := SystemDelete(ctx, system, fixture.posts, fixture.selector(id, at).And(guard))
				return err
			}},
			{"upsert", golem.CodeConflict, false, func(id byte, at time.Time, guard golem.Predicate[mutationResultPost]) error {
				_, err := SystemUpsert(ctx, system, fixture.posts, fixture.selector(id, at).And(guard), fixture.createInput(id, at, "created"), fixture.retitle("touched"))
				return err
			}},
			{"nested-update", golem.CodeNotFound, false, func(id byte, at time.Time, guard golem.Predicate[mutationResultPost]) error {
				_, err := SystemUpdate(ctx, system, fixture.users, author, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, fixture.selector(id, at).And(guard), fixture.retitle("touched"))))
				return err
			}},
		}
		next := byte(1)
		for _, operation := range operations {
			for _, name := range names {
				guard := guards[name]
				t.Run(operation.name+"/"+name, func(t *testing.T) {
					first := next
					next += byte(len(stamps))
					rowIDs := make([]golem.UUID, len(stamps))
					for index, at := range stamps {
						id := first + byte(index)
						rowIDs[index] = golem.UUID{15: id}
						if _, err := SystemCreate(ctx, system, fixture.posts, fixture.createInput(id, at, "seeded")); err != nil {
							t.Fatal(err)
						}
					}
					want := fixture.ids(t, golem.Where(fixture.postID.In(rowIDs...).And(guard)))
					var matched []byte
					for index, at := range stamps {
						id := first + byte(index)
						err := operation.run(id, at, guard)
						if err == nil {
							matched = append(matched, id)
							continue
						}
						var failure *golem.Error
						if !errors.As(err, &failure) || failure.Code != operation.miss {
							t.Fatalf("row %d: guard miss err=%v; want %s", id, err, operation.miss)
						}
					}
					if string(matched) != string(want) {
						t.Fatalf("guard matched rows %v; findMany with the same filter selects %v", matched, want)
					}
					affected := fixture.ids(t, golem.Where(fixture.postID.In(rowIDs...).And(fixture.title.Eq("touched"))))
					if operation.deletes {
						remaining := fixture.ids(t, golem.Where(fixture.postID.In(rowIDs...)))
						affected = nil
						for index := range stamps {
							id := first + byte(index)
							if bytes.IndexByte(remaining, id) < 0 {
								affected = append(affected, id)
							}
						}
					}
					if string(affected) != string(want) {
						t.Fatalf("rows changed %v; findMany with the same filter selects %v", affected, want)
					}
					_, err := SystemUpdate(ctx, system, fixture.posts, fixture.selector(first+1, offGrid).And(guard), fixture.retitle("refused"))
					assertPrecisionRefusal(t, "off-grid selector", err, schema.PostAt)
				})
			}
		}
		refusals := map[string]func() error{
			"update": func() error {
				_, err := SystemUpdate(ctx, system, fixture.posts, fixture.selector(1, offGrid), fixture.retitle("refused"))
				return err
			},
			"delete": func() error {
				_, err := SystemDelete(ctx, system, fixture.posts, fixture.selector(1, offGrid))
				return err
			},
			"upsert": func() error {
				_, err := SystemUpsert(ctx, system, fixture.posts, fixture.selector(1, offGrid), fixture.createInput(1, offGrid, "refused"), fixture.retitle("refused"))
				return err
			},
			"nested-update": func() error {
				_, err := SystemUpdate(ctx, system, fixture.users, author, golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, fixture.selector(1, offGrid), fixture.retitle("refused"))))
				return err
			},
		}
		for name, refuse := range refusals {
			t.Run("selector-refused/"+name, func(t *testing.T) {
				assertPrecisionRefusal(t, name, refuse(), schema.PostAt)
			})
		}
	})
}
