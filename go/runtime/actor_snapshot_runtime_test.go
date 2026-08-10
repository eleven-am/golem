package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type snapshotRuntimeActor struct {
	Subject string
	Roles   map[string]bool
}

func TestForPrincipalSnapshotsMutableActorOnceForPoliciesHooksAndConcurrentReads(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "actor-snapshot.db"))
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

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}

	var stable atomic.Pointer[snapshotRuntimeActor]
	policy := func(model golem.ModelID) golem.PolicyBinding[*snapshotRuntimeActor] {
		if model == fixture.User {
			return golem.GeneratedPolicyBinding[*snapshotRuntimeActor, testUser](model, func(actor *snapshotRuntimeActor) (golem.FrozenPolicy, error) {
				if previous := stable.Load(); previous == nil {
					stable.Store(actor)
				} else if previous != actor {
					return golem.FrozenPolicy{}, errors.New("policy factories received different actor snapshots")
				}
				rules := golem.NewRules[testUser]()
				if actor.Roles["reader"] {
					rules.CanRead(golem.All[testUser]())
				} else {
					rules.CanRead(golem.None[testUser]())
				}
				return rules.Freeze(fixture.User)
			})
		}
		return golem.GeneratedPolicyBinding[*snapshotRuntimeActor, testPost](model, func(actor *snapshotRuntimeActor) (golem.FrozenPolicy, error) {
			if stable.Load() != actor {
				return golem.FrozenPolicy{}, errors.New("policy factories received different actor snapshots")
			}
			rules := golem.NewRules[testPost]()
			rules.CanRead(golem.All[testPost]())
			return rules.Freeze(fixture.Post)
		})
	}
	var hookCalls atomic.Int64
	before := golem.GeneratedBeforeHookBinding[*snapshotRuntimeActor, testUser, golem.FindManyHookRequest[testUser]](fixture.User, golem.HookFindMany, func(hookCtx context.Context, _ *golem.FindManyHookRequest[testUser]) error {
		actor := golem.ActorFrom[*snapshotRuntimeActor](hookCtx)
		if actor == nil || actor != stable.Load() || !actor.Roles["reader"] {
			return errors.New("hook did not receive the stable actor snapshot")
		}
		hookCalls.Add(1)
		return nil
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[*snapshotRuntimeActor]{policy(fixture.User), policy(fixture.Post)}, []golem.HookBinding[*snapshotRuntimeActor]{before})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}

	var snapshots atomic.Int64
	app, err := Open(ctx, Config[*snapshotRuntimeActor, *snapshotRuntimeActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal *snapshotRuntimeActor) (*snapshotRuntimeActor, error) {
			return principal, nil
		},
		SnapshotActor: func(input *snapshotRuntimeActor) (*snapshotRuntimeActor, error) {
			snapshots.Add(1)
			roles := make(map[string]bool, len(input.Roles))
			for role, allowed := range input.Roles {
				roles[role] = allowed
			}
			return &snapshotRuntimeActor{Subject: input.Subject, Roles: roles}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &snapshotRuntimeActor{Subject: "user-1", Roles: map[string]bool{"reader": true}}
	caller, err := app.ForPrincipal(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() != 1 || stable.Load() == source {
		t.Fatalf("snapshot calls=%d stable=%p source=%p", snapshots.Load(), stable.Load(), source)
	}

	name := golem.GeneratedTextField[testUser, string](fixture.UserName)
	const reads = 24
	var wait sync.WaitGroup
	wait.Add(reads + 1)
	go func() {
		defer wait.Done()
		for index := 0; index < 20_000; index++ {
			source.Roles["reader"] = index%2 == 0
			source.Roles["admin"] = index%3 == 0
		}
	}()
	for index := 0; index < reads; index++ {
		go func() {
			defer wait.Done()
			rows, readErr := CallerFindMany(ctx, caller, userDescriptor, golem.Select[testUser](name))
			if readErr != nil || len(rows) != 1 {
				t.Errorf("rows=%d error=%v", len(rows), readErr)
			}
		}()
	}
	wait.Wait()
	if snapshots.Load() != 1 || hookCalls.Load() != reads {
		t.Fatalf("snapshot calls=%d hook calls=%d", snapshots.Load(), hookCalls.Load())
	}
}

func TestForPrincipalRejectsMutableActorWithoutSnapshotterAsUnauthenticated(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "actor-reject.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, _ := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	allowUser := golem.GeneratedPolicyBinding[*snapshotRuntimeActor, testUser](fixture.User, func(*snapshotRuntimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	allowPost := golem.GeneratedPolicyBinding[*snapshotRuntimeActor, testPost](fixture.Post, func(*snapshotRuntimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[*snapshotRuntimeActor]{allowUser, allowPost}, nil)
	bindings, _ := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	app, err := Open(ctx, Config[*snapshotRuntimeActor, *snapshotRuntimeActor]{Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(_ context.Context, actor *snapshotRuntimeActor) (*snapshotRuntimeActor, error) { return actor, nil }})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, &snapshotRuntimeActor{Roles: map[string]bool{"reader": true}})
	var failure *golem.Error
	if caller != nil || !errors.As(err, &failure) || failure.Code != golem.CodeUnauthenticated || failure.Message != "principal actor could not be snapshotted" {
		t.Fatalf("caller=%#v error=%v", caller, err)
	}
}
