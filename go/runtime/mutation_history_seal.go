package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

type mutationHistorySeal struct{ historyEpoch, factEpoch uint64 }

// sealHistory stages the graph and references in the existing fact buffer.
// It runs after mutation callbacks and before flush takes the state mutex;
// encoding does not hold that mutex. Default writers do not call this owner
// until historical consumer routing has passed its activation gates.
func (state *mutationState) sealHistory(ctx context.Context, graph *mutationHistoryGraph, resolver mutationfact.HistoricalSchemaResolver) error {
	if state == nil || graph == nil || graph.snapshot == nil || graph.snapshot.state != state {
		return fmt.Errorf("P5_POLICY_HISTORY: graph does not belong to this mutation transaction")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := graph.check(); err != nil {
		return err
	}
	state.mu.Lock()
	if state.finished || state.flushed || state.historySeal != nil || state.failure != nil {
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: mutation state is failed or already finalized")
	}
	if graph.snapshot.epoch != state.historyEpoch {
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: graph write epoch changed before sealing")
	}
	version := mutationHistorySeal{historyEpoch: state.historyEpoch, factEpoch: state.factEpoch}
	rows := append([]mutationfact.OutboxRow(nil), state.facts...)
	maximum := state.limits.outboxBytes
	state.mu.Unlock()
	sealed, _, err := mutationfact.SealPolicyHistoryCausation(rows, graph.value, resolver)
	if err != nil {
		return err
	}
	bytes := 0
	for _, row := range sealed {
		if row.EncodedBytes() > maximum-bytes {
			return &mutationLimitError{limit: golem.LimitOutboxBytes, max: maximum}
		}
		bytes += row.EncodedBytes()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finished || state.flushed || state.historySeal != nil || state.failure != nil || state.historyEpoch != version.historyEpoch || state.factEpoch != version.factEpoch {
		return fmt.Errorf("P5_POLICY_HISTORY: mutation evidence changed during historical graph sealing")
	}
	state.facts = sealed
	state.bytes = bytes
	state.historySeal = &version
	return nil
}
