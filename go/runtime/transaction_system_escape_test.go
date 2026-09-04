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
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

type systemEscapePrincipal struct{}
type systemEscapeActor struct{}
type systemEscapeUser struct{}
type systemEscapePost struct{}

type systemEscapeFixture struct {
	app            *App[systemEscapePrincipal, systemEscapeActor]
	database       *sqlx.DB
	schema         schematest.Fixture
	userDescriptor golem.ModelDescriptor[systemEscapeUser]
	postDescriptor golem.ModelDescriptor[systemEscapePost]
	userID         golem.EqualField[systemEscapeUser, golem.UUID]
	userName       golem.TextField[systemEscapeUser, string]
	postID         golem.EqualField[systemEscapePost, golem.UUID]
	authorID       golem.EqualField[systemEscapePost, golem.UUID]
	postTitle      golem.TextField[systemEscapePost, string]
}

func openSystemEscapeFixture(t *testing.T) systemEscapeFixture {
	t.Helper()
	ctx := context.Background()
	schema := schematest.New(t)
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "system-escape.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, systemEscapeUUIDText(1), "seed"); err != nil {
		t.Fatal(err)
	}

	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[systemEscapeUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[systemEscapePost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	writableUsers := golem.GeneratedPolicyBinding[systemEscapeActor, systemEscapeUser](schema.User, func(systemEscapeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[systemEscapeUser]()
		rules.CanRead(golem.All[systemEscapeUser]())
		rules.CanCreate(golem.All[systemEscapeUser]())
		return rules.Freeze(schema.User)
	})
	readOnlyPosts := golem.GeneratedPolicyBinding[systemEscapeActor, systemEscapePost](schema.Post, func(systemEscapeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[systemEscapePost]()
		rules.CanRead(golem.All[systemEscapePost]())
		return rules.Freeze(schema.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[systemEscapeActor]{writableUsers, readOnlyPosts}, nil)
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[systemEscapePrincipal, systemEscapeActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, systemEscapePrincipal) (systemEscapeActor, error) {
			return systemEscapeActor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return systemEscapeFixture{
		app: app, database: database, schema: schema,
		userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID:    golem.GeneratedEqualField[systemEscapeUser, golem.UUID](schema.UserID),
		userName:  golem.GeneratedTextField[systemEscapeUser, string](schema.UserName),
		postID:    golem.GeneratedEqualField[systemEscapePost, golem.UUID](schema.PostID),
		authorID:  golem.GeneratedEqualField[systemEscapePost, golem.UUID](schema.AuthorID),
		postTitle: golem.GeneratedTextField[systemEscapePost, string](schema.PostTitle),
	}
}

func (fixture systemEscapeFixture) caller(t *testing.T) *Caller[systemEscapePrincipal, systemEscapeActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), systemEscapePrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func (fixture systemEscapeFixture) createUser(id byte, name string) golem.CreateInput[systemEscapeUser] {
	return golem.GeneratedCreateInput[systemEscapeUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, name),
	)
}

func (fixture systemEscapeFixture) createPost(id byte, title string) golem.CreateInput[systemEscapePost] {
	return golem.GeneratedCreateInput[systemEscapePost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title),
	)
}

func systemEscapeUUIDText(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", last)
}

func (fixture systemEscapeFixture) countRows(t *testing.T, table, id string) int {
	t.Helper()
	var count int
	if err := fixture.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM "`+table+`" WHERE "id" = ?`, id); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCallerTransactionSystemEscapeCommitsWithTheCallerWrite(t *testing.T) {
	fixture := openSystemEscapeFixture(t)
	ctx := context.Background()
	err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		if _, createErr := CallerTxCreate(ctx, transaction, fixture.userDescriptor, fixture.createUser(41, "authorized")); createErr != nil {
			return createErr
		}
		escape := CallerTxSystem(transaction)
		if escape.system.executor != transaction.caller.executor {
			t.Fatal("system escape is not bound to the caller transaction execution")
		}
		if escape.system.app != transaction.caller.app {
			t.Fatal("system escape is not bound to the caller application")
		}
		if escape.execution != transaction.caller.execution {
			t.Fatalf("system escape execution=%d caller execution=%d", escape.execution, transaction.caller.execution)
		}
		_, createErr := SystemTxCreate(ctx, escape, fixture.postDescriptor, fixture.createPost(41, "unauthorized"))
		return createErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fixture.countRows(t, "users", systemEscapeUUIDText(41)); got != 1 {
		t.Fatalf("committed caller users=%d want 1", got)
	}
	if got := fixture.countRows(t, "posts", systemEscapeUUIDText(41)); got != 1 {
		t.Fatalf("committed escape posts=%d want 1", got)
	}
}

func TestCallerTransactionSystemEscapeRollsBackWithTheCallerWrite(t *testing.T) {
	fixture := openSystemEscapeFixture(t)
	ctx := context.Background()
	sentinel := errors.New("abort transaction")
	err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		if _, createErr := CallerTxCreate(ctx, transaction, fixture.userDescriptor, fixture.createUser(42, "authorized")); createErr != nil {
			return createErr
		}
		if _, createErr := SystemTxCreate(ctx, CallerTxSystem(transaction), fixture.postDescriptor, fixture.createPost(42, "unauthorized")); createErr != nil {
			return createErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback err=%v want %v", err, sentinel)
	}
	if got := fixture.countRows(t, "users", systemEscapeUUIDText(42)); got != 0 {
		t.Fatalf("rolled-back caller users=%d want 0", got)
	}
	if got := fixture.countRows(t, "posts", systemEscapeUUIDText(42)); got != 0 {
		t.Fatalf("rolled-back escape posts=%d want 0", got)
	}

	panicValue := struct{ message string }{"panic transaction"}
	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("recovered=%#v", recovered)
			}
		}()
		_ = CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
			if _, createErr := SystemTxCreate(ctx, CallerTxSystem(transaction), fixture.postDescriptor, fixture.createPost(43, "panicked")); createErr != nil {
				t.Fatal(createErr)
			}
			panic(panicValue)
		})
	}()
	if got := fixture.countRows(t, "posts", systemEscapeUUIDText(43)); got != 0 {
		t.Fatalf("panicked escape posts=%d want 0", got)
	}
}

func TestCallerTransactionSystemEscapeWritesWhatThePolicyRefusesTheCaller(t *testing.T) {
	fixture := openSystemEscapeFixture(t)
	ctx := context.Background()
	err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		_, refusal := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(44, "refused"))
		var failure *golem.Error
		if !errors.As(refusal, &failure) || failure.Code != golem.CodeForbidden {
			t.Fatalf("caller post create refusal=%v", refusal)
		}
		_, createErr := SystemTxCreate(ctx, CallerTxSystem(transaction), fixture.postDescriptor, fixture.createPost(44, "escaped"))
		return createErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fixture.countRows(t, "posts", systemEscapeUUIDText(44)); got != 1 {
		t.Fatalf("escaped posts=%d want 1", got)
	}
}

func TestCallerTransactionSystemEscapeIsObserved(t *testing.T) {
	fixture := openSystemEscapeFixture(t)
	collector := &p8ObservationCollector{}
	fixture.app.observer = collector
	ctx := context.Background()
	if err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		_, createErr := CallerTxCreate(ctx, transaction, fixture.userDescriptor, fixture.createUser(45, "authorized"))
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	if records := collector.matching(observe.KindTransaction, observe.OperationSystemEscape); len(records) != 0 {
		t.Fatalf("authorized transaction emitted %d escape observations", len(records))
	}
	if err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		_, createErr := SystemTxCreate(ctx, CallerTxSystem(transaction), fixture.postDescriptor, fixture.createPost(46, "escaped"))
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	records := collector.matching(observe.KindTransaction, observe.OperationSystemEscape)
	if len(records) != 1 {
		t.Fatalf("escape observations=%d want 1", len(records))
	}
	if records[0].phase != observe.PhaseOpen || records[0].outcome != observe.OutcomeSuccess || records[0].reason != observe.ReasonNone {
		t.Fatalf("escape observation=%#v", records[0])
	}
	if records[0].provider != golem.SQLite {
		t.Fatalf("escape observation provider=%v", records[0].provider)
	}
}

func TestCallerTransactionSystemEscapeExpiresWithTheTransaction(t *testing.T) {
	fixture := openSystemEscapeFixture(t)
	ctx := context.Background()
	var escaped *SystemTx[systemEscapePrincipal, systemEscapeActor]
	if err := CallerTransaction(ctx, fixture.caller(t), func(transaction *CallerTx[systemEscapePrincipal, systemEscapeActor]) error {
		escaped = CallerTxSystem(transaction)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemTxCreate(ctx, escaped, fixture.postDescriptor, fixture.createPost(47, "expired")); err == nil {
		t.Fatal("escaped system transaction remained usable after commit")
	}
	if got := fixture.countRows(t, "posts", systemEscapeUUIDText(47)); got != 0 {
		t.Fatalf("expired escape posts=%d want 0", got)
	}
	if CallerTxSystem[systemEscapePrincipal, systemEscapeActor](nil) != nil {
		t.Fatal("nil caller transaction produced a system stance")
	}
}
