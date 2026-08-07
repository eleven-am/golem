package postgresql

import (
	"context"
	"os"
	"testing"
)

func TestP6PostgreSQLExactNumericAndBinaryAnalyticsProfiles(t *testing.T) {
	profiles := []struct {
		name string
		env  string
	}{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dsn := os.Getenv(profile.env)
			if dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			database, report, err := New().Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if !report.AnalyticsExact || !report.BinaryText {
				t.Fatalf("capability report exact=%t binary=%t", report.AnalyticsExact, report.BinaryText)
			}

			var integerSum, decimalSum, decimalAverage, binaryOrder, minimum, maximum string
			err = database.QueryRowx(`SELECT
(SELECT sum(value)::text FROM (VALUES (9223372036854775807::bigint), (9223372036854775807::bigint)) AS integers(value)),
(SELECT sum(value)::text FROM (VALUES (92233720368547758.07::numeric), (92233720368547758.07::numeric)) AS decimals(value)),
(SELECT round(avg(value), 2)::text FROM (VALUES (1.00::numeric), (2.01::numeric)) AS decimals(value)),
(SELECT array_agg(value ORDER BY value COLLATE "C")::text FROM (VALUES ('z'::text), ('A'), ('a'), ('Z')) AS strings(value)),
(SELECT min(value COLLATE "C") FROM (VALUES ('z'::text), ('A'), ('a'), ('Z')) AS strings(value)),
(SELECT max(value COLLATE "C") FROM (VALUES ('z'::text), ('A'), ('a'), ('Z')) AS strings(value))`).Scan(&integerSum, &decimalSum, &decimalAverage, &binaryOrder, &minimum, &maximum)
			if err != nil {
				t.Fatal(err)
			}
			if integerSum != "18446744073709551614" || decimalSum != "184467440737095516.14" || decimalAverage != "1.51" {
				t.Fatalf("integer=%q decimalSum=%q decimalAverage=%q", integerSum, decimalSum, decimalAverage)
			}
			if binaryOrder != "{A,Z,a,z}" || minimum != "A" || maximum != "z" {
				t.Fatalf("binary order=%q min=%q max=%q", binaryOrder, minimum, maximum)
			}
		})
	}
}
