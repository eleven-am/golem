package publicvalue_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/publicvalue"
)

var (
	testEnum   = ir.EnumID{1}
	testMember = ir.EnumValueID{2}
)

func wireOf(enum ir.EnumID, member ir.EnumValueID) (string, bool) {
	if enum == testEnum && member == testMember {
		return "wire-name", true
	}
	return "", false
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestValueConvertsEveryScalarKindToItsPublicGoType(t *testing.T) {
	decimal := must(golem.NewDecimal(1234, 2))
	date := must(golem.NewDate(2026, time.November, 3))
	clock := must(golem.NewTime(12, 34, 56, 789012))
	uuid := golem.UUID{15: 9}
	instant := time.Unix(1_700_000_000, 123_000).UTC()
	document := must(golem.NewJSONDocument[any]([]byte(`{"a":[1,"x",null,true]}`)))
	cases := []struct {
		name  string
		value ir.Value
		want  any
	}{
		{"bool", ir.BoolValue(true), true},
		{"int16", must(ir.SignedValue(ir.ValueInt16, -7)), int16(-7)},
		{"int32", must(ir.SignedValue(ir.ValueInt32, 70000)), int32(70000)},
		{"int64", must(ir.SignedValue(ir.ValueInt64, 5_000_000_000)), int64(5_000_000_000)},
		{"float32", must(ir.Float32Value(1.5)), float32(1.5)},
		{"float64", must(ir.Float64Value(2.25)), 2.25},
		{"decimal", must(ir.NewDecimalValue(1234, 2)), decimal},
		{"string", must(ir.StringValue("text")), "text"},
		{"bytes", ir.BytesValue([]byte{1, 2, 3}), []byte{1, 2, 3}},
		{"uuid", ir.UUIDValue(uuid.Bytes()), uuid},
		{"date", must(ir.NewDateValue(2026, 11, 3)), date},
		{"time", must(ir.NewTimeValue(clock.Microseconds())), clock},
		{"datetime", must(ir.NewDateTimeValue(1_700_000_000, 123_000)), instant},
		{"enum", must(ir.NewEnumValue(testEnum, testMember)), "wire-name"},
		{"json", jsonValue(t), document},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := publicvalue.Value(test.value, wireOf)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(test.want) || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %T(%#v), want %T(%#v)", got, got, test.want, test.want)
			}
		})
	}
}

func jsonValue(t *testing.T) ir.Value {
	t.Helper()
	number := must(ir.NewJSONNumber(false, []byte("1"), 0))
	items := []ir.JSONValue{must(ir.JSONNumberValueOf(number)), must(ir.JSONStringValue("x")), ir.JSONNullValue(), ir.JSONBoolValue(true)}
	member := must(ir.NewJSONMember("a", must(ir.JSONArrayValue(items))))
	return must(ir.NewJSONValue(must(ir.JSONObjectValue([]ir.JSONMember{member}))))
}

func TestValueRefusesWhatHasNoScalarPublicForm(t *testing.T) {
	list := must(ir.NewListValue([]ir.Value{must(ir.StringValue("a"))}))
	if _, err := publicvalue.Value(list, wireOf); err == nil || !strings.Contains(err.Error(), "no scalar public form") {
		t.Fatalf("list through Value error=%v", err)
	}
	if _, err := publicvalue.Value(ir.Value{}, wireOf); err == nil {
		t.Fatal("zero value was converted")
	}
	enum := must(ir.NewEnumValue(ir.EnumID{9}, testMember))
	if _, err := publicvalue.Value(enum, wireOf); err == nil || !strings.Contains(err.Error(), "wire value") {
		t.Fatalf("enum without wire value error=%v", err)
	}
}

func TestTypedBuildsPlainSlicesOfTheElementPublicType(t *testing.T) {
	element := must(ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0))
	enumElement := must(ir.NewTypeRef(ir.ValueEnum, false, 0, 0, testEnum, nil, 0))
	capability := ir.CapabilityScalarListJSON
	stringList := must(ir.NewTypeRef(ir.ValueScalarList, false, 0, 0, ir.EnumID{}, &element, capability))
	enumList := must(ir.NewTypeRef(ir.ValueScalarList, false, 0, 0, ir.EnumID{}, &enumElement, capability))
	cases := []struct {
		name  string
		value ir.Value
		typ   ir.TypeRef
		want  any
	}{
		{"strings", must(ir.NewListValue([]ir.Value{must(ir.StringValue("a")), must(ir.StringValue("b"))})), stringList, []string{"a", "b"}},
		{"empty strings", must(ir.NewListValue(nil)), stringList, []string{}},
		{"enums as wire strings", must(ir.NewListValue([]ir.Value{must(ir.NewEnumValue(testEnum, testMember))})), enumList, []string{"wire-name"}},
		{"scalar", must(ir.StringValue("x")), element, "x"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := publicvalue.Typed(test.value, test.typ, wireOf)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(test.want) || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %T(%#v), want %T(%#v)", got, got, test.want, test.want)
			}
		})
	}
}

func TestTypedRefusesListsItCannotShape(t *testing.T) {
	element := must(ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0))
	capability := ir.CapabilityScalarListJSON
	list := must(ir.NewListValue([]ir.Value{must(ir.StringValue("a"))}))
	if _, err := publicvalue.Typed(list, element, wireOf); err == nil || !strings.Contains(err.Error(), "no element type") {
		t.Fatalf("list against a scalar type error=%v", err)
	}
	enumList := must(ir.NewListValue([]ir.Value{must(ir.NewEnumValue(ir.EnumID{9}, testMember))}))
	enumElement := must(ir.NewTypeRef(ir.ValueEnum, false, 0, 0, ir.EnumID{9}, nil, 0))
	enumListType := must(ir.NewTypeRef(ir.ValueScalarList, false, 0, 0, ir.EnumID{}, &enumElement, capability))
	if _, err := publicvalue.Typed(enumList, enumListType, wireOf); err == nil || !strings.Contains(err.Error(), "list element 0") {
		t.Fatalf("list element failure error=%v", err)
	}
}

func TestTypedHasAPublicTypeForEveryValidListElementKind(t *testing.T) {
	for kind := ir.ValueKind(0); kind < 64; kind++ {
		for _, enum := range []ir.EnumID{{}, testEnum} {
			element, err := ir.NewTypeRef(kind, false, 18, 2, enum, nil, 0)
			if err != nil {
				element, err = ir.NewTypeRef(kind, false, 6, 0, enum, nil, 0)
			}
			if err != nil {
				element, err = ir.NewTypeRef(kind, false, 0, 0, enum, nil, 0)
			}
			if err != nil {
				continue
			}
			list, err := ir.NewTypeRef(ir.ValueScalarList, false, 0, 0, ir.EnumID{}, &element, ir.CapabilityScalarListJSON)
			if err != nil {
				continue
			}
			empty := must(ir.NewListValue(nil))
			got, err := publicvalue.Typed(empty, list, wireOf)
			if err != nil || reflect.TypeOf(got).Kind() != reflect.Slice {
				t.Fatalf("element kind %d: %T/%v", kind, got, err)
			}
		}
	}
}
