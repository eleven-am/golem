package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/jmoiron/sqlx"
)

type cascadeEffects struct {
	deleted []mutationdecode.Row
	nulled  []mutationdecode.Row
}

func (effects *cascadeEffects) empty() bool {
	return effects == nil || len(effects.deleted) == 0 && len(effects.nulled) == 0
}

type cascadeFrontier struct {
	model policyir.ModelID
	rows  []mutationdecode.Row
}

func captureCascadeEffects(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, limits normalizedMutationLimits, state *mutationState, model policyir.ModelID, parents []mutationdecode.Row) (*cascadeEffects, error) {
	if len(parents) == 0 || len(registry.DeleteEffects(golem.ModelID(model))) == 0 {
		return nil, nil
	}
	remaining, err := state.remainingTouched()
	if err != nil {
		return nil, err
	}
	budget := remaining - len(parents)
	if budget < 0 {
		return nil, mutationbatch.LimitError(model, fmt.Sprintf("deleted rows exceed %d", limits.touchedRows))
	}
	deleted := make(map[string]struct{}, len(parents))
	for _, row := range parents {
		key, err := mutationbatch.PrimaryKey(registry, provider, row)
		if err != nil {
			return nil, err
		}
		deleted[key] = struct{}{}
	}
	nulled := map[string]int{}
	effects := &cascadeEffects{}
	frontier := []cascadeFrontier{{model: model, rows: parents}}
	for len(frontier) != 0 {
		current := frontier[0]
		frontier = frontier[1:]
		for _, effect := range registry.DeleteEffects(golem.ModelID(current.model)) {
			source := policyir.ModelID(effect.SourceModelID())
			statements, err := mutationbatch.RenderDependents(mutationbatch.DependentsRequest{
				Registry: registry, Provider: provider, Parent: current.model, Parents: current.rows, Effect: effect,
				MaxRows: uint32(max(budget, 1)), MaxParameters: uint32(limits.statementParameters),
			})
			if err != nil {
				return nil, err
			}
			var next []mutationdecode.Row
			for _, statement := range statements {
				rows, _, err := executeMutationBatchStatement(ctx, queryer, registry, provider, source, statement, 0)
				if err != nil {
					return nil, err
				}
				for _, row := range rows {
					key, err := mutationbatch.PrimaryKey(registry, provider, row)
					if err != nil {
						return nil, err
					}
					if _, gone := deleted[key]; gone {
						continue
					}
					if effect.Action() == compilerir.ActionCascade {
						deleted[key] = struct{}{}
						if index, present := nulled[key]; present {
							effects.nulled[index] = mutationdecode.Row{}
							delete(nulled, key)
						}
						effects.deleted = append(effects.deleted, row)
						next = append(next, row)
						continue
					}
					if _, present := nulled[key]; !present {
						nulled[key] = len(effects.nulled)
						effects.nulled = append(effects.nulled, row)
					}
				}
				if len(effects.deleted)+len(nulled) > budget {
					return nil, mutationbatch.LimitError(source, fmt.Sprintf("cascaded rows exceed the %d of %d touched rows that remain", budget, limits.touchedRows))
				}
			}
			if len(next) != 0 {
				frontier = append(frontier, cascadeFrontier{model: source, rows: next})
			}
		}
	}
	compacted := effects.nulled[:0]
	for _, row := range effects.nulled {
		if row.ModelID() != (policyir.ModelID{}) {
			compacted = append(compacted, row)
		}
	}
	effects.nulled = compacted
	return effects, nil
}

func recordCascadeEffects(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, limits normalizedMutationLimits, state *mutationState, effects *cascadeEffects) error {
	if effects.empty() {
		return nil
	}
	for model, rows := range groupCascadeRows(effects.deleted) {
		remaining, err := readCascadeRows(ctx, queryer, registry, provider, limits, model, rows)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return fmt.Errorf("P4_MUTATION_CASCADE: %d cascaded rows survived their parent delete", len(remaining))
		}
	}
	after := make(map[string]mutationdecode.Row, len(effects.nulled))
	for model, rows := range groupCascadeRows(effects.nulled) {
		current, err := readCascadeRows(ctx, queryer, registry, provider, limits, model, rows)
		if err != nil {
			return err
		}
		for _, row := range current {
			key, err := mutationbatch.PrimaryKey(registry, provider, row)
			if err != nil {
				return err
			}
			after[key] = row
		}
	}
	if err := state.touch(len(effects.deleted) + len(effects.nulled)); err != nil {
		return err
	}
	for _, row := range append(append([]mutationdecode.Row(nil), effects.deleted...), effects.nulled...) {
		if err := markCascadeSemanticRecord(state, registry, row); err != nil {
			return err
		}
	}
	recordedAt := time.Now()
	for _, row := range effects.deleted {
		before := row
		if err := buildCascadeFact(state, registry, mutationir.Delete, &before, nil, recordedAt); err != nil {
			return err
		}
	}
	for _, row := range effects.nulled {
		key, err := mutationbatch.PrimaryKey(registry, provider, row)
		if err != nil {
			return err
		}
		current, present := after[key]
		if !present {
			return fmt.Errorf("P4_MUTATION_CASCADE: a row whose reference was cleared is absent")
		}
		before := row
		if err := buildCascadeFact(state, registry, mutationir.Update, &before, &current, recordedAt); err != nil {
			return err
		}
	}
	return nil
}

func buildCascadeFact(state *mutationState, registry *schema.Registry, operation mutationir.Operation, before, after *mutationdecode.Row, recordedAt time.Time) error {
	model := before.ModelID()
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok || !metadata.SubscriptionsEnabled() {
		return nil
	}
	requirement, err := mutationplan.ModelFactRequirement(registry, model, operation)
	if err != nil {
		return err
	}
	if !requirement.Enabled() {
		return nil
	}
	_, err = state.buildFact(registry, requirement, before, after, recordedAt)
	return err
}

func readCascadeRows(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, limits normalizedMutationLimits, model policyir.ModelID, rows []mutationdecode.Row) ([]mutationdecode.Row, error) {
	statements, err := mutationbatch.RenderRows(mutationbatch.RowsRequest{Registry: registry, Provider: provider, Model: model, Rows: rows, MaxParameters: uint32(limits.statementParameters)})
	if err != nil {
		return nil, err
	}
	var result []mutationdecode.Row
	for _, statement := range statements {
		current, _, err := executeMutationBatchStatement(ctx, queryer, registry, provider, model, statement, 0)
		if err != nil {
			return nil, err
		}
		result = append(result, current...)
	}
	return result, nil
}

func groupCascadeRows(rows []mutationdecode.Row) map[policyir.ModelID][]mutationdecode.Row {
	result := make(map[policyir.ModelID][]mutationdecode.Row)
	for _, row := range rows {
		result[row.ModelID()] = append(result[row.ModelID()], row)
	}
	return result
}

func markCascadeSemanticRecord(state *mutationState, registry *schema.Registry, row mutationdecode.Row) error {
	model := row.ModelID()
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok || !metadata.SemanticIndexed() {
		return nil
	}
	primary, err := semanticPrimaryKeyFields(registry, model)
	if err != nil {
		return err
	}
	identity, err := semanticIdentityFromRow(primary, row)
	if err != nil {
		return err
	}
	key, err := semantickey.Encode(identity)
	if err != nil {
		return err
	}
	return state.markSemantic(golem.ModelID(model), key, identity)
}
