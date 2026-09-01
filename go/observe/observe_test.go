package observe

import (
	"context"
	"go/types"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"golang.org/x/tools/go/packages"
)

type observerFunc func(context.Context, Observation)

func (invoke observerFunc) ObserveGolem(ctx context.Context, value Observation) {
	invoke(ctx, value)
}

func TestObservationIsValidatedImmutablePanicSafeAndDeadlineBounded(t *testing.T) {
	model := golem.ModelID{15: 7}
	called := 0
	internalvalue.Emit(observerFunc(func(ctx context.Context, value Observation) {
		called++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > observationDeadline {
			t.Fatalf("observation deadline=%v present=%t", deadline, ok)
		}
		if value.Kind() != KindRead || value.Phase() != PhaseFinish || value.Outcome() != OutcomeSuccess || value.Reason() != ReasonNone || value.Provider() != golem.SQLite || value.ModelID() != model || value.Operation() != OperationReadFindMany || value.StatementCount() != 3 || value.AggregateCount() != 2 {
			t.Fatalf("observation=%#v", value)
		}
	}), internalvalue.Value{
		KindValue: string(KindRead), PhaseValue: string(PhaseFinish), OutcomeValue: string(OutcomeSuccess),
		ProviderValue: golem.SQLite, ModelIDValue: model, OperationValue: string(OperationReadFindMany),
		StatementCountValue: 3, AggregateCountValue: 2,
	})
	if called != 1 {
		t.Fatalf("valid observations=%d", called)
	}
	internalvalue.Emit(observerFunc(func(context.Context, Observation) { called++ }), internalvalue.Value{KindValue: "forged", PhaseValue: string(PhaseFinish), OutcomeValue: string(OutcomeSuccess), OperationValue: string(OperationReadFindMany)})
	if called != 1 {
		t.Fatalf("invalid observation was delivered")
	}
	internalvalue.Emit(observerFunc(func(context.Context, Observation) { panic("observer") }), internalvalue.Value{KindValue: string(KindRead), PhaseValue: string(PhaseFinish), OutcomeValue: string(OutcomeSuccess), OperationValue: string(OperationReadFindMany)})
}

func TestObservationRejectsEveryNumericOverflow(t *testing.T) {
	base := internalvalue.Value{KindValue: string(KindRead), PhaseValue: string(PhaseFinish), OutcomeValue: string(OutcomeSuccess), OperationValue: string(OperationReadFindMany)}
	invalid := []internalvalue.Value{
		func() internalvalue.Value {
			value := base
			value.StatementCountValue = int(maximumObservationCount + 1)
			return value
		}(),
		func() internalvalue.Value {
			value := base
			value.AttemptValue = int(maximumObservationCount + 1)
			return value
		}(),
		func() internalvalue.Value {
			value := base
			value.QueueDepthValue = int(maximumObservationCount + 1)
			return value
		}(),
		func() internalvalue.Value {
			value := base
			value.QueueLimitValue = int(maximumObservationCount + 1)
			return value
		}(),
		func() internalvalue.Value {
			value := base
			value.AggregateCountValue = maximumObservationCount + 1
			return value
		}(),
	}
	for index, value := range invalid {
		called := false
		internalvalue.Emit(observerFunc(func(context.Context, Observation) { called = true }), value)
		if called {
			t.Fatalf("numeric overflow %d was delivered", index)
		}
	}
}

func TestQueueObservationRequiresCanonicalTypeAndMatchingOperation(t *testing.T) {
	base := internalvalue.Value{KindValue: string(KindQueue), PhaseValue: string(PhaseStart), OutcomeValue: string(OutcomeSuccess), OperationValue: string(OperationQueueExecute), QueueTypeValue: "mail.deliver"}
	called := 0
	internalvalue.Emit(observerFunc(func(_ context.Context, value Observation) {
		called++
		if value.QueueType() != "mail.deliver" {
			t.Fatalf("queue type=%q", value.QueueType())
		}
	}), base)
	invalid := []internalvalue.Value{
		func() internalvalue.Value { value := base; value.QueueTypeValue = ""; return value }(),
		func() internalvalue.Value { value := base; value.QueueTypeValue = "Mail Deliver"; return value }(),
		func() internalvalue.Value { value := base; value.KindValue = string(KindRead); return value }(),
		func() internalvalue.Value {
			value := base
			value.OperationValue = string(OperationReadFindMany)
			return value
		}(),
		{KindValue: string(KindRead), PhaseValue: string(PhaseFinish), OutcomeValue: string(OutcomeSuccess), OperationValue: string(OperationReadFindMany), QueueTypeValue: "mail.deliver"},
	}
	for _, value := range invalid {
		internalvalue.Emit(observerFunc(func(context.Context, Observation) { called++ }), value)
	}
	if called != 1 {
		t.Fatalf("delivered queue observations=%d", called)
	}
}

func TestPublicObserveInventoryHasNoProducerOrInternalTypeLeak(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes}, ".")
	if err != nil || len(loaded) != 1 || len(loaded[0].Errors) != 0 {
		t.Fatalf("load observe package: packages=%d err=%v errors=%v", len(loaded), err, loaded[0].Errors)
	}
	scope := loaded[0].Types.Scope()
	wantTypes := map[string]bool{"Kind": true, "Phase": true, "Outcome": true, "Reason": true, "Operation": true, "Observation": true, "Observer": true, "DispatcherConfig": true, "Dispatcher": true}
	wantFunctions := map[string]bool{"NewDispatcher": true}
	for _, name := range scope.Names() {
		object := scope.Lookup(name)
		if !object.Exported() {
			continue
		}
		rendered := types.TypeString(object.Type(), func(pkg *types.Package) string { return pkg.Path() })
		if strings.Contains(rendered, "/internal/") {
			t.Fatalf("exported %s leaks internal type: %s", name, rendered)
		}
		switch object.(type) {
		case *types.Const:
			if _, ok := object.Type().(*types.Named); !ok {
				if name != "DefaultQueueCapacity" && name != "MaximumQueueCapacity" {
					t.Fatalf("exported constant %s is not closed typed", name)
				}
			}
		case *types.TypeName:
			if !wantTypes[name] {
				t.Fatalf("unexpected exported type %s", name)
			}
			delete(wantTypes, name)
		case *types.Func:
			if !wantFunctions[name] {
				t.Fatalf("unexpected exported function %s", name)
			}
			delete(wantFunctions, name)
		default:
			t.Fatalf("unexpected exported producer/value %s (%T)", name, object)
		}
	}
	if len(wantTypes) != 0 || len(wantFunctions) != 0 {
		t.Fatalf("missing public inventory: types=%v functions=%v", wantTypes, wantFunctions)
	}
	observation := scope.Lookup("Observation").Type()
	methods := types.NewMethodSet(observation)
	wantMethods := map[string]bool{"Kind": true, "Phase": true, "Outcome": true, "Reason": true, "Provider": true, "ModelID": true, "Operation": true, "Duration": true, "StatementCount": true, "Attempt": true, "QueueDepth": true, "QueueLimit": true, "AggregateCount": true, "QueueType": true}
	for index := 0; index < methods.Len(); index++ {
		delete(wantMethods, methods.At(index).Obj().Name())
	}
	if len(wantMethods) != 0 || methods.Len() != 14 {
		t.Fatalf("Observation method set len=%d missing=%v", methods.Len(), wantMethods)
	}
}
