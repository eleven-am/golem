package runtime

import (
	"context"
	"errors"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	queueworker "github.com/eleven-am/golem/go/internal/queue/worker"
	"github.com/eleven-am/golem/go/queue"
)

var errFailedLeaseLost = errors.New("QUEUE_LEASE_LOST: the job lease was lost before the failure committed")

type JobFailed[P, A any] struct {
	name string
	run  func(ctx context.Context, transaction *SystemTx[P, A], meta queue.Meta, payload []byte, code queue.FailureCode) error
}

func OnJobFailed[P, A, T any](jobType queue.Type[T], callback func(context.Context, *SystemTx[P, A], queue.Failure[T]) error) JobFailed[P, A] {
	if callback == nil {
		return JobFailed[P, A]{}
	}
	return JobFailed[P, A]{
		name: jobType.Name(),
		run: func(ctx context.Context, transaction *SystemTx[P, A], meta queue.Meta, payload []byte, code queue.FailureCode) error {
			return callback(ctx, transaction, jobType.DecodeFailure(meta, payload, code))
		},
	}
}

func (app *App[P, A]) failedCallbacks(config *QueueConfig, declared []JobFailed[P, A], registry *queue.Registry) (map[string]JobFailed[P, A], error) {
	if len(declared) == 0 {
		return nil, nil
	}
	if config == nil {
		return nil, queue.Fail(queue.CodeConfigInvalid, "queue failure callbacks require a queue")
	}
	callbacks := make(map[string]JobFailed[P, A], len(declared))
	for _, callback := range declared {
		if callback.name == "" || callback.run == nil {
			return nil, queue.Fail(queue.CodeConfigInvalid, "queue failure callback is empty")
		}
		if _, registered := registry.Lookup(callback.name); !registered {
			return nil, queue.Fail(queue.CodeConfigInvalid, "queue failure callback names unregistered job type %q", callback.name)
		}
		if _, repeated := callbacks[callback.name]; repeated {
			return nil, queue.Fail(queue.CodeConfigInvalid, "job type %q has two failure callbacks", callback.name)
		}
		callbacks[callback.name] = callback
	}
	return callbacks, nil
}

func (app *App[P, A]) failJob(callbacks map[string]JobFailed[P, A]) queueworker.FailFunc {
	if len(callbacks) == 0 {
		return nil
	}
	return func(ctx context.Context, record queueprovider.Record, code string) (bool, error) {
		callback, declared := callbacks[record.Type]
		if !declared {
			return app.queueStore.Fail(ctx, nil, record.ID, record.LeaseToken, code)
		}
		meta := queue.Meta{ID: queue.JobID(record.ID), Attempt: int(record.AttemptCount), MaxAttempts: int(record.MaxAttempts), EnqueuedAt: record.EnqueuedAt}
		var changed bool
		err := SystemTransaction(ctx, System[P, A]{app: app}, func(transaction *SystemTx[P, A]) error {
			executor, err := transaction.system.executor.transactionFor(app.database)
			if err != nil {
				return err
			}
			changed, err = app.queueStore.Fail(ctx, executor, record.ID, record.LeaseToken, code)
			if err != nil {
				return err
			}
			if !changed {
				return errFailedLeaseLost
			}
			if failure := callback.run(ctx, transaction, meta, record.Payload, queue.FailureCode(code)); failure != nil {
				return &queueworker.CallbackError{Err: failure}
			}
			return nil
		})
		if errors.Is(err, errFailedLeaseLost) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return changed, nil
	}
}
