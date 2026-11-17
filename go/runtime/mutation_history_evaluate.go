package runtime

import (
	"context"
	"errors"
	"fmt"

	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// evaluate keeps transaction epoch validation around the shared, database-free
// historical evaluator. Durable consumers use the same Graph.Evaluate owner.
func (graph *mutationHistoryGraph) evaluate(ctx context.Context, phase mutationHistoryPhase, root mutationHistoryRoot, condition policyir.Condition) (bool, error) {
	if graph == nil || graph.snapshot == nil {
		return false, fmt.Errorf("policy history: evaluation requires a captured graph")
	}
	if err := graph.check(); err != nil {
		return false, err
	}
	if condition.ModelID() != root.model {
		return false, fmt.Errorf("policy history: condition and source root models differ")
	}
	providers, err := providerSet(graph.snapshot.registry)
	if err != nil {
		return false, err
	}
	matched, err := graph.value.Evaluate(ctx, graph.snapshot.registry, providers, policyhistory.Phase(phase), root.key, condition)
	if checkErr := graph.check(); checkErr != nil {
		return false, checkErr
	}
	if err != nil {
		var limit *policyhistory.LimitError
		if errors.As(err, &limit) {
			return false, &mutationLimitError{limit: limit.Limit, max: limit.Max}
		}
		return false, err
	}
	return matched, nil
}
