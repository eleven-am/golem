package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type transactionQuerySpy struct {
	sqlx.ExtContext
	queries atomic.Int64
}

func (spy *transactionQuerySpy) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	spy.queries.Add(1)
	return spy.ExtContext.QueryContext(ctx, query, args...)
}

func (spy *transactionQuerySpy) QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error) {
	spy.queries.Add(1)
	return spy.ExtContext.QueryxContext(ctx, query, args...)
}

func (spy *transactionQuerySpy) QueryRowxContext(ctx context.Context, query string, args ...any) *sqlx.Row {
	spy.queries.Add(1)
	return spy.ExtContext.QueryRowxContext(ctx, query, args...)
}

type transactionFixture struct {
	database       *sqlx.DB
	app            *App[testPrincipal, testActor]
	userDescriptor golem.ModelDescriptor[testUser]
	postDescriptor golem.ModelDescriptor[testPost]
	userID         golem.EqualField[testUser, golem.UUID]
	userName       golem.TextField[testUser, string]
	postTitle      golem.TextField[testPost, string]
	posts          golem.ToMany[testUser, testPost]
}

func openTransactionFixture(t *testing.T) transactionFixture {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "transactions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userPolicy := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(actor testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		if actor.Allow {
			rules.CanRead(golem.All[testUser]())
		} else {
			rules.CanRead(golem.None[testUser]())
		}
		return rules.Freeze(fixture.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(actor testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		if actor.Allow {
			rules.CanRead(golem.All[testPost]())
		} else {
			rules.CanRead(golem.None[testPost]())
		}
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userPolicy, postPolicy}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal testPrincipal) (testActor, error) {
			return testActor{Allow: principal.Allow}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return transactionFixture{
		database:       database,
		app:            app,
		userDescriptor: userDescriptor,
		postDescriptor: postDescriptor,
		userID:         golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID),
		userName:       golem.GeneratedTextField[testUser, string](fixture.UserName),
		postTitle:      golem.GeneratedTextField[testPost, string](fixture.PostTitle),
		posts:          golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
	}
}

func TestCallerTransactionReadsUncommittedRootCountAndRelationsWithoutConnectionEscape(t *testing.T) {
	fixture := openTransactionFixture(t)
	// With one connection, any accidental App.database query from inside the
	// callback blocks behind the transaction until the context deadline. Root,
	// count, and relation-loader success therefore prove Tx executor use.
	fixture.database.SetMaxOpenConns(1)
	fixture.database.SetMaxIdleConns(1)
	caller, err := fixture.app.ForPrincipal(context.Background(), testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		if _, execErr := transaction.caller.executor.transaction.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "alice"); execErr != nil {
			return execErr
		}
		if _, execErr := transaction.caller.executor.transaction.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, "00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "draft"); execErr != nil {
			return execErr
		}
		spy := &transactionQuerySpy{ExtContext: transaction.caller.executor.transaction}
		transaction.caller.executor.executor = spy
		readContext := context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)
		count, countErr := CallerTxCount(readContext, transaction, fixture.userDescriptor)
		if countErr != nil || count != 1 {
			t.Fatalf("transaction count=%d err=%v", count, countErr)
		}
		rows, readErr := CallerTxFindMany(readContext, transaction, fixture.userDescriptor,
			golem.Select[testUser](fixture.userID, fixture.userName, fixture.posts.Select(fixture.postTitle)),
		)
		if readErr != nil {
			return readErr
		}
		if len(rows) != 1 {
			t.Fatalf("transaction users=%d", len(rows))
		}
		children, present := golem.Many(rows[0], fixture.posts).Get()
		if !present || len(children) != 1 {
			t.Fatalf("transaction posts=%d present=%t", len(children), present)
		}
		if title, selected := golem.Value(children[0], fixture.postTitle).Get(); !selected || title != "draft" {
			t.Fatalf("transaction title=%q selected=%t", title, selected)
		}
		if spy.queries.Load() < 3 {
			t.Fatalf("transaction queryer observed %d statements; want root, count, and relation-loader queries", spy.queries.Load())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := SystemCount(context.Background(), fixture.app.System(), fixture.userDescriptor)
	if err != nil || count != 1 {
		t.Fatalf("committed count=%d err=%v", count, err)
	}
}

func TestCallerTransactionRollbackCancellationPanicAndNoReplay(t *testing.T) {
	fixture := openTransactionFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int64
	sentinel := errors.New("abort transaction")
	err = CallerTransaction(context.Background(), caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		callbacks.Add(1)
		_, execErr := transaction.caller.executor.transaction.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "rolled-back")
		if execErr != nil {
			return execErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || callbacks.Load() != 1 {
		t.Fatalf("rollback err=%v callbacks=%d", err, callbacks.Load())
	}
	assertSystemUserCount(t, fixture, 0)

	canceledContext, cancel := context.WithCancel(context.Background())
	err = CallerTransaction(canceledContext, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		if _, execErr := transaction.caller.executor.transaction.ExecContext(canceledContext, `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000002", "canceled"); execErr != nil {
			return execErr
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v", err)
	}
	assertSystemUserCount(t, fixture, 0)

	panicValue := struct{ message string }{"panic transaction"}
	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("recovered=%#v", recovered)
			}
		}()
		_ = CallerTransaction(context.Background(), caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
			if _, execErr := transaction.caller.executor.transaction.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000003", "panicked"); execErr != nil {
				t.Fatal(execErr)
			}
			panic(panicValue)
		})
	}()
	assertSystemUserCount(t, fixture, 0)
}

func TestTransactionCapabilitiesExpireAndKeepCallerSystemAuthorizationSeparate(t *testing.T) {
	fixture := openTransactionFixture(t)
	if reflect.TypeOf((*CallerTx[testPrincipal, testActor])(nil)).NumMethod() != 0 || reflect.TypeOf(CallerTx[testPrincipal, testActor]{}).NumMethod() != 0 {
		t.Fatal("CallerTx must not expose DB, System, or Transaction methods")
	}
	if reflect.TypeOf((*SystemTx[testPrincipal, testActor])(nil)).NumMethod() != 0 || reflect.TypeOf(SystemTx[testPrincipal, testActor]{}).NumMethod() != 0 {
		t.Fatal("SystemTx must not expose DB, System, or Transaction methods")
	}
	denied, err := fixture.app.ForPrincipal(context.Background(), testPrincipal{Allow: false})
	if err != nil {
		t.Fatal(err)
	}
	var escaped *CallerTx[testPrincipal, testActor]
	err = CallerTransaction(context.Background(), denied, func(transaction *CallerTx[testPrincipal, testActor]) error {
		escaped = transaction
		count, countErr := CallerTxCount(context.Background(), transaction, fixture.userDescriptor)
		var failure *golem.Error
		if count != 0 || !errors.As(countErr, &failure) || failure.Code != golem.CodeForbidden {
			t.Fatalf("denied caller count=%d err=%v", count, countErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerTxCount(context.Background(), escaped, fixture.userDescriptor); err == nil {
		t.Fatal("escaped caller transaction remained usable after commit")
	}

	err = SystemTransaction(context.Background(), fixture.app.System(), func(transaction *SystemTx[testPrincipal, testActor]) error {
		if _, execErr := transaction.system.executor.transaction.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000004", "system"); execErr != nil {
			return execErr
		}
		count, countErr := SystemTxCount(context.Background(), transaction, fixture.userDescriptor)
		if countErr != nil || count != 1 {
			t.Fatalf("system count=%d err=%v", count, countErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSystemUserCount(t, fixture, 1)
}

func assertSystemUserCount(t *testing.T, fixture transactionFixture, expected int64) {
	t.Helper()
	count, err := SystemCount(context.Background(), fixture.app.System(), fixture.userDescriptor)
	if err != nil || count != expected {
		t.Fatalf("system count=%d want=%d err=%v", count, expected, err)
	}
}
