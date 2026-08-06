package graphql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type mixedCustomArgs struct{ Where golem.FrozenPredicate }

func TestGeneratedExecutorRejectsMissingCustomBindingsAtConstruction(t *testing.T) {
	fixture := computedFixture(t)
	_, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:              fixture.bundle,
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil)},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return &principalIsolationCaller{}, nil },
		ReportInternalError: func(context.Context, error) {},
	})
	if err == nil {
		t.Fatal("missing custom bindings were accepted at construction")
	}
}

func TestGraphQLMixedOrdinaryAndCustomRootsShareExactOperationCaller(t *testing.T) {
	fixture := computedFixture(t)
	search := customContract(t, fixture.compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, fixture.compilation, compilerir.CustomOperationMutation, "importUsers")
	caller := &principalIsolationCaller{id: 91, principal: 7, row: principalIsolationRow(t, fixture, 7)}
	customCalls := 0
	query, err := BindCustomQuery[*principalIsolationCaller, mixedCustomArgs, []golem.RuntimeModelRow](customSpec(search),
		func(arguments []CustomArgument) (mixedCustomArgs, error) {
			where, ok := CustomArgumentValue[golem.FrozenPredicate](arguments, "where")
			if !ok {
				return mixedCustomArgs{}, errCustomTest("where is not frozen")
			}
			return mixedCustomArgs{Where: where}, nil
		},
		func(_ context.Context, got *principalIsolationCaller, arguments mixedCustomArgs) ([]golem.RuntimeModelRow, error) {
			if got != caller || arguments.Where.View().RootModelID() != fixture.model {
				return nil, errCustomTest("custom caller identity changed")
			}
			customCalls++
			return []golem.RuntimeModelRow{got.row}, nil
		},
		func(value []golem.RuntimeModelRow) (any, error) {
			items := make([]any, len(value))
			for index := range value {
				items[index] = value[index]
			}
			return items, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := BindCustomMutation[*principalIsolationCaller, struct{}, golem.RuntimeModelRow](customSpec(importUsers),
		func([]CustomArgument) (struct{}, error) { return struct{}{}, nil },
		func(context.Context, *principalIsolationCaller, struct{}) (golem.RuntimeModelRow, error) {
			return caller.row, nil
		},
		func(value golem.RuntimeModelRow) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:              fixture.bundle,
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil)},
		CustomBindings:      []CustomBinding{query, mutation},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ReportInternalError: func(_ context.Context, err error) { t.Logf("internal: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(fixture.compilation)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 7, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 7, Request{Query: `query Mixed { users(take: 1) { id } searchUsers(where: { all: true }) { id } }`, OperationName: "Mixed"})
	if len(response.Errors) != 0 {
		t.Fatalf("mixed response errors=%#v data=%#v", response.Errors, response.Data)
	}
	if caller.reads.Load() != 1 || customCalls != 1 {
		t.Fatalf("ordinary reads=%d custom calls=%d", caller.reads.Load(), customCalls)
	}
}
