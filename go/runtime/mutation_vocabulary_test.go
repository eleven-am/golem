package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

var mutationVocabularyNamespaceSequence atomic.Uint64

type mutationVocabularyFixture struct {
	mutationResultFixture
	bigInt      golem.OrderedField[mutationResultPost, int64]
	optionalInt golem.NullableOrderedField[mutationResultPost, int64]
}

func TestPublicSetNullIncrementDecrementAndExactAuthorizationAcrossProviders(t *testing.T) {
	forEachMutationVocabularyProvider(t, func(t *testing.T, fixture mutationVocabularyFixture) {
		ctx := context.Background()
		posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
		insert := fixture.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title","big_int","decimal_value","optional_int") VALUES (?,?,?,?,?,?)`)
		if _, err := fixture.app.database.ExecContext(ctx, insert, mutationResultUUIDText(201), mutationResultUUIDText(1), "before", int64(10), 0, int64(5)); err != nil {
			t.Fatal(err)
		}
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		first := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "set"),
			golem.GeneratedIncrementFieldValue(fixture.schema.Post, fixture.bigInt, int64(2)),
			golem.GeneratedNullFieldValue(fixture.schema.Post, fixture.optionalInt),
		)
		row, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(201), first, golem.Select[mutationResultPost](fixture.title, fixture.bigInt, fixture.optionalInt))
		if err != nil {
			t.Fatal(err)
		}
		if title, ok := golem.Value(row, fixture.title).Get(); !ok || title != "set" {
			t.Fatalf("set result title=%q present=%t", title, ok)
		}
		if value, ok := golem.Value(row, fixture.bigInt).Get(); !ok || value != 12 {
			t.Fatalf("increment result=%d present=%t", value, ok)
		}
		if _, ok := golem.Value(row, fixture.optionalInt).Get(); ok {
			t.Fatal("null result was reported as a concrete value")
		}
		second := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedDecrementFieldValue(fixture.schema.Post, fixture.bigInt, int64(3)),
		)
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(201), second); err != nil {
			t.Fatal(err)
		}
		denied := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedIncrementFieldValue(fixture.schema.Post, fixture.bigInt, int64(1)),
		)
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(201), denied); err == nil {
			t.Fatal("increment whose exact locked pre-image denied the field committed")
		}
		var title string
		var big int64
		var optional *int64
		query := fixture.app.database.Rebind(`SELECT "title","big_int","optional_int" FROM ` + posts + ` WHERE "id"=?`)
		if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(201)).Scan(&title, &big, &optional); err != nil {
			t.Fatal(err)
		}
		if title != "set" || big != 9 || optional != nil {
			t.Fatalf("persisted vocabulary title=%q big=%d optional=%v", title, big, optional)
		}
		var facts int
		if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != 2 {
			t.Fatalf("successful/denied exact facts=%d err=%v", facts, err)
		}
	})
}

func forEachMutationVocabularyProvider(t *testing.T, run func(*testing.T, mutationVocabularyFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "mutation-vocabulary.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		schema := schematest.NewMutationVocabulary(t)
		if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, openMutationVocabularyFixture(t, database, golem.SQLite, schema))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			sequence := mutationVocabularyNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_vocabulary_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_vocabulary_system_%s_%d_%d", profile.name, os.Getpid(), sequence))
			schema := schematest.NewMutationVocabularyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, openMutationVocabularyFixture(t, database, golem.PostgreSQL, schema))
		})
	}
}

func openMutationVocabularyFixture(t testing.TB, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) mutationVocabularyFixture {
	t.Helper()
	ctx := context.Background()
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle, schema.PostBigInt, schema.PostDecimal, schema.PostOptionalInt}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
	title := golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle)
	bigInt := golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostBigInt)
	optionalInt := golem.GeneratedNullableOrderedField[mutationResultPost, int64](schema.PostOptionalInt)
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		rules.CannotUpdateFields(golem.All[mutationResultPost](), bigInt, optionalInt)
		rules.CanUpdateFields(bigInt.GTE(10), bigInt)
		rules.CanUpdateFields(optionalInt.Eq(5), optionalInt)
		return rules.Freeze(schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: database, Provider: provider, Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	postAuthor := golem.GeneratedToOne[mutationResultPost, mutationResultUser](schema.PostAuthor, schema.Authorship, schema.User)
	base := mutationResultFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID), userName: userName,
		postID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID), authorID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID),
		title: title, author: postAuthor,
	}
	return mutationVocabularyFixture{mutationResultFixture: base, bigInt: bigInt, optionalInt: optionalInt}
}
