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
	"github.com/jmoiron/sqlx"
)

type expansionRuntimeActor struct{}
type expansionRuntimeUser struct{}
type expansionRuntimePost struct{}

func TestRuntimeRecursivelyScopesRootRelationPolicyAndRejectsCyclesBeforeSQL(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "policy-expansion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, "00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "matching-post"); err != nil {
		t.Fatal(err)
	}

	userDescriptor := golem.GeneratedModelDescriptor[expansionRuntimeUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[expansionRuntimePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[expansionRuntimeUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[expansionRuntimePost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[expansionRuntimeUser, expansionRuntimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[expansionRuntimePost, expansionRuntimeUser](fixture.PostAuthor, fixture.Authorship, fixture.User)

	open := func(userPolicy func(golem.TextField[expansionRuntimeUser, string], golem.ToMany[expansionRuntimeUser, expansionRuntimePost], golem.TextField[expansionRuntimePost, string]) golem.PolicyBinding[expansionRuntimeActor], postPolicy func(golem.TextField[expansionRuntimePost, string], golem.ToOne[expansionRuntimePost, expansionRuntimeUser], golem.TextField[expansionRuntimeUser, string]) golem.PolicyBinding[expansionRuntimeActor]) *App[struct{}, expansionRuntimeActor] {
		t.Helper()
		bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[expansionRuntimeActor]{
			userPolicy(userName, posts, postTitle),
			postPolicy(postTitle, author, userName),
		}, nil)
		bindings, buildErr := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		app, openErr := Open(ctx, Config[struct{}, expansionRuntimeActor]{
			Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
			ResolvePrincipal: func(context.Context, struct{}) (expansionRuntimeActor, error) { return expansionRuntimeActor{}, nil },
		})
		if openErr != nil {
			t.Fatal(openErr)
		}
		return app
	}
	userDependsOnMatchingPost := func(_ golem.TextField[expansionRuntimeUser, string], relation golem.ToMany[expansionRuntimeUser, expansionRuntimePost], title golem.TextField[expansionRuntimePost, string]) golem.PolicyBinding[expansionRuntimeActor] {
		return golem.GeneratedPolicyBinding[expansionRuntimeActor, expansionRuntimeUser](fixture.User, func(expansionRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[expansionRuntimeUser]()
			rules.CanRead(relation.Some(title.Contains("matching")))
			return rules.Freeze(fixture.User)
		})
	}
	postPolicy := func(allowMatching bool) func(golem.TextField[expansionRuntimePost, string], golem.ToOne[expansionRuntimePost, expansionRuntimeUser], golem.TextField[expansionRuntimeUser, string]) golem.PolicyBinding[expansionRuntimeActor] {
		return func(title golem.TextField[expansionRuntimePost, string], _ golem.ToOne[expansionRuntimePost, expansionRuntimeUser], _ golem.TextField[expansionRuntimeUser, string]) golem.PolicyBinding[expansionRuntimeActor] {
			return golem.GeneratedPolicyBinding[expansionRuntimeActor, expansionRuntimePost](fixture.Post, func(expansionRuntimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[expansionRuntimePost]()
				if allowMatching {
					rules.CanRead(title.Contains("matching"))
				} else {
					rules.CanRead(title.StartsWith("visible-"))
				}
				return rules.Freeze(fixture.Post)
			})
		}
	}
	countUsers := func(app *App[struct{}, expansionRuntimeActor]) (int64, error) {
		caller, callErr := app.ForPrincipal(ctx, struct{}{})
		if callErr != nil {
			return 0, callErr
		}
		return CallerCount(ctx, caller, userDescriptor)
	}

	// The physical matching post exists, but the target Post policy hides it.
	// The User row policy must therefore see no authorized matching relation.
	hiddenCount, err := countUsers(open(userDependsOnMatchingPost, postPolicy(false)))
	if err != nil {
		t.Fatalf("hidden read: %v: %v", err, errors.Unwrap(err))
	}
	if hiddenCount != 0 {
		t.Fatalf("target-policy-invisible post exposed %d user rows", hiddenCount)
	}

	visibleCount, err := countUsers(open(userDependsOnMatchingPost, postPolicy(true)))
	if err != nil {
		t.Fatalf("visible read: %v: %v", err, errors.Unwrap(err))
	}
	if visibleCount != 1 {
		t.Fatalf("target-policy-visible post produced %d user rows", visibleCount)
	}

	mutualPostPolicy := func(_ golem.TextField[expansionRuntimePost, string], relation golem.ToOne[expansionRuntimePost, expansionRuntimeUser], name golem.TextField[expansionRuntimeUser, string]) golem.PolicyBinding[expansionRuntimeActor] {
		return golem.GeneratedPolicyBinding[expansionRuntimeActor, expansionRuntimePost](fixture.Post, func(expansionRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[expansionRuntimePost]()
			rules.CanRead(relation.Is(name.Eq("owner")))
			return rules.Freeze(fixture.Post)
		})
	}
	cycleApp := open(userDependsOnMatchingPost, mutualPostPolicy)
	cycleCaller, err := cycleApp.ForPrincipal(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	// Closing the only database handle is an execution tripwire: a correctly
	// rejected policy cycle never reaches QueryContext. Any SQL attempt would
	// instead surface a database-closed execution failure.
	if err := closeExpansionDatabase(database); err != nil {
		t.Fatal(err)
	}
	count, err := CallerCount(ctx, cycleCaller, userDescriptor)
	var failure *golem.Error
	if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Message != "read is not permitted" {
		t.Fatalf("cycle count=%d error=%v", count, err)
	}
	if cause := errors.Unwrap(failure); cause == nil || !strings.Contains(cause.Error(), "cyclic relation policy") {
		t.Fatalf("cycle trusted cause=%v", cause)
	}
}

func closeExpansionDatabase(database *sqlx.DB) error {
	if database == nil {
		return errors.New("nil expansion database")
	}
	return database.Close()
}
