package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestMutationMASK_ONE_STRATEGYIndexedCorrelatedSQLiteAgreesWithBatchAndCoversToOne(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewIndexed(t)
	database, statementCount, err := openP3CountingSQLite("file:" + filepath.Join(t.TempDir(), "correlated.db"))
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
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "a-allowed"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "aa-denied"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000001", "b-allowed"},
		{"00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000002", "c-allowed"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, _ := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	postID := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	postAuthorID := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[testPost, testUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	allowUser := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	limitedPost := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(postTitle.EndsWith("allowed"))
		rules.CanReadFields(golem.All[testPost](), postTitle, postID)
		rules.CannotReadFields(golem.All[testPost](), postAuthorID)
		rules.CanReadFields(postTitle.StartsWith("b-"), postAuthorID)
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, limitedPost}, nil)
	bindings, _ := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
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
		golem.Select[testUser](userName, posts.Args(golem.OrderBy(postTitle.Desc()), golem.Take[testPost](1), golem.Select[testPost](postTitle, postAuthorID))),
	}
	statementCount.Store(0)
	correlated, err := CallerFindMany(ctx, caller, userDescriptor, options...)
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	if got := statementCount.Load(); got != 1 {
		t.Fatalf("indexed correlated to-many executed %d statements, want one", got)
	}
	batched, err := CallerFindMany(context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched), caller, userDescriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	if len(correlated) != 2 || len(batched) != 2 {
		t.Fatalf("correlated=%d batched=%d", len(correlated), len(batched))
	}
	for index, want := range []string{"b-allowed", "c-allowed"} {
		left, leftOK := golem.Many(correlated[index], posts).Get()
		right, rightOK := golem.Many(batched[index], posts).Get()
		if !leftOK || !rightOK || len(left) != 1 || len(right) != 1 {
			t.Fatalf("row=%d correlated=%v batch=%v", index, left, right)
		}
		leftTitle, _ := golem.Value(left[0], postTitle).Get()
		rightTitle, _ := golem.Value(right[0], postTitle).Get()
		if leftTitle != want || rightTitle != want {
			t.Fatalf("row=%d correlated=%q batch=%q want=%q", index, leftTitle, rightTitle, want)
		}
		leftKey, rightKey := golem.Value(left[0], postAuthorID), golem.Value(right[0], postAuthorID)
		if leftKey.State() != rightKey.State() {
			t.Fatalf("row=%d mask state correlated=%d batch=%d", index, leftKey.State(), rightKey.State())
		}
		wantState := golem.ReadNull
		if index == 0 {
			wantState = golem.ReadPresent
		}
		if leftKey.State() != wantState {
			t.Fatalf("row=%d correlation-key mask state=%d want=%d", index, leftKey.State(), wantState)
		}
	}

	// To-one is correlated too: a root post read carries its author JSON and
	// does not execute one author query per post.
	statementCount.Store(0)
	postRows, err := SystemFindMany(ctx, app.System(), postDescriptor,
		golem.OrderBy(postTitle.Asc()), golem.Select[testPost](postTitle, author.Select(userName)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(postRows) != 4 {
		t.Fatalf("posts=%d", len(postRows))
	}
	if got := statementCount.Load(); got != 1 {
		t.Fatalf("correlated to-one executed %d statements, want one", got)
	}
	for _, row := range postRows {
		loaded, ok := golem.One(row, author).Get()
		if !ok {
			t.Fatal("correlated to-one author is absent")
		}
		if name, present := golem.Value(loaded, userName).Get(); !present || (name != "alice" && name != "bob") {
			t.Fatalf("author=%q present=%t", name, present)
		}
	}

	anchorID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000011")
	if err != nil {
		t.Fatal(err)
	}
	postCursor := golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, anchorID))
	cursorOptions := []golem.ReadOption[testUser]{
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](posts.Args(golem.OrderBy(postTitle.Asc()), golem.Cursor(postCursor), golem.Take[testPost](2), golem.Select[testPost](postTitle))),
	}
	statementCount.Store(0)
	cursorRows, err := SystemFindMany(ctx, app.System(), userDescriptor, cursorOptions...)
	if err != nil {
		t.Fatal(err)
	}
	if got := statementCount.Load(); got != 1 {
		t.Fatalf("correlated cursor executed %d statements, want one", got)
	}
	batchCursorRows, err := SystemFindMany(context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched), app.System(), userDescriptor, cursorOptions...)
	if err != nil {
		t.Fatal(err)
	}
	for index, candidate := range [][]golem.Row[testUser]{cursorRows, batchCursorRows} {
		alice, alicePresent := golem.Many(candidate[0], posts).Get()
		bob, bobPresent := golem.Many(candidate[1], posts).Get()
		if !alicePresent || len(alice) != 2 || !bobPresent || bob == nil || len(bob) != 0 {
			t.Fatalf("strategy=%d cursor ownership alice=%v bob=%v states=%t/%t", index, alice, bob, alicePresent, bobPresent)
		}
	}

	// Tied caller order is completed by the planner's stable key order. Pin the
	// emitted aggregate ordinal for both signed take directions and prove the
	// production correlated result remains byte-for-byte equal to batching.
	for index := 1; index <= 4; index++ {
		id := "00000000-0000-0000-0000-00000000003" + string(rune('0'+index))
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, id, "00000000-0000-0000-0000-000000000001", "tie"); err != nil {
			t.Fatal(err)
		}
	}
	for _, take := range []int{2, -2} {
		tiedOptions := []golem.ReadOption[testUser]{
			golem.OrderBy(userName.Asc()),
			golem.Select[testUser](posts.Args(golem.Where(postTitle.Eq("tie")), golem.OrderBy(postTitle.Asc()), golem.Take[testPost](take), golem.Select[testPost](postID))),
		}
		var baseline []string
		for repetition := 0; repetition < 8; repetition++ {
			statementCount.Store(0)
			rows, readErr := SystemFindMany(ctx, app.System(), userDescriptor, tiedOptions...)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := statementCount.Load(); got != 1 {
				t.Fatalf("take=%d repetition=%d correlated statements=%d want=1", take, repetition, got)
			}
			children, present := golem.Many(rows[0], posts).Get()
			if !present || len(children) != 2 {
				t.Fatalf("take=%d tied children=%v present=%t", take, children, present)
			}
			current := make([]string, len(children))
			for index := range children {
				value, ok := golem.Value(children[index], postID).Get()
				if !ok {
					t.Fatalf("take=%d child=%d id absent", take, index)
				}
				current[index] = value.String()
			}
			if repetition == 0 {
				baseline = current
			} else if baseline[0] != current[0] || baseline[1] != current[1] {
				t.Fatalf("take=%d unstable order baseline=%v current=%v", take, baseline, current)
			}
		}
		batchedRows, readErr := SystemFindMany(context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched), app.System(), userDescriptor, tiedOptions...)
		if readErr != nil {
			t.Fatal(readErr)
		}
		batchedChildren, present := golem.Many(batchedRows[0], posts).Get()
		if !present || len(batchedChildren) != len(baseline) {
			t.Fatalf("take=%d batch tied children=%v present=%t", take, batchedChildren, present)
		}
		for index := range batchedChildren {
			value, ok := golem.Value(batchedChildren[index], postID).Get()
			if !ok || value.String() != baseline[index] {
				t.Fatalf("take=%d child=%d correlated=%q batch=%q", take, index, baseline[index], value.String())
			}
		}
	}

	// A relation nested inside correlated JSON has no nested payload slot yet.
	// Its to-one fallback must still be one bounded batch, never N queries for
	// N decoded children.
	statementCount.Store(0)
	nestedRows, err := SystemFindMany(ctx, app.System(), userDescriptor,
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](posts.Args(golem.OrderBy(postTitle.Asc()), golem.Select[testPost](postTitle, author.Select(userName)))),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := statementCount.Load(); got != 2 {
		t.Fatalf("correlated posts with nested to-one executed %d statements, want root plus one bounded batch", got)
	}
	for _, parent := range nestedRows {
		children, present := golem.Many(parent, posts).Get()
		if !present {
			t.Fatal("nested post relation absent")
		}
		for _, child := range children {
			loaded, present := golem.One(child, author).Get()
			if !present {
				t.Fatal("bounded nested author absent")
			}
			if _, present := golem.Value(loaded, userName).Get(); !present {
				t.Fatal("bounded nested author name absent")
			}
		}
	}
}
