package decode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

func TestExactDecoderPostgreSQLLiveDriverRepresentations(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not configured")
	}
	runExactDecoderPostgreSQLLive(t, dsn, "c")
}

func TestExactDecoderPostgreSQLLiveLinguisticDriverRepresentations(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN is not configured")
	}
	runExactDecoderPostgreSQLLive(t, dsn, "linguistic")
}

func runExactDecoderPostgreSQLLive(t *testing.T, dsn, profile string) {
	t.Helper()
	fixture := newMatrixFixture(t)
	ctx := context.Background()
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_decode_%s_%d", profile, time.Now().UnixNano()))
	schema := fixture.postgres
	schema.Namespace.Name = namespace
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	})
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}

	columns := make([]string, 29)
	placeholders := make([]string, 29)
	for index := range columns {
		columns[index] = fmt.Sprintf(`"f_%02d"`, index)
		placeholders[index] = fmt.Sprintf("$%d", index+1)
	}
	values := []any{
		true, int64(-1234), int64(1234567), int64(9_007_199_254_740_993), float64(1.5), math.Float64frombits(0x3ff0000000000001), "-123.45", "Grüße", []byte{0, 1, 2, 255}, "00112233-4455-6677-8899-aabbccddeeff",
		"2024-02-29", "23:59:59.123456", time.Unix(-1, 123456000).UTC(), "live", `{"n":9007199254740993,"z":null,"a":[true,"x"]}`,
		`[true,false]`, `["α","β"]`, `[-32768,32767]`, `[-2147483648,2147483647]`, `[-9007199254740993,9007199254740993]`, `[-123.45,0.01]`,
		`["00112233-4455-6677-8899-aabbccddeeff"]`, `["2024-02-29"]`, `["23:59:59.123"]`, `["1969-12-31T23:59:59.123Z"]`, `["live","draft"]`, `[1.5,0.25]`, `[1.0000000000000002,-2.5]`, nil,
	}
	qualified := `"` + string(namespace) + `"."decode_matrix"`
	statement := `INSERT INTO ` + qualified + `(` + strings.Join(columns, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
	if _, err := database.ExecContext(ctx, statement, values...); err != nil {
		t.Fatal(err)
	}
	selected := append(append([]string(nil), columns[:20]...), columns[21:]...)
	rows, err := database.QueryxContext(ctx, `SELECT `+strings.Join(selected, ",")+` FROM `+qualified)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no live PostgreSQL row: %v", rows.Err())
	}
	decoder, err := New(fixture.plans[policyir.ProviderPostgreSQL], fixture.registry, policyir.ProviderPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	scan := decoder.NewScan()
	if err := rows.Scan(scan.Destinations()...); err != nil {
		t.Fatal(err)
	}
	cells, err := scan.Decode()
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	if len(cells) != len(selected) {
		t.Fatalf("cells=%d selected=%d", len(cells), len(selected))
	}
	integer, ok := cells[3].PolicyValue()
	gotInteger, integerOK := integer.Signed()
	if !ok || !integerOK || gotInteger != 9_007_199_254_740_993 {
		t.Fatalf("int64=%d policy=%t integer=%t", gotInteger, ok, integerOK)
	}
	decimal, ok := cells[6].PolicyValue()
	coefficient, scale, decimalOK := decimal.Decimal()
	if !ok || !decimalOK || coefficient != -12345 || scale != 2 {
		t.Fatalf("decimal=%d/%d policy=%t decimal=%t", coefficient, scale, ok, decimalOK)
	}
	instant, ok := cells[12].PolicyValue()
	seconds, nanos, instantOK := instant.DateTime()
	if !ok || !instantOK || seconds != -1 || nanos != 123456000 {
		t.Fatalf("datetime=%d/%d policy=%t datetime=%t", seconds, nanos, ok, instantOK)
	}
	if !cells[len(cells)-1].IsNull() {
		t.Fatal("PostgreSQL nullable text was not selected-null")
	}

	invalidLists := []struct {
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
	for _, test := range invalidLists {
		t.Run("reject out-of-process "+test.name, func(t *testing.T) {
			planned := fixture.special[test.field]
			placeholders := make([]string, len(planned.Fields()))
			arguments := make([]any, len(planned.Fields()))
			for index, field := range planned.Fields() {
				placeholders[index] = fmt.Sprintf("$%d::text", index+1)
				arguments[index] = "00112233-4455-6677-8899-aabbccddeeff"
				if field.FieldID() == policyir.FieldID(fixture.fields[test.field]) {
					arguments[index] = test.raw
				}
			}
			invalidRows, queryErr := database.QueryxContext(ctx, `SELECT `+strings.Join(placeholders, ","), arguments...)
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			defer invalidRows.Close()
			if !invalidRows.Next() {
				t.Fatalf("no malformed row: %v", invalidRows.Err())
			}
			decoder, decoderErr := New(planned, fixture.registry, policyir.ProviderPostgreSQL)
			if decoderErr != nil {
				t.Fatal(decoderErr)
			}
			scan := decoder.NewScan()
			if scanErr := invalidRows.Scan(scan.Destinations()...); scanErr != nil {
				t.Fatal(scanErr)
			}
			if _, decodeErr := scan.Decode(); decodeErr == nil {
				t.Fatal("malformed out-of-process list decoded successfully")
			}
		})
	}
}
