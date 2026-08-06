package graphql

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type principalIsolationCaller struct {
	id        int64
	principal int
	row       golem.RuntimeModelRow
	reads     atomic.Int64
	dbReads   *atomic.Int64
}

func (caller *principalIsolationCaller) ExecuteFrozenRead(context.Context, golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	caller.reads.Add(1)
	if caller.dbReads != nil {
		caller.dbReads.Add(1)
	}
	return []golem.RuntimeModelRow{caller.row}, nil
}

// The generated runtime Caller implements this same marker. Keeping it on the
// spy proves that the instance shared by ordinary roots is also the exact
// capability eligible for an operation-local custom-root scope; System-like
// fallback values cannot be substituted by this test.
func (*principalIsolationCaller) GolemGraphQLCallerCapability() {}

func TestGraphQLOperationCreatesOneCallerExecutionAndSharesOnlyWithinOperation(t *testing.T) {
	fixture := computedFixture(t)
	server, callers, batchCalls := principalIsolationServer(t, fixture)
	document := principalIsolationDocument(fixture)
	variables := map[string]any{"prefix": "same"}

	for operation := 1; operation <= 2; operation++ {
		response := server.Execute(context.Background(), operation, Request{Query: document, OperationName: "Isolation", Variables: variables})
		assertPrincipalIsolationResponse(t, response, operation)
	}
	if got := len(*callers); got != 2 {
		t.Fatalf("BeginCaller allocations = %d, want one fresh caller for each of two operations", got)
	}
	if (*callers)[0] == (*callers)[1] || (*callers)[0].id == (*callers)[1].id {
		t.Fatalf("caller crossed operation boundary: first=%p/%d second=%p/%d", (*callers)[0], (*callers)[0].id, (*callers)[1], (*callers)[1].id)
	}
	for index, caller := range *callers {
		if caller.reads.Load() != 2 {
			t.Fatalf("operation %d used caller %d for %d roots, want 2", index+1, caller.id, caller.reads.Load())
		}
		if _, ok := any(caller).(CustomCallerCapability); !ok {
			t.Fatalf("operation %d caller is not the custom-root-eligible caller capability", index+1)
		}
	}
	// Both roots have the same computed field, arguments, and row cache key.
	// One loader scope therefore coalesces within an operation, but a fresh
	// scope forces one new load for the second operation.
	if got := batchCalls.Load(); got != 2 {
		t.Fatalf("computed batch loads = %d, want one per operation", got)
	}
}

type principalIsolationContextKey struct{}

func TestGraphQLMissingInvalidPrincipalTouchesDatabaseZeroTimes(t *testing.T) {
	fixture := computedFixture(t)
	document, err := graphqlschema.Build(fixture.compilation)
	if err != nil {
		t.Fatal(err)
	}
	var begins, dbReads atomic.Int64
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:         fixture.bundle,
		CustomBindings: fixture.stubCustomBindings(t),
		ComputedBindings: []ComputedBinding{
			fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil),
		},
		BeginCaller: func(_ context.Context, principal int) (CallerExecution, error) {
			begins.Add(1)
			if principal <= 0 {
				return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "graphql", fixture.model, golem.FieldID{}, "principal is invalid", nil)
			}
			return &principalIsolationCaller{principal: principal, row: principalIsolationRow(t, fixture, principal), dbReads: &dbReads}, nil
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			principal, ok := ctx.Value(principalIsolationContextKey{}).(int)
			return principal, ok
		},
		ReportInternalError: func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	query := `query { ` + fixture.contract.Roots.FindMany + `(take: 1) { id } }`

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"`+query+`"}`)))
	if !strings.Contains(missing.Body.String(), "UNAUTHENTICATED") || begins.Load() != 0 || dbReads.Load() != 0 {
		t.Fatalf("missing principal response=%s begins=%d db=%d", missing.Body.String(), begins.Load(), dbReads.Load())
	}

	invalidRequest := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"`+query+`"}`))
	invalidRequest = invalidRequest.WithContext(context.WithValue(invalidRequest.Context(), principalIsolationContextKey{}, -1))
	invalidHTTP := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidHTTP, invalidRequest)
	if !strings.Contains(invalidHTTP.Body.String(), "UNAUTHENTICATED") || begins.Load() != 1 || dbReads.Load() != 0 {
		t.Fatalf("invalid HTTP principal response=%s begins=%d db=%d", invalidHTTP.Body.String(), begins.Load(), dbReads.Load())
	}

	invalidDirect := server.Execute(context.Background(), -2, Request{Query: query})
	if len(invalidDirect.Errors) != 1 || invalidDirect.Errors[0].Extensions["code"] != "UNAUTHENTICATED" || begins.Load() != 2 || dbReads.Load() != 0 {
		t.Fatalf("invalid direct principal response=%#v begins=%d db=%d", invalidDirect, begins.Load(), dbReads.Load())
	}
}

func TestGraphQLConcurrentPrincipalIsolationWithSameDocumentsVariablesKeysAndAliases(t *testing.T) {
	fixture := computedFixture(t)
	server, callers, batchCalls := principalIsolationServer(t, fixture)
	document := principalIsolationDocument(fixture)
	variables := map[string]any{"prefix": "same"}
	const operations = 128

	start := make(chan struct{})
	results := make(chan error, operations)
	var workers sync.WaitGroup
	workers.Add(operations)
	for principal := 1; principal <= operations; principal++ {
		principal := principal
		go func() {
			defer workers.Done()
			<-start
			response := server.Execute(context.Background(), principal, Request{Query: document, OperationName: "Isolation", Variables: variables})
			results <- principalIsolationResponseError(response, principal)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(*callers); got != operations {
		t.Fatalf("concurrent caller allocations = %d, want %d", got, operations)
	}
	seen := make(map[int64]struct{}, operations)
	for _, caller := range *callers {
		if caller.reads.Load() != 2 {
			t.Fatalf("principal %d caller %d read count = %d", caller.principal, caller.id, caller.reads.Load())
		}
		if _, duplicate := seen[caller.id]; duplicate {
			t.Fatalf("caller identity %d was reused across principals", caller.id)
		}
		seen[caller.id] = struct{}{}
	}
	// Every principal deliberately uses UUID(...01) and identical computed
	// arguments/aliases. Only operation-local loaders can still return all 128
	// distinct dependency-derived values.
	if got := batchCalls.Load(); got != operations {
		t.Fatalf("concurrent computed batches = %d, want one isolated load per operation", got)
	}
}

func principalIsolationServer(t *testing.T, fixture computedTestFixture) (*Server[int], *[]*principalIsolationCaller, *atomic.Int64) {
	t.Helper()
	document, err := graphqlschema.Build(fixture.compilation)
	if err != nil {
		t.Fatal(err)
	}
	var nextID atomic.Int64
	var batchCalls atomic.Int64
	callers := &[]*principalIsolationCaller{}
	var callersLock sync.Mutex
	greeting, err := BindComputed(string(fixture.greeting.ExtensionID), func(_ context.Context, request ComputedRequest) (any, error) {
		return principalIsolationName(request.Parent, fixture.name)
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := fixture.batchBinding(t, nil, func(_ context.Context, parents []ComputedBatchParent, _ []ComputedArgument) (map[string]ComputedBatchResult, error) {
		batchCalls.Add(1)
		result := make(map[string]ComputedBatchResult, len(parents))
		for _, parent := range parents {
			name, nameErr := principalIsolationName(parent.Parent(), fixture.name)
			if nameErr != nil {
				return nil, nameErr
			}
			result[parent.CacheKey()] = ComputedBatchResult{Value: name}
		}
		return result, nil
	})
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: fixture.bundle, ComputedBindings: []ComputedBinding{greeting, batch},
		CustomBindings: fixture.stubCustomBindings(t),
		BeginCaller: func(_ context.Context, principal int) (CallerExecution, error) {
			caller := &principalIsolationCaller{id: nextID.Add(1), principal: principal, row: principalIsolationRow(t, fixture, principal)}
			callersLock.Lock()
			*callers = append(*callers, caller)
			callersLock.Unlock()
			return caller, nil
		},
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("unexpected GraphQL isolation error: %v", err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 0, false },
		Limits:               Limits{MaxResolverConcurrency: 256},
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("unexpected GraphQL isolation server error: %v", err)
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return server, callers, &batchCalls
}

func principalIsolationDocument(fixture computedTestFixture) string {
	root := fixture.contract.Roots.FindMany
	return `query Isolation($prefix: String!) {
  first: ` + root + `(take: 1) { id name greeting(prefix: $prefix) batchGreeting(prefix: $prefix) }
  second: ` + root + `(take: 1) { id name greeting(prefix: $prefix) batchGreeting(prefix: $prefix) }
}`
}

func principalIsolationRow(t testing.TB, fixture computedTestFixture, principal int) golem.RuntimeModelRow {
	t.Helper()
	row, err := golem.RuntimeModelReadRow(fixture.model,
		// Identical primary/computed cache key across every principal is
		// intentional; only the masked dependency differs.
		golem.RuntimePresentReadCell(fixture.id, fixture.uuid(1), nil),
		golem.RuntimePresentReadCell(fixture.name, fmt.Sprintf("principal-%03d", principal), nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func principalIsolationName(row golem.RuntimeModelRow, field golem.FieldID) (string, error) {
	cell := golem.RuntimeTransportField(row, field)
	value, present := cell.Get()
	name, ok := value.(string)
	if !present || !ok {
		return "", fmt.Errorf("principal dependency is absent or has type %T", value)
	}
	return name, nil
}

func assertPrincipalIsolationResponse(t testing.TB, response Response, principal int) {
	t.Helper()
	if err := principalIsolationResponseError(response, principal); err != nil {
		t.Fatal(err)
	}
}

func principalIsolationResponseError(response Response, principal int) error {
	if len(response.Errors) != 0 || response.Data == nil {
		return fmt.Errorf("principal %d response=%#v", principal, response)
	}
	want := fmt.Sprintf("principal-%03d", principal)
	data, ok := response.Data.(map[string]any)
	if !ok {
		return fmt.Errorf("principal %d data type = %T", principal, response.Data)
	}
	for _, alias := range []string{"first", "second"} {
		rows, ok := data[alias].([]any)
		if !ok || len(rows) != 1 {
			return fmt.Errorf("principal %d alias %s rows=%#v", principal, alias, data[alias])
		}
		row, ok := rows[0].(map[string]any)
		if !ok || row["name"] != want || row["greeting"] != want || row["batchGreeting"] != want {
			return fmt.Errorf("principal %d alias %s row=%#v want dependency %q", principal, alias, rows[0], want)
		}
	}
	return nil
}
