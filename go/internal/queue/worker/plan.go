package worker

import (
	"context"
	"maps"
	"time"

	"github.com/eleven-am/golem/go/internal/observeexec"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

const planQueueType = "plan"

type activePlan struct {
	generation    uint64
	concurrency   int
	claimBatch    int
	maxConcurrent map[string]int
	capacities    map[string]int
	appliedAt     time.Time
}

func (plan *activePlan) status() queue.PlanStatus {
	return queue.PlanStatus{
		Generation: plan.generation, Concurrency: plan.concurrency, ClaimBatch: plan.claimBatch,
		MaxConcurrent: maps.Clone(plan.maxConcurrent), Capacities: maps.Clone(plan.capacities), AppliedAt: plan.appliedAt,
	}
}

func (worker *Worker) initialPlan(registrations []queue.Registration) *activePlan {
	plan := &activePlan{
		generation: 1, concurrency: worker.limits.Concurrency, claimBatch: worker.limits.ClaimBatch,
		maxConcurrent: make(map[string]int, len(registrations)), capacities: make(map[string]int), appliedAt: time.Now(),
	}
	for _, registration := range registrations {
		plan.maxConcurrent[registration.Type] = registration.MaxConcurrent
	}
	for _, group := range worker.claimGroups {
		if group.resource != nil {
			plan.capacities[group.resource.Name] = int(group.resource.Concurrency)
		}
	}
	return plan
}

func (plan *activePlan) resourceFor(resource *queueprovider.ClaimResource) *queueprovider.ClaimResource {
	if resource == nil {
		return nil
	}
	resolved := *resource
	resolved.Concurrency = int64(plan.capacities[resource.Name])
	return &resolved
}

func (worker *Worker) currentPlan() *activePlan {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()
	return worker.plan
}

func (worker *Worker) Plan() queue.PlanStatus {
	return worker.currentPlan().status()
}

func (worker *Worker) Apply(ctx context.Context, request queue.Plan) (queue.PlanStatus, error) {
	next, err := worker.resolvePlan(request)
	if err != nil {
		worker.emitPlan(observe.OutcomeFailure, observe.ReasonInvalidInput, 1)
		return queue.PlanStatus{}, err
	}
	select {
	case worker.dispatchLock <- struct{}{}:
	case <-ctx.Done():
		return queue.PlanStatus{}, ctx.Err()
	}
	worker.mutex.Lock()
	next.generation = worker.plan.generation + 1
	worker.plan = next
	worker.mutex.Unlock()
	<-worker.dispatchLock
	worker.emitPlan(observe.OutcomeSuccess, observe.ReasonNone, int(next.generation))
	worker.Wake()
	return next.status(), nil
}

func (worker *Worker) resolvePlan(request queue.Plan) (*activePlan, error) {
	base := worker.basePlan
	if request.Concurrency < 0 {
		return nil, queue.Fail(queue.CodeConfigInvalid, "plan concurrency must not be negative")
	}
	if request.ClaimBatch < 0 || request.ClaimBatch > queueprovider.MaximumClaimJobs {
		return nil, queue.Fail(queue.CodeConfigInvalid, "plan claim batch must be within 0..%d", queueprovider.MaximumClaimJobs)
	}
	next := &activePlan{
		concurrency: orInt(request.Concurrency, base.concurrency), claimBatch: orInt(request.ClaimBatch, base.claimBatch),
		maxConcurrent: maps.Clone(base.maxConcurrent), capacities: maps.Clone(base.capacities), appliedAt: time.Now(),
	}
	for name, limit := range request.MaxConcurrent {
		if _, registered := base.maxConcurrent[name]; !registered {
			return nil, queue.Fail(queue.CodeConfigInvalid, "plan names unregistered job type %q", name)
		}
		if limit < 0 {
			return nil, queue.Fail(queue.CodeConfigInvalid, "plan cap for %q must not be negative", name)
		}
		next.maxConcurrent[name] = limit
	}
	for name, capacity := range request.Capacities {
		if _, configured := base.capacities[name]; !configured {
			return nil, queue.Fail(queue.CodeConfigInvalid, "plan names unconfigured resource %q", name)
		}
		candidate := worker.resourceNamed(name)
		candidate.Concurrency = int64(capacity)
		if err := queueprovider.ValidateClaimResource(candidate); err != nil {
			return nil, queue.Fail(queue.CodeConfigInvalid, "plan capacity for resource %q is invalid: %v", name, err)
		}
		next.capacities[name] = capacity
	}
	return next, nil
}

func (worker *Worker) resourceNamed(name string) queueprovider.ClaimResource {
	for _, group := range worker.claimGroups {
		if group.resource != nil && group.resource.Name == name {
			return *group.resource
		}
	}
	return queueprovider.ClaimResource{}
}

func orInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func (worker *Worker) emitPlan(outcome observe.Outcome, reason observe.Reason, attempt int) {
	observeexec.EmitQueue(worker.observer, worker.provider, planQueueType, observe.PhaseApply, outcome, reason, attempt, 0)
}
