package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
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
