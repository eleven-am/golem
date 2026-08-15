package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteExactAggregateCapabilityProbeAndLoss(t *testing.T) {
	database, report, err := New().Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if !report.AnalyticsExact {
		t.Fatal("SQLite provider did not publish its probed exact analytics capability")
	}

	var integer, sum, average, negativeAverage string
	err = database.QueryRowx(`SELECT
golem_analytics_integer_sum_v1(value),
golem_analytics_decimal_sum_v1(value, 2),
golem_analytics_decimal_avg_v1(value, 2),
golem_analytics_decimal_avg_v1(-value, 2)
FROM (SELECT 9223372036854775807 AS value UNION ALL SELECT 9223372036854775807)`).Scan(&integer, &sum, &average, &negativeAverage)
	if err != nil {
		t.Fatal(err)
	}
	if integer != "18446744073709551614" || sum != "184467440737095516.14" || average != "92233720368547758.07" || negativeAverage != "-92233720368547758.07" {
		t.Fatalf("integer=%q sum=%q average=%q negativeAverage=%q", integer, sum, average, negativeAverage)
	}

	var positiveHalf, negativeHalf string
	if err := database.QueryRowx(`SELECT
golem_analytics_decimal_avg_v1(value, 0),
golem_analytics_decimal_avg_v1(-value, 0)
FROM (SELECT 1 AS value UNION ALL SELECT 2)`).Scan(&positiveHalf, &negativeHalf); err != nil {
		t.Fatal(err)
	}
	if positiveHalf != "2" || negativeHalf != "-2" {
		t.Fatalf("half-away rounding positive=%q negative=%q", positiveHalf, negativeHalf)
	}

	var empty sql.NullString
	if err := database.Get(&empty, `SELECT golem_analytics_integer_sum_v1(value) FROM (SELECT NULL AS value)`); err != nil {
		t.Fatal(err)
	}
	if empty.Valid {
		t.Fatalf("all-null exact sum=%q, want SQL NULL", empty.String)
	}

	var comparison, ordering int
	if err := database.QueryRowx(`SELECT golem_analytics_numeric_compare_v1('10', '2'), ('10' COLLATE golem_analytics_numeric_v1) > '2'`).Scan(&comparison, &ordering); err != nil {
		t.Fatal(err)
	}
	if comparison != 1 || ordering != 1 {
		t.Fatalf("comparison=%d ordering=%d", comparison, ordering)
	}

	// The runtime capability is earned by the complete probe result. Losing any
	// exact arithmetic or numeric-ordering component must withdraw it; accepting
	// a partial probe would let a pooled connection silently use lossy SQL.
	losses := []struct {
		name                         string
		integer, decimalSum, average string
		comparison, ordering         int
	}{
		{name: "integer-sum", integer: "18446744073709551613", decimalSum: sum, average: average, comparison: 1, ordering: 1},
		{name: "decimal-sum", integer: integer, decimalSum: "184467440737095516.13", average: average, comparison: 1, ordering: 1},
		{name: "decimal-average", integer: integer, decimalSum: sum, average: "92233720368547758.06", comparison: 1, ordering: 1},
		{name: "numeric-comparison", integer: integer, decimalSum: sum, average: average, comparison: 0, ordering: 1},
		{name: "numeric-collation", integer: integer, decimalSum: sum, average: average, comparison: 1, ordering: 0},
	}
	for _, loss := range losses {
		t.Run("capability-loss/"+loss.name, func(t *testing.T) {
			if validAnalyticsProbe(loss.integer, loss.decimalSum, loss.average, loss.comparison, loss.ordering) {
				t.Fatal("incomplete exact-analytics probe retained the capability")
			}
		})
	}
}
