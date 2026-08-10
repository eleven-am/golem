package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	providerapi "github.com/eleven-am/golem/go/provider"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

const (
	p6MetricsPostgreSQLNamespace       physical.PhysicalName = "p6_metrics_app"
	p6MetricsPostgreSQLSystemNamespace physical.PhysicalName = "p6_metrics_system"
)

type p6MetricsHarness struct {
	profile  p5ExtensionProviderProfile
	database *sqlx.DB
	handle   *providerapi.Database
	trace    *p5ExtensionSQLTrace
	app      *p6metrics.App[p6metrics.Principal]
}

func (h *p6MetricsHarness) table(name string) string {
	if h.profile.provider == golem.PostgreSQL {
		return `"` + string(p6MetricsPostgreSQLNamespace) + `"."` + name + `"`
	}
	return `"` + name + `"`
}

func newP6MetricsHarness(t *testing.T, profile p5ExtensionProviderProfile, limits golemruntime.AnalyticsLimits) *p6MetricsHarness {
	t.Helper()
	ctx := context.Background()
	trace := &p5ExtensionSQLTrace{}
	var database *sqlx.DB
	var apply func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "p6-metrics.sqlite")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		_ = bootstrap.Close()
		connector := p5ExtensionDriverConnector{driver: registeredDriver, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"}
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: connector, trace: trace}), "sqlite")
		apply = sqliteprovider.New().ApplyInitial
	} else {
		p5SocialPostgreSQLLock.Lock()
		t.Cleanup(p5SocialPostgreSQLLock.Unlock)
		configuration, err := pgx.ParseConfig(profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		if configuration.RuntimeParams == nil {
			configuration.RuntimeParams = map[string]string{}
		}
		configuration.RuntimeParams["timezone"] = "UTC"
		configuration.RuntimeParams["datestyle"] = "ISO, YMD"
		configuration.RuntimeParams["intervalstyle"] = "iso_8601"
		configuration.RuntimeParams["standard_conforming_strings"] = "on"
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: stdlib.GetConnector(*configuration), trace: trace}), "pgx")
		p6AcquirePostgreSQLTestLock(t, profile.dsn, 0x50364d4554524943)
		p6CleanupMetricsPostgreSQL(t, database)
		apply = postgresprovider.New().ApplyInitial
		t.Cleanup(func() {
			cleanup, _, err := postgresprovider.New().Open(context.Background(), profile.dsn)
			if err != nil {
				t.Errorf("open metrics cleanup: %v", err)
				return
			}
			defer cleanup.Close()
			p6CleanupMetricsPostgreSQL(t, cleanup)
		})
	}
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	t.Cleanup(func() { _ = database.Close() })

	var encoded []byte
	for _, document := range p6metrics.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if profile.provider == golem.PostgreSQL && (schema.Namespace.Name != p6MetricsPostgreSQLNamespace || schema.System.Namespace.Name != p6MetricsPostgreSQLSystemNamespace) {
		t.Fatalf("metrics namespaces=%q/%q", schema.Namespace.Name, schema.System.Namespace.Name)
	}
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	databaseHandle := p8AdoptTracedProviderHandle(database, profile)
	app, err := p6metrics.Open(ctx, p6metrics.Config[p6metrics.Principal]{Database: databaseHandle, AnalyticsLimits: limits,
		AuditPrincipal:    func(p6metrics.Principal) string { return "p6-metrics" },
		ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal p6metrics.Principal) (p6metrics.Actor, error) {
			return p6metrics.Actor{CategoryPrefix: principal.CategoryPrefix}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &p6MetricsHarness{profile: profile, database: database, handle: databaseHandle, trace: trace, app: app}
	harness.seed(t)
	trace.reset()
	return harness
}

func p6CleanupMetricsPostgreSQL(t *testing.T, database *sqlx.DB) {
	t.Helper()
	for _, namespace := range []physical.PhysicalName{p6MetricsPostgreSQLNamespace, p6MetricsPostgreSQLSystemNamespace} {
		if _, err := database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
			t.Errorf("drop metrics schema %s: %v", namespace, err)
		}
	}
}

func (h *p6MetricsHarness) seed(t *testing.T) {
	t.Helper()
	categoryIDs := []golem.UUID{{14: 2, 15: 1}, {14: 2, 15: 2}, {14: 2, 15: 3}, {14: 2, 15: 4}, {14: 2, 15: 5}}
	categories := []struct {
		name   string
		parent *golem.UUID
	}{
		{"public-root", nil},
		{"public-child", &categoryIDs[0]},
		{"private-child", &categoryIDs[0]},
		{"public-orphan", nil},
		{"public-hidden-parent-child", &categoryIDs[2]},
	}
	for index, category := range categories {
		parent := p6metrics.Categories.ParentID.CreateNull()
		if category.parent != nil {
			parent = p6metrics.Categories.ParentID.Create(*category.parent)
		}
		if _, err := h.app.System().Categories.Create(context.Background(), p6metrics.Categories.Create(
			p6metrics.Categories.ID.Create(categoryIDs[index]),
			p6metrics.Categories.Name.Create(category.name),
			parent,
		)); err != nil {
			t.Fatalf("seed category %d: %v: %v", index, err, errors.Unwrap(err))
		}
	}
	labels := []string{"A", "Z", "a", "z", "é", "e\u0301"}
	big := []int64{math.MaxInt64, math.MaxInt64, -5, -7, 10, 0}
	amounts := []string{"99999999999999.9999", "99999999999999.9999", "0.0001", "-0.0001", "1", "-1"}
	for index := range labels {
		id := golem.UUID{14: 1, 15: byte(index + 1)}
		reference := golem.UUID{13: 2, 15: byte(20 - index)}
		day := p6MustDate(t, fmt.Sprintf("2024-01-%02d", index+1))
		clock := p6MustTime(t, fmt.Sprintf("0%d:02:03.%06d", index+1, index+1))
		instant := time.Date(1960+index*12, time.Month(index+1), index+1, index+1, 2, 3, (index+1)*1000, time.UTC)
		state := p6metrics.StatusAlpha
		if index%2 != 0 {
			state = p6metrics.StatusOmega
		}
		category := p6metrics.Metrics.CategoryID.CreateNull()
		switch index {
		case 0, 1:
			category = p6metrics.Metrics.CategoryID.Create(categoryIDs[1])
		case 2:
			category = p6metrics.Metrics.CategoryID.Create(categoryIDs[2])
		case 4:
			category = p6metrics.Metrics.CategoryID.Create(categoryIDs[3])
		case 5:
			category = p6metrics.Metrics.CategoryID.Create(categoryIDs[4])
		}
		_, err := h.app.System().Metrics.Create(context.Background(), p6metrics.Metrics.Create(
			p6metrics.Metrics.ID.Create(id),
			p6metrics.Metrics.Flag.Create(index%2 == 0),
			p6metrics.Metrics.Small.Create(int16(index-2)),
			p6metrics.Metrics.Integer.Create(int32(index*100-200)),
			p6metrics.Metrics.Big.Create(big[index]),
			p6metrics.Metrics.Float.Create(float32(index)+0.25),
			p6metrics.Metrics.Double.Create(float64(index)+0.125),
			p6metrics.Metrics.Amount.Create(p6MustDecimal(t, amounts[index])),
			p6metrics.Metrics.Label.Create(labels[index]),
			p6metrics.Metrics.Reference.Create(reference),
			p6metrics.Metrics.Day.Create(day),
			p6metrics.Metrics.Clock.Create(clock),
			p6metrics.Metrics.OccurredAt.Create(instant),
			p6metrics.Metrics.State.Create(state),
			p6metrics.Metrics.OptionalBig.CreateNull(),
			p6metrics.Metrics.OptionalAmount.CreateNull(),
			p6metrics.Metrics.OptionalLabel.CreateNull(),
			p6metrics.Metrics.OptionalDay.CreateNull(),
			p6metrics.Metrics.OptionalClock.CreateNull(),
			p6metrics.Metrics.OptionalInstant.CreateNull(),
			category,
		))
		if err != nil {
			t.Fatalf("seed metric %d: %v: %v", index, err, errors.Unwrap(err))
		}
	}
}

func p6MustDecimal(t *testing.T, value string) golem.Decimal {
	t.Helper()
	result, err := golem.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func p6MustDate(t *testing.T, value string) golem.Date {
	t.Helper()
	result, err := golem.ParseDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func p6MustTime(t *testing.T, value string) golem.Time {
	t.Helper()
	result, err := golem.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestP6GeneratedMetricExactNullAndScalarMatrixAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatalf("aggregate: %v: %v", err, errors.Unwrap(err))
			}

			count := p6metrics.Metrics.CountAll()
			flagCount := p6metrics.Metrics.Flag.Count()
			smallCount := p6metrics.Metrics.Small.Count()
			integerCount := p6metrics.Metrics.Integer.Count()
			bigCount := p6metrics.Metrics.Big.Count()
			floatCount := p6metrics.Metrics.Float.Count()
			doubleCount := p6metrics.Metrics.Double.Count()
			amountCount := p6metrics.Metrics.Amount.Count()
			labelCount := p6metrics.Metrics.Label.Count()
			referenceCount := p6metrics.Metrics.Reference.Count()
			dayCount := p6metrics.Metrics.Day.Count()
			clockCount := p6metrics.Metrics.Clock.Count()
			instantCount := p6metrics.Metrics.OccurredAt.Count()
			stateCount := p6metrics.Metrics.State.Count()
			optionalBigCount := p6metrics.Metrics.OptionalBig.Count()
			optionalAmountCount := p6metrics.Metrics.OptionalAmount.Count()
			optionalLabelCount := p6metrics.Metrics.OptionalLabel.Count()
			optionalDayCount := p6metrics.Metrics.OptionalDay.Count()
			optionalClockCount := p6metrics.Metrics.OptionalClock.Count()
			optionalInstantCount := p6metrics.Metrics.OptionalInstant.Count()
			categoryIDCount := p6metrics.Metrics.CategoryID.Count()
			smallSum, smallAvg, smallMin, smallMax := p6metrics.Metrics.Small.Sum(), p6metrics.Metrics.Small.Avg(), p6metrics.Metrics.Small.Min(), p6metrics.Metrics.Small.Max()
			integerSum, integerAvg, integerMin, integerMax := p6metrics.Metrics.Integer.Sum(), p6metrics.Metrics.Integer.Avg(), p6metrics.Metrics.Integer.Min(), p6metrics.Metrics.Integer.Max()
			bigSum, bigAvg, bigMin, bigMax := p6metrics.Metrics.Big.Sum(), p6metrics.Metrics.Big.Avg(), p6metrics.Metrics.Big.Min(), p6metrics.Metrics.Big.Max()
			floatSum, floatAvg, floatMin, floatMax := p6metrics.Metrics.Float.Sum(), p6metrics.Metrics.Float.Avg(), p6metrics.Metrics.Float.Min(), p6metrics.Metrics.Float.Max()
			doubleSum, doubleAvg, doubleMin, doubleMax := p6metrics.Metrics.Double.Sum(), p6metrics.Metrics.Double.Avg(), p6metrics.Metrics.Double.Min(), p6metrics.Metrics.Double.Max()
			amountSum, amountAvg, amountMin, amountMax := p6metrics.Metrics.Amount.Sum(), p6metrics.Metrics.Amount.Avg(), p6metrics.Metrics.Amount.Min(), p6metrics.Metrics.Amount.Max()
			optionalBigSum, optionalAmountAvg := p6metrics.Metrics.OptionalBig.Sum(), p6metrics.Metrics.OptionalAmount.Avg()
			labelMin, labelMax := p6metrics.Metrics.Label.Min(), p6metrics.Metrics.Label.Max()
			dayMin, dayMax := p6metrics.Metrics.Day.Min(), p6metrics.Metrics.Day.Max()
			clockMin, clockMax := p6metrics.Metrics.Clock.Min(), p6metrics.Metrics.Clock.Max()
			instantMin, instantMax := p6metrics.Metrics.OccurredAt.Min(), p6metrics.Metrics.OccurredAt.Max()
			h.trace.reset()
			result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(
				p6metrics.Metrics.AggregateSelect(
					count,
					flagCount, smallCount, integerCount, bigCount, floatCount, doubleCount,
					amountCount, labelCount, referenceCount, dayCount, clockCount, instantCount, stateCount,
					optionalBigCount, optionalAmountCount, optionalLabelCount,
					optionalDayCount, optionalClockCount, optionalInstantCount, categoryIDCount,
					smallSum, smallAvg, smallMin, smallMax,
					integerSum, integerAvg, integerMin, integerMax,
					bigSum, bigAvg, bigMin, bigMax,
					floatSum, floatAvg, floatMin, floatMax,
					doubleSum, doubleAvg, doubleMin, doubleMax,
					amountSum, amountAvg, amountMin, amountMax,
					optionalBigSum, optionalAmountAvg,
					labelMin, labelMax, dayMin, dayMax, clockMin, clockMax, instantMin, instantMax,
				),
			))
			if err != nil {
				t.Fatalf("aggregate result: %v: %v", err, errors.Unwrap(err))
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("aggregate statements=%d", len(statements))
			}
			if value, ok := golem.AggregateValue(result, count).Get(); !ok || value != 6 {
				t.Fatalf("count=%d/%t", value, ok)
			}
			assertCount := func(name string, measure golem.Measure[p6metrics.Metric, int64], want int64) {
				t.Helper()
				if value, ok := golem.AggregateValue(result, measure).Get(); !ok || value != want {
					t.Fatalf("%s count=%d/%t; want %d", name, value, ok, want)
				}
			}
			for name, measure := range map[string]golem.Measure[p6metrics.Metric, int64]{
				"bool": flagCount, "int16": smallCount, "int32": integerCount, "int64": bigCount,
				"float32": floatCount, "float64": doubleCount, "decimal": amountCount,
				"string": labelCount, "uuid": referenceCount, "date": dayCount, "time": clockCount,
				"datetime": instantCount, "enum": stateCount,
			} {
				assertCount(name, measure, 6)
			}
			for name, measure := range map[string]golem.Measure[p6metrics.Metric, int64]{
				"nullable-int64": optionalBigCount, "nullable-decimal": optionalAmountCount,
				"nullable-string": optionalLabelCount, "nullable-date": optionalDayCount,
				"nullable-time": optionalClockCount, "nullable-datetime": optionalInstantCount,
			} {
				assertCount(name, measure, 0)
			}
			assertCount("nullable-relation-uuid", categoryIDCount, 5)
			if value, ok := golem.AggregateValue(result, smallSum).Get(); !ok || value.String() != "3" {
				t.Fatalf("int16 sum=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, smallAvg).Get(); !ok || value != 0.5 {
				t.Fatalf("int16 avg=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, smallMin).Get(); !ok || value != -2 {
				t.Fatalf("int16 min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, smallMax).Get(); !ok || value != 3 {
				t.Fatalf("int16 max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, integerSum).Get(); !ok || value.String() != "300" {
				t.Fatalf("int32 sum=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, integerAvg).Get(); !ok || value != 50 {
				t.Fatalf("int32 avg=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, integerMin).Get(); !ok || value != -200 {
				t.Fatalf("int32 min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, integerMax).Get(); !ok || value != 300 {
				t.Fatalf("int32 max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, bigSum).Get(); !ok || value.String() != "18446744073709551612" {
				t.Fatalf("exact integer sum=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, bigAvg).Get(); !ok || math.Abs(value-3.0744573456182584e18) > 1024 {
				t.Fatalf("int64 avg=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, bigMin).Get(); !ok || value != -7 {
				t.Fatalf("int64 min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, bigMax).Get(); !ok || value != math.MaxInt64 {
				t.Fatalf("int64 max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, floatSum).Get(); !ok || value != 16.5 {
				t.Fatalf("float32 sum=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, floatAvg).Get(); !ok || value != 2.75 {
				t.Fatalf("float32 avg=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, floatMin).Get(); !ok || value != 0.25 {
				t.Fatalf("float32 min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, floatMax).Get(); !ok || value != 5.25 {
				t.Fatalf("float32 max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, doubleSum).Get(); !ok || value != 15.75 {
				t.Fatalf("float64 sum=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, doubleAvg).Get(); !ok || value != 2.625 {
				t.Fatalf("float64 avg=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, doubleMin).Get(); !ok || value != 0.125 {
				t.Fatalf("float64 min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, doubleMax).Get(); !ok || value != 5.125 {
				t.Fatalf("float64 max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, amountSum).Get(); !ok || value.String() != "199999999999999.9998" {
				t.Fatalf("exact decimal sum=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, amountAvg).Get(); !ok || value.String() != "33333333333333.3333" {
				t.Fatalf("exact decimal average=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, amountMin).Get(); !ok || value.String() != "-1" {
				t.Fatalf("decimal min=%q/%t", value.String(), ok)
			}
			if value, ok := golem.AggregateValue(result, amountMax).Get(); !ok || value.String() != "99999999999999.9999" {
				t.Fatalf("decimal max=%q/%t", value.String(), ok)
			}
			if _, ok := golem.AggregateValue(result, optionalBigSum).Get(); ok {
				t.Fatal("all-NULL integer sum was present")
			}
			if _, ok := golem.AggregateValue(result, optionalAmountAvg).Get(); ok {
				t.Fatal("all-NULL decimal average was present")
			}

			// Move the same six-row fixture into a controlled mixed-null state only
			// after the all-NULL aggregate cells above have been proved absent. The
			// nullable UUID begins mixed in the shared seed, so normalize it before
			// populating exactly one row across every supported nullable type.
			if updated, updateErr := h.app.System().Metrics.UpdateMany(
				context.Background(),
				p6metrics.Metrics.CategoryID.IsNotNull(),
				p6metrics.Metrics.UpdateMany(p6metrics.Metrics.CategoryID.Null()),
			); updateErr != nil || updated != 5 {
				t.Fatalf("normalize nullable UUID rows=%d error=%v", updated, updateErr)
			}
			mixedID := golem.UUID{14: 1, 15: 1}
			mixedCategoryID := golem.UUID{14: 2, 15: 2}
			if _, updateErr := h.app.System().Metrics.Update(
				context.Background(),
				p6metrics.Metrics.ByID.Value(mixedID),
				p6metrics.Metrics.Update(
					p6metrics.Metrics.OptionalBig.Set(42),
					p6metrics.Metrics.OptionalAmount.Set(p6MustDecimal(t, "12.3400")),
					p6metrics.Metrics.OptionalLabel.Set("present"),
					p6metrics.Metrics.OptionalDay.Set(p6MustDate(t, "2025-02-03")),
					p6metrics.Metrics.OptionalClock.Set(p6MustTime(t, "04:05:06.000007")),
					p6metrics.Metrics.OptionalInstant.Set(time.Date(2025, time.February, 3, 4, 5, 6, 7000, time.UTC)),
					p6metrics.Metrics.CategoryID.Set(mixedCategoryID),
				),
			); updateErr != nil {
				t.Fatalf("populate nullable scalar row: %v: %v", updateErr, errors.Unwrap(updateErr))
			}

			h.trace.reset()
			mixedResult, aggregateErr := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(
				p6metrics.Metrics.AggregateSelect(
					count,
					optionalBigCount,
					optionalAmountCount,
					optionalLabelCount,
					optionalDayCount,
					optionalClockCount,
					optionalInstantCount,
					categoryIDCount,
				),
			))
			if aggregateErr != nil {
				t.Fatalf("mixed nullable aggregate: %v: %v", aggregateErr, errors.Unwrap(aggregateErr))
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("mixed nullable aggregate statements=%d", len(statements))
			}
			if value, ok := golem.AggregateValue(mixedResult, count).Get(); !ok || value != 6 {
				t.Fatalf("mixed nullable total count=%d/%t; want 6", value, ok)
			}
			for name, measure := range map[string]golem.Measure[p6metrics.Metric, int64]{
				"nullable-int64": optionalBigCount, "nullable-decimal": optionalAmountCount,
				"nullable-string": optionalLabelCount, "nullable-date": optionalDayCount,
				"nullable-time": optionalClockCount, "nullable-datetime": optionalInstantCount,
				"nullable-uuid": categoryIDCount,
			} {
				if value, ok := golem.AggregateValue(mixedResult, measure).Get(); !ok || value != 1 {
					t.Fatalf("%s mixed count=%d/%t; want 1", name, value, ok)
				}
			}

			if value, ok := golem.AggregateValue(result, labelMin).Get(); !ok || value != "A" {
				t.Fatalf("binary label min=%q/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, labelMax).Get(); !ok || value != "é" {
				t.Fatalf("binary label max=%q/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, dayMin).Get(); !ok || value.String() != "2024-01-01" {
				t.Fatalf("date min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, dayMax).Get(); !ok || value.String() != "2024-01-06" {
				t.Fatalf("date max=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, clockMin).Get(); !ok || value.String() != "01:02:03.000001" {
				t.Fatalf("time min=%v/%t", value, ok)
			}
			if value, ok := golem.AggregateValue(result, clockMax).Get(); !ok || value.String() != "06:02:03.000006" {
				t.Fatalf("time max=%v/%t", value, ok)
			}
			minimum, minimumOK := golem.AggregateValue(result, instantMin).Get()
			maximum, maximumOK := golem.AggregateValue(result, instantMax).Get()
			if !minimumOK || !maximumOK || minimum.Location() != time.UTC || maximum.Location() != time.UTC || minimum.Nanosecond()%1000 != 0 || maximum.Nanosecond()%1000 != 0 || !minimum.Before(maximum) {
				t.Fatalf("datetime min/max=%v/%t %v/%t", minimum, minimumOK, maximum, maximumOK)
			}

			label := p6metrics.Metrics.Label.Dimension()
			flag := p6metrics.Metrics.Flag.Dimension()
			reference := p6metrics.Metrics.Reference.Dimension()
			state := p6metrics.Metrics.State.Dimension()
			h.trace.reset()
			groups, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(label, flag, reference, state),
				p6metrics.Metrics.GroupMeasures(count),
				p6metrics.Metrics.GroupOrderBy(label.Asc()),
				p6metrics.Metrics.GroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("group statements=%d", len(statements))
			}
			wantLabels := []string{"A", "Z", "a", "e\u0301", "z", "é"}
			gotLabels := make([]string, len(groups))
			for index, row := range groups {
				value, ok := golem.GroupValue(row, label).Get()
				if !ok {
					t.Fatalf("group %d label absent", index)
				}
				gotLabels[index] = value
				if _, ok := golem.GroupValue(row, flag).Get(); !ok {
					t.Fatalf("group %d bool absent", index)
				}
				if _, ok := golem.GroupValue(row, reference).Get(); !ok {
					t.Fatalf("group %d UUID absent", index)
				}
				if _, ok := golem.GroupValue(row, state).Get(); !ok {
					t.Fatalf("group %d enum absent", index)
				}
			}
			if !reflect.DeepEqual(gotLabels, wantLabels) {
				t.Fatalf("binary group order=%q want=%q", gotLabels, wantLabels)
			}
		})
	}
}

func TestP6AggregateScalarResultMatrixProviderAgreement(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			integer, floating, decimal := p6metrics.Metrics.Integer.Sum(), p6metrics.Metrics.Double.Avg(), p6metrics.Metrics.Amount.Sum()
			result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(integer, floating, decimal)))
			if err != nil {
				t.Fatal(err)
			}
			i, iok := golem.AggregateValue(result, integer).Get()
			f, fok := golem.AggregateValue(result, floating).Get()
			d, dok := golem.AggregateValue(result, decimal).Get()
			if !iok || !fok || !dok || i.String() != "300" || f != 2.625 || d.String() != "199999999999999.9998" {
				t.Fatalf("provider-neutral scalar matrix integer=%q/%t float=%v/%t decimal=%q/%t", i.String(), iok, f, fok, d.String(), dok)
			}
		})
	}
}

func TestP6EmptyAndAllNullAggregateCells(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			count, nullSum, nullMin := p6metrics.Metrics.CountAll(), p6metrics.Metrics.OptionalBig.Sum(), p6metrics.Metrics.OptionalLabel.Min()
			result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count, nullSum, nullMin)))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := golem.AggregateValue(result, nullSum).Get(); ok {
				t.Fatal("all-null sum was present")
			}
			if _, ok := golem.AggregateValue(result, nullMin).Get(); ok {
				t.Fatal("all-null minimum was present")
			}
			missing := golem.UUID{15: 99}
			empty, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(
				p6metrics.Metrics.AggregateWhere(p6metrics.Metrics.ID.Eq(missing)), p6metrics.Metrics.AggregateSelect(count, p6metrics.Metrics.Big.Sum()),
			))
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := golem.AggregateValue(empty, count).Get(); !ok || value != 0 {
				t.Fatalf("empty count=%d/%t", value, ok)
			}
			if _, ok := golem.AggregateValue(empty, p6metrics.Metrics.Big.Sum()).Get(); ok {
				t.Fatal("empty sum was present")
			}
		})
	}
}

func TestP6ExactIntegerDecimalAndTemporalNeverPassThroughFloat(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			integer, decimal, day, clock, instant := p6metrics.Metrics.Big.Sum(), p6metrics.Metrics.Amount.Avg(), p6metrics.Metrics.Day.Min(), p6metrics.Metrics.Clock.Max(), p6metrics.Metrics.OccurredAt.Min()
			result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(integer, decimal, day, clock, instant)))
			if err != nil {
				t.Fatal(err)
			}
			i, iok := golem.AggregateValue(result, integer).Get()
			d, dok := golem.AggregateValue(result, decimal).Get()
			date, dateOK := golem.AggregateValue(result, day).Get()
			wall, wallOK := golem.AggregateValue(result, clock).Get()
			moment, momentOK := golem.AggregateValue(result, instant).Get()
			if !iok || !dok || !dateOK || !wallOK || !momentOK || i.String() != "18446744073709551612" || d.String() != "33333333333333.3333" || date.String() != "2024-01-01" || wall.String() != "06:02:03.000006" || moment.Nanosecond()%1000 != 0 {
				t.Fatalf("exact/temporal values=%q %q %v %v %v", i.String(), d.String(), date, wall, moment)
			}
		})
	}
}

func TestP6GeneratedMetricSignedPagingAndCompleteTiesAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			state := p6metrics.Metrics.State.Dimension()
			count := p6metrics.Metrics.CountAll()
			request := func(skip int) golem.GroupRequest[p6metrics.Metric] {
				options := []golem.GroupOption[p6metrics.Metric]{
					p6metrics.Metrics.GroupDimensions(state),
					p6metrics.Metrics.GroupMeasures(count),
					p6metrics.Metrics.GroupOrderBy(count.Asc()),
					p6metrics.Metrics.GroupTake(-1),
				}
				if skip != 0 {
					options = append(options, p6metrics.Metrics.GroupSkip(skip))
				}
				return p6metrics.Metrics.GroupBy(options...)
			}
			for _, test := range []struct {
				skip int
				want p6metrics.Status
			}{{0, p6metrics.StatusOmega}, {1, p6metrics.StatusAlpha}} {
				h.trace.reset()
				rows, err := caller.Metrics.GroupBy(context.Background(), request(test.skip))
				if err != nil || len(rows) != 1 {
					t.Fatalf("skip=%d rows=%d error=%v", test.skip, len(rows), err)
				}
				value, ok := golem.GroupValue(rows[0], state).Get()
				if !ok || value != test.want {
					t.Fatalf("skip=%d state=%q/%t want=%q", test.skip, value, ok, test.want)
				}
				if statements := h.trace.snapshot(); len(statements) != 1 {
					t.Fatalf("skip=%d statements=%d", test.skip, len(statements))
				}
			}
		})
	}
}

func TestP6LocalGroupByCompleteSemanticOracle(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			label, count, sum := p6metrics.Metrics.Label.Dimension(), p6metrics.Metrics.CountAll(), p6metrics.Metrics.Integer.Sum()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(label), p6metrics.Metrics.GroupMeasures(count, sum),
				p6metrics.Metrics.GroupHaving(count.Eq(1)), p6metrics.Metrics.GroupOrderBy(label.Asc()), p6metrics.Metrics.GroupSkip(1), p6metrics.Metrics.GroupTake(3),
			))
			if err != nil || len(rows) != 3 {
				t.Fatalf("complete local groups=%d error=%v", len(rows), err)
			}
			want := []string{"Z", "a", "e\u0301"}
			for index, row := range rows {
				value, ok := golem.GroupValue(row, label).Get()
				if !ok || value != want[index] {
					t.Fatalf("group %d=%q/%t", index, value, ok)
				}
			}
		})
	}
}

func TestP6NullKeyAndNullableMeasureGroups(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			key, count, sum := p6metrics.Metrics.OptionalLabel.Dimension(), p6metrics.Metrics.CountAll(), p6metrics.Metrics.OptionalBig.Sum()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(p6metrics.Metrics.GroupDimensions(key), p6metrics.Metrics.GroupMeasures(count, sum)))
			if err != nil || len(rows) != 1 {
				t.Fatalf("null groups=%d error=%v", len(rows), err)
			}
			if _, ok := golem.GroupValue(rows[0], key).Get(); ok {
				t.Fatal("NULL group key was present")
			}
			if value, ok := golem.GroupValue(rows[0], count).Get(); !ok || value != 6 {
				t.Fatalf("NULL group count=%d/%t", value, ok)
			}
			if _, ok := golem.GroupValue(rows[0], sum).Get(); ok {
				t.Fatal("all-null grouped sum was present")
			}
		})
	}
}

func TestP6HavingAndOrderPrivateMeasureIsAuthorizedButNotReturned(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			state, count, private := p6metrics.Metrics.State.Dimension(), p6metrics.Metrics.CountAll(), p6metrics.Metrics.Amount.Sum()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(state), p6metrics.Metrics.GroupMeasures(count),
				p6metrics.Metrics.GroupHaving(private.GT(golem.MustParseExactDecimal("0"))), p6metrics.Metrics.GroupOrderBy(private.Desc()),
			))
			if err != nil || len(rows) == 0 {
				t.Fatalf("private measure rows=%d error=%v", len(rows), err)
			}
			if _, ok := golem.GroupValue(rows[0], private).Get(); ok {
				t.Fatal("private having/order measure was returned")
			}
		})
	}
}

func TestP6SignedTakeSkipAndCanonicalTieBreakAgreement(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			state, count := p6metrics.Metrics.State.Dimension(), p6metrics.Metrics.CountAll()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(state), p6metrics.Metrics.GroupMeasures(count), p6metrics.Metrics.GroupOrderBy(count.Asc()), p6metrics.Metrics.GroupSkip(1), p6metrics.Metrics.GroupTake(-1),
			))
			if err != nil || len(rows) != 1 {
				t.Fatalf("signed rows=%d error=%v", len(rows), err)
			}
			if value, ok := golem.GroupValue(rows[0], state).Get(); !ok || value != p6metrics.StatusAlpha {
				t.Fatalf("canonical tie=%q/%t", value, ok)
			}
		})
	}
}

func TestP6BinaryAnalyticalStringSemanticsAcrossProviderCollations(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, _ := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			label := p6metrics.Metrics.Label.Dimension()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(p6metrics.Metrics.GroupDimensions(label), p6metrics.Metrics.GroupOrderBy(label.Asc())))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"A", "Z", "a", "e\u0301", "z", "é"}
			for index, row := range rows {
				value, ok := golem.GroupValue(row, label).Get()
				if !ok || value != want[index] {
					t.Fatalf("binary order %d=%q/%t", index, value, ok)
				}
			}
		})
	}
}

func TestP6TextWhereStillUsesDeclaredP2ComparisonMode(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, _ := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			count := p6metrics.Metrics.CountAll()
			query := func(predicate golem.Predicate[p6metrics.Metric]) int64 {
				result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateWhere(predicate), p6metrics.Metrics.AggregateSelect(count)))
				if err != nil {
					t.Fatal(err)
				}
				value, ok := golem.AggregateValue(result, count).Get()
				if !ok {
					t.Fatal("count absent")
				}
				return value
			}
			if sensitive := query(p6metrics.Metrics.Label.Contains("A")); sensitive != 1 {
				t.Fatalf("sensitive count=%d", sensitive)
			}
			if insensitive := query(p6metrics.Metrics.Label.Compare(golem.ASCIIInsensitive()).Contains("A")); insensitive != 2 {
				t.Fatalf("insensitive count=%d", insensitive)
			}
		})
	}
}

func TestP6TextMeasureHavingDefaultAndASCIIInsensitiveAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			state, minimum := p6metrics.Metrics.State.Dimension(), p6metrics.Metrics.Label.Min()
			query := func(predicate golem.GroupPredicate[p6metrics.Metric]) []golem.GroupRow[p6metrics.Metric] {
				rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
					p6metrics.Metrics.GroupDimensions(state), p6metrics.Metrics.GroupHaving(predicate), p6metrics.Metrics.GroupOrderBy(state.Asc()),
				))
				if err != nil {
					t.Fatal(err)
				}
				return rows
			}
			if rows := query(golem.TextMeasureContains(minimum, "a", golem.DefaultComparison())); len(rows) != 0 {
				t.Fatalf("sensitive text having rows=%d", len(rows))
			}
			rows := query(golem.TextMeasureContains(minimum, "a", golem.ASCIIInsensitive()))
			if len(rows) != 1 {
				t.Fatalf("insensitive text having rows=%d", len(rows))
			}
			if value, ok := golem.GroupValue(rows[0], state).Get(); !ok || value != p6metrics.StatusAlpha {
				t.Fatalf("insensitive group=%q/%t", value, ok)
			}
			if rows := query(golem.TextMeasureStartsWith(minimum, "a", golem.ASCIIInsensitive())); len(rows) != 1 {
				t.Fatalf("insensitive startsWith rows=%d", len(rows))
			}
			if rows := query(golem.TextMeasureEndsWith(minimum, "A", golem.DefaultComparison())); len(rows) != 1 {
				t.Fatalf("sensitive endsWith rows=%d", len(rows))
			}
		})
	}
}

func TestP6GraphQLTextMeasureHavingComparisonModesAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			sensitive := h.execute(t, principal, `query { groupByPosts(by: [authorID], having: {min: {title: {contains: "A", mode: sensitive}}}) { key { authorID } count } }`, nil)
			if len(sensitive.Errors) != 0 || len(p5SocialSlice(t, sensitive.Data["groupByPosts"])) != 0 {
				t.Fatalf("sensitive GraphQL text having=%#v", sensitive)
			}
			insensitive := h.execute(t, principal, `query { groupByPosts(by: [authorID], having: {min: {title: {contains: "A", startsWith: "A", endsWith: "OPEN", mode: insensitive}}}) { key { authorID } count } }`, nil)
			if len(insensitive.Errors) != 0 {
				t.Fatalf("insensitive GraphQL errors=%#v", insensitive.Errors)
			}
			rows := p5SocialSlice(t, insensitive.Data["groupByPosts"])
			if len(rows) != 1 || p5SocialMap(t, rows[0])["count"] != "3" {
				t.Fatalf("insensitive GraphQL rows=%#v", rows)
			}
		})
	}
}

func TestP6StringNullAndUnicodeCorpus(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, _ := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			label, optional, count := p6metrics.Metrics.Label.Dimension(), p6metrics.Metrics.OptionalLabel.Dimension(), p6metrics.Metrics.CountAll()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(p6metrics.Metrics.GroupDimensions(optional, label), p6metrics.Metrics.GroupMeasures(count), p6metrics.Metrics.GroupOrderBy(optional.Asc(), label.Asc())))
			if err != nil || len(rows) != 6 {
				t.Fatalf("unicode/null rows=%d error=%v", len(rows), err)
			}
			if _, ok := golem.GroupValue(rows[0], optional).Get(); ok {
				t.Fatal("nullable string key was present")
			}
			seen := map[string]bool{}
			for _, row := range rows {
				value, _ := golem.GroupValue(row, label).Get()
				seen[value] = true
			}
			if !seen["é"] || !seen["e\u0301"] || len(seen) != 6 {
				t.Fatalf("unicode corpus=%#v", seen)
			}
		})
	}
}

func TestP6ContributionAndIntermediateOverflowReturnNoRows(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{MaxContributionRows: 3, MaxIntermediateGroups: 2})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			count := p6metrics.Metrics.CountAll()
			h.trace.reset()
			if _, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count))); err == nil {
				t.Fatal("contribution overflow succeeded")
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("contribution guard statements=%d", len(statements))
			}

			intermediateApp, err := p6metrics.Open(context.Background(), p6metrics.Config[p6metrics.Principal]{
				Database:          h.handle,
				AnalyticsLimits:   golemruntime.AnalyticsLimits{MaxContributionRows: 10, MaxIntermediateGroups: 2},
				AuditPrincipal:    func(p6metrics.Principal) string { return "p6-metrics" },
				ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
				ResolvePrincipal: func(_ context.Context, principal p6metrics.Principal) (p6metrics.Actor, error) {
					return p6metrics.Actor{CategoryPrefix: principal.CategoryPrefix}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			intermediateCaller, err := intermediateApp.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			label := p6metrics.Metrics.Label.Dimension()
			h.trace.reset()
			rows, err := intermediateCaller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(label), p6metrics.Metrics.GroupMeasures(count), p6metrics.Metrics.GroupTake(100),
			))
			if err == nil || rows != nil {
				t.Fatalf("intermediate overflow rows=%#v error=%v", rows, err)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("intermediate guard statements=%d", len(statements))
			}
		})
	}
}

func TestP6Programmatic34424GroupsAreComplete(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{
				MaxContributionRows: 40_000, MaxIntermediateGroups: 40_000, MaxProgrammaticGroups: 40_000,
			})
			h.insertScaleGroups(t, 34_424)
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			label := p6metrics.Metrics.Label.Dimension()
			h.trace.reset()
			rows, err := caller.Metrics.GroupBy(context.Background(), p6metrics.Metrics.GroupBy(
				p6metrics.Metrics.GroupDimensions(label),
				p6metrics.Metrics.GroupOrderBy(label.Asc()),
			))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 34_424 {
				t.Fatalf("programmatic groups=%d want=34424", len(rows))
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("programmatic scale statements=%d", len(statements))
			}
		})
	}
}

func TestP6ForwardToOneRelationGroupProviderOracle(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-"})
			if err != nil {
				t.Fatal(err)
			}
			dimension := p6metrics.Metrics.CategoryParentName
			count := p6metrics.Metrics.CountAll()
			average := p6metrics.Metrics.Amount.Avg()
			h.trace.reset()
			rows, err := caller.Metrics.RelationGroupBy(context.Background(), p6metrics.Metrics.RelationGroupBy(
				p6metrics.Metrics.RelationGroupDimensions(dimension),
				p6metrics.Metrics.RelationGroupMeasures(count, average),
				p6metrics.Metrics.RelationGroupOrderBy(dimension.Asc()),
				p6metrics.Metrics.RelationGroupTake(100),
			))
			if err != nil || len(rows) != 1 {
				t.Fatalf("relation rows=%d error=%v", len(rows), err)
			}
			name, nameOK := golem.RelationGroupValue(rows[0], dimension).Get()
			contributions, countOK := golem.RelationGroupValue(rows[0], count).Get()
			exactAverage, averageOK := golem.RelationGroupValue(rows[0], average).Get()
			if !nameOK || !countOK || !averageOK || name != "public-root" || contributions != 2 || exactAverage.String() != "99999999999999.9999" {
				t.Fatalf("relation result=%q/%t count=%d/%t avg=%q/%t", name, nameOK, contributions, countOK, exactAverage.String(), averageOK)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("relation statements=%d", len(statements))
			}
		})
	}
}

func TestP6RelationAbsentAndInvisibleTargetsAreIndistinguishable(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-"})
			if err != nil {
				t.Fatal(err)
			}
			dimension, count := p6metrics.Metrics.CategoryParentName, p6metrics.Metrics.CountAll()
			rows, err := caller.Metrics.RelationGroupBy(context.Background(), p6metrics.Metrics.RelationGroupBy(
				p6metrics.Metrics.RelationGroupDimensions(dimension), p6metrics.Metrics.RelationGroupMeasures(count),
			))
			if err != nil || len(rows) != 1 {
				t.Fatalf("caller relation rows=%d error=%v", len(rows), err)
			}
			value, ok := golem.RelationGroupValue(rows[0], count).Get()
			if !ok || value != 2 {
				t.Fatalf("authorized inner contributions=%d/%t", value, ok)
			}
			var direct int64
			query := `SELECT COUNT(*) FROM ` + h.table("p6_metrics") + ` AS m JOIN ` + h.table("p6_categories") + ` AS c ON m."category_id"=c."id" JOIN ` + h.table("p6_categories") + ` AS p ON c."parent_id"=p."id" WHERE c."name" LIKE ? AND p."name" LIKE ?`
			if err := h.database.GetContext(context.Background(), &direct, h.database.Rebind(query), "public-%", "public-%"); err != nil {
				t.Fatal(err)
			}
			if direct != 2 || value != direct {
				t.Fatalf("runtime=%d direct authorized inner oracle=%d", value, direct)
			}
		})
	}
}

func TestP6RelationHopPolicyAndConditionalTerminalDischarge(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := h.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			dimension, count := p5social.Posts.AuthorName, p5social.Posts.CountAll()
			h.trace.reset()
			if _, err := caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(dimension), p5social.Posts.RelationGroupMeasures(count),
			)); err == nil || len(h.trace.snapshot()) != 0 {
				t.Fatal("undischarged conditional terminal did not refuse before SQL")
			}
			h.trace.reset()
			rows, err := caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(dimension), p5social.Posts.RelationGroupMeasures(count),
				p5social.Posts.RelationGroupWhere(p5social.Posts.AuthorID.Eq(principal.UserID)),
			))
			if err != nil || len(rows) != 1 || len(h.trace.snapshot()) != 1 {
				t.Fatalf("discharged relation rows=%d statements=%d error=%v", len(rows), len(h.trace.snapshot()), err)
			}
		})
	}
}

func TestP6RelationAverageUsesOneSQLContributionSet(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-"})
			if err != nil {
				t.Fatal(err)
			}
			dimension, average := p6metrics.Metrics.CategoryParentName, p6metrics.Metrics.Amount.Avg()
			h.trace.reset()
			rows, err := caller.Metrics.RelationGroupBy(context.Background(), p6metrics.Metrics.RelationGroupBy(
				p6metrics.Metrics.RelationGroupDimensions(dimension), p6metrics.Metrics.RelationGroupMeasures(average),
			))
			if err != nil || len(rows) != 1 {
				t.Fatalf("relation average rows=%d error=%v", len(rows), err)
			}
			value, ok := golem.RelationGroupValue(rows[0], average).Get()
			if !ok || value.String() != "99999999999999.9999" || len(h.trace.snapshot()) != 1 {
				t.Fatalf("relation average=%q/%t statements=%d", value.String(), ok, len(h.trace.snapshot()))
			}
		})
	}
}

func TestP6RelationDepthLimitRefusesBeforeSQL(t *testing.T) {
	h := newP6MetricsHarness(t, p5ExtensionProviderProfiles()[0], golemruntime.AnalyticsLimits{MaxRelationDepth: 1})
	caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-"})
	if err != nil {
		t.Fatal(err)
	}
	h.trace.reset()
	dimension := p6metrics.Metrics.CategoryParentName
	if _, err := caller.Metrics.RelationGroupBy(context.Background(), p6metrics.Metrics.RelationGroupBy(
		p6metrics.Metrics.RelationGroupDimensions(dimension),
	)); err == nil || len(h.trace.snapshot()) != 0 {
		t.Fatalf("depth-limited relation error=%v statements=%d", err, len(h.trace.snapshot()))
	}
}

func (h *p6MetricsHarness) insertScaleGroups(t *testing.T, total int) {
	t.Helper()
	table := `"p6_metrics"`
	if h.profile.provider == golem.PostgreSQL {
		table = `"` + string(p6MetricsPostgreSQLNamespace) + `"."p6_metrics"`
	}
	query := `INSERT INTO ` + table + ` ("id","flag","small","integer_value","big_value","float_value","double_value","amount","label","reference","day","clock","occurred_at","state","optional_big","optional_amount","optional_label","optional_day","optional_clock","optional_instant") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	tx, err := h.database.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Preparex(tx.Rebind(query))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer statement.Close()
	amount, instant := any(int64(0)), any(int64(0))
	if h.profile.provider == golem.PostgreSQL {
		amount, instant = "0", time.Unix(0, 0).UTC()
	}
	for index := 6; index < total; index++ {
		id := fmt.Sprintf("10000000-0000-0000-0000-%012d", index)
		label := fmt.Sprintf("scale-%05d", index)
		if _, err := statement.Exec(id, false, int16(0), int32(0), int64(0), float32(0), float64(0), amount, label, "20000000-0000-0000-0000-000000000001", "2024-01-01", "12:00:00.000000", instant, "alpha", nil, nil, nil, nil, nil, nil); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert scale group %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h.trace.reset()
}

func TestP6IndependentSocialAnalyticsOracleSQLite(t *testing.T) {
	runP6IndependentProviderOracle(t, p5ExtensionProviderProfiles()[0])
}

func TestP6IndependentSocialAnalyticsOraclePostgreSQLProfiles(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles()[1:] {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			runP6IndependentProviderOracle(t, profile)
		})
	}
}

func runP6IndependentProviderOracle(t *testing.T, profile p5ExtensionProviderProfile) {
	t.Helper()
	t.Run("social-direct-sql", func(t *testing.T) {
		h := newP5SocialGeneratedHarness(t, profile)
		principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
		caller, err := h.app.ForPrincipal(context.Background(), principal)
		if err != nil {
			t.Fatal(err)
		}
		var directPosts, directTags, directOwnedPosts int64
		if err := h.database.GetContext(context.Background(), &directPosts, `SELECT COUNT(*) FROM `+h.table("posts")); err != nil {
			t.Fatal(err)
		}
		if err := h.database.GetContext(context.Background(), &directTags, h.database.Rebind(`SELECT COUNT(*) FROM `+h.table("tags")+` WHERE "name" = ?`), "tag-a"); err != nil {
			t.Fatal(err)
		}
		if err := h.database.GetContext(context.Background(), &directOwnedPosts, h.database.Rebind(`SELECT COUNT(*) FROM `+h.table("posts")+` WHERE "author_id" = ?`), p5SocialID(1)); err != nil {
			t.Fatal(err)
		}
		postCount, tagCount := p5social.Posts.CountAll(), p5social.Tags.CountAll()
		posts, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(p5social.Posts.AggregateSelect(postCount)))
		if err != nil {
			t.Fatal(err)
		}
		tags, err := caller.Tags.Aggregate(context.Background(), p5social.Tags.Aggregate(p5social.Tags.AggregateSelect(tagCount)))
		if err != nil {
			t.Fatal(err)
		}
		gotPosts, postOK := golem.AggregateValue(posts, postCount).Get()
		gotTags, tagOK := golem.AggregateValue(tags, tagCount).Get()
		if !postOK || !tagOK || gotPosts != directPosts || gotTags != directTags || directPosts != 4 || directTags != 1 {
			t.Fatalf("social direct/runtime posts=%d/%d tags=%d/%d", directPosts, gotPosts, directTags, gotTags)
		}
		dimension := p5social.Posts.AuthorName
		relation, err := caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
			p5social.Posts.RelationGroupDimensions(dimension), p5social.Posts.RelationGroupMeasures(postCount), p5social.Posts.RelationGroupWhere(p5social.Posts.AuthorID.Eq(principal.UserID)),
		))
		if err != nil || len(relation) != 1 {
			t.Fatalf("social relation=%d error=%v", len(relation), err)
		}
		gotOwned, ok := golem.RelationGroupValue(relation[0], postCount).Get()
		if !ok || gotOwned != directOwnedPosts || gotOwned != 3 {
			t.Fatalf("owned posts direct/runtime=%d/%d", directOwnedPosts, gotOwned)
		}
	})
	t.Run("metrics-direct-sql", func(t *testing.T) {
		h := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
		caller, err := h.app.ForPrincipal(context.Background(), p6metrics.Principal{})
		if err != nil {
			t.Fatal(err)
		}
		var raw []int64
		if err := h.database.SelectContext(context.Background(), &raw, `SELECT "big_value" FROM `+h.table("p6_metrics")); err != nil {
			t.Fatal(err)
		}
		directSum := new(big.Int)
		for _, value := range raw {
			directSum.Add(directSum, big.NewInt(value))
		}
		var directCount int64
		if err := h.database.GetContext(context.Background(), &directCount, `SELECT COUNT(*) FROM `+h.table("p6_metrics")); err != nil {
			t.Fatal(err)
		}
		count, sum, minimum, maximum := p6metrics.Metrics.CountAll(), p6metrics.Metrics.Big.Sum(), p6metrics.Metrics.Label.Min(), p6metrics.Metrics.Label.Max()
		result, err := caller.Metrics.Aggregate(context.Background(), p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count, sum, minimum, maximum)))
		if err != nil {
			t.Fatal(err)
		}
		gotCount, countOK := golem.AggregateValue(result, count).Get()
		gotSum, sumOK := golem.AggregateValue(result, sum).Get()
		gotMin, minOK := golem.AggregateValue(result, minimum).Get()
		gotMax, maxOK := golem.AggregateValue(result, maximum).Get()
		if !countOK || !sumOK || !minOK || !maxOK || gotCount != directCount || gotSum.String() != directSum.String() || gotMin != "A" || gotMax != "é" {
			t.Fatalf("metrics direct/runtime count=%d/%d sum=%s/%s min/max=%q/%q", directCount, gotCount, directSum.String(), gotSum.String(), gotMin, gotMax)
		}
	})
}
