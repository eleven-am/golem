package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/concurrencyclaim"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// scalarConcurrencyClaim retains only the closed comparable claim. The raw
// caller value is never recovered for a SQL bind; after the authorized row is
// locked, the observed database value owns the CAS argument.
type scalarConcurrencyClaim struct {
	existing concurrencyclaim.ExistingVersion
}

type observedConcurrencyClaim interface {
	validateObserved(scalarMutationStatementResult, policyir.FieldID, mutationir.Operation) error
}

type existingExpectationConcurrencyClaim struct {
	expectation concurrencyclaim.ConcurrencyExpectation
}

type absentUpsertFaultStage uint8

const (
	absentUpsertFaultBegin absentUpsertFaultStage = iota + 1
	absentUpsertFaultGuard
	absentUpsertFaultAuthorizedProbe
	absentUpsertFaultExactProbe
	absentUpsertFaultBeforeCreate
	absentUpsertFaultCleanup
	absentUpsertFaultFinish
)

type absentUpsertFault func(absentUpsertFaultStage, *sqlxUpsertAttempt) error
type absentUpsertFaultContextKey struct{}

type concurrencyAbortFailure struct{ cause error }

func (failure *concurrencyAbortFailure) Error() string {
	return "optimistic-concurrency attempt abort failed"
}
func (failure *concurrencyAbortFailure) Unwrap() error { return failure.cause }
func (*concurrencyAbortFailure) UntrustedRetryCause()  {}

type absentCreateRollbackFailure struct{ cause error }

func (failure *absentCreateRollbackFailure) Error() string {
	return "expect-absent create savepoint rollback failed"
}
func (failure *absentCreateRollbackFailure) Unwrap() error { return failure.cause }
func (*absentCreateRollbackFailure) UntrustedRetryCause()  {}

// contextWithAbsentUpsertFault is an unexported deterministic provider-fault
// seam used only by runtime acceptance tests. Production contexts never carry
// this key; the mutation protocol remains otherwise unchanged.
func contextWithAbsentUpsertFault(ctx context.Context, fault absentUpsertFault) context.Context {
	if ctx == nil || fault == nil {
		return ctx
	}
	return context.WithValue(ctx, absentUpsertFaultContextKey{}, fault)
}

func applyAbsentUpsertFault(ctx context.Context, stage absentUpsertFaultStage, attempt *sqlxUpsertAttempt) error {
	if ctx == nil {
		return nil
	}
	fault, _ := ctx.Value(absentUpsertFaultContextKey{}).(absentUpsertFault)
	if fault == nil {
		return nil
	}
	return fault(stage, attempt)
}

func freezeScalarConcurrencyClaim(value golem.ExistingVersion) (scalarConcurrencyClaim, error) {
	claim := concurrencyclaim.ExistingVersion(value)
	if !concurrencyclaim.ValidExistingVersion(claim) {
		return scalarConcurrencyClaim{}, fmt.Errorf("P4_RUNTIME_CONCURRENCY: existing-version expectation is invalid")
	}
	return scalarConcurrencyClaim{existing: claim}, nil
}

func observedConcurrencyVersion(result scalarMutationStatementResult, field policyir.FieldID, operation mutationir.Operation) (int64, error) {
	var observed int64
	found := false
	for _, cell := range result.cells {
		if cell.FieldID() != field || cell.IsNull() {
			continue
		}
		value, ok := cell.PolicyValue()
		if !ok {
			break
		}
		observed, ok = value.Signed()
		found = ok && value.Kind() == policyir.ValueInt64
		break
	}
	if !found || observed < 1 {
		return 0, scalarMutationError(operation, scalarMutationInvariant, mutationsql.SelectPreImage, 0, "locked concurrency token is invalid", nil)
	}
	if observed == math.MaxInt64 {
		return 0, scalarMutationError(operation, scalarMutationConflict, mutationsql.SelectPreImage, 0, "mutation concurrency token is exhausted", nil)
	}
	return observed, nil
}

func (claim scalarConcurrencyClaim) validateObserved(result scalarMutationStatementResult, field policyir.FieldID, operation mutationir.Operation) error {
	observed, err := observedConcurrencyVersion(result, field, operation)
	if err != nil {
		return err
	}
	if claim.existing != concurrencyclaim.NewExistingVersion(observed) {
		return scalarMutationError(operation, scalarMutationConflict, mutationsql.SelectPreImage, 0, "mutation expectation is stale", nil)
	}
	return nil
}

func (claim existingExpectationConcurrencyClaim) validateObserved(result scalarMutationStatementResult, field policyir.FieldID, operation mutationir.Operation) error {
	observed, err := observedConcurrencyVersion(result, field, operation)
	if err != nil {
		return err
	}
	if claim.expectation != concurrencyclaim.NewExistingExpectation(observed) {
		return scalarMutationError(operation, scalarMutationConflict, mutationsql.SelectPreImage, 0, "mutation expectation is stale", nil)
	}
	return nil
}

func requireVersionedModel(registry *schema.Registry, model golem.ModelID, operation string) error {
	if registry == nil {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("optimistic-concurrency registry is absent"))
	}
	fact, ok := registry.Model(model)
	if !ok {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("optimistic-concurrency model is absent"))
	}
	if _, enabled := fact.OptimisticConcurrency(); !enabled {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("model does not declare optimistic concurrency"))
	}
	return nil
}

func refuseLegacyVersionedMutation(registry *schema.Registry, model golem.ModelID, operation string) error {
	if registry == nil {
		return nil
	}
	fact, ok := registry.Model(model)
	if !ok {
		return nil
	}
	if _, enabled := fact.OptimisticConcurrency(); !enabled {
		return nil
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("optimistic-concurrency mutation requires an explicit expectation"))
}

func equalFrozenMutationTarget(left, right golem.FrozenMutationTarget) bool {
	l, r := left.Selector(), right.Selector()
	if l.ModelID() != r.ModelID() || l.KeyID() != r.KeyID() || !equalGolemFieldIDs(l.Fields(), r.Fields()) || !bytes.Equal(left.SelectorPredicate().CanonicalBytes(), right.SelectorPredicate().CanonicalBytes()) {
		return false
	}
	leftGuard, leftHasGuard := left.Guard()
	rightGuard, rightHasGuard := right.Guard()
	return leftHasGuard == rightHasGuard && (!leftHasGuard || bytes.Equal(leftGuard.CanonicalBytes(), rightGuard.CanonicalBytes()))
}

func equalGolemFieldIDs(left, right []golem.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// CallerUpdateVersioned is the typed generated-client ABI for one caller CAS.
func CallerUpdateVersioned[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller == nil || caller.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if err := requireVersionedModel(caller.app.registry, descriptor.Metadata().ModelID(), "update"); err != nil {
		return golem.Row[M]{}, err
	}
	claim, err := freezeScalarConcurrencyClaim(expected)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Update, descriptor.Metadata().ModelID(), err)
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(frozenInput.Relations()) != 0 {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Update, descriptor.Metadata().ModelID(), fmt.Errorf("versioned nested mutation requires an expectation for every written row"))
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerVersionedRootScalar(ctx, caller, descriptor, mutationir.Update, &frozenInput, frozenTarget, claim, projection, mutationir.Update)
}

// CallerDeleteVersioned is the typed generated-client ABI for one caller CAS.
func CallerDeleteVersioned[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller == nil || caller.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if err := requireVersionedModel(caller.app.registry, descriptor.Metadata().ModelID(), "delete"); err != nil {
		return golem.Row[M]{}, err
	}
	claim, err := freezeScalarConcurrencyClaim(expected)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Delete, descriptor.Metadata().ModelID(), err)
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerVersionedRootScalar(ctx, caller, descriptor, mutationir.Delete, nil, frozenTarget, claim, projection, mutationir.Delete)
}

func SystemUpdateVersioned[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "system execution is unavailable", nil)
	}
	if err := requireVersionedModel(system.app.registry, descriptor.Metadata().ModelID(), "update"); err != nil {
		return golem.Row[M]{}, err
	}
	claim, err := freezeScalarConcurrencyClaim(expected)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Update, descriptor.Metadata().ModelID(), err)
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(frozenInput.Relations()) != 0 {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Update, descriptor.Metadata().ModelID(), fmt.Errorf("versioned nested mutation requires an expectation for every written row"))
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemVersionedRootScalar(ctx, system, descriptor, mutationir.Update, &frozenInput, frozenTarget, claim, projection, mutationir.Update)
}

func SystemDeleteVersioned[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "system execution is unavailable", nil)
	}
	if err := requireVersionedModel(system.app.registry, descriptor.Metadata().ModelID(), "delete"); err != nil {
		return golem.Row[M]{}, err
	}
	claim, err := freezeScalarConcurrencyClaim(expected)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Delete, descriptor.Metadata().ModelID(), err)
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemVersionedRootScalar(ctx, system, descriptor, mutationir.Delete, nil, frozenTarget, claim, projection, mutationir.Delete)
}

func executeSystemVersionedRootScalar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target golem.FrozenMutationTarget, claim observedConcurrencyClaim, projection scalarMutationProjection, observedOperation mutationir.Operation) (result golem.Row[M], resultErr error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	ctx, observation, deferred := beginDeferredExecutionObservation(ctx, system.app, system.executor, golem.ModelID(model), observe.KindMutation, mutationObservationOperation(observedOperation))
	defer func() { finishDeferredObservation(observation, deferred, resultErr) }()
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	program, err := prepareSystemVersionedScalarProgram(system, scalarMutationPrepareRequest{operation: operation, model: model, input: input, target: &target, result: requirements, runtimeValues: newMutationRuntimeValues()})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	return executeVersionedScalarKernel(ctx, system.app, system.executor, descriptor, projection, program, claim, nil, func(context.Context, *sqlxUpsertAttempt) (mutationsql.Program, error) { return program, nil }, 1)
}

func CallerTxUpdateVersioned[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerUpdateVersioned(ctx, transaction.caller, descriptor, target, expected, input, projections...)
}

func CallerTxDeleteVersioned[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerDeleteVersioned(ctx, transaction.caller, descriptor, target, expected, projections...)
}

func SystemTxUpdateVersioned[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemUpdateVersioned(ctx, transaction.system, descriptor, target, expected, input, projections...)
}

func SystemTxDeleteVersioned[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ExistingVersion, projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemDeleteVersioned(ctx, transaction.system, descriptor, target, expected, projections...)
}

func CallerUpsertVersioned[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ConcurrencyExpectation, create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller == nil || caller.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if err := requireVersionedModel(caller.app.registry, descriptor.Metadata().ModelID(), "upsert"); err != nil {
		return golem.Row[M]{}, err
	}
	expectation := concurrencyclaim.ConcurrencyExpectation(expected)
	if !concurrencyclaim.ValidExpectation(expectation) {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, descriptor.Metadata().ModelID(), fmt.Errorf("optimistic-concurrency upsert expectation is invalid"))
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, frozenCreate, frozenUpdate, err := freezeVersionedUpsertInputs(caller.app.registry, target, create, update)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, descriptor.Metadata().ModelID(), err)
	}
	if !concurrencyclaim.IsAbsent(expectation) {
		return executeCallerExistingVersionedUpsert(ctx, caller, descriptor, frozenTarget, frozenUpdate, existingExpectationConcurrencyClaim{expectation: expectation}, projection)
	}
	return executeCallerAbsentVersionedUpsert(ctx, caller, descriptor, frozenTarget, frozenCreate, frozenUpdate, projection)
}

func SystemUpsertVersioned[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ConcurrencyExpectation, create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "system execution is unavailable", nil)
	}
	if err := requireVersionedModel(system.app.registry, descriptor.Metadata().ModelID(), "upsert"); err != nil {
		return golem.Row[M]{}, err
	}
	expectation := concurrencyclaim.ConcurrencyExpectation(expected)
	if !concurrencyclaim.ValidExpectation(expectation) {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, descriptor.Metadata().ModelID(), fmt.Errorf("optimistic-concurrency upsert expectation is invalid"))
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, frozenCreate, frozenUpdate, err := freezeVersionedUpsertInputs(system.app.registry, target, create, update)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, descriptor.Metadata().ModelID(), err)
	}
	if !concurrencyclaim.IsAbsent(expectation) {
		return executeSystemExistingVersionedUpsert(ctx, system, descriptor, frozenTarget, frozenUpdate, existingExpectationConcurrencyClaim{expectation: expectation}, projection)
	}
	return executeSystemAbsentVersionedUpsert(ctx, system, descriptor, frozenTarget, frozenCreate, frozenUpdate, projection)
}

func CallerTxUpsertVersioned[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ConcurrencyExpectation, create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerUpsertVersioned(ctx, transaction.caller, descriptor, target, expected, create, update, projections...)
}

func SystemTxUpsertVersioned[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], expected golem.ConcurrencyExpectation, create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemUpsertVersioned(ctx, transaction.system, descriptor, target, expected, create, update, projections...)
}

func freezeVersionedUpsertInputs[M any](registry *schema.Registry, target golem.MutationTarget[M], create golem.CreateInput[M], update golem.UpdateInput[M]) (golem.FrozenMutationTarget, golem.FrozenMutationInput, golem.FrozenMutationInput, error) {
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, err
	}
	frozenCreate, err := golem.RuntimeFreezeCreateInput(create)
	if err != nil {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, err
	}
	frozenUpdate, err := golem.RuntimeFreezeUpdateInput(update)
	if err != nil {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, err
	}
	if len(frozenCreate.Relations()) != 0 || len(frozenUpdate.Relations()) != 0 {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, fmt.Errorf("versioned nested upsert requires an expectation for every written row")
	}
	if _, err := mutationbind.CreateInput(frozenCreate, registry); err != nil {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, err
	}
	if _, err := mutationbind.UpdateInput(frozenUpdate, registry); err != nil {
		return golem.FrozenMutationTarget{}, golem.FrozenMutationInput{}, golem.FrozenMutationInput{}, err
	}
	return frozenTarget, frozenCreate, frozenUpdate, nil
}

func rebaseOptimisticUpsertError(model golem.ModelID, err error) error {
	if err == nil {
		return nil
	}
	var public *golem.Error
	if errors.As(err, &public) {
		return golem.RuntimeOperationError(public.Code, "upsert", model, public.Field, public.Message, err)
	}
	return publicRootUpsertError(model, err)
}

func executeObservedVersionedUpsertRetries[P, A, M any](ctx context.Context, app *App[P, A], source *executionBinding, model golem.ModelID, execute func(context.Context, uint32) (golem.Row[M], error)) (row golem.Row[M], resultErr error) {
	if app == nil || source == nil || execute == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", model, golem.FieldID{}, "mutation request is invalid", nil)
	}
	ctx, observation, deferred := beginDeferredExecutionObservation(ctx, app, source, model, observe.KindMutation, observe.OperationMutationUpsert)
	defer func() { finishDeferredObservation(observation, deferred, resultErr) }()
	maxAttempts := uint32(app.mutationLimits.upsertAttempts)
	if source.scoped {
		maxAttempts = 1
	}
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	var last error
	for attempt := uint32(1); attempt <= maxAttempts; attempt++ {
		row, err := execute(ctx, attempt)
		if err == nil {
			return row, nil
		}
		last = err
		if mutationupsert.UniqueCollision(err) {
			return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", err)
		}
		if !mutationupsert.RetryableInterference(err) {
			return golem.Row[M]{}, err
		}
	}
	return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", last)
}

func executeCallerAbsentVersionedUpsert[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, create, update golem.FrozenMutationInput, projection scalarMutationProjection) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	prepared, err := prepareCallerRootUpsert(caller, rootUpsertPrepareRequest{
		model: model, target: target, create: create, update: update, result: requirements,
		runtimeValues: newMutationRuntimeValues(), concurrencyAbsent: true,
	})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	hooks := &callerMutationHookExecution[A]{bindings: caller.app.bindings, actor: caller.actor, executor: func(binding *executionBinding) golem.HookExecutor { return newCallerHookExecutor(caller, binding) }}
	return executeObservedVersionedUpsertRetries(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), func(attemptContext context.Context, attempt uint32) (golem.Row[M], error) {
		return executeAbsentVersionedUpsertAttempt(attemptContext, caller.app, caller.executor, descriptor, projection, prepared, caller.policies, hooks, attempt)
	})
}

func executeSystemAbsentVersionedUpsert[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, create, update golem.FrozenMutationInput, projection scalarMutationProjection) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	prepared, err := prepareSystemRootUpsert(system, rootUpsertPrepareRequest{
		model: model, target: target, create: create, update: update, result: requirements,
		runtimeValues: newMutationRuntimeValues(), concurrencyAbsent: true,
	})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	return executeObservedVersionedUpsertRetries(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), func(attemptContext context.Context, attempt uint32) (golem.Row[M], error) {
		return executeAbsentVersionedUpsertAttempt[P, A](attemptContext, system.app, system.executor, descriptor, projection, prepared, nil, nil, attempt)
	})
}

func executeAbsentVersionedUpsertAttempt[P, A, M any](ctx context.Context, app *App[P, A], source *executionBinding, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, prepared preparedRuntimeUpsert, policies mutationplan.PolicySet, hooks *callerMutationHookExecution[A], attemptOrdinal uint32) (row golem.Row[M], resultErr error) {
	backend := sqlxUpsertBackend{database: app.database, provider: app.provider, binding: source, mutation: mutationConfig(app, source)}
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultBegin, nil); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	generic, err := backend.Begin(ctx, prepared.kernel.TransactionRequirement(), attemptOrdinal)
	if err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	attempt, ok := generic.(*sqlxUpsertAttempt)
	if !ok || attempt == nil {
		return golem.Row[M]{}, publicRootUpsertError(descriptor.Metadata().ModelID(), fmt.Errorf("expect-absent upsert received a foreign transaction"))
	}
	committed := false
	defer func() {
		if !committed {
			if abortErr := attempt.Abort(); abortErr != nil {
				row = golem.Row[M]{}
				resultErr = golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "mutation could not be completed", errors.Join(resultErr, &concurrencyAbortFailure{cause: abortErr}))
			}
		}
	}()
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultGuard, attempt); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	rows, err := attempt.Query(ctx, prepared.kernel.GuardStatement())
	if err != nil || rows != 1 {
		if err == nil {
			err = fmt.Errorf("expect-absent selector guard returned %d rows", rows)
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultAuthorizedProbe, attempt); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	rows, err = attempt.Query(ctx, prepared.kernel.ProbeStatement())
	if err != nil || rows > 1 {
		if err == nil {
			err = fmt.Errorf("expect-absent authorized probe returned %d rows", rows)
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	if rows == 1 {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeConflict, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "mutation conflicted", nil)
	}
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultExactProbe, attempt); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	rows, err = attempt.Query(ctx, prepared.kernel.ExactSelectorProbeStatement())
	if err != nil || rows > 1 {
		if err == nil {
			err = fmt.Errorf("expect-absent private selector probe returned %d rows", rows)
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	if rows == 1 {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeNotFound, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "record not found", nil)
	}
	executor := runtimeUpsertBranchExecutor[P, A, M]{app: app, descriptor: descriptor, projection: projection, prepared: prepared, binding: source, policies: policies, hooks: hooks}
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultBeforeCreate, attempt); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	value, err := executeAbsentCreateSavepoint(ctx, attempt, func() (any, error) {
		return executor.ExecuteBranch(ctx, attempt, prepared.kernel.CreateBranch(), mutationupsert.NewFrozenValues(nil))
	})
	if err != nil {
		if mutationupsert.UniqueCollision(err) {
			return reclassifyAbsentCollision[M](ctx, attempt, prepared.kernel, descriptor.Metadata().ModelID())
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	if cleanup, present := prepared.kernel.CleanupStatement(); present {
		if err := applyAbsentUpsertFault(ctx, absentUpsertFaultCleanup, attempt); err != nil {
			return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
		}
		rows, err = attempt.Query(ctx, cleanup)
		if err != nil || rows != 1 {
			if err == nil {
				err = fmt.Errorf("expect-absent selector guard cleanup returned %d rows", rows)
			}
			return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
		}
	}
	if err := applyAbsentUpsertFault(ctx, absentUpsertFaultFinish, attempt); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	if err := attempt.Finish(ctx); err != nil {
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(descriptor.Metadata().ModelID(), err)
	}
	committed = true
	row, ok = value.(golem.Row[M])
	if !ok {
		return golem.Row[M]{}, publicRootUpsertError(descriptor.Metadata().ModelID(), fmt.Errorf("expect-absent create branch returned an invalid result"))
	}
	return row, nil
}

func executeAbsentCreateSavepoint(ctx context.Context, attempt *sqlxUpsertAttempt, execute func() (any, error)) (any, error) {
	if attempt == nil || attempt.binding == nil || execute == nil {
		return nil, fmt.Errorf("expect-absent create savepoint is unavailable")
	}
	execer, ok := attempt.queryer.(sqlx.ExecerContext)
	if !ok {
		return nil, fmt.Errorf("expect-absent create queryer cannot own a savepoint")
	}
	state, err := attempt.binding.mutationState()
	if err != nil {
		return nil, err
	}
	scope, err := state.beginScope()
	if err != nil {
		return nil, err
	}
	const savepoint = `"golem_oc_absent_create"`
	if _, err := execer.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		_ = scope.rollback()
		return nil, err
	}
	value, executionErr := execute()
	if executionErr != nil {
		_, rollbackErr := execer.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+savepoint)
		_, releaseErr := execer.ExecContext(context.Background(), "RELEASE SAVEPOINT "+savepoint)
		scopeErr := scope.rollback()
		if rollbackErr != nil || releaseErr != nil || scopeErr != nil {
			return nil, &absentCreateRollbackFailure{cause: errors.Join(executionErr, rollbackErr, releaseErr, scopeErr)}
		}
		return nil, executionErr
	}
	if _, err := execer.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		_ = scope.rollback()
		return nil, err
	}
	if err := scope.release(); err != nil {
		return nil, err
	}
	return value, nil
}

func reclassifyAbsentCollision[M any](ctx context.Context, attempt *sqlxUpsertAttempt, program mutationupsert.Program, model golem.ModelID) (golem.Row[M], error) {
	rows, err := attempt.Query(ctx, program.ProbeStatement())
	if err != nil || rows > 1 {
		if err == nil {
			err = fmt.Errorf("expect-absent collision reach probe returned %d rows", rows)
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(model, err)
	}
	if rows == 1 {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", nil)
	}
	rows, err = attempt.Query(ctx, program.ExactSelectorProbeStatement())
	if err != nil || rows > 1 {
		if err == nil {
			err = fmt.Errorf("expect-absent collision private probe returned %d rows", rows)
		}
		return golem.Row[M]{}, publicAbsentUpsertExecutionError(model, err)
	}
	if rows == 1 {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeNotFound, "upsert", model, golem.FieldID{}, "record not found", nil)
	}
	return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", nil)
}

func publicAbsentUpsertExecutionError(model golem.ModelID, err error) error {
	var cleanup *absentCreateRollbackFailure
	if errors.As(err, &cleanup) {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", model, golem.FieldID{}, "mutation could not be completed", err)
	}
	if mutationupsert.RetryableInterference(err) {
		return golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", err)
	}
	var hook *mutationHookFailure
	if errors.As(err, &hook) {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", model, golem.FieldID{}, "mutation hook rejected the operation", err)
	}
	return publicRootUpsertError(model, err)
}

func executeCallerExistingVersionedUpsert[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, input golem.FrozenMutationInput, claim observedConcurrencyClaim, projection scalarMutationProjection) (golem.Row[M], error) {
	runtimeValues := newMutationRuntimeValues()
	program, hooks, afterPrecheck, err := prepareCallerVersionedScalarExecution(caller, descriptor, mutationir.Update, &input, target, projection, runtimeValues)
	if err != nil {
		return golem.Row[M]{}, rebaseOptimisticUpsertError(descriptor.Metadata().ModelID(), err)
	}
	return executeObservedVersionedUpsertRetries(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), func(attemptContext context.Context, attempt uint32) (golem.Row[M], error) {
		row, executionErr := executeVersionedScalarKernel(attemptContext, caller.app, caller.executor, descriptor, projection, program, claim, hooks, afterPrecheck, attempt)
		return row, rebaseOptimisticUpsertError(descriptor.Metadata().ModelID(), executionErr)
	})
}

func executeSystemExistingVersionedUpsert[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, input golem.FrozenMutationInput, claim observedConcurrencyClaim, projection scalarMutationProjection) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, rebaseOptimisticUpsertError(descriptor.Metadata().ModelID(), publicMutationPreparationError(mutationir.Update, golem.ModelID(model), err))
	}
	program, err := prepareSystemVersionedScalarProgram(system, scalarMutationPrepareRequest{operation: mutationir.Update, model: model, input: &input, target: &target, result: requirements, runtimeValues: newMutationRuntimeValues()})
	if err != nil {
		return golem.Row[M]{}, rebaseOptimisticUpsertError(descriptor.Metadata().ModelID(), publicMutationPreparationError(mutationir.Update, golem.ModelID(model), err))
	}
	return executeObservedVersionedUpsertRetries(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), func(attemptContext context.Context, attempt uint32) (golem.Row[M], error) {
		row, executionErr := executeVersionedScalarKernel(attemptContext, system.app, system.executor, descriptor, projection, program, claim, nil, func(context.Context, *sqlxUpsertAttempt) (mutationsql.Program, error) { return program, nil }, attempt)
		return row, rebaseOptimisticUpsertError(descriptor.Metadata().ModelID(), executionErr)
	})
}

func prepareCallerVersionedScalarExecution[P, A, M any](caller *Caller[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target golem.FrozenMutationTarget, projection scalarMutationProjection, runtimeValues *mutationRuntimeValues) (mutationsql.Program, *callerMutationHookExecution[A], func(context.Context, *sqlxUpsertAttempt) (mutationsql.Program, error), error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return mutationsql.Program{}, nil, nil, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	if runtimeValues == nil {
		runtimeValues = newMutationRuntimeValues()
	}
	request := scalarMutationPrepareRequest{operation: operation, model: model, input: input, target: &target, result: requirements, runtimeValues: runtimeValues}
	program, err := prepareCallerVersionedScalarProgram(caller, request)
	if err != nil {
		return mutationsql.Program{}, nil, nil, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	hooks := &callerMutationHookExecution[A]{bindings: caller.app.bindings, actor: caller.actor, executor: func(binding *executionBinding) golem.HookExecutor { return newCallerHookExecutor(caller, binding) }}
	afterPrecheck := func(kernelContext context.Context, _ *sqlxUpsertAttempt) (mutationsql.Program, error) {
		hookRequest, hookErr := scalarBeforeHookRequest(operation, golem.ModelID(model), input, &target)
		if hookErr != nil {
			return mutationsql.Program{}, hookErr
		}
		prepared := program
		validate := func(transformed golem.RuntimeMutationHookRequest) error {
			transformedInput, transformedTarget, partsErr := scalarRequestParts(transformed)
			if partsErr != nil {
				return partsErr
			}
			if transformedTarget == nil || !equalFrozenMutationTarget(target, *transformedTarget) {
				return fmt.Errorf("optimistic-concurrency before hook changed the immutable target")
			}
			if transformedInput != nil && len(transformedInput.Relations()) != 0 {
				return fmt.Errorf("versioned nested mutation requires an expectation for every written row")
			}
			next, prepareErr := prepareCallerVersionedScalarProgram(caller, scalarMutationPrepareRequest{operation: operation, model: model, input: transformedInput, target: transformedTarget, result: requirements, runtimeValues: runtimeValues})
			if prepareErr == nil {
				prepared = next
			}
			return prepareErr
		}
		hookContext := golem.RuntimeContextWithActor(kernelContext, caller.actor)
		hookContext, hookObservation := observeexec.BeginChild(hookContext, golem.ModelID(model), observe.KindHook, hookObservationOperation(hookRequest.Operation()), observe.PhaseBefore)
		transformed, hookErr := golem.RuntimeInvokeMutationBeforeHooks(hookContext, caller.app.bindings, hookRequest, validate)
		finishObservation(hookObservation, hookErr)
		if hookErr != nil {
			return mutationsql.Program{}, &mutationHookFailure{operation: hookRequest.Operation(), phase: golem.HookBefore, cause: hookErr}
		}
		if hookErr = validate(transformed); hookErr != nil {
			return mutationsql.Program{}, &mutationHookFailure{operation: hookRequest.Operation(), phase: golem.HookBefore, cause: hookErr}
		}
		return prepared, nil
	}
	return program, hooks, afterPrecheck, nil
}

func executeCallerVersionedRootScalar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target golem.FrozenMutationTarget, claim observedConcurrencyClaim, projection scalarMutationProjection, observedOperation mutationir.Operation) (result golem.Row[M], resultErr error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	ctx, observation, deferred := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, golem.ModelID(model), observe.KindMutation, mutationObservationOperation(observedOperation))
	defer func() { finishDeferredObservation(observation, deferred, resultErr) }()
	program, hooks, prepareAfterPrecheck, err := prepareCallerVersionedScalarExecution(caller, descriptor, operation, input, target, projection, newMutationRuntimeValues())
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeVersionedScalarKernel(ctx, caller.app, caller.executor, descriptor, projection, program, claim, hooks, prepareAfterPrecheck, 1)
}

func executeVersionedScalarKernel[P, A, M any](ctx context.Context, app *App[P, A], source *executionBinding, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, initial mutationsql.Program, claim observedConcurrencyClaim, hooks *callerMutationHookExecution[A], afterPrecheck func(context.Context, *sqlxUpsertAttempt) (mutationsql.Program, error), attemptOrdinal uint32) (row golem.Row[M], resultErr error) {
	backend := sqlxUpsertBackend{database: app.database, provider: app.provider, binding: source, mutation: mutationConfig(app, source)}
	generic, err := backend.Begin(ctx, initial.TransactionRequirement(), attemptOrdinal)
	if err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(initial.Operation(), scalarMutationProviderFailureKind(err), 0, 0, "concurrency transaction could not begin", err))
	}
	attempt, ok := generic.(*sqlxUpsertAttempt)
	if !ok || attempt == nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(initial.Operation(), scalarMutationInvariant, 0, 0, "concurrency transaction is invalid", nil))
	}
	committed := false
	defer func() {
		if !committed {
			if abortErr := attempt.Abort(); abortErr != nil {
				row = golem.Row[M]{}
				resultErr = golem.RuntimeOperationError(golem.CodeBadUserInput, scalarMutationOperationName(initial.Operation()), descriptor.Metadata().ModelID(), golem.FieldID{}, "mutation could not be completed", errors.Join(resultErr, &concurrencyAbortFailure{cause: abortErr}))
			}
		}
	}()
	statements := initial.Statements()
	if len(statements) < 2 || statements[0].Role() != mutationsql.SelectPreImage || !initial.RequiresConcurrencyPrecheck() {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(initial.Operation(), scalarMutationInvariant, 0, 0, "concurrency program has no exact pre-image boundary", nil))
	}
	arguments, err := scalarMutationArguments(initial.Operation(), 0, statements[0], nil)
	if err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), err)
	}
	preimage, err := queryExactlyOneMutationRow(ctx, attempt.queryer, app.registry, policyir.ModelID(descriptor.Metadata().ModelID()), app.provider, initial.Operation(), 0, statements[0], arguments)
	if err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), err)
	}
	field, enabled := initial.OptimisticConcurrency()
	if !enabled {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(initial.Operation(), scalarMutationInvariant, 0, 0, "concurrency field is absent from program", nil))
	}
	if err := claim.validateObserved(preimage, field, initial.Operation()); err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), err)
	}
	program, err := afterPrecheck(ctx, attempt)
	if err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), err)
	}
	nextStatements := program.Statements()
	if nextField, ok := program.OptimisticConcurrency(); !program.RequiresConcurrencyPrecheck() || !ok || nextField != field || len(nextStatements) < 2 {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "concurrency program changed after precheck", nil))
	}
	if !sameConcurrencyPrecheck(statements[0], nextStatements[0]) {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(program.Operation(), scalarMutationInvalid, mutationsql.SelectPreImage, 0, "before hook changed the locked precheck contract", nil))
	}
	row, err = executeVersionedScalarProjectionOnAttempt(ctx, app, attempt, descriptor, projection, program, preimage, hooks)
	if err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), err)
	}
	if err := attempt.Finish(ctx); err != nil {
		return golem.Row[M]{}, publicScalarMutationError(descriptor.Metadata().ModelID(), scalarMutationError(program.Operation(), scalarMutationProviderFailureKind(err), 0, 0, "concurrency transaction could not commit", err))
	}
	committed = true
	return row, nil
}

func sameConcurrencyPrecheck(left, right mutationsql.Statement) bool {
	if left.Role() != mutationsql.SelectPreImage || right.Role() != mutationsql.SelectPreImage || left.SQL() != right.SQL() || left.Cardinality() != right.Cardinality() {
		return false
	}
	leftColumns, rightColumns := left.Columns(), right.Columns()
	if len(leftColumns) != len(rightColumns) {
		return false
	}
	for index := range leftColumns {
		if leftColumns[index].FieldID() != rightColumns[index].FieldID() || leftColumns[index].Alias() != rightColumns[index].Alias() {
			return false
		}
	}
	leftAuthorizations, rightAuthorizations := left.AuthorizationColumns(), right.AuthorizationColumns()
	if len(leftAuthorizations) != len(rightAuthorizations) {
		return false
	}
	for index := range leftAuthorizations {
		if leftAuthorizations[index].FieldID() != rightAuthorizations[index].FieldID() || leftAuthorizations[index].Alias() != rightAuthorizations[index].Alias() {
			return false
		}
	}
	leftBindings, rightBindings := left.Bindings(), right.Bindings()
	if len(leftBindings) != len(rightBindings) {
		return false
	}
	for index := range leftBindings {
		leftValue, leftStatic := leftBindings[index].StaticValue()
		rightValue, rightStatic := rightBindings[index].StaticValue()
		if !leftStatic || !rightStatic || !equalMutationPhysicalValue(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func executeVersionedScalarProjectionOnAttempt[P, A, M any](ctx context.Context, app *App[P, A], attempt *sqlxUpsertAttempt, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, program mutationsql.Program, preimage scalarMutationStatementResult, hooks *callerMutationHookExecution[A]) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	var verified scalarMutationVerifiedObserver
	if hooks != nil {
		verified = hooks.verifiedObserver(app.registry)
	}
	if !projection.active {
		if _, err := executeVersionedScalarProgramAfterPrecheck(ctx, attempt.queryer, attempt.binding, app.registry, model, app.provider, program, preimage, nil, verified); err != nil {
			return golem.Row[M]{}, err
		}
		return golem.RuntimeReadRow(descriptor)
	}
	var projected golem.Row[M]
	materialized := false
	identity := program.IdentityVerification()
	imageStatement := identity.AfterStatement()
	observeAt := uint32(len(program.Statements()) - 1)
	if program.Operation() == mutationir.Delete {
		var present bool
		imageStatement, present = identity.BeforeStatement()
		if !present {
			return golem.Row[M]{}, scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "delete projection has no locked pre-image", nil)
		}
		observeAt = imageStatement
	}
	observer := func(observerContext context.Context, observerBinding *executionBinding, statement uint32, execution scalarMutationExecution) error {
		if statement != observeAt {
			return nil
		}
		source, ok := execution.statement(imageStatement)
		if !ok {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statement, "versioned mutation result image is absent", nil)
		}
		value, err := materializeScalarMutationProjection(observerContext, app, observerBinding, descriptor, projection.planned, source)
		if err != nil {
			return err
		}
		projected, materialized = value, true
		return nil
	}
	if _, err := executeVersionedScalarProgramAfterPrecheck(ctx, attempt.queryer, attempt.binding, app.registry, model, app.provider, program, preimage, observer, verified); err != nil {
		return golem.Row[M]{}, err
	}
	if !materialized {
		return golem.Row[M]{}, scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "versioned mutation projection was not materialized", nil)
	}
	return projected, nil
}
