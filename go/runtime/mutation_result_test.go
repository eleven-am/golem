package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type mutationResultPrincipal struct{}
type mutationResultActor struct{}
type mutationResultUser struct{}
type mutationResultPost struct{}

type mutationResultFixture struct {
	app            *App[mutationResultPrincipal, mutationResultActor]
	userDescriptor golem.ModelDescriptor[mutationResultUser]
	postDescriptor golem.ModelDescriptor[mutationResultPost]
	userID         golem.EqualField[mutationResultUser, golem.UUID]
	userName       golem.TextField[mutationResultUser, string]
	postID         golem.EqualField[mutationResultPost, golem.UUID]
	authorID       golem.EqualField[mutationResultPost, golem.UUID]
	title          golem.TextField[mutationResultPost, string]
	author         golem.ToOne[mutationResultPost, mutationResultUser]
	schema         schematest.Fixture
}

func TestDeleteProjectionUsesAuthorizedPreImage(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		alice := golem.UUID{15: 1}
		bob := golem.UUID{15: 2}

		empty, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(11, alice, "empty"))
		if err != nil {
			t.Fatal(err)
		}
		if golem.Value(empty, fixture.postID).IsSelected() || golem.Value(empty, fixture.title).IsSelected() {
			t.Fatal("zero projection disclosed fields")
		}

		selected, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(11),
			fixture.updateTitle("selected"), golem.Select[mutationResultPost](fixture.postID, fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		if title, present := golem.Value(selected, fixture.title).Get(); !present || title != "selected" {
			t.Fatalf("selected title=%q present=%t", title, present)
		}

		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(12, bob, "masked-source")); err != nil {
			t.Fatal(err)
		}
		masked, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(12),
			fixture.updateTitle("masked-result"), golem.Select[mutationResultPost](fixture.title))
		if err != nil {
			t.Fatalf("%v: %v", err, errors.Unwrap(err))
		}
		if title := golem.Value(masked, fixture.title); !title.IsSelected() || !title.IsNull() {
			t.Fatalf("conditional mutation field mask state=%d", title.State())
		}

		deleted, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(11),
			golem.Select[mutationResultPost](fixture.title, fixture.author.Select(fixture.userName)))
		if err != nil {
			t.Fatal(err)
		}
		if title, present := golem.Value(deleted, fixture.title).Get(); !present || title != "selected" {
			t.Fatalf("delete pre-image title=%q present=%t", title, present)
		}
		author, present := golem.One(deleted, fixture.author).Get()
		if !present {
			t.Fatal("delete relation projection is absent")
		}
		if name, ok := golem.Value(author, fixture.userName).Get(); !ok || name != "alice" {
			t.Fatalf("delete pre-image author=%q present=%t", name, ok)
		}
		var count int
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+profile.posts+` WHERE "id" = `+profile.placeholder(1), mutationResultUUIDText(11)); err != nil || count != 0 {
			t.Fatalf("deleted rows=%d err=%v", count, err)
		}
	})
}

func TestRootScalarMutationProjectionCardinalityRefusesBeforeWrite(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	projection := golem.Select[mutationResultPost](fixture.title)
	_, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(20, golem.UUID{15: 1}, "refused"), projection, projection)
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(20)); err != nil || count != 0 {
		t.Fatalf("refused create rows=%d err=%v", count, err)
	}
}

func TestSystemUpdateExecutesNestedConnectAtomicallyOnSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(24, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM "_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user),
	)
	if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(24), input); err != nil {
		t.Fatal(err)
	}
	var title, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "title", "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(24)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "after" || author != mutationResultUUIDText(2) {
		t.Fatalf("nested update title=%q author=%q", title, author)
	}
	rows, err := fixture.app.database.QueryxContext(ctx, `SELECT "causation_id", "transaction_ordinal" FROM "_golem_outbox" ORDER BY "transaction_ordinal"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var causation string
	var ordinals []int64
	for rows.Next() {
		var current string
		var ordinal int64
		if err := rows.Scan(&current, &ordinal); err != nil {
			t.Fatal(err)
		}
		if causation == "" {
			causation = current
		} else if current != causation {
			t.Fatalf("nested facts split causation IDs: %q != %q", current, causation)
		}
		ordinals = append(ordinals, ordinal)
	}
	if len(ordinals) != 2 || ordinals[0] != 1 || ordinals[1] != 2 {
		t.Fatalf("nested fact ordinals=%v", ordinals)
	}
}

func TestSystemCreateExecutesInverseNestedCreateWithRuntimeOwnedForeignKey(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	post := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 25}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "nested"),
	)
	input := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 5}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "nested-owner"),
		golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, input); err != nil {
		t.Fatal(err)
	}
	var author, title string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id", "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(25)).Scan(&author, &title); err != nil {
		t.Fatal(err)
	}
	if author != mutationResultUUIDText(5) || title != "nested" {
		t.Fatalf("inverse nested create author=%q title=%q", author, title)
	}
	var facts int
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || facts != 1 {
		t.Fatalf("nested create facts=%d err=%v", facts, err)
	}
	conflict := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 6}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "must-rollback"),
		golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, conflict); err == nil {
		t.Fatal("nested child conflict unexpectedly committed")
	}
	var users int
	if err := fixture.app.database.GetContext(ctx, &users, `SELECT COUNT(*) FROM "users" WHERE "id" = ?`, mutationResultUUIDText(6)); err != nil || users != 0 {
		t.Fatalf("failed nested graph left root users=%d err=%v", users, err)
	}
}

func TestRootScalarMutationTxMethodsJoinRollbackAndCommit(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	system := fixture.app.System()
	rollbackCause := errors.New("rollback")
	err := SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		row, createErr := SystemTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(30, golem.UUID{15: 1}, "rollback"), golem.Select[mutationResultPost](fixture.title))
		if createErr != nil {
			return createErr
		}
		if title, present := golem.Value(row, fixture.title).Get(); !present || title != "rollback" {
			t.Fatalf("transaction projection title=%q present=%t", title, present)
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback error=%v", err)
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(30)); err != nil || count != 0 {
		t.Fatalf("rolled-back rows=%d err=%v", count, err)
	}

	if err := SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, createErr := SystemTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(31, golem.UUID{15: 1}, "commit")); createErr != nil {
			return createErr
		}
		updated, updateErr := SystemTxUpdate(ctx, transaction, fixture.postDescriptor, fixture.target(31), fixture.updateTitle("committed"), golem.Select[mutationResultPost](fixture.title))
		if updateErr != nil {
			return updateErr
		}
		if title, present := golem.Value(updated, fixture.title).Get(); !present || title != "committed" {
			t.Fatalf("updated title=%q present=%t", title, present)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := fixture.app.database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(31)); err != nil || title != "committed" {
		t.Fatalf("committed title=%q err=%v", title, err)
	}
}

func newMutationResultFixture(t *testing.T) mutationResultFixture {
	return newMutationResultFixtureWithLimits(t, MutationLimits{})
}

func newMutationResultFixtureWithLimits(t *testing.T, limits MutationLimits) mutationResultFixture {
	return newMutationResultFixtureWithHooks(t, limits, nil, nil)
}

func newMutationResultFixtureWithHooks(t *testing.T, limits MutationLimits, hookFactory func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor], afterCommitError func(context.Context, golem.AfterCommitFailure)) mutationResultFixture {
	return newMutationResultFixtureWithHooksAndDatabase(t, limits, hookFactory, afterCommitError, nil)
}

func newMutationResultFixtureWithHooksAndDatabase(t *testing.T, limits MutationLimits, hookFactory func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor], afterCommitError func(context.Context, golem.AfterCommitFailure), supplied *sqlx.DB) mutationResultFixture {
	return newMutationResultFixtureWithHooksAndDatabaseMode(t, limits, hookFactory, afterCommitError, supplied, true)
}

func newMutationResultFixtureWithHooksAndDatabaseMode(t *testing.T, limits MutationLimits, hookFactory func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor], afterCommitError func(context.Context, golem.AfterCommitFailure), supplied *sqlx.DB, initialize bool) mutationResultFixture {
	t.Helper()
	ctx := context.Background()
	schemaFixture := schematest.NewSubscribedIndexed(t)
	provider := sqlite.New()
	database := supplied
	if database == nil {
		var err error
		database, _, err = provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "mutation-result.db"))
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { database.Close() })
	if initialize {
		if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
			if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, user[0], user[1]); err != nil {
				t.Fatal(err)
			}
		}
	}

	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[mutationResultUser, string](schemaFixture.UserName)
	postTitle := golem.GeneratedTextField[mutationResultPost, string](schemaFixture.PostTitle)
	var hooks []golem.HookBinding[mutationResultActor]
	if hookFactory != nil {
		hooks = hookFactory(schemaFixture, postTitle)
	}
	postAuthor := golem.GeneratedToOne[mutationResultPost, mutationResultUser](schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User)
	allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schemaFixture.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schemaFixture.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schemaFixture.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		rules.CannotReadFields(golem.All[mutationResultPost](), postTitle)
		rules.CanReadFields(postAuthor.Is(userName.Eq("alice")), postTitle)
		return rules.Freeze(schemaFixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, hooks)
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: database, Provider: golem.SQLite, Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		MutationLimits:   limits,
		AfterCommitError: afterCommitError,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return mutationResultFixture{
		app: app, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: golem.GeneratedEqualField[mutationResultUser, golem.UUID](schemaFixture.UserID), userName: userName,
		postID:   golem.GeneratedEqualField[mutationResultPost, golem.UUID](schemaFixture.PostID),
		authorID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schemaFixture.AuthorID),
		title:    postTitle, author: postAuthor, schema: schemaFixture,
	}
}

func (fixture mutationResultFixture) createPost(id byte, author golem.UUID, title string) golem.CreateInput[mutationResultPost] {
	return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, author),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
	)
}

func (fixture mutationResultFixture) updateTitle(title string) golem.UpdateInput[mutationResultPost] {
	return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func (fixture mutationResultFixture) target(id byte) golem.MutationTarget[mutationResultPost] {
	return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
}

func mutationResultUUIDText(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", last)
}
