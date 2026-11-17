package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// This transaction-local journal records actual row writes, independently of
// declaration/outbox order. It is input to historical graph construction, not
// a cache of predicate truth or proof of an unchanged related-row snapshot.
// Its scalar payload budget is separate from the eventual encoded outbox cap.
const maxMutationHistoryBytes = policyhistory.MaxScalarBytes

type mutationHistoryTransition struct {
	owner         *mutationScope
	key           string
	before, after *mutationdecode.Row
	bytes         int
}

type mutationHistoryImage struct {
	key           string
	before, after *mutationdecode.Row
}

func projectMutationHistory(registry *schema.Registry, row *mutationdecode.Row, fields []golem.FieldID) (*mutationdecode.Row, int, error) {
	if row == nil {
		return nil, 0, nil
	}
	if len(fields) > (maxMutationHistoryBytes-20)/21 {
		return nil, 0, &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
	}
	cells := make([]mutationdecode.Cell, 0, len(fields))
	size := 20 // model identity and field count
	for _, field := range fields {
		cell, present := row.Cell(policyir.FieldID(field))
		if !present {
			return nil, 0, fmt.Errorf("policy history: required persisted field %x is absent", field)
		}
		size += 21 // field identity, null flag and payload length
		if size > maxMutationHistoryBytes {
			return nil, 0, &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
		}
		if !cell.IsNull() {
			value, present := cell.PolicyValue()
			if !present {
				return nil, 0, fmt.Errorf("policy history: persisted scalar value is absent")
			}
			bytes, err := value.ScalarCanonicalSize()
			if err != nil {
				return nil, 0, err
			}
			if bytes > maxMutationHistoryBytes-size {
				return nil, 0, &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
			}
			size += bytes
		}
		cells = append(cells, cell)
	}
	projected, err := mutationdecode.NewRow(registry, row.ModelID(), cells)
	return &projected, size, err
}

// recordHistory is called immediately after a successful physical application,
// before observers or hooks can write again. A prior surviving transition owns
// the current before-image: a locked preimage can predate a nested hook write.
func (state *mutationState) recordHistory(ctx context.Context, registry *schema.Registry, provider policyir.Provider, before, after *mutationdecode.Row, continuation bool) error {
	image := after
	if image == nil {
		image = before
	}
	if state == nil || registry == nil || image == nil {
		return fmt.Errorf("policy history: transaction, registry and row transition are required")
	}
	fields := registry.PolicyCaptureFields(golem.ModelID(image.ModelID()))
	if len(fields) == 0 {
		return nil
	}
	key, err := mutationbatch.PrimaryKey(registry, provider, *image)
	if err != nil {
		return err
	}
	if before != nil {
		if before.ModelID() != image.ModelID() {
			return fmt.Errorf("policy history: transition crosses model identities")
		}
		beforeKey, err := mutationbatch.PrimaryKey(registry, provider, *before)
		if err != nil {
			return err
		}
		if beforeKey != key {
			return fmt.Errorf("policy history: transition changes its primary identity")
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return fmt.Errorf("policy history: transaction is already finalized")
	}
	previous := false
	for index := len(state.history) - 1; index >= 0; index-- {
		if state.history[index].key == key {
			before = state.history[index].after
			previous = true
			break
		}
	}
	if continuation && !previous {
		return fmt.Errorf("policy history: row stamp has no preceding application")
	}
	if before != nil && after != nil && before.ModelID() != after.ModelID() {
		return fmt.Errorf("policy history: transition crosses model identities")
	}
	if before != nil {
		beforeKey, err := mutationbatch.PrimaryKey(registry, provider, *before)
		if err != nil {
			return err
		}
		if beforeKey != key {
			return fmt.Errorf("policy history: transition changes its primary identity")
		}
	}
	first, firstBytes, err := projectMutationHistory(registry, before, fields)
	if err != nil {
		return err
	}
	last, lastBytes, err := projectMutationHistory(registry, after, fields)
	if err != nil {
		return err
	}
	bytes := len(key) + firstBytes + lastBytes
	if bytes > maxMutationHistoryBytes-state.historyBytes {
		return &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
	}
	if len(state.history) >= 2*MaxMutationTouchedRows {
		return &mutationLimitError{limit: golem.LimitTouchedRows, max: MaxMutationTouchedRows}
	}
	state.history = append(state.history, mutationHistoryTransition{owner: state.currentScope(ctx), key: key, before: first, after: last, bytes: bytes})
	state.historyBytes += bytes
	state.historyEpoch++
	return nil
}

// removeHistory runs under state.mu after a SQL savepoint rollback. Keeping
// contributions rather than one mutable final cell restores earlier surviving
// after-images when nested writes to the same row are abandoned.
func (state *mutationState) removeHistory(scope *mutationScope) {
	originalCount := len(state.history)
	remaining := make([]mutationHistoryTransition, 0, len(state.history))
	state.historyBytes = 0
	for _, entry := range state.history {
		if !scope.contains(entry.owner) {
			remaining = append(remaining, entry)
			state.historyBytes += entry.bytes
		}
	}
	state.history = remaining
	if len(remaining) != originalCount {
		state.historyEpoch++
	}
}

func cloneMutationHistoryRow(row *mutationdecode.Row) *mutationdecode.Row {
	if row == nil {
		return nil
	}
	copy := *row
	return &copy
}

func (state *mutationState) historyImages() []mutationHistoryImage {
	state.mu.Lock()
	defer state.mu.Unlock()
	positions := map[string]int{}
	result := make([]mutationHistoryImage, 0, len(state.history))
	for _, entry := range state.history {
		if index, present := positions[entry.key]; present {
			result[index].after = cloneMutationHistoryRow(entry.after)
		} else {
			positions[entry.key] = len(result)
			result = append(result, mutationHistoryImage{key: entry.key, before: cloneMutationHistoryRow(entry.before), after: cloneMutationHistoryRow(entry.after)})
		}
	}
	return result
}

func recordBatchHistory(ctx context.Context, state *mutationState, registry *schema.Registry, provider policyir.Provider, verification mutationbatch.Verification) error {
	for _, row := range verification.Rows() {
		before := row.Before()
		var after *mutationdecode.Row
		if value, present := row.After(); present {
			after = &value
		}
		if err := state.recordHistory(ctx, registry, provider, &before, after, false); err != nil {
			return err
		}
	}
	return nil
}
