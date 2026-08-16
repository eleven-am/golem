package worker

import (
	"context"
	"errors"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

type operator struct{ store queueprovider.Store }

// NewOperator exposes durable job control over a store. Every method acts on
// state the database owns; none of them reaches into a running handler.
func NewOperator(store queueprovider.Store) (queue.Operator, error) {
	if store == nil {
		return nil, queue.Fail(queue.CodeConfigInvalid, "durable job store is required")
	}
	return operator{store: store}, nil
}

func (control operator) Inspect(ctx context.Context, id queue.JobID) (queue.Status, error) {
	record, err := control.store.Inspect(ctx, string(id))
	if errors.Is(err, queueprovider.ErrNotFound) {
		return queue.Status{}, queue.Fail(queue.CodeJobNotFound, "job %s is absent", id)
	}
	if err != nil {
		return queue.Status{}, queue.Fail(queue.CodeStoreFailure, "inspect job %s: %v", id, err)
	}
	return queue.Status{
		ID: queue.JobID(record.ID), Type: record.Type, State: queue.State(record.State),
		Attempt: int(record.AttemptCount), MaxAttempts: int(record.MaxAttempts),
		AvailableAt: record.AvailableAt, LastCode: record.LastCode,
		CancelRequested: record.CancelRequested, EnqueuedAt: record.EnqueuedAt,
		FinishedAt: record.FinishedAt,
	}, nil
}

func (control operator) Cancel(ctx context.Context, id queue.JobID) (bool, error) {
	changed, err := control.store.Cancel(ctx, string(id))
	if err != nil {
		return false, queue.Fail(queue.CodeStoreFailure, "cancel job %s: %v", id, err)
	}
	return changed, nil
}

func (control operator) Requeue(ctx context.Context, id queue.JobID) (bool, error) {
	changed, err := control.store.Requeue(ctx, string(id))
	if err != nil {
		return false, queue.Fail(queue.CodeStoreFailure, "requeue job %s: %v", id, err)
	}
	return changed, nil
}

func (control operator) RunRetention(ctx context.Context, policy queue.RetentionPolicy) (int, error) {
	deleted, err := control.store.RunRetention(ctx, queueprovider.RetentionPolicy{OlderThan: policy.OlderThan, MaxRows: policy.MaxRows})
	if err != nil {
		return 0, queue.Fail(queue.CodeStoreFailure, "run job retention: %v", err)
	}
	return deleted, nil
}
