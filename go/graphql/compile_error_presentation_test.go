package graphql

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func TestInvariantCompileErrorsAreInternalAndReported(t *testing.T) {
	compilation, _ := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "User" {
			compilation.Contract.Models[index].Exposed = false
		}
	}
	var reports []error
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: generatedTestBundle(t, compilation),
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			t.Fatal("a refused compile began a caller")
			return nil, nil
		},
		ReportInternalError: func(_ context.Context, report error) { reports = append(reports, report) },
	})
	if err != nil {
		t.Fatal(err)
	}
	post := generatedTestContract(t, compilation.Contract, "Post")
	query, queryErrors := gqlparser.LoadQuery(schema, `{ `+post.Roots.FindMany+` { author { id } } }`)
	if len(queryErrors) != 0 {
		t.Fatal(queryErrors)
	}
	response := executor.Execute(context.Background(), 1, Operation{Document: query, Definition: query.Operations[0]})
	want := Response{Errors: []Error{{Message: "internal server error", Path: []any{post.Roots.FindMany}, Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR"}}}}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response=%#v want %#v", response, want)
	}
	var invariant *compileerr.Error
	if len(reports) != 1 || !errors.As(reports[0], &invariant) || invariant.Kind() != compileerr.KindInvariant {
		t.Fatalf("reports=%#v want one Invariant", reports)
	}
	user := generatedTestContract(t, compilation.Contract, "User")
	if reports[0].Error() != "internal compile failure: model "+string(user.ModelID)+" is not exposed" {
		t.Fatalf("report text=%q", reports[0].Error())
	}
}

func TestSubscriptionCompileErrorsArePresentedThroughTheSharedPresenter(t *testing.T) {
	fixture, _ := p7ComputedSubscriptionFixture(t)
	var reports []error
	batch := fixture.batchBinding(t, nil, func(context.Context, []ComputedBatchParent, []ComputedArgument) (map[string]ComputedBatchResult, error) {
		return nil, nil
	})
	executorValue, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:           fixture.bundle,
		ComputedBindings: []ComputedBinding{fixture.greetingBinding(t), batch},
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			t.Fatal("a refused compile began a caller")
			return nil, nil
		},
		ReportInternalError: func(_ context.Context, report error) { reports = append(reports, report) },
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := executorValue.(SubscriptionExecutor[int])
	for _, test := range []struct {
		name, source, message string
		path                  []any
		extensions            map[string]any
	}{
		{name: "two-roots", source: `subscription { a: userEvents { eventID } b: userEvents { eventID } }`, message: "A subscription must select exactly one root field",
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "identity-with-selection", source: `subscription { userEvents { id { x } } }`, message: "Field id on User must not have a selection set", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "typename-with-selection", source: `subscription { userEvents { __typename { x } } }`, message: "Field __typename on User has an invalid shape for this event", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "entity-unknown-field", source: `subscription { userEvents { entity { nope } } }`, message: "Field nope on User is not available for this operation", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "where-empty", source: `subscription { userEvents(where: {}) { eventID } }`, message: "Argument where on User must not be empty", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where"}},
		{name: "metadata-with-selection", source: `subscription { userEvents { eventID { x } } }`, message: "Field eventID on User has an invalid shape for this event", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "entity-without-selection", source: `subscription { userEvents { entity } }`, message: "Field entity on User requires a selection set", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
		{name: "no-event-fields", source: `subscription($skip: Boolean!) { userEvents { eventID @skip(if: $skip) } }`, message: "Selection must not be empty", path: []any{"userEvents"},
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, parseErr := parser.ParseQuery(&ast.Source{Name: "subscription.graphql", Input: test.source})
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			_, err := executor.Subscribe(context.Background(), 1, Operation{Document: document, Definition: document.Operations[0], Variables: map[string]any{"skip": true}})
			if err == nil {
				t.Fatal("subscription compile accepted an invalid document")
			}
			presented := PresentError(context.Background(), err, nil, func(_ context.Context, report error) { reports = append(reports, report) })
			want := Error{Message: test.message, Path: test.path, Extensions: test.extensions}
			if !reflect.DeepEqual(presented, want) {
				t.Fatalf("presented=%#v want %#v", presented, want)
			}
		})
	}
	if len(reports) != 0 {
		t.Fatalf("typed subscription refusals were reported as internal: %#v", reports)
	}
}

func TestPresentCompileErrorCarriesEveryTypedField(t *testing.T) {
	root := func(err error) error { return compileerr.Root(err, "items") }
	reported := 0
	report := func(context.Context, error) { reported++ }
	tests := []struct {
		name string
		err  error
		path []any
		want Error
	}{
		{name: "range", err: root(compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerTake, compileerr.Path("take"), compileerr.Model("Post"), compileerr.Bounds(-5, 5))),
			want: Error{Message: "Argument take on Post must be between -5 and 5, excluding 0", Path: []any{"items"}, Extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take", "min": -5, "max": 5}}},
		{name: "limit", err: root(compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit("MaxListItems", 7))),
			want: Error{Message: "Input exceeds MaxListItems (7)", Path: []any{"items"}, Extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxListItems", "max": 7}}},
		{name: "explicit-path-wins-over-root", err: root(compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)), path: []any{"explicit"},
			want: Error{Message: "Selection must not be empty", Path: []any{"explicit"}, Extensions: map[string]any{"code": "BAD_USER_INPUT"}}},
		{name: "golem-error-passthrough", err: root(golem.RuntimeOperationError(golem.CodeNotFound, "read", golem.ModelID{}, golem.FieldID{}, "record not found", nil)),
			want: Error{Message: "record not found", Path: []any{"items"}, Extensions: map[string]any{"code": string(golem.CodeNotFound)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := PresentError(context.Background(), test.err, test.path, report)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("presented=%#v want %#v", got, test.want)
			}
		})
	}
	if reported != 0 {
		t.Fatalf("typed failures were reported as internal %d times", reported)
	}
	internal := PresentError(context.Background(), root(errors.New("untyped compile failure with secret detail")), nil, report)
	wantInternal := Error{Message: "internal server error", Path: []any{"items"}, Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR"}}
	if !reflect.DeepEqual(internal, wantInternal) || reported != 1 {
		t.Fatalf("untyped failure presented=%#v reported=%d", internal, reported)
	}
}

func TestSubscriptionInvariantsArePresentedAsInternalAndReportedOnce(t *testing.T) {
	fixture, _ := p7ComputedSubscriptionFixture(t)
	batch := fixture.batchBinding(t, nil, func(context.Context, []ComputedBatchParent, []ComputedArgument) (map[string]ComputedBatchResult, error) {
		return nil, nil
	})
	executorValue, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle:           fixture.bundle,
		ComputedBindings: []ComputedBinding{fixture.greetingBinding(t), batch},
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			t.Fatal("a refused compile began a caller")
			return nil, nil
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := executorValue.(SubscriptionExecutor[int])
	for name, source := range map[string]string{
		"unknown-root":        `subscription { nope { eventID } }`,
		"unknown-event-field": `subscription { userEvents { nope } }`,
	} {
		t.Run(name, func(t *testing.T) {
			document, parseErr := parser.ParseQuery(&ast.Source{Name: "subscription.graphql", Input: source})
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			_, err := executor.Subscribe(context.Background(), 1, Operation{Document: document, Definition: document.Operations[0]})
			var reports []error
			presented := PresentError(context.Background(), err, nil, func(_ context.Context, report error) { reports = append(reports, report) })
			root := "nope"
			if name == "unknown-event-field" {
				root = "userEvents"
			}
			want := Error{Message: "internal server error", Path: []any{root}, Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR"}}
			if !reflect.DeepEqual(presented, want) || len(reports) != 1 {
				t.Fatalf("presented=%#v reports=%d want %#v reported once", presented, len(reports), want)
			}
		})
	}
}
