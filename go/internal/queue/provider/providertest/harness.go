// Package providertest contains the provider-neutral durable job store
// conformance gates. Provider packages invoke them against live databases; the
// harness never computes expected state through provider SQL.
package providertest

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

// Fixture is one isolated live store and the database it owns, supplied by a
// provider package.
type Fixture struct {
	Store    queueprovider.Store
	Database *sqlx.DB
}

const (
	longLease  = 5 * time.Minute
	shortLease = 80 * time.Millisecond
	expiry     = 200 * time.Millisecond
)

// ClaimIsExclusiveUnderConcurrency proves concurrent claimers over one backlog
// lease every job exactly once.
func ClaimIsExclusiveUnderConcurrency(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	const backlog = 40
	const claimers = 4
	identities := make(map[string]struct{}, backlog)
	for index := 0; index < backlog; index++ {
		identities[enqueue(t, fixture, "gate.claim", request{})] = struct{}{}
	}
	var claimed sync.Map
	var total atomic.Int64
	var failures sync.Map
	deadline := time.Now().Add(15 * time.Second)
	var group sync.WaitGroup
	for claimer := 0; claimer < claimers; claimer++ {
		group.Add(1)
		go func(claimer int) {
			defer group.Done()
			for total.Load() < backlog && time.Now().Before(deadline) {
				records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.claim"}, Limit: 3, LeaseDuration: longLease})
				if err != nil {
					failures.Store(claimer, err)
					return
				}
				for _, record := range records {
					if _, duplicate := claimed.LoadOrStore(record.ID, record.LeaseToken); duplicate {
						failures.Store(claimer, errors.New("job "+record.ID+" was leased twice"))
						return
					}
					total.Add(1)
				}
			}
		}(claimer)
	}
	group.Wait()
	failures.Range(func(_, value any) bool {
		t.Fatalf("concurrent claim: %v", value)
		return false
	})
	if total.Load() != backlog {
		t.Fatalf("leased %d of %d jobs exactly once", total.Load(), backlog)
	}
	for identity := range identities {
		record := inspect(t, fixture, identity)
		if record.State != queueprovider.StateLeased || record.AttemptCount != 1 {
			t.Fatalf("job %s ended state=%s attempts=%d", identity, record.State, record.AttemptCount)
		}
	}
}

// StaleTokenCannotTransition proves every transition is fenced on the lease
// token that claimed the row.
func StaleTokenCannotTransition(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	identity := enqueue(t, fixture, "gate.fence", request{})
	stale := claimOne(t, fixture, "gate.fence", shortLease)
	time.Sleep(expiry)
	fresh := claimOne(t, fixture, "gate.fence", longLease)
	if fresh.ID != identity || fresh.LeaseToken == stale.LeaseToken {
		t.Fatalf("reclaim=%#v", fresh)
	}
	transitions := []struct {
		name string
		call func() (bool, error)
	}{
		{name: "succeed", call: func() (bool, error) { return fixture.Store.Succeed(ctx, identity, stale.LeaseToken, "stale") }},
		{name: "fail", call: func() (bool, error) { return fixture.Store.Fail(ctx, identity, stale.LeaseToken, "stale") }},
		{name: "retry", call: func() (bool, error) { return fixture.Store.RetryAt(ctx, identity, stale.LeaseToken, 0, "stale") }},
		{name: "cancel", call: func() (bool, error) { return fixture.Store.MarkCanceled(ctx, identity, stale.LeaseToken, "stale") }},
		{name: "release", call: func() (bool, error) { return fixture.Store.Release(ctx, identity, stale.LeaseToken) }},
	}
	for _, transition := range transitions {
		changed, err := transition.call()
		if err != nil || changed {
			t.Fatalf("stale %s changed=%t error=%v", transition.name, changed, err)
		}
		record := inspect(t, fixture, identity)
		if record.State != queueprovider.StateLeased || record.LeaseToken != fresh.LeaseToken || record.LastCode != "" {
			t.Fatalf("stale %s mutated %#v", transition.name, record)
		}
	}
	renewal, err := fixture.Store.Renew(ctx, identity, stale.LeaseToken, longLease)
	if err != nil || renewal.Renewed {
		t.Fatalf("stale renew=%#v error=%v", renewal, err)
	}
	if renewal, err := fixture.Store.Renew(ctx, identity, fresh.LeaseToken, longLease); err != nil || !renewal.Renewed || renewal.CancelRequested {
		t.Fatalf("owner renew=%#v error=%v", renewal, err)
	}
}

// ExpiredLeaseIsReclaimedByOrdinaryClaim proves crashed-worker recovery is the
// ordinary claim predicate rather than a separate path.
func ExpiredLeaseIsReclaimedByOrdinaryClaim(t testing.TB, fixture Fixture) {
	t.Helper()
	identity := enqueue(t, fixture, "gate.recover", request{})
	stranded := claimOne(t, fixture, "gate.recover", shortLease)
	if stranded.AttemptCount != 1 {
		t.Fatalf("first claim attempts=%d", stranded.AttemptCount)
	}
	if records := claim(t, fixture, "gate.recover", longLease, 5); len(records) != 0 {
		t.Fatalf("live lease was reclaimed: %#v", records)
	}
	time.Sleep(expiry)
	reclaimed := claimOne(t, fixture, "gate.recover", longLease)
	if reclaimed.ID != identity || reclaimed.AttemptCount != 2 || reclaimed.LeaseToken == stranded.LeaseToken {
		t.Fatalf("reclaimed=%#v", reclaimed)
	}
}

// ExclusiveKeyBlocksOnlyLiveHolders proves the three-way liveness rule: an
// unexpired holder blocks, a stranded expired row does not, and a candidate is
// never its own blocker.
func ExclusiveKeyBlocksOnlyLiveHolders(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	holding := enqueue(t, fixture, "gate.hold.a", request{exclusive: "shared-key"})
	waiting := enqueue(t, fixture, "gate.hold.b", request{exclusive: "shared-key"})
	live := claimOne(t, fixture, "gate.hold.a", longLease)
	if records := claim(t, fixture, "gate.hold.b", longLease, 5); len(records) != 0 {
		t.Fatalf("live holder failed to block: %#v", records)
	}
	if changed, err := fixture.Store.Release(ctx, live.ID, live.LeaseToken); err != nil || !changed {
		t.Fatalf("release changed=%t error=%v", changed, err)
	}
	stranded := claimOne(t, fixture, "gate.hold.a", shortLease)
	time.Sleep(expiry)
	released := claimOne(t, fixture, "gate.hold.b", longLease)
	if released.ID != waiting {
		t.Fatalf("stranded holder froze the key: %#v", released)
	}
	if changed, err := fixture.Store.Release(ctx, released.ID, released.LeaseToken); err != nil || !changed {
		t.Fatalf("release changed=%t error=%v", changed, err)
	}
	self := claimOne(t, fixture, "gate.hold.a", longLease)
	if self.ID != holding || self.LeaseToken == stranded.LeaseToken || self.AttemptCount != 3 {
		t.Fatalf("candidate blocked itself: %#v", self)
	}
}

// ExclusiveKeyNeverDoubleLeasesUnderRace proves concurrent claimers never lease
// two jobs sharing one exclusivity key.
func ExclusiveKeyNeverDoubleLeasesUnderRace(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	const claimers = 4
	for round := 0; round < 8; round++ {
		key, err := queueprovider.NewIdentifier()
		if err != nil {
			t.Fatal(err)
		}
		jobType := "gate.race." + strconv.Itoa(round)
		siblings := []string{
			enqueue(t, fixture, jobType, request{exclusive: key}),
			enqueue(t, fixture, jobType, request{exclusive: key}),
		}
		var leased atomic.Int64
		var failure atomic.Value
		start := make(chan struct{})
		var group sync.WaitGroup
		for claimer := 0; claimer < claimers; claimer++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{jobType}, Limit: 5, LeaseDuration: longLease})
				if err != nil {
					failure.Store(err)
					return
				}
				leased.Add(int64(len(records)))
			}()
		}
		close(start)
		group.Wait()
		if stored := failure.Load(); stored != nil {
			t.Fatalf("exclusive race claim: %v", stored)
		}
		if leased.Load() != 1 {
			t.Fatalf("round %d leased %d jobs sharing one key", round, leased.Load())
		}
		live := 0
		for _, sibling := range siblings {
			record := inspect(t, fixture, sibling)
			if record.State == queueprovider.StateLeased {
				live++
			}
		}
		if live != 1 {
			t.Fatalf("round %d holds %d live leases on one key", round, live)
		}
	}
}

// DedupeCoalescesActiveAndReleasesOnTerminal proves the partial unique index
// binds active work only.
func DedupeCoalescesActiveAndReleasesOnTerminal(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	first := enqueue(t, fixture, "gate.dedupe", request{dedupe: "coalesce-key"})
	second := enqueue(t, fixture, "gate.dedupe", request{dedupe: "coalesce-key"})
	if second != first {
		t.Fatalf("active duplicate created %s beside %s", second, first)
	}
	leased := claimOne(t, fixture, "gate.dedupe", longLease)
	coalesced := enqueue(t, fixture, "gate.dedupe", request{dedupe: "coalesce-key"})
	if coalesced != first {
		t.Fatalf("leased duplicate created %s beside %s", coalesced, first)
	}
	if changed, err := fixture.Store.Succeed(ctx, leased.ID, leased.LeaseToken, "done"); err != nil || !changed {
		t.Fatalf("succeed changed=%t error=%v", changed, err)
	}
	terminal := inspect(t, fixture, first)
	if terminal.State != queueprovider.StateSucceeded || terminal.DedupeKey != "coalesce-key" || terminal.FinishedAt == nil {
		t.Fatalf("terminal=%#v", terminal)
	}
	successor := enqueue(t, fixture, "gate.dedupe", request{dedupe: "coalesce-key"})
	if successor == first {
		t.Fatalf("terminal row still bound the dedupe key")
	}
}

func EnqueueReportsInsertedAndCoalescedState(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	firstRequest := newRequest(t, "gate.enqueue-result", request{dedupe: "enqueue-result-key"})
	first, err := fixture.Store.Enqueue(ctx, nil, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Inserted || first.ID != firstRequest.ID || first.State != queueprovider.StatePending {
		t.Fatalf("insert result=%#v", first)
	}
	pending, err := fixture.Store.Enqueue(ctx, nil, newRequest(t, "gate.enqueue-result", request{dedupe: "enqueue-result-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if pending.Inserted || pending.ID != first.ID || pending.State != queueprovider.StatePending {
		t.Fatalf("pending collision result=%#v", pending)
	}
	leased := claimOne(t, fixture, "gate.enqueue-result", longLease)
	collision, err := fixture.Store.Enqueue(ctx, nil, newRequest(t, "gate.enqueue-result", request{dedupe: "enqueue-result-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if collision.Inserted || collision.ID != first.ID || collision.State != queueprovider.StateLeased {
		t.Fatalf("leased collision result=%#v", collision)
	}
	if changed, err := fixture.Store.Succeed(ctx, leased.ID, leased.LeaseToken, "done"); err != nil || !changed {
		t.Fatalf("succeed changed=%t error=%v", changed, err)
	}
}

// TransactionalEnqueueIsAtomicWithCallerTransaction proves the insert runs on
// the caller's executor rather than escaping to the pool.
func TransactionalEnqueueIsAtomicWithCallerTransaction(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	rolled := newRequest(t, "gate.tx", request{})
	transaction, err := fixture.Database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Enqueue(ctx, transaction, rolled); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Inspect(ctx, rolled.ID); !errors.Is(err, queueprovider.ErrNotFound) {
		t.Fatalf("rolled-back enqueue survived: %v", err)
	}
	committed := newRequest(t, "gate.tx", request{})
	transaction, err = fixture.Database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Enqueue(ctx, transaction, committed); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed := claimOne(t, fixture, "gate.tx", longLease)
	if claimed.ID != committed.ID {
		t.Fatalf("committed enqueue is not claimable: %#v", claimed)
	}
}

type request struct {
	dedupe    string
	exclusive string
	delay     time.Duration
}

func newRequest(t testing.TB, jobType string, options request) queueprovider.EnqueueRequest {
	t.Helper()
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	return queueprovider.EnqueueRequest{
		ID: identity, Type: jobType, Payload: []byte(`{"gate":true}`), MaxAttempts: 5,
		Delay: options.delay, DedupeKey: options.dedupe, ExclusiveKey: options.exclusive,
	}
}

func enqueue(t testing.TB, fixture Fixture, jobType string, options request) string {
	t.Helper()
	stored, err := fixture.Store.Enqueue(context.Background(), nil, newRequest(t, jobType, options))
	if err != nil {
		t.Fatal(err)
	}
	return stored.ID
}

func claim(t testing.TB, fixture Fixture, jobType string, lease time.Duration, limit int) []queueprovider.Record {
	t.Helper()
	records, err := fixture.Store.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{jobType}, Limit: limit, LeaseDuration: lease})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func claimOne(t testing.TB, fixture Fixture, jobType string, lease time.Duration) queueprovider.Record {
	t.Helper()
	records := claim(t, fixture, jobType, lease, 1)
	if len(records) != 1 {
		t.Fatalf("claim of %s returned %d jobs", jobType, len(records))
	}
	return records[0]
}

func inspect(t testing.TB, fixture Fixture, identity string) queueprovider.Record {
	t.Helper()
	record, err := fixture.Store.Inspect(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func IdentityBoundIsOwnedByTheQueueContract(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	if err := fixture.Store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	longest := strings.Repeat("a", queueprovider.MaximumIdentityBytes)
	overlong := strings.Repeat("a", queueprovider.MaximumIdentityBytes+1)

	if _, err := fixture.Store.Cancel(ctx, overlong); err == nil {
		t.Errorf("Cancel accepted a %d-byte identity beyond the %d-byte contract bound", len(overlong), queueprovider.MaximumIdentityBytes)
	}
	if _, err := fixture.Store.Requeue(ctx, overlong); err == nil {
		t.Errorf("Requeue accepted a %d-byte identity beyond the %d-byte contract bound", len(overlong), queueprovider.MaximumIdentityBytes)
	}
	if _, err := fixture.Store.Release(ctx, overlong, longest); err == nil {
		t.Errorf("Release accepted a %d-byte identity beyond the %d-byte contract bound", len(overlong), queueprovider.MaximumIdentityBytes)
	}
	if _, err := fixture.Store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: overlong, Type: "gate.identity", Payload: []byte(`{}`), MaxAttempts: 1}); err == nil {
		t.Errorf("Enqueue accepted a %d-byte identity beyond the %d-byte contract bound", len(overlong), queueprovider.MaximumIdentityBytes)
	}

	if _, err := fixture.Store.Cancel(ctx, longest); err != nil {
		t.Errorf("Cancel rejected a %d-byte identity the contract permits: %v", len(longest), err)
	}
	if _, err := fixture.Store.Requeue(ctx, longest); err != nil {
		t.Errorf("Requeue rejected a %d-byte identity the contract permits: %v", len(longest), err)
	}
	if _, err := fixture.Store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: longest, Type: "gate.identity", Payload: []byte(`{}`), MaxAttempts: 1}); err != nil {
		t.Errorf("Enqueue rejected a %d-byte identity the contract permits: %v", len(longest), err)
	}
}
