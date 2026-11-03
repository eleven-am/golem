package bind

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type refusal struct {
	name      string
	operation readir.Operation
	arguments map[string]any
	message   string
	argument  string
}

func filter(field string, operations map[string]any) map[string]any {
	return map[string]any{"where": map[string]any{field: operations}}
}

func refusalsForEntry(selector string) []refusal {
	return []refusal{
		{"text-too-long", readir.FindMany, filter("text", map[string]any{"equals": strings.Repeat("x", 121)}), "Input where.text.equals on Entry exceeds the maximum length of 120", "where.text.equals"},
		{"text-with-nul", readir.FindMany, filter("text", map[string]any{"equals": "a\x00b"}), "Input where.text.equals on Entry must not contain NUL characters", "where.text.equals"},
		{"text-wrong-kind", readir.FindMany, filter("text", map[string]any{"equals": 5}), "Input where.text.equals on Entry must be of type String", "where.text.equals"},
		{"flag-wrong-kind", readir.FindMany, filter("flag", map[string]any{"equals": "yes"}), "Input where.flag.equals on Entry must be of type Boolean", "where.flag.equals"},
		{"small-out-of-range", readir.FindMany, filter("small", map[string]any{"equals": json.Number("70000")}), "Input where.small.equals on Entry is outside the Int range", "where.small.equals"},
		{"integer-wrong-kind", readir.FindMany, filter("integer", map[string]any{"equals": "x"}), "Input where.integer.equals on Entry must be of type Int", "where.integer.equals"},
		{"big-not-canonical", readir.FindMany, filter("big", map[string]any{"equals": "01"}), "Input where.big.equals on Entry is not a canonical BigInt", "where.big.equals"},
		{"big-out-of-range", readir.FindMany, filter("big", map[string]any{"equals": "9223372036854775808"}), "Input where.big.equals on Entry is outside the BigInt range", "where.big.equals"},
		{"single-not-finite", readir.FindMany, filter("single", map[string]any{"equals": math.Inf(1)}), "Input where.single.equals on Entry must be a finite Float", "where.single.equals"},
		{"single-out-of-range", readir.FindMany, filter("single", map[string]any{"equals": 1e300}), "Input where.single.equals on Entry is outside the Float range", "where.single.equals"},
		{"double-wrong-kind", readir.FindMany, filter("double", map[string]any{"equals": "x"}), "Input where.double.equals on Entry must be of type Float", "where.double.equals"},
		{"amount-malformed", readir.FindMany, filter("amount", map[string]any{"equals": "abc"}), "Input where.amount.equals on Entry is not a valid Decimal", "where.amount.equals"},
		{"amount-not-canonical", readir.FindMany, filter("amount", map[string]any{"equals": "1.50"}), "Input where.amount.equals on Entry is not a canonical Decimal", "where.amount.equals"},
		{"ref-malformed", readir.FindMany, filter("ref", map[string]any{"equals": "private-driver-token"}), "Input where.ref.equals on Entry is not a valid UUID", "where.ref.equals"},
		{"day-malformed", readir.FindMany, filter("day", map[string]any{"equals": "2024-13-45"}), "Input where.day.equals on Entry is not a valid Date", "where.day.equals"},
		{"clock-malformed", readir.FindMany, filter("clock", map[string]any{"equals": "25:00:00"}), "Input where.clock.equals on Entry is not a valid Time", "where.clock.equals"},
		{"instant-malformed", readir.FindMany, filter("instant", map[string]any{"equals": "yesterday"}), "Input where.instant.equals on Entry is not a valid DateTime", "where.instant.equals"},
		{"payload-not-canonical", readir.FindMany, filter("payload", map[string]any{"equals": "not base64!"}), "Input where.payload.equals on Entry is not a canonical Bytes", "where.payload.equals"},
		{"state-unknown", readir.FindMany, filter("state", map[string]any{"equals": "Nope"}), "Input where.state.equals on Entry is not a value of enum Kind", "where.state.equals"},
		{"state-wrong-kind", readir.FindMany, filter("state", map[string]any{"equals": 5}), "Input where.state.equals on Entry must be of type Kind", "where.state.equals"},
		{"list-has-wrong-kind", readir.FindMany, filter("tags", map[string]any{"has": 5}), "Input where.tags.has on Entry must be of type String", "where.tags.has"},
		{"list-element-wrong-kind", readir.FindMany, filter("tags", map[string]any{"hasEvery": []any{"a", 5}}), "Input where.tags.hasEvery[1] on Entry must be of type String", "where.tags.hasEvery[1]"},
		{"list-too-long", readir.FindMany, filter("tags", map[string]any{"hasSome": []any{"a", "b", "c", "d"}}), "Input exceeds MaxListItems (3)", ""},
		{"in-list-too-long", readir.FindMany, filter("text", map[string]any{"in": []any{"a", "b", "c", "d"}}), "Input exceeds MaxListItems (3)", ""},
		{"in-list-null-item", readir.FindMany, filter("text", map[string]any{"in": []any{"a", nil}}), "Input where.text.in[1] on Entry cannot be null", "where.text.in[1]"},
		{"in-not-a-list", readir.FindMany, filter("text", map[string]any{"in": "a"}), "Input where.text.in on Entry has the wrong kind of value", "where.text.in"},
		{"mode-unknown", readir.FindMany, filter("text", map[string]any{"equals": "x", "mode": "weird"}), "Input where.text.mode on Entry is not one of the allowed values", "where.text.mode"},
		{"is-null-wrong-kind", readir.FindMany, filter("text", map[string]any{"isNull": "x"}), "Input where.text.isNull on Entry must be of type Boolean", "where.text.isNull"},
		{"list-is-empty-wrong-kind", readir.FindMany, filter("tags", map[string]any{"isEmpty": "x"}), "Input where.tags.isEmpty on Entry must be of type Boolean", "where.tags.isEmpty"},
		{"operator-unsupported-for-type", readir.FindMany, filter("flag", map[string]any{"contains": true}), "Input where.flag.contains on Entry is not supported for this field or provider", "where.flag.contains"},
		{"operator-unknown", readir.FindMany, filter("text", map[string]any{"wat": "x"}), "Input where.text.wat on Entry is not supported for this field or provider", "where.text.wat"},
		{"list-operator-unknown", readir.FindMany, filter("tags", map[string]any{"wat": "x"}), "Input where.tags.wat on Entry is not supported for this field or provider", "where.tags.wat"},
		{"relation-operator-unknown", readir.FindMany, filter("owner", map[string]any{"wat": map[string]any{"all": true}}), "Input where.owner.wat on Entry is not supported for this field or provider", "where.owner.wat"},
		{"relation-filter-empty", readir.FindMany, filter("owner", map[string]any{}), "Input where.owner on Entry must not be empty", "where.owner"},
		{"relation-filter-wrong-shape", readir.FindMany, map[string]any{"where": map[string]any{"owner": "x"}}, "Input where.owner on Entry has the wrong kind of value", "where.owner"},
		{"unknown-field", readir.FindMany, filter("nope", map[string]any{"equals": 1}), "Input where.nope on Entry is not available for this operation", "where.nope"},
		{"field-null", readir.FindMany, map[string]any{"where": map[string]any{"text": nil}}, "Input where.text on Entry cannot be null", "where.text"},
		{"where-wrong-shape", readir.FindMany, map[string]any{"where": "x"}, "Argument where on Entry has the wrong kind of value", "where"},
		{"unexpected-argument", readir.FindMany, map[string]any{"nope": 1}, "Argument nope on Entry is not available for this operation", "nope"},
		{"count-rejects-take", readir.Count, map[string]any{"take": 1}, "Argument take on Entry is not available for this operation", "take"},
		{"take-null", readir.FindMany, map[string]any{"take": nil}, "Argument take on Entry cannot be null", "take"},
		{"skip-null", readir.FindMany, map[string]any{"skip": nil}, "Argument skip on Entry cannot be null", "skip"},
		{"take-wrong-kind", readir.FindMany, map[string]any{"take": "x"}, "Argument take on Entry must be of type Int", "take"},
		{"skip-wrong-kind", readir.FindMany, map[string]any{"skip": "x"}, "Argument skip on Entry must be of type Int", "skip"},
		{"order-direction", readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"text": "sideways"}}}, "Input orderBy[0].text on Entry is not one of the allowed values", "orderBy[0].text"},
		{"order-direction-wrong-kind", readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"text": 3}}}, "Input orderBy[0].text on Entry is not one of the allowed values", "orderBy[0].text"},
		{"order-unknown-field", readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"nope": "asc"}}}, "Input orderBy[0].nope on Entry is not available for this operation", "orderBy[0].nope"},
		{"order-relation-field", readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"owner": "asc"}}}, "Input orderBy[0].owner on Entry is not available for this operation", "orderBy[0].owner"},
		{"order-entry-wrong-shape", readir.FindMany, map[string]any{"orderBy": []any{"text"}}, "Input orderBy[0] on Entry has the wrong kind of value", "orderBy[0]"},
		{"order-not-a-list", readir.FindMany, map[string]any{"orderBy": "text"}, "Argument orderBy on Entry has the wrong kind of value", "orderBy"},
		{"order-too-long", readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"text": "asc"}, map[string]any{"flag": "asc"}, map[string]any{"small": "asc"}, map[string]any{"integer": "asc"}}}, "Input exceeds MaxListItems (3)", ""},
		{"distinct-unknown", readir.FindMany, map[string]any{"distinct": []any{"nope"}}, "Input distinct[0] on Entry is not available for this operation", "distinct[0]"},
		{"distinct-wrong-kind", readir.FindMany, map[string]any{"distinct": []any{5}}, "Input distinct[0] on Entry is not available for this operation", "distinct[0]"},
		{"distinct-too-long", readir.FindMany, map[string]any{"distinct": []any{"text", "flag", "small", "integer"}}, "Input exceeds MaxListItems (3)", ""},
		{"find-one-requires-where", readir.FindUnique, map[string]any{}, "Argument where on Entry is required", "where"},
		{"find-one-null-where", readir.FindUnique, map[string]any{"where": nil}, "Argument where on Entry cannot be null", "where"},
		{"find-one-two-arms", readir.FindUnique, map[string]any{"where": map[string]any{selector: "00000000-0000-0000-0000-000000000001", "nope": "x"}}, "Argument where on Entry must set exactly one member", "where"},
		{"find-one-null-arm", readir.FindUnique, map[string]any{"where": map[string]any{selector: nil}}, "Input where." + selector + " on Entry cannot be null", "where." + selector},
		{"find-one-unknown-arm", readir.FindUnique, map[string]any{"where": map[string]any{"nope": "x"}}, "Input where.nope on Entry is not available for this operation", "where.nope"},
		{"find-one-id-malformed", readir.FindUnique, map[string]any{"where": map[string]any{selector: "private-driver-token"}}, "Input where." + selector + " on Entry is not a valid UUID", "where." + selector},
		{"cursor-wrong-shape", readir.FindMany, map[string]any{"cursor": "x"}, "Argument cursor on Entry has the wrong kind of value", "cursor"},
		{"negative-take-needs-order", readir.FindMany, map[string]any{"take": -2}, "Argument take on Entry is negative and requires orderBy", "take"},
	}
}

func TestBinderRefusalsAreTypedAndNameOnlySchemaTerms(t *testing.T) {
	binder, model := hookValuesBinder(t)
	binder.limits.MaxListItems = 3
	selection := scalarSelection(t, entryField(t, model, "Text").ID)
	_, entryContract := modelNamed(t, binder.compilation, "Entry")
	for _, test := range refusalsForEntry(entryContract.Selectors[0].Name) {
		t.Run(test.name, func(t *testing.T) {
			input := QueryInput{Operation: test.operation, Model: model.ID, Arguments: test.arguments}
			if test.operation != readir.Count {
				input.Selections = []readir.Selection{selection}
			}
			_, err := binder.Query(input)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Internal() {
				t.Fatalf("error=%v want a non-internal typed refusal", err)
			}
			if typed.Message() != test.message || err.Error() != test.message {
				t.Fatalf("message=%q Error()=%q want %q", typed.Message(), err.Error(), test.message)
			}
			if typed.Argument() != test.argument {
				t.Fatalf("argument=%q want %q", typed.Argument(), test.argument)
			}
			for _, marker := range []string{"private-driver-token", "sideways", "not base64"} {
				if strings.Contains(err.Error(), marker) {
					t.Fatalf("message echoes caller text %q: %s", marker, err.Error())
				}
			}
		})
	}
}

func TestBinderCompoundSelectorRefusalsNameTheComponent(t *testing.T) {
	compilation := socialCompilation(t)
	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	friendship, contract := modelNamed(t, compilation, "Friendship")
	selector := contract.Selectors[0]
	selection := scalarSelection(t, selector.Fields[0])
	first := fieldContract(t, contract, selector.Fields[0]).GraphQLName
	second := fieldContract(t, contract, selector.Fields[1]).GraphQLName
	id := "00000000-0000-0000-0000-000000000001"
	for _, test := range []struct {
		name, message, argument string
		value                   any
	}{
		{"component-missing", "Input where." + selector.Name + "." + second + " on Friendship is required", "where." + selector.Name + "." + second, map[string]any{first: id}},
		{"component-null", "Input where." + selector.Name + "." + second + " on Friendship cannot be null", "where." + selector.Name + "." + second, map[string]any{first: id, second: nil}},
		{"component-unknown", "Input where." + selector.Name + ".nope on Friendship is not available for this operation", "where." + selector.Name + ".nope", map[string]any{first: id, second: id, "nope": id}},
		{"component-malformed", "Input where." + selector.Name + "." + second + " on Friendship is not a valid UUID", "where." + selector.Name + "." + second, map[string]any{first: id, second: "private-driver-token"}},
		{"compound-wrong-shape", "Input where." + selector.Name + " on Friendship has the wrong kind of value", "where." + selector.Name, "x"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := binder.Query(QueryInput{Operation: readir.FindUnique, Model: friendship.ID, Arguments: map[string]any{"where": map[string]any{selector.Name: test.value}}, Selections: []readir.Selection{selection}})
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Internal() || typed.Message() != test.message || typed.Argument() != test.argument {
				t.Fatalf("error=%v argument=%v want %q at %q", err, typed, test.message, test.argument)
			}
		})
	}
}

func TestBinderInvariantsAreInternalAndKeepTheirCause(t *testing.T) {
	binder, model := hookValuesBinder(t)
	selection := scalarSelection(t, entryField(t, model, "Text").ID)
	var invariant *compileerr.Error
	_, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: compilerir.ModelID("00000000000000000000000000000001"), Selections: []readir.Selection{selection}})
	if !errors.As(err, &invariant) || invariant.Kind() != compileerr.KindInvariant || !strings.Contains(err.Error(), "is absent or not exposed") {
		t.Fatalf("absent model error=%v", err)
	}
	_, err = binder.Query(QueryInput{Operation: readir.Operation(99), Model: model.ID, Selections: []readir.Selection{selection}})
	if !errors.As(err, &invariant) || invariant.Kind() != compileerr.KindInvariant || !strings.Contains(err.Error(), "unsupported read operation 99") {
		t.Fatalf("unsupported operation error=%v", err)
	}
	_, err = binder.Query(QueryInput{Operation: readir.FindMany, Model: model.ID})
	if !errors.As(err, &invariant) || invariant.Kind() != compileerr.KindInvariant || !strings.Contains(err.Error(), "empty projection") {
		t.Fatalf("empty projection error=%v", err)
	}
	_, err = binder.FreezePredicate(mustCondition(t, binder, model))
	if err != nil {
		t.Fatal(err)
	}
	var absent *Binder
	if _, err = absent.FreezePredicate(mustCondition(t, binder, model)); !errors.As(err, &invariant) || invariant.Kind() != compileerr.KindInvariant {
		t.Fatalf("absent binder error=%v", err)
	}
	if _, err = fixedID("not-hex"); !errors.As(err, &invariant) || invariant.Kind() != compileerr.KindInvariant {
		t.Fatalf("malformed identity error=%v", err)
	}
	var public *golem.Error
	if errors.As(err, &public) {
		t.Fatal("an invariant looks like a public error")
	}
}

func mustCondition(t *testing.T, binder *Binder, model compilerir.ModelDeclIR) policyir.Condition {
	t.Helper()
	request, err := binder.Query(QueryInput{Operation: readir.FindMany, Model: model.ID, Arguments: map[string]any{"where": map[string]any{"all": true}}, Selections: []readir.Selection{scalarSelection(t, entryField(t, model, "Text").ID)}})
	if err != nil {
		t.Fatal(err)
	}
	where, ok := request.Where()
	if !ok {
		t.Fatal("request lost its predicate")
	}
	return where
}
