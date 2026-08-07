// Package outbox coordinates immutable mutation facts through durable,
// causation-level at-least-once publication.
package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

var ErrPublisherRunning = errors.New("P7_PUBLISHER_LIFECYCLE: publisher is already running")

// PublisherTransport is the minimal P7-C/P7-D adapter edge. P7-D bridges the
// exact public EventTransport ABI to this internal sealed causal batch.
type PublisherTransport interface {
	Publish(context.Context, eventvalue.EventBatch) error
}

type Limits struct {
	ClaimGroups     int
	Concurrency     int
	LeaseDuration   time.Duration
	PublishTimeout  time.Duration
	RetryBase       time.Duration
	RetryCap        time.Duration
	ShutdownGrace   time.Duration
	MaxEncodedBytes int
}

func (limits Limits) normalized() (Limits, error) {
	if limits.ClaimGroups == 0 {
		limits.ClaimGroups = 64
	}
	if limits.Concurrency == 0 {
		limits.Concurrency = 8
	}
	if limits.LeaseDuration == 0 {
		limits.LeaseDuration = 30 * time.Second
	}
	if limits.PublishTimeout == 0 {
		limits.PublishTimeout = 15 * time.Second
	}
	if limits.RetryBase == 0 {
		limits.RetryBase = 250 * time.Millisecond
	}
	if limits.RetryCap == 0 {
		limits.RetryCap = 5 * time.Minute
	}
	if limits.ShutdownGrace == 0 {
		limits.ShutdownGrace = 15 * time.Second
	}
	if limits.MaxEncodedBytes == 0 {
		limits.MaxEncodedBytes = eventcodec.DefaultMaxEncodedBytes
	}
	if err := eventprovider.ValidateClaim(eventprovider.ClaimOptions{Groups: limits.ClaimGroups, LeaseDuration: limits.LeaseDuration}); err != nil {
		return Limits{}, err
	}
	if limits.Concurrency <= 0 || limits.Concurrency > 128 {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: concurrency must be within 1..128")
	}
	if limits.PublishTimeout <= 0 || limits.PublishTimeout > 2*time.Minute || limits.PublishTimeout%time.Microsecond != 0 {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: publish timeout is invalid")
	}
	if limits.RetryBase <= 0 || limits.RetryBase > time.Minute || limits.RetryBase%time.Microsecond != 0 {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: retry base is invalid")
	}
	if limits.RetryCap < limits.RetryBase || limits.RetryCap > 24*time.Hour || limits.RetryCap%time.Microsecond != 0 {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: retry cap is invalid")
	}
	if limits.ShutdownGrace <= 0 || limits.ShutdownGrace > 2*time.Minute || limits.ShutdownGrace%time.Microsecond != 0 {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: shutdown grace is invalid")
	}
	if limits.MaxEncodedBytes < 1 || limits.MaxEncodedBytes > eventcodec.HardMaxEncodedBytes {
		return Limits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: encoded event bytes are invalid")
	}
	return limits, nil
}

type crashHooks struct {
	AfterClaim            func(eventprovider.Lease) error
	BeforePublish         func(eventvalue.EventBatch) error
	AfterPublishBeforeAck func(eventvalue.EventBatch) error
	AfterAck              func(eventprovider.Lease) error
}

type Publisher struct {
	coordinator   eventprovider.Coordinator
	resolver      mutationfact.HistoricalSchemaResolver
	compatibility activeEventSchemaCompatibility
	transport     PublisherTransport
	limits        Limits
	observer      events.Observer
	hooks         crashHooks

	mu      sync.Mutex
	running bool
}

type activeEventSchemaCompatibility interface {
	CanDeliverEventSchema(golem.ModelID, golem.EventSchemaDigest) bool
}

func NewPublisher(coordinator eventprovider.Coordinator, resolver mutationfact.HistoricalSchemaResolver, transport PublisherTransport, limits Limits) (*Publisher, error) {
	return NewPublisherObserved(coordinator, resolver, transport, limits, nil)
}

func NewPublisherObserved(coordinator eventprovider.Coordinator, resolver mutationfact.HistoricalSchemaResolver, transport PublisherTransport, limits Limits, observer events.Observer) (*Publisher, error) {
	if coordinator == nil || resolver == nil || transport == nil {
		return nil, fmt.Errorf("P7_PUBLISHER_CONFIG: coordinator, historical resolver, and transport are required")
	}
	normalized, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	compatibility, ok := resolver.(activeEventSchemaCompatibility)
	if !ok {
		return nil, fmt.Errorf("P7_PUBLISHER_CONFIG: active event-schema compatibility is required")
	}
	return &Publisher{coordinator: coordinator, resolver: resolver, compatibility: compatibility, transport: transport, limits: normalized, observer: observer}, nil
}

func (publisher *Publisher) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("P7_PUBLISHER_LIFECYCLE: context is nil")
	}
	publisher.mu.Lock()
	if publisher.running {
		publisher.mu.Unlock()
		return ErrPublisherRunning
	}
	publisher.running = true
	publisher.mu.Unlock()
	defer func() {
		publisher.mu.Lock()
		publisher.running = false
		publisher.mu.Unlock()
	}()

	for {
		if err := ctx.Err(); err != nil {
			events.Observe(publisher.observer, ctx, golem.ModelID{}, "", events.ObservationCancellation, events.OutcomeCancelled, "", 0, 0, 0, 0, 1)
			return nil
		}
		leases, err := publisher.coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: publisher.limits.ClaimGroups, LeaseDuration: publisher.limits.LeaseDuration})
		if err != nil {
			if ctx.Err() != nil {
				events.Observe(publisher.observer, ctx, golem.ModelID{}, "", events.ObservationCancellation, events.OutcomeCancelled, "", 0, 0, 0, 0, 1)
				return nil
			}
			events.Observe(publisher.observer, ctx, golem.ModelID{}, "", events.ObservationPublisherClaim, events.OutcomeFailure, "", 0, 0, publisher.limits.ClaimGroups, 0, 1)
			return fmt.Errorf("P7_PUBLISHER_CLAIM: %w", err)
		}
		if len(leases) != 0 {
			events.Observe(publisher.observer, ctx, golem.ModelID{}, "", events.ObservationPublisherClaim, events.OutcomeSuccess, "", 0, len(leases), publisher.limits.ClaimGroups, 0, int64(len(leases)))
		}
		if len(leases) == 0 {
			if !waitContext(ctx, publisher.limits.RetryBase) {
				return nil
			}
			continue
		}
		if err := publisher.runClaimed(ctx, leases); err != nil {
			return err
		}
	}
}

func (publisher *Publisher) runClaimed(ctx context.Context, leases []eventprovider.Lease) error {
	work := make(chan eventprovider.Lease)
	errorsChannel := make(chan error, len(leases))
	workers := publisher.limits.Concurrency
	if workers > len(leases) {
		workers = len(leases)
	}
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for lease := range work {
				if err := publisher.publishLease(ctx, lease); err != nil {
					errorsChannel <- err
				}
			}
		}()
	}
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(work)
		for _, lease := range leases {
			select {
			case work <- lease:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		timer := time.NewTimer(publisher.limits.ShutdownGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			return nil
		}
	}
	<-feedDone
	// All workers have exited, so the bounded channel has no future senders.
	// Drain its current contents without closing it: on the shutdown-grace path
	// workers may still own their send capability after this function returns.
	for {
		select {
		case err := <-errorsChannel:
			if err != nil && ctx.Err() == nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (publisher *Publisher) publishLease(ctx context.Context, lease eventprovider.Lease) error {
	causation, err := parseUUID[golem.CausationID](lease.Delivery.CausationID)
	if err != nil || len(lease.Facts) == 0 {
		return publisher.blockInvalid(ctx, lease, "fact-invalid")
	}
	if publisher.hooks.AfterClaim != nil {
		if err := publisher.hooks.AfterClaim(lease); err != nil {
			return err
		}
	}
	notices := make([]eventvalue.Notice, len(lease.Facts))
	for index, row := range lease.Facts {
		if row.CausationID != lease.Delivery.CausationID || row.TransactionOrdinal != int64(index+1) {
			return publisher.blockInvalid(ctx, lease, "fact-order-invalid")
		}
		envelope, encodeErr := eventcodec.EncodeStoredRow(codecFactRow(row), publisher.resolver, eventcodec.Limits{MaxEncodedBytes: publisher.limits.MaxEncodedBytes})
		if encodeErr != nil {
			return publisher.blockInvalid(ctx, lease, classifyFactFailure(encodeErr))
		}
		if !publisher.compatibility.CanDeliverEventSchema(envelope.ModelID(), envelope.ResolvedEventSchemaDigest()) {
			return publisher.blockInvalid(ctx, lease, "event-schema-incompatible")
		}
		notice, noticeErr := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
		if noticeErr != nil {
			return publisher.blockInvalid(ctx, lease, "event-invalid")
		}
		notices[index] = notice
	}
	batch, err := eventvalue.NewEventBatch(causation, notices)
	if err != nil {
		return publisher.blockInvalid(ctx, lease, "fact-order-invalid")
	}
	if publisher.hooks.BeforePublish != nil {
		if err := publisher.hooks.BeforePublish(batch); err != nil {
			return err
		}
	}
	started := time.Now()
	publishErr, owned := publisher.publishWithRenewal(ctx, lease, batch)
	if !owned || publishErr != nil {
		publisher.observeBatch(ctx, batch, events.ObservationPublisherAttempt, events.OutcomeFailure, lease.Delivery.AttemptCount, time.Since(started))
	} else {
		publisher.observeBatch(ctx, batch, events.ObservationPublisherAttempt, events.OutcomeSuccess, lease.Delivery.AttemptCount, time.Since(started))
	}
	if !owned {
		return nil
	}
	if publishErr != nil {
		if ctx.Err() != nil {
			publisher.releaseLease(lease)
			return nil
		}
		delay := retryDelay(lease.Delivery.CausationID, lease.Delivery.AttemptCount, publisher.limits.RetryBase, publisher.limits.RetryCap)
		changed, retryErr := publisher.coordinator.Retry(ctx, lease.Delivery.CausationID, lease.Delivery.LeaseToken, delay, "transport-failure")
		if retryErr != nil {
			return fmt.Errorf("P7_PUBLISHER_RETRY: %w", retryErr)
		}
		if !changed {
			return nil
		}
		publisher.observeBatch(ctx, batch, events.ObservationPublisherRetry, events.OutcomeSuccess, lease.Delivery.AttemptCount, 0)
		return nil
	}
	if publisher.hooks.AfterPublishBeforeAck != nil {
		if err := publisher.hooks.AfterPublishBeforeAck(batch); err != nil {
			return err
		}
	}
	changed, ackErr := publisher.coordinator.Acknowledge(ctx, lease.Delivery.CausationID, lease.Delivery.LeaseToken)
	if ackErr != nil {
		return fmt.Errorf("P7_PUBLISHER_ACK: %w", ackErr)
	}
	if !changed {
		return nil
	}
	publisher.observeBatch(ctx, batch, events.ObservationPublisherAck, events.OutcomeSuccess, lease.Delivery.AttemptCount, 0)
	if publisher.hooks.AfterAck != nil {
		if err := publisher.hooks.AfterAck(lease); err != nil {
			return err
		}
	}
	return nil
}

func codecFactRow(row eventprovider.FactRow) mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
		GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
		BeforeIdentity: append([]byte(nil), row.BeforeIdentity...), AfterIdentity: append([]byte(nil), row.AfterIdentity...),
		CausationID: row.CausationID, TransactionOrdinal: row.TransactionOrdinal,
		Metadata: append([]byte(nil), row.Metadata...), DeleteSnapshot: append([]byte(nil), row.DeleteSnapshot...),
		RecordedAt: row.RecordedAt,
	}
}

func (publisher *Publisher) publishWithRenewal(ctx context.Context, lease eventprovider.Lease, batch eventvalue.EventBatch) (error, bool) {
	attempt, cancel := context.WithTimeout(ctx, publisher.limits.PublishTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- publisher.transport.Publish(attempt, batch) }()
	renewEvery := publisher.limits.LeaseDuration / 3
	if renewEvery < time.Microsecond {
		renewEvery = time.Microsecond
	}
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err, true
		case <-ticker.C:
			renewed, err := publisher.coordinator.Renew(attempt, lease.Delivery.CausationID, lease.Delivery.LeaseToken, publisher.limits.LeaseDuration)
			if err != nil || !renewed {
				cancel()
				publisher.waitForTransport(done)
				return err, false
			}
		case <-attempt.Done():
			select {
			case err := <-done:
				if err == nil {
					return nil, true
				}
				return err, true
			case <-time.After(publisher.limits.ShutdownGrace):
				return attempt.Err(), true
			}
		}
	}
}

// waitForTransport gives a context-respecting transport a bounded interval to
// observe cancellation. A hostile adapter can retain its own goroutine by
// violating the transport contract, but it can never hold Publisher shutdown.
func (publisher *Publisher) waitForTransport(done <-chan error) {
	timer := time.NewTimer(publisher.limits.ShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (publisher *Publisher) blockInvalid(ctx context.Context, lease eventprovider.Lease, code string) error {
	changed, err := publisher.coordinator.Block(ctx, lease.Delivery.CausationID, lease.Delivery.LeaseToken, code)
	if err != nil {
		return fmt.Errorf("P7_PUBLISHER_BLOCK: %w", err)
	}
	if !changed {
		return nil
	}
	events.Observe(publisher.observer, ctx, golem.ModelID{}, "", events.ObservationPublisherBlock, events.OutcomeSuccess, "", int(lease.Delivery.AttemptCount), 0, 0, 0, 1)
	return nil
}

func (publisher *Publisher) observeBatch(ctx context.Context, batch eventvalue.EventBatch, kind events.ObservationKind, outcome events.Outcome, attempt int64, duration time.Duration) {
	for _, notice := range batch.Events() {
		events.Observe(publisher.observer, ctx, notice.ModelID(), notice.Action(), kind, outcome, "", int(attempt), 0, 0, duration, 1)
	}
}

func (publisher *Publisher) releaseLease(lease eventprovider.Lease) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = publisher.coordinator.Release(ctx, lease.Delivery.CausationID, lease.Delivery.LeaseToken)
}

func classifyFactFailure(err error) string {
	message := err.Error()
	if contains(message, "historical") || contains(message, "schema") || contains(message, "generation") {
		return "schema-unavailable"
	}
	return "fact-invalid"
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func retryDelay(causation string, attempt int64, base, maximum time.Duration) time.Duration {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	delay := base
	for exponent > 0 && delay < maximum {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
		exponent--
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(causation))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(attempt))
	_, _ = hash.Write(encoded[:])
	sum := hash.Sum(nil)
	jitterWindow := base / 4
	if jitterWindow > 0 {
		jitter := time.Duration(binary.BigEndian.Uint64(sum[:8]) % uint64(jitterWindow+1))
		if delay > maximum-jitter {
			return maximum
		}
		delay += jitter
	}
	if delay > maximum {
		return maximum
	}
	return delay.Truncate(time.Microsecond)
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func parseUUID[T ~[16]byte](value string) (T, error) {
	var result T
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return result, fmt.Errorf("invalid UUID")
	}
	compact := value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != compact {
		return result, fmt.Errorf("invalid UUID")
	}
	copy(result[:], decoded)
	return result, nil
}

// Operator exposes only causation-specific state recovery and bounded
// retention. It cannot read fact bytes or mutate application data.
type Operator struct{ coordinator eventprovider.Coordinator }

func NewOperator(coordinator eventprovider.Coordinator) (Operator, error) {
	if coordinator == nil {
		return Operator{}, fmt.Errorf("P7_OPERATOR_CONFIG: coordinator is required")
	}
	return Operator{coordinator: coordinator}, nil
}

func (operator Operator) Inspect(ctx context.Context, causation string) (eventprovider.Delivery, error) {
	return operator.coordinator.Inspect(ctx, causation)
}
func (operator Operator) Resume(ctx context.Context, causation string) error {
	changed, err := operator.coordinator.Resume(ctx, causation)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("P7_OPERATOR_STATE: causation is not blocked")
	}
	return nil
}
func (operator Operator) Retire(ctx context.Context, causation string) error {
	changed, err := operator.coordinator.Retire(ctx, causation)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("P7_OPERATOR_STATE: causation is not blocked")
	}
	return nil
}
func (operator Operator) RunRetention(ctx context.Context, policy eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return operator.coordinator.RunRetention(ctx, policy)
}
