package p8mutation

import "time"

func runtimeResourceMutations() []Mutation {
	providerGate := func(pkg, test string) Gate {
		return Gate{
			Directory: "go",
			Package:   pkg,
			Test:      test,
			Required:  []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
		}
	}
	return []Mutation{
		{
			Label:   "AFTER_COMMIT_ERROR_REWRITES_SUCCESS",
			Summary: "propagate an after-commit hook failure after the database result is already committed",
			Patches: []Patch{{
				Path: "go/runtime/mutation_state.go",
				Before: `	for _, work := range after {
		if err := invokeAfterCommitSafely(ctx, work.invoke); err != nil && report != nil {
			reportAfterCommitSafely(ctx, report, golem.RuntimeAfterCommitFailure(work.operation, work.model, err))
		}
	}
`,
				After: `	for _, work := range after {
		if err := invokeAfterCommitSafely(ctx, work.invoke); err != nil {
			panic(err)
		}
	}
`,
			}},
			Gate:    providerGate("./internal/p8oracle", "TestP8AfterCommitFailureDoesNotChangeCommittedResult"),
			Timeout: 15 * time.Minute,
		},
		{
			Label:   "COMPUTED_PRIVATE_DEPENDENCY_ESCAPES",
			Summary: "retain a private scalar beneath its null mask and promote it only in the computed dependency copy",
			Patches: []Patch{
				{
					Path: "go/golem/read.go",
					Before: `func RuntimeNullReadCell(field FieldID) RuntimeReadCell {
	return RuntimeReadCell{field: field, cell: readCell{state: ReadNull}}
}
`,
					After: `func RuntimeNullReadCell(field FieldID, retained ...RuntimeReadCell) RuntimeReadCell {
	masked := readCell{state: ReadNull}
	if len(retained) == 1 {
		masked = cloneReadCell(retained[0].cell)
		masked.state = ReadNull
	}
	return RuntimeReadCell{field: field, cell: masked}
}
`,
				},
				{
					Path:   "go/runtime/runtime.go",
					Before: "\t\t\t\tpublicCells = append(publicCells, golem.RuntimeNullReadCell(golem.FieldID(cell.FieldID())))\n",
					After:  "\t\t\t\tpublicCells = append(publicCells, golem.RuntimeNullReadCell(golem.FieldID(cell.FieldID()), cell.RuntimeCell()))\n",
				},
				{
					Path: "go/golem/computed_runtime_bridge.go",
					Before: `		if _, duplicate := result.cells[field]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: field %x is duplicated", field)
		}
		result.cells[field] = cloneReadCell(cell)
	}
	for _, relation := range relations {
`,
					After: `		if _, duplicate := result.cells[field]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: field %x is duplicated", field)
		}
		cell.state = ReadPresent
		result.cells[field] = cloneReadCell(cell)
	}
	for _, relation := range relations {
`,
				},
			},
			Gate:    providerGate("./internal/p8oracle", "TestP8ComputedAndBatchedDependencyDisclosureOracle"),
			Timeout: 15 * time.Minute,
		},
		{
			Label:   "HOOK_RESULT_BEFORE_VERIFICATION",
			Summary: "invoke result hooks from a partial statement image before identity and field verification",
			Patches: []Patch{{
				Path: "go/runtime/mutation_scalar.go",
				Before: `		if observer != nil {
			if err := observer(ctx, binding, uint32(index), result); err != nil {
				return scalarMutationExecution{}, err
			}
		}
	}
	if err := verifyScalarMutationIdentity(program, result.statements); err != nil {
		return scalarMutationExecution{}, err
	}
	if err := verifyScalarMutationFieldAuthorizations(registry, program, result.statements); err != nil {
		return scalarMutationExecution{}, err
	}
	if verified != nil {
		if err := verified(ctx, binding, program, result); err != nil {
			return scalarMutationExecution{}, err
		}
	}
`,
				After: `		if observer != nil {
			if err := observer(ctx, binding, uint32(index), result); err != nil {
				return scalarMutationExecution{}, err
			}
		}
		if verified != nil {
			if err := verified(ctx, binding, program, result); err != nil {
				return scalarMutationExecution{}, err
			}
		}
	}
	if err := verifyScalarMutationIdentity(program, result.statements); err != nil {
		return scalarMutationExecution{}, err
	}
	if err := verifyScalarMutationFieldAuthorizations(registry, program, result.statements); err != nil {
		return scalarMutationExecution{}, err
	}
`,
			}},
			Gate:    providerGate("./internal/p8oracle", "TestP8HookPhaseAndResultCrossSurfaceOracle"),
			Timeout: 15 * time.Minute,
		},
		{
			Label:   "RELATION_LOAD_N_PLUS_ONE",
			Summary: "replace one correlated relation payload with one child query per decoded parent",
			Patches: []Patch{{
				Path:   "go/runtime/runtime.go",
				Before: "\t\t\tchildren, correlatedErr := finishCorrelatedRelation(ctx, app, executor, operation, relation, result)\n",
				After:  "\t\t\tchildren, correlatedErr := executeToManyCorrelatedOracle(ctx, app, executor, operation, parent, relation, endpoint, result)\n",
			}},
			Gate:    providerGate("./internal/p8oracle/load", "TestP8StatementAndConnectionBudgetMatrix"),
			Timeout: 15 * time.Minute,
		},
		{
			Label:   "CANCEL_LEAKS_GOROUTINE_OR_CONNECTION",
			Summary: "detach a computed loader dispatch from the cancelled request context",
			Patches: []Patch{{
				Path:   "go/graphql/computed_execution.go",
				Before: "\t\tif err := entry.loader.Dispatch(ctx); err != nil {\n",
				After:  "\t\tif err := entry.loader.Dispatch(context.Background()); err != nil {\n",
			}},
			Gate:    providerGate("./internal/p8oracle/failure", "TestP8CancellationAndSlowClientRecoveryMatrix"),
			Timeout: 15 * time.Minute,
		},
		{
			Label:   "SLOW_SUBSCRIBER_DROPS_AND_CONTINUES",
			Summary: "drop an overflowing subscriber notice without terminating the slow subscription",
			Patches: []Patch{{
				Path: "go/internal/subscription/hub.go",
				Before: `	default:
		cleanup := hub.removeLocked(item, events.CodeSubscriptionOverflow)
		hub.mu.Unlock()
		runCleanup(cleanup)
		events.Observe(hub.config.Observer, context.Background(), notice.ModelID(), notice.Action(), events.ObservationOverflow, events.OutcomeFailure, "", 0, cap(item.queue), cap(item.queue), 0, 1)
`,
				After: `	default:
		hub.mu.Unlock()
		events.Observe(hub.config.Observer, context.Background(), notice.ModelID(), notice.Action(), events.ObservationOverflow, events.OutcomeFailure, "", 0, cap(item.queue), cap(item.queue), 0, 1)
`,
			}},
			Gate:    providerGate("./internal/p8oracle/event", "TestP8EventOverflowCancellationAndIdentityParity"),
			Timeout: 15 * time.Minute,
		},
	}
}
