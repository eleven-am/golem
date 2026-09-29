package postgresql

import (
	"context"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type postgresqlClaimOutcome struct {
	ids []string
	err error
}

func seedPostgreSQLClaimRace(t *testing.T, variable, namespace string) (*queueStore, *sqlx.DB) {
	t.Helper()
	store, database := openClaimNamespaceAt(t, variable, namespace)
	if _, err := database.Exec(`INSERT INTO ` + store.table() + ` ("id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","enqueued_at","updated_at") VALUES
		('a','type0','\x00'::bytea,'pending',0,5,clock_timestamp()-interval '2 minute',NULL,NULL,clock_timestamp()-interval '2 minute',clock_timestamp()),
		('b','type0','\x00'::bytea,'leased',1,5,clock_timestamp()-interval '1 minute','token-b',clock_timestamp()-interval '1 minute',clock_timestamp()-interval '2 minute',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	return store, database
}

func claimPostgreSQLIDs(store *queueStore, limit int) postgresqlClaimOutcome {
	records, err := store.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"type0"}, Limit: limit, LeaseDuration: time.Minute})
	ids := make([]string, len(records))
	for index, record := range records {
		ids[index] = record.ID
	}
	return postgresqlClaimOutcome{ids: ids, err: err}
}

func postgresqlClaimProfiles(t *testing.T, run func(*testing.T, string)) {
	for _, profile := range []struct{ name, variable string }{{"c", testenv.PostgreSQLDSNVariable}, {"linguistic", testenv.LinguisticDSNVariable}} {
		t.Run(profile.name, func(t *testing.T) { run(t, profile.variable) })
	}
}

func TestPostgreSQLClaimLocksOnlyTheJobsItReturns(t *testing.T) {
	postgresqlClaimProfiles(t, func(t *testing.T, variable string) {
		ctx := context.Background()
		store, database := seedPostgreSQLClaimRace(t, variable, "queue_claim_locks")
		if _, err := database.Exec(`CREATE FUNCTION "queue_claim_locks"."hold_lease"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(4242); RETURN NEW; END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TRIGGER "hold_lease" BEFORE UPDATE ON ` + store.table() + ` FOR EACH ROW EXECUTE FUNCTION "queue_claim_locks"."hold_lease"()`); err != nil {
			t.Fatal(err)
		}
		gate, err := database.Connx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_lock(4242)`); err != nil {
			t.Fatal(err)
		}
		released := false
		release := func() {
			if !released {
				released = true
				if _, err := gate.ExecContext(ctx, `SELECT pg_advisory_unlock(4242)`); err != nil {
					t.Error(err)
				}
			}
		}
		defer release()
		waiters := func() int {
			var count int
			if err := database.Get(&count, `SELECT COUNT(*) FROM pg_locks WHERE "locktype"='advisory' AND NOT "granted"`); err != nil {
				t.Fatal(err)
			}
			return count
		}
		first, second := make(chan postgresqlClaimOutcome, 1), make(chan postgresqlClaimOutcome, 1)
		go func() { first <- claimPostgreSQLIDs(store, 1) }()
		deadline := time.Now().Add(10 * time.Second)
		for waiters() < 1 {
			if time.Now().After(deadline) {
				t.Fatal("the first worker never reached its lease update")
			}
			time.Sleep(10 * time.Millisecond)
		}
		go func() { second <- claimPostgreSQLIDs(store, 1) }()
		for waiters() < 2 {
			select {
			case outcome := <-second:
				release()
				<-first
				t.Fatalf("a second worker came back with %v err=%v while the first worker held only job a, want [b]", outcome.ids, outcome.err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("the second worker neither claimed nor finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
		release()
		firstOutcome, secondOutcome := <-first, <-second
		if firstOutcome.err != nil || len(firstOutcome.ids) != 1 || firstOutcome.ids[0] != "a" {
			t.Fatalf("first worker claimed %v err=%v, want [a]", firstOutcome.ids, firstOutcome.err)
		}
		if secondOutcome.err != nil || len(secondOutcome.ids) != 1 || secondOutcome.ids[0] != "b" {
			t.Fatalf("second worker claimed %v err=%v, want [b]", secondOutcome.ids, secondOutcome.err)
		}
	})
}

func TestPostgreSQLClaimLooksPastJobsAnotherWorkerHolds(t *testing.T) {
	postgresqlClaimProfiles(t, func(t *testing.T, variable string) {
		store, database := seedPostgreSQLClaimRace(t, variable, "queue_claim_skips")
		holder, err := database.Beginx()
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Rollback()
		var held string
		if err := holder.Get(&held, `SELECT "id" FROM `+store.table()+` WHERE "id"='a' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 2} {
			outcome := claimPostgreSQLIDs(store, limit)
			if outcome.err != nil || len(outcome.ids) != 1 || outcome.ids[0] != "b" {
				t.Fatalf("limit %d claimed %v err=%v while another worker held a, want [b]", limit, outcome.ids, outcome.err)
			}
			if _, err := database.Exec(`UPDATE ` + store.table() + ` SET "status"='leased',"lease_token"='token-b',"lease_until"=clock_timestamp()-interval '1 minute',"available_at"=clock_timestamp()-interval '1 minute' WHERE "id"='b'`); err != nil {
				t.Fatal(err)
			}
		}
		if err := holder.Rollback(); err != nil {
			t.Fatal(err)
		}
		outcome := claimPostgreSQLIDs(store, 2)
		if outcome.err != nil || len(outcome.ids) != 2 || outcome.ids[0] != "a" || outcome.ids[1] != "b" {
			t.Fatalf("after release claimed %v err=%v, want [a b]", outcome.ids, outcome.err)
		}
	})
}
