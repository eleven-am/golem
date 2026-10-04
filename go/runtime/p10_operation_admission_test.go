package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type admissionProbeKey struct{}

type admissionProbe struct {
	arrived chan struct{}
	hold    func()
	call    func(context.Context) error
	callErr error
}

func newAdmissionProbe(hold func(), call func(context.Context) error) *admissionProbe {
	return &admissionProbe{arrived: make(chan struct{}), hold: hold, call: call}
}

func admissionProbeHook(ctx context.Context) error {
	probe, ok := ctx.Value(admissionProbeKey{}).(*admissionProbe)
	if !ok {
		return nil
	}
	close(probe.arrived)
	probe.hold()
	if probe.call != nil {
		probe.callErr = probe.call(ctx)
	}
	return nil
}

func admissionProbeHooks(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
	return []golem.HookBinding[mutationResultActor]{
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.FindManyHookResult[mutationResultPost]](schema.Post, golem.HookFindMany, func(ctx context.Context, _ golem.FindManyHookResult[mutationResultPost]) error {
			return admissionProbeHook(ctx)
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.FindFirstHookResult[mutationResultPost]](schema.Post, golem.HookFindFirst, func(ctx context.Context, _ golem.FindFirstHookResult[mutationResultPost]) error {
			return admissionProbeHook(ctx)
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.FindOneHookResult[mutationResultPost]](schema.Post, golem.HookFindOne, func(ctx context.Context, _ golem.FindOneHookResult[mutationResultPost]) error {
			return admissionProbeHook(ctx)
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(ctx context.Context, _ golem.CreateHookResult[mutationResultPost]) error {
			return admissionProbeHook(ctx)
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(ctx context.Context, _ golem.UpdateManyHookResult[mutationResultPost]) error {
			return admissionProbeHook(ctx)
		}),
	}
}

type admissionTx = CallerTx[mutationResultPrincipal, mutationResultActor]

type admissionOperation struct {
	name string
	run  func(context.Context, mutationResultFixture, *admissionTx) error
}

func admissionOperations() []admissionOperation {
	const seeded = 150
	return []admissionOperation{
		{name: "findMany", run: func(ctx context.Context, fixture mutationResultFixture, tx *admissionTx) error {
			_, err := CallerTxFindMany(ctx, tx, fixture.postDescriptor)
			return err
		}},
		{name: "findFirst", run: func(ctx context.Context, fixture mutationResultFixture, tx *admissionTx) error {
			_, _, err := CallerTxFindFirst(ctx, tx, fixture.postDescriptor)
			return err
		}},
		{name: "findUnique", run: func(ctx context.Context, fixture mutationResultFixture, tx *admissionTx) error {
			_, err := CallerTxFindUnique(ctx, tx, fixture.postDescriptor, golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: seeded})))
			return err
		}},
		{name: "create", run: func(ctx context.Context, fixture mutationResultFixture, tx *admissionTx) error {
			_, err := CallerTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(151, golem.UUID{15: 1}, "admitted-create"))
			return err
		}},
		{name: "updateMany", run: func(ctx context.Context, fixture mutationResultFixture, tx *admissionTx) error {
			_, err := CallerTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: seeded}), fixture.updateManyTitle("admitted-update"))
			return err
		}},
	}
}

func admissionClosed(channel chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func admissionFinalisationIsDraining() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "(*executionBinding).closeCalls") && strings.Contains(stack, "sync.(*Cond).Wait") {
			return true
		}
	}
	return false
}

func admissionAwait(condition func() bool) {
	for !condition() {
		goruntime.Gosched()
	}
}

func seedAdmissionPost(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(150, golem.UUID{15: 1}, "admission-seed")); err != nil {
		t.Fatal(err)
	}
}

func TestSiblingArrivingDuringAnOperationHookIsRefusedAndTheHookKeepsItsTransactionAcrossProviders(t *testing.T) {
	for _, operation := range admissionOperations() {
		operation := operation
		t.Run(operation.name, func(t *testing.T) {
			forEachHookedMutationResultProvider(t, MutationLimits{}, admissionProbeHooks, func(t testing.TB, fixture mutationResultFixture) {
				seedAdmissionPost(t, fixture)
				caller := mustMutationResultCaller(t, fixture)
				release := make(chan struct{})
				siblingRelease := make(chan struct{})
				var tx *admissionTx
				probe := newAdmissionProbe(func() { <-release }, func(ctx context.Context) error {
					_, err := CallerTxCount(ctx, tx, fixture.postDescriptor)
					return err
				})
				sibling := newAdmissionProbe(func() { <-siblingRelease }, nil)
				var operationErr, siblingErr error
				siblingAdmitted := false
				err := CallerTransaction(context.Background(), caller, func(inner *admissionTx) error {
					tx = inner
					operationResult := make(chan error, 1)
					go func() {
						operationResult <- operation.run(context.WithValue(context.Background(), admissionProbeKey{}, probe), fixture, tx)
					}()
					select {
					case <-probe.arrived:
					case err := <-operationResult:
						return fmt.Errorf("%s ended before its hook ran: %w", operation.name, err)
					}
					siblingResult := make(chan error, 1)
					go func() {
						_, err := CallerTxCreate(context.WithValue(context.Background(), admissionProbeKey{}, sibling), tx, fixture.postDescriptor, fixture.createPost(152, golem.UUID{15: 1}, "sibling"))
						siblingResult <- err
					}()
					select {
					case siblingErr = <-siblingResult:
					case <-sibling.arrived:
						siblingAdmitted = true
					}
					close(release)
					operationErr = <-operationResult
					close(siblingRelease)
					if siblingAdmitted {
						siblingErr = <-siblingResult
					}
					return nil
				})
				if err != nil {
					t.Fatalf("transaction = %v", err)
				}
				if siblingAdmitted || !errors.Is(siblingErr, errTransactionConcurrentUse) {
					t.Fatalf("sibling during the %s hook admitted=%t err=%v, want the concurrent-use refusal", operation.name, siblingAdmitted, siblingErr)
				}
				if probe.callErr != nil {
					t.Fatalf("the %s hook's own transaction call = %v", operation.name, probe.callErr)
				}
				if operationErr != nil {
					t.Fatalf("%s = %v", operation.name, operationErr)
				}
			})
		})
	}
}

func TestCallbackReturningDuringAnOperationHookWaitsForTheOperationAcrossProviders(t *testing.T) {
	for _, operation := range admissionOperations() {
		operation := operation
		t.Run(operation.name, func(t *testing.T) {
			forEachHookedMutationResultProvider(t, MutationLimits{}, admissionProbeHooks, func(t testing.TB, fixture mutationResultFixture) {
				seedAdmissionPost(t, fixture)
				caller := mustMutationResultCaller(t, fixture)
				returned := make(chan struct{})
				var tx *admissionTx
				probe := newAdmissionProbe(func() {
					admissionAwait(func() bool { return admissionClosed(returned) || admissionFinalisationIsDraining() })
				}, func(ctx context.Context) error {
					_, err := CallerTxCount(ctx, tx, fixture.postDescriptor)
					return err
				})
				operationResult := make(chan error, 1)
				finishedFirst := make(chan bool, 1)
				err := CallerTransaction(context.Background(), caller, func(inner *admissionTx) error {
					tx = inner
					go func() {
						err := operation.run(context.WithValue(context.Background(), admissionProbeKey{}, probe), fixture, tx)
						finishedFirst <- !admissionClosed(returned)
						operationResult <- err
					}()
					select {
					case <-probe.arrived:
					case err := <-operationResult:
						return fmt.Errorf("%s ended before its hook ran: %w", operation.name, err)
					}
					return nil
				})
				close(returned)
				if err != nil {
					t.Fatalf("transaction = %v", err)
				}
				if !<-finishedFirst {
					t.Fatalf("the transaction finished while the %s operation was still in its hook", operation.name)
				}
				if probe.callErr != nil {
					t.Fatalf("the %s hook's own transaction call = %v", operation.name, probe.callErr)
				}
				if err := <-operationResult; err != nil {
					t.Fatalf("%s = %v", operation.name, err)
				}
			})
		})
	}
}
