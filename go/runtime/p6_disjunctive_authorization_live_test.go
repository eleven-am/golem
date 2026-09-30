package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestRelationHopDisjunctivePolicyCannotEscapeJoinCorrelation(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-", AlsoCategory: "private-child"})
			if err != nil {
				t.Fatal(err)
			}
			dimension, count := p6metrics.Metrics.CategoryParentName, p6metrics.Metrics.CountAll()
			rows, err := caller.Metrics.RelationGroupBy(context.Background(), p6metrics.Metrics.RelationGroupBy(
				p6metrics.Metrics.RelationGroupDimensions(dimension),
				p6metrics.Metrics.RelationGroupMeasures(count),
				p6metrics.Metrics.RelationGroupOrderBy(dimension.Asc()),
			))
			if err != nil {
				t.Fatalf("relation group: %v: %v", err, errors.Unwrap(err))
			}
			got := map[string]int64{}
			for _, row := range rows {
				name, nameOK := golem.RelationGroupValue(row, dimension).Get()
				contributions, countOK := golem.RelationGroupValue(row, count).Get()
				if !nameOK || !countOK {
					t.Fatalf("relation group row is incomplete: %v/%v", nameOK, countOK)
				}
				got[name] = contributions
			}
			if want := map[string]int64{"private-child": 1, "public-root": 3}; !reflect.DeepEqual(got, want) {
				t.Fatalf("disjunctive hop policy relation counts=%v want=%v", got, want)
			}
			var direct int64
			query := `SELECT COUNT(*) FROM ` + h.table("p6_metrics") + ` AS m JOIN ` + h.table("p6_categories") + ` AS c ON m."category_id"=c."id" JOIN ` + h.table("p6_categories") + ` AS p ON c."parent_id"=p."id" WHERE (c."name" LIKE ? OR c."name" = ?) AND (p."name" LIKE ? OR p."name" = ?)`
			if err := h.database.GetContext(context.Background(), &direct, h.database.Rebind(query), "public-%", "private-child", "public-%", "private-child"); err != nil {
				t.Fatal(err)
			}
			if total := got["private-child"] + got["public-root"]; direct != 4 || total != direct {
				t.Fatalf("runtime total=%d direct authorized oracle=%d", total, direct)
			}

			metrics := p6metrics.Metrics.Scope()
			categories := golem.InnerJoin(metrics, p6metrics.Metrics.Category)
			pairs := metrics.Count()
			scoped, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).Join(categories).Select(pairs))
			if err != nil || len(scoped) != 1 {
				t.Fatalf("scoped join rows=%d err=%v", len(scoped), err)
			}
			if value, ok := golem.ScopedValue(scoped[0], pairs).Get(); !ok || value != 5 {
				t.Fatalf("scoped disjunctive join pairs=%d/%v want 5", value, ok)
			}
		})
	}
}

type scopedOperandCase struct {
	name  string
	where func(golem.Scope[p6metrics.Metric]) golem.ScopedPredicate
	want  []string
}

func TestScopedPredicateOperandsForEveryScalarTypeAcrossProviders(t *testing.T) {
	uuid := func(index int) golem.UUID { return golem.UUID{13: 2, 15: byte(20 - index)} }
	instant := func(index int) time.Time {
		return time.Date(1960+index*12, time.Month(index+1), index+1, index+1, 2, 3, (index+1)*1000, time.UTC)
	}
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			if updated, err := h.app.System().Metrics.UpdateMany(context.Background(), p6metrics.Metrics.Label.Eq("z"), p6metrics.Metrics.UpdateMany(p6metrics.Metrics.OptionalClock.Set(p6MustTime(t, "07:08:09")))); err != nil || updated != 1 {
				t.Fatalf("seed whole-second clock updated=%d err=%v", updated, err)
			}
			day := func(value string) golem.Date { return p6MustDate(t, value) }
			clock := func(value string) golem.Time { return p6MustTime(t, value) }
			amount := func(value string) golem.Decimal { return p6MustDecimal(t, value) }
			cases := []scopedOperandCase{
				{"bool", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Flag.At(s).Eq(true)
				}, []string{"A", "a", "é"}},
				{"int16-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Small.At(s).In(int16(-2), int16(3))
				}, []string{"A", "e\u0301"}},
				{"int32-gt", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Integer.At(s).GT(int32(100))
				}, []string{"e\u0301", "é"}},
				{"int64-lt", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Big.At(s).LT(int64(0))
				}, []string{"a", "z"}},
				{"float32-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Float.At(s).Eq(float32(2.25))
				}, []string{"a"}},
				{"float64-gte", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Double.At(s).GTE(4.125)
				}, []string{"e\u0301", "é"}},
				{"decimal-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Amount.At(s).Eq(amount("0.0001"))
				}, []string{"a"}},
				{"decimal-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Amount.At(s).In(amount("1"), amount("-1"))
				}, []string{"e\u0301", "é"}},
				{"decimal-lt", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Amount.At(s).LT(amount("0"))
				}, []string{"e\u0301", "z"}},
				{"string-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).Eq("é")
				}, []string{"é"}},
				{"string-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).In("A", "z")
				}, []string{"A", "z"}},
				{"string-lt", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).LT("a")
				}, []string{"A", "Z"}},
				{"string-starts", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).StartsWith("e")
				}, []string{"e\u0301"}},
				{"string-contains", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).Contains("\u0301")
				}, []string{"e\u0301"}},
				{"string-ends", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Label.At(s).EndsWith("Z")
				}, []string{"Z"}},
				{"uuid-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Reference.At(s).Eq(uuid(2))
				}, []string{"a"}},
				{"date-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Day.At(s).Eq(day("2024-01-03"))
				}, []string{"a"}},
				{"date-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Day.At(s).In(day("2024-01-01"), day("2024-01-06"))
				}, []string{"A", "e\u0301"}},
				{"date-gt", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Day.At(s).GT(day("2024-01-04"))
				}, []string{"e\u0301", "é"}},
				{"time-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Clock.At(s).Eq(clock("03:02:03.000003"))
				}, []string{"a"}},
				{"time-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Clock.At(s).In(clock("01:02:03.000001"), clock("02:02:03.000002"))
				}, []string{"A", "Z"}},
				{"time-gte", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Clock.At(s).GTE(clock("05:02:03.000005"))
				}, []string{"e\u0301", "é"}},
				{"time-lt-whole-second", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.Clock.At(s).LT(clock("02:02:03"))
				}, []string{"A"}},
				{"time-eq-whole-second", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.OptionalClock.At(s).Eq(clock("07:08:09"))
				}, []string{"z"}},
				{"time-in-whole-second", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.OptionalClock.At(s).In(clock("07:08:09"), clock("23:00:00"))
				}, []string{"z"}},
				{"datetime-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.OccurredAt.At(s).Eq(instant(2))
				}, []string{"a"}},
				{"enum-eq", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.State.At(s).Eq(p6metrics.StatusOmega)
				}, []string{"Z", "e\u0301", "z"}},
				{"enum-not-in", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.State.At(s).NotIn(p6metrics.StatusOmega)
				}, []string{"A", "a", "é"}},
				{"nullable-is-null", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return golem.AndScoped(p6metrics.Metrics.OptionalLabel.At(s).IsNull(), p6metrics.Metrics.OptionalClock.At(s).IsNull())
				}, []string{"A", "Z", "a", "e\u0301", "é"}},
				{"nullable-uuid-not-null", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return p6metrics.Metrics.CategoryID.At(s).IsNotNull()
				}, []string{"A", "Z", "a", "e\u0301", "é"}},
				{"disjunction-of-types", func(s golem.Scope[p6metrics.Metric]) golem.ScopedPredicate {
					return golem.OrScoped(p6metrics.Metrics.Clock.At(s).Eq(clock("01:02:03.000001")), p6metrics.Metrics.Label.At(s).Eq("z"), p6metrics.Metrics.Day.At(s).Eq(day("2024-01-05")))
				}, []string{"A", "z", "é"}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					metrics := p6metrics.Metrics.Scope()
					label := p6metrics.Metrics.Label.At(metrics)
					rows, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).Where(test.where(metrics)).Select(label))
					if err != nil {
						t.Fatalf("scoped %s: %v: %v", test.name, err, errors.Unwrap(err))
					}
					got := make([]string, 0, len(rows))
					for _, row := range rows {
						value, ok := golem.ScopedValue(row, label).Get()
						if !ok {
							t.Fatal("label is null")
						}
						got = append(got, value)
					}
					sort.Strings(got)
					want := append([]string(nil), test.want...)
					sort.Strings(want)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("scoped %s labels=%q want=%q", test.name, got, want)
					}
				})
			}

			t.Run("temporal-projection", func(t *testing.T) {
				metrics := p6metrics.Metrics.Scope()
				label := p6metrics.Metrics.Label.At(metrics)
				dayField, clockField := p6metrics.Metrics.Day.At(metrics), p6metrics.Metrics.Clock.At(metrics)
				rows, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).Where(label.Eq("a")).Select(dayField, clockField))
				if err != nil || len(rows) != 1 {
					t.Fatalf("temporal projection rows=%d err=%v: %v", len(rows), err, errors.Unwrap(err))
				}
				if value, ok := golem.ScopedValue(rows[0], dayField).Get(); !ok || value.String() != "2024-01-03" {
					t.Fatalf("scoped day=%q/%v", value.String(), ok)
				}
				if value, ok := golem.ScopedValue(rows[0], clockField).Get(); !ok || value.String() != "03:02:03.000003" {
					t.Fatalf("scoped clock=%q/%v", value.String(), ok)
				}
			})

			t.Run("group-predicates", func(t *testing.T) {
				metrics := p6metrics.Metrics.Scope()
				label := p6metrics.Metrics.Label.At(metrics)
				labelCount := label.Count()
				rows, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).GroupBy(label).Having(labelCount.GT(int64(0))).Select(label, labelCount))
				if err != nil || len(rows) != 6 {
					t.Fatalf("count-field having rows=%d err=%v: %v", len(rows), err, errors.Unwrap(err))
				}
				earliest := p6metrics.Metrics.Day.At(metrics).Min()
				rows, err = caller.Metrics.Scoped(context.Background(), golem.From(metrics).Having(earliest.Eq(day("2024-01-01"))).Select(earliest))
				if err != nil || len(rows) != 1 {
					t.Fatalf("date min having rows=%d err=%v: %v", len(rows), err, errors.Unwrap(err))
				}
				latest := p6metrics.Metrics.Clock.At(metrics).Max()
				rows, err = caller.Metrics.Scoped(context.Background(), golem.From(metrics).Having(latest.Eq(clock("06:02:03.000006"))).Select(latest))
				if err != nil || len(rows) != 1 {
					t.Fatalf("time max having rows=%d err=%v: %v", len(rows), err, errors.Unwrap(err))
				}
			})
		})
	}
}
