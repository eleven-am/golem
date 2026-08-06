package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// This file is deliberately an end-to-end oracle rather than another planner
// unit test. Expected rows are derived directly from the seed data and the
// author-facing policy declarations; no production plan, predicate evaluator,
// SQL renderer, or decoder is used to compute the expected result.

type oracleUser struct{}
type oraclePost struct{}

type oraclePrincipal struct {
	UserPrefix string
	PostPrefix string
}

type oracleActor = oraclePrincipal

type oracleHarness struct {
	app        *App[oraclePrincipal, oracleActor]
	fixture    schematest.Fixture
	users      golem.ModelDescriptor[oracleUser]
	posts      golem.ModelDescriptor[oraclePost]
	userID     golem.EqualField[oracleUser, golem.UUID]
	userName   golem.TextField[oracleUser, string]
	postID     golem.EqualField[oraclePost, golem.UUID]
	authorID   golem.EqualField[oraclePost, golem.UUID]
	postTitle  golem.TextField[oraclePost, string]
	userPosts  golem.ToMany[oracleUser, oraclePost]
	postAuthor golem.ToOne[oraclePost, oracleUser]
}

func newOracleHarness(t *testing.T) oracleHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "p3-read-oracle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}

	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil,
	))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil,
	))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}

	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		if actor.UserPrefix == "" {
			rules.CanRead(golem.All[oracleUser]())
		} else {
			rules.CanRead(userName.StartsWith(actor.UserPrefix))
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
		} else {
			rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		}
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return oracleHarness{
		app: app, fixture: fixture, users: users, posts: posts,
		userID:     golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID),
		userName:   userName,
		postID:     golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID),
		authorID:   golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.AuthorID),
		postTitle:  postTitle,
		userPosts:  golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post),
		postAuthor: golem.GeneratedToOne[oraclePost, oracleUser](fixture.PostAuthor, fixture.Authorship, fixture.User),
	}
}

func (h oracleHarness) seedUser(t *testing.T, id, name string) {
	t.Helper()
	if _, err := h.app.database.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, id, name); err != nil {
		t.Fatal(err)
	}
}

func (h oracleHarness) seedPost(t *testing.T, id, authorID, title string) {
	t.Helper()
	if _, err := h.app.database.ExecContext(context.Background(), `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, id, authorID, title); err != nil {
		t.Fatal(err)
	}
}

func TestP3ReadOraclePolicyPrecedesPagingAndPrimaryKeyBreaksTies(t *testing.T) {
	h := newOracleHarness(t)
	for _, seed := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "hidden-a"},
		{"00000000-0000-0000-0000-000000000002", "hidden-b"},
		{"00000000-0000-0000-0000-000000000004", "visible-same"},
		{"00000000-0000-0000-0000-000000000003", "visible-same"},
		{"00000000-0000-0000-0000-000000000005", "visible-z"},
	} {
		h.seedUser(t, seed[0], seed[1])
	}
	caller, err := h.app.ForPrincipal(context.Background(), oraclePrincipal{UserPrefix: "visible-"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"00000000-0000-0000-0000-000000000003",
		"00000000-0000-0000-0000-000000000004",
	}
	for repetition := 0; repetition < 20; repetition++ {
		rows, readErr := CallerFindMany(context.Background(), caller, h.users,
			golem.OrderBy(h.userName.Asc()),
			golem.Take[oracleUser](2),
			golem.Select[oracleUser](h.userID, h.userName),
		)
		if readErr != nil {
			t.Fatal(readErr)
		}
		got := oracleUserIDs(t, rows, h.userID)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("repetition %d IDs=%v want=%v", repetition, got, want)
		}
	}
	count, err := CallerCount(context.Background(), caller, h.users)
	if err != nil || count != 3 {
		t.Fatalf("authorized count=%d err=%v, want 3", count, err)
	}
	systemCount, err := SystemCount(context.Background(), h.app.System(), h.users)
	if err != nil || systemCount != 5 {
		t.Fatalf("system count=%d err=%v, want 5", systemCount, err)
	}
}

func TestP3ReadOracleRelationTargetPolicyPrecedesPerParentPaging(t *testing.T) {
	h := newOracleHarness(t)
	h.seedUser(t, "00000000-0000-0000-0000-000000000001", "alpha")
	h.seedUser(t, "00000000-0000-0000-0000-000000000002", "beta")
	h.seedPost(t, "00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-a")
	h.seedPost(t, "00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-z")
	h.seedPost(t, "00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000001", "zzzz-denied")
	h.seedPost(t, "00000000-0000-0000-0000-000000000014", "00000000-0000-0000-0000-000000000002", "zzzz-denied-too")

	caller, err := h.app.ForPrincipal(context.Background(), oraclePrincipal{PostPrefix: "allowed-"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(context.Background(), caller, h.users,
		golem.OrderBy(h.userName.Asc()),
		golem.Select[oracleUser](h.userName, h.userPosts.Args(
			golem.OrderBy(h.postTitle.Desc()),
			golem.Take[oraclePost](1),
			golem.Select[oraclePost](h.postID, h.postTitle),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("users=%d want=2", len(rows))
	}
	alicePosts, present := golem.Many(rows[0], h.userPosts).Get()
	if !present || len(alicePosts) != 1 {
		t.Fatalf("alpha posts=%d present=%t", len(alicePosts), present)
	}
	if title, ok := golem.Value(alicePosts[0], h.postTitle).Get(); !ok || title != "allowed-z" {
		t.Fatalf("alpha first authorized title=%q present=%t, want allowed-z", title, ok)
	}
	betaPosts, present := golem.Many(rows[1], h.userPosts).Get()
	if !present || betaPosts == nil || len(betaPosts) != 0 {
		t.Fatalf("beta posts=%v present=%t, want selected non-nil empty relation", betaPosts, present)
	}

	// Relation accessors must detach their returned graph. Mutating one readout
	// cannot alter the opaque row or a later readout.
	alicePosts[0] = golem.Row[oraclePost]{}
	alicePosts = append(alicePosts, golem.Row[oraclePost]{})
	again, present := golem.Many(rows[0], h.userPosts).Get()
	if !present || len(again) != 1 {
		t.Fatalf("relation graph mutated through accessor: rows=%d present=%t", len(again), present)
	}
	if title, ok := golem.Value(again[0], h.postTitle).Get(); !ok || title != "allowed-z" {
		t.Fatalf("detached relation title=%q present=%t", title, ok)
	}
}

func TestP3ReadOracleConcurrentPrincipalIsolation(t *testing.T) {
	h := newOracleHarness(t)
	for index := 0; index < 8; index++ {
		h.seedUser(t, fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1), fmt.Sprintf("alpha-%02d", index))
		h.seedUser(t, fmt.Sprintf("10000000-0000-0000-0000-%012d", index+1), fmt.Sprintf("beta-%02d", index))
	}

	const executions = 64
	var wait sync.WaitGroup
	wait.Add(executions)
	for index := 0; index < executions; index++ {
		go func(index int) {
			defer wait.Done()
			prefix := "alpha-"
			if index%2 != 0 {
				prefix = "beta-"
			}
			caller, err := h.app.ForPrincipal(context.Background(), oraclePrincipal{UserPrefix: prefix})
			if err != nil {
				t.Errorf("caller %d: %v", index, err)
				return
			}
			rows, err := CallerFindMany(context.Background(), caller, h.users,
				golem.OrderBy(h.userName.Asc()), golem.Select[oracleUser](h.userName),
			)
			if err != nil {
				t.Errorf("read %d: %v", index, err)
				return
			}
			if len(rows) != 8 {
				t.Errorf("read %d returned %d rows, want 8", index, len(rows))
				return
			}
			for rowIndex, row := range rows {
				name, ok := golem.Value(row, h.userName).Get()
				want := fmt.Sprintf("%s%02d", prefix, rowIndex)
				if !ok || name != want {
					t.Errorf("read %d row %d=%q present=%t want=%q", index, rowIndex, name, ok, want)
					return
				}
			}
		}(index)
	}
	wait.Wait()
}

func TestP3ReadOracleInvisibleAndMissingUniqueHaveSamePublicDisclosure(t *testing.T) {
	h := newOracleHarness(t)
	h.seedUser(t, "00000000-0000-0000-0000-000000000001", "hidden-user")
	caller, err := h.app.ForPrincipal(context.Background(), oraclePrincipal{UserPrefix: "visible-"})
	if err != nil {
		t.Fatal(err)
	}

	read := func(id string) *golem.Error {
		t.Helper()
		parsed, parseErr := golem.ParseUUID(id)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		selector := golem.GeneratedUniqueSelectorValue[oracleUser](h.fixture.User, h.fixture.UserKey, golem.GeneratedSelectorComponent(h.fixture.UserID, parsed))
		_, readErr := CallerFindUnique(context.Background(), caller, h.users, selector, golem.Select[oracleUser](h.userName))
		var public *golem.Error
		if !errors.As(readErr, &public) {
			t.Fatalf("error=%v is not a public read error", readErr)
		}
		return public
	}
	invisible := read("00000000-0000-0000-0000-000000000001")
	missing := read("00000000-0000-0000-0000-000000000099")
	if invisible.Code != golem.CodeNotFound || missing.Code != golem.CodeNotFound {
		t.Fatalf("codes invisible=%s missing=%s", invisible.Code, missing.Code)
	}
	if invisible.Error() != missing.Error() || invisible.Operation != missing.Operation || invisible.Model != missing.Model || invisible.Field != missing.Field {
		t.Fatalf("public disclosure differs: invisible=%#v missing=%#v", invisible, missing)
	}
}

func oracleUserIDs(t *testing.T, rows []golem.Row[oracleUser], field golem.EqualField[oracleUser, golem.UUID]) []string {
	t.Helper()
	result := make([]string, len(rows))
	for index, row := range rows {
		value, ok := golem.Value(row, field).Get()
		if !ok {
			t.Fatalf("row %d ID is not present", index)
		}
		result[index] = value.String()
	}
	return result
}
