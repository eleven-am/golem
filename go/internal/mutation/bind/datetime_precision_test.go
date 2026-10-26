package bind

import (
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestDateTimeWritesTruncateTowardNegativeInfinityToDeclaredPrecision(t *testing.T) {
	instant := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.UTC)
	for _, check := range []struct {
		name      string
		value     time.Time
		precision uint16
		seconds   int64
		nanos     uint32
	}{
		{"precision-0", instant, 0, instant.Unix(), 0},
		{"precision-3", instant, 3, instant.Unix(), 123_000_000},
		{"precision-6", instant, 6, instant.Unix(), 123_456_000},
		{"negative-precision-6", time.Unix(-1, 999_999_999), 6, -1, 999_999_000},
		{"negative-precision-0", time.Unix(-1, 1), 0, -1, 0},
		{"negative-precision-3", time.Unix(-2, 500_999), 3, -2, 0},
		{"year-9999", time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), 6, 253402300799, 999_999_000},
		{"year-0001", time.Date(1, 1, 1, 0, 0, 0, 999, time.UTC), 6, -62135596800, 0},
	} {
		t.Run(check.name, func(t *testing.T) {
			logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &check.precision}
			value := bindPrecisionValue(t, check.value, logical)
			seconds, nanos, ok := value.DateTime()
			if !ok || seconds != check.seconds || nanos != check.nanos {
				t.Fatalf("bound=%d.%09d ok=%t want=%d.%09d", seconds, nanos, ok, check.seconds, check.nanos)
			}
		})
	}
}

func TestTimeWritesTruncateToDeclaredPrecision(t *testing.T) {
	clock, err := golem.NewTime(23, 59, 59, 999_999)
	if err != nil {
		t.Fatal(err)
	}
	for precision, want := range map[uint16]int64{0: 86_399_000_000, 3: 86_399_999_000, 6: 86_399_999_999} {
		precision := precision
		logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeTime, Precision: &precision}
		value := bindPrecisionValue(t, clock, logical)
		if got, ok := value.Time(); !ok || got != want {
			t.Fatalf("precision %d bound=%d want=%d", precision, got, want)
		}
	}
}

func TestDateTimeListElementsTruncateToDeclaredPrecision(t *testing.T) {
	precision := uint16(3)
	element := compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &precision}
	capability := listCapability
	logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &element, Capability: &capability}
	list := golem.List[time.Time]{time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC), time.Unix(-1, 999_999_999).UTC()}
	value := bindPrecisionValue(t, list, logical)
	items, ok := value.List()
	if !ok || len(items) != 2 {
		t.Fatalf("bound list=%#v", value)
	}
	for index, want := range []struct {
		seconds int64
		nanos   uint32
	}{{1792929600, 123_000_000}, {-1, 999_000_000}} {
		seconds, nanos, _ := items[index].DateTime()
		if seconds != want.seconds || nanos != want.nanos {
			t.Fatalf("element %d=%d.%09d want=%d.%09d", index, seconds, nanos, want.seconds, want.nanos)
		}
	}
}

func TestDateTimeSelectorTargetRefusesSubMicrosecondNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	at := time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC)
	frozen, err := golem.RuntimeMutationTargetFromIdentity(fixture.Post, fixture.PostKey, []golem.RuntimeSelectorValue{
		{Field: fixture.PostID, Value: golem.NewUUID([16]byte{4})},
		{Field: fixture.PostAt, Value: at},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Target(frozen, fixture.Post, fixture.Registry)
	var bound *Error
	var value *ValueError
	if !errors.As(err, &bound) || bound.Code != CodeValue || bound.Field != fixture.PostAt || bound.FieldName != "At" || !errors.As(err, &value) || value.Reason != golem.ReasonValuePrecision {
		t.Fatalf("sub-microsecond selector err=%v", err)
	}
	onGrid, err := golem.RuntimeMutationTargetFromIdentity(fixture.Post, fixture.PostKey, []golem.RuntimeSelectorValue{
		{Field: fixture.PostID, Value: golem.NewUUID([16]byte{4})},
		{Field: fixture.PostAt, Value: at.Truncate(time.Microsecond)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Target(onGrid, fixture.Post, fixture.Registry); err != nil {
		t.Fatalf("on-grid selector refused: %v", err)
	}
}

func TestDateTimeListBatchFilterRefusesSubMicrosecondNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	stamps := golem.GeneratedNullableListField[bindPost, time.Time](fixture.PostStamps)
	frozen, err := stamps.Has(time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC)).Freeze(golem.GeneratedModelDescriptor[bindPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = BatchPredicate(frozen, fixture.Post, fixture.Registry)
	var bound *Error
	var value *ValueError
	if !errors.As(err, &bound) || bound.Code != CodeValue || bound.Field != fixture.PostStamps || !errors.As(err, &value) || value.Reason != golem.ReasonValuePrecision {
		t.Fatalf("sub-microsecond list filter err=%v", err)
	}
}

func bindPrecisionValue(t *testing.T, raw any, logical compilerir.LogicalTypeIR) policyir.Value {
	t.Helper()
	typ, err := bindType(logical, false)
	if err != nil {
		t.Fatal(err)
	}
	value, err := bindValue(raw, logical, typ, nil)
	if err != nil {
		t.Fatalf("bind %v: %v", raw, err)
	}
	return value
}
