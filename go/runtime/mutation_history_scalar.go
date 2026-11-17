package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/mutation/rowversion"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func recordScalarHistory(ctx context.Context, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, program mutationsql.Program, result scalarMutationExecution, row scalarMutationStatementResult) error {
	if binding == nil || !binding.mutation.enabled || len(registry.PolicyCaptureFields(golem.ModelID(model))) == 0 {
		return nil
	}
	continuation := false
	switch row.role {
	case mutationsql.ApplyCreate, mutationsql.ApplyUpdate, mutationsql.ApplyDelete:
	case mutationsql.StampVersion:
		// A create stamp updates the model row only on providers that do not
		// advance a global clock. The other stamp is bookkeeping, not evidence.
		if program.Operation() != mutationir.Create || rowversion.Advances(program.Provider()) {
			return nil
		}
		continuation = true
	default:
		return nil
	}
	image, err := mutationdecode.FromReadCells(registry, model, row.cells)
	if err != nil {
		return err
	}
	var before, after *mutationdecode.Row
	after = &image
	if row.role == mutationsql.ApplyDelete {
		before, after = &image, nil
	} else if row.role == mutationsql.ApplyUpdate {
		index, present := program.IdentityVerification().BeforeStatement()
		if !present {
			return fmt.Errorf("policy history: update has no before-image statement")
		}
		value, present := result.statement(index)
		if !present {
			return fmt.Errorf("policy history: update before-image is absent")
		}
		decoded, err := mutationdecode.FromReadCells(registry, model, value.cells)
		if err != nil {
			return err
		}
		before = &decoded
	} else if continuation {
		before = &image
	}
	state, err := binding.mutationState()
	if err != nil {
		return err
	}
	return state.recordHistory(ctx, registry, program.Provider(), before, after, continuation)
}
