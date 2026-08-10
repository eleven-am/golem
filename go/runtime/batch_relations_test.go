package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

func TestBatchChunkMagnitudeAndPerParentPagingSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("33,000-parent live SQLite batch oracle")
	}
	const (
		parentCount       = 33_000
		parentsWithPosts  = 950
		childrenPerParent = 3
	)
	ctx := context.Background()
	fixture := schematest.New(t)
	database, statementCount, err := openP3CountingSQLite("file:" + filepath.Join(t.TempDir(), "batch-magnitude.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	users, err := tx.PrepareContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := tx.PrepareContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < parentCount; index++ {
		userID := fmt.Sprintf("00000000-0000-0000-0000-%012x", index+1)
		if _, err := users.ExecContext(ctx, userID, fmt.Sprintf("user-%05d", index)); err != nil {
			t.Fatal(err)
		}
		if index >= parentsWithPosts {
			continue
		}
		for child := 0; child < childrenPerParent; child++ {
			postID := fmt.Sprintf("10000000-0000-0000-%04x-%012x", child, index+1)
			if _, err := posts.ExecContext(ctx, postID, userID, fmt.Sprintf("%05d-child-%d", index, child)); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = users.Close()
	_ = posts.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
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
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	postRelation := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)

	read := func(take int) []golem.Row[testUser] {
		statementCount.Store(0)
		rows, readErr := SystemFindMany(ctx, app.System(), userDescriptor,
			golem.OrderBy(userName.Asc()),
			golem.Select[testUser](userName, postRelation.Args(
				golem.OrderBy(postTitle.Asc()), golem.Skip[testPost](1), golem.Take[testPost](take), golem.Select[testPost](postTitle),
			)),
		)
		if readErr != nil {
			t.Fatalf("magnitude read take=%d: %v", take, readErr)
		}
		wantStatements := int64(1 + (parentCount+readsql.BatchChunkKeys-1)/readsql.BatchChunkKeys)
		if got := statementCount.Load(); got != wantStatements {
			t.Fatalf("take=%d statements=%d want=%d (one root plus exact bounded chunks)", take, got, wantStatements)
		}
		return rows
	}
	for _, take := range []int{2, -2} {
		rows := read(take)
		if len(rows) != parentCount {
			t.Fatalf("take=%d parents=%d want=%d", take, len(rows), parentCount)
		}
		for index, row := range rows {
			children, present := golem.Many(row, postRelation).Get()
			if !present || children == nil {
				t.Fatalf("take=%d parent=%d relation is absent or nil", take, index)
			}
			want := 0
			if index < parentsWithPosts {
				want = 2
			}
			if len(children) != want {
				t.Fatalf("take=%d parent=%d children=%d want=%d", take, index, len(children), want)
			}
		}
		// These positions pin the first parent, both sides of the exact
		// BatchChunkKeys boundary, the final parent with children, the first
		// empty parent, a middle parent, and the final parent. This makes a
		// mutation that drops or duplicates a chunk observable even when the
		// aggregate row count happens to remain unchanged.
		for _, index := range []int{0, readsql.BatchChunkKeys - 1, readsql.BatchChunkKeys, parentsWithPosts - 1, parentsWithPosts, parentCount / 2, parentCount - 1} {
			name, present := golem.Value(rows[index], userName).Get()
			if !present || name != fmt.Sprintf("user-%05d", index) {
				t.Fatalf("take=%d parent=%d name=%q present=%t", take, index, name, present)
			}
			children, present := golem.Many(rows[index], postRelation).Get()
			if !present || children == nil {
				t.Fatalf("take=%d boundary parent=%d relation is absent or nil", take, index)
			}
			if index >= parentsWithPosts {
				if len(children) != 0 {
					t.Fatalf("take=%d boundary parent=%d children=%d want=0", take, index, len(children))
				}
				continue
			}
			wantSuffixes := []int{1, 2}
			if take < 0 {
				wantSuffixes = []int{0, 1}
			}
			for childIndex, wantSuffix := range wantSuffixes {
				title, childPresent := golem.Value(children[childIndex], postTitle).Get()
				wantTitle := fmt.Sprintf("%05d-child-%d", index, wantSuffix)
				if !childPresent || title != wantTitle {
					t.Fatalf("take=%d boundary parent=%d child=%d title=%q present=%t want=%q", take, index, childIndex, title, childPresent, wantTitle)
				}
			}
		}
	}
}

func TestCanonicalBatchTupleIsTypedAndCollisionFree(t *testing.T) {
	one, _ := policyir.SignedValue(policyir.ValueInt64, 1)
	text, _ := policyir.StringValue("1")
	float, _ := policyir.Float64Value(1)
	values := []policyir.Value{one, text, float}
	seen := map[string]bool{}
	for _, value := range values {
		identity, err := canonicalBatchTuple([]policyir.Value{value})
		if err != nil {
			t.Fatal(err)
		}
		if seen[identity] {
			t.Fatal("typed identities collided")
		}
		seen[identity] = true
	}
}

func TestBatchChunkMagnitudePostgreSQLProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("70,000-parent live PostgreSQL batch oracle")
	}
	profiles := []struct{ name, dsn string }{
		{"c", strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))},
		{"linguistic", strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))},
	}
	if profiles[0].dsn == "" || profiles[1].dsn == "" {
		t.Skip("both PostgreSQL profile DSNs are required")
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			const parentCount = 70_000
			ctx := context.Background()
			fixture := schematest.New(t)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_batch_%d", time.Now().UnixNano()))
			schema := fixture.PostgreSQL
			schema.Namespace.Name = namespace
			_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
			_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
			})
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `INSERT INTO "`+string(namespace)+`"."users"("id","name") SELECT ('00000000-0000-0000-0000-' || lpad(to_hex(i), 12, '0'))::uuid, 'user-' || lpad(i::text, 5, '0') FROM generate_series(1, $1) AS i`, parentCount); err != nil {
				t.Fatal(err)
			}
			bundle := postgresRuntimeBundle(t, fixture, schema)
			userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
			postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
			descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
			descriptors, _ := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
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
			bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, nil)
			bindings, _ := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
			app, err := Open(ctx, Config[testPrincipal, testActor]{Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
			if err != nil {
				t.Fatal(err)
			}
			userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
			postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
			relation := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
			rows, err := SystemFindMany(ctx, app.System(), userDescriptor, golem.OrderBy(userName.Asc()), golem.Select[testUser](relation.Args(golem.OrderBy(postTitle.Asc()), golem.Take[testPost](1), golem.Select[testPost](postTitle))))
			if err != nil || len(rows) != parentCount {
				t.Fatalf("rows=%d err=%v", len(rows), err)
			}
			children, present := golem.Many(rows[len(rows)-1], relation).Get()
			if !present || children == nil || len(children) != 0 {
				t.Fatalf("last empty relation=%v present=%t", children, present)
			}
		})
	}
}
