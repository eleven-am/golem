package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

const lockKeyRaceLimit = 3

const lockKeyFailFast = 500 * time.Millisecond

type lockKeyTx = SystemTx[graphMutationPrincipal, graphMutationActor]

func forEachPostgreSQLLockKeyFixture(t *testing.T, run func(*testing.T, socialMutationFixture)) {
	t.Helper()
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			run(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func testLockKey(t testing.TB, parts ...string) golem.LockKey {
	t.Helper()
	key, err := golem.NewLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()), parts...)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func lockKeyHeldElsewhere(t testing.TB, database *sqlx.DB, key golem.LockKey) bool {
	t.Helper()
	identity, err := golem.RuntimeLockKeyIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	class, hash := rowlock.AppLockID(rowlock.AppKey(identity))
	probe, err := database.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Rollback() }()
	var acquired bool
	if err := probe.Get(&acquired, `SELECT pg_catalog.pg_try_advisory_xact_lock($1, $2)`, class, hash); err != nil {
		t.Fatal(err)
	}
	return !acquired
}

func waitForLockKeyWaiter(database *sqlx.DB) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := database.Get(&waiting, `SELECT COUNT(*) FROM pg_catalog.pg_stat_activity WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' AND strpos(query, 'pg_advisory_xact_lock($1, $2)') > 0`); err != nil {
			return err
		}
		if waiting > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func holdInSystemTransaction(t *testing.T, fixture socialMutationFixture, hold func(context.Context, *lockKeyTx) error) func() {
	t.Helper()
	held, release, done := make(chan error, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- SystemTransaction(context.Background(), fixture.app.System(), func(transaction *lockKeyTx) error {
			err := hold(context.Background(), transaction)
			held <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(release)
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(finish)
	return finish
}

func holdLockKeys(t *testing.T, fixture socialMutationFixture, keys ...golem.LockKey) func() {
	t.Helper()
	return holdInSystemTransaction(t, fixture, func(ctx context.Context, transaction *lockKeyTx) error {
		return SystemTxLockKeys(ctx, transaction, keys...)
	})
}

func requireLockKeyConflict(t testing.TB, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeConflict {
		t.Fatalf("err=%v; want CONFLICT", err)
	}
}

func countCheckInsert(ctx context.Context, fixture socialMutationFixture, transaction *lockKeyTx, id byte, counted func()) error {
	count, err := SystemTxCount(ctx, transaction, fixture.userDescriptor)
	if err != nil {
		return err
	}
	counted()
	if count >= lockKeyRaceLimit {
		return nil
	}
	_, err = SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(id, "racer"))
	return err
}

func TestPostgreSQLCountCheckInsertRacesWithoutALockKey(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1, 2)
		var counted sync.WaitGroup
		counted.Add(2)
		errs := make(chan error, 2)
		for _, id := range []byte{10, 11} {
			go func() {
				errs <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
					return countCheckInsert(ctx, fixture, transaction, id, func() {
						counted.Done()
						counted.Wait()
					})
				})
			}()
		}
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if got := socialCount(t, fixture, fixture.schema.User); got != lockKeyRaceLimit+1 {
			t.Fatalf("users=%d; want the unlocked race to overshoot to %d", got, lockKeyRaceLimit+1)
		}
	})
}

func TestPostgreSQLLockKeySerializesCountCheckInsert(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1, 2)
		key := testLockKey(t, "users")
		firstCounted := make(chan struct{})
		errs := make(chan error, 3)
		go func() {
			errs <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
					return err
				}
				return countCheckInsert(ctx, fixture, transaction, 10, func() {
					close(firstCounted)
					if err := waitForLockKeyWaiter(fixture.app.database); err != nil {
						errs <- fmt.Errorf("the second transaction never waited on the lock key: %w", err)
					}
				})
			})
		}()
		go func() {
			<-firstCounted
			errs <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
					return err
				}
				return countCheckInsert(ctx, fixture, transaction, 11, func() {})
			})
		}()
		for range 2 {
			select {
			case err := <-errs:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the locked transactions did not finish")
			}
		}
		if got := socialCount(t, fixture, fixture.schema.User); got != lockKeyRaceLimit {
			t.Fatalf("users=%d; want the lock key to stop the count at %d", got, lockKeyRaceLimit)
		}
	})
}

func TestPostgreSQLLockKeyTakenAfterARowLockFailsFastWithConflict(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1)
		key := testLockKey(t, "after-row")
		release := holdLockKeys(t, fixture, key)
		var lockErr error
		var elapsed time.Duration
		if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
			if _, err := SystemTxUpdate(ctx, transaction, fixture.userDescriptor, fixture.userTarget(1), golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "row-first"))); err != nil {
				return err
			}
			started := time.Now()
			lockErr = SystemTxLockKeys(ctx, transaction, key)
			elapsed = time.Since(started)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		release()
		requireLockKeyConflict(t, lockErr)
		var contention *rowlock.ContentionError
		if !errors.As(lockErr, &contention) {
			t.Fatalf("err=%v; want the ledger's contention cause", lockErr)
		}
		if elapsed > lockKeyFailFast {
			t.Fatalf("an out-of-order lock key took %s; want NOWAIT", elapsed)
		}
	})
}

func TestPostgreSQLLockKeysInOppositeCallOrdersBothSucceed(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		for iteration := range 10 {
			first, second := testLockKey(t, "a", fmt.Sprint(iteration)), testLockKey(t, "b", fmt.Sprint(iteration))
			var start sync.WaitGroup
			start.Add(2)
			errs := make(chan error, 2)
			for _, keys := range [][]golem.LockKey{{first, second}, {second, first}} {
				go func() {
					errs <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
						start.Done()
						start.Wait()
						if err := SystemTxLockKeys(ctx, transaction, keys...); err != nil {
							return err
						}
						time.Sleep(10 * time.Millisecond)
						return nil
					})
				}()
			}
			for range 2 {
				select {
				case err := <-errs:
					if err != nil {
						t.Fatalf("iteration %d: %v", iteration, err)
					}
				case <-time.After(30 * time.Second):
					t.Fatalf("iteration %d: the transactions did not finish", iteration)
				}
			}
		}
	})
}

func TestPostgreSQLLockKeyIsOrderedAfterUpsertGuards(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1)
		rename := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "upserted"))
		key := testLockKey(t, "guard")
		if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
			if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
				return err
			}
			_, err := SystemTxUpsert(ctx, transaction, fixture.userDescriptor, fixture.userTarget(5), fixture.userCreate(5, "upserted"), rename)
			return err
		}); err != nil {
			t.Fatalf("an upsert after a lock key in an uncontended transaction failed: %v", err)
		}
		release := holdInSystemTransaction(t, fixture, func(ctx context.Context, transaction *lockKeyTx) error {
			_, err := SystemTxUpsert(ctx, transaction, fixture.userDescriptor, fixture.userTarget(6), fixture.userCreate(6, "holder"), rename)
			return err
		})
		contended := testLockKey(t, "guard-contended")
		outcome := make(chan error, 1)
		go func() {
			var upsertErr error
			err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if err := SystemTxLockKeys(ctx, transaction, contended); err != nil {
					return err
				}
				_, upsertErr = SystemTxUpsert(ctx, transaction, fixture.userDescriptor, fixture.userTarget(6), fixture.userCreate(6, "contender"), rename)
				return nil
			})
			outcome <- errors.Join(err, upsertErr)
		}()
		select {
		case err := <-outcome:
			release()
			requireLockKeyConflict(t, err)
		case <-time.After(5 * time.Second):
			release()
			t.Fatal("an upsert guard taken after a lock key waited instead of failing fast")
		}
		reversed := testLockKey(t, "guard-reversed")
		releaseKey := holdLockKeys(t, fixture, reversed)
		reversedOutcome := make(chan error, 1)
		go func() {
			var lockErr error
			err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if _, err := SystemTxUpsert(ctx, transaction, fixture.userDescriptor, fixture.userTarget(1), fixture.userCreate(1, "guard-first"), rename); err != nil {
					return err
				}
				lockErr = SystemTxLockKeys(ctx, transaction, reversed)
				return nil
			})
			reversedOutcome <- errors.Join(err, lockErr)
		}()
		select {
		case err := <-reversedOutcome:
			releaseKey()
			requireLockKeyConflict(t, err)
		case <-time.After(5 * time.Second):
			releaseKey()
			t.Fatal("a lock key taken after an upsert locked its row waited instead of failing fast")
		}
	})
}

func TestPostgreSQLSavepointRollbackReleasesALockKey(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		key := testLockKey(t, "savepoint")
		rolledBack := errors.New("roll back to the savepoint")
		if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
			_, err := withinHookWriteSavepoint(ctx, transaction.system.executor, func() (struct{}, error) {
				if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
					return struct{}{}, err
				}
				if !lockKeyHeldElsewhere(t, fixture.app.database, key) {
					t.Error("the lock key was not held inside the savepoint")
				}
				return struct{}{}, rolledBack
			})
			if !errors.Is(err, rolledBack) {
				return fmt.Errorf("savepoint err=%v; want the rollback sentinel", err)
			}
			if lockKeyHeldElsewhere(t, fixture.app.database, key) {
				t.Error("the lock key survived the rollback to its savepoint")
			}
			if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
				return err
			}
			if !lockKeyHeldElsewhere(t, fixture.app.database, key) {
				t.Error("the ledger still recorded a lock key the savepoint released, so it was not taken again")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPostgreSQLCancelledLockKeyWaitIsAConflict(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		key := testLockKey(t, "cancel")
		release := holdLockKeys(t, fixture, key)
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		outcome := make(chan error, 1)
		go func() {
			outcome <- SystemTransaction(context.Background(), fixture.app.System(), func(transaction *lockKeyTx) error {
				return SystemTxLockKeys(ctx, transaction, key)
			})
		}()
		if err := waitForLockKeyWaiter(fixture.app.database); err != nil {
			t.Fatalf("the lock key never waited: %v", err)
		}
		cancel()
		select {
		case err := <-outcome:
			requireLockKeyConflict(t, err)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v; want it to wrap context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a cancelled lock key wait did not return")
		}
	})
}

func TestPostgreSQLLockKeyDeadlockWithAnUntrackedWaitIsAConflict(t *testing.T) {
	forEachPostgreSQLLockKeyFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		key := testLockKey(t, "deadlock")
		inserted, keyHeld := make(chan struct{}), make(chan struct{})
		first, second := make(chan error, 1), make(chan error, 1)
		var lockErr error
		go func() {
			first <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if _, err := transaction.system.executor.transaction.ExecContext(ctx, `SET LOCAL deadlock_timeout = '1s'`); err != nil {
					return err
				}
				if _, err := SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(9, "first")); err != nil {
					return err
				}
				close(inserted)
				<-keyHeld
				lockErr = SystemTxLockKeys(ctx, transaction, key)
				return lockErr
			})
		}()
		go func() {
			second <- SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
				if _, err := transaction.system.executor.transaction.ExecContext(ctx, `SET LOCAL deadlock_timeout = '10s'`); err != nil {
					return err
				}
				<-inserted
				if err := SystemTxLockKeys(ctx, transaction, key); err != nil {
					return err
				}
				close(keyHeld)
				if err := waitForLockKeyWaiter(fixture.app.database); err != nil {
					return fmt.Errorf("the first transaction never waited on the lock key: %w", err)
				}
				_, err := SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(9, "second"))
				return err
			})
		}()
		var firstErr, secondErr error
		for range 2 {
			select {
			case firstErr = <-first:
			case secondErr = <-second:
			case <-time.After(20 * time.Second):
				t.Fatal("the deadlocked transactions did not finish")
			}
		}
		if secondErr != nil {
			t.Fatalf("the surviving transaction failed: %v", secondErr)
		}
		requireLockKeyConflict(t, lockErr)
		requireLockKeyConflict(t, firstErr)
		if !deadlockDetected(lockErr) {
			t.Fatalf("err=%v; want PostgreSQL's deadlock as the cause", lockErr)
		}
	})
}
