package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type hookControlKey uint8

const (
	hookVetoBefore hookControlKey = iota + 1
	hookVetoAfter
)

func TestFindOneAndFindFirstHooksReplaceObserveVetoAndSystemBypasses(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "all-read-hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}

	users := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil,
	))
	posts := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil,
	))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
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
	bobID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	bobSelector := golem.GeneratedUniqueSelectorValue[testUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, bobID))

	var firstBefore, firstAfter, oneBefore, oneAfter atomic.Int64
	firstBeforeHook := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindFirstHookRequest[testUser]](fixture.User, golem.HookFindFirst, func(hookCtx context.Context, request *golem.FindFirstHookRequest[testUser]) error {
		firstBefore.Add(1)
		if hookCtx.Value(hookVetoBefore) != nil {
			return errors.New("findFirst before veto")
		}
		request.ReplaceOptions(golem.Where(name.Eq("bob")), golem.Select[testUser](name))
		return nil
	})
	firstAfterHook := golem.GeneratedAfterHookBinding[testActor, testUser, golem.FindFirstHookResult[testUser]](fixture.User, golem.HookFindFirst, func(hookCtx context.Context, result golem.FindFirstHookResult[testUser]) error {
		firstAfter.Add(1)
		row, found := result.Row()
		value, present := golem.Value(row, name).Get()
		if !found || !present || value != "bob" {
			return errors.New("findFirst after observed the wrong detached result")
		}
		if hookCtx.Value(hookVetoAfter) != nil {
			return errors.New("findFirst after veto")
		}
		return nil
	})
	oneBeforeHook := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindOneHookRequest[testUser]](fixture.User, golem.HookFindOne, func(hookCtx context.Context, request *golem.FindOneHookRequest[testUser]) error {
		oneBefore.Add(1)
		if hookCtx.Value(hookVetoBefore) != nil {
			return errors.New("findOne before veto")
		}
		request.ReplaceSelector(bobSelector)
		request.ReplaceOptions(golem.Select[testUser](name))
		return nil
	})
	oneAfterHook := golem.GeneratedAfterHookBinding[testActor, testUser, golem.FindOneHookResult[testUser]](fixture.User, golem.HookFindOne, func(hookCtx context.Context, result golem.FindOneHookResult[testUser]) error {
		oneAfter.Add(1)
		value, present := golem.Value(result.Row(), name).Get()
		if !present || value != "bob" {
			return errors.New("findOne after observed the wrong detached result")
		}
		if hookCtx.Value(hookVetoAfter) != nil {
			return errors.New("findOne after veto")
		}
		return nil
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, []golem.HookBinding[testActor]{firstBeforeHook, firstAfterHook, oneBeforeHook, oneAfterHook})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
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
	aliceSelector := golem.GeneratedUniqueSelectorValue[testUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, aliceID))

	first, found, err := CallerFindFirst(ctx, caller, users, golem.Where(name.Eq("alice")), golem.Select[testUser](name))
	if err != nil || !found {
		t.Fatalf("findFirst found=%t err=%v", found, err)
	}
	if value, present := golem.Value(first, name).Get(); !present || value != "bob" {
		t.Fatalf("findFirst replacement value=%q present=%t", value, present)
	}
	one, err := CallerFindUnique(ctx, caller, users, aliceSelector, golem.Select[testUser](name))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := golem.Value(one, name).Get(); !present || value != "bob" {
		t.Fatalf("findOne replacement value=%q present=%t", value, present)
	}

	assertHookVeto := func(t *testing.T, err error, operation string) {
		t.Helper()
		var public *golem.Error
		if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || public.Operation != operation || public.Error() == "" {
			t.Fatalf("hook veto error=%#v", err)
		}
	}
	_, _, err = CallerFindFirst(context.WithValue(ctx, hookVetoBefore, true), caller, users, golem.Select[testUser](name))
	assertHookVeto(t, err, "findFirst")
	_, err = CallerFindUnique(context.WithValue(ctx, hookVetoBefore, true), caller, users, aliceSelector, golem.Select[testUser](name))
	assertHookVeto(t, err, "findUnique")
	_, _, err = CallerFindFirst(context.WithValue(ctx, hookVetoAfter, true), caller, users, golem.Select[testUser](name))
	assertHookVeto(t, err, "findFirst")
	_, err = CallerFindUnique(context.WithValue(ctx, hookVetoAfter, true), caller, users, aliceSelector, golem.Select[testUser](name))
	assertHookVeto(t, err, "findUnique")

	firstBeforeAtSystem, firstAfterAtSystem := firstBefore.Load(), firstAfter.Load()
	oneBeforeAtSystem, oneAfterAtSystem := oneBefore.Load(), oneAfter.Load()
	if _, found, err := SystemFindFirst(context.WithValue(ctx, hookVetoBefore, true), app.System(), users, golem.Select[testUser](name)); err != nil || !found {
		t.Fatalf("system findFirst found=%t err=%v", found, err)
	}
	if _, err := SystemFindUnique(context.WithValue(ctx, hookVetoBefore, true), app.System(), users, aliceSelector, golem.Select[testUser](name)); err != nil {
		t.Fatalf("system findUnique: %v", err)
	}
	if firstBefore.Load() != firstBeforeAtSystem || firstAfter.Load() != firstAfterAtSystem || oneBefore.Load() != oneBeforeAtSystem || oneAfter.Load() != oneAfterAtSystem {
		t.Fatal("system reads invoked caller hooks")
	}
}
