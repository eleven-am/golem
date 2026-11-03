package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestScalarListBindsFromPlainSlicesAndGolemLists(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	field, ok := fixture.Registry.Field(fixture.Post, fixture.PostList)
	if !ok {
		t.Fatal("list fixture field is absent")
	}
	typ, err := bindType(field.LogicalType(), field.Nullable())
	if err != nil {
		t.Fatal(err)
	}
	bind := func(raw any) (int, error) {
		input, err := golem.RuntimeMutationInputFromValues(golem.RuntimeMutationCreateInput, fixture.Post,
			[]golem.RuntimeMutationFieldValue{{Field: fixture.PostList, Operation: golem.MutationFieldCreate, Value: raw, HasValue: true}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		operation, err := bindScalarOperation(InputCreate, input.Fields()[0], field, typ, fixture.Registry)
		if err != nil {
			return 0, err
		}
		value, present := operation.Value()
		items, isList := value.List()
		if !present || !isList {
			t.Fatalf("bound value present=%v list=%v", present, isList)
		}
		return len(items), nil
	}
	for name, test := range map[string]struct {
		raw  any
		want int
	}{
		"plain slice": {[]string{"a", "b"}, 2},
		"golem list":  {golem.List[string]{"a", "b"}, 2},
		"empty slice": {[]string{}, 0},
		"nil golem":   {golem.List[string](nil), 0},
	} {
		got, err := bind(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("%s: items=%d err=%v, want %d", name, got, err, test.want)
		}
	}
	if _, err := bind([]int64{1}); err == nil {
		t.Fatal("list of the wrong element type was accepted")
	}
	if _, err := bind("not a list"); err == nil {
		t.Fatal("scalar was accepted as a list")
	}
}
