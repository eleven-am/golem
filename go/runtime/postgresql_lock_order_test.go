package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type lockOrderWriter struct {
	name string
	run  func(context.Context) error
}

type lockOrderOutcome struct {
	name string
	err  error
}

func deadlockDetected(err error) bool {
	var postgres *pgconn.PgError
	return errors.As(err, &postgres) && postgres.Code == "40P01"
}

func lockOrderMarker(qualifiedTable string) string {
	return strings.Trim(strings.SplitN(qualifiedTable, ".", 2)[0], `"`)
}

func lockWaiters(t testing.TB, database *sqlx.DB, marker string) int {
	t.Helper()
	var waiting int
	if err := database.Get(&waiting, `SELECT COUNT(*) FROM pg_catalog.pg_stat_activity WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' AND strpos(query, $1) > 0`, marker); err != nil {
		t.Fatal(err)
	}
	return waiting
}

func raceBehindHeldRows(t testing.TB, database *sqlx.DB, marker, table string, held []string, writers ...lockOrderWriter) []lockOrderOutcome {
	t.Helper()
	ctx := context.Background()
	gate, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range held {
		if _, err := gate.ExecContext(ctx, `SELECT 1 FROM `+table+` WHERE "id" = $1 FOR UPDATE`, id); err != nil {
			_ = gate.Rollback()
			t.Fatal(err)
		}
	}
	results := make(chan lockOrderOutcome, len(writers))
	for index, writer := range writers {
		writer := writer
		go func() {
			results <- lockOrderOutcome{name: writer.name, err: writer.run(ctx)}
		}()
		deadline := time.Now().Add(15 * time.Second)
		for lockWaiters(t, database, marker)+len(results) < index+1 {
			if time.Now().After(deadline) {
				_ = gate.Rollback()
				t.Fatalf("writer %q neither waited on a row lock nor finished", writer.name)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	outcomes := make([]lockOrderOutcome, 0, len(writers))
	for range writers {
		select {
		case outcome := <-results:
			outcomes = append(outcomes, outcome)
		case <-time.After(30 * time.Second):
			t.Fatal("concurrent writers did not finish")
		}
	}
	return outcomes
}

func assertSerializedOrOneConflict(t testing.TB, outcomes []lockOrderOutcome, tolerated map[string]golem.ErrorCode) {
	t.Helper()
	assertNoDeadlock(t, outcomes)
	conflicts, successes := 0, 0
	for _, outcome := range outcomes {
		if outcome.err == nil {
			successes++
			continue
		}
		var failure *golem.Error
		if !errors.As(outcome.err, &failure) {
			t.Errorf("%s failed without a public error: %v", outcome.name, outcome.err)
			continue
		}
		if failure.Code == golem.CodeConflict {
			conflicts++
			continue
		}
		if code, ok := tolerated[outcome.name]; !ok || code != failure.Code {
			t.Errorf("%s failed with %s: %v", outcome.name, failure.Code, outcome.err)
		}
	}
	if conflicts > 1 || successes == 0 {
		t.Errorf("outcomes %v: want the writers to complete serially or with exactly one CONFLICT", outcomes)
	}
}

func assertNoDeadlock(t testing.TB, outcomes []lockOrderOutcome) {
	t.Helper()
	for _, outcome := range outcomes {
		if deadlockDetected(outcome.err) {
			t.Errorf("%s was aborted by a PostgreSQL deadlock: %v", outcome.name, outcome.err)
		}
	}
}

func socialComment(fixture socialMutationFixture, id byte) golem.CreateInput[socialMutationComment] {
	return golem.GeneratedCreateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentPostID), golem.UUID{15: 50}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentAuthorID), golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "seed"))
}

func socialReply(fixture socialMutationFixture, parent byte, body string) golem.UpdateInput[socialMutationComment] {
	return golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.commentBody, body),
		golem.GeneratedNestedConnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplyTo, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(parent)))
}

func forEachPostgreSQLSocialProfile(t *testing.T, run func(*testing.T, socialMutationFixture)) {
	t.Helper()
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil)
			ctx := context.Background()
			fixture.seedUsers(t, 1)
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(50, 1, "thread")); err != nil {
				t.Fatal(err)
			}
			for _, comment := range []byte{70, 71} {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.commentDescriptor, socialComment(fixture, comment)); err != nil {
					t.Fatal(err)
				}
			}
			run(t, fixture)
		})
	}
}

func TestPostgreSQLOpposingUpdateManyCallsTakeRowLocksInOneOrder(t *testing.T) {
	forEachPostgreSQLSocialProfile(t, func(t *testing.T, fixture socialMutationFixture) {
		caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
		body := func(value string) golem.UpdateManyInput[socialMutationComment] {
			return golem.GeneratedUpdateManyInput[socialMutationComment](fixture.schema.Comment, golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.commentBody, value))
		}
		outcomes := raceBehindHeldRows(t, fixture.app.database, lockOrderMarker(comments), comments, []string{mutationResultUUIDText(70), mutationResultUUIDText(71)},
			lockOrderWriter{name: "updateMany 70,71", run: func(ctx context.Context) error {
				_, err := CallerUpdateMany(ctx, caller, fixture.commentDescriptor, fixture.commentID.In(golem.UUID{15: 70}, golem.UUID{15: 71}), body("a"))
				return err
			}},
			lockOrderWriter{name: "updateMany 71,70", run: func(ctx context.Context) error {
				_, err := CallerUpdateMany(ctx, caller, fixture.commentDescriptor, fixture.commentID.In(golem.UUID{15: 71}, golem.UUID{15: 70}), body("b"))
				return err
			}},
		)
		assertSerializedOrOneConflict(t, outcomes, nil)
	})
}

func TestPostgreSQLCascadeDeleteRacingAChildRelinkTakesRowLocksInOneOrder(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "lock_order_cascade", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), assertCascadeRacingRelink)
}

func TestPostgreSQLCascadeDeleteWhoseChildrenSortBeforeTheParentTakesRowLocksInOneOrder(t *testing.T) {
	allowAll := func(schemaFixture schematest.Fixture, config *Config[mutationResultPrincipal, mutationResultActor]) {
		users := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schemaFixture.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultUser]()
			rules.CanRead(golem.All[mutationResultUser]())
			rules.CanUpdate(golem.All[mutationResultUser]())
			rules.CanDelete(golem.All[mutationResultUser]())
			return rules.Freeze(schemaFixture.User)
		})
		posts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schemaFixture.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultPost]()
			rules.CanRead(golem.All[mutationResultPost]())
			rules.CanCreate(golem.All[mutationResultPost]())
			rules.CanUpdate(golem.All[mutationResultPost]())
			rules.CanDelete(golem.All[mutationResultPost]())
			return rules.Freeze(schemaFixture.Post)
		})
		bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{users, posts}, nil))
		if err != nil {
			panic(err)
		}
		config.Bindings = bindings
	}
	runConfiguredRelationDeleteProviderProfiles(t, "lock_child_first", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDeleteParentAfterChild(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeleteParentAfterChildPostgreSQLNamespaces(compilerir.ActionCascade), allowAll, assertCascadeRacingRelink)
}

func assertCascadeRacingRelink(t *testing.T, profile mutationProviderAcceptanceFixture) {
	if profile.provider != golem.PostgreSQL {
		t.Skip("row-lock order is a PostgreSQL property; SQLite serializes writers")
	}
	ctx, fixture := context.Background(), profile.fixture
	for _, post := range []byte{10, 12} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 2}, "child")); err != nil {
			t.Fatal(err)
		}
	}
	caller := mustMutationResultCaller(t, fixture)
	relink := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "relinked"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, cascadeUserTarget(fixture, 2)))
	postTarget := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 12}))
	outcomes := raceBehindHeldRows(t, fixture.app.database, lockOrderMarker(profile.posts), profile.posts, []string{mutationResultUUIDText(10)},
		lockOrderWriter{name: "cascade delete of the parent", run: func(ctx context.Context) error {
			_, err := CallerDelete(ctx, caller, fixture.userDescriptor, cascadeUserTarget(fixture, 2))
			return err
		}},
		lockOrderWriter{name: "update relinking a child to the parent", run: func(ctx context.Context) error {
			_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, postTarget, relink)
			return err
		}},
	)
	assertSerializedOrOneConflict(t, outcomes, map[string]golem.ErrorCode{"update relinking a child to the parent": golem.CodeNotFound})
}

func TestPostgreSQLOpposingSelfRelationLinksTakeRowLocksInOneOrder(t *testing.T) {
	forEachPostgreSQLSocialProfile(t, func(t *testing.T, fixture socialMutationFixture) {
		caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
		outcomes := raceBehindHeldRows(t, fixture.app.database, lockOrderMarker(comments), comments, []string{mutationResultUUIDText(70), mutationResultUUIDText(71)},
			lockOrderWriter{name: "link 70 to 71", run: func(ctx context.Context) error {
				_, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(70), socialReply(fixture, 71, "a"))
				return err
			}},
			lockOrderWriter{name: "link 71 to 70", run: func(ctx context.Context) error {
				_, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(71), socialReply(fixture, 70, "b"))
				return err
			}},
		)
		assertSerializedOrOneConflict(t, outcomes, nil)
	})
}

func TestPostgreSQLUpdateManyRacingAnOpposingLinkTakesRowLocksInOneOrder(t *testing.T) {
	forEachPostgreSQLSocialProfile(t, func(t *testing.T, fixture socialMutationFixture) {
		caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
		outcomes := raceBehindHeldRows(t, fixture.app.database, lockOrderMarker(comments), comments, []string{mutationResultUUIDText(70), mutationResultUUIDText(71)},
			lockOrderWriter{name: "updateMany 70,71", run: func(ctx context.Context) error {
				_, err := CallerUpdateMany(ctx, caller, fixture.commentDescriptor, fixture.commentID.In(golem.UUID{15: 70}, golem.UUID{15: 71}),
					golem.GeneratedUpdateManyInput[socialMutationComment](fixture.schema.Comment, golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.commentBody, "bulk")))
				return err
			}},
			lockOrderWriter{name: "link 71 to 70", run: func(ctx context.Context) error {
				_, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(71), socialReply(fixture, 70, "linked"))
				return err
			}},
		)
		assertSerializedOrOneConflict(t, outcomes, nil)
	})
}
