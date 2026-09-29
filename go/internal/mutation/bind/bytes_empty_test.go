package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestCreateInputBindsEmptyBytesAsNonNilValue(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	payload := golem.GeneratedNullableOpaqueField[bindPost, []byte](fixture.PostBytes)
	frozen := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post, golem.GeneratedCreateFieldValue(fixture.Post, payload, []byte{})))
	public := frozen.Fields()[0]
	field, ok := fixture.Registry.Field(fixture.Post, fixture.PostBytes)
	if !ok {
		t.Fatal("bytes fixture field is absent")
	}
	typ, err := bindType(field.LogicalType(), field.Nullable())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := bindScalarOperation(InputCreate, public, field, typ, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	value, present := operation.Value()
	if !present {
		t.Fatal("empty bytes became an absent or NULL operation")
	}
	got, ok := value.Bytes()
	if !ok || got == nil || len(got) != 0 {
		t.Fatalf("bound empty bytes=%#v ok=%t; want a non-nil empty slice", got, ok)
	}
}
