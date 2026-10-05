package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
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

func awaitLockWaiters(t testing.TB, database *sqlx.DB, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := database.Get(&waiting, `SELECT COUNT(*) FROM pg_catalog.pg_stat_activity WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' AND strpos(query, $1) > 0`, marker); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d writers are waiting on row locks, want %d", waiting, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
		awaitLockWaiters(t, database, marker, index+1)
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
		assertNoDeadlock(t, outcomes)
		for _, outcome := range outcomes {
			if outcome.err != nil {
				t.Errorf("%s: %v", outcome.name, outcome.err)
			}
		}
	})
}
