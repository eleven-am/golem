package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type findUniqueHookWhereKey struct{}

func TestFindOneBeforeHookAppendingWhereIsRefusedAsDuplicateOption(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "find-unique-hook-where.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "alice"); err != nil {
		t.Fatal(err)
	}
	users := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil,
	))
	posts := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil,
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	name := golem.GeneratedTextField[testUser, string](fixture.UserName)
	allowUser := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	allowPost := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	appendWhere := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindOneHookRequest[testUser]](fixture.User, golem.HookFindOne, func(hookCtx context.Context, request *golem.FindOneHookRequest[testUser]) error {
		if hookCtx.Value(findUniqueHookWhereKey{}) != nil {
			request.AppendOptions(golem.Where(name.Eq("alice")))
		}
		return nil
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, []golem.HookBinding[testActor]{appendWhere}))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	aliceID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	alice := golem.GeneratedUniqueSelectorValue[testUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, aliceID))
	if _, err := CallerFindUnique(ctx, caller, users, alice, golem.Select[testUser](name)); err != nil {
		t.Fatalf("findUnique without an appended where: %v", err)
	}
	_, err = CallerFindUnique(context.WithValue(ctx, findUniqueHookWhereKey{}, true), caller, users, alice, golem.Select[testUser](name))
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || !strings.Contains(err.Error(), "option kind 1 appears more than once") {
		t.Fatalf("findUnique with a hook-appended where err=%v; want BAD_USER_INPUT duplicate where option", err)
	}
}
