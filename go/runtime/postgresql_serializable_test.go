package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/jackc/pgx/v5/pgconn"
)

func installCommitTimeFailure(t *testing.T, fixture socialMutationFixture, sqlState string) {
	t.Helper()
	name := fmt.Sprintf("golem_commit_failure_%d", time.Now().UnixNano())
	table := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	statements := []string{
		`CREATE FUNCTION public.` + name + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit-time failure' USING ERRCODE = '` + sqlState + `'; END $$`,
		`CREATE CONSTRAINT TRIGGER ` + name + ` AFTER INSERT ON ` + table + ` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.` + name + `()`,
	}
	t.Cleanup(func() {
		_, _ = fixture.app.database.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS `+name+` ON `+table)
		_, _ = fixture.app.database.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS public.`+name+`()`)
	})
	for _, statement := range statements {
		if _, err := fixture.app.database.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgreSQLCommitTimeSerializationFailureIsAConflict(t *testing.T) {
	for _, sqlState := range []string{"40001", "40P01"} {
		t.Run(sqlState, func(t *testing.T) {
			forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
				ctx := context.Background()
				installCommitTimeFailure(t, fixture, sqlState)
				err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
					_, err := SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(1, "deferred"))
					return err
				})
				requireLockKeyConflict(t, err)
				var postgres *pgconn.PgError
				if !errors.As(err, &postgres) || postgres.Code != sqlState {
					t.Fatalf("err=%v; want the provider cause %s preserved", err, sqlState)
				}
				if got := socialCount(t, fixture, fixture.schema.User); got != 0 {
					t.Fatalf("users=%d; want the failed commit to leave none", got)
				}
			})
		})
	}
}

func transactionIsolation(t *testing.T, ctx context.Context, transaction *lockKeyTx) string {
	t.Helper()
	var level string
	if err := transaction.system.executor.transaction.GetContext(ctx, &level, `SHOW transaction_isolation`); err != nil {
		t.Fatal(err)
	}
	return level
}

func TestPostgreSQLIsolationOptionSetsTheTransactionLevel(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		for _, run := range []struct {
			options []golem.TransactionOption
			want    string
		}{
			{nil, "read committed"},
			{[]golem.TransactionOption{golem.Isolation(golem.ReadCommitted)}, "read committed"},
			{[]golem.TransactionOption{golem.Isolation(golem.Serializable)}, "serializable"},
		} {
			var got string
			if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				got = transactionIsolation(t, ctx, transaction)
				return nil
			}, run.options...); err != nil {
				t.Fatal(err)
			}
			if got != run.want {
				t.Fatalf("options=%v isolation=%q; want %q", run.options, got, run.want)
			}
		}
	})
}

func raceSerializableCountCheckInsert(t *testing.T, fixture socialMutationFixture, options ...golem.TransactionOption) ([]error, int64) {
	t.Helper()
	ctx := context.Background()
	var counted sync.WaitGroup
	counted.Add(2)
	var calls atomic.Int64
	errs := make(chan error, 2)
	for _, id := range []byte{10, 11} {
		go func() {
			first := true
			errs <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				calls.Add(1)
				return countCheckInsert(ctx, fixture, transaction, id, func() {
					if first {
						first = false
						counted.Done()
						counted.Wait()
					}
				})
			}, options...)
		}()
	}
	results := make([]error, 0, 2)
	for range 2 {
		select {
		case err := <-errs:
			results = append(results, err)
		case <-time.After(60 * time.Second):
			t.Fatal("the serializable transactions did not finish")
		}
	}
	return results, calls.Load()
}

func TestPostgreSQLSerializableCountCheckInsertFailsOneTransactionWithConflict(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		fixture.seedUsers(t, 1, 2)
		results, calls := raceSerializableCountCheckInsert(t, fixture, golem.Isolation(golem.Serializable))
		failures := 0
		for _, err := range results {
			if err != nil {
				requireLockKeyConflict(t, err)
				failures++
			}
		}
		if failures != 1 || calls != 2 {
			t.Fatalf("results=%v calls=%d; want exactly one CONFLICT and no replay", results, calls)
		}
		if got := socialCount(t, fixture, fixture.schema.User); got != lockKeyRaceLimit {
			t.Fatalf("users=%d; want serializable isolation to stop the count at %d", got, lockKeyRaceLimit)
		}
	})
}

func TestPostgreSQLSerializableCountCheckInsertSucceedsUnderRetryConflicts(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		fixture.seedUsers(t, 1, 2)
		results, calls := raceSerializableCountCheckInsert(t, fixture, golem.Isolation(golem.Serializable), golem.RetryConflicts(3))
		for _, err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		if calls < 3 {
			t.Fatalf("calls=%d; want the conflicted transaction replayed", calls)
		}
		if got := socialCount(t, fixture, fixture.schema.User); got != lockKeyRaceLimit {
			t.Fatalf("users=%d; want the retried count to stop at %d", got, lockKeyRaceLimit)
		}
	})
}
