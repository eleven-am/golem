package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type testPrincipal struct{ Allow bool }
type testActor struct{ Allow bool }
type testUser struct{}
type testPost struct{}

func TestOpenCreatesIsolatedCallerAndExplicitSystemExecutions(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime.db"))
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

	var policyBuilds atomic.Int64
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(actor testActor) (golem.FrozenPolicy, error) {
		policyBuilds.Add(1)
		rules := golem.NewRules[testUser]()
		if actor.Allow {
			rules.CanRead(golem.All[testUser]())
		} else {
			rules.CanRead(golem.None[testUser]())
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(actor testActor) (golem.FrozenPolicy, error) {
		policyBuilds.Add(1)
		rules := golem.NewRules[testPost]()
		if actor.Allow {
			rules.CanRead(golem.All[testPost]())
		} else {
			rules.CanRead(golem.None[testPost]())
		}
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
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
	request, err := golem.FreezeFindMany(userDescriptor, golem.Select[testUser](golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)))
	if err != nil {
		t.Fatal(err)
	}

	const executions = 32
	var wait sync.WaitGroup
	wait.Add(executions)
	for index := 0; index < executions; index++ {
		go func(index int) {
			defer wait.Done()
			caller, callErr := app.ForPrincipal(ctx, testPrincipal{Allow: index%2 == 0})
			if callErr != nil {
				t.Errorf("caller %d: %v", index, callErr)
				return
			}
			prepared, prepareErr := caller.Prepare(request)
			if prepareErr != nil {
				t.Errorf("prepare %d: %v", index, prepareErr)
				return
			}
			policy, ok := prepared.policies.Policy(policyir.ModelID(fixture.User))
			if !ok {
				t.Errorf("caller %d has no user policy", index)
				return
			}
			constraint, constraintErr := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(fixture.User))
			if constraintErr != nil {
				t.Errorf("constraint %d: %v", index, constraintErr)
				return
			}
			truth, constant := constraint.Constant()
			if !constant || truth != (index%2 == 0) {
				t.Errorf("caller %d leaked policy truth=%t constant=%t", index, truth, constant)
			}
		}(index)
	}
	wait.Wait()
	if policyBuilds.Load() != executions*2 {
		t.Fatalf("policy builds=%d want=%d", policyBuilds.Load(), executions*2)
	}

	systemPrepared, err := app.System().Prepare(request)
	if err != nil || !systemPrepared.IsSystem() || systemPrepared.policies != nil {
		t.Fatalf("system prepared=%#v err=%v", systemPrepared, err)
	}
}

func TestMutationPrincipalFailureCannotBecomeSystem(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-auth.db"))
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
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, nil)
	bindings, _ := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) {
		return testActor{}, errors.New("invalid session")
	}})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	var failure *golem.Error
	if caller != nil || !errors.As(err, &failure) || failure.Code != golem.CodeUnauthenticated {
		t.Fatalf("caller=%#v error=%v", caller, err)
	}
	if app.System().app == nil {
		t.Fatal("explicit system capability was not retained")
	}
}

func TestRootReadExecutionDecodesRowsAndAppliesPolicyBeforePaging(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-read.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "alice"}, {"00000000-0000-0000-0000-000000000002", "bob"}, {"00000000-0000-0000-0000-000000000003", "carol"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, _ := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(actor testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		if actor.Allow {
			rules.CanRead(golem.All[testUser]())
		} else {
			rules.CanRead(golem.None[testUser]())
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
	bindings, _ := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(_ context.Context, principal testPrincipal) (testActor, error) {
		return testActor{Allow: principal.Allow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[testUser, string](fixture.UserName)
	rows, err := SystemFindMany(ctx, app.System(), userDescriptor, golem.OrderBy(name.Desc()), golem.Take[testUser](2), golem.Select[testUser](id, name))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	firstName, ok := golem.Value(rows[0], name).Get()
	if !ok || firstName != "carol" {
		t.Fatalf("first name=%q present=%t", firstName, ok)
	}
	firstID, ok := golem.Value(rows[0], id).Get()
	if !ok || firstID.String() != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("first ID=%v present=%t", firstID, ok)
	}
	selector := golem.GeneratedUniqueSelectorValue[testUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, golem.UUID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}))
	unique, err := SystemFindUnique(ctx, app.System(), userDescriptor, selector, golem.Select[testUser](name))
	if err != nil {
		t.Fatal(err)
	}
	uniqueName, ok := golem.Value(unique, name).Get()
	if !ok || uniqueName != "bob" {
		t.Fatalf("unique name=%q present=%t", uniqueName, ok)
	}
	count, err := SystemCount(ctx, app.System(), userDescriptor)
	if err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	allowed, _ := app.ForPrincipal(ctx, testPrincipal{Allow: true})
	callerRows, err := CallerFindMany(ctx, allowed, userDescriptor, golem.Take[testUser](1), golem.Select[testUser](name))
	if err != nil || len(callerRows) != 1 {
		t.Fatalf("allowed rows=%d err=%v", len(callerRows), err)
	}
	denied, _ := app.ForPrincipal(ctx, testPrincipal{Allow: false})
	callerRows, err = CallerFindMany(ctx, denied, userDescriptor, golem.Take[testUser](1), golem.Select[testUser](name))
	var deniedError *golem.Error
	if len(callerRows) != 0 || !errors.As(err, &deniedError) || deniedError.Code != golem.CodeForbidden {
		t.Fatalf("denied rows=%d err=%v", len(callerRows), err)
	}
}

func TestRelationExecutionUsesAuthorizedBoundedChildPlans(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-relations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "alice"}, {"00000000-0000-0000-0000-000000000002", "bob"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "alpha"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "zeta"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, _ := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
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
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, nil)
	bindings, _ := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}

	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	postAuthorID := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	rows, err := SystemFindMany(ctx, app.System(), userDescriptor,
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](userName, posts.Args(golem.OrderBy(postTitle.Desc()), golem.Take[testPost](1), golem.Select[testPost](postTitle))),
	)
	if err != nil {
		t.Fatalf("relation read: %v: %v", err, errors.Unwrap(err))
	}
	if len(rows) != 2 {
		t.Fatalf("users=%d", len(rows))
	}
	alicePosts, ok := golem.Many(rows[0], posts).Get()
	if !ok || len(alicePosts) != 1 {
		t.Fatalf("alice posts=%d present=%t", len(alicePosts), ok)
	}
	if title, present := golem.Value(alicePosts[0], postTitle).Get(); !present || title != "zeta" {
		t.Fatalf("bounded ordered title=%q present=%t", title, present)
	}
	bobPosts, ok := golem.Many(rows[1], posts).Get()
	if !ok || bobPosts == nil || len(bobPosts) != 0 {
		t.Fatalf("bob posts=%v present=%t", bobPosts, ok)
	}
	// The retained correlated executor is a context-scoped acceptance oracle,
	// never a production N+1 fallback. Both strategies must expose the same
	// ordered graph and empty-list state.
	oracleContext := context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadCorrelatedOracle)
	oracleRows, err := SystemFindMany(oracleContext, app.System(), userDescriptor,
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](userName, posts.Args(golem.OrderBy(postTitle.Desc()), golem.Take[testPost](1), golem.Select[testPost](postTitle))),
	)
	if err != nil || len(oracleRows) != len(rows) {
		t.Fatalf("correlated oracle rows=%d err=%v", len(oracleRows), err)
	}
	for index := range rows {
		batched, batchedOK := golem.Many(rows[index], posts).Get()
		correlated, correlatedOK := golem.Many(oracleRows[index], posts).Get()
		if batchedOK != correlatedOK || len(batched) != len(correlated) || len(batched) == 0 && (batched == nil || correlated == nil) {
			t.Fatalf("strategy shape mismatch row=%d batch=%v oracle=%v", index, batched, correlated)
		}
		for child := range batched {
			left, _ := golem.Value(batched[child], postTitle).Get()
			right, _ := golem.Value(correlated[child], postTitle).Get()
			if left != right {
				t.Fatalf("strategy value mismatch row=%d child=%d batch=%q oracle=%q", index, child, left, right)
			}
		}
	}

	author := golem.GeneratedToOne[testPost, testUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	postRows, err := SystemFindMany(ctx, app.System(), postDescriptor,
		golem.OrderBy(postTitle.Asc()),
		golem.Select[testPost](postTitle, author.Select(userName, posts.Count())),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(postRows) != 2 {
		t.Fatalf("posts=%d", len(postRows))
	}
	loadedAuthor, ok := golem.One(postRows[0], author).Get()
	if !ok {
		t.Fatal("to-one author was not loaded")
	}
	if name, present := golem.Value(loadedAuthor, userName).Get(); !present || name != "alice" {
		t.Fatalf("author=%q present=%t", name, present)
	}
	if count, present := golem.RelationCount(loadedAuthor, posts).Get(); !present || count != 2 {
		t.Fatalf("nested author post count=%d present=%t", count, present)
	}

	// One cursor anchor belongs to Alice. Bob now has an otherwise matching
	// child in the same loader chunk; per-parent cursor ownership must leave
	// Bob empty in both the batch and correlated oracle strategies.
	if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, "00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "beta"); err != nil {
		t.Fatal(err)
	}
	alphaID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000011")
	postCursor := golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, alphaID))
	cursorOptions := []golem.ReadOption[testUser]{
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](posts.Args(golem.OrderBy(postTitle.Asc()), golem.Cursor(postCursor), golem.Take[testPost](2), golem.Select[testPost](postTitle))),
	}
	batchCursorRows, err := SystemFindMany(ctx, app.System(), userDescriptor, cursorOptions...)
	if err != nil {
		t.Fatal(err)
	}
	oracleCursorRows, err := SystemFindMany(context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadCorrelatedOracle), app.System(), userDescriptor, cursorOptions...)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range [][]golem.Row[testUser]{batchCursorRows, oracleCursorRows} {
		alice, _ := golem.Many(candidate[0], posts).Get()
		bob, present := golem.Many(candidate[1], posts).Get()
		if len(alice) != 2 || !present || bob == nil || len(bob) != 0 {
			t.Fatalf("per-parent cursor batch alice=%d bob=%v present=%t", len(alice), bob, present)
		}
	}

	// A caller's child policy is part of the child SQL plan, before its Take.
	// With descending order the unauthorized "zeta" row would win if paging
	// happened first; the only returned row must therefore be "alpha".
	limitedPosts := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(postTitle.Eq("alpha"))
		// The loader correlation key is never caller-readable. The batch path
		// may use it privately for exact attachment but must not expose it.
		rules.CannotReadFields(golem.All[testPost](), postAuthorID)
		return rules.Freeze(fixture.Post)
	})
	callerPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, limitedPosts}, nil)
	callerBindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), callerPackage)
	if err != nil {
		t.Fatal(err)
	}
	callerApp, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: callerBindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := callerApp.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	authorizedRows, err := CallerFindMany(ctx, caller, userDescriptor,
		golem.Where(userName.Eq("alice")),
		golem.Select[testUser](posts.Args(golem.OrderBy(postTitle.Desc()), golem.Take[testPost](1), golem.Select[testPost](postTitle))),
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizedPosts, ok := golem.Many(authorizedRows[0], posts).Get()
	if !ok || len(authorizedPosts) != 1 {
		t.Fatalf("authorized posts=%d present=%t", len(authorizedPosts), ok)
	}
	if title, present := golem.Value(authorizedPosts[0], postTitle).Get(); !present || title != "alpha" {
		t.Fatalf("authorization-before-page title=%q present=%t", title, present)
	}
	if golem.Value(authorizedPosts[0], postAuthorID).State() != golem.ReadUnselected {
		t.Fatal("private batch correlation key leaked into the public child row")
	}
}

func TestRelationCountExecutesTargetPolicyAndWhereInSQLAndStripsDependencies(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-relation-count.db"))
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
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-match"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-other"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000001", "denied-match"},
		{"00000000-0000-0000-0000-000000000014", "00000000-0000-0000-0000-000000000002", "denied-match"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	userID := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	allowUsers := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	limitedPosts := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(postTitle.StartsWith("allowed"))
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUsers, limitedPosts}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	options := []golem.ReadOption[testUser]{
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](userName, posts.Count(golem.Where(postTitle.EndsWith("match")))),
	}
	rows, err := CallerFindMany(ctx, caller, userDescriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	for index, want := range []int64{1, 0} {
		count := golem.RelationCount(rows[index], posts)
		got, present := count.Get()
		if !present || got != want || count.State() != golem.ReadPresent {
			t.Fatalf("caller row %d count=%d present=%t state=%d want=%d", index, got, present, count.State(), want)
		}
		if golem.Value(rows[index], userID).State() != golem.ReadUnselected {
			t.Fatalf("caller row %d leaked private primary-key dependency", index)
		}
		if golem.Many(rows[index], posts).State() != golem.ReadUnselected {
			t.Fatalf("caller row %d count selection leaked relation rows", index)
		}
	}

	// The explicit system path proves the per-count where is independent of the
	// target policy: it includes denied-match rows while preserving correlation.
	systemRows, err := SystemFindMany(ctx, app.System(), userDescriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []int64{2, 1} {
		got, present := golem.RelationCount(systemRows[index], posts).Get()
		if !present || got != want {
			t.Fatalf("system row %d count=%d present=%t want=%d", index, got, present, want)
		}
	}
}

func TestConditionalMaskRelationHydrationIsPrivatePolicyScopedAndProjectionInvariant(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-mask-relations.db"))
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
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-mask"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-public"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "denied-mask"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		rules.CannotReadFields(golem.All[testUser](), userName)
		rules.CanReadFields(posts.Some(postTitle.Contains("mask")), userName)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(postTitle.StartsWith("allowed-"))
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
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

	minimal, err := CallerFindMany(ctx, caller, userDescriptor,
		golem.OrderBy(userID.Asc()),
		golem.Select[testUser](userID, userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(minimal) != 2 {
		t.Fatalf("minimal rows=%d", len(minimal))
	}
	if value, present := golem.Value(minimal[0], userName).Get(); !present || value != "alice" {
		t.Fatalf("alice minimal name=%q present=%t", value, present)
	}
	if value := golem.Value(minimal[1], userName); !value.IsSelected() || !value.IsNull() {
		t.Fatalf("bob minimal mask state=%d", value.State())
	}
	if value := golem.Many(minimal[0], posts); value.IsSelected() {
		t.Fatal("policy-only relation hydration leaked into the public row")
	}

	withPublicPage, err := CallerFindMany(ctx, caller, userDescriptor,
		golem.OrderBy(userID.Asc()),
		golem.Select[testUser](userID, userName, posts.Args(
			golem.Where(postTitle.Eq("allowed-public")),
			golem.Take[testPost](1),
			golem.Select[testPost](postTitle),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if value, present := golem.Value(withPublicPage[0], userName).Get(); !present || value != "alice" {
		t.Fatalf("alice paged name=%q present=%t", value, present)
	}
	if value := golem.Value(withPublicPage[1], userName); !value.IsSelected() || !value.IsNull() {
		t.Fatalf("bob paged mask state=%d", value.State())
	}
	publicPosts, present := golem.Many(withPublicPage[0], posts).Get()
	if !present || len(publicPosts) != 1 {
		t.Fatalf("public posts=%d present=%t", len(publicPosts), present)
	}
	if title, present := golem.Value(publicPosts[0], postTitle).Get(); !present || title != "allowed-public" {
		t.Fatalf("public relation title=%q present=%t", title, present)
	}
}

func TestCallerReadHooksCanNarrowRequestsAndObserveDetachedMaskedResults(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "alice"}, {"00000000-0000-0000-0000-000000000002", "bob"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, _ := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
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
	var beforeCalls, afterCalls atomic.Int64
	before := golem.GeneratedBeforeHookBinding[testActor, testUser, golem.FindManyHookRequest[testUser]](fixture.User, golem.HookFindMany, func(hookCtx context.Context, request *golem.FindManyHookRequest[testUser]) error {
		beforeCalls.Add(1)
		actor := golem.ActorFrom[testActor](hookCtx)
		wanted := "bob"
		if actor.Allow {
			wanted = "alice"
		}
		request.AppendOptions(golem.Where(name.Eq(wanted)))
		return nil
	})
	after := golem.GeneratedAfterHookBinding[testActor, testUser, golem.FindManyHookResult[testUser]](fixture.User, golem.HookFindMany, func(hookCtx context.Context, result golem.FindManyHookResult[testUser]) error {
		afterCalls.Add(1)
		if !golem.ActorFrom[testActor](hookCtx).Allow {
			return errors.New("unexpected actor")
		}
		rows := result.Rows()
		if len(rows) != 1 {
			return errors.New("unexpected result length")
		}
		rows[0] = golem.Row[testUser]{}
		return nil
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, []golem.HookBinding[testActor]{before, after})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(_ context.Context, principal testPrincipal) (testActor, error) {
		return testActor{Allow: principal.Allow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(ctx, caller, userDescriptor, golem.Select[testUser](name))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("caller rows=%d", len(rows))
	}
	if value, present := golem.Value(rows[0], name).Get(); !present || value != "alice" {
		t.Fatalf("hooked row=%q present=%t", value, present)
	}
	if beforeCalls.Load() != 1 || afterCalls.Load() != 1 {
		t.Fatalf("hook calls before=%d after=%d", beforeCalls.Load(), afterCalls.Load())
	}
	systemRows, err := SystemFindMany(ctx, app.System(), userDescriptor, golem.Select[testUser](name))
	if err != nil || len(systemRows) != 2 {
		t.Fatalf("system rows=%d err=%v", len(systemRows), err)
	}
	if beforeCalls.Load() != 1 || afterCalls.Load() != 1 {
		t.Fatal("system read invoked caller hooks")
	}
}
