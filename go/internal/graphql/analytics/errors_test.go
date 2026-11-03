package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

var (
	socialOnce        sync.Once
	socialCompilation compilerir.CompilationIR
	socialFailure     error
)

func social(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	socialOnce.Do(func() {
		result := compile.Compile(context.Background(), compile.Config{Dir: "../../../runtime/testdata/p5social", Pattern: "."})
		if len(result.Diagnostics) != 0 || result.Compilation == nil {
			socialFailure = errors.New("p5social did not compile")
			return
		}
		socialCompilation = *result.Compilation
	})
	if socialFailure != nil {
		t.Fatal(socialFailure)
	}
	return socialCompilation
}

var (
	metricsOnce        sync.Once
	metricsCompilation compilerir.CompilationIR
	metricsFailure     error
)

func metricsCompiler(t *testing.T) *Compiler {
	t.Helper()
	metricsOnce.Do(func() {
		result := compile.Compile(context.Background(), compile.Config{Dir: "../../../runtime/testdata/p6metrics", Pattern: "."})
		if len(result.Diagnostics) != 0 || result.Compilation == nil {
			metricsFailure = errors.New("p6metrics did not compile")
			return
		}
		metricsCompilation = *result.Compilation
	})
	if metricsFailure != nil {
		t.Fatal(metricsFailure)
	}
	compiler, err := New(metricsCompilation, Limits{Bind: socialBindLimits})
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func socialCompiler(t *testing.T) *Compiler {
	t.Helper()
	compiler, err := New(social(t), Limits{Bind: socialBindLimits})
	if err != nil {
		t.Fatal(err)
	}
	compiler.limits.ListItems = 2
	return compiler
}

func compileRoot(t *testing.T, compiler *Compiler, source string, variables map[string]any) error {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "analytics.graphql", Input: source})
	if err != nil {
		t.Fatal(err)
	}
	field := document.Operations[0].SelectionSet[0].(*ast.Field)
	_, matched, err := compiler.Compile(field, document.Fragments, variables)
	if !matched {
		t.Fatalf("%s did not match an analytics root", field.Name)
	}
	return err
}

type analyticsCase struct {
	name      string
	source    string
	variables map[string]any
	kind      compileerr.Kind
	message   string
	argument  string
}

func assertAnalyticsCases(t *testing.T, compiler *Compiler, cases []analyticsCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := compileRoot(t, compiler, test.source, test.variables)
			var typed *compileerr.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error=%v is not a compileerr.Error", err)
			}
			if typed.Kind() != test.kind || typed.Message() != test.message || typed.Argument() != test.argument {
				t.Fatalf("kind=%d message=%q argument=%q want kind=%d message=%q argument=%q", typed.Kind(), typed.Message(), typed.Argument(), test.kind, test.message, test.argument)
			}
			if err.Error() != test.message {
				t.Fatalf("Error()=%q want the public message %q", err.Error(), test.message)
			}
		})
	}
}

var socialBindLimits = graphqlbind.Limits{MaxInputDepth: 3, MaxInputNodes: 64, MaxListItems: 2}

func TestAggregateRefusalsAreTypedAndLocated(t *testing.T) {
	assertAnalyticsCases(t, socialCompiler(t), []analyticsCase{
		{"aggregate-without-measure", `{ aggregatePosts { __typename } }`, nil, compileerr.KindNotAllowedHere, "An aggregate on Post must select at least one measure", ""},
		{"aggregate-where-null", `{ aggregatePosts(where: null) { count } }`, nil, compileerr.KindNullNotAllowed, "Argument where on Post cannot be null", "where"},
		{"aggregate-where-empty", `{ aggregatePosts(where: {}) { count } }`, nil, compileerr.KindEmpty, "Argument where on Post must not be empty", "where"},
		{"aggregate-argument-not-accepted", `{ aggregatePosts(skip: 1) { count } }`, nil, compileerr.KindFieldUnavailable, "Argument skip on Post is not available for this operation", "skip"},
		{"aggregate-unparseable-argument", `{ aggregatePosts(where: 99999999999999999999) { count } }`, nil, compileerr.KindInvalidValue, "Argument where on aggregatePosts has the wrong kind of value", "where"},
		{"aggregate-count-with-children", `{ aggregatePosts { count { x } } }`, nil, compileerr.KindSelectionShape, "Field count on Post must not have a selection set", ""},
		{"aggregate-typename-with-children", `{ aggregatePosts { __typename { x } count } }`, nil, compileerr.KindSelectionShape, "Field __typename on Post must not have a selection set", ""},
		{"aggregate-has-no-key", `{ aggregatePosts { count key { title } } }`, nil, compileerr.KindFieldUnavailable, "Field key on Post is not available for this operation", ""},
		{"aggregate-unknown-output-field", `{ aggregatePosts { nope } }`, nil, compileerr.KindFieldUnavailable, "Field nope on Post is not available for this operation", ""},
		{"aggregate-everything-skipped", `{ aggregatePosts { count @skip(if: true) } }`, nil, compileerr.KindEmpty, "Selection must not be empty", ""},
		{"aggregate-measure-category-empty", `{ aggregatePosts { sum { reactions @skip(if: true) } } }`, nil, compileerr.KindEmpty, "Selection must not be empty", ""},
		{"aggregate-measure-unsupported-for-field", `{ aggregatePosts { sum { title } } }`, nil, compileerr.KindOperatorUnsupported, "Input sum.title on Post is not supported for this field or provider", "sum.title"},
		{"aggregate-measure-unknown-field", `{ aggregatePosts { sum { nope } } }`, nil, compileerr.KindFieldUnavailable, "Field nope on Post is not available for this operation", ""},
		{"aggregate-measure-with-children", `{ aggregatePosts { sum { reactions { x } } } }`, nil, compileerr.KindFieldUnavailable, "Field reactions on Post is not available for this operation", ""},
		{"aggregate-fragment-missing", `{ aggregatePosts { ...Missing } }`, nil, compileerr.KindFragmentInvalid, "Fragment spreads in this operation cannot be resolved", ""},
		{"aggregate-fragment-cycle", `{ aggregatePosts { ...Loop } } fragment Loop on PostAggregate { count ...Loop }`, nil, compileerr.KindFragmentInvalid, "Fragment spreads in this operation form a cycle", ""},
		{"key-selection-fragment-missing", `{ groupByPosts(by: [title]) { key { ...Missing } } }`, nil, compileerr.KindFragmentInvalid, "Fragment spreads in this operation cannot be resolved", ""},
		{"measure-selection-fragment-missing", `{ aggregatePosts { sum { ...Missing } } }`, nil, compileerr.KindFragmentInvalid, "Fragment spreads in this operation cannot be resolved", ""},
		{"inline-fragment-fragment-missing", `{ aggregatePosts { ... { ...Missing } } }`, nil, compileerr.KindFragmentInvalid, "Fragment spreads in this operation cannot be resolved", ""},
		{"directive-value-unparseable", `{ aggregatePosts { count @skip(if: 99999999999999999999) } }`, nil, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if", ""},
		{"aggregate-fields-cannot-merge", `{ aggregatePosts { same: count same: sum { reactions } } }`, nil, compileerr.KindFragmentInvalid, "Fields with the same response name cannot be merged", ""},
		{"aggregate-skip-not-boolean", `{ aggregatePosts { count @skip(if: "yes") } }`, nil, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if", ""},
		{"aggregate-include-without-if", `{ aggregatePosts { count @include } }`, nil, compileerr.KindDirectiveInvalid, "Directive @include requires a Boolean if", ""},
		{"aggregate-inline-fragment-skip-not-boolean", `{ aggregatePosts { ... @skip(if: 1) { count } } }`, nil, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if", ""},
		{"aggregate-spread-skip-not-boolean", `{ aggregatePosts { ...Part @skip(if: 1) } } fragment Part on PostAggregate { count }`, nil, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if", ""},
	})
}

func TestGroupRefusalsAreTypedAndLocated(t *testing.T) {
	assertAnalyticsCases(t, socialCompiler(t), []analyticsCase{
		{"by-absent", `{ groupByPosts { count } }`, nil, compileerr.KindRequired, "Argument by on Post is required", "by"},
		{"by-empty", `{ groupByPosts(by: []) { count } }`, nil, compileerr.KindEmpty, "Argument by on Post must not be empty", "by"},
		{"by-not-a-list", `{ groupByPosts(by: title) { count } }`, nil, compileerr.KindInvalidValue, "Argument by on Post has the wrong kind of value", "by"},
		{"by-item-not-text", `{ groupByPosts(by: [1]) { count } }`, nil, compileerr.KindInvalidValue, "Input by[0] on Post must be of type String", "by[0]"},
		{"by-duplicate", `{ groupByPosts(by: [title, title]) { count } }`, nil, compileerr.KindDuplicate, "Argument by on Post names a field more than once", "by"},
		{"by-unknown-dimension", `{ groupByPosts(by: [title, nope]) { count } }`, nil, compileerr.KindFieldUnavailable, "Input by[1] on Post is not available for this operation", "by[1]"},
		{"by-relation-dimension-on-local-group", `{ groupByPosts(by: [authorName]) { count } }`, nil, compileerr.KindFieldUnavailable, "Input by[0] on Post is not available for this operation", "by[0]"},
		{"relation-group-without-relation-dimension", `{ relationGroupByPosts(by: [title]) { count } }`, nil, compileerr.KindNotAllowedHere, "Relation grouping on Post requires a relation dimension in by", "by"},
		{"argument-not-accepted", `{ groupByPosts(by: [title], nope: 1) { count } }`, nil, compileerr.KindFieldUnavailable, "Argument nope on Post is not available for this operation", "nope"},
		{"selected-key-not-in-by", `{ groupByPosts(by: [title]) { key { id } } }`, nil, compileerr.KindNotAllowedHere, "Key id on Post must be listed in by", "id"},
		{"key-unknown-field", `{ groupByPosts(by: [title]) { key { nope } } }`, nil, compileerr.KindFieldUnavailable, "Field nope on Post is not available for this operation", ""},
		{"key-relation-field-on-local-group", `{ groupByPosts(by: [title]) { key { authorName } } }`, nil, compileerr.KindFieldUnavailable, "Field authorName on Post is not available for this operation", ""},
		{"key-selection-skipped", `{ groupByPosts(by: [title]) { key { title @skip(if: true) } } }`, nil, compileerr.KindEmpty, "Selection must not be empty", ""},
		{"skip-negative", `{ groupByPosts(by: [title], skip: -1) { count } }`, nil, compileerr.KindIntegerRange, "Argument skip on Post must be at least 0", "skip"},
		{"skip-platform", `query($k: Int) { groupByPosts(by: [title], skip: $k) { count } }`, map[string]any{"k": json.Number("99999999999")}, compileerr.KindIntegerRange, "Argument skip on Post is outside the supported integer range", "skip"},
		{"skip-wrong-kind", `{ groupByPosts(by: [title], skip: "x") { count } }`, nil, compileerr.KindInvalidValue, "Argument skip on Post must be of type Int", "skip"},
		{"take-zero", `{ groupByPosts(by: [title], take: 0) { count } }`, nil, compileerr.KindIntegerRange, "Argument take on Post must not be 0", "take"},
		{"take-over-maximum", `{ groupByPosts(by: [title], take: 101) { count } }`, nil, compileerr.KindIntegerRange, "Argument take on Post must be between -100 and 100, excluding 0", "take"},
		{"take-negative-over-maximum", `{ groupByPosts(by: [title], take: -101) { count } }`, nil, compileerr.KindIntegerRange, "Argument take on Post must be between -100 and 100, excluding 0", "take"},
		{"take-platform", `query($n: Int) { groupByPosts(by: [title], take: $n) { count } }`, map[string]any{"n": json.Number("99999999999")}, compileerr.KindIntegerRange, "Argument take on Post is outside the supported integer range", "take"},
		{"take-unparseable", `{ groupByPosts(by: [title], take: 99999999999999999999) { count } }`, nil, compileerr.KindInvalidValue, "Argument take on groupByPosts has the wrong kind of value", "take"},
		{"where-refusal-keeps-its-path", `{ groupByPosts(by: [title], where: {AND: []}) { count } }`, nil, compileerr.KindEmpty, "Input where.AND on Post must not be empty", "where.AND"},
	})
}

func TestHavingRefusalsAreTypedAndLocated(t *testing.T) {
	assertAnalyticsCases(t, socialCompiler(t), []analyticsCase{
		{"having-null", `{ groupByPosts(by: [title], having: null) { count } }`, nil, compileerr.KindNullNotAllowed, "Argument having on Post cannot be null", "having"},
		{"having-empty", `{ groupByPosts(by: [title], having: {}) { count } }`, nil, compileerr.KindEmpty, "Argument having on Post must not be empty", "having"},
		{"having-not-an-object", `{ groupByPosts(by: [title], having: "x") { count } }`, nil, compileerr.KindInvalidValue, "Argument having on Post has the wrong kind of value", "having"},
		{"having-and-empty", `{ groupByPosts(by: [title], having: {AND: []}) { count } }`, nil, compileerr.KindEmpty, "Input having.AND on Post must not be empty", "having.AND"},
		{"having-or-not-a-list", `{ groupByPosts(by: [title], having: {OR: "x"}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.OR on Post has the wrong kind of value", "having.OR"},
		{"having-list-items", `{ groupByPosts(by: [title], having: {AND: [{count: {gte: "1"}}, {count: {gte: "1"}}, {count: {gte: "1"}}]}) { count } }`, nil, compileerr.KindInputLimit, "Input exceeds MaxListItems (2)", ""},
		{"having-depth", `{ groupByPosts(by: [title], having: {NOT: {NOT: {NOT: {count: {gte: "1"}}}}}) { count } }`, nil, compileerr.KindInputLimit, "Input exceeds MaxInputDepth (3)", ""},
		{"having-nested-element-is-indexed", `{ groupByPosts(by: [title], having: {AND: [{count: {gte: "1"}}, {}]}) { count } }`, nil, compileerr.KindEmpty, "Input having.AND[1] on Post must not be empty", "having.AND[1]"},
		{"having-not-keeps-its-path", `{ groupByPosts(by: [title], having: {NOT: {}}) { count } }`, nil, compileerr.KindEmpty, "Input having.NOT on Post must not be empty", "having.NOT"},
		{"having-unknown-member", `{ groupByPosts(by: [title], having: {nope: 1}) { count } }`, nil, compileerr.KindFieldUnavailable, "Input having.nope on Post is not available for this operation", "having.nope"},
		{"having-category-empty", `{ groupByPosts(by: [title], having: {sum: {}}) { count } }`, nil, compileerr.KindEmpty, "Input having.sum on Post must not be empty", "having.sum"},
		{"having-category-not-an-object", `{ groupByPosts(by: [title], having: {sum: 1}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.sum on Post has the wrong kind of value", "having.sum"},
		{"having-comparison-empty", `{ groupByPosts(by: [title], having: {count: {}}) { count } }`, nil, compileerr.KindEmpty, "Input having.count on Post must not be empty", "having.count"},
		{"having-comparison-not-an-object", `{ groupByPosts(by: [title], having: {count: 1}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.count on Post has the wrong kind of value", "having.count"},
		{"having-key-not-in-by", `{ groupByPosts(by: [title], having: {key: {id: {equals: "00000000-0000-0000-0000-000000000001"}}}) { count } }`, nil, compileerr.KindNotAllowedHere, "Key having.key.id on Post must be listed in by", "having.key.id"},
		{"having-key-unknown", `{ groupByPosts(by: [title], having: {key: {nope: {equals: 1}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.key.nope on Post is not supported for this field or provider", "having.key.nope"},
		{"having-measure-unsupported-for-field", `{ groupByPosts(by: [title], having: {sum: {title: {gte: "a"}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.sum.title on Post is not supported for this field or provider", "having.sum.title"},
		{"having-mode-unknown", `{ groupByPosts(by: [title], having: {key: {title: {equals: "a", mode: bogus}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.key.title.mode on Post is not one of the allowed values", "having.key.title.mode"},
		{"having-text-operator-on-count", `{ groupByPosts(by: [title], having: {count: {contains: "a"}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.count.contains on Post is not supported for this field or provider", "having.count.contains"},
		{"having-text-operator-on-uuid", `{ groupByPosts(by: [id], having: {key: {id: {startsWith: "a"}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.key.id.startsWith on Post is not supported for this field or provider", "having.key.id.startsWith"},
		{"having-text-operand-not-text", `{ groupByPosts(by: [title], having: {key: {title: {contains: 1}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.key.title.contains on Post must be of type String", "having.key.title.contains"},
		{"having-is-null-not-boolean", `{ groupByPosts(by: [title], having: {count: {isNull: "x"}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.count.isNull on Post must be of type Boolean", "having.count.isNull"},
		{"having-mode-on-equality", `{ groupByPosts(by: [title], having: {key: {title: {equals: "a", mode: insensitive}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.key.title.mode on Post is not supported for this field or provider", "having.key.title.mode"},
		{"having-explicit-sensitive-mode-on-range", `{ groupByPosts(by: [title], having: {key: {title: {gte: "a", mode: sensitive}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.key.title.mode on Post is not supported for this field or provider", "having.key.title.mode"},
		{"having-unknown-comparison", `{ groupByPosts(by: [title], having: {count: {nope: 1}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.count.nope on Post is not supported for this field or provider", "having.count.nope"},
		{"having-null-operand", `{ groupByPosts(by: [title], having: {count: {equals: null}}) { count } }`, nil, compileerr.KindNullNotAllowed, "Input having.count.equals on Post cannot be null", "having.count.equals"},
		{"having-count-malformed", `{ groupByPosts(by: [title], having: {count: {gte: "x"}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.count.gte on Post is not a valid BigInt", "having.count.gte"},
		{"having-count-out-of-range", `{ groupByPosts(by: [title], having: {count: {gte: "9223372036854775808"}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.count.gte on Post is outside the BigInt range", "having.count.gte"},
		{"having-value-wrong-kind", `{ groupByPosts(by: [title], having: {key: {title: {equals: 1}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.key.title.equals on Post must be of type String", "having.key.title.equals"},
	})
}

func TestOrderRefusalsAreTypedAndLocated(t *testing.T) {
	assertAnalyticsCases(t, socialCompiler(t), []analyticsCase{
		{"order-empty", `{ groupByPosts(by: [title], orderBy: []) { count } }`, nil, compileerr.KindEmpty, "Argument orderBy on Post must not be empty", "orderBy"},
		{"order-not-a-list", `{ groupByPosts(by: [title], orderBy: "x") { count } }`, nil, compileerr.KindInvalidValue, "Argument orderBy on Post has the wrong kind of value", "orderBy"},
		{"order-list-items", `{ groupByPosts(by: [title], orderBy: [{count: asc}, {count: asc}, {count: asc}]) { count } }`, nil, compileerr.KindInputLimit, "Input exceeds MaxListItems (2)", ""},
		{"order-item-empty", `{ groupByPosts(by: [title], orderBy: [{}]) { count } }`, nil, compileerr.KindExactlyOne, "Input orderBy[0] on Post must set exactly one member", "orderBy[0]"},
		{"order-category-empty", `{ groupByPosts(by: [title], orderBy: [{key: {}}]) { count } }`, nil, compileerr.KindExactlyOne, "Input orderBy[0].key on Post must set exactly one member", "orderBy[0].key"},
		{"order-count-twice", `{ groupByPosts(by: [title], orderBy: [{count: asc}, {count: desc}]) { count } }`, nil, compileerr.KindDuplicate, "Argument orderBy on Post names a field more than once", "orderBy"},
		{"order-key-twice", `{ groupByPosts(by: [title], orderBy: [{key: {title: asc}}, {key: {title: desc}}]) { count } }`, nil, compileerr.KindDuplicate, "Argument orderBy on Post names a field more than once", "orderBy"},
		{"order-key-unknown", `{ groupByPosts(by: [title], orderBy: [{key: {nope: asc}}]) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input orderBy[0].key.nope on Post is not supported for this field or provider", "orderBy[0].key.nope"},
		{"order-measure-unsupported", `{ groupByPosts(by: [title], orderBy: [{sum: {title: asc}}]) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input orderBy[0].sum.title on Post is not supported for this field or provider", "orderBy[0].sum.title"},
		{"order-key-not-in-by", `{ groupByPosts(by: [title], orderBy: [{key: {id: asc}}]) { count } }`, nil, compileerr.KindNotAllowedHere, "Key id on Post must be listed in by", "id"},
	})
}

func TestMeasureValueRefusalsOnTypedMetricsAreTypedAndLocated(t *testing.T) {
	assertAnalyticsCases(t, metricsCompiler(t), []analyticsCase{
		{"integer-sum-malformed", `{ groupByMetrics(by: [label], having: {sum: {integer: {gte: "x"}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.sum.integer.gte on Metric is not a valid BigInt", "having.sum.integer.gte"},
		{"decimal-sum-malformed", `{ groupByMetrics(by: [label], having: {sum: {amount: {gte: "x"}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.sum.amount.gte on Metric is not a valid Decimal", "having.sum.amount.gte"},
		{"average-malformed", `{ groupByMetrics(by: [label], having: {avg: {double: {gte: "x"}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.avg.double.gte on Metric must be of type Float", "having.avg.double.gte"},
		{"enum-value-unknown", `{ groupByMetrics(by: [state], having: {key: {state: {equals: NOPE}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.key.state.equals on Metric is not one of the allowed values", "having.key.state.equals"},
		{"text-operator-on-number", `{ groupByMetrics(by: [integer], having: {key: {integer: {startsWith: "a"}}}) { count } }`, nil, compileerr.KindOperatorUnsupported, "Input having.key.integer.startsWith on Metric is not supported for this field or provider", "having.key.integer.startsWith"},
		{"extension-value-wrong-kind", `{ groupByMetrics(by: [integer], having: {key: {integer: {equals: "x"}}}) { count } }`, nil, compileerr.KindInvalidValue, "Input having.key.integer.equals on Metric must be of type Int", "having.key.integer.equals"},
	})
}

func TestAnalyticsFaultsNoCallerCanCauseAreInvariants(t *testing.T) {
	compiler := socialCompiler(t)
	invariants := map[string]error{
		"direction":          func() error { _, err := sortDirection("sideways"); return err }(),
		"direction-not-text": func() error { _, err := sortDirection(1); return err }(),
		"no-comparison":      func() error { _, err := combinePredicates(nil); return err }(),
		"unsupported-type":   func() error { _, err := graphQLType(compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}); return err }(),
		"model-identity":     func() error { _, err := publicModelID("zz"); return err }(),
		"field-identity":     func() error { _, err := publicFieldID("zz"); return err }(),
		"relation-identity":  func() error { _, err := publicRelationID("zz"); return err }(),
		"coerce-unsupported": func() error {
			_, err := compiler.coerceTermValue(golem.FrozenAnalyticsTerm{Operator: golem.AggregateMinimum}, compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}, false, "x")
			return err
		}(),
	}
	for name, err := range invariants {
		t.Run(name, func(t *testing.T) {
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || typed.Message() != "internal server error" {
				t.Fatalf("error=%#v want an Invariant", err)
			}
		})
	}
}

func TestCompileDeclinesWhatIsNotAnAnalyticsRoot(t *testing.T) {
	var absent *Compiler
	for name, compiler := range map[string]*Compiler{"absent-compiler": absent, "unknown-root": socialCompiler(t)} {
		t.Run(name, func(t *testing.T) {
			root, matched, err := compiler.Compile(&ast.Field{Name: "users"}, nil, nil)
			if err != nil || matched || root.ResponseName != "" {
				t.Fatalf("matched=%t err=%v root=%#v", matched, err, root)
			}
		})
	}
	if _, matched, err := socialCompiler(t).Compile(nil, nil, nil); err != nil || matched {
		t.Fatalf("nil field matched=%t err=%v", matched, err)
	}
}
