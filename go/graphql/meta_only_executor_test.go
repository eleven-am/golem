package graphql

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type metaOnlyProbe struct {
	begins, authentications int
	beginErr, authErr       error
}

func metaOnlyOperation(t *testing.T, schema *ast.Schema, query string) Operation {
	t.Helper()
	document, errs := gqlparser.LoadQuery(schema, query)
	if len(errs) != 0 {
		t.Fatalf("query errors = %v", errs)
	}
	return Operation{Document: document, Definition: document.Operations[0], Variables: map[string]any{}}
}

func metaOnlyExecutor(t *testing.T, probe *metaOnlyProbe, authenticate bool) (Executor[int], *ast.Schema, string) {
	t.Helper()
	compilation, bundle := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	config := GeneratedExecutorConfig[int]{
		Bundle: bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			probe.begins++
			if probe.beginErr != nil {
				return nil, probe.beginErr
			}
			return &generatedTestCaller{}, nil
		},
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
	}
	if authenticate {
		config.AuthenticatePrincipal = func(context.Context, int) error {
			probe.authentications++
			return probe.authErr
		}
	}
	executor, err := NewGeneratedExecutor(config)
	if err != nil {
		t.Fatal(err)
	}
	return executor, schema, generatedTestContract(t, compilation.Contract, "Post").Roots.FindMany
}

func TestMetaOnlyOperationsAuthenticateWithoutBeginningACaller(t *testing.T) {
	policyFailure := errors.New("policy construction failed")
	for _, query := range []string{
		`{ __typename }`,
		`{ __schema { queryType { name } } }`,
		`{ ...Meta } fragment Meta on Query { kind: __typename __type(name: "Post") { name } }`,
		`mutation { __typename }`,
	} {
		probe := &metaOnlyProbe{beginErr: policyFailure}
		executor, schema, _ := metaOnlyExecutor(t, probe, true)
		response := executor.Execute(context.Background(), 1, metaOnlyOperation(t, schema, query))
		if len(response.Errors) != 0 || !reflect.DeepEqual(response.Data, map[string]any{}) {
			t.Fatalf("%s response=%#v", query, response)
		}
		if probe.begins != 0 || probe.authentications != 1 {
			t.Fatalf("%s begins=%d authentications=%d", query, probe.begins, probe.authentications)
		}
	}
}

func TestMetaOnlyOperationsAreRefusedExactlyLikeDataWhenThePrincipalIsRefused(t *testing.T) {
	refusal := golem.RuntimeReadError(golem.CodeUnauthenticated, "begin", golem.ModelID{}, golem.FieldID{}, "principal could not be resolved", errors.New("session revoked"))
	probe := &metaOnlyProbe{beginErr: refusal, authErr: refusal}
	executor, schema, findMany := metaOnlyExecutor(t, probe, true)
	data := executor.Execute(context.Background(), 1, metaOnlyOperation(t, schema, `{ `+findMany+`(take: 1) { title } }`))
	if data.Data != nil || len(data.Errors) != 1 || data.Errors[0].Extensions["code"] != "UNAUTHENTICATED" {
		t.Fatalf("data response=%#v", data)
	}
	for _, query := range []string{`{ __typename }`, `{ __schema { queryType { name } } }`, `mutation { __typename }`} {
		meta := executor.Execute(context.Background(), 1, metaOnlyOperation(t, schema, query))
		if !reflect.DeepEqual(meta, data) {
			t.Fatalf("%s response=%#v, data response=%#v", query, meta, data)
		}
	}
	if probe.begins != 1 || probe.authentications != 3 {
		t.Fatalf("begins=%d authentications=%d", probe.begins, probe.authentications)
	}
}

func TestMixedMetaAndDataOperationsRunTheFullCallerSetup(t *testing.T) {
	probe := &metaOnlyProbe{}
	executor, schema, findMany := metaOnlyExecutor(t, probe, true)
	response := executor.Execute(context.Background(), 1, metaOnlyOperation(t, schema, `{ __typename `+findMany+`(take: 1) { title } }`))
	if len(response.Errors) != 0 || probe.begins != 1 || probe.authentications != 0 {
		t.Fatalf("response=%#v begins=%d authentications=%d", response, probe.begins, probe.authentications)
	}
}

func TestMetaOnlyOperationsWithoutAPrincipalAuthenticatorFallBackToTheFullCallerSetup(t *testing.T) {
	probe := &metaOnlyProbe{}
	executor, schema, _ := metaOnlyExecutor(t, probe, false)
	response := executor.Execute(context.Background(), 1, metaOnlyOperation(t, schema, `{ __typename }`))
	if len(response.Errors) != 0 || !reflect.DeepEqual(response.Data, map[string]any{}) || probe.begins != 1 {
		t.Fatalf("response=%#v begins=%d", response, probe.begins)
	}
}
