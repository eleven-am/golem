package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type graphMutationPrincipal struct{}
type graphMutationActor struct{}
type graphMutationUser struct{}
type graphMutationPost struct{}
type graphMutationComment struct{}
type recursiveMutationComment struct{}
type compositeMutationTenant struct{}
type compositeMutationItem struct{}

type graphMutationFixture struct {
	app               *App[graphMutationPrincipal, graphMutationActor]
	schema            schematest.GraphFixture
	userDescriptor    golem.ModelDescriptor[graphMutationUser]
	postDescriptor    golem.ModelDescriptor[graphMutationPost]
	commentDescriptor golem.ModelDescriptor[graphMutationComment]
	userID            golem.EqualField[graphMutationUser, golem.UUID]
	userName          golem.TextField[graphMutationUser, string]
	postID            golem.EqualField[graphMutationPost, golem.UUID]
	postTitle         golem.TextField[graphMutationPost, string]
	commentID         golem.EqualField[graphMutationComment, golem.UUID]
	commentBody       golem.TextField[graphMutationComment, string]
}

type recursiveMutationFixture struct {
	app        *App[graphMutationPrincipal, graphMutationActor]
	schema     schematest.RecursiveCommentFixture
	descriptor golem.ModelDescriptor[recursiveMutationComment]
	id         golem.EqualField[recursiveMutationComment, golem.UUID]
	body       golem.TextField[recursiveMutationComment, string]
}

type compositeMutationFixture struct {
	app              *App[graphMutationPrincipal, graphMutationActor]
	schema           schematest.CompositeRelationFixture
	tenantDescriptor golem.ModelDescriptor[compositeMutationTenant]
	itemDescriptor   golem.ModelDescriptor[compositeMutationItem]
	tenantRegion     golem.EqualField[compositeMutationTenant, golem.UUID]
	tenantID         golem.EqualField[compositeMutationTenant, golem.UUID]
	itemRegion       golem.EqualField[compositeMutationItem, golem.UUID]
	itemID           golem.EqualField[compositeMutationItem, golem.UUID]
	ownerRegion      golem.EqualField[compositeMutationItem, golem.UUID]
	ownerID          golem.EqualField[compositeMutationItem, golem.UUID]
}

func TestCreateNestedDenialRollsBackDataAndFactsAtEveryDepth(t *testing.T) {
	for _, test := range []struct {
		name string
		deny func(schematest.GraphFixture) golem.ModelID
	}{
		{name: "post at depth one", deny: func(schema schematest.GraphFixture) golem.ModelID { return schema.Post }},
		{name: "comment at depth two", deny: func(schema schematest.GraphFixture) golem.ModelID { return schema.Comment }},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := schematest.NewSubscribedGraph(t)
			t.Run("sqlite", func(t *testing.T) {
				assertNestedDenialAtomic(t, newGraphMutationFixture(t, schema, test.deny(schema)))
			})
			for _, profile := range postgresAcceptanceProfiles() {
				profile := profile
				t.Run("postgresql-"+profile.name, func(t *testing.T) {
					if profile.dsn == "" {
						t.Skip(profile.env + " is not configured")
					}
					assertNestedDenialAtomic(t, newPostgresGraphMutationFixtureWithHooks(t, profile, test.deny(schema), nil))
				})
			}
		})
	}
}

func assertNestedDenialAtomic(t testing.TB, fixture graphMutationFixture) {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CallerCreate(context.Background(), caller, fixture.userDescriptor, fixture.deepCreate(1, 2, 3))
	if err == nil {
		t.Fatal("denied nested graph unexpectedly committed")
	}
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeForbidden {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("nested denial failure: %T: %v", cause, cause)
		}
		t.Fatalf("nested denial=%#v err=%v", public, err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 0, 0, 0, 0)
}

func TestNestedHookAndFactOrderIsDeterministic(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertNestedHookAndFactOrder(t, func(schema schematest.GraphFixture, hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
			return newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks)
		})
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertNestedHookAndFactOrder(t, func(_ schematest.GraphFixture, hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
				return newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
			})
		})
	}
}

func assertNestedHookAndFactOrder(t testing.TB, open func(schematest.GraphFixture, []golem.HookBinding[graphMutationActor]) graphMutationFixture) {
	t.Helper()
	var lock sync.Mutex
	before, after, afterCommit := make([]string, 0, 3), make([]string, 0, 3), make([]string, 0, 3)
	record := func(target *[]string, model string) {
		lock.Lock()
		defer lock.Unlock()
		*target = append(*target, model)
	}
	schema := schematest.NewSubscribedGraph(t)
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			record(&before, "user")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			record(&after, "user")
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			record(&afterCommit, "user")
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
			record(&before, "post")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			record(&after, "post")
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			record(&afterCommit, "post")
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookRequest[graphMutationComment]](schema.Comment, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationComment]) error {
			record(&before, "comment")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](schema.Comment, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationComment]) error {
			record(&after, "comment")
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](schema.Comment, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationComment]) error {
			record(&afterCommit, "comment")
			return nil
		}),
	}
	fixture := open(schema, hooks)
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(context.Background(), caller, fixture.userDescriptor, fixture.deepCreate(31, 32, 33)); err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("nested hook-order failure: %T: %v", cause, cause)
		}
		t.Fatal(err)
	}
	wantBefore := []string{"user", "post", "comment"}
	wantReverse := []string{"comment", "post", "user"}
	if !reflect.DeepEqual(before, wantBefore) || !reflect.DeepEqual(after, wantReverse) || !reflect.DeepEqual(afterCommit, wantReverse) {
		t.Fatalf("hook order before=%v after=%v afterCommit=%v", before, after, afterCommit)
	}
	type orderedFact struct {
		ModelID string `db:"model_id"`
		Ordinal int64  `db:"transaction_ordinal"`
	}
	var facts []orderedFact
	if err := fixture.app.database.Select(&facts, `SELECT "model_id", "transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	modelHex := func(model golem.ModelID) string { return hex.EncodeToString(model[:]) }
	wantModels := []string{modelHex(schema.User), modelHex(schema.Post), modelHex(schema.Comment)}
	if len(facts) != len(wantModels) {
		t.Fatalf("fact count=%d want=%d", len(facts), len(wantModels))
	}
	for index, fact := range facts {
		if fact.ModelID != wantModels[index] || fact.Ordinal != int64(index+1) {
			t.Fatalf("fact[%d]=%#v want model=%s ordinal=%d", index, fact, wantModels[index], index+1)
		}
	}
}

func TestNestedCompositeRelationsAndRecursiveComments(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertNestedCompositeRelation(t, newCompositeMutationFixture(t))
		assertRecursiveComments(t, newRecursiveMutationFixture(t))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertNestedCompositeRelation(t, newPostgresCompositeMutationFixture(t, profile))
			assertRecursiveComments(t, newPostgresRecursiveMutationFixture(t, profile))
		})
	}
}

func TestNestedMutationEmitsFactsForEveryChangedRow(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertNestedFacts(t, newGraphMutationFixture(t, schematest.NewSubscribedGraph(t), golem.ModelID{}), 11, 12, 13)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertNestedFacts(t, newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil), 11, 12, 13)
		})
	}
}

func TestNestedAndBatchFactsHaveStableTransactionOrdinals(t *testing.T) {
	t.Run("nested sqlite", func(t *testing.T) {
		assertNestedFacts(t, newGraphMutationFixture(t, schematest.NewSubscribedGraph(t), golem.ModelID{}), 21, 22, 23)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("nested postgresql "+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertNestedFacts(t, newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil), 21, 22, 23)
		})
	}

	t.Run("batch sqlite", func(t *testing.T) {
		assertBatchFactOrdinals(t, newMutationResultFixture(t))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("batch postgresql "+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture, _ := newMutationResultPostgresFixture(t, context.Background(), profile)
			assertBatchFactOrdinals(t, fixture)
		})
	}
}

func assertNestedFacts(t testing.TB, fixture graphMutationFixture, user, post, comment byte) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, fixture.deepCreate(user, post, comment)); err != nil {
		t.Fatal(err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 1, 1, 3)
	assertNestedFactOrder(t, fixture, 3)
}

func assertBatchFactOrdinals(t testing.TB, batch mutationResultFixture) {
	t.Helper()
	for _, id := range []byte{24, 25} {
		if _, err := SystemCreate(context.Background(), batch.app.System(), batch.postDescriptor, batch.createPost(id, golem.UUID{15: 1}, "ordinal-batch")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := batch.app.database.Exec(`DELETE FROM ` + nestedAcceptanceOutbox(batch.app)); err != nil {
		t.Fatal(err)
	}
	if count, err := SystemUpdateMany(context.Background(), batch.app.System(), batch.postDescriptor, batch.title.Eq("ordinal-batch"), batch.updateManyTitle("ordinal-updated")); err != nil || count != 2 {
		t.Fatalf("batch count=%d err=%v", count, err)
	}
	var ordinals []int64
	if err := batch.app.database.Select(&ordinals, `SELECT "transaction_ordinal" FROM `+nestedAcceptanceOutbox(batch.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(ordinals) != 2 || ordinals[0] != 1 || ordinals[1] != 2 {
		t.Fatalf("batch transaction ordinals=%v", ordinals)
	}
}

func newGraphMutationFixture(t testing.TB, schema schematest.GraphFixture, deniedCreate golem.ModelID) graphMutationFixture {
	return newGraphMutationFixtureWithHooks(t, schema, deniedCreate, nil)
}

func newGraphMutationFixtureWithHooks(t testing.TB, schema schematest.GraphFixture, deniedCreate golem.ModelID, hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
	t.Helper()
	ctx := context.Background()
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "nested-graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	return openGraphMutationFixtureWithHooks(t, database, golem.SQLite, schema, deniedCreate, hooks)
}

func newPostgresGraphMutationFixtureWithHooks(t testing.TB, profile postgresAcceptanceProfile, deniedCreate golem.ModelID, hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_graph_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_graph_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schemaFixture := schematest.NewSubscribedGraphPostgreSQLNamespaces(t, namespace, systemNamespace)
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
	return openGraphMutationFixtureWithHooks(t, database, golem.PostgreSQL, schemaFixture, deniedCreate, hooks)
}

func openGraphMutationFixtureWithHooks(t testing.TB, database *sqlx.DB, provider golem.Provider, schema schematest.GraphFixture, deniedCreate golem.ModelID, hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
	t.Helper()
	ctx := context.Background()
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	commentIdentity := golem.GeneratedIdentityMetadata(schema.Comment, schema.CommentKey, golem.PrimaryIdentity, schema.CommentID)
	userPosts := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postAuthor := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	postComments := golem.GeneratedRelationMetadata(schema.Post, schema.Comment, schema.PostComments, schema.Commenting, golem.RelationInverse, golem.RelationToMany)
	commentPost := golem.GeneratedRelationMetadata(schema.Comment, schema.Post, schema.CommentPost, schema.Commenting, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[graphMutationUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userPosts},
	))
	postScalars := []golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle}
	if schema.PostUpdatedAt != (golem.FieldID{}) {
		postScalars = append(postScalars, schema.PostUpdatedAt)
	}
	postDescriptor := golem.GeneratedModelDescriptor[graphMutationPost](schema.Post, golem.GeneratedDescriptorShape(
		postScalars, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postAuthor, postComments},
	))
	commentDescriptor := golem.GeneratedModelDescriptor[graphMutationComment](schema.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.CommentID, schema.CommentPostID, schema.CommentBody}, nil, []golem.IdentityMetadata{commentIdentity}, []golem.RelationMetadata{commentPost},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata(), commentDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userPolicy := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationUser](schema.User, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationUser]()
		rules.CanRead(golem.All[graphMutationUser]())
		if deniedCreate != schema.User {
			rules.CanCreate(golem.All[graphMutationUser]())
		}
		rules.CanUpdate(golem.All[graphMutationUser]())
		rules.CanDelete(golem.All[graphMutationUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationPost](schema.Post, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationPost]()
		rules.CanRead(golem.All[graphMutationPost]())
		if deniedCreate != schema.Post {
			rules.CanCreate(golem.All[graphMutationPost]())
		}
		rules.CanUpdate(golem.All[graphMutationPost]())
		rules.CanDelete(golem.All[graphMutationPost]())
		return rules.Freeze(schema.Post)
	})
	commentPolicy := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationComment](schema.Comment, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationComment]()
		rules.CanRead(golem.All[graphMutationComment]())
		if deniedCreate != schema.Comment {
			rules.CanCreate(golem.All[graphMutationComment]())
		}
		rules.CanUpdate(golem.All[graphMutationComment]())
		rules.CanDelete(golem.All[graphMutationComment]())
		return rules.Freeze(schema.Comment)
	})
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[graphMutationActor]{userPolicy, postPolicy, commentPolicy}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[graphMutationPrincipal, graphMutationActor]{
		DB: database, Provider: provider, Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return graphMutationFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor, commentDescriptor: commentDescriptor,
		userID: golem.GeneratedEqualField[graphMutationUser, golem.UUID](schema.UserID), userName: golem.GeneratedTextField[graphMutationUser, string](schema.UserName),
		postID: golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.PostID), postTitle: golem.GeneratedTextField[graphMutationPost, string](schema.PostTitle),
		commentID: golem.GeneratedEqualField[graphMutationComment, golem.UUID](schema.CommentID), commentBody: golem.GeneratedTextField[graphMutationComment, string](schema.CommentBody),
	}
}

func (fixture graphMutationFixture) deepCreate(user, post, comment byte) golem.CreateInput[graphMutationUser] {
	commentInput := golem.GeneratedCreateInput[graphMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: comment}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "nested-comment"),
	)
	postInput := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: post}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "nested-post"),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, commentInput),
	)
	return golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: user}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "nested-user"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postInput),
	)
}

func assertGraphMutationRowsAndFacts(t testing.TB, fixture graphMutationFixture, users, posts, comments, facts int) {
	t.Helper()
	for table, want := range map[string]int{
		nestedAcceptanceTable(fixture.app, fixture.schema.User):    users,
		nestedAcceptanceTable(fixture.app, fixture.schema.Post):    posts,
		nestedAcceptanceTable(fixture.app, fixture.schema.Comment): comments,
		nestedAcceptanceOutbox(fixture.app):                        facts,
	} {
		var got int
		if err := fixture.app.database.Get(&got, `SELECT COUNT(*) FROM `+table); err != nil || got != want {
			t.Fatalf("%s rows=%d want=%d err=%v", table, got, want, err)
		}
	}
}

func assertNestedFactOrder(t testing.TB, fixture graphMutationFixture, want int) {
	t.Helper()
	type factRow struct {
		Causation string `db:"causation_id"`
		Ordinal   int64  `db:"transaction_ordinal"`
	}
	var rows []factRow
	if err := fixture.app.database.Select(&rows, `SELECT "causation_id", "transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != want {
		t.Fatalf("nested fact rows=%d want=%d", len(rows), want)
	}
	for index, row := range rows {
		if row.Ordinal != int64(index+1) || row.Causation == "" || index > 0 && row.Causation != rows[0].Causation {
			t.Fatalf("nested fact %d causation=%q ordinal=%d", index, row.Causation, row.Ordinal)
		}
	}
}

func assertNestedCompositeRelation(t testing.TB, fixture compositeMutationFixture) {
	t.Helper()
	item := golem.GeneratedCreateInput[compositeMutationItem](fixture.schema.Item,
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemRegion, golem.UUID{15: 11}),
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemID, golem.UUID{15: 12}),
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerRegion, golem.UUID{15: 91}),
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerID, golem.UUID{15: 92}),
	)
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.itemDescriptor, item); err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[compositeMutationItem](fixture.schema.Item, fixture.schema.ItemKey,
		golem.GeneratedSelectorComponent(fixture.schema.ItemRegion, golem.UUID{15: 11}),
		golem.GeneratedSelectorComponent(fixture.schema.ItemID, golem.UUID{15: 12}),
	)
	tenant := golem.GeneratedCreateInput[compositeMutationTenant](fixture.schema.Tenant,
		golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, golem.UUID{15: 21}),
		golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, golem.UUID{15: 22}),
		golem.GeneratedNestedConnect[compositeMutationTenant, compositeMutationItem](fixture.schema.Tenant, fixture.schema.TenantItems, fixture.schema.Ownership, fixture.schema.Item, selector),
	)
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.tenantDescriptor, tenant); err != nil {
		t.Fatal(err)
	}
	var ownerRegion, ownerID string
	table := nestedAcceptanceTable(fixture.app, fixture.schema.Item)
	query := fixture.app.database.Rebind(`SELECT "owner_region", "owner_id" FROM ` + table + ` WHERE "region" = ? AND "id" = ?`)
	if err := fixture.app.database.QueryRowx(query, mutationResultUUIDText(11), mutationResultUUIDText(12)).Scan(&ownerRegion, &ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerRegion != mutationResultUUIDText(21) || ownerID != mutationResultUUIDText(22) {
		t.Fatalf("composite owner=(%q,%q)", ownerRegion, ownerID)
	}
	createdOwner := golem.GeneratedCreateInput[compositeMutationTenant](fixture.schema.Tenant,
		golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, golem.UUID{15: 31}),
		golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, golem.UUID{15: 32}),
	)
	ownedItem := golem.GeneratedCreateInput[compositeMutationItem](fixture.schema.Item,
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemRegion, golem.UUID{15: 41}),
		golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemID, golem.UUID{15: 42}),
		golem.GeneratedNestedCreate[compositeMutationItem, compositeMutationTenant](fixture.schema.Item, fixture.schema.ItemOwner, fixture.schema.Ownership, fixture.schema.Tenant, createdOwner),
	)
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(context.Background(), caller, fixture.itemDescriptor, ownedItem); err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("composite source dependency failure: %T: %v", cause, cause)
		}
		t.Fatal(err)
	}
	if err := fixture.app.database.QueryRowx(query, mutationResultUUIDText(41), mutationResultUUIDText(42)).Scan(&ownerRegion, &ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerRegion != mutationResultUUIDText(31) || ownerID != mutationResultUUIDText(32) {
		t.Fatalf("composite source dependency owner=(%q,%q)", ownerRegion, ownerID)
	}
}

func assertRecursiveComments(t testing.TB, fixture recursiveMutationFixture) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.descriptor, fixture.deepCreate(41, 42, 43)); err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("recursive nested failure: %T: %v", cause, cause)
		}
		t.Fatal(err)
	}
	type commentRow struct {
		ID       string  `db:"id"`
		ParentID *string `db:"parent_id"`
	}
	var rows []commentRow
	if err := fixture.app.database.Select(&rows, `SELECT "id", "parent_id" FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != mutationResultUUIDText(41) || rows[0].ParentID != nil || rows[1].ParentID == nil || *rows[1].ParentID != rows[0].ID || rows[2].ParentID == nil || *rows[2].ParentID != rows[1].ID {
		t.Fatalf("recursive rows=%#v", rows)
	}
	assertRecursiveFacts(t, fixture, 3)
	if _, err := fixture.app.database.ExecContext(context.Background(), `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	rootTarget := golem.GeneratedUniqueSelectorValue[recursiveMutationComment](fixture.schema.Comment, fixture.schema.CommentKey,
		golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: 41}))
	childTarget := golem.GeneratedUniqueSelectorValue[recursiveMutationComment](fixture.schema.Comment, fixture.schema.CommentKey,
		golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: 42}))
	disconnect := golem.GeneratedUpdateInput[recursiveMutationComment](fixture.schema.Comment,
		golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.body, "root-disconnected"),
		golem.GeneratedNestedDisconnect[recursiveMutationComment, recursiveMutationComment](fixture.schema.Comment, fixture.schema.Replies, fixture.schema.Threading, fixture.schema.Comment, childTarget),
	)
	if _, err := CallerUpdate(context.Background(), caller, fixture.descriptor, rootTarget, disconnect); err != nil {
		t.Fatalf("optional recursive disconnect: %v", err)
	}
	rows = nil
	if err := fixture.app.database.Select(&rows, `SELECT "id", "parent_id" FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1].ParentID != nil || rows[2].ParentID == nil || *rows[2].ParentID != rows[1].ID {
		t.Fatalf("optional disconnect rows=%#v", rows)
	}
	assertRecursiveFacts(t, fixture, 2)
}

func nestedAcceptanceTable[P, A any](app *App[P, A], model golem.ModelID) string {
	provider := golem.SQLite
	if app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	physicalModel, _ := app.registry.PhysicalModel(provider, model)
	if provider == golem.SQLite {
		return `"` + string(physicalModel.Name()) + `"`
	}
	namespace, _ := app.registry.PhysicalNamespace(provider)
	return `"` + string(namespace) + `"."` + string(physicalModel.Name()) + `"`
}

func nestedAcceptanceOutbox[P, A any](app *App[P, A]) string {
	if app.provider != policyir.ProviderPostgreSQL {
		return `"_golem_outbox"`
	}
	namespace, _ := app.registry.PhysicalSystemNamespace(golem.PostgreSQL)
	return `"` + string(namespace) + `"."_golem_outbox"`
}

func newRecursiveMutationFixture(t testing.TB) recursiveMutationFixture {
	t.Helper()
	ctx := context.Background()
	schemaFixture := schematest.NewSubscribedRecursiveComment(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "recursive-comments.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
		t.Fatal(err)
	}
	return openRecursiveMutationFixture(t, database, golem.SQLite, schemaFixture)
}

func newPostgresRecursiveMutationFixture(t testing.TB, profile postgresAcceptanceProfile) recursiveMutationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_recursive_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_recursive_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schemaFixture := schematest.NewSubscribedRecursiveCommentPostgreSQLNamespaces(t, namespace, systemNamespace)
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
	return openRecursiveMutationFixture(t, database, golem.PostgreSQL, schemaFixture)
}

func openRecursiveMutationFixture(t testing.TB, database *sqlx.DB, provider golem.Provider, schemaFixture schematest.RecursiveCommentFixture) recursiveMutationFixture {
	t.Helper()
	ctx := context.Background()
	identity := golem.GeneratedIdentityMetadata(schemaFixture.Comment, schemaFixture.CommentKey, golem.PrimaryIdentity, schemaFixture.CommentID)
	parent := golem.GeneratedRelationMetadata(schemaFixture.Comment, schemaFixture.Comment, schemaFixture.Parent, schemaFixture.Threading, golem.RelationSource, golem.RelationToOne)
	replies := golem.GeneratedRelationMetadata(schemaFixture.Comment, schemaFixture.Comment, schemaFixture.Replies, schemaFixture.Threading, golem.RelationInverse, golem.RelationToMany)
	descriptor := golem.GeneratedModelDescriptor[recursiveMutationComment](schemaFixture.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.CommentID, schemaFixture.ParentID, schemaFixture.Body}, nil, []golem.IdentityMetadata{identity}, []golem.RelationMetadata{parent, replies},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), descriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	policy := golem.GeneratedPolicyBinding[graphMutationActor, recursiveMutationComment](schemaFixture.Comment, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[recursiveMutationComment]()
		rules.CanRead(golem.All[recursiveMutationComment]())
		rules.CanCreate(golem.All[recursiveMutationComment]())
		rules.CanUpdate(golem.All[recursiveMutationComment]())
		rules.CanDelete(golem.All[recursiveMutationComment]())
		return rules.Freeze(schemaFixture.Comment)
	})
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[graphMutationActor]{policy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[graphMutationPrincipal, graphMutationActor]{
		DB: database, Provider: provider, Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return recursiveMutationFixture{
		app: app, schema: schemaFixture, descriptor: descriptor,
		id: golem.GeneratedEqualField[recursiveMutationComment, golem.UUID](schemaFixture.CommentID), body: golem.GeneratedTextField[recursiveMutationComment, string](schemaFixture.Body),
	}
}

func (fixture recursiveMutationFixture) deepCreate(root, child, grandchild byte) golem.CreateInput[recursiveMutationComment] {
	grandchildInput := golem.GeneratedCreateInput[recursiveMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.id, golem.UUID{15: grandchild}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.body, "grandchild"),
	)
	childInput := golem.GeneratedCreateInput[recursiveMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.id, golem.UUID{15: child}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.body, "child"),
		golem.GeneratedNestedCreate[recursiveMutationComment, recursiveMutationComment](fixture.schema.Comment, fixture.schema.Replies, fixture.schema.Threading, fixture.schema.Comment, grandchildInput),
	)
	return golem.GeneratedCreateInput[recursiveMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.id, golem.UUID{15: root}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.body, "root"),
		golem.GeneratedNestedCreate[recursiveMutationComment, recursiveMutationComment](fixture.schema.Comment, fixture.schema.Replies, fixture.schema.Threading, fixture.schema.Comment, childInput),
	)
}

func assertRecursiveFacts(t testing.TB, fixture recursiveMutationFixture, want int) {
	t.Helper()
	type factRow struct {
		ModelID string `db:"model_id"`
		Ordinal int64  `db:"transaction_ordinal"`
	}
	var rows []factRow
	if err := fixture.app.database.Select(&rows, `SELECT "model_id", "transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	wantModel := hex.EncodeToString(fixture.schema.Comment[:])
	if len(rows) != want {
		t.Fatalf("recursive facts=%d want=%d", len(rows), want)
	}
	for index, row := range rows {
		if row.ModelID != wantModel || row.Ordinal != int64(index+1) {
			t.Fatalf("recursive fact[%d]=%#v", index, row)
		}
	}
}

func newCompositeMutationFixture(t testing.TB) compositeMutationFixture {
	t.Helper()
	ctx := context.Background()
	schemaFixture := schematest.NewCompositeRelation(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "composite-nested.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
		t.Fatal(err)
	}
	return openCompositeMutationFixture(t, database, golem.SQLite, schemaFixture)
}

func newPostgresCompositeMutationFixture(t testing.TB, profile postgresAcceptanceProfile) compositeMutationFixture {
	t.Helper()
	ctx := context.Background()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_composite_%s_%d_%d", profile.name, os.Getpid(), time.Now().UnixNano()))
	schemaFixture := schematest.NewCompositeRelationPostgreSQLNamespace(t, namespace)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	return openCompositeMutationFixture(t, database, golem.PostgreSQL, schemaFixture)
}

func openCompositeMutationFixture(t testing.TB, database *sqlx.DB, provider golem.Provider, schemaFixture schematest.CompositeRelationFixture) compositeMutationFixture {
	t.Helper()
	ctx := context.Background()
	tenantIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Tenant, schemaFixture.TenantKey, golem.PrimaryIdentity, schemaFixture.TenantRegion, schemaFixture.TenantID)
	itemIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Item, schemaFixture.ItemKey, golem.PrimaryIdentity, schemaFixture.ItemRegion, schemaFixture.ItemID)
	tenantItems := golem.GeneratedRelationMetadata(schemaFixture.Tenant, schemaFixture.Item, schemaFixture.TenantItems, schemaFixture.Ownership, golem.RelationInverse, golem.RelationToMany)
	itemOwner := golem.GeneratedRelationMetadata(schemaFixture.Item, schemaFixture.Tenant, schemaFixture.ItemOwner, schemaFixture.Ownership, golem.RelationSource, golem.RelationToOne)
	tenantDescriptor := golem.GeneratedModelDescriptor[compositeMutationTenant](schemaFixture.Tenant, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.TenantRegion, schemaFixture.TenantID}, nil, []golem.IdentityMetadata{tenantIdentity}, []golem.RelationMetadata{tenantItems},
	))
	itemDescriptor := golem.GeneratedModelDescriptor[compositeMutationItem](schemaFixture.Item, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.ItemRegion, schemaFixture.ItemID, schemaFixture.OwnerRegion, schemaFixture.OwnerID}, nil, []golem.IdentityMetadata{itemIdentity}, []golem.RelationMetadata{itemOwner},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), tenantDescriptor.Metadata(), itemDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	tenantPolicy := allowCompositePolicy[compositeMutationTenant](schemaFixture.Tenant)
	itemPolicy := allowCompositePolicy[compositeMutationItem](schemaFixture.Item)
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[graphMutationActor]{tenantPolicy, itemPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[graphMutationPrincipal, graphMutationActor]{
		DB: database, Provider: provider, Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return compositeMutationFixture{
		app: app, schema: schemaFixture, tenantDescriptor: tenantDescriptor, itemDescriptor: itemDescriptor,
		tenantRegion: golem.GeneratedEqualField[compositeMutationTenant, golem.UUID](schemaFixture.TenantRegion), tenantID: golem.GeneratedEqualField[compositeMutationTenant, golem.UUID](schemaFixture.TenantID),
		itemRegion: golem.GeneratedEqualField[compositeMutationItem, golem.UUID](schemaFixture.ItemRegion), itemID: golem.GeneratedEqualField[compositeMutationItem, golem.UUID](schemaFixture.ItemID),
		ownerRegion: golem.GeneratedEqualField[compositeMutationItem, golem.UUID](schemaFixture.OwnerRegion), ownerID: golem.GeneratedEqualField[compositeMutationItem, golem.UUID](schemaFixture.OwnerID),
	}
}

func allowCompositePolicy[M any](model golem.ModelID) golem.PolicyBinding[graphMutationActor] {
	return golem.GeneratedPolicyBinding[graphMutationActor, M](model, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[M]()
		rules.CanRead(golem.All[M]())
		rules.CanCreate(golem.All[M]())
		rules.CanUpdate(golem.All[M]())
		rules.CanDelete(golem.All[M]())
		return rules.Freeze(model)
	})
}
