package golem_test

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestRuntimeMutationRequestInputRetainsOriginalUnkeyedLiteralShape(t *testing.T) {
	input := golem.RuntimeMutationRequestInput{
		golem.RuntimeMutationCreate,
		golem.ModelID{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	}
	if input.Operation != golem.RuntimeMutationCreate {
		t.Fatalf("unkeyed compatibility input = %#v", input)
	}
}

func TestRuntimeVersionedMutationRequestInputCarriesOnlyClosedClaims(t *testing.T) {
	typeOf := reflect.TypeOf(golem.RuntimeVersionedMutationRequestInput{})
	want := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Request", typeOf: reflect.TypeOf(golem.RuntimeMutationRequestInput{})},
		{name: "ExistingVersion", typeOf: reflect.TypeOf((*golem.ExistingVersion)(nil))},
		{name: "ConcurrencyExpectation", typeOf: reflect.TypeOf((*golem.ConcurrencyExpectation)(nil))},
	}
	if typeOf.NumField() != len(want) {
		t.Fatalf("versioned request input fields = %d, want %d", typeOf.NumField(), len(want))
	}
	for index, field := range want {
		actual := typeOf.Field(index)
		if actual.Name != field.name || actual.Type != field.typeOf {
			t.Fatalf("versioned request field %d = %s %v, want %s %v", index, actual.Name, actual.Type, field.name, field.typeOf)
		}
	}
	var freeze func(golem.RuntimeVersionedMutationRequestInput) (golem.RuntimeMutationRequest, error) = golem.RuntimeFreezeVersionedMutationRequest
	_ = freeze
}
