package contract

import (
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func stringListType(levels ...uint32) ir.GraphQLTypeIR {
	result := ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}
	for index := len(levels) - 1; index >= 0; index-- {
		element := result
		result = ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: levels[index], Element: &element}
	}
	return result
}

func TestCheckListBoundsEnforcesEveryLevelAndNeverTruncates(t *testing.T) {
	four := []any{"a", "b", "c", "d"}
	cases := []struct {
		name    string
		typ     ir.GraphQLTypeIR
		value   any
		exceeds uint32
	}{
		{"empty list", stringListType(2), []any{}, 0},
		{"below the maximum", stringListType(4), four[:3], 0},
		{"exactly the maximum", stringListType(4), four, 0},
		{"one above the maximum", stringListType(3), four, 3},
		{"nil list", stringListType(1), []any(nil), 0},
		{"untyped nil", stringListType(1), nil, 0},
		{"pointer to an oversized list", stringListType(2), &four, 2},
		{"typed slice above the maximum", stringListType(1), []string{"a", "b"}, 1},
		{"array above the maximum", stringListType(1), [2]string{"a", "b"}, 1},
		{"nested within both maxima", stringListType(2, 4), []any{four, four}, 0},
		{"nested outer level above its maximum", stringListType(2, 4), []any{four, four, four}, 2},
		{"nested inner level above its maximum", stringListType(3, 3), []any{four[:3], four}, 3},
		{"nested inner level of a later row", stringListType(5, 2), []any{[]any{"a"}, []any{"a", "b"}, []any{"a", "b", "c"}}, 2},
		{"nested nil inner list", stringListType(2, 1), []any{nil, []any{"a"}}, 0},
		{"scalar type is never a list", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}, "text", 0},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := CheckListBounds(test.typ, reflect.ValueOf(test.value))
			var exceeded *ListBoundError
			switch {
			case test.exceeds == 0 && err != nil:
				t.Fatalf("unexpected error %v", err)
			case test.exceeds != 0 && (!errors.As(err, &exceeded) || exceeded.Max != test.exceeds):
				t.Fatalf("error = %v, want a ListBoundError with maximum %d", err, test.exceeds)
			}
		})
	}
}

func TestCheckListBoundsFailsExplicitlyOnAContractWithoutAMaximumOrAValueThatIsNotAList(t *testing.T) {
	var exceeded *ListBoundError
	missing := CheckListBounds(stringListType(0), reflect.ValueOf([]any{"a"}))
	if missing == nil || errors.As(missing, &exceeded) {
		t.Fatalf("a list level without a declared maximum must be a contract fault, got %v", missing)
	}
	missingInner := CheckListBounds(stringListType(4, 0), reflect.ValueOf([]any{[]any{"a"}}))
	if missingInner == nil || errors.As(missingInner, &exceeded) {
		t.Fatalf("an inner level without a declared maximum must be a contract fault, got %v", missingInner)
	}
	notList := CheckListBounds(stringListType(2), reflect.ValueOf("text"))
	if notList == nil || errors.As(notList, &exceeded) {
		t.Fatalf("a non-list value for a list type must be a fault, got %v", notList)
	}
}

func TestListItemsErrorIsAPublicLimitErrorOnlyForAnExceededBound(t *testing.T) {
	cause := &ListBoundError{Max: 100}
	err := ListItemsError("customQuery", "custom query searchLibrary", cause)
	var public *golem.Error
	if !errors.As(err, &public) {
		t.Fatalf("error = %T %v", err, err)
	}
	if public.Code != golem.CodeLimitExceeded || public.Reason != golem.ReasonLimitExceeded || public.Limit != golem.LimitListItems || public.LimitMax != 100 || public.Operation != "customQuery" {
		t.Fatalf("public error = %#v", public)
	}
	if public.Message != "custom query searchLibrary returned more than 100 items" {
		t.Fatalf("message = %q", public.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause must stay reachable for trusted logs")
	}
	other := errors.New("list result declares no maximum item count")
	if ListItemsError("customQuery", "custom query searchLibrary", other) != other {
		t.Fatal("a fault that is not an exceeded bound must pass through unchanged")
	}
}
