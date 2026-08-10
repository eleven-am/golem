package decode

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type matrixModel struct{}
type matrixStatus string

const matrixStatusLive matrixStatus = "live"

type matrixFixture struct {
	registry   *schema.Registry
	descriptor golem.ModelDescriptor[matrixModel]
	model      golem.ModelID
	fields     map[string]golem.FieldID
	plans      map[policyir.Provider]readplan.Plan
	special    map[string]readplan.Plan
	sqlite     physical.PhysicalSchema
	postgres   physical.PhysicalSchema
}

func TestExactDecoderMatrixSQLiteAndPostgreSQLRepresentations(t *testing.T) {
	fixture := newMatrixFixture(t)
	instant := time.Unix(-1, 123456000).In(time.FixedZone("offset", 2*60*60))
	uuidText := "00112233-4455-6677-8899-aabbccddeeff"

	cases := []struct {
		name     string
		sqlite   any
		postgres any
		kind     policyir.ValueKind
		assert   func(*testing.T, policyir.Value)
	}{
		{name: "Bool", sqlite: int64(1), postgres: true, kind: policyir.ValueBool, assert: func(t *testing.T, value policyir.Value) {
			got, ok := value.Bool()
			if !ok || !got {
				t.Fatalf("bool=%v present=%t", got, ok)
			}
		}},
		{name: "Int16", sqlite: int64(-1234), postgres: int64(-1234), kind: policyir.ValueInt16, assert: signed(-1234)},
		{name: "Int32", sqlite: int64(1234567), postgres: int64(1234567), kind: policyir.ValueInt32, assert: signed(1234567)},
		{name: "Int64", sqlite: int64(9_007_199_254_740_993), postgres: int64(9_007_199_254_740_993), kind: policyir.ValueInt64, assert: signed(9_007_199_254_740_993)},
		{name: "Float32", sqlite: float64(1.5), postgres: float64(1.5), kind: policyir.ValueFloat32, assert: func(t *testing.T, value policyir.Value) {
			bits, ok := value.Float32Bits()
			if !ok || bits != math.Float32bits(1.5) {
				t.Fatalf("float32 bits=%x present=%t", bits, ok)
			}
		}},
		{name: "Float64", sqlite: math.Float64frombits(0x3ff0000000000001), postgres: math.Float64frombits(0x3ff0000000000001), kind: policyir.ValueFloat64, assert: func(t *testing.T, value policyir.Value) {
			bits, ok := value.Float64Bits()
			if !ok || bits != 0x3ff0000000000001 {
				t.Fatalf("float64 bits=%x present=%t", bits, ok)
			}
		}},
		{name: "Decimal", sqlite: int64(-12345), postgres: "-123.45", kind: policyir.ValueDecimal, assert: func(t *testing.T, value policyir.Value) {
			coefficient, scale, ok := value.Decimal()
			if !ok || coefficient != -12345 || scale != 2 {
				t.Fatalf("decimal=%d/%d present=%t", coefficient, scale, ok)
			}
		}},
		{name: "String", sqlite: "Grüße", postgres: "Grüße", kind: policyir.ValueString, assert: func(t *testing.T, value policyir.Value) {
			got, ok := value.Text()
			if !ok || got != "Grüße" {
				t.Fatalf("text=%q present=%t", got, ok)
			}
		}},
		{name: "Bytes", sqlite: []byte{0, 1, 2, 255}, postgres: []byte{0, 1, 2, 255}, kind: policyir.ValueBytes, assert: func(t *testing.T, value policyir.Value) {
			got, ok := value.Bytes()
			if !ok || !reflect.DeepEqual(got, []byte{0, 1, 2, 255}) {
				t.Fatalf("bytes=%v present=%t", got, ok)
			}
		}},
		{name: "UUID", sqlite: uuidText, postgres: uuidText, kind: policyir.ValueUUID, assert: func(t *testing.T, value policyir.Value) {
			got, ok := value.UUID()
			parsed, _ := golem.ParseUUID(uuidText)
			if !ok || got != parsed.Bytes() {
				t.Fatalf("uuid=%x present=%t", got, ok)
			}
		}},
		{name: "Date", sqlite: "2024-02-29", postgres: time.Date(2024, time.February, 29, 0, 0, 0, 0, time.UTC), kind: policyir.ValueDate, assert: func(t *testing.T, value policyir.Value) {
			year, month, day, ok := value.Date()
			if !ok || year != 2024 || month != 2 || day != 29 {
				t.Fatalf("date=%d-%d-%d present=%t", year, month, day, ok)
			}
		}},
		{name: "Time", sqlite: "23:59:59.123456", postgres: "23:59:59.123456", kind: policyir.ValueTime, assert: func(t *testing.T, value policyir.Value) {
			micros, ok := value.Time()
			if !ok || micros != 86_399_123_456 {
				t.Fatalf("time=%d present=%t", micros, ok)
			}
		}},
		{name: "DateTime", sqlite: int64(-876544), postgres: instant, kind: policyir.ValueDateTime, assert: func(t *testing.T, value policyir.Value) {
			seconds, nanos, ok := value.DateTime()
			if !ok || seconds != -1 || nanos != 123456000 {
				t.Fatalf("datetime=%d/%d present=%t", seconds, nanos, ok)
			}
		}},
		{name: "Enum", sqlite: "live", postgres: "live", kind: policyir.ValueEnum, assert: func(t *testing.T, value policyir.Value) {
			_, _, ok := value.Enum()
			if !ok {
				t.Fatal("enum was not preserved")
			}
		}},
		{name: "JSON", sqlite: `{"n":9007199254740993,"z":null,"a":[true,"x"]}`, postgres: `{"n":9007199254740993,"z":null,"a":[true,"x"]}`, kind: policyir.ValueJSON, assert: func(t *testing.T, value policyir.Value) {
			document, ok := value.JSON()
			if !ok {
				t.Fatal("JSON was not preserved")
			}
			object, ok := document.Object()
			if !ok || len(object) != 3 {
				t.Fatalf("JSON object=%v present=%t", object, ok)
			}
		}},
		{name: "ListBool", sqlite: `[true,false]`, postgres: `[true,false]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueBool, 2)},
		{name: "ListString", sqlite: `["α","β"]`, postgres: `["α","β"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueString, 2)},
		{name: "ListInt16", sqlite: `[-32768,32767]`, postgres: `[-32768,32767]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueInt16, 2)},
		{name: "ListInt32", sqlite: `[-2147483648,2147483647]`, postgres: `[-2147483648,2147483647]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueInt32, 2)},
		{name: "ListInt64", sqlite: `[-9007199254740993,9007199254740993]`, postgres: `[-9007199254740993,9007199254740993]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueInt64, 2)},
		{name: "ListUUID", sqlite: `["00112233-4455-6677-8899-aabbccddeeff"]`, postgres: `["00112233-4455-6677-8899-aabbccddeeff"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueUUID, 1)},
		{name: "ListDate", sqlite: `["2024-02-29"]`, postgres: `["2024-02-29"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueDate, 1)},
		{name: "ListTime", sqlite: `["23:59:59.123"]`, postgres: `["23:59:59.123"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueTime, 1)},
		{name: "ListDateTime", sqlite: `["1969-12-31T23:59:59.123Z"]`, postgres: `["1969-12-31T23:59:59.123Z"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueDateTime, 1)},
		{name: "ListEnum", sqlite: `["live","draft"]`, postgres: `["live","draft"]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueEnum, 2)},
		{name: "ListFloat32", sqlite: `[1.5,0.25]`, postgres: `[1.5,0.25]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueFloat32, 2)},
		{name: "ListFloat64", sqlite: `[1.0000000000000002,-2.5]`, postgres: `[1.0000000000000002,-2.5]`, kind: policyir.ValueScalarList, assert: listKinds(policyir.ValueFloat64, 2)},
		{name: "NullableString", sqlite: "present", postgres: "present", kind: policyir.ValueString, assert: func(t *testing.T, value policyir.Value) {
			got, ok := value.Text()
			if !ok || got != "present" {
				t.Fatalf("nullable text=%q present=%t", got, ok)
			}
		}},
	}

	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		provider := provider
		t.Run(providerName(provider), func(t *testing.T) {
			decoder, err := New(fixture.plans[provider], fixture.registry, provider)
			if err != nil {
				t.Fatal(err)
			}
			scan := decoder.NewScan()
			destinations := scan.Destinations()
			if len(destinations) != len(cases) {
				t.Fatalf("destinations=%d cases=%d", len(destinations), len(cases))
			}
			for index, test := range cases {
				raw := test.sqlite
				if provider == policyir.ProviderPostgreSQL {
					raw = test.postgres
				}
				assignScanDestination(t, destinations[index], raw)
			}
			cells, err := scan.Decode()
			if err != nil {
				t.Fatalf("%v: %v", err, errors.Unwrap(err))
			}
			for index, test := range cases {
				value, ok := cells[index].PolicyValue()
				if !ok || value.Kind() != test.kind {
					t.Fatalf("%s kind=%d present=%t want=%d", test.name, value.Kind(), ok, test.kind)
				}
				if err := value.Validate(); err != nil {
					t.Fatalf("%s invalid policy value: %v", test.name, err)
				}
				test.assert(t, value)
			}
			assertPublicRuntimeTypes(t, fixture, cells)
		})
	}
}

func TestExactDecoderNullAndMalformedBoundaries(t *testing.T) {
	fixture := newMatrixFixture(t)
	decoder, err := New(fixture.plans[policyir.ProviderSQLite], fixture.registry, policyir.ProviderSQLite)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("non-null field rejects NULL", func(t *testing.T) {
		scan := decoder.NewScan()
		_, err := scan.Decode()
		var failure *Error
		if !errors.As(err, &failure) {
			t.Fatalf("error=%T %v", err, err)
		}
	})

	t.Run("nullable field preserves selected null", func(t *testing.T) {
		field := matrixPlanField(t, fixture, policyir.ProviderSQLite, "NullableString")
		cell, err := decodeNullableField(field)
		if err != nil {
			t.Fatal(err)
		}
		if !cell.IsNull() || cell.RuntimeCell().FieldID() != fixture.fields["NullableString"] {
			t.Fatalf("cell=%#v", cell)
		}
	})

	invalid := []struct {
		name     string
		field    string
		provider policyir.Provider
		raw      any
	}{
		{name: "boolean-domain", field: "Bool", provider: policyir.ProviderSQLite, raw: int64(2)},
		{name: "int16-overflow", field: "Int16", provider: policyir.ProviderSQLite, raw: int64(math.MaxInt16 + 1)},
		{name: "float32-inexact", field: "Float32", provider: policyir.ProviderSQLite, raw: float64(0.1)},
		{name: "float64-nan", field: "Float64", provider: policyir.ProviderSQLite, raw: math.NaN()},
		{name: "uuid", field: "UUID", provider: policyir.ProviderSQLite, raw: "not-a-uuid"},
		{name: "date", field: "Date", provider: policyir.ProviderSQLite, raw: "2023-02-29"},
		{name: "time", field: "Time", provider: policyir.ProviderSQLite, raw: "24:00:00"},
		{name: "datetime-precision", field: "DateTime", provider: policyir.ProviderPostgreSQL, raw: time.Unix(0, 1)},
		{name: "enum", field: "Enum", provider: policyir.ProviderSQLite, raw: "removed"},
		{name: "json", field: "JSON", provider: policyir.ProviderSQLite, raw: `{"unterminated":`},
		{name: "list-not-array", field: "ListString", provider: policyir.ProviderSQLite, raw: `"x"`},
		{name: "list-element-type", field: "ListInt64", provider: policyir.ProviderSQLite, raw: `["1"]`},
		{name: "list-int16-overflow", field: "ListInt16", provider: policyir.ProviderSQLite, raw: `[32768]`},
		{name: "list-decimal-scale-sqlite", field: "ListDecimal", provider: policyir.ProviderSQLite, raw: `[0.001]`},
		{name: "list-decimal-scale-postgresql", field: "ListDecimal", provider: policyir.ProviderPostgreSQL, raw: `[0.001]`},
		{name: "list-decimal-precision-sqlite", field: "ListDecimal", provider: policyir.ProviderSQLite, raw: `[10000000000000000]`},
		{name: "list-decimal-precision-postgresql", field: "ListDecimal", provider: policyir.ProviderPostgreSQL, raw: `[10000000000000000]`},
		{name: "list-time-precision-sqlite", field: "ListTime", provider: policyir.ProviderSQLite, raw: `["12:00:00.1234"]`},
		{name: "list-time-precision-postgresql", field: "ListTime", provider: policyir.ProviderPostgreSQL, raw: `["12:00:00.1234"]`},
		{name: "list-datetime-precision-sqlite", field: "ListDateTime", provider: policyir.ProviderSQLite, raw: `["2024-01-02T12:00:00.1234Z"]`},
		{name: "list-datetime-precision-postgresql", field: "ListDateTime", provider: policyir.ProviderPostgreSQL, raw: `["2024-01-02T12:00:00.1234Z"]`},
		{name: "list-string-length-sqlite", field: "ListString", provider: policyir.ProviderSQLite, raw: `["abc"]`},
		{name: "list-string-length-postgresql", field: "ListString", provider: policyir.ProviderPostgreSQL, raw: `["abc"]`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			field := matrixPlanField(t, fixture, test.provider, test.field)
			if _, err := decodeValue(fixture.registry, test.provider, policyir.ModelID(fixture.model), field, test.raw); err == nil {
				t.Fatal("malformed value decoded successfully")
			}
		})
	}
}

func TestExactDecoderSQLiteLiveDriverRepresentations(t *testing.T) {
	fixture := newMatrixFixture(t)
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, filepath.Join(t.TempDir(), "decode-matrix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.sqlite); err != nil {
		t.Fatal(err)
	}
	columns := make([]string, 29)
	placeholders := make([]string, 29)
	for index := range columns {
		columns[index] = fmt.Sprintf(`"f_%02d"`, index)
		placeholders[index] = "?"
	}
	values := []any{
		int64(1), int64(-1234), int64(1234567), int64(9_007_199_254_740_993), float64(1.5), math.Float64frombits(0x3ff0000000000001), int64(-12345), "Grüße", []byte{0, 1, 2, 255}, "00112233-4455-6677-8899-aabbccddeeff",
		"2024-02-29", "23:59:59.123456", int64(-876544), "live", `{"n":9007199254740993,"z":null,"a":[true,"x"]}`,
		`[true,false]`, `["α","β"]`, `[-32768,32767]`, `[-2147483648,2147483647]`, `[-9007199254740993,9007199254740993]`, `[-123.45,0.01]`,
		`["00112233-4455-6677-8899-aabbccddeeff"]`, `["2024-02-29"]`, `["23:59:59.123"]`, `["1969-12-31T23:59:59.123Z"]`, `["live","draft"]`, `[1.5,0.25]`, `[1.0000000000000002,-2.5]`, nil,
	}
	statement := `INSERT INTO "decode_matrix"(` + strings.Join(columns, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
	if _, err := database.ExecContext(ctx, statement, values...); err != nil {
		t.Fatal(err)
	}
	selected := append(append([]string(nil), columns[:20]...), columns[21:]...)
	rows, err := database.QueryxContext(ctx, `SELECT `+strings.Join(selected, ",")+` FROM "decode_matrix"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no live row: %v", rows.Err())
	}
	decoder, err := New(fixture.plans[policyir.ProviderSQLite], fixture.registry, policyir.ProviderSQLite)
	if err != nil {
		t.Fatal(err)
	}
	scan := decoder.NewScan()
	if err := rows.Scan(scan.Destinations()...); err != nil {
		t.Fatal(err)
	}
	cells, err := scan.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != len(selected) {
		t.Fatalf("cells=%d selected=%d", len(cells), len(selected))
	}
	integer, ok := cells[3].PolicyValue()
	if got, present := integer.Signed(); !ok || !present || got != 9_007_199_254_740_993 {
		t.Fatalf("live int64=%d policy=%t signed=%t", got, ok, present)
	}
	instant, ok := cells[12].PolicyValue()
	seconds, nanos, present := instant.DateTime()
	if !ok || !present || seconds != -1 || nanos != 123456000 {
		t.Fatalf("live datetime=%d/%d policy=%t datetime=%t", seconds, nanos, ok, present)
	}
	if !cells[len(cells)-1].IsNull() {
		t.Fatal("live nullable text was not decoded as selected null")
	}
}

func TestScalarListLogicalParametersFailClosedThroughSQLiteScan(t *testing.T) {
	fixture := newMatrixFixture(t)
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, filepath.Join(t.TempDir(), "decode-invalid-lists.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tests := []struct {
		name  string
		field string
		raw   string
	}{
		{name: "decimal scale", field: "ListDecimal", raw: `[0.001]`},
		{name: "decimal precision", field: "ListDecimal", raw: `[10000000000000000]`},
		{name: "time precision", field: "ListTime", raw: `["12:00:00.1234"]`},
		{name: "datetime precision", field: "ListDateTime", raw: `["2024-01-02T12:00:00.1234Z"]`},
		{name: "string rune length", field: "ListString", raw: `["abc"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planned := fixture.special[test.field]
			placeholders := make([]string, len(planned.Fields()))
			arguments := make([]any, len(planned.Fields()))
			for index, field := range planned.Fields() {
				placeholders[index] = "?"
				arguments[index] = "00112233-4455-6677-8899-aabbccddeeff"
				if field.FieldID() == policyir.FieldID(fixture.fields[test.field]) {
					arguments[index] = test.raw
				}
			}
			rows, queryErr := database.QueryxContext(ctx, `SELECT `+strings.Join(placeholders, ","), arguments...)
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatalf("no row: %v", rows.Err())
			}
			decoder, decoderErr := New(planned, fixture.registry, policyir.ProviderSQLite)
			if decoderErr != nil {
				t.Fatal(decoderErr)
			}
			scan := decoder.NewScan()
			if scanErr := rows.Scan(scan.Destinations()...); scanErr != nil {
				t.Fatal(scanErr)
			}
			if _, decodeErr := scan.Decode(); decodeErr == nil {
				t.Fatal("malformed out-of-process list decoded successfully")
			}
		})
	}
}

func TestFloatingScalarListsDecodeExactlyAndDetach(t *testing.T) {
	fixture := newMatrixFixture(t)
	tests := []struct {
		name string
		raw  string
		read func(golem.Row[matrixModel]) (any, bool)
		want any
	}{
		{name: "ListFloat32", raw: `[1.5,0.25]`, want: golem.List[float32]{1.5, 0.25}, read: func(row golem.Row[matrixModel]) (any, bool) {
			return golem.Value(row, golem.GeneratedListField[matrixModel, float32](fixture.fields["ListFloat32"])).Get()
		}},
		{name: "ListFloat64", raw: `[1.0000000000000002,-2.5]`, want: golem.List[float64]{math.Float64frombits(0x3ff0000000000001), -2.5}, read: func(row golem.Row[matrixModel]) (any, bool) {
			return golem.Value(row, golem.GeneratedListField[matrixModel, float64](fixture.fields["ListFloat64"])).Get()
		}},
	}
	for _, test := range tests {
		field := matrixPlanField(t, fixture, policyir.ProviderSQLite, test.name)
		cell, err := decodeValue(fixture.registry, policyir.ProviderSQLite, policyir.ModelID(fixture.model), field, test.raw)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		value, ok := cell.PolicyValue()
		if !ok {
			t.Fatalf("%s has no policy value", test.name)
		}
		values, ok := value.List()
		if !ok || len(values) != 2 {
			t.Fatalf("%s policy list=%v present=%t", test.name, values, ok)
		}
		runtimeRow, err := golem.RuntimeModelReadRow(fixture.model, cell.RuntimeCell())
		if err != nil {
			t.Fatal(err)
		}
		row, err := golem.RuntimeTypedReadRow(fixture.descriptor, runtimeRow)
		if err != nil {
			t.Fatal(err)
		}
		first, ok := test.read(row)
		if !ok || !reflect.DeepEqual(first, test.want) {
			t.Fatalf("%s public=%v present=%t want=%v", test.name, first, ok, test.want)
		}
		reflected := reflect.ValueOf(first)
		reflected.Index(0).Set(reflect.Zero(reflected.Index(0).Type()))
		second, ok := test.read(row)
		if !ok || !reflect.DeepEqual(second, test.want) {
			t.Fatalf("%s detached=%v present=%t want=%v", test.name, second, ok, test.want)
		}
	}
}

func TestFractionalDecimalScalarListDecodesExactly(t *testing.T) {
	fixture := newMatrixFixture(t)
	field := matrixPlanField(t, fixture, policyir.ProviderSQLite, "ListDecimal")
	cell, err := decodeValue(fixture.registry, policyir.ProviderSQLite, policyir.ModelID(fixture.model), field, `[-123.45,0.01]`)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := cell.PolicyValue()
	if !ok {
		t.Fatal("decimal list has no policy value")
	}
	values, ok := value.List()
	if !ok || len(values) != 2 {
		t.Fatalf("decimal list=%v present=%t", values, ok)
	}
	firstCoefficient, firstScale, firstOK := values[0].Decimal()
	secondCoefficient, secondScale, secondOK := values[1].Decimal()
	if !firstOK || firstCoefficient != -12345 || firstScale != 2 || !secondOK || secondCoefficient != 1 || secondScale != 2 {
		t.Fatalf("decimal elements=%v", values)
	}
	runtimeRow, err := golem.RuntimeModelReadRow(fixture.model, cell.RuntimeCell())
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeTypedReadRow(fixture.descriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := golem.ParseDecimal("-123.45")
	second, _ := golem.ParseDecimal("0.01")
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, golem.Decimal](fixture.fields["ListDecimal"])), golem.List[golem.Decimal]{first, second})
}

func TestExponentDecimalScalarListPreservesExactValue(t *testing.T) {
	fixture := newMatrixFixture(t)
	field := matrixPlanField(t, fixture, policyir.ProviderPostgreSQL, "ListDecimal")
	cell, err := decodeValue(fixture.registry, policyir.ProviderPostgreSQL, policyir.ModelID(fixture.model), field, `[1.2345e2,-5e-1]`)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := cell.PolicyValue()
	values, listOK := value.List()
	if !ok || !listOK || len(values) != 2 {
		t.Fatalf("decimal list=%v policy=%t list=%t", values, ok, listOK)
	}
	firstCoefficient, firstScale, firstOK := values[0].Decimal()
	secondCoefficient, secondScale, secondOK := values[1].Decimal()
	if !firstOK || firstCoefficient != 12345 || firstScale != 2 || !secondOK || secondCoefficient != -5 || secondScale != 1 {
		t.Fatalf("exponent decimal elements=%v", values)
	}
	runtimeRow, err := golem.RuntimeModelReadRow(fixture.model, cell.RuntimeCell())
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeTypedReadRow(fixture.descriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := golem.ParseDecimal("123.45")
	second, _ := golem.ParseDecimal("-0.5")
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, golem.Decimal](fixture.fields["ListDecimal"])), golem.List[golem.Decimal]{first, second})
}

func TestPublicScalarListAccessorIsExactAndDetached(t *testing.T) {
	fixture := newMatrixFixture(t)
	field := matrixPlanField(t, fixture, policyir.ProviderSQLite, "ListInt64")
	cell, err := decodeValue(fixture.registry, policyir.ProviderSQLite, policyir.ModelID(fixture.model), field, `[-9007199254740993,9007199254740993]`)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRow, err := golem.RuntimeModelReadRow(fixture.model, cell.RuntimeCell())
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeTypedReadRow(fixture.descriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	handle := golem.GeneratedListField[matrixModel, int64](fixture.fields["ListInt64"])
	first, ok := golem.Value(row, handle).Get()
	want := golem.List[int64]{-9_007_199_254_740_993, 9_007_199_254_740_993}
	if !ok || !reflect.DeepEqual(first, want) {
		t.Fatalf("first=%v present=%t want=%v", first, ok, want)
	}
	first[0] = 0
	second, ok := golem.Value(row, handle).Get()
	if !ok || !reflect.DeepEqual(second, want) {
		t.Fatalf("second=%v present=%t want detached %v", second, ok, want)
	}
}

func signed(want int64) func(*testing.T, policyir.Value) {
	return func(t *testing.T, value policyir.Value) {
		got, ok := value.Signed()
		if !ok || got != want {
			t.Fatalf("signed=%d present=%t want=%d", got, ok, want)
		}
	}
}

func listKinds(kind policyir.ValueKind, length int) func(*testing.T, policyir.Value) {
	return func(t *testing.T, value policyir.Value) {
		values, ok := value.List()
		if !ok || len(values) != length {
			t.Fatalf("list length=%d present=%t want=%d", len(values), ok, length)
		}
		for index, element := range values {
			if element.Kind() != kind {
				t.Fatalf("element %d kind=%d want=%d", index, element.Kind(), kind)
			}
		}
	}
}

func assignScanDestination(t *testing.T, destination, raw any) {
	t.Helper()
	switch target := destination.(type) {
	case *sql.NullBool:
		target.Bool, target.Valid = raw.(bool), true
	case *sql.NullInt64:
		target.Int64, target.Valid = raw.(int64), true
	case *sql.NullFloat64:
		target.Float64, target.Valid = raw.(float64), true
	case *sql.NullString:
		target.String, target.Valid = raw.(string), true
	case *[]byte:
		*target = append([]byte(nil), raw.([]byte)...)
	case *sql.NullTime:
		target.Time, target.Valid = raw.(time.Time), true
	default:
		t.Fatalf("unsupported scan destination %T", destination)
	}
}

func assertPublicRuntimeTypes(t *testing.T, fixture matrixFixture, cells []Cell) {
	t.Helper()
	runtimeCells := make([]golem.RuntimeReadCell, len(cells))
	for index, cell := range cells {
		runtimeCells[index] = cell.RuntimeCell()
	}
	runtimeRow, err := golem.RuntimeModelReadRow(fixture.model, runtimeCells...)
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeTypedReadRow(fixture.descriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, bool](fixture.fields["Bool"])), true)
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, int16](fixture.fields["Int16"])), int16(-1234))
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, int32](fixture.fields["Int32"])), int32(1234567))
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, int64](fixture.fields["Int64"])), int64(9_007_199_254_740_993))
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, float32](fixture.fields["Float32"])), float32(1.5))
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, float64](fixture.fields["Float64"])), math.Float64frombits(0x3ff0000000000001))
	decimal, _ := golem.ParseDecimal("-123.45")
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, golem.Decimal](fixture.fields["Decimal"])), decimal)
	assertPresentType(t, golem.Value(row, golem.GeneratedTextField[matrixModel, string](fixture.fields["String"])), "Grüße")
	bytesHandle := golem.GeneratedBytesField[matrixModel](fixture.fields["Bytes"])
	assertPresentType(t, golem.Value(row, bytesHandle), []byte{0, 1, 2, 255})
	mutableBytes, _ := golem.Value(row, bytesHandle).Get()
	mutableBytes[0] = 99
	assertPresentType(t, golem.Value(row, bytesHandle), []byte{0, 1, 2, 255})
	uuid, _ := golem.ParseUUID("00112233-4455-6677-8899-aabbccddeeff")
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, golem.UUID](fixture.fields["UUID"])), uuid)
	date, _ := golem.ParseDate("2024-02-29")
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, golem.Date](fixture.fields["Date"])), date)
	clock, _ := golem.ParseTime("23:59:59.123456")
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, golem.Time](fixture.fields["Time"])), clock)
	assertPresentType(t, golem.Value(row, golem.GeneratedEqualField[matrixModel, matrixStatus](fixture.fields["Enum"])), matrixStatusLive)
	json, ok := golem.Value(row, golem.GeneratedJSONField[matrixModel](fixture.fields["JSON"])).Get()
	if !ok || len(json.Bytes()) == 0 {
		t.Fatal("public JSON document is absent")
	}
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, bool](fixture.fields["ListBool"])), golem.List[bool]{true, false})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, string](fixture.fields["ListString"])), golem.List[string]{"α", "β"})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, int16](fixture.fields["ListInt16"])), golem.List[int16]{math.MinInt16, math.MaxInt16})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, int32](fixture.fields["ListInt32"])), golem.List[int32]{math.MinInt32, math.MaxInt32})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, int64](fixture.fields["ListInt64"])), golem.List[int64]{-9_007_199_254_740_993, 9_007_199_254_740_993})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, golem.UUID](fixture.fields["ListUUID"])), golem.List[golem.UUID]{uuid})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, golem.Date](fixture.fields["ListDate"])), golem.List[golem.Date]{date})
	listClock, _ := golem.ParseTime("23:59:59.123")
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, golem.Time](fixture.fields["ListTime"])), golem.List[golem.Time]{listClock})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, time.Time](fixture.fields["ListDateTime"])), golem.List[time.Time]{time.Unix(-1, 123000000).UTC()})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, matrixStatus](fixture.fields["ListEnum"])), golem.List[matrixStatus]{"live", "draft"})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, float32](fixture.fields["ListFloat32"])), golem.List[float32]{1.5, 0.25})
	assertPresentType(t, golem.Value(row, golem.GeneratedListField[matrixModel, float64](fixture.fields["ListFloat64"])), golem.List[float64]{math.Float64frombits(0x3ff0000000000001), -2.5})
}

func assertPresentType[V any](t *testing.T, value golem.ReadValue[V], want V) {
	t.Helper()
	got, ok := value.Get()
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("value=%#v present=%t want=%#v", got, ok, want)
	}
}

func decodeNullableField(field readplan.Field) (Cell, error) {
	scan := Scan{decoder: Decoder{fields: []readplan.Field{field}}, slots: []slot{{kind: slotText}}}
	cells, err := scan.Decode()
	if err != nil {
		return Cell{}, err
	}
	return cells[0], nil
}

func matrixPlanField(t *testing.T, fixture matrixFixture, provider policyir.Provider, name string) readplan.Field {
	t.Helper()
	want := policyir.FieldID(fixture.fields[name])
	for _, field := range fixture.plans[provider].Fields() {
		if field.FieldID() == want {
			return field
		}
	}
	if special, ok := fixture.special[name]; ok {
		for _, field := range special.Fields() {
			if field.FieldID() == want {
				return field
			}
		}
	}
	t.Fatalf("plan field %s is absent", name)
	return readplan.Field{}
}

func providerName(provider policyir.Provider) string {
	if provider == policyir.ProviderSQLite {
		return "sqlite"
	}
	return "postgresql"
}

func newMatrixFixture(t *testing.T) matrixFixture {
	t.Helper()
	modelID := compilerir.ModelID(matrixID(1))
	keyID := compilerir.KeyID(matrixID(2))
	enumID := compilerir.EnumID(matrixID(3))
	liveID, draftID := compilerir.EnumValueID(matrixID(4)), compilerir.EnumValueID(matrixID(5))
	precision, scale, temporalPrecision, listTemporalPrecision := uint16(18), uint16(2), uint16(6), uint16(3)
	listStringMaxLength := uint32(2)
	listCapability := compilerir.CapabilityID("scalar-list:json-array:v1")

	type fieldSpec struct {
		name     string
		typ      compilerir.LogicalTypeIR
		nullable bool
	}
	list := func(element compilerir.LogicalTypeIR) compilerir.LogicalTypeIR {
		return compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &element, Capability: &listCapability}
	}
	specs := []fieldSpec{
		{name: "Bool", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeBool}},
		{name: "Int16", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}},
		{name: "Int32", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}},
		{name: "Int64", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}},
		{name: "Float32", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat32}},
		{name: "Float64", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}},
		{name: "Decimal", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Precision: &precision, Scale: &scale}},
		{name: "String", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}},
		{name: "Bytes", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeBytes}},
		{name: "UUID", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}},
		{name: "Date", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeDate}},
		{name: "Time", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeTime, Precision: &temporalPrecision}},
		{name: "DateTime", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &temporalPrecision}},
		{name: "Enum", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum, EnumID: &enumID}},
		{name: "JSON", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}},
		{name: "ListBool", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeBool})},
		{name: "ListString", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeString, MaxLength: &listStringMaxLength})},
		{name: "ListInt16", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16})},
		{name: "ListInt32", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32})},
		{name: "ListInt64", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64})},
		{name: "ListDecimal", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Precision: &precision, Scale: &scale})},
		{name: "ListUUID", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID})},
		{name: "ListDate", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeDate})},
		{name: "ListTime", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeTime, Precision: &listTemporalPrecision})},
		{name: "ListDateTime", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &listTemporalPrecision})},
		{name: "ListEnum", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum, EnumID: &enumID})},
		{name: "ListFloat32", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat32})},
		{name: "ListFloat64", typ: list(compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64})},
		{name: "NullableString", typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, nullable: true},
	}
	fields := make(map[string]golem.FieldID, len(specs))
	logicalFields := make([]compilerir.FieldIR, len(specs))
	contractFields := make([]compilerir.FieldContractIR, len(specs))
	primaryField := compilerir.FieldID(matrixID(109))
	for index, spec := range specs {
		id := compilerir.FieldID(matrixID(100 + index))
		fields[spec.name] = golem.FieldID(matrixFixed(t, string(id)))
		logicalFields[index] = compilerir.FieldIR{
			ID: id, GoName: spec.name, LogicalName: spec.name, DeclarationOrder: uint32(index), Kind: compilerir.FieldScalar,
			Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(fmt.Sprintf("f_%02d", index)), Type: spec.typ, Nullable: spec.nullable},
		}
		contractFields[index] = compilerir.FieldContractIR{FieldID: id}
	}
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Schema:        compilerir.SchemaIdentityIR{ID: compilerir.SchemaID(matrixID(9)), StableName: "p3_decode_matrix"},
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Enums:         []compilerir.EnumIR{{ID: enumID, LogicalName: "Status", Values: []compilerir.EnumValueIR{{ID: draftID, GoName: "Draft", WireValue: "draft"}, {ID: liveID, GoName: "Live", WireValue: "live"}}}},
		Models: []compilerir.ModelDeclIR{{
			ID: modelID, LogicalName: "Matrix", Table: compilerir.TableBindingIR{PhysicalName: "decode_matrix"}, Fields: logicalFields,
			PrimaryKey: &compilerir.KeyIR{ID: keyID, Kind: compilerir.KeyPrimary, PhysicalName: "pk_decode_matrix", Fields: []compilerir.FieldID{primaryField}},
		}},
	}
	contract := compilerir.ContractIR{
		FormatVersion: compilerir.ContractFormatVersion,
		Models:        []compilerir.ModelContractIR{{ModelID: modelID, Fields: contractFields}},
		Enums:         []compilerir.EnumContractIR{{EnumID: enumID, GraphQLName: "Status"}},
	}

	sqliteSchema, err := sqliteprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatalf("lower SQLite decode matrix: %v", err)
	}
	postgresSchema, err := postgresprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatalf("lower PostgreSQL decode matrix: %v", err)
	}
	bundle := golem.GeneratedSchemaBundle(
		golem.SchemaDigest{0xd3}, "decode-matrix", "p3",
		matrixModelDocument(t, model), matrixContractDocument(t, contract),
		matrixProviderDocument(t, golem.SQLite, sqliteSchema), matrixProviderDocument(t, golem.PostgreSQL, postgresSchema),
	)
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("decode matrix registry: %v", err)
	}
	publicModel := golem.ModelID(matrixFixed(t, string(modelID)))
	scanFields := make([]golem.FieldID, 0, len(specs))
	selections := make([]golem.Selection[matrixModel], 0, len(specs)-3)
	for _, spec := range specs {
		scanFields = append(scanFields, fields[spec.name])
		if spec.name == "ListDecimal" {
			continue
		}
		selections = append(selections, golem.GeneratedOpaqueField[matrixModel, any](fields[spec.name]))
	}
	descriptor := golem.GeneratedModelDescriptor[matrixModel](publicModel, golem.GeneratedDescriptorShape(scanFields, nil, nil, nil))
	frozen, err := golem.FreezeFindMany(descriptor, golem.Select(selections...))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	plans := make(map[policyir.Provider]readplan.Plan, 2)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		plans[provider], err = readplan.System(request, registry, readplan.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
	}
	special := make(map[string]readplan.Plan, 1)
	for _, name := range []string{"ListDecimal", "ListString", "ListTime", "ListDateTime"} {
		frozen, freezeErr := golem.FreezeFindMany(descriptor, golem.Select[matrixModel](golem.GeneratedOpaqueField[matrixModel, any](fields[name])))
		if freezeErr != nil {
			t.Fatal(freezeErr)
		}
		request, bindErr := readbind.Request(frozen, registry, policyir.PortableProviders())
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		special[name], err = readplan.System(request, registry, readplan.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
	}
	return matrixFixture{registry: registry, descriptor: descriptor, model: publicModel, fields: fields, plans: plans, special: special, sqlite: sqliteSchema, postgres: postgresSchema}
}

func matrixModelDocument(t *testing.T, model compilerir.ModelIR) golem.SchemaDocument {
	t.Helper()
	payload, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	return golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), matrixDigest(t, string(fingerprint)), payload)
}

func matrixContractDocument(t *testing.T, contract compilerir.ContractIR) golem.SchemaDocument {
	t.Helper()
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	return golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), matrixDigest(t, string(fingerprint)), payload)
}

func matrixProviderDocument(t *testing.T, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload))
}

func matrixDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func matrixFixed(t *testing.T, value string) [16]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("fixed ID %q: %v", value, err)
	}
	var result [16]byte
	copy(result[:], decoded)
	return result
}

func matrixID(value int) string { return fmt.Sprintf("%032x", value) }
