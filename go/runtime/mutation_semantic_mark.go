package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/jmoiron/sqlx"
)

// semanticMark is one record whose owner row was written inside the current
// transaction. It carries no evidence that an indexed field changed; the drain
// decides that from the recomputed content hash, so a spurious mark costs one
// hash and no provider call.
type semanticMark struct {
	model    golem.ModelID
	key      string
	identity []any
}

// semanticMarkIdentity is the comparable half of a mark. The identity values
// themselves are not comparable, so deduplication keys on the record key, which
// is a total encoding of exactly those values.
type semanticMarkIdentity struct {
	model golem.ModelID
	key   string
}

// markScalarSemanticRecord is the scalar kernel's only marking seam. It runs
// after identity verification, on the verified post-image statement.
//
// The record key is the model's PRIMARY key, deliberately not the program's
// identity verification field list. Those two differ: writing any field that
// belongs to any identity makes the plan IdentityMayChange, and the renderer
// then widens verification to the primary key plus every unique key's fields.
// Shadow storage mirrors the primary key alone, so the widened list cannot be
// written and would refuse an ordinary update of a unique indexed field.
func markScalarSemanticRecord(state *mutationState, registry *schema.Registry, model policyir.ModelID, program mutationsql.Program, result scalarMutationExecution) error {
	verification := program.IdentityVerification()
	primary, err := semanticPrimaryKeyFields(registry, model)
	if err != nil {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, verification.AfterStatement(), "semantic primary identity is absent", err)
	}
	mark := func(statementIndex uint32) error {
		statement, ok := result.statement(statementIndex)
		if !ok {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statementIndex, "semantic identity statement is absent", nil)
		}
		identity, identityErr := semanticIdentityFromCells(primary, statement.cells)
		if identityErr != nil {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statementIndex, "semantic record identity could not be projected", identityErr)
		}
		key, keyErr := semantickey.Encode(identity)
		if keyErr != nil {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statementIndex, "semantic record key could not be encoded", keyErr)
		}
		return state.markSemantic(golem.ModelID(model), key, identity)
	}
	if before, ok := verification.BeforeStatement(); ok {
		if err := mark(before); err != nil {
			return err
		}
	}
	return mark(verification.AfterStatement())
}

// semanticPrimaryKeyFields is the one ordering that matters. Shadow storage
// mirrors the owner's physical primary key, which is lowered from this exact
// declaration order, so a key encoded in any other order names a record no
// drain can find.
func semanticPrimaryKeyFields(registry *schema.Registry, model policyir.ModelID) ([]policyir.FieldID, error) {
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: registry does not own model %x", model)
	}
	primary := metadata.PrimaryKey()
	if len(primary) == 0 {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: model %x has no primary identity", model)
	}
	fields := make([]policyir.FieldID, len(primary))
	for index, field := range primary {
		fields[index] = policyir.FieldID(field)
	}
	return fields, nil
}

// markBatchSemanticRecords marks every row the batch verification proved it
// touched. One statement can touch many rows, so the buffer grows per row while
// the drain job it schedules stays one per index per transaction.
func markBatchSemanticRecords(state *mutationState, model policyir.ModelID, primary []policyir.FieldID, verification mutationbatch.Verification) error {
	for _, row := range verification.Rows() {
		images := []mutationdecode.Row{row.Before()}
		if after, present := row.After(); present {
			images = append(images, after)
		}
		for _, image := range images {
			identity, err := semanticIdentityFromRow(primary, image)
			if err != nil {
				return err
			}
			key, err := semantickey.Encode(identity)
			if err != nil {
				return err
			}
			if err := state.markSemantic(golem.ModelID(model), key, identity); err != nil {
				return err
			}
		}
	}
	return nil
}

func semanticIdentityFromRow(fields []policyir.FieldID, row mutationdecode.Row) ([]any, error) {
	values := make([]any, len(fields))
	for index, field := range fields {
		cell, ok := row.Cell(field)
		if !ok {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity field %x is absent from the verified image", field)
		}
		value, err := semanticMarkValue(cell)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

func semanticIdentityFromCells(fields []policyir.FieldID, cells []readdecode.Cell) ([]any, error) {
	indexed := make(map[policyir.FieldID]readdecode.Cell, len(cells))
	for _, cell := range cells {
		indexed[cell.FieldID()] = cell
	}
	values := make([]any, len(fields))
	for index, field := range fields {
		cell, ok := indexed[field]
		if !ok {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity field %x is absent from the verified projection", field)
		}
		exact, err := exactMutationCell(cell)
		if err != nil {
			return nil, err
		}
		value, err := semanticMarkValue(exact)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

// semanticMarkValue projects one authorized identity cell into the plain Go
// value the shared record-key encoder canonicalizes. Going through the logical
// value rather than the driver's scan representation is what makes a key
// written by the mutation kernel equal to the key the refresh scan derives
// from the same row.
func semanticMarkValue(cell mutationdecode.Cell) (any, error) {
	if cell.IsNull() {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity field %x is NULL", cell.FieldID())
	}
	value, ok := cell.PolicyValue()
	if !ok {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity field %x has no exact value", cell.FieldID())
	}
	result, ok := semanticRecordKeyValue(value)
	if !ok {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity field %x has unsupported kind %d", cell.FieldID(), value.Kind())
	}
	return result, nil
}

func semanticRecordKeyValue(value policyir.Value) (any, bool) {
	switch value.Kind() {
	case policyir.ValueBool:
		result, _ := value.Bool()
		return result, true
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		result, _ := value.Signed()
		return result, true
	case policyir.ValueString:
		result, _ := value.Text()
		return result, true
	case policyir.ValueBytes:
		result, _ := value.Bytes()
		return result, true
	case policyir.ValueUUID:
		result, _ := value.UUID()
		return result, true
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return time.Unix(seconds, int64(nanos)).UTC(), true
	default:
		return nil, false
	}
}

// modelSemanticIndexed is the one registry lookup the nested builders need.
// Root plans carry the decision from BuildRoot; nested nodes assemble their own
// PlanInput and must stamp the identical fact rather than re-deciding it.
func modelSemanticIndexed(registry *schema.Registry, model policyir.ModelID) bool {
	metadata, ok := registry.Model(golem.ModelID(model))
	return ok && metadata.SemanticIndexed()
}

// flushSemanticMarks writes the transaction's marks into every semantic index
// the mutated models own and schedules one deduped drain per index, both on the
// executor the write itself is using. It refuses rather than silently skipping:
// App.Open already guarantees a semantic-indexed application configured a
// queue, so an unreachable queue here is a broken invariant, and marks that
// nothing ever drains are exactly the silent staleness this design removes.
func (app *App[P, A]) flushSemanticMarks(ctx context.Context, executor sqlx.ExecerContext, marks []semanticMark) error {
	if len(marks) == 0 {
		return nil
	}
	if app == nil || app.semantic == nil {
		return fmt.Errorf("P9_SEMANTIC_MARK: semantic marks were recorded without a semantic runtime")
	}
	queueExecutor, ok := executor.(queueprovider.Executor)
	if !ok {
		return fmt.Errorf("P9_SEMANTIC_MARK: mutation executor cannot record a drain job")
	}
	for _, model := range semanticMarkedModels(marks) {
		// The mark is decided from the compiler's logical model and the write is
		// carried out against the physical index inventory. They are derived from
		// the same compilation, so a disagreement is a lowering fault; letting it
		// pass would drop every mark for that model with nothing to show for it.
		if !app.semanticIndexed(model) {
			return fmt.Errorf("P9_SEMANTIC_MARK: model %x is semantic-indexed but owns no physical index", model)
		}
		records := make([]semanticruntime.MarkRecord, 0, len(marks))
		for _, mark := range marks {
			if mark.model == model {
				records = append(records, semanticruntime.MarkRecord{Key: mark.key, Identity: mark.identity})
			}
		}
		if err := app.semantic.MarkStale(ctx, executor, semanticIndexModel(model), app.mutationLimits.statementParameters, records); err != nil {
			return err
		}
		if err := app.enqueueSemanticDrains(ctx, queueExecutor, model); err != nil {
			return err
		}
	}
	return nil
}

// semanticMarkedModels returns the marked models in first-mark order, so one
// transaction's shadow writes and drain jobs are issued in a stable order
// regardless of map iteration.
func semanticMarkedModels(marks []semanticMark) []golem.ModelID {
	seen := make(map[golem.ModelID]struct{}, len(marks))
	result := make([]golem.ModelID, 0, len(marks))
	for _, mark := range marks {
		if _, duplicate := seen[mark.model]; duplicate {
			continue
		}
		seen[mark.model] = struct{}{}
		result = append(result, mark.model)
	}
	return result
}
