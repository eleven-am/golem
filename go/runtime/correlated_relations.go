package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func correlatedPayloadAvailable(rows []executedRow, relation readplan.Relation) bool {
	for index := range rows {
		if rows[index].correlated == nil {
			return false
		}
		if _, ok := rows[index].correlated[relation.FieldID()]; !ok {
			return false
		}
	}
	return true
}

// finishCorrelatedRelation materializes nested graphs once over the flattened
// child set, preserving the one-statement immediate relation while allowing
// deeper relations to use bounded batches. It never falls back per parent.
func finishCorrelatedRelation[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, operation golem.ReadOperation, relation readplan.Relation, parents []executedRow) ([][]executedRow, error) {
	if !correlatedPayloadAvailable(parents, relation) {
		return nil, fmt.Errorf("P3_RUNTIME_CORRELATED: relation %x has no decoded correlated payload", relation.RelationID())
	}
	result := make([][]executedRow, len(parents))
	sizes := make([]int, len(parents))
	total := 0
	for index := range parents {
		children := parents[index].correlated[relation.FieldID()]
		if err := enforceDecodedResultLimit(operation, relation.Child(), golem.FieldID(relation.FieldID()), len(children)); err != nil {
			return nil, err
		}
		sizes[index] = len(children)
		total += len(children)
	}
	flat := make([]executedRow, 0, total)
	for index := range parents {
		flat = append(flat, parents[index].correlated[relation.FieldID()]...)
	}
	finished, err := finishPlanRows(ctx, app, executor, operation, relation.Child(), flat)
	if err != nil {
		return nil, err
	}
	offset := 0
	for index, size := range sizes {
		result[index] = append(make([]executedRow, 0, size), finished[offset:offset+size]...)
		offset += size
	}
	return result, nil
}
