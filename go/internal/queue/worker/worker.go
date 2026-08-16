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

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

// ErrLeaseLost is the handler cancellation cause when a fenced renewal fails.
// The job now belongs to another worker and this handler's outcome is discarded.
var ErrLeaseLost = errors.New("QUEUE_LEASE_LOST: the job lease was lost")

// ErrCanceled is the handler cancellation cause when durable cancellation is
// observed on a heartbeat.
var ErrCanceled = errors.New("QUEUE_CANCELED: cancellation was requested")

var errHandlerPanic = errors.New("QUEUE_HANDLER_PANIC: handler panicked")

const (
	codeRetry             = "retry"
	codeTerminal          = "terminal"
	codeCanceled          = "canceled"
	codeAttemptsExhausted = "attempts_exhausted"
	codePanic             = "handler_panic"
)

// Worker claims and executes registered job types. One Worker owns one Run at a
// time; a second concurrent Run is refused rather than sharing capacity.
type Worker struct {
	store         queueprovider.Store
	registry      *queue.Registry
	registrations []queue.Registration
	limits        queue.Limits
	wake          chan struct{}
	running       atomic.Bool
	handlers      sync.WaitGroup
	mutex         sync.Mutex
	active        int
	inflight      map[string]int
}

// New binds a registry to a durable store. Every refusal is CodeConfigInvalid
// and happens before any background work exists.
func New(store queueprovider.Store, registry *queue.Registry, limits queue.Limits) (*Worker, error) {
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
	return &Worker{
		store:         store,
		registry:      registry,
		registrations: registrations,
		limits:        resolved,
		wake:          make(chan struct{}, 1),
		inflight:      make(map[string]int, len(registrations)),
	}, nil
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
	if err := worker.store.EnsureSchema(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return queue.Fail(queue.CodeStoreFailure, "durable job storage is unavailable: %v", err)
	}
	for {
		if ctx.Err() != nil {
			worker.awaitHandlers()
			return nil
		}
		claimed, err := worker.dispatch(ctx)
		if err == nil && claimed != 0 {
			continue
		}
		if !worker.idle(ctx) {
			worker.awaitHandlers()
			return nil
		}
	}
}

type cohort struct {
	types []string
	limit int
}

func (worker *Worker) dispatch(ctx context.Context) (int, error) {
	claimed := 0
	for _, capped := range []bool{false, true} {
		group, ok := worker.cohort(capped)
		if !ok {
			continue
		}
		records, err := worker.store.Claim(ctx, queueprovider.ClaimOptions{Types: group.types, Limit: group.limit, LeaseDuration: worker.limits.LeaseDuration})
		if err != nil {
			return claimed, err
		}
		for _, record := range records {
			if ctx.Err() != nil {
				worker.release(ctx, record)
				continue
			}
			claimed++
			worker.start(ctx, record)
		}
	}
	return claimed, nil
}

// cohort selects the job types with free capacity and the largest claim that
// cannot overrun any of them. Capacity is enforced here so a gated job is never
// leased and bounced back through the database.
func (worker *Worker) cohort(capped bool) (cohort, bool) {
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
	for _, registration := range worker.registrations {
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

func (worker *Worker) start(ctx context.Context, record queueprovider.Record) {
	registration, found := worker.registry.Lookup(record.Type)
	if !found {
		worker.release(ctx, record)
		return
	}
	worker.mutex.Lock()
	worker.active++
	worker.inflight[record.Type]++
	worker.mutex.Unlock()
	worker.handlers.Add(1)
	go func() {
		defer worker.finish(record.Type)
		worker.run(ctx, record, registration)
	}()
}

func (worker *Worker) finish(typeName string) {
	worker.mutex.Lock()
	worker.active--
	worker.inflight[typeName]--
	worker.mutex.Unlock()
	worker.handlers.Done()
	worker.Wake()
}

func (worker *Worker) run(ctx context.Context, record queueprovider.Record, registration queue.Registration) {
	if record.CancelRequested {
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.MarkCanceled(book, record.ID, record.LeaseToken, codeCanceled)
		})
		return
	}
	handlerContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	handlerContext, stopTimeout := context.WithTimeout(handlerContext, registration.Timeout)
	defer stopTimeout()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("%w: %v", errHandlerPanic, recovered)
			}
		}()
		done <- registration.Handle(handlerContext, record.Payload, queue.Meta{
			ID: queue.JobID(record.ID), Attempt: int(record.AttemptCount),
			MaxAttempts: int(record.MaxAttempts), EnqueuedAt: record.EnqueuedAt,
		})
	}()

	renewEvery := worker.limits.LeaseDuration / 3
	if renewEvery < time.Millisecond {
		renewEvery = time.Millisecond
	}
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()

	var graceTimer *time.Timer
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	var grace <-chan time.Time
	startGrace := func() {
		if graceTimer == nil {
			graceTimer = time.NewTimer(worker.limits.ShutdownGrace)
			grace = graceTimer.C
		}
	}
	deadline := handlerContext.Done()
	canceled := false
	lost := false

	for {
		select {
		case err := <-done:
			if lost {
				return
			}
			worker.record(ctx, record, registration, err, canceled)
			return
		case <-deadline:
			deadline = nil
			startGrace()
		case <-grace:
			return
		case <-ticker.C:
			renewal, err := worker.renew(ctx, record)
			if err != nil || !renewal.Renewed {
				lost = true
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

func (worker *Worker) record(ctx context.Context, record queueprovider.Record, registration queue.Registration, err error, canceled bool) {
	if canceled {
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.MarkCanceled(book, record.ID, record.LeaseToken, codeCanceled)
		})
		return
	}
	outcome := queue.Classify(err)
	switch {
	case outcome.Resolution == queue.ResolutionSucceeded:
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Succeed(book, record.ID, record.LeaseToken, outcome.Code)
		})
	case outcome.Resolution == queue.ResolutionFailed:
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Fail(book, record.ID, record.LeaseToken, codeTerminal)
		})
	case record.AttemptCount >= record.MaxAttempts:
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.Fail(book, record.ID, record.LeaseToken, codeAttemptsExhausted)
		})
	default:
		delay := outcome.Delay
		if !outcome.Scheduled {
			delay = registration.Backoff.Delay(int(record.AttemptCount))
		}
		code := codeRetry
		if errors.Is(err, errHandlerPanic) {
			code = codePanic
		}
		worker.finalize(ctx, func(book context.Context) (bool, error) {
			return worker.store.RetryAt(book, record.ID, record.LeaseToken, delay.Truncate(time.Microsecond), code)
		})
	}
}

func (worker *Worker) release(ctx context.Context, record queueprovider.Record) {
	worker.finalize(ctx, func(book context.Context) (bool, error) {
		return worker.store.Release(book, record.ID, record.LeaseToken)
	})
}

// finalize runs one durable transition on a context shutdown cannot cancel, so
// completion bookkeeping survives the cancellation that ended the handler.
func (worker *Worker) finalize(ctx context.Context, transition func(context.Context) (bool, error)) {
	book, cancel := worker.bookkeeping(ctx)
	defer cancel()
	_, _ = transition(book)
}

func (worker *Worker) bookkeeping(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), worker.limits.LeaseDuration)
}

func (worker *Worker) idle(ctx context.Context) bool {
	timer := time.NewTimer(worker.limits.PollInterval)
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

func (worker *Worker) awaitHandlers() {
	done := make(chan struct{})
	go func() {
		worker.handlers.Wait()
		close(done)
	}()
	timer := time.NewTimer(worker.limits.ShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}
