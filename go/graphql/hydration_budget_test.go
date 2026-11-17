package graphql

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
)

func TestHydrationBudgetSpansResultsAndCountsRequestsBeforeSQL(t *testing.T) {
	ctx := context.Background()
	fixture := computedFixture(t)
	compiler, err := graphqloperation.New(fixture.compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	document := computedQuery(t, `{searchUsers(where:{all:true}){id}}`)
	operation, err := compiler.Compile(document, document.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	root := operation.Custom[0]
	row := principalIsolationRow(t, fixture, 7)
	assertBudget := func(t *testing.T, err error, maximum int) {
		t.Helper()
		var limit *golem.Error
		if !errors.As(err, &limit) || limit.Code != golem.CodeLimitExceeded || limit.Limit != golem.LimitHydratedRows || limit.LimitMax != maximum {
			t.Fatalf("expected hydration limit %d, got %v", maximum, err)
		}
	}

	t.Run("shared custom and computed budget with deduplication", func(t *testing.T) {
		caller := &storedRowsCaller{stored: []golem.RuntimeModelRow{row}}
		hydrator := &resultHydrator{compiler: compiler, caller: caller, maxRows: 2}
		if _, _, err := hydrator.root(ctx, root, []any{row, row}); err != nil {
			t.Fatal(err)
		}
		execution := &computedExecution{hydrator: hydrator}
		slot := selectset.Slot{Kind: selectset.SlotComputed, Computed: &graphqlextension.ComputedSelection{Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: fixture.contract.GraphQLName, Nullable: true}}}
		if _, err := execution.hydrateValues(ctx, slot, []any{row, row}, nil); err != nil {
			t.Fatal(err)
		}
		_, _, err := hydrator.root(ctx, root, []any{row})
		assertBudget(t, err, 2)
		if !reflect.DeepEqual(caller.takes, []int{1, 1}) {
			t.Fatalf("overflow reached SQL or deduplication was lost: %v", caller.takes)
		}
	})

	t.Run("denied identities still consume work", func(t *testing.T) {
		caller := &storedRowsCaller{}
		hydrator := &resultHydrator{compiler: compiler, caller: caller, maxRows: 1}
		if _, _, err := hydrator.root(ctx, root, []any{row}); err != nil {
			t.Fatal(err)
		}
		_, _, err := hydrator.root(ctx, root, []any{row})
		assertBudget(t, err, 1)
		if !reflect.DeepEqual(caller.takes, []int{1}) {
			t.Fatalf("denied hydration reset the budget: %v", caller.takes)
		}
	})

	t.Run("capacity retries consume their identities once", func(t *testing.T) {
		rows := make([]golem.RuntimeModelRow, 4)
		values := make([]any, len(rows))
		for i := range rows {
			cell := golem.RuntimePresentReadCell(fixture.id, fixture.uuid(byte(i+1)), nil)
			value, err := golem.RuntimeModelReadRow(fixture.model, cell)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{fixture.id}, []golem.RuntimeReadCell{cell})
			if err != nil {
				t.Fatal(err)
			}
			rows[i] = golem.RuntimeModelRowWithIdentity(value, identity)
			values[i] = rows[i]
		}
		caller := &capacityCaller{remaining: rows}
		hydrator := &resultHydrator{compiler: compiler, caller: caller, maxRows: 4}
		if _, _, err := hydrator.root(ctx, root, values); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(caller.takes, []int{4, 2, 2}) {
			t.Fatalf("chunk reduction changed: %v", caller.takes)
		}
		caller.remaining = []golem.RuntimeModelRow{row}
		_, _, err := hydrator.root(ctx, root, []any{row})
		assertBudget(t, err, 4)
		if len(caller.takes) != 3 {
			t.Fatalf("exhausted budget reached SQL: %v", caller.takes)
		}
	})
}

type hydrationBudgetEventCaller struct {
	storedRowsCaller
	source EventStream
}

func (caller *hydrationBudgetEventCaller) SubscribeFrozenEvents(context.Context, golem.FrozenReadRequest, bool) (EventStream, error) {
	return caller.source, nil
}

func TestHydrationBudgetResetsForEachSubscriptionResponse(t *testing.T) {
	fixture, eventSchema := p7ComputedSubscriptionFixture(t)
	for i := range fixture.compilation.Contract.Models {
		for j := range fixture.compilation.Contract.Models[i].Computed {
			field := &fixture.compilation.Contract.Models[i].Computed[j]
			if field.ExtensionID == fixture.greeting.ExtensionID {
				field.Result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: fixture.contract.GraphQLName, Nullable: true}
			}
		}
	}
	fixture.bundle = bundleForCompilation(t, fixture.compilation)
	row := principalIsolationRow(t, fixture, 7)
	binding, err := BindComputed(string(fixture.greeting.ExtensionID), func(context.Context, ComputedRequest) (any, error) { return row, nil })
	if err != nil {
		t.Fatal(err)
	}
	var version atomic.Int64
	caller := &hydrationBudgetEventCaller{storedRowsCaller: storedRowsCaller{stored: []golem.RuntimeModelRow{row}}, source: &p7ComputedEventStream{fixture: fixture, version: &version, schema: eventSchema}}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: fixture.bundle, Limits: Limits{MaxHydratedRows: 1},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ComputedBindings:    []ComputedBinding{binding, fixture.batchBinding(t, nil, nil)},
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("internal subscription error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	document := computedQuery(t, `subscription {userEvents{entity{first:greeting(prefix:"a"){id} second:greeting(prefix:"b"){id}}}}`)
	stream, err := executor.(SubscriptionExecutor[int]).Subscribe(context.Background(), 1, Operation{Document: document, Definition: document.Operations[0]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for i := 0; i < 3; i++ {
		response, err := stream.Recv(context.Background())
		if err != nil || len(response.Errors) != 1 {
			t.Fatalf("response %d: %#v err=%v", i, response, err)
		}
		failure := response.Errors[0]
		if failure.Extensions["code"] != "LIMIT_EXCEEDED" || failure.Extensions["limit"] != "hydrated_rows" || !reflect.DeepEqual(failure.Path, []any{"userEvents", "entity", "second"}) {
			t.Fatalf("response %d budget failure: %#v", i, failure)
		}
		entity := response.Data.(map[string]any)["userEvents"].(map[string]any)["entity"].(map[string]any)
		if entity["first"].(map[string]any)["id"] != fixture.uuid(1).String() || entity["second"] != nil {
			t.Fatalf("response %d reused the previous budget: %#v", i, response)
		}
	}
	if !reflect.DeepEqual(caller.takes, []int{1, 1, 1}) {
		t.Fatalf("subscription budget reached SQL or carried across events: %v", caller.takes)
	}
}
