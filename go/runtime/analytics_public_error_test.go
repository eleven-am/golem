package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestAnalyticsPlanRefusalsCarryLimitsAndReasons(t *testing.T) {
	profile := p5ExtensionProviderProfiles()[0]
	harness := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
	metricModel := p6metrics.GolemGeneratedMetricDescriptor.Metadata().ModelID()
	categoryModel := p6metrics.GolemGeneratedCategoryDescriptor.Metadata().ModelID()
	frozenLabel, err := golem.RuntimeFreezeCreateInput(p6metrics.Metrics.Create(p6metrics.Metrics.Label.Create("x")))
	if err != nil {
		t.Fatal(err)
	}
	label := frozenLabel.Fields()[0].FieldID()
	allMetrics := func(rules *golem.Rules[p6metrics.Metric]) { rules.CanRead(golem.All[p6metrics.Metric]()) }
	allCategories := func(rules *golem.Rules[p6metrics.Category]) { rules.CanRead(golem.All[p6metrics.Category]()) }
	count := p6metrics.Metrics.CountAll()
	tests := []struct {
		name     string
		limits   golemruntime.ReadLimits
		metric   func(*golem.Rules[p6metrics.Metric])
		category func(*golem.Rules[p6metrics.Category])
		run      func(context.Context, *golemruntime.Caller[p6metrics.Principal, p6metrics.Actor]) error
		code     golem.ErrorCode
		reason   golem.Reason
		field    golem.FieldID
		limit    golem.Limit
		maximum  int
		message  string
	}{
		{
			name: "selected fields", limits: golemruntime.ReadLimits{MaxSelectedFields: 1}, metric: allMetrics, category: allCategories,
			run: func(ctx context.Context, caller *golemruntime.Caller[p6metrics.Principal, p6metrics.Actor]) error {
				_, err := golemruntime.CallerGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, p6metrics.Metrics.GroupBy(
					p6metrics.Metrics.GroupDimensions(p6metrics.Metrics.Label.Dimension(), p6metrics.Metrics.State.Dimension()),
					p6metrics.Metrics.GroupMeasures(count),
				))
				return err
			},
			code: golem.CodeLimitExceeded, reason: golem.ReasonLimitExceeded, limit: golem.LimitSelectedFields, maximum: 1,
			message: "request exceeds the selected_fields limit of 1",
		},
		{
			name: "not granted", metric: func(*golem.Rules[p6metrics.Metric]) {}, category: allCategories,
			run: func(ctx context.Context, caller *golemruntime.Caller[p6metrics.Principal, p6metrics.Actor]) error {
				_, err := golemruntime.CallerAggregate(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count)))
				return err
			},
			code: golem.CodeForbidden, reason: golem.ReasonNotGranted, message: "operation is not granted",
		},
		{
			name: "policy invalid",
			metric: func(rules *golem.Rules[p6metrics.Metric]) {
				rules.CanRead(p6metrics.Metrics.Category.Is(golem.All[p6metrics.Category]()))
			},
			category: func(rules *golem.Rules[p6metrics.Category]) {
				rules.CanRead(p6metrics.Categories.Metrics.Some(golem.All[p6metrics.Metric]()))
			},
			run: func(ctx context.Context, caller *golemruntime.Caller[p6metrics.Principal, p6metrics.Actor]) error {
				_, err := golemruntime.CallerGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, p6metrics.Metrics.GroupBy(
					p6metrics.Metrics.GroupDimensions(p6metrics.Metrics.Label.Dimension()),
					p6metrics.Metrics.GroupMeasures(count),
				))
				return err
			},
			code: golem.CodeForbidden, reason: golem.ReasonPolicyInvalid, message: "policy cannot be applied to this request",
		},
		{
			name: "caller-named field not readable",
			metric: func(rules *golem.Rules[p6metrics.Metric]) {
				rules.CanRead(golem.All[p6metrics.Metric]())
				rules.CannotReadFields(golem.All[p6metrics.Metric](), p6metrics.Metrics.Label)
			},
			category: allCategories,
			run: func(ctx context.Context, caller *golemruntime.Caller[p6metrics.Principal, p6metrics.Actor]) error {
				_, err := golemruntime.CallerAggregate(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, p6metrics.Metrics.Aggregate(
					p6metrics.Metrics.AggregateWhere(p6metrics.Metrics.Label.Eq("secret-label")),
					p6metrics.Metrics.AggregateSelect(count),
				))
				return err
			},
			code: golem.CodeForbidden, reason: golem.ReasonFieldNotReadable, field: label, message: "field is not readable in this position",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metricPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Metric](metricModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Metric]()
				test.metric(rules)
				return rules.Freeze(metricModel)
			})
			categoryPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Category](categoryModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Category]()
				test.category(rules)
				return rules.Freeze(categoryModel)
			})
			bundle := p6metrics.GolemGeneratedSchemaBundle()
			bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[p6metrics.Actor]{metricPolicy, categoryPolicy}, nil))
			if err != nil {
				t.Fatal(err)
			}
			descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
			if err != nil {
				t.Fatal(err)
			}
			app, err := golemruntime.Open(context.Background(), golemruntime.Config[p6metrics.Principal, p6metrics.Actor]{
				Database: harness.handle, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ReadLimits: test.limits,
				AuditPrincipal:    func(p6metrics.Principal) string { return "analytics-errors" },
				ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
				ResolvePrincipal: func(context.Context, p6metrics.Principal) (p6metrics.Actor, error) {
					return p6metrics.Actor{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			caller, err := app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			refused := test.run(context.Background(), caller)
			var failure *golem.Error
			if !errors.As(refused, &failure) || failure.Code != test.code || failure.Reason != test.reason || failure.Field != test.field || failure.Limit != test.limit || failure.LimitMax != test.maximum || failure.Message != test.message || failure.Operation != "analytics" {
				t.Fatalf("failure=%#v err=%v", failure, refused)
			}
			if !errors.Is(refused, test.code) || !errors.Is(refused, test.reason) {
				t.Fatalf("errors.Is mismatch: %v", refused)
			}
		})
	}
}

func TestAnalyticsWhereRefusalsMatchFindManyAcrossProviders(t *testing.T) {
	metricModel := p6metrics.GolemGeneratedMetricDescriptor.Metadata().ModelID()
	categoryModel := p6metrics.GolemGeneratedCategoryDescriptor.Metadata().ModelID()
	offGrid := time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)
	outOfRange := time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC)
	frozenAt, err := golem.RuntimeFreezeCreateInput(p6metrics.Metrics.Create(p6metrics.Metrics.OccurredAt.Create(offGrid)))
	if err != nil {
		t.Fatal(err)
	}
	occurredAt := frozenAt.Fields()[0].FieldID()
	frozenClock, err := golem.RuntimeFreezeCreateInput(p6metrics.Metrics.Create(p6metrics.Metrics.Clock.Create(golem.Time{})))
	if err != nil {
		t.Fatal(err)
	}
	clock := frozenClock.Fields()[0].FieldID()
	misTyped := golem.GeneratedTextField[p6metrics.Metric, string](occurredAt)
	tests := []struct {
		name   string
		where  golem.Predicate[p6metrics.Metric]
		code   golem.ErrorCode
		reason golem.Reason
		field  golem.FieldID
		system bool
	}{
		{name: "off-grid datetime in list with an unrepresentable member", where: p6metrics.Metrics.OccurredAt.In(offGrid, outOfRange), code: golem.CodeBadUserInput, system: true},
		{name: "unrepresentable datetime operand", where: p6metrics.Metrics.OccurredAt.Eq(outOfRange), code: golem.CodeBadUserInput, system: true},
		{name: "text operand on a datetime field", where: misTyped.Eq("x"), code: golem.CodeBadUserInput, system: true},
		{name: "text membership on a datetime field", where: misTyped.In("x", "y"), code: golem.CodeBadUserInput, system: true},
		{name: "text operator on a datetime field", where: misTyped.Contains("x"), code: golem.CodeBadUserInput, system: true},
		{name: "unknown field", where: golem.GeneratedTextField[p6metrics.Metric, string](golem.FieldID{1, 2, 3}).Eq("x"), code: golem.CodeBadUserInput, system: true},
		{name: "unreadable field", where: p6metrics.Metrics.Clock.Eq(golem.Time{}), code: golem.CodeForbidden, reason: golem.ReasonFieldNotReadable, field: clock},
	}
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			harness := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			metricPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Metric](metricModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Metric]()
				rules.CanRead(golem.All[p6metrics.Metric]())
				rules.CannotReadFields(golem.All[p6metrics.Metric](), p6metrics.Metrics.Clock)
				return rules.Freeze(metricModel)
			})
			categoryPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Category](categoryModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Category]()
				rules.CanRead(golem.All[p6metrics.Category]())
				return rules.Freeze(categoryModel)
			})
			bundle := p6metrics.GolemGeneratedSchemaBundle()
			bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[p6metrics.Actor]{metricPolicy, categoryPolicy}, nil))
			if err != nil {
				t.Fatal(err)
			}
			descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
			if err != nil {
				t.Fatal(err)
			}
			app, err := golemruntime.Open(context.Background(), golemruntime.Config[p6metrics.Principal, p6metrics.Actor]{
				Database: harness.handle, Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
				AuditPrincipal:    func(p6metrics.Principal) string { return "analytics-where" },
				ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
				ResolvePrincipal: func(context.Context, p6metrics.Principal) (p6metrics.Actor, error) {
					return p6metrics.Actor{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			caller, err := app.ForPrincipal(context.Background(), p6metrics.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			system := app.System()
			descriptor := p6metrics.GolemGeneratedMetricDescriptor
			count := p6metrics.Metrics.CountAll()
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					ctx := context.Background()
					where := test.where
					_, readErr := golemruntime.CallerFindMany(ctx, caller, descriptor, p6metrics.Metrics.Where(where), p6metrics.Metrics.Select(p6metrics.Metrics.ID))
					read := assertAnalyticsWhereRefusal(t, "findMany", readErr, test.code, test.reason, test.field)
					analyticsRuns := map[string]error{}
					_, analyticsRuns["caller aggregate"] = golemruntime.CallerAggregate(ctx, caller, descriptor, p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateWhere(where), p6metrics.Metrics.AggregateSelect(count)))
					_, analyticsRuns["caller groupBy"] = golemruntime.CallerGroupBy(ctx, caller, descriptor, p6metrics.Metrics.GroupBy(
						p6metrics.Metrics.GroupDimensions(p6metrics.Metrics.Label.Dimension()),
						p6metrics.Metrics.GroupMeasures(count),
						p6metrics.Metrics.GroupWhere(where),
					))
					for name, analyticsErr := range analyticsRuns {
						refused := assertAnalyticsWhereRefusal(t, name, analyticsErr, read.Code, read.Reason, read.Field)
						if refused.Operation != "analytics" {
							t.Fatalf("%s operation=%q", name, refused.Operation)
						}
					}
					if !test.system {
						return
					}
					_, systemReadErr := golemruntime.SystemFindMany(ctx, system, descriptor, p6metrics.Metrics.Where(where), p6metrics.Metrics.Select(p6metrics.Metrics.ID))
					systemRead := assertAnalyticsWhereRefusal(t, "system findMany", systemReadErr, test.code, test.reason, test.field)
					systemRuns := map[string]error{}
					_, systemRuns["system aggregate"] = golemruntime.SystemAggregate(ctx, system, descriptor, p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateWhere(where), p6metrics.Metrics.AggregateSelect(count)))
					_, systemRuns["system groupBy"] = golemruntime.SystemGroupBy(ctx, system, descriptor, p6metrics.Metrics.GroupBy(
						p6metrics.Metrics.GroupDimensions(p6metrics.Metrics.Label.Dimension()),
						p6metrics.Metrics.GroupMeasures(count),
						p6metrics.Metrics.GroupWhere(where),
					))
					for name, analyticsErr := range systemRuns {
						assertAnalyticsWhereRefusal(t, name, analyticsErr, systemRead.Code, systemRead.Reason, systemRead.Field)
					}
				})
			}
		})
	}
}

func assertAnalyticsWhereRefusal(t *testing.T, name string, err error, code golem.ErrorCode, reason golem.Reason, field golem.FieldID) *golem.Error {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Reason != reason || failure.Field != field {
		t.Fatalf("%s refusal=%#v err=%v want code=%s reason=%q field=%x", name, failure, err, code, reason, field)
	}
	if !errors.Is(err, code) {
		t.Fatalf("%s errors.Is(%s) mismatch: %v", name, code, err)
	}
	return failure
}
