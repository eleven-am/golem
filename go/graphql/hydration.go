package graphql

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/commitreport"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

type resultHydrator struct {
	compiler      *graphqloperation.Compiler
	caller        CallerExecution
	maxRows       int
	requestedRows int
}

func (executor *generatedExecutor[P]) newHydrator(caller CallerExecution) *resultHydrator {
	return &resultHydrator{compiler: executor.compiler, caller: caller, maxRows: executor.maxHydratedRows}
}

func (hydrator *resultHydrator) read(ctx context.Context, hydration *graphqloperation.Hydration) (graphqloperation.HydratedRows, error) {
	// One hydrator belongs to one serial operation execution, including its
	// computed fields and mutation roots. Reserve identities before SQL so
	// missing/denied rows count, while chunk retries do not count twice.
	if hydration.Len() > hydrator.maxRows-hydrator.requestedRows {
		return nil, golem.RuntimeLimitError("graphqlHydration", golem.ModelID{}, golem.LimitHydratedRows, hydrator.maxRows, "GraphQL result exceeds the operation's model hydration limit", nil)
	}
	hydrator.requestedRows += hydration.Len()
	rows := make([]golem.RuntimeModelRow, 0, hydration.Len())
	chunk := hydration.Chunk()
	for start := 0; start < hydration.Len(); {
		end := start + chunk
		if end > hydration.Len() {
			end = hydration.Len()
		}
		request, err := hydrator.compiler.HydrationRequest(hydration, start, end)
		if err != nil {
			return nil, err
		}
		batch, err := hydrator.caller.ExecuteFrozenRead(ctx, request)
		if err != nil {
			reduced, retry := reduceHydrationChunk(start, end, err)
			if retry {
				chunk = reduced - start
				continue
			}
			return nil, err
		}
		rows = append(rows, batch...)
		start = end
	}
	return hydration.Index(rows)
}

func (hydrator *resultHydrator) root(ctx context.Context, root graphqloperation.CustomRoot, value any) (any, int, error) {
	if objects, needed := hydrator.compiler.CustomObjectHydration(root); needed {
		prepared, err := objects.Collect(value)
		if err != nil {
			return nil, 0, err
		}
		rows, err := hydrator.objectRows(ctx, objects)
		if err != nil {
			return nil, 0, err
		}
		return objects.Restore(prepared, rows)
	}
	hydration, needed, err := hydrator.compiler.CustomHydration(root)
	if err != nil || !needed {
		return value, 0, err
	}
	if err := hydration.Collect(value); err != nil {
		return nil, 0, err
	}
	rows, err := hydrator.read(ctx, hydration)
	if err != nil {
		return nil, 0, err
	}
	hydrated, absent, err := hydration.Restore(value, rows)
	if err == nil && hydration.AbsentIsMissing(absent) && root.Kind == compilerir.CustomOperationQuery {
		return nil, absent, golem.RuntimeOperationError(golem.CodeNotFound, "customQuery", golem.ModelID{}, golem.FieldID{}, "record not found", nil)
	}
	return hydrated, absent, err
}

func (hydrator *resultHydrator) objectRows(ctx context.Context, objects *graphqloperation.ObjectHydration) ([]graphqloperation.HydratedRows, error) {
	groups := objects.Groups()
	rows := make([]graphqloperation.HydratedRows, len(groups))
	for i, group := range groups {
		var err error
		rows[i], err = hydrator.read(ctx, group)
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func reduceHydrationChunk(start, end int, err error) (int, bool) {
	if end-start <= 1 || !readsql.StatementCapacityExceeded(err) {
		return end, false
	}
	return start + (end-start)/2, true
}

type unreadableResult struct{ count int }

func (failure *unreadableResult) Error() string {
	return fmt.Sprintf("%d result rows are not readable by the caller", failure.count)
}

func presentCommittedResult(ctx context.Context, label string, cause error, path []any, report func(context.Context, error)) Error {
	message := label + " committed, but its result is unavailable"
	commitreport.From(ctx).Committed()
	var unreadable *unreadableResult
	var failure *golem.Error
	var committed error
	switch {
	case errors.As(cause, &unreadable):
		committed = golem.RuntimeCommittedResultError("customMutation", golem.ReasonResultNotReadable, unreadable.count, "", 0, message, cause)
	case errors.As(cause, &failure) && failure.Code == golem.CodeLimitExceeded:
		if report != nil {
			report(ctx, cause)
		}
		committed = golem.RuntimeCommittedResultError("customMutation", golem.ReasonLimitExceeded, 0, failure.Limit, failure.LimitMax, message, cause)
	case errors.As(cause, &failure):
		committed = golem.RuntimeCommittedResultError("customMutation", "", 0, "", 0, message, cause)
	default:
		if report != nil {
			report(ctx, cause)
		}
		committed = golem.RuntimeCommittedResultError("customMutation", "", 0, "", 0, message, cause)
	}
	return PresentError(ctx, committed, path, report)
}
