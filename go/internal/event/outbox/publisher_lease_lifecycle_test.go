package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type leaseStoreGroup struct {
	lease  eventprovider.Lease
	status eventprovider.Status
	token  string
	until  time.Duration
}

type leaseStore struct {
	mu       sync.Mutex
	now      time.Duration
	issued   int
	advanced bool
	groups   []*leaseStoreGroup
	renewed  chan string
}

func newLeaseStore(t *testing.T, fixture schematest.Fixture, groups int) *leaseStore {
	t.Helper()
	store := &leaseStore{renewed: make(chan string, 4096)}
	for index := 1; index <= groups; index++ {
		lease := p7EvidenceLease(t, fixture, byte(index), time.Unix(1_700_000_000, 0).UTC(), 1)
		store.groups = append(store.groups, &leaseStoreGroup{lease: lease, status: eventprovider.StatusPending})
	}
	return store
}

func (store *leaseStore) advance(duration time.Duration) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.now += duration
	store.advanced = true
}

func (store *leaseStore) leased() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	held := 0
	for _, group := range store.groups {
		if group.status == eventprovider.StatusLeased {
			held++
		}
	}
	return held
}

func (store *leaseStore) claim(options eventprovider.ClaimOptions) []eventprovider.Lease {
	store.mu.Lock()
	defer store.mu.Unlock()
	var leases []eventprovider.Lease
	for _, group := range store.groups {
		if len(leases) == options.Groups {
			break
		}
		if group.status != eventprovider.StatusPending && (group.status != eventprovider.StatusLeased || group.until > store.now) {
			continue
		}
		store.issued++
		group.status = eventprovider.StatusLeased
		group.token = fmt.Sprintf("00000000-0000-4000-8000-%012d", store.issued)
		group.until = store.now + options.LeaseDuration
		lease := group.lease
		lease.Delivery.Status = eventprovider.StatusLeased
		lease.Delivery.LeaseToken = group.token
		lease.Facts = eventprovider.CloneFacts(group.lease.Facts)
		leases = append(leases, lease)
	}
	return leases
}

func (store *leaseStore) transition(causation, token string, apply func(*leaseStoreGroup)) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, group := range store.groups {
		if group.lease.Delivery.CausationID == causation && group.status == eventprovider.StatusLeased && group.token == token {
			apply(group)
			return true
		}
	}
	return false
}

func (store *leaseStore) settle(status eventprovider.Status) func(*leaseStoreGroup) {
	return func(group *leaseStoreGroup) {
		group.status = status
		group.token = ""
	}
}

type leaseStoreCoordinator struct {
	store   *leaseStore
	claimed chan struct{}
	acked   chan string
	onClaim func()
}

func newLeaseStoreCoordinator(store *leaseStore) *leaseStoreCoordinator {
	return &leaseStoreCoordinator{store: store, claimed: make(chan struct{}, 4096), acked: make(chan string, 4096)}
}

func (coordinator *leaseStoreCoordinator) Claim(ctx context.Context, options eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	leases := coordinator.store.claim(options)
	select {
	case coordinator.claimed <- struct{}{}:
	default:
	}
	if coordinator.onClaim != nil {
		coordinator.onClaim()
	}
	return leases, nil
}

func (coordinator *leaseStoreCoordinator) Renew(_ context.Context, causation, token string, duration time.Duration) (bool, error) {
	store := coordinator.store
	return store.transition(causation, token, func(group *leaseStoreGroup) {
		group.until = store.now + duration
		if store.advanced {
			select {
			case store.renewed <- causation:
			default:
			}
		}
	}), nil
}

func (coordinator *leaseStoreCoordinator) Acknowledge(_ context.Context, causation, token string) (bool, error) {
	changed := coordinator.store.transition(causation, token, coordinator.store.settle(eventprovider.StatusDelivered))
	if changed {
		coordinator.acked <- causation
	}
	return changed, nil
}

func (coordinator *leaseStoreCoordinator) Retry(_ context.Context, causation, token string, _ time.Duration, _ string) (bool, error) {
	return coordinator.store.transition(causation, token, coordinator.store.settle(eventprovider.StatusPending)), nil
}

func (coordinator *leaseStoreCoordinator) Block(_ context.Context, causation, token, _ string) (bool, error) {
	return coordinator.store.transition(causation, token, coordinator.store.settle(eventprovider.StatusBlocked)), nil
}

func (coordinator *leaseStoreCoordinator) Release(_ context.Context, causation, token string) (bool, error) {
	return coordinator.store.transition(causation, token, coordinator.store.settle(eventprovider.StatusPending)), nil
}

func (*leaseStoreCoordinator) Inspect(context.Context, string) (eventprovider.Delivery, error) {
	return eventprovider.Delivery{}, nil
}
func (*leaseStoreCoordinator) Resume(context.Context, string) (bool, error) { return false, nil }
func (*leaseStoreCoordinator) Retire(context.Context, string) (bool, error) { return false, nil }
func (*leaseStoreCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return eventprovider.RetentionResult{}, nil
}

type leaseDeliveryRecorder struct {
	mu        sync.Mutex
	published map[golem.CausationID][]string
}

func (recorder *leaseDeliveryRecorder) record(causation golem.CausationID, publisher string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.published == nil {
		recorder.published = map[golem.CausationID][]string{}
	}
	recorder.published[causation] = append(recorder.published[causation], publisher)
}

func (recorder *leaseDeliveryRecorder) snapshot() map[golem.CausationID][]string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	copied := make(map[golem.CausationID][]string, len(recorder.published))
	for causation, publishers := range recorder.published {
		copied[causation] = append([]string(nil), publishers...)
	}
	return copied
}

type gatedLeaseTransport struct {
	name     string
	recorder *leaseDeliveryRecorder
	gate     chan struct{}
	entered  chan golem.CausationID
}

func newGatedLeaseTransport(name string, recorder *leaseDeliveryRecorder, gate chan struct{}) *gatedLeaseTransport {
	return &gatedLeaseTransport{name: name, recorder: recorder, gate: gate, entered: make(chan golem.CausationID, 4096)}
}

func (transport *gatedLeaseTransport) Publish(ctx context.Context, batch eventvalue.EventBatch) error {
	select {
	case transport.entered <- batch.CausationID():
	default:
	}
	select {
	case <-transport.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	transport.recorder.record(batch.CausationID(), transport.name)
	return nil
}

func leaseLifecycleLimits() Limits {
	return Limits{
		ClaimGroups: 4, Concurrency: 2, LeaseDuration: 30 * time.Millisecond, PublishTimeout: 2 * time.Minute,
		RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: 2 * time.Minute,
	}
}

func TestPublisherNeverHoldsLeasesItCannotRenew(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	store := newLeaseStore(t, fixture, 4)
	recorder := &leaseDeliveryRecorder{}
	gate := make(chan struct{})
	opened := make(chan struct{})
	close(opened)
	first, second := newLeaseStoreCoordinator(store), newLeaseStoreCoordinator(store)
	firstTransport := newGatedLeaseTransport("first", recorder, gate)
	firstPublisher, err := NewPublisher(first, publisherTestResolver{fixture.Registry}, firstTransport, leaseLifecycleLimits())
	if err != nil {
		t.Fatal(err)
	}
	secondPublisher, err := NewPublisher(second, publisherTestResolver{fixture.Registry}, newGatedLeaseTransport("second", recorder, opened), leaseLifecycleLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- firstPublisher.Run(ctx) }()
	<-first.claimed
	if held := store.leased(); held != 2 {
		t.Fatalf("publisher with concurrency 2 holds %d leases", held)
	}
	for inFlight := map[golem.CausationID]bool{}; len(inFlight) < 2; {
		inFlight[<-firstTransport.entered] = true
	}
	store.advance(time.Hour)
	for renewed := map[string]bool{}; len(renewed) < 2; {
		renewed[<-store.renewed] = true
	}
	go func() { secondDone <- secondPublisher.Run(ctx) }()
	for acknowledged := 0; acknowledged < 2; acknowledged++ {
		<-second.acked
	}
	close(gate)
	<-first.claimed
	cancel()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	published := recorder.snapshot()
	if len(published) != 4 {
		t.Fatalf("published causations=%d want 4: %v", len(published), published)
	}
	for causation, publishers := range published {
		if len(publishers) != 1 {
			t.Fatalf("causation %x was delivered by %v", causation, publishers)
		}
	}
}

func TestPublisherReleasesClaimedLeasesThatNeverStarted(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	store := newLeaseStore(t, fixture, 2)
	recorder := &leaseDeliveryRecorder{}
	coordinator := newLeaseStoreCoordinator(store)
	transport := newGatedLeaseTransport("first", recorder, make(chan struct{}))
	publisher, err := NewPublisher(coordinator, publisherTestResolver{fixture.Registry}, transport, Limits{
		ClaimGroups: 2, Concurrency: 2, LeaseDuration: time.Minute, PublishTimeout: 2 * time.Minute,
		RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.onClaim = cancel
	if err := publisher.Run(ctx); err != nil {
		t.Fatal(err)
	}
	reclaimed := store.claim(eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
	if len(reclaimed) != 2 {
		t.Fatalf("claimable after shutdown=%d want 2", len(reclaimed))
	}
	if entered := len(transport.entered); entered != 0 {
		t.Fatalf("transport entered %d times after shutdown was requested", entered)
	}
}
