package selectset

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
)

func socialRequest(t *testing.T, model string, source string) (Request, compilerir.CompilationIR) {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document := parseQuery(t, source)
	return Request{Compilation: *compiled.Compilation, Model: modelByName(t, compiled.Compilation.Contract, model), Selections: document.Operations[0].SelectionSet, Fragments: document.Fragments}, *compiled.Compilation
}

func assertTyped(t *testing.T, err error, kind compileerr.Kind, message string) {
	t.Helper()
	var typed *compileerr.Error
	if !errors.As(err, &typed) || typed.Kind() != kind {
		t.Fatalf("error=%v want typed kind %d", err, kind)
	}
	if typed.Message() != message || err.Error() != message && !typed.Internal() {
		t.Fatalf("message=%q want %q", typed.Message(), message)
	}
}

func TestSelectionRefusalsAreTypedAndNameOnlySchemaTerms(t *testing.T) {
	tests := []struct {
		name, model, source string
		kind                compileerr.Kind
		message             string
	}{
		{"scalar-with-selection", "Post", `{ id { x } }`, compileerr.KindSelectionShape, "Field id on Post must not have a selection set"},
		{"relation-without-selection", "Post", `{ author }`, compileerr.KindSelectionShape, "Field author on Post requires a selection set"},
		{"unknown-field", "Post", `{ nope }`, compileerr.KindFieldUnavailable, "Field nope on Post is not available for this operation"},
		{"count-of-scalar", "User", `{ _count { name } }`, compileerr.KindFieldUnavailable, "Field name on User is not available for this operation"},
		{"count-of-to-one", "Post", `{ _count { author } }`, compileerr.KindFieldUnavailable, "Field author on Post is not available for this operation"},
		{"skip-without-if", "Post", `{ id @skip }`, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if"},
		{"include-with-string", "Post", `{ id @include(if: "x") }`, compileerr.KindDirectiveInvalid, "Directive @include requires a Boolean if"},
		{"skip-through-inline-fragment", "Post", `{ ... on Post @skip(if: 1) { id } }`, compileerr.KindDirectiveInvalid, "Directive @skip requires a Boolean if"},
		{"skip-through-fragment-spread", "Post", `{ ...F @include(if: 1) } fragment F on Post { id }`, compileerr.KindDirectiveInvalid, "Directive @include requires a Boolean if"},
		{"fragment-missing", "Post", `{ ...Nope }`, compileerr.KindFragmentInvalid, "Fragment spreads in this operation cannot be resolved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, _ := socialRequest(t, test.model, test.source)
			_, err := Compile(request)
			assertTyped(t, err, test.kind, test.message)
		})
	}
}

func TestSelectionInvariantsAreInternalAndKeepTheirCause(t *testing.T) {
	request, compilation := socialRequest(t, "Post", `{ id author { id } }`)
	invariants := []struct {
		name   string
		mutate func(*Request)
		cause  string
	}{
		{"missing-model-and-selection", func(r *Request) { r.Model, r.Selections = "", nil }, "selection compile requires a model and a selection set"},
		{"absent-model", func(r *Request) { r.Model = compilerir.ModelID("0123456789abcdef0123456789abcdef") }, "is absent"},
		{"unexposed-model", func(r *Request) {
			models := append([]compilerir.ModelContractIR(nil), r.Compilation.Contract.Models...)
			for index := range models {
				if models[index].ModelID == r.Model {
					models[index].Exposed = false
				}
			}
			r.Compilation.Contract.Models = models
		}, "is not exposed"},
		{"absent-relation", func(r *Request) { r.Compilation.Model.Relations = nil }, "is absent"},
	}
	_ = compilation
	for _, test := range invariants {
		t.Run(test.name, func(t *testing.T) {
			copied := request
			test.mutate(&copied)
			_, err := Compile(copied)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || !typed.Internal() || typed.Message() != "internal server error" {
				t.Fatalf("error=%v want an Invariant", err)
			}
			if !strings.Contains(err.Error(), test.cause) {
				t.Fatalf("report text %q lacks %q", err.Error(), test.cause)
			}
		})
	}
}

func TestComputedSelectionRefusalsAndInvariants(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	user := modelByName(t, compilation.Contract, "User")
	contract := contractForModel(t, &compilation, user)
	model := modelForID(t, compilation.Model, user)
	contract.Computed = append(contract.Computed,
		compilerir.ComputedFieldContractIR{ExtensionID: "computed-peer", Name: "peer", Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "User", Nullable: true}, Requires: []compilerir.FieldID{model.Fields[0].ID}},
		compilerir.ComputedFieldContractIR{ExtensionID: "computed-orphan", Name: "orphan", Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}, Requires: []compilerir.FieldID{"ffffffffffffffffffffffffffffffff"}},
	)
	run := func(source string) error {
		document := parseQuery(t, source)
		_, err := Compile(Request{Compilation: compilation, Model: user, Selections: document.Operations[0].SelectionSet})
		return err
	}
	assertTyped(t, run(`{ peer }`), compileerr.KindSelectionShape, "Field peer on User requires a selection set")
	assertTyped(t, run(`{ greeting(prefix: "x") { id } }`), compileerr.KindSelectionShape, "Field greeting on User must not have a selection set")
	assertTyped(t, run(`{ same: greeting(prefix: "x", prefix: "y") same: greeting(prefix: "x") }`), compileerr.KindDuplicate, "Argument prefix is given more than once")
	err := run(`{ orphan }`)
	var typed *compileerr.Error
	if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || !strings.Contains(err.Error(), "references absent field") {
		t.Fatalf("absent dependency error=%v", err)
	}
}

func TestExtensionValueRefusalsAreTypedAndKeepSchemaTermsOnly(t *testing.T) {
	scalar := func(name string) compilerir.GraphQLTypeIR {
		return compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: name, Nullable: true}
	}
	list := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Nullable: true, Element: &compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}}
	tests := []struct {
		name    string
		typ     compilerir.GraphQLTypeIR
		raw     any
		kind    compileerr.Kind
		message string
	}{
		{"non-null-is-null", compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, nil, compileerr.KindNullNotAllowed, "Value cannot be null"},
		{"list-wrong-shape", list, "x", compileerr.KindInvalidValue, "Value has the wrong kind of value"},
		{"list-too-long", list, []any{int32(1), int32(2), int32(3)}, compileerr.KindInputLimit, "Input exceeds MaxListItems (2)"},
		{"list-element-wrong-kind", list, []any{int32(1), "x"}, compileerr.KindInvalidValue, "Input [1] must be of type Int"},
		{"enum-unknown", compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeEnum, Name: "Kind", Nullable: true}, "Nope", compileerr.KindInvalidValue, "Value is not a value of enum Kind"},
		{"boolean-wrong-kind", scalar("Boolean"), "x", compileerr.KindInvalidValue, "Value must be of type Boolean"},
		{"int-out-of-range", scalar("Int"), int64(math.MaxInt32) + 1, compileerr.KindInvalidValue, "Value is outside the Int range"},
		{"int-wrong-kind", scalar("Int"), "x", compileerr.KindInvalidValue, "Value must be of type Int"},
		{"int-json-number-overflow", scalar("Int"), json.Number("99999999999999999999"), compileerr.KindInvalidValue, "Value is outside the Int range"},
		{"float-wrong-kind", scalar("Float"), "x", compileerr.KindInvalidValue, "Value must be of type Float"},
		{"string-wrong-kind", scalar("String"), 5, compileerr.KindInvalidValue, "Value must be of type String"},
		{"bigint-not-canonical", scalar("BigInt"), "01", compileerr.KindInvalidValue, "Value is not a canonical BigInt"},
		{"decimal-malformed", scalar("Decimal"), "abc", compileerr.KindInvalidValue, "Value is not a valid Decimal"},
		{"uuid-malformed", scalar("UUID"), "abc", compileerr.KindInvalidValue, "Value is not a valid UUID"},
		{"date-malformed", scalar("Date"), "abc", compileerr.KindInvalidValue, "Value is not a valid Date"},
		{"time-malformed", scalar("Time"), "abc", compileerr.KindInvalidValue, "Value is not a valid Time"},
		{"datetime-malformed", scalar("DateTime"), "abc", compileerr.KindInvalidValue, "Value is not a valid DateTime"},
		{"bytes-not-canonical", scalar("Bytes"), "abc!", compileerr.KindInvalidValue, "Value is not a canonical Bytes"},
		{"json-unencodable", scalar("JSON"), func() {}, compileerr.KindInvalidValue, "Value is not a valid JSON"},
		{"json-too-deep", scalar("JSON"), deeplyNested(40), compileerr.KindInputLimit, "Value exceeds the JSON depth limit (32)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CoerceExtensionValue(compilerir.CompilationIR{}, test.typ, test.raw, 2)
			assertTyped(t, err, test.kind, test.message)
		})
	}
	for name, typ := range map[string]compilerir.GraphQLTypeIR{
		"unknown-scalar":   scalar("Mystery"),
		"unsupported-kind": {Kind: compilerir.GraphQLTypeModel, Name: "User", Nullable: true},
		"list-no-element":  {Kind: compilerir.GraphQLTypeList, Nullable: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CoerceExtensionValue(compilerir.CompilationIR{}, typ, "x", 2)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant {
				t.Fatalf("error=%v want an Invariant", err)
			}
		})
	}
}

func deeplyNested(depth int) any {
	var value any = "leaf"
	for index := 0; index < depth; index++ {
		value = []any{value}
	}
	return value
}
