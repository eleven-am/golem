// Package worker runs registered job handlers against a durable job store. It
// claims within its free capacity, heartbeats the lease it was given, and
// records every outcome through the store's fenced transitions.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

// ErrLeaseLost is the handler cancellation cause when a fenced renewal fails.
// The job now belongs to another worker and this handler's outcome is discarded.
var ErrLeaseLost = errors.New("QUEUE_LEASE_LOST: the job lease was lost")

// ErrCanceled is the handler cancellation cause when durable cancellation is
// observed on a heartbeat.
var ErrCanceled = errors.New("QUEUE_CANCELED: cancellation was requested")

var errHandlerPanic = errors.New("QUEUE_HANDLER_PANIC: handler panicked")
var errShutdown = errors.New("QUEUE_SHUTDOWN: the worker shutdown grace expired")

const (
	codeRetry             = "retry"
	codeTerminal          = "terminal"
	codeCanceled          = "canceled"
	codeAttemptsExhausted = queueprovider.CodeAttemptsExhausted
	codePanic             = "handler_panic"
)

// Worker claims and executes registered job types. One Worker owns one Run at a
// time; a second concurrent Run is refused rather than sharing capacity.
type Worker struct {
	store       queueprovider.Store
	registry    *queue.Registry
	claimGroups []claimGroup
	claimOffset int
	limits      queue.Limits
	provider    golem.Provider
	observer    observe.Observer
	wake        chan struct{}
	running     atomic.Bool
	mutex       sync.Mutex
	active      int
	inflight    map[string]int
}

type claimGroup struct {
	registrations []queue.Registration
	resource      *queueprovider.ClaimResource
}

// New binds a registry to a durable store. Every refusal is CodeConfigInvalid
// and happens before any background work exists.
func New(store queueprovider.Store, registry *queue.Registry, limits queue.Limits, provider golem.Provider, observer observe.Observer, resources ...queue.Resource) (*Worker, error) {
	if store == nil {
		return nil, queue.Fail(queue.CodeConfigInvalid, "durable job store is required")
	}
	if registry == nil {
		return nil, queue.Fail(queue.CodeConfigInvalid, "job registry is required")
	}
	registrations := registry.Registrations()
	if len(registrations) == 0 {
		return nil, queue.Fail(queue.CodeConfigInvalid, "job registry declares no job types")
	}
	if len(registrations) > queueprovider.MaximumClaimTypes {
		return nil, queue.Fail(queue.CodeConfigInvalid, "job registry declares more than %d job types", queueprovider.MaximumClaimTypes)
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	resolved := limits.Resolved()
	resolved.LeaseDuration = resolved.LeaseDuration.Truncate(time.Microsecond)
	if resolved.ClaimBatch > queueprovider.MaximumClaimJobs {
		return nil, queue.Fail(queue.CodeConfigInvalid, "ClaimBatch is above %d", queueprovider.MaximumClaimJobs)
	}
	claimGroups, err := normalizeClaimGroups(registrations, resources)
	if err != nil {
		return nil, err
	}
	return &Worker{
		store:       store,
		registry:    registry,
		claimGroups: claimGroups,
		limits:      resolved,
		provider:    provider,
		observer:    observer,
		wake:        make(chan struct{}, 1),
		inflight:    make(map[string]int, len(registrations)),
	}, nil
}

func normalizeClaimGroups(registrations []queue.Registration, resources []queue.Resource) ([]claimGroup, error) {
	normalized := make([]queueprovider.ClaimResource, 0, len(resources))
	resourceNames := make(map[string]struct{}, len(resources))
	typeOwners := make(map[string]string)
	registered := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		registered[registration.Type] = struct{}{}
	}
	for _, resource := range resources {
		if resource.Concurrency <= 0 {
			return nil, queue.Fail(queue.CodeConfigInvalid, "resource %q concurrency must be positive", resource.Name)
		}
		plan := queueprovider.ClaimResource{
			Name:        resource.Name,
			Concurrency: int64(resource.Concurrency),
			Costs:       make(map[string]int64, len(resource.Costs)),
		}
		for name, cost := range resource.Costs {
			if _, exists := registered[name]; !exists {
				return nil, queue.Fail(queue.CodeConfigInvalid, "resource %q names unregistered job type %q", resource.Name, name)
			}
			if cost <= 0 {
				return nil, queue.Fail(queue.CodeConfigInvalid, "resource %q cost for %q must be positive", resource.Name, name)
			}
			plan.Costs[name] = int64(cost)
		}
		if err := queueprovider.ValidateClaimResource(plan); err != nil {
			return nil, queue.Fail(queue.CodeConfigInvalid, "resource %q is invalid: %v", resource.Name, err)
		}
		if _, exists := resourceNames[plan.Name]; exists {
			return nil, queue.Fail(queue.CodeConfigInvalid, "resource %q is repeated", plan.Name)
		}
		resourceNames[plan.Name] = struct{}{}
		for name := range plan.Costs {
			if owner, exists := typeOwners[name]; exists {
				return nil, queue.Fail(queue.CodeConfigInvalid, "job type %q belongs to resources %q and %q", name, owner, plan.Name)
			}
			typeOwners[name] = plan.Name
		}
		normalized = append(normalized, plan)
	}
	groups := make([]claimGroup, 0, len(normalized)+1)
	unpooled := claimGroup{}
	for _, registration := range registrations {
		if _, pooled := typeOwners[registration.Type]; !pooled {
			unpooled.registrations = append(unpooled.registrations, registration)
		}
	}
	if len(unpooled.registrations) != 0 {
		groups = append(groups, unpooled)
	}
	for index := range normalized {
		plan := &normalized[index]
		group := claimGroup{resource: plan}
		for _, registration := range registrations {
			if _, exists := plan.Costs[registration.Type]; exists {
				group.registrations = append(group.registrations, registration)
			}
		}
		if len(group.registrations) != 0 {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

// Wake nudges an idle worker to claim without waiting for the poll interval. It
// never blocks and coalesces concurrent nudges into one.
func (worker *Worker) Wake() {
	if worker == nil {
		return
	}
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

// Run owns the claim loop for the lifetime of ctx. It returns on cancellation
// or on an unrecoverable configuration failure; a store failure is retried
// rather than ending the worker. Handlers that ignore their context may still
// be running when Run returns: Go cannot kill a goroutine, so shutdown requests
// termination and then stops waiting.
func (worker *Worker) Run(ctx context.Context) error {
	if worker == nil || ctx == nil {
		return queue.Fail(queue.CodeConfigInvalid, "worker and context are required")
	}
	if !worker.running.CompareAndSwap(false, true) {
		return queue.Fail(queue.CodeWorkerRunning, "worker is already running")
	}
	defer worker.running.Store(false)
	runObserver, stopObserver := worker.runObserver()
	defer stopObserver()
	var handlers sync.WaitGroup
	handlerContext, cancelHandlers := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelHandlers(nil)
	if err := worker.store.EnsureSchema(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return queue.Fail(queue.CodeStoreFailure, "durable job storage is unavailable: %v", err)
	}
	storeFailures := 0
	retentionFailures := 0
	nextRetention := time.Time{}
	for {
		if ctx.Err() != nil {
			worker.awaitHandlers(&handlers, cancelHandlers)
			return nil
		}
		var claimed int
		var err error
		now := time.Now()
		if !now.Before(nextRetention) {
			_, retentionErr := worker.store.RunRetention(ctx, queueprovider.RetentionPolicy{
				OlderThan: now.Add(-worker.limits.RetentionAge), MaxRows: worker.limits.RetentionRows,
				States: []queueprovider.State{queueprovider.StateSucceeded, queueprovider.StateFailed, queueprovider.StateCanceled},
			})
			if retentionErr != nil {
				retentionFailures++
				delay := worker.limits.PollInterval + (queue.Backoff{Base: worker.limits.PollInterval, Cap: 30 * time.Second}).Delay(retentionFailures)
				nextRetention = now.Add(delay)
			} else {
				retentionFailures = 0
				nextRetention = now.Add(worker.limits.RetentionEvery)
			}
		}
		claimed, err = worker.dispatch(ctx, handlerContext, runObserver, &handlers)
		if err == nil && claimed != 0 {
			storeFailures = 0
			continue
		}
		if err != nil {
			storeFailures++
			delay := worker.limits.PollInterval + (queue.Backoff{Base: worker.limits.PollInterval, Cap: 30 * time.Second}).Delay(storeFailures)
			if worker.waitForRetry(ctx, delay) {
				continue
			}
			worker.awaitHandlers(&handlers, cancelHandlers)
			return nil
		}
		storeFailures = 0
		if !worker.idle(ctx) {
			worker.awaitHandlers(&handlers, cancelHandlers)
			return nil
		}
	}
}

type cohort struct {
	types []string
	limit int
}

func (worker *Worker) dispatch(ctx, handlerContext context.Context, observer observe.Observer, handlers *sync.WaitGroup) (int, error) {
	claimed := 0
	offset := worker.claimOffset
	if len(worker.claimGroups) != 0 {
		worker.claimOffset = (worker.claimOffset + 1) % len(worker.claimGroups)
	}
	for _, capped := range []bool{false, true} {
		for position := range worker.claimGroups {
			claimGroup := worker.claimGroups[(offset+position)%len(worker.claimGroups)]
			group, ok := worker.cohort(claimGroup, capped)
			if !ok {
				continue
			}
			records, err := worker.store.Claim(ctx, queueprovider.ClaimOptions{Types: group.types, Limit: group.limit, LeaseDuration: worker.limits.LeaseDuration, Resource: claimGroup.resource})
			if err != nil {
				return claimed, err
			}
			for _, record := range records {
				if ctx.Err() != nil {
					worker.release(ctx, record)
					continue
				}
				claimed++
				worker.start(handlerContext, record, observer, handlers)
			}
		}
	}
	return claimed, nil
}

// cohort selects the job types with free capacity and the largest claim that
// cannot overrun any of them. Capacity is enforced here so a gated job is never
// leased and bounced back through the database.
func (worker *Worker) cohort(group claimGroup, capped bool) (cohort, bool) {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()
	limit := worker.limits.Concurrency - worker.active
	if limit > worker.limits.ClaimBatch {
		limit = worker.limits.ClaimBatch
	}
	if limit <= 0 {
		return cohort{}, false
	}
	var types []string
	for _, registration := range group.registrations {
		if (registration.MaxConcurrent != 0) != capped {
			continue
		}
		if capped {
			remaining := registration.MaxConcurrent - worker.inflight[registration.Type]
			if remaining <= 0 {
				continue
			}
			if remaining < limit {
				limit = remaining
			}
		}
		types = append(types, registration.Type)
	}
	if len(types) == 0 {
		return cohort{}, false
	}
	return cohort{types: types, limit: limit}, true
}

func (worker *Worker) start(ctx context.Context, record queueprovider.Record, observer observe.Observer, handlers *sync.WaitGroup) {
	registration, found := worker.registry.Lookup(record.Type)
	if !found {
		worker.release(ctx, record)
		return
	}
	worker.mutex.Lock()
	worker.active++
	worker.inflight[record.Type]++
	worker.mutex.Unlock()
	handlers.Add(1)
	go func() {
		defer worker.finish(record.Type, handlers)
		worker.run(ctx, record, registration, observer)
	}()
}

func (worker *Worker) finish(typeName string, handlers *sync.WaitGroup) {
	worker.mutex.Lock()
	worker.active--
	worker.inflight[typeName]--
	worker.mutex.Unlock()
	handlers.Done()
	worker.Wake()
}

func (worker *Worker) run(ctx context.Context, record queueprovider.Record, registration queue.Registration, observer observe.Observer) {
	if record.CancelRequested {
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.MarkCanceled(book, record.ID, record.LeaseToken, codeCanceled)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseCancel, observe.OutcomeCancelled, observe.ReasonNone, 0)
		}
		return
	}
	handlerContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	handlerContext, stopTimeout := context.WithTimeout(handlerContext, registration.Timeout)
	defer stopTimeout()

	type handlerResult struct {
		err   error
		cause error
	}
	started := time.Now()
	worker.emit(observer, record, observe.PhaseStart, observe.OutcomeSuccess, observe.ReasonNone, 0)
	done := make(chan handlerResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- handlerResult{err: fmt.Errorf("%w: %v", errHandlerPanic, recovered), cause: context.Cause(handlerContext)}
			}
		}()
		err := registration.Handle(handlerContext, record.Payload, queue.Meta{
			ID: queue.JobID(record.ID), Attempt: int(record.AttemptCount),
			MaxAttempts: int(record.MaxAttempts), EnqueuedAt: record.EnqueuedAt,
		})
		done <- handlerResult{err: err, cause: context.Cause(handlerContext)}
	}()

	renewEvery := worker.limits.LeaseDuration / 3
	if renewEvery < time.Millisecond {
		renewEvery = time.Millisecond
	}
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()
	renewalChannel := ticker.C

	var graceTimer *time.Timer
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	var grace <-chan time.Time
	startGrace := func() {
		if graceTimer == nil {
			graceTimer = time.NewTimer(worker.limits.AbandonGrace)
			grace = graceTimer.C
		}
	}
	deadline := handlerContext.Done()
	canceled := false
	lost := false

	for {
		select {
		case result := <-done:
			if lost || errors.Is(result.cause, errShutdown) {
				return
			}
			if errors.Is(result.cause, context.DeadlineExceeded) {
				result.err = context.DeadlineExceeded
			}
			worker.record(ctx, observer, record, registration, result.err, result.cause, canceled, time.Since(started))
			return
		case <-deadline:
			deadline = nil
			startGrace()
		case <-grace:
			cause := context.Cause(handlerContext)
			if lost || errors.Is(cause, errShutdown) || errors.Is(cause, ErrLeaseLost) {
				return
			}
			worker.record(ctx, observer, record, registration, cause, cause, canceled, time.Since(started))
			return
		case <-renewalChannel:
			renewal, err := worker.renew(ctx, record)
			if err != nil || !renewal.Renewed {
				lost = true
				renewalChannel = nil
				cancel(ErrLeaseLost)
				startGrace()
				continue
			}
			if renewal.CancelRequested && !canceled {
				canceled = true
				cancel(ErrCanceled)
			}
		}
	}
}

func (worker *Worker) renew(ctx context.Context, record queueprovider.Record) (queueprovider.Renewal, error) {
	book, cancel := worker.bookkeeping(ctx)
	defer cancel()
	return worker.store.Renew(book, record.ID, record.LeaseToken, worker.limits.LeaseDuration)
}

func (worker *Worker) record(ctx context.Context, observer observe.Observer, record queueprovider.Record, registration queue.Registration, err, cause error, canceled bool, duration time.Duration) {
	if canceled {
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.MarkCanceled(book, record.ID, record.LeaseToken, codeCanceled)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseCancel, observe.OutcomeCancelled, observe.ReasonNone, duration)
		}
		return
	}
	outcome := queue.Classify(err)
	switch {
	case outcome.Resolution == queue.ResolutionSucceeded:
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Succeed(book, record.ID, record.LeaseToken, outcome.Code)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseFinish, observe.OutcomeSuccess, observe.ReasonNone, duration)
		}
	case outcome.Resolution == queue.ResolutionFailed:
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Fail(book, record.ID, record.LeaseToken, codeTerminal)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseFinish, observe.OutcomeFailure, observationReason(err, cause), duration)
		}
	case !outcome.Uncounted && record.AttemptCount >= record.MaxAttempts:
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Fail(book, record.ID, record.LeaseToken, codeAttemptsExhausted)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseFinish, observe.OutcomeFailure, observe.ReasonLimit, duration)
		}
	default:
		delay := outcome.Delay
		if !outcome.Scheduled {
			delay = registration.Backoff.Delay(int(record.AttemptCount))
		}
		code := codeRetry
		if errors.Is(err, errHandlerPanic) {
			code = codePanic
		}
		changed, _ := worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.RetryAt(book, record.ID, record.LeaseToken, delay.Truncate(time.Microsecond), code, outcome.Uncounted)
		})
		if changed {
			worker.emit(observer, record, observe.PhaseRetry, observe.OutcomeRetrying, observationReason(err, cause), duration)
		}
	}
}

func observationReason(err, cause error) observe.Reason {
	switch {
	case errors.Is(cause, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return observe.ReasonTimeout
	case errors.Is(err, errHandlerPanic):
		return observe.ReasonPanic
	default:
		return observe.ReasonNone
	}
}

func (worker *Worker) emit(observer observe.Observer, record queueprovider.Record, phase observe.Phase, outcome observe.Outcome, reason observe.Reason, duration time.Duration) {
	observeexec.EmitQueue(observer, worker.provider, record.Type, phase, outcome, reason, int(record.AttemptCount), duration)
}

func (worker *Worker) runObserver() (observe.Observer, func()) {
	if worker.observer == nil {
		return nil, func() {}
	}
	dispatcher, err := observe.NewDispatcher(worker.observer, observe.DispatcherConfig{})
	if err != nil {
		return nil, func() {}
	}
	return dispatcher, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}
}

func (worker *Worker) release(ctx context.Context, record queueprovider.Record) {
	_, _ = worker.finalize(ctx, func(book context.Context) (bool, error) {
		return worker.store.Release(book, record.ID, record.LeaseToken)
	})
}

// finalize runs one durable transition on a context shutdown cannot cancel, so
// completion bookkeeping survives the cancellation that ended the handler.
func (worker *Worker) finalize(ctx context.Context, transition func(context.Context) (bool, error)) (bool, error) {
	book, cancel := worker.bookkeeping(ctx)
	defer cancel()
	return transition(book)
}

func (worker *Worker) bookkeeping(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), worker.limits.LeaseDuration)
}

func (worker *Worker) idle(ctx context.Context) bool {
	return worker.idleFor(ctx, worker.limits.PollInterval)
}

func (worker *Worker) idleFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-worker.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (worker *Worker) waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (worker *Worker) awaitHandlers(handlers *sync.WaitGroup, cancel context.CancelCauseFunc) {
	done := make(chan struct{})
	go func() {
		handlers.Wait()
		close(done)
	}()
	timer := time.NewTimer(worker.limits.ShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancel(errShutdown)
		abandon := time.NewTimer(worker.limits.AbandonGrace)
		defer abandon.Stop()
		select {
		case <-done:
		case <-abandon.C:
		}
	}
}
