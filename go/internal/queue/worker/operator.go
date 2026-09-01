package worker

import (
	"context"
	"errors"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

type operator struct {
	store    queueprovider.Store
	provider golem.Provider
	observer observe.Observer
}

// NewOperator exposes durable job control over a store. Every method acts on
// state the database owns; none of them reaches into a running handler.
func NewOperator(store queueprovider.Store, provider golem.Provider, observer observe.Observer) (queue.Operator, error) {
	if store == nil {
		return nil, queue.Fail(queue.CodeConfigInvalid, "durable job store is required")
	}
	return operator{store: store, provider: provider, observer: observer}, nil
}

func (control operator) Inspect(ctx context.Context, id queue.JobID) (queue.Status, error) {
	record, err := control.store.Inspect(ctx, string(id))
	if errors.Is(err, queueprovider.ErrNotFound) {
		return queue.Status{}, queue.Fail(queue.CodeJobNotFound, "job %s is absent", id)
	}
	if err != nil {
		return queue.Status{}, queue.Fail(queue.CodeStoreFailure, "inspect job %s: %v", id, err)
	}
	return statusFromSummary(queueprovider.Summary{
		ID: record.ID, Type: record.Type, State: record.State,
		AttemptCount: record.AttemptCount, MaxAttempts: record.MaxAttempts,
		AvailableAt: record.AvailableAt, LastCode: record.LastCode,
		CancelRequested: record.CancelRequested, EnqueuedAt: record.EnqueuedAt,
		FinishedAt: record.FinishedAt,
	}), nil
}

func (control operator) List(ctx context.Context, query queue.JobQuery) (queue.JobPage, error) {
	providerQuery := queueprovider.JobQuery{
		Types:  append([]string(nil), query.Types...),
		States: make([]queueprovider.State, len(query.States)),
		Limit:  query.Limit,
	}
	for index, state := range query.States {
		providerQuery.States[index] = queueprovider.State(state)
	}
	if query.Before != nil {
		providerQuery.Before = &queueprovider.JobCursor{EnqueuedAt: query.Before.EnqueuedAt.UTC().Truncate(time.Microsecond), ID: string(query.Before.ID)}
	}
	if err := queueprovider.ValidateJobQuery(providerQuery); err != nil {
		return queue.JobPage{}, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	page, err := control.store.List(ctx, providerQuery)
	if err != nil {
		return queue.JobPage{}, queue.Fail(queue.CodeStoreFailure, "list jobs: %v", err)
	}
	result := queue.JobPage{Jobs: make([]queue.Status, len(page.Jobs))}
	for index, job := range page.Jobs {
		result.Jobs[index] = statusFromSummary(job)
	}
	if page.More && len(result.Jobs) != 0 {
		last := result.Jobs[len(result.Jobs)-1]
		result.Next = &queue.JobCursor{EnqueuedAt: last.EnqueuedAt, ID: last.ID}
	}
	return result, nil
}

func (control operator) ListFailed(ctx context.Context, query queue.FailedQuery) (queue.FailedPage, error) {
	providerQuery := queueprovider.FailedQuery{Types: append([]string(nil), query.Types...), Limit: query.Limit}
	if query.Before != nil {
		providerQuery.Before = &queueprovider.FailedCursor{FinishedAt: query.Before.FinishedAt.UTC().Truncate(time.Microsecond), ID: string(query.Before.ID)}
	}
	if err := queueprovider.ValidateFailedQuery(providerQuery); err != nil {
		return queue.FailedPage{}, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	page, err := control.store.ListFailed(ctx, providerQuery)
	if err != nil {
		return queue.FailedPage{}, queue.Fail(queue.CodeStoreFailure, "list failed jobs: %v", err)
	}
	result := queue.FailedPage{Jobs: make([]queue.Status, len(page.Jobs))}
	for index, job := range page.Jobs {
		if job.FinishedAt == nil {
			return queue.FailedPage{}, queue.Fail(queue.CodeStoreFailure, "failed job %s has no finish time", job.ID)
		}
		result.Jobs[index] = statusFromSummary(job)
	}
	if page.More && len(result.Jobs) != 0 {
		last := result.Jobs[len(result.Jobs)-1]
		result.Next = &queue.FailedCursor{FinishedAt: *last.FinishedAt, ID: last.ID}
	}
	return result, nil
}

func (control operator) CountByState(ctx context.Context, query queue.CountQuery) (queue.StateCounts, error) {
	providerQuery := queueprovider.CountQuery{Types: append([]string(nil), query.Types...)}
	if err := queueprovider.ValidateCountQuery(providerQuery); err != nil {
		return queue.StateCounts{}, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	counts, err := control.store.CountByState(ctx, providerQuery)
	if err != nil {
		return queue.StateCounts{}, queue.Fail(queue.CodeStoreFailure, "count jobs by state: %v", err)
	}
	return queue.StateCounts{
		Pending: counts.Pending, Leased: counts.Leased, Succeeded: counts.Succeeded,
		Failed: counts.Failed, Canceled: counts.Canceled,
	}, nil
}

func (control operator) Cancel(ctx context.Context, id queue.JobID) (bool, error) {
	value := string(id)
	if err := queueprovider.ValidateOperatorIDs([]string{value}); err != nil {
		return false, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	result, err := control.store.Cancel(ctx, value)
	if err != nil {
		return false, queue.Fail(queue.CodeStoreFailure, "cancel job %s: %v", id, err)
	}
	if result.Changed && result.Terminal {
		observeexec.EmitQueue(control.observer, control.provider, result.Type, observe.PhaseCancel, observe.OutcomeCancelled, observe.ReasonNone, int(result.AttemptCount), 0)
	}
	return result.Changed, nil
}

func (control operator) CancelMany(ctx context.Context, ids []queue.JobID) (int, error) {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = string(id)
	}
	if err := queueprovider.ValidateOperatorIDs(values); err != nil {
		return 0, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	batch, err := control.store.CancelMany(ctx, values)
	for _, result := range batch.Terminal {
		observeexec.EmitQueue(control.observer, control.provider, result.Type, observe.PhaseCancel, observe.OutcomeCancelled, observe.ReasonNone, int(result.AttemptCount), 0)
	}
	if err != nil {
		return batch.Changed, queue.Fail(queue.CodeStoreFailure, "cancel jobs: %v", err)
	}
	return batch.Changed, nil
}

func (control operator) Requeue(ctx context.Context, id queue.JobID) (bool, error) {
	changed, err := control.store.Requeue(ctx, string(id))
	if err != nil {
		return false, queue.Fail(queue.CodeStoreFailure, "requeue job %s: %v", id, err)
	}
	return changed, nil
}

func (control operator) RequeueFailed(ctx context.Context, ids []queue.JobID) (int, error) {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = string(id)
	}
	if err := queueprovider.ValidateOperatorIDs(values); err != nil {
		return 0, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	changed, err := control.store.RequeueFailed(ctx, values)
	if err != nil {
		return changed, queue.Fail(queue.CodeStoreFailure, "requeue failed jobs: %v", err)
	}
	return changed, nil
}

func (control operator) RunRetention(ctx context.Context, policy queue.RetentionPolicy) (int, error) {
	states := make([]queueprovider.State, len(policy.States))
	for index, state := range policy.States {
		states[index] = queueprovider.State(state)
	}
	providerPolicy := queueprovider.RetentionPolicy{OlderThan: policy.OlderThan, MaxRows: policy.MaxRows, States: states}
	if err := queueprovider.ValidateRetention(providerPolicy); err != nil {
		return 0, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	deleted, err := control.store.RunRetention(ctx, providerPolicy)
	if err != nil {
		return 0, queue.Fail(queue.CodeStoreFailure, "run job retention: %v", err)
	}
	return deleted, nil
}

func statusFromSummary(summary queueprovider.Summary) queue.Status {
	return queue.Status{
		ID: queue.JobID(summary.ID), Type: summary.Type, State: queue.State(summary.State),
		Attempt: int(summary.AttemptCount), MaxAttempts: int(summary.MaxAttempts),
		AvailableAt: summary.AvailableAt, LastCode: summary.LastCode,
		CancelRequested: summary.CancelRequested, EnqueuedAt: summary.EnqueuedAt,
		FinishedAt: summary.FinishedAt,
	}
}
