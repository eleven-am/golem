package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// This is the automatic writer format of this binary, not an application
// option. Reader-first rollout retains V3 until the provider and compatibility
// gates allow a later writer release. Explicit test sealing is independent.
const mutationHistoryWriterVersion = mutationfact.FormatVersion

// Registered capture inventory makes historical evidence a writer contract for
// the entire application, independent of active actors or subscribers. Begin,
// producer admission and legacy notice routing must agree on that contract.
func mutationHistoryWriterRequired(registry *schema.Registry) bool {
	return mutationHistoryWriterVersion == mutationfact.HistoryFactVersion && registry != nil && registry.PolicyCaptureFingerprint() != (golem.SchemaDigest{})
}

// finalizeMutationHistory stages history through the existing mutation owner.
// Callers must finish their mutation callbacks first. Automatic writers do not
// invoke this function until provider and consumer activation gates are met.
func finalizeMutationHistory(ctx context.Context, binding *executionBinding) error {
	if ctx == nil {
		return fmt.Errorf("P5_POLICY_HISTORY: finalization context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := binding.mutationState()
	if err != nil {
		return err
	}
	state.mu.Lock()
	if state.finished || state.flushed {
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: mutation state is already finalized")
	}
	if state.failure != nil {
		failure := state.failure
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: mutation state contains a failed mutation: %w", failure)
	}
	if state.historySeal != nil && (state.historySeal.historyEpoch != state.historyEpoch || state.historySeal.factEpoch != state.factEpoch) {
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: mutation evidence changed after historical graph sealing")
	}
	// A transaction with no emitted facts or no captured writes retains its
	// ordinary V3 behavior. Missing configuration is never this no-op signal.
	if len(state.facts) == 0 || len(state.history) == 0 {
		state.mu.Unlock()
		return nil
	}
	config := binding.mutation
	if config.registry == nil || config.eventSchemas == nil || state.registry != config.registry || state.binding != binding {
		state.mu.Unlock()
		return fmt.Errorf("P5_POLICY_HISTORY: captured writes require their owning registry and event schema resolver")
	}
	sealed := state.historySeal != nil
	state.mu.Unlock()
	if sealed {
		return ctx.Err()
	}
	// The existing snapshot and seal owners enforce their epoch checks around
	// SQL reads and encoding; neither executes while holding the state mutex.
	snapshot, err := newMutationHistorySnapshot(binding, config.registry)
	if err != nil {
		return err
	}
	graph, err := newMutationHistoryGraph(ctx, snapshot)
	if err != nil {
		return err
	}
	return state.sealHistory(ctx, graph, config.eventSchemas)
}
