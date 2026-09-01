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

// SharedResourceCapacityIsAtomicAndWeighted proves a named budget is enforced
// across claimers, admits a cheaper candidate behind a blocked one, and ignores
// expired leases when recomputing usage.
func SharedResourceCapacityIsAtomicAndWeighted(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	concurrent := queueprovider.ClaimResource{Name: "gate.resource.concurrent", Concurrency: 2, Costs: map[string]int64{"gate.resource.concurrent": 1}}
	for index := 0; index < 8; index++ {
		enqueue(t, fixture, "gate.resource.concurrent", request{})
	}
	var group sync.WaitGroup
	var mutex sync.Mutex
	var leased []queueprovider.Record
	var claimErr error
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for attempt := 0; attempt < 20; attempt++ {
				records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.concurrent"}, Limit: 1, LeaseDuration: longLease, Resource: &concurrent})
				mutex.Lock()
				if err != nil && claimErr == nil {
					claimErr = err
				}
				leased = append(leased, records...)
				mutex.Unlock()
				if err != nil || len(records) != 0 {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	group.Wait()
	if claimErr != nil || len(leased) != 2 {
		t.Fatalf("resource concurrency leased=%d error=%v", len(leased), claimErr)
	}

	weighted := queueprovider.ClaimResource{
		Name:        "gate.resource.weighted",
		Concurrency: 3,
		Costs: map[string]int64{
			"gate.resource.holder": 2,
			"gate.resource.heavy":  2,
			"gate.resource.cheap":  1,
		},
	}
	enqueue(t, fixture, "gate.resource.holder", request{})
	holder, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.holder"}, Limit: 1, LeaseDuration: longLease, Resource: &weighted})
	if err != nil || len(holder) != 1 {
		t.Fatalf("resource holder=%#v error=%v", holder, err)
	}
	heavy := enqueue(t, fixture, "gate.resource.heavy", request{})
	time.Sleep(2 * time.Millisecond)
	cheap := enqueue(t, fixture, "gate.resource.cheap", request{})
	admitted, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.heavy", "gate.resource.cheap"}, Limit: 1, LeaseDuration: longLease, Resource: &weighted})
	if err != nil || len(admitted) != 1 || admitted[0].ID != cheap {
		t.Fatalf("weighted admission=%#v error=%v heavy=%s cheap=%s", admitted, err, heavy, cheap)
	}

	if changed, err := fixture.Store.Release(ctx, holder[0].ID, holder[0].LeaseToken); err != nil || !changed {
		t.Fatalf("release holder changed=%t error=%v", changed, err)
	}
	expiring, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.holder"}, Limit: 1, LeaseDuration: shortLease, Resource: &weighted})
	if err != nil || len(expiring) != 1 {
		t.Fatalf("expiring holder=%#v error=%v", expiring, err)
	}
	if blocked, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.heavy"}, Limit: 1, LeaseDuration: longLease, Resource: &weighted}); err != nil || len(blocked) != 0 {
		t.Fatalf("live resource holder admitted=%#v error=%v", blocked, err)
	}
	time.Sleep(expiry)
	afterExpiry, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.heavy"}, Limit: 1, LeaseDuration: longLease, Resource: &weighted})
	if err != nil || len(afterExpiry) != 1 || afterExpiry[0].ID != heavy {
		t.Fatalf("expired resource holder admission=%#v error=%v", afterExpiry, err)
	}

	deep := queueprovider.ClaimResource{
		Name:        "gate.resource.deep",
		Concurrency: 3,
		Costs: map[string]int64{
			"gate.resource.deep.holder": 2,
			"gate.resource.deep.heavy":  2,
			"gate.resource.deep.cheap":  1,
		},
	}
	enqueue(t, fixture, "gate.resource.deep.holder", request{})
	deepHolder, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.deep.holder"}, Limit: 1, LeaseDuration: longLease, Resource: &deep})
	if err != nil || len(deepHolder) != 1 {
		t.Fatalf("deep resource holder=%#v error=%v", deepHolder, err)
	}
	for index := 0; index <= queueprovider.MaximumClaimJobs; index++ {
		enqueue(t, fixture, "gate.resource.deep.heavy", request{})
	}
	deepCheap := enqueue(t, fixture, "gate.resource.deep.cheap", request{})
	deepAdmission, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.deep.heavy", "gate.resource.deep.cheap"}, Limit: 1, LeaseDuration: longLease, Resource: &deep})
	if err != nil || len(deepAdmission) != 1 || deepAdmission[0].ID != deepCheap {
		t.Fatalf("deep weighted admission=%#v error=%v", deepAdmission, err)
	}

	disjointA := queueprovider.ClaimResource{Name: "gate.resource.disjoint", Concurrency: 3, Costs: map[string]int64{"gate.resource.catalog.a": 2}}
	disjointB := queueprovider.ClaimResource{Name: "gate.resource.disjoint", Concurrency: 3, Costs: map[string]int64{"gate.resource.catalog.b": 2}}
	enqueue(t, fixture, "gate.resource.catalog.a", request{})
	enqueue(t, fixture, "gate.resource.catalog.b", request{})
	catalogA, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.catalog.a"}, Limit: 1, LeaseDuration: longLease, Resource: &disjointA})
	if err != nil || len(catalogA) != 1 {
		t.Fatalf("first disjoint catalog=%#v error=%v", catalogA, err)
	}
	if catalogB, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.catalog.b"}, Limit: 1, LeaseDuration: longLease, Resource: &disjointB}); err != nil || len(catalogB) != 0 {
		t.Fatalf("disjoint catalog exceeded shared capacity=%#v error=%v", catalogB, err)
	}

	cleanup := queueprovider.ClaimResource{
		Name:        "gate.resource.cleanup",
		Concurrency: 3,
		Costs: map[string]int64{
			"gate.resource.cleanup.canceled":  1,
			"gate.resource.cleanup.exhausted": 1,
			"gate.resource.cleanup.holder":    1,
		},
	}
	canceledID := enqueue(t, fixture, "gate.resource.cleanup.canceled", request{})
	exhaustedID := enqueue(t, fixture, "gate.resource.cleanup.exhausted", request{maxAttempts: 1})
	enqueue(t, fixture, "gate.resource.cleanup.holder", request{})
	for _, jobType := range []string{"gate.resource.cleanup.canceled", "gate.resource.cleanup.exhausted"} {
		if records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{jobType}, Limit: 1, LeaseDuration: shortLease, Resource: &cleanup}); err != nil || len(records) != 1 {
			t.Fatalf("cleanup expiring claim type=%s records=%#v error=%v", jobType, records, err)
		}
	}
	if holder, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.cleanup.holder"}, Limit: 1, LeaseDuration: longLease, Resource: &cleanup}); err != nil || len(holder) != 1 {
		t.Fatalf("cleanup holder=%#v error=%v", holder, err)
	}
	if canceled, err := fixture.Store.Cancel(ctx, canceledID); err != nil || !canceled.Changed || canceled.Terminal {
		t.Fatalf("cleanup cancellation=%#v error=%v", canceled, err)
	}
	time.Sleep(expiry)
	full := queueprovider.ClaimResource{Name: cleanup.Name, Concurrency: 1, Costs: cleanup.Costs}
	if records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.resource.cleanup.canceled", "gate.resource.cleanup.exhausted"}, Limit: 1, LeaseDuration: longLease, Resource: &full}); err != nil || len(records) != 0 {
		t.Fatalf("full resource cleanup records=%#v error=%v", records, err)
	}
	if canceled := inspect(t, fixture, canceledID); canceled.State != queueprovider.StateCanceled {
		t.Fatalf("full resource left canceled lease in state %s", canceled.State)
	}
	if exhausted := inspect(t, fixture, exhaustedID); exhausted.State != queueprovider.StateFailed || exhausted.LastCode != queueprovider.CodeAttemptsExhausted {
		t.Fatalf("full resource left exhausted lease %#v", exhausted)
	}

}

// ExpiredLeaseCancellationIsImmediate proves cancellation terminalizes an
// expired lease directly while a live lease remains cooperative.
func ExpiredLeaseCancellationIsImmediate(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	expiredID := enqueue(t, fixture, "gate.cancel.expired.direct", request{})
	claimOne(t, fixture, "gate.cancel.expired.direct", shortLease)
	time.Sleep(expiry)
	expired, err := fixture.Store.Cancel(ctx, expiredID)
	if err != nil || !expired.Changed || !expired.Terminal {
		t.Fatalf("expired cancellation=%#v error=%v", expired, err)
	}
	record := inspect(t, fixture, expiredID)
	if record.State != queueprovider.StateCanceled || record.LeaseToken != "" || record.LeaseUntil != nil || record.LastCode != queueprovider.CodeCanceled || record.FinishedAt == nil {
		t.Fatalf("expired cancellation record=%#v", record)
	}
	liveID := enqueue(t, fixture, "gate.cancel.live.direct", request{})
	claimOne(t, fixture, "gate.cancel.live.direct", longLease)
	live, err := fixture.Store.Cancel(ctx, liveID)
	if err != nil || !live.Changed || live.Terminal {
		t.Fatalf("live cancellation=%#v error=%v", live, err)
	}
	if record := inspect(t, fixture, liveID); record.State != queueprovider.StateLeased || !record.CancelRequested || record.LeaseToken == "" {
		t.Fatalf("live cancellation record=%#v", record)
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
		{name: "retry", call: func() (bool, error) { return fixture.Store.RetryAt(ctx, identity, stale.LeaseToken, 0, "stale", false) }},
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

// ExpiredLeaseCannotBeRenewed proves a paused owner cannot resurrect a lease
// after it has become available to another worker.
func ExpiredLeaseCannotBeRenewed(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	identity := enqueue(t, fixture, "gate.renew.expired", request{})
	expired := claimOne(t, fixture, "gate.renew.expired", shortLease)
	time.Sleep(expiry)
	if renewal, err := fixture.Store.Renew(ctx, identity, expired.LeaseToken, longLease); err != nil || renewal.Renewed {
		t.Fatalf("expired renewal=%#v error=%v", renewal, err)
	}
	reclaimed := claimOne(t, fixture, "gate.renew.expired", longLease)
	if reclaimed.ID != identity || reclaimed.LeaseToken == expired.LeaseToken || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaimed job=%#v", reclaimed)
	}
}

// ExpiredLeaseCannotTransition proves every owner-only disposition expires
// with the lease even before another worker replaces its token.
func ExpiredLeaseCannotTransition(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	identity := enqueue(t, fixture, "gate.transition.expired", request{})
	expired := claimOne(t, fixture, "gate.transition.expired", shortLease)
	time.Sleep(expiry)
	transitions := []struct {
		name string
		call func() (bool, error)
	}{
		{name: "succeed", call: func() (bool, error) { return fixture.Store.Succeed(ctx, identity, expired.LeaseToken, "expired") }},
		{name: "fail", call: func() (bool, error) { return fixture.Store.Fail(ctx, identity, expired.LeaseToken, "expired") }},
		{name: "retry", call: func() (bool, error) {
			return fixture.Store.RetryAt(ctx, identity, expired.LeaseToken, 0, "expired", false)
		}},
		{name: "uncounted retry", call: func() (bool, error) {
			return fixture.Store.RetryAt(ctx, identity, expired.LeaseToken, 0, "expired", true)
		}},
		{name: "cancel", call: func() (bool, error) { return fixture.Store.MarkCanceled(ctx, identity, expired.LeaseToken, "expired") }},
		{name: "release", call: func() (bool, error) { return fixture.Store.Release(ctx, identity, expired.LeaseToken) }},
	}
	for _, transition := range transitions {
		changed, err := transition.call()
		if err != nil || changed {
			t.Fatalf("expired %s changed=%t error=%v", transition.name, changed, err)
		}
		record := inspect(t, fixture, identity)
		if record.State != queueprovider.StateLeased || record.LeaseToken != expired.LeaseToken || record.AttemptCount != 1 || record.LastCode != "" {
			t.Fatalf("expired %s mutated %#v", transition.name, record)
		}
	}
	reclaimed := claimOne(t, fixture, "gate.transition.expired", longLease)
	if reclaimed.ID != identity || reclaimed.LeaseToken == expired.LeaseToken || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaimed job=%#v", reclaimed)
	}
}

// UncountedRetryPreservesAttempt proves the decrement is atomic, provider
// equivalent, and still protected by the lease token.
func UncountedRetryPreservesAttempt(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()

	countedID := enqueue(t, fixture, "gate.retry.counted", request{})
	counted := claimOne(t, fixture, "gate.retry.counted", longLease)
	if changed, err := fixture.Store.RetryAt(ctx, countedID, counted.LeaseToken, 0, "retry", false); err != nil || !changed {
		t.Fatalf("counted retry changed=%t error=%v", changed, err)
	}
	if record := inspect(t, fixture, countedID); record.State != queueprovider.StatePending || record.AttemptCount != 1 {
		t.Fatalf("counted retry=%#v", record)
	}
	if reclaimed := claimOne(t, fixture, "gate.retry.counted", longLease); reclaimed.AttemptCount != 2 {
		t.Fatalf("counted reclaim=%#v", reclaimed)
	}

	uncountedID := enqueue(t, fixture, "gate.retry.uncounted", request{})
	uncounted := claimOne(t, fixture, "gate.retry.uncounted", longLease)
	if changed, err := fixture.Store.RetryAt(ctx, uncountedID, uncounted.LeaseToken, 0, "retry", true); err != nil || !changed {
		t.Fatalf("uncounted retry changed=%t error=%v", changed, err)
	}
	if record := inspect(t, fixture, uncountedID); record.State != queueprovider.StatePending || record.AttemptCount != 0 {
		t.Fatalf("uncounted retry=%#v", record)
	}
	fresh := claimOne(t, fixture, "gate.retry.uncounted", longLease)
	if fresh.AttemptCount != 1 || fresh.LeaseToken == uncounted.LeaseToken {
		t.Fatalf("uncounted reclaim=%#v", fresh)
	}
	if changed, err := fixture.Store.RetryAt(ctx, uncountedID, uncounted.LeaseToken, 0, "stale", true); err != nil || changed {
		t.Fatalf("stale uncounted retry changed=%t error=%v", changed, err)
	}
	if record := inspect(t, fixture, uncountedID); record.State != queueprovider.StateLeased || record.AttemptCount != 1 || record.LeaseToken != fresh.LeaseToken {
		t.Fatalf("stale uncounted retry mutated %#v", record)
	}
}

// ExpiredFinalAttemptFailsWithoutReexecution proves a crashed final attempt is terminal.
func ExpiredFinalAttemptFailsWithoutReexecution(t testing.TB, fixture Fixture) {
	t.Helper()
	identity := enqueue(t, fixture, "gate.exhausted", request{maxAttempts: 2})
	first := claimOne(t, fixture, "gate.exhausted", shortLease)
	if first.AttemptCount != 1 {
		t.Fatalf("first claim attempts=%d", first.AttemptCount)
	}
	time.Sleep(expiry)
	second := claimOne(t, fixture, "gate.exhausted", shortLease)
	if second.AttemptCount != 2 {
		t.Fatalf("second claim attempts=%d", second.AttemptCount)
	}
	time.Sleep(expiry)
	if records := claim(t, fixture, "gate.exhausted", longLease, 1); len(records) != 0 {
		t.Fatalf("exhausted job was reclaimed: %#v", records)
	}
	record := inspect(t, fixture, identity)
	if record.State != queueprovider.StateFailed || record.AttemptCount != 2 || record.LastCode != queueprovider.CodeAttemptsExhausted || record.FinishedAt == nil {
		t.Fatalf("exhausted job=%#v", record)
	}
}

// ExpiredCanceledLeaseIsTerminalWithoutReexecution proves durable
// cancellation survives a crash even on the final attempt.
func ExpiredCanceledLeaseIsTerminalWithoutReexecution(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	identity := enqueue(t, fixture, "gate.cancel.crash", request{maxAttempts: 1})
	claimOne(t, fixture, "gate.cancel.crash", shortLease)
	result, err := fixture.Store.Cancel(ctx, identity)
	if err != nil || !result.Changed || result.Terminal {
		t.Fatalf("cancel final lease result=%#v error=%v", result, err)
	}
	time.Sleep(expiry)
	if records := claim(t, fixture, "gate.cancel.crash", longLease, 1); len(records) != 0 {
		t.Fatalf("canceled final lease was reclaimed: %#v", records)
	}
	record := inspect(t, fixture, identity)
	if record.State != queueprovider.StateCanceled || record.AttemptCount != 1 || record.LastCode != queueprovider.CodeCanceled || record.FinishedAt == nil || !record.CancelRequested || record.LeaseToken != "" {
		t.Fatalf("expired canceled job=%#v", record)
	}
}

// RetentionIsStateSelectiveAndPreservesLiveRows proves manual cleanup removes
// only selected terminal evidence and never active work.
func RetentionIsStateSelectiveAndPreservesLiveRows(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()

	succeeded := enqueue(t, fixture, "gate.retention.succeeded", request{})
	succeededLease := claimOne(t, fixture, "gate.retention.succeeded", longLease)
	if changed, err := fixture.Store.Succeed(ctx, succeeded, succeededLease.LeaseToken, "done"); err != nil || !changed {
		t.Fatalf("succeed changed=%t error=%v", changed, err)
	}
	failed := enqueue(t, fixture, "gate.retention.failed", request{})
	failedLease := claimOne(t, fixture, "gate.retention.failed", longLease)
	if changed, err := fixture.Store.Fail(ctx, failed, failedLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail changed=%t error=%v", changed, err)
	}
	canceled := enqueue(t, fixture, "gate.retention.canceled", request{delay: time.Hour})
	if result, err := fixture.Store.Cancel(ctx, canceled); err != nil || !result.Changed || !result.Terminal {
		t.Fatalf("cancel result=%#v error=%v", result, err)
	}
	pending := enqueue(t, fixture, "gate.retention.pending", request{delay: time.Hour})
	leased := enqueue(t, fixture, "gate.retention.leased", request{})
	claimOne(t, fixture, "gate.retention.leased", longLease)

	cutoff := time.Now().Add(time.Hour)
	deleted, err := fixture.Store.RunRetention(ctx, queueprovider.RetentionPolicy{OlderThan: cutoff, MaxRows: 10, States: []queueprovider.State{queueprovider.StateFailed}})
	if err != nil || deleted != 1 {
		t.Fatalf("selected retention deleted=%d error=%v", deleted, err)
	}
	if _, err := fixture.Store.Inspect(ctx, failed); !errors.Is(err, queueprovider.ErrNotFound) {
		t.Fatalf("selected failed row survived: %v", err)
	}
	for _, identity := range []string{succeeded, canceled, pending, leased} {
		if _, err := fixture.Store.Inspect(ctx, identity); err != nil {
			t.Fatalf("selected retention removed %s: %v", identity, err)
		}
	}

	deleted, err = fixture.Store.RunRetention(ctx, queueprovider.RetentionPolicy{OlderThan: cutoff, MaxRows: 10})
	if err != nil || deleted != 2 {
		t.Fatalf("default retention deleted=%d error=%v", deleted, err)
	}
	for _, identity := range []string{succeeded, canceled} {
		if _, err := fixture.Store.Inspect(ctx, identity); !errors.Is(err, queueprovider.ErrNotFound) {
			t.Fatalf("terminal row %s survived default retention: %v", identity, err)
		}
	}
	for _, identity := range []string{pending, leased} {
		if _, err := fixture.Store.Inspect(ctx, identity); err != nil {
			t.Fatalf("default retention removed live row %s: %v", identity, err)
		}
	}
}

// FailedJobsCanBeDiscoveredAndRecovered proves stable redacted paging and bounded recovery.
func FailedJobsCanBeDiscoveredAndRecovered(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	oldest := enqueue(t, fixture, "gate.failed", request{})
	oldestLease := claimOne(t, fixture, "gate.failed", longLease)
	if changed, err := fixture.Store.Fail(ctx, oldest, oldestLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail oldest changed=%t error=%v", changed, err)
	}
	time.Sleep(2 * time.Millisecond)
	newest := enqueue(t, fixture, "gate.failed", request{})
	newestLease := claimOne(t, fixture, "gate.failed", longLease)
	if changed, err := fixture.Store.Fail(ctx, newest, newestLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail newest changed=%t error=%v", changed, err)
	}
	time.Sleep(2 * time.Millisecond)
	other := enqueue(t, fixture, "gate.other", request{})
	otherLease := claimOne(t, fixture, "gate.other", longLease)
	if changed, err := fixture.Store.Fail(ctx, other, otherLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail other changed=%t error=%v", changed, err)
	}
	pending := enqueue(t, fixture, "gate.failed", request{delay: time.Hour})

	first, err := fixture.Store.ListFailed(ctx, queueprovider.FailedQuery{Types: []string{"gate.failed"}, Limit: 1})
	if err != nil || len(first.Jobs) != 1 || !first.More || first.Jobs[0].ID != newest || first.Jobs[0].FinishedAt == nil {
		t.Fatalf("first failed page=%#v error=%v", first, err)
	}
	cursor := queueprovider.FailedCursor{FinishedAt: *first.Jobs[0].FinishedAt, ID: first.Jobs[0].ID}
	second, err := fixture.Store.ListFailed(ctx, queueprovider.FailedQuery{Types: []string{"gate.failed"}, Limit: 1, Before: &cursor})
	if err != nil || len(second.Jobs) != 1 || second.More || second.Jobs[0].ID != oldest {
		t.Fatalf("second failed page=%#v error=%v", second, err)
	}
	if second.Jobs[0].State != queueprovider.StateFailed || second.Jobs[0].LastCode != "terminal" || second.Jobs[0].AttemptCount != 1 {
		t.Fatalf("failed summary=%#v", second.Jobs[0])
	}

	changed, err := fixture.Store.RequeueFailed(ctx, []string{oldest, pending, "absent"})
	if err != nil || changed != 1 {
		t.Fatalf("recover changed=%d error=%v", changed, err)
	}
	recovered := inspect(t, fixture, oldest)
	if recovered.State != queueprovider.StatePending || recovered.AttemptCount != 0 || recovered.LastCode != "" || recovered.FinishedAt != nil {
		t.Fatalf("recovered job=%#v", recovered)
	}
	if remaining, err := fixture.Store.ListFailed(ctx, queueprovider.FailedQuery{Limit: 3}); err != nil || len(remaining.Jobs) != 2 || remaining.Jobs[0].ID != other || remaining.Jobs[1].ID != newest {
		t.Fatalf("remaining failed=%#v error=%v", remaining, err)
	}

	blocked := enqueue(t, fixture, "gate.dedupe", request{dedupe: "same"})
	blockedLease := claimOne(t, fixture, "gate.dedupe", longLease)
	if changed, err := fixture.Store.Fail(ctx, blocked, blockedLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail dedupe changed=%t error=%v", changed, err)
	}
	active := enqueue(t, fixture, "gate.dedupe", request{dedupe: "same", delay: time.Hour})
	if active == blocked {
		t.Fatal("terminal dedupe key did not admit a successor")
	}
	if changed, err := fixture.Store.RequeueFailed(ctx, []string{blocked}); err != nil || changed != 0 {
		t.Fatalf("dedupe-conflicted recovery changed=%d error=%v", changed, err)
	}
	if record := inspect(t, fixture, blocked); record.State != queueprovider.StateFailed {
		t.Fatalf("dedupe-conflicted job=%#v", record)
	}

	sharedFirst := enqueue(t, fixture, "gate.dedupe", request{dedupe: "batch-shared"})
	sharedFirstLease := claimOne(t, fixture, "gate.dedupe", longLease)
	if changed, err := fixture.Store.Fail(ctx, sharedFirst, sharedFirstLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail first shared job changed=%t error=%v", changed, err)
	}
	sharedSecond := enqueue(t, fixture, "gate.dedupe", request{dedupe: "batch-shared"})
	sharedSecondLease := claimOne(t, fixture, "gate.dedupe", longLease)
	if changed, err := fixture.Store.Fail(ctx, sharedSecond, sharedSecondLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail second shared job changed=%t error=%v", changed, err)
	}
	if changed, err := fixture.Store.RequeueFailed(ctx, []string{sharedFirst, sharedSecond}); err != nil || changed != 1 {
		t.Fatalf("shared-dedupe recovery changed=%d error=%v", changed, err)
	}
	states := map[queueprovider.State]int{}
	states[inspect(t, fixture, sharedFirst).State]++
	states[inspect(t, fixture, sharedSecond).State]++
	if states[queueprovider.StatePending] != 1 || states[queueprovider.StateFailed] != 1 {
		t.Fatalf("shared-dedupe recovery states=%v", states)
	}

	for round := 0; round < 8; round++ {
		key := "race-shared-" + strconv.Itoa(round)
		typeName := "gate.dedupe.race." + strconv.Itoa(round)
		first := enqueue(t, fixture, typeName, request{dedupe: key})
		firstLease := claimOne(t, fixture, typeName, longLease)
		if changed, err := fixture.Store.Fail(ctx, first, firstLease.LeaseToken, "terminal"); err != nil || !changed {
			t.Fatalf("fail raced first changed=%t error=%v", changed, err)
		}
		second := enqueue(t, fixture, typeName, request{dedupe: key})
		secondLease := claimOne(t, fixture, typeName, longLease)
		if changed, err := fixture.Store.Fail(ctx, second, secondLease.LeaseToken, "terminal"); err != nil || !changed {
			t.Fatalf("fail raced second changed=%t error=%v", changed, err)
		}
		start := make(chan struct{})
		var recovered atomic.Int64
		var failure atomic.Value
		var group sync.WaitGroup
		for _, id := range []string{first, second} {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				changed, err := fixture.Store.Requeue(ctx, id)
				if err != nil {
					failure.Store(err)
					return
				}
				if changed {
					recovered.Add(1)
				}
			}()
		}
		close(start)
		group.Wait()
		if value := failure.Load(); value != nil || recovered.Load() != 1 {
			t.Fatalf("concurrent shared-dedupe recovery changed=%d error=%v", recovered.Load(), value)
		}
	}
}

// JobsCanBeListedCountedAndCanceledInBulk proves bounded payload-free
// discovery, state counts, and exact-identity bulk cancellation.
func JobsCanBeListedCountedAndCanceledInBulk(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	pendingOldest := enqueue(t, fixture, "gate.operator.pending.a", request{delay: time.Hour})
	time.Sleep(2 * time.Millisecond)
	pendingNewest := enqueue(t, fixture, "gate.operator.pending.b", request{delay: time.Hour})

	leased := enqueue(t, fixture, "gate.operator.leased", request{})
	claimOne(t, fixture, "gate.operator.leased", longLease)
	succeeded := enqueue(t, fixture, "gate.operator.succeeded", request{})
	succeededLease := claimOne(t, fixture, "gate.operator.succeeded", longLease)
	if changed, err := fixture.Store.Succeed(ctx, succeeded, succeededLease.LeaseToken, "done"); err != nil || !changed {
		t.Fatalf("succeed changed=%t error=%v", changed, err)
	}
	failed := enqueue(t, fixture, "gate.operator.failed", request{})
	failedLease := claimOne(t, fixture, "gate.operator.failed", longLease)
	if changed, err := fixture.Store.Fail(ctx, failed, failedLease.LeaseToken, "terminal"); err != nil || !changed {
		t.Fatalf("fail changed=%t error=%v", changed, err)
	}

	first, err := fixture.Store.List(ctx, queueprovider.JobQuery{States: []queueprovider.State{queueprovider.StatePending}, Limit: 1})
	if err != nil || len(first.Jobs) != 1 || !first.More || first.Jobs[0].ID != pendingNewest {
		t.Fatalf("first job page=%#v error=%v", first, err)
	}
	cursor := queueprovider.JobCursor{EnqueuedAt: first.Jobs[0].EnqueuedAt, ID: first.Jobs[0].ID}
	second, err := fixture.Store.List(ctx, queueprovider.JobQuery{States: []queueprovider.State{queueprovider.StatePending}, Limit: 1, Before: &cursor})
	if err != nil || len(second.Jobs) != 1 || second.More || second.Jobs[0].ID != pendingOldest {
		t.Fatalf("second job page=%#v error=%v", second, err)
	}
	filtered, err := fixture.Store.List(ctx, queueprovider.JobQuery{Types: []string{"gate.operator.failed"}, States: []queueprovider.State{queueprovider.StateFailed}, Limit: 2})
	if err != nil || len(filtered.Jobs) != 1 || filtered.More || filtered.Jobs[0].ID != failed || filtered.Jobs[0].LastCode != "terminal" {
		t.Fatalf("filtered job page=%#v error=%v", filtered, err)
	}

	counts, err := fixture.Store.CountByState(ctx, queueprovider.CountQuery{})
	if err != nil || counts != (queueprovider.StateCounts{Pending: 2, Leased: 1, Succeeded: 1, Failed: 1}) {
		t.Fatalf("state counts=%#v error=%v", counts, err)
	}
	filteredCounts, err := fixture.Store.CountByState(ctx, queueprovider.CountQuery{Types: []string{"gate.operator.pending.a", "gate.operator.failed"}})
	if err != nil || filteredCounts != (queueprovider.StateCounts{Pending: 1, Failed: 1}) {
		t.Fatalf("filtered state counts=%#v error=%v", filteredCounts, err)
	}

	batch, err := fixture.Store.CancelMany(ctx, []string{pendingOldest, leased, succeeded, "absent"})
	if err != nil || batch.Changed != 2 || len(batch.Terminal) != 1 || !batch.Terminal[0].Terminal || batch.Terminal[0].Type != "gate.operator.pending.a" {
		t.Fatalf("cancel batch=%#v error=%v", batch, err)
	}
	if record := inspect(t, fixture, pendingOldest); record.State != queueprovider.StateCanceled || record.FinishedAt == nil {
		t.Fatalf("bulk-canceled pending job=%#v", record)
	}
	if record := inspect(t, fixture, leased); record.State != queueprovider.StateLeased || !record.CancelRequested {
		t.Fatalf("bulk-requested leased cancellation=%#v", record)
	}
	if record := inspect(t, fixture, succeeded); record.State != queueprovider.StateSucceeded {
		t.Fatalf("bulk cancellation changed terminal job=%#v", record)
	}
	if repeated, err := fixture.Store.CancelMany(ctx, []string{pendingOldest, leased, succeeded, "absent"}); err != nil || repeated.Changed != 0 || len(repeated.Terminal) != 0 {
		t.Fatalf("repeated cancel batch=%#v error=%v", repeated, err)
	}
	counts, err = fixture.Store.CountByState(ctx, queueprovider.CountQuery{})
	if err != nil || counts != (queueprovider.StateCounts{Pending: 1, Leased: 1, Succeeded: 1, Failed: 1, Canceled: 1}) {
		t.Fatalf("post-cancel state counts=%#v error=%v", counts, err)
	}
}

// CancellationIsDurableAndIdempotent proves immediate and cooperative
// cancellation preserve one stable durable request.
func CancellationIsDurableAndIdempotent(t testing.TB, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	pending := enqueue(t, fixture, "gate.cancel.pending", request{delay: time.Hour})
	if result, err := fixture.Store.Cancel(ctx, pending); err != nil || !result.Changed || !result.Terminal || result.Type != "gate.cancel.pending" || result.AttemptCount != 0 {
		t.Fatalf("cancel pending result=%#v error=%v", result, err)
	}
	pendingRecord := inspect(t, fixture, pending)
	if pendingRecord.State != queueprovider.StateCanceled || pendingRecord.LastCode != "canceled" || pendingRecord.FinishedAt == nil {
		t.Fatalf("canceled pending job=%#v", pendingRecord)
	}
	if result, err := fixture.Store.Cancel(ctx, pending); err != nil || result.Changed {
		t.Fatalf("repeat pending cancellation result=%#v error=%v", result, err)
	}

	leased := enqueue(t, fixture, "gate.cancel.leased", request{})
	claim := claimOne(t, fixture, "gate.cancel.leased", longLease)
	if result, err := fixture.Store.Cancel(ctx, leased); err != nil || !result.Changed || result.Terminal || result.Type != "gate.cancel.leased" || result.AttemptCount != 1 {
		t.Fatalf("cancel leased result=%#v error=%v", result, err)
	}
	requested := inspect(t, fixture, leased)
	if requested.State != queueprovider.StateLeased || !requested.CancelRequested || requested.FinishedAt != nil || requested.LastCode != "" {
		t.Fatalf("leased cancellation request=%#v", requested)
	}
	time.Sleep(2 * time.Millisecond)
	if result, err := fixture.Store.Cancel(ctx, leased); err != nil || result.Changed {
		t.Fatalf("repeat leased cancellation result=%#v error=%v", result, err)
	}
	repeated := inspect(t, fixture, leased)
	if !repeated.UpdatedAt.Equal(requested.UpdatedAt) {
		t.Fatalf("repeat cancellation rewrote timestamp: first=%s second=%s", requested.UpdatedAt, repeated.UpdatedAt)
	}
	renewal, err := fixture.Store.Renew(ctx, leased, claim.LeaseToken, longLease)
	if err != nil || !renewal.Renewed || !renewal.CancelRequested {
		t.Fatalf("renew after cancellation=%#v error=%v", renewal, err)
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
	dedupe      string
	exclusive   string
	delay       time.Duration
	maxAttempts int
}

func newRequest(t testing.TB, jobType string, options request) queueprovider.EnqueueRequest {
	t.Helper()
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	maxAttempts := options.maxAttempts
	if maxAttempts == 0 {
		maxAttempts = 5
	}
	return queueprovider.EnqueueRequest{
		ID: identity, Type: jobType, Payload: []byte(`{"gate":true}`), MaxAttempts: maxAttempts,
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
