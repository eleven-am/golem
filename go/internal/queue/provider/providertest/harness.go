// Package providertest contains the provider-neutral durable job store
// conformance gates. Provider packages invoke them against live databases; the
// harness never computes expected state through provider SQL.
package providertest

import (
	"context"
	"errors"
	"strconv"
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
	identity, err := fixture.Store.Enqueue(context.Background(), nil, newRequest(t, jobType, options))
	if err != nil {
		t.Fatal(err)
	}
	return identity
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
