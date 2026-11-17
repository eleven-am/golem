package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/jmoiron/sqlx"
)

func recordAppliedScalarCascadeHistory(ctx context.Context, queryer sqlx.QueryerContext, binding *executionBinding, registry *schema.Registry, provider policyir.Provider, effects *cascadeEffects) error {
	if effects.empty() || binding == nil || !binding.mutation.enabled {
		return nil
	}
	state, err := binding.mutationState()
	if err != nil {
		return err
	}
	return recordAppliedCascadeHistory(ctx, queryer, registry, provider, binding.mutation.limits, state, effects)
}

// Database effects happen inside the parent's delete statement. Capture them
// before an observer can write again, without moving V3 fact construction or
// version stamping out of their existing execution owners.
func recordAppliedCascadeHistory(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, limits normalizedMutationLimits, state *mutationState, effects *cascadeEffects) error {
	if effects.empty() || effects.historyApplied {
		return nil
	}
	for model, rows := range groupCascadeRows(effects.deleted) {
		if len(registry.PolicyCaptureFields(golem.ModelID(model))) == 0 {
			continue
		}
		remaining, err := readCascadeRows(ctx, queryer, registry, provider, limits, model, rows)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return fmt.Errorf("policy history: cascaded rows survived application")
		}
		for _, value := range rows {
			before := value
			if err := state.recordHistory(ctx, registry, provider, &before, nil, false); err != nil {
				return err
			}
		}
	}
	for model, rows := range groupCascadeRows(effects.nulled) {
		if len(registry.PolicyCaptureFields(golem.ModelID(model))) == 0 {
			continue
		}
		current, err := readCascadeRows(ctx, queryer, registry, provider, limits, model, rows)
		if err != nil {
			return err
		}
		if len(current) != len(rows) {
			return fmt.Errorf("policy history: cleared-reference row is absent")
		}
		before := make(map[string]mutationdecode.Row, len(rows))
		for _, row := range rows {
			key, err := mutationbatch.PrimaryKey(registry, provider, row)
			if err != nil {
				return err
			}
			before[key] = row
		}
		for _, row := range current {
			key, err := mutationbatch.PrimaryKey(registry, provider, row)
			if err != nil {
				return err
			}
			previous, present := before[key]
			if !present {
				return fmt.Errorf("policy history: cleared-reference identity changed")
			}
			after := row
			if err := state.recordHistory(ctx, registry, provider, &previous, &after, false); err != nil {
				return err
			}
		}
	}
	effects.historyApplied = true
	return nil
}
