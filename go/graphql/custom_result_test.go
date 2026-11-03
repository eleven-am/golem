package graphql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	contract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

type importCaller struct {
	principalIsolationCaller
	dispatch func(context.Context, any, func(context.Context, any) (any, error)) (any, error)
}

func (caller *importCaller) GolemGraphQLDispatchCustomMutation(ctx context.Context, resolver any, run func(context.Context, any) (any, error)) (any, error) {
	return caller.dispatch(ctx, resolver, run)
}

func importUsersArguments(t *testing.T, model golem.ModelID) map[string]any {
	t.Helper()
	create, err := golem.RuntimeMutationInputFromValues(golem.RuntimeMutationCreateInput, model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := golem.RuntimeMutationInputFromValues(golem.RuntimeMutationUpdateManyInput, model, []golem.RuntimeMutationFieldValue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	createArgument, err := golem.RuntimeCustomMutationInputValue(golem.RuntimeMutationCreateInput, create)
	if err != nil {
		t.Fatal(err)
	}
	patchArgument, err := golem.RuntimeCustomMutationInputValue(golem.RuntimeMutationUpdateManyInput, patch)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"metadata": metadata, "data": createArgument, "patch": patchArgument}
}

func TestCustomExecutionSeparatesResolverFailureFromResultFailure(t *testing.T) {
	fixture := computedFixture(t)
	search := customContract(t, fixture.compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, fixture.compilation, compilerir.CustomOperationMutation, "importUsers")
	arguments := importUsersArguments(t, fixture.model)
	row := principalIsolationRow(t, fixture, 1)
	query, err := BindCustomQuery[*importCaller, mixedCustomArgs, []golem.RuntimeModelRow](customSpec(search),
		func([]CustomArgument) (mixedCustomArgs, error) { return mixedCustomArgs{}, nil },
		func(context.Context, *importCaller, mixedCustomArgs) ([]golem.RuntimeModelRow, error) {
			return nil, nil
		},
		func([]golem.RuntimeModelRow) (any, error) { return []any{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		resolve func() (golem.RuntimeModelRow, error)
		encode  func(golem.RuntimeModelRow) (any, error)
		isFault bool
	}
	cases := map[string]outcome{
		"resolver fails": {
			resolve: func() (golem.RuntimeModelRow, error) {
				return golem.RuntimeModelRow{}, errCustomTest("resolver failed")
			},
			encode: func(value golem.RuntimeModelRow) (any, error) { return value, nil },
		},
		"result encoder fails": {
			resolve: func() (golem.RuntimeModelRow, error) { return row, nil },
			encode:  func(golem.RuntimeModelRow) (any, error) { return nil, errCustomTest("encode failed") },
			isFault: true,
		},
		"result violates the contract": {
			resolve: func() (golem.RuntimeModelRow, error) { return row, nil },
			encode:  func(golem.RuntimeModelRow) (any, error) { return 7, nil },
			isFault: true,
		},
	}
	for name, test := range cases {
		test := test
		t.Run(name, func(t *testing.T) {
			mutation, err := BindCustomMutation[*customTypedCaller, struct{}, golem.RuntimeModelRow](customSpec(importUsers),
				func([]CustomArgument) (struct{}, error) { return struct{}{}, nil },
				func(context.Context, *customTypedCaller, struct{}) (golem.RuntimeModelRow, error) {
					return test.resolve()
				},
				test.encode)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewCustomRegistry(fixture.bundle, query, mutation)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := registry.ForCaller(&customTypedCaller{})
			if err != nil {
				t.Fatal(err)
			}
			_, executeErr := execution.Execute(context.Background(), CustomMutation, "importUsers", arguments)
			var result *CustomResultError
			if executeErr == nil || errors.As(executeErr, &result) != test.isFault {
				t.Fatalf("error %v: result failure = %t, want %t", executeErr, errors.As(executeErr, &result), test.isFault)
			}
		})
	}

	dispatched := map[string]struct {
		dispatch func(context.Context, any, func(context.Context, any) (any, error)) (any, error)
		encode   func(golem.RuntimeModelRow) (any, error)
		isFault  bool
	}{
		"dispatch fails": {
			dispatch: func(context.Context, any, func(context.Context, any) (any, error)) (any, error) {
				return nil, errCustomTest("dispatch failed")
			},
			encode: func(value golem.RuntimeModelRow) (any, error) { return value, nil },
		},
		"dispatched value has the wrong type": {
			dispatch: func(context.Context, any, func(context.Context, any) (any, error)) (any, error) {
				return "not a row", nil
			},
			encode:  func(value golem.RuntimeModelRow) (any, error) { return value, nil },
			isFault: true,
		},
		"dispatched value cannot be encoded": {
			dispatch: func(context.Context, any, func(context.Context, any) (any, error)) (any, error) { return row, nil },
			encode:   func(golem.RuntimeModelRow) (any, error) { return nil, errCustomTest("encode failed") },
			isFault:  true,
		},
	}
	for name, test := range dispatched {
		test := test
		t.Run("dispatcher "+name, func(t *testing.T) {
			mutation, err := BindCustomMutation[*importCaller, struct{}, golem.RuntimeModelRow](customSpec(importUsers),
				func([]CustomArgument) (struct{}, error) { return struct{}{}, nil },
				func(context.Context, *importCaller, struct{}) (golem.RuntimeModelRow, error) { return row, nil },
				test.encode)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewCustomRegistry(fixture.bundle, query, mutation)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := registry.ForCaller(&importCaller{dispatch: test.dispatch})
			if err != nil {
				t.Fatal(err)
			}
			_, executeErr := execution.Execute(context.Background(), CustomMutation, "importUsers", arguments)
			var result *CustomResultError
			if executeErr == nil || errors.As(executeErr, &result) != test.isFault {
				t.Fatalf("error %v: result failure = %t, want %t", executeErr, errors.As(executeErr, &result), test.isFault)
			}
		})
	}
}

func TestCustomRowWithoutIdentityIsInternalAndReported(t *testing.T) {
	fixture := computedFixture(t)
	search := customContract(t, fixture.compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, fixture.compilation, compilerir.CustomOperationMutation, "importUsers")
	handBuilt, err := golem.RuntimeModelReadRow(fixture.model, golem.RuntimePresentReadCell(fixture.id, fixture.uuid(1), nil))
	if err != nil {
		t.Fatal(err)
	}
	caller := &principalIsolationCaller{id: 1, principal: 7, row: handBuilt}
	query, err := BindCustomQuery[*principalIsolationCaller, mixedCustomArgs, []golem.RuntimeModelRow](customSpec(search),
		func([]CustomArgument) (mixedCustomArgs, error) { return mixedCustomArgs{}, nil },
		func(context.Context, *principalIsolationCaller, mixedCustomArgs) ([]golem.RuntimeModelRow, error) {
			return []golem.RuntimeModelRow{handBuilt}, nil
		},
		func(value []golem.RuntimeModelRow) (any, error) { return []any{value[0]}, nil })
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := BindCustomMutation[*principalIsolationCaller, struct{}, golem.RuntimeModelRow](customSpec(importUsers),
		func([]CustomArgument) (struct{}, error) { return struct{}{}, nil },
		func(context.Context, *principalIsolationCaller, struct{}) (golem.RuntimeModelRow, error) {
			return handBuilt, nil
		},
		func(value golem.RuntimeModelRow) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	var reported []error
	report := func(_ context.Context, err error) {
		mutex.Lock()
		defer mutex.Unlock()
		reported = append(reported, err)
	}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:              fixture.bundle,
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil)},
		CustomBindings:      []CustomBinding{query, mutation},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ReportInternalError: report,
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(fixture.compilation)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 7, true }, ReportInternalError: report}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 7, Request{Query: `query { searchUsers(where: { all: true }) { id } }`})
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "INTERNAL_SERVER_ERROR" || len(response.Errors[0].Path) != 1 || response.Errors[0].Path[0] != "searchUsers" {
		t.Fatalf("response errors = %#v", response.Errors)
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "no identity") {
		t.Fatalf("reported = %v", reported)
	}
	if caller.reads.Load() != 0 {
		t.Fatalf("a row without identity must fail before any read, got %d reads", caller.reads.Load())
	}
}

func TestPresentCommittedResultNamesWhatHappenedWithoutEchoingTheCause(t *testing.T) {
	forbidden := golem.RuntimeOperationError(golem.CodeForbidden, "read", golem.ModelID{}, golem.FieldID{}, "read is not permitted", errCustomTest("secret policy detail"))
	limited := golem.RuntimeLimitError("customMutation", golem.ModelID{}, golem.LimitTake, 3, "result is too long", errCustomTest("secret limit detail"))
	cases := map[string]struct {
		cause      error
		reason     string
		unreadable any
		limit      any
		reported   int
	}{
		"unreadable rows":       {cause: &unreadableResult{count: 2}, reason: "RESULT_NOT_READABLE", unreadable: 2},
		"limit exceeded":        {cause: &CustomResultError{cause: limited}, reason: "LIMIT_EXCEEDED", limit: "take", reported: 1},
		"list maximum exceeded": {cause: &CustomResultError{cause: contract.ListItemsError("customMutation", "custom mutation importUsers", &contract.ListBoundError{Max: 3})}, reason: "LIMIT_EXCEEDED", limit: "list_items", reported: 1},
		"public read failure":   {cause: forbidden},
		"internal fault":        {cause: &CustomResultError{cause: errCustomTest("secret internal detail")}, reported: 1},
	}
	for name, test := range cases {
		test := test
		t.Run(name, func(t *testing.T) {
			var reported []error
			presented := presentCommittedResult(context.Background(), "custom mutation importUsers", test.cause, []any{"root"}, func(_ context.Context, err error) { reported = append(reported, err) })
			if presented.Message != "custom mutation importUsers committed, but its result is unavailable" || presented.Extensions["code"] != "COMMITTED_RESULT_UNAVAILABLE" || presented.Extensions["committed"] != true {
				t.Fatalf("presented = %#v", presented)
			}
			if got, _ := presented.Extensions["reason"].(string); got != test.reason {
				t.Fatalf("reason = %q, want %q", got, test.reason)
			}
			if presented.Extensions["unreadable"] != test.unreadable || presented.Extensions["limit"] != test.limit {
				t.Fatalf("extensions = %#v", presented.Extensions)
			}
			if len(reported) != test.reported {
				t.Fatalf("reported %d times, want %d: %v", len(reported), test.reported, reported)
			}
			if len(presented.Path) != 1 || presented.Path[0] != "root" {
				t.Fatalf("path = %#v", presented.Path)
			}
			for _, secret := range []string{"secret"} {
				if strings.Contains(presented.Message, secret) {
					t.Fatalf("message echoes the cause: %q", presented.Message)
				}
				for _, value := range presented.Extensions {
					if text, ok := value.(string); ok && strings.Contains(text, secret) {
						t.Fatalf("extensions echo the cause: %#v", presented.Extensions)
					}
				}
			}
		})
	}
}

type capacityCaller struct {
	principalIsolationCaller
	remaining []golem.RuntimeModelRow
	takes     []int
}

func (caller *capacityCaller) ExecuteFrozenRead(_ context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	take, _ := request.Take()
	caller.takes = append(caller.takes, take)
	if take > 2 {
		overflow := readsql.ValidateStatementComplexity(policyir.ModelID{1}, strings.Repeat("x", 2), 1, readsql.MaxStatementAliases)
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "findMany", golem.ModelID{}, golem.FieldID{}, "read plan could not be rendered", overflow)
	}
	rows := caller.remaining[:take]
	caller.remaining = caller.remaining[take:]
	return rows, nil
}

func TestHydrationReadHalvesItsChunkWhenAStatementIsTooLarge(t *testing.T) {
	fixture := computedFixture(t)
	search := customContract(t, fixture.compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, fixture.compilation, compilerir.CustomOperationMutation, "importUsers")
	var rows []golem.RuntimeModelRow
	for key := 1; key <= 4; key++ {
		cell := golem.RuntimePresentReadCell(fixture.id, fixture.uuid(byte(key)), nil)
		row, err := golem.RuntimeModelReadRow(fixture.model, cell)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{fixture.id}, []golem.RuntimeReadCell{cell})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, golem.RuntimeModelRowWithIdentity(row, identity))
	}
	caller := &capacityCaller{remaining: append([]golem.RuntimeModelRow(nil), rows...)}
	query, err := BindCustomQuery[*capacityCaller, mixedCustomArgs, []golem.RuntimeModelRow](customSpec(search),
		func([]CustomArgument) (mixedCustomArgs, error) { return mixedCustomArgs{}, nil },
		func(context.Context, *capacityCaller, mixedCustomArgs) ([]golem.RuntimeModelRow, error) {
			return rows, nil
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
	mutation, err := BindCustomMutation[*capacityCaller, struct{}, golem.RuntimeModelRow](customSpec(importUsers),
		func([]CustomArgument) (struct{}, error) { return struct{}{}, nil },
		func(context.Context, *capacityCaller, struct{}) (golem.RuntimeModelRow, error) { return rows[0], nil },
		func(value golem.RuntimeModelRow) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:              fixture.bundle,
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil)},
		CustomBindings:      []CustomBinding{query, mutation},
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("unexpected internal error: %v", err) },
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
	response := server.Execute(context.Background(), 7, Request{Query: `query { searchUsers(where: { all: true }) { id } }`})
	if len(response.Errors) != 0 {
		t.Fatalf("errors = %#v", response.Errors)
	}
	items, _ := response.Data.(map[string]any)["searchUsers"].([]any)
	if len(items) != 4 || !reflect.DeepEqual(caller.takes, []int{4, 2, 2}) {
		t.Fatalf("items=%d reads with take %v, want 4 items read as [4 2 2]", len(items), caller.takes)
	}
}

type storedRowsCaller struct {
	principalIsolationCaller
	stored []golem.RuntimeModelRow
	takes  []int
}

func (caller *storedRowsCaller) ExecuteFrozenRead(_ context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	take, _ := request.Take()
	caller.takes = append(caller.takes, take)
	return caller.stored, nil
}

func TestComputedModelValuesAreHydratedOncePerSlotAndFailPerOccurrence(t *testing.T) {
	fixture := computedFixture(t)
	compiler, err := graphqloperation.New(fixture.compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	keyed := func(key byte) golem.RuntimeModelRow {
		cell := golem.RuntimePresentReadCell(fixture.id, fixture.uuid(key), nil)
		row, err := golem.RuntimeModelReadRow(fixture.model, cell)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{fixture.id}, []golem.RuntimeReadCell{cell})
		if err != nil {
			t.Fatal(err)
		}
		return golem.RuntimeModelRowWithIdentity(row, identity)
	}
	handBuilt, err := golem.RuntimeModelReadRow(fixture.model, golem.RuntimePresentReadCell(fixture.id, fixture.uuid(9), nil))
	if err != nil {
		t.Fatal(err)
	}
	var key policyir.FieldID
	copy(key[:], fixture.id[:])
	selection, err := readir.NewScalarSelection(key)
	if err != nil {
		t.Fatal(err)
	}
	user := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: fixture.contract.GraphQLName}
	slotFor := func(result compilerir.GraphQLTypeIR) selectset.Slot {
		return selectset.Slot{Kind: selectset.SlotComputed, ResponseName: "owner", Computed: &graphqlextension.ComputedSelection{Result: result, Projection: []readir.Selection{selection}}}
	}
	values := func() []any { return []any{keyed(1), keyed(2), keyed(3), handBuilt, nil} }

	caller := &storedRowsCaller{stored: []golem.RuntimeModelRow{keyed(1), keyed(2)}}
	execution := &computedExecution{hydrator: &resultHydrator{compiler: compiler, caller: caller}}

	nullable := user
	nullable.Nullable = true
	hydrated, err := execution.hydrateValues(context.Background(), slotFor(nullable), values(), nil)
	var failures interface{ ComputedFieldFailures() map[int]error }
	if !errors.As(err, &failures) || len(failures.ComputedFieldFailures()) != 1 || failures.ComputedFieldFailures()[3] == nil {
		t.Fatalf("only the row without identity may fail, got %v", err)
	}
	if hydrated[0] == nil || hydrated[1] == nil || hydrated[2] != nil || hydrated[4] != nil {
		t.Fatalf("hydrated = %#v", hydrated)
	}
	if !reflect.DeepEqual(caller.takes, []int{3}) {
		t.Fatalf("every parent's identities must be read in one request, got takes %v", caller.takes)
	}

	caller = &storedRowsCaller{stored: []golem.RuntimeModelRow{keyed(1), keyed(2)}}
	execution.hydrator.caller = caller
	_, err = execution.hydrateValues(context.Background(), slotFor(user), values(), nil)
	if !errors.As(err, &failures) {
		t.Fatalf("err = %v", err)
	}
	var missing *golem.Error
	if byIndex := failures.ComputedFieldFailures(); len(byIndex) != 2 || !errors.As(byIndex[2], &missing) || missing.Code != golem.CodeNotFound {
		t.Fatalf("a non-null computed result that is gone must be NOT_FOUND for that occurrence only: %v", byIndex)
	}

	if _, err := (&computedExecution{}).hydrateValues(context.Background(), slotFor(user), values(), nil); err == nil {
		t.Fatal("a computed model result was accepted without a caller to read it")
	}
	scalar := slotFor(compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"})
	if scalarValues, err := (&computedExecution{}).hydrateValues(context.Background(), scalar, []any{"x"}, nil); err != nil || scalarValues[0] != "x" {
		t.Fatalf("scalar results must pass through untouched: %v %v", scalarValues, err)
	}
}

func TestPresentErrorReportsAnExceededListMaximumOnceAndOtherLimitsNever(t *testing.T) {
	cases := map[string]struct {
		cause    error
		limit    string
		max      int
		reported int
	}{
		"list items": {cause: contract.ListItemsError("customQuery", "custom query searchLibrary", &contract.ListBoundError{Max: 100}), limit: "list_items", max: 100, reported: 1},
		"take":       {cause: golem.RuntimeLimitError("customQuery", golem.ModelID{}, golem.LimitTake, 7, "take is too large", nil), limit: "take", max: 7},
	}
	for name, test := range cases {
		test := test
		t.Run(name, func(t *testing.T) {
			var reported []error
			presented := PresentError(context.Background(), &CustomResultError{cause: test.cause}, []any{"root"}, func(_ context.Context, err error) { reported = append(reported, err) })
			if presented.Extensions["code"] != "LIMIT_EXCEEDED" || presented.Extensions["reason"] != "LIMIT_EXCEEDED" || presented.Extensions["limit"] != test.limit || presented.Extensions["max"] != test.max {
				t.Fatalf("presented = %#v", presented)
			}
			if len(reported) != test.reported {
				t.Fatalf("reported %d times, want %d: %v", len(reported), test.reported, reported)
			}
		})
	}
}
