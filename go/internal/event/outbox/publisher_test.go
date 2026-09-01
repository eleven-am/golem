package outbox

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type publisherTestResolver struct{ registry *schema.Registry }

func (resolver publisherTestResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return resolver.registry, golem.SchemaDigest{}, reference.FormatVersion == mutationfact.FormatVersionV1 && reference.Generation == resolver.registry.GenerationDigest()
}

func (resolver publisherTestResolver) CanDeliverEventSchema(modelID golem.ModelID, digest golem.EventSchemaDigest) bool {
	model, ok := resolver.registry.Model(modelID)
	if !ok {
		return false
	}
	fingerprint, _, enabled := model.EventSchema()
	parsed, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	return enabled && err == nil && golem.EventSchemaDigest(parsed) == digest
}

type publisherTestCoordinator struct {
	mu           sync.Mutex
	renewed      bool
	renewCalls   int
	ackCalls     int
	retryCalls   int
	blockCalls   int
	releaseCalls int
}

func (*publisherTestCoordinator) Claim(context.Context, eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	return nil, nil
}
func (coordinator *publisherTestCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.renewCalls++
	return coordinator.renewed, nil
}
func (coordinator *publisherTestCoordinator) Acknowledge(context.Context, string, string) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.ackCalls++
	return true, nil
}
func (coordinator *publisherTestCoordinator) Retry(context.Context, string, string, time.Duration, string) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.retryCalls++
	return true, nil
}
func (coordinator *publisherTestCoordinator) Block(context.Context, string, string, string) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.blockCalls++
	return true, nil
}
func (coordinator *publisherTestCoordinator) Release(context.Context, string, string) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.releaseCalls++
	return true, nil
}
func (*publisherTestCoordinator) Inspect(context.Context, string) (eventprovider.Delivery, error) {
	return eventprovider.Delivery{}, nil
}
func (*publisherTestCoordinator) Resume(context.Context, string) (bool, error) { return false, nil }
func (*publisherTestCoordinator) Retire(context.Context, string) (bool, error) { return false, nil }
func (*publisherTestCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return eventprovider.RetentionResult{}, nil
}

type publisherRunCoordinator struct {
	publisherTestCoordinator
	entered chan struct{}
	once    sync.Once
}

type transientClaimCoordinator struct {
	publisherTestCoordinator
	calls     atomic.Int64
	recovered chan struct{}
}

type transientAcknowledgeCoordinator struct {
	publisherTestCoordinator
	lease     eventprovider.Lease
	claims    atomic.Int64
	continued chan struct{}
}

type retentionFailureCoordinator struct {
	publisherTestCoordinator
	claimed chan struct{}
	once    sync.Once
}

func (coordinator *transientClaimCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	if coordinator.calls.Add(1) == 1 {
		return nil, errors.New("temporary database outage")
	}
	select {
	case <-coordinator.recovered:
	default:
		close(coordinator.recovered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (coordinator *transientAcknowledgeCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	if coordinator.claims.Add(1) == 1 {
		return []eventprovider.Lease{coordinator.lease}, nil
	}
	select {
	case <-coordinator.continued:
	default:
		close(coordinator.continued)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (coordinator *transientAcknowledgeCoordinator) Acknowledge(context.Context, string, string) (bool, error) {
	return false, errors.New("temporary acknowledgement outage")
}

func (*retentionFailureCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return eventprovider.RetentionResult{}, errors.New("retention storage unavailable")
}

func (coordinator *retentionFailureCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	coordinator.once.Do(func() { close(coordinator.claimed) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (coordinator *publisherRunCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	coordinator.once.Do(func() { close(coordinator.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type captureTransport struct {
	mu      sync.Mutex
	batches []eventvalue.EventBatch
	errors  []error
}

type publisherCaptureObserver struct {
	mu           sync.Mutex
	observations []events.Observation
}

func (observer *publisherCaptureObserver) ObserveEvent(_ context.Context, observation events.Observation) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
}

func TestPublisherReportsSanitizedAttemptAndAcknowledgement(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &publisherTestCoordinator{renewed: true}
	observer := &publisherCaptureObserver{}
	publisher, err := NewPublisherObserved(coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{}, Limits{
		ClaimGroups: 1, Concurrency: 1, LeaseDuration: time.Second, PublishTimeout: time.Second,
		RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: 20 * time.Millisecond,
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.publishLease(context.Background(), publisherValidLease(t, fixture)); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.observations) != 2 || observer.observations[0].Kind() != events.ObservationPublisherAttempt || observer.observations[0].Outcome() != events.OutcomeSuccess || observer.observations[1].Kind() != events.ObservationPublisherAck || observer.observations[1].Outcome() != events.OutcomeSuccess {
		t.Fatalf("publisher observations=%#v", observer.observations)
	}
}

func TestPublisherRunOwnershipAndShutdownGrace(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &publisherTestCoordinator{renewed: true}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transportFunc(func(context.Context, eventvalue.EventBatch) error {
		once.Do(func() { close(entered) })
		<-release
		return errors.New("late hostile transport failure")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.runClaimed(ctx, []eventprovider.Lease{publisherValidLease(t, fixture)}) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hostile transport was not entered")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled publisher returned hostile failure: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("publisher exceeded shutdown grace")
	}
	// A late hostile return must have a safe buffered destination and must never
	// send through a channel closed by the already-returned publisher worker.
	close(release)
	time.Sleep(25 * time.Millisecond)
}

func TestPublisherRetriesTransientClaimFailure(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &transientClaimCoordinator{publisherTestCoordinator: publisherTestCoordinator{renewed: true}, recovered: make(chan struct{})}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	select {
	case <-coordinator.recovered:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("publisher did not retry its failed claim")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPublisherClaimsDespiteRetentionFailure(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &retentionFailureCoordinator{
		publisherTestCoordinator: publisherTestCoordinator{renewed: true},
		claimed:                  make(chan struct{}),
	}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	select {
	case <-coordinator.claimed:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("retention failure blocked publisher claims")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPublisherRetriesAfterTransientAcknowledgementFailure(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &transientAcknowledgeCoordinator{
		publisherTestCoordinator: publisherTestCoordinator{renewed: true},
		lease:                    publisherValidLease(t, fixture),
		continued:                make(chan struct{}),
	}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	select {
	case <-coordinator.continued:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("publisher stopped after a transient acknowledgement failure")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func (transport *captureTransport) Publish(_ context.Context, batch eventvalue.EventBatch) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.batches = append(transport.batches, batch)
	if len(transport.errors) == 0 {
		return nil
	}
	err := transport.errors[0]
	transport.errors = transport.errors[1:]
	return err
}

func TestPublisherRejectsOrdinalAndDuplicatedColumnCorruptionBeforeTransport(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	valid := publisherValidLease(t, fixture)
	for name, mutate := range map[string]func(*eventprovider.Lease){
		"ordinal-gap":             func(lease *eventprovider.Lease) { lease.Facts[0].TransactionOrdinal = 2 },
		"duplicated-model-column": func(lease *eventprovider.Lease) { lease.Facts[0].ModelID = "000000000000000000000000000000ff" },
	} {
		t.Run(name, func(t *testing.T) {
			lease := valid
			lease.Facts = eventprovider.CloneFacts(valid.Facts)
			mutate(&lease)
			coordinator := &publisherTestCoordinator{renewed: true}
			transport := &captureTransport{}
			publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
			if err := publisher.publishLease(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			if len(transport.batches) != 0 || coordinator.blockCalls != 1 || coordinator.ackCalls != 0 {
				t.Fatalf("transport=%d blocks=%d acks=%d", len(transport.batches), coordinator.blockCalls, coordinator.ackCalls)
			}
		})
	}
}

func TestPublisherReusesIdenticalCausalBatchAfterAmbiguousAcceptanceWindows(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := publisherValidLease(t, fixture)
	for _, test := range []struct {
		name      string
		transport *captureTransport
		hook      bool
	}{
		{name: "transport-accepted-then-returned-error", transport: &captureTransport{errors: []error{errors.New("ambiguous accept"), nil}}},
		{name: "crash-after-accept-before-ack", transport: &captureTransport{}, hook: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &publisherTestCoordinator{renewed: true}
			publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, test.transport)
			if test.hook {
				publisher.hooks.AfterPublishBeforeAck = func(eventvalue.EventBatch) error { return errors.New("crash window") }
			}
			first := publisher.publishLease(context.Background(), lease)
			if test.hook && first == nil {
				t.Fatal("crash hook did not interrupt acknowledgement")
			}
			publisher.hooks = crashHooks{}
			if err := publisher.publishLease(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			if len(test.transport.batches) != 2 {
				t.Fatalf("published batches=%d", len(test.transport.batches))
			}
			assertIdenticalBatches(t, test.transport.batches[0], test.transport.batches[1])
			if test.hook && coordinator.ackCalls != 1 {
				t.Fatalf("crash window acks=%d", coordinator.ackCalls)
			}
			if !test.hook && (coordinator.retryCalls != 1 || coordinator.ackCalls != 1) {
				t.Fatalf("ambiguous retry=%d ack=%d", coordinator.retryCalls, coordinator.ackCalls)
			}
		})
	}
}

func TestPublisherRenewalLossCancelsContextRespectingTransportWithinGrace(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := publisherValidLease(t, fixture)
	coordinator := &publisherTestCoordinator{renewed: false}
	exited := make(chan struct{})
	transport := transportFunc(func(ctx context.Context, _ eventvalue.EventBatch) error {
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
	publisher.limits.LeaseDuration = 3 * time.Millisecond
	publisher.limits.ShutdownGrace = 20 * time.Millisecond
	started := time.Now()
	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("renewal loss exceeded bounded shutdown grace")
	}
	select {
	case <-exited:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context-respecting transport leaked after cancellation")
	}
}

func TestPublisherRunOwnershipAndCancellationLifecycle(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &publisherRunCoordinator{entered: make(chan struct{})}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- publisher.Run(ctx) }()
	select {
	case <-coordinator.entered:
	case <-time.After(time.Second):
		t.Fatal("publisher did not enter owned run")
	}
	if err := publisher.Run(context.Background()); !errors.Is(err, ErrPublisherRunning) {
		t.Fatalf("second owner error=%v", err)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("cancelled run error=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("publisher did not stop after cancellation")
	}
}

type transportFunc func(context.Context, eventvalue.EventBatch) error

func (function transportFunc) Publish(ctx context.Context, batch eventvalue.EventBatch) error {
	return function(ctx, batch)
}

func publisherForTest(t *testing.T, coordinator eventprovider.Coordinator, resolver mutationfact.HistoricalSchemaResolver, transport PublisherTransport) *Publisher {
	t.Helper()
	publisher, err := NewPublisher(coordinator, resolver, transport, Limits{
		ClaimGroups: 1, Concurrency: 1, LeaseDuration: time.Second, PublishTimeout: time.Second,
		RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func publisherValidLease(t *testing.T, fixture schematest.Fixture) eventprovider.Lease {
	t.Helper()
	id := [16]byte{1}
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue(id)),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cause := mutationfact.CausationID{8}
	envelope, err := mutationfact.New(fixture.Registry, mutationfact.EventID{9}, requirement, cause, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := envelope.OutboxRow(time.Unix(1_700_000_000, 123_456_000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return eventprovider.Lease{Delivery: eventprovider.Delivery{
		CausationID: stored.CausationID, Status: eventprovider.StatusLeased, LeaseToken: "00000000-0000-4000-8000-000000000001", AttemptCount: 1,
	}, Facts: []eventprovider.FactRow{{
		EventID: stored.EventID, FactVersion: stored.FactVersion, CodecIdentity: stored.CodecIdentity,
		GenerationFingerprint: stored.GenerationFingerprint, ModelID: stored.ModelID, Action: stored.Action,
		BeforeIdentity: stored.BeforeIdentity, AfterIdentity: stored.AfterIdentity, CausationID: stored.CausationID,
		TransactionOrdinal: stored.TransactionOrdinal, Metadata: stored.Metadata, DeleteSnapshot: stored.DeleteSnapshot,
		RecordedAt: stored.RecordedAt,
	}}}
}

func assertIdenticalBatches(t *testing.T, left, right eventvalue.EventBatch) {
	t.Helper()
	if left.CausationID() != right.CausationID() {
		t.Fatal("causation identity changed across retry")
	}
	leftEvents, rightEvents := left.Events(), right.Events()
	if len(leftEvents) != len(rightEvents) {
		t.Fatal("causal batch width changed across retry")
	}
	for index := range leftEvents {
		if leftEvents[index].EventID() != rightEvents[index].EventID() || leftEvents[index].TransactionOrdinal() != rightEvents[index].TransactionOrdinal() || !bytes.Equal(leftEvents[index].Encoded(), rightEvents[index].Encoded()) {
			t.Fatalf("event %d identity or bytes changed across retry", index)
		}
	}
}
