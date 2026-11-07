package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

const leaseMarginDivisor = 10

var errCallbackPanic = errors.New("QUEUE_FAILURE_CALLBACK_PANIC: failure callback panicked")

type FailFunc func(ctx context.Context, record queueprovider.Record, code string) (bool, error)

type CallbackError struct {
	Err error
}

func (failure *CallbackError) Error() string { return failure.Err.Error() }

func (failure *CallbackError) Unwrap() error { return failure.Err }

func finalizeCode(record queueprovider.Record) (string, observe.Reason) {
	if record.LastCode == string(queue.FailureTerminal) {
		return codeTerminal, observe.ReasonNone
	}
	return codeAttemptsExhausted, observe.ReasonLimit
}

func (worker *Worker) failJob(ctx context.Context, observer observe.Observer, record queueprovider.Record, code string, reason observe.Reason, duration time.Duration, leaseUntil time.Time) {
	book, cancel := context.WithDeadline(context.WithoutCancel(ctx), leaseUntil.Add(-worker.limits.LeaseDuration/leaseMarginDivisor))
	defer cancel()
	started := time.Now()
	if worker.fail != nil && !record.Finalize {
		persisted, err := worker.store.DeferFailure(book, record.ID, record.LeaseToken, code)
		if err != nil {
			worker.emit(observer, record, observe.PhaseCommit, observe.OutcomeFailure, observe.ReasonProvider, time.Since(started))
			return
		}
		if !persisted {
			return
		}
	}
	changed, err := worker.callFail(book, record, code)
	var callback *CallbackError
	switch {
	case errors.As(err, &callback):
		afterReason := observe.ReasonNone
		if errors.Is(callback, errCallbackPanic) {
			afterReason = observe.ReasonPanic
		}
		worker.emit(observer, record, observe.PhaseAfter, observe.OutcomeFailure, afterReason, time.Since(started))
	case err != nil:
		worker.emit(observer, record, observe.PhaseCommit, observe.OutcomeFailure, observe.ReasonProvider, time.Since(started))
	case changed:
		worker.emit(observer, record, observe.PhaseFinish, observe.OutcomeFailure, reason, duration)
	}
}

func (worker *Worker) callFail(ctx context.Context, record queueprovider.Record, code string) (changed bool, err error) {
	if worker.fail == nil {
		return worker.store.Fail(ctx, nil, record.ID, record.LeaseToken, code)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			changed = false
			err = &CallbackError{Err: fmt.Errorf("%w: %v", errCallbackPanic, recovered)}
		}
	}()
	return worker.fail(ctx, record, code)
}
