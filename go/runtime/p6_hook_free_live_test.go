package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestP6AnalyticsAndScopedNeverInvokeOrdinaryReadHooks(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			bundle := p6metrics.GolemGeneratedSchemaBundle()
			metricModel := p6metrics.GolemGeneratedMetricDescriptor.Metadata().ModelID()
			categoryModel := p6metrics.GolemGeneratedCategoryDescriptor.Metadata().ModelID()

			metricPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Metric](metricModel, func(actor p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Metric]()
				p6metrics.Metric{}.DefinePolicy(rules, actor)
				return rules.Freeze(metricModel)
			})
			categoryPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Category](categoryModel, func(actor p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Category]()
				p6metrics.Category{}.DefinePolicy(rules, actor)
				return rules.Freeze(categoryModel)
			})
			const (
				metricHooks = iota
				categoryHooks
			)
			const (
				findOneHooks = iota
				findFirstHooks
				findManyHooks
			)
			const (
				beforeHooks = iota
				afterHooks
			)
			var hookCalls [2][3][2]atomic.Int64
			metricFindOneBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindOneHookRequest[p6metrics.Metric]](
				metricModel, golem.HookFindOne, func(context.Context, *golem.FindOneHookRequest[p6metrics.Metric]) error {
					hookCalls[metricHooks][findOneHooks][beforeHooks].Add(1)
					return nil
				},
			)
			metricFindOneAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindOneHookResult[p6metrics.Metric]](
				metricModel, golem.HookFindOne, func(context.Context, golem.FindOneHookResult[p6metrics.Metric]) error {
					hookCalls[metricHooks][findOneHooks][afterHooks].Add(1)
					return nil
				},
			)
			metricFindFirstBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindFirstHookRequest[p6metrics.Metric]](
				metricModel, golem.HookFindFirst, func(context.Context, *golem.FindFirstHookRequest[p6metrics.Metric]) error {
					hookCalls[metricHooks][findFirstHooks][beforeHooks].Add(1)
					return nil
				},
			)
			metricFindFirstAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindFirstHookResult[p6metrics.Metric]](
				metricModel, golem.HookFindFirst, func(context.Context, golem.FindFirstHookResult[p6metrics.Metric]) error {
					hookCalls[metricHooks][findFirstHooks][afterHooks].Add(1)
					return nil
				},
			)
			metricFindManyBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindManyHookRequest[p6metrics.Metric]](
				metricModel,
				golem.HookFindMany,
				func(context.Context, *golem.FindManyHookRequest[p6metrics.Metric]) error {
					hookCalls[metricHooks][findManyHooks][beforeHooks].Add(1)
					return nil
				},
			)
			metricFindManyAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Metric, golem.FindManyHookResult[p6metrics.Metric]](
				metricModel, golem.HookFindMany, func(context.Context, golem.FindManyHookResult[p6metrics.Metric]) error {
					hookCalls[metricHooks][findManyHooks][afterHooks].Add(1)
					return nil
				},
			)
			categoryFindOneBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindOneHookRequest[p6metrics.Category]](
				categoryModel, golem.HookFindOne, func(context.Context, *golem.FindOneHookRequest[p6metrics.Category]) error {
					hookCalls[categoryHooks][findOneHooks][beforeHooks].Add(1)
					return nil
				},
			)
			categoryFindOneAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindOneHookResult[p6metrics.Category]](
				categoryModel, golem.HookFindOne, func(context.Context, golem.FindOneHookResult[p6metrics.Category]) error {
					hookCalls[categoryHooks][findOneHooks][afterHooks].Add(1)
					return nil
				},
			)
			categoryFindFirstBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindFirstHookRequest[p6metrics.Category]](
				categoryModel, golem.HookFindFirst, func(context.Context, *golem.FindFirstHookRequest[p6metrics.Category]) error {
					hookCalls[categoryHooks][findFirstHooks][beforeHooks].Add(1)
					return nil
				},
			)
			categoryFindFirstAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindFirstHookResult[p6metrics.Category]](
				categoryModel, golem.HookFindFirst, func(context.Context, golem.FindFirstHookResult[p6metrics.Category]) error {
					hookCalls[categoryHooks][findFirstHooks][afterHooks].Add(1)
					return nil
				},
			)
			categoryFindManyBefore := golem.GeneratedBeforeHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindManyHookRequest[p6metrics.Category]](
				categoryModel, golem.HookFindMany, func(context.Context, *golem.FindManyHookRequest[p6metrics.Category]) error {
					hookCalls[categoryHooks][findManyHooks][beforeHooks].Add(1)
					return nil
				},
			)
			categoryFindManyAfter := golem.GeneratedAfterHookBinding[p6metrics.Actor, p6metrics.Category, golem.FindManyHookResult[p6metrics.Category]](
				categoryModel, golem.HookFindMany, func(context.Context, golem.FindManyHookResult[p6metrics.Category]) error {
					hookCalls[categoryHooks][findManyHooks][afterHooks].Add(1)
					return nil
				},
			)
			bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(
				bundle.GenerationDigest(),
				[]golem.PolicyBinding[p6metrics.Actor]{metricPolicy, categoryPolicy},
				[]golem.HookBinding[p6metrics.Actor]{
					metricFindOneBefore, metricFindOneAfter, metricFindFirstBefore, metricFindFirstAfter, metricFindManyBefore, metricFindManyAfter,
					categoryFindOneBefore, categoryFindOneAfter, categoryFindFirstBefore, categoryFindFirstAfter, categoryFindManyBefore, categoryFindManyAfter,
				},
			))
			if err != nil {
				t.Fatal(err)
			}
			descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
			if err != nil {
				t.Fatal(err)
			}
			app, err := golemruntime.Open(context.Background(), golemruntime.Config[p6metrics.Principal, p6metrics.Actor]{
				DB: harness.database, Provider: profile.provider, Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
				AuditPrincipal:    func(p6metrics.Principal) string { return "hook-oracle" },
				ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
				ResolvePrincipal: func(_ context.Context, principal p6metrics.Principal) (p6metrics.Actor, error) {
					return p6metrics.Actor{CategoryPrefix: principal.CategoryPrefix}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			caller, err := app.ForPrincipal(context.Background(), p6metrics.Principal{CategoryPrefix: "public-"})
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			metricID := golem.UUID{14: 1, 15: 1}
			categoryID := golem.UUID{14: 2, 15: 1}
			if _, err := golemruntime.CallerFindUnique(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, p6metrics.Metrics.ByID.Value(metricID), golem.Select[p6metrics.Metric](p6metrics.Metrics.ID)); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerFindUnique(ctx, caller, p6metrics.GolemGeneratedCategoryDescriptor, p6metrics.Categories.ByID.Value(categoryID), golem.Select[p6metrics.Category](p6metrics.Categories.ID)); err != nil {
				t.Fatal(err)
			}
			if _, found, err := golemruntime.CallerFindFirst(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, golem.Select[p6metrics.Metric](p6metrics.Metrics.ID)); err != nil || !found {
				t.Fatalf("ordinary Metric FindFirst found=%t error=%v", found, err)
			}
			if _, found, err := golemruntime.CallerFindFirst(ctx, caller, p6metrics.GolemGeneratedCategoryDescriptor, golem.Select[p6metrics.Category](p6metrics.Categories.ID)); err != nil || !found {
				t.Fatalf("ordinary Category FindFirst found=%t error=%v", found, err)
			}
			if _, err := golemruntime.CallerFindMany(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, golem.Select[p6metrics.Metric](p6metrics.Metrics.ID)); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerFindMany(ctx, caller, p6metrics.GolemGeneratedCategoryDescriptor, golem.Select[p6metrics.Category](p6metrics.Categories.ID)); err != nil {
				t.Fatal(err)
			}
			for model := range hookCalls {
				for operation := range hookCalls[model] {
					for phase := range hookCalls[model][operation] {
						if got := hookCalls[model][operation][phase].Load(); got != 1 {
							t.Fatalf("ordinary hook proof model=%d operation=%d phase=%d calls=%d want 1", model, operation, phase, got)
						}
						hookCalls[model][operation][phase].Store(0)
					}
				}
			}

			count := p6metrics.Metrics.CountAll()
			label := p6metrics.Metrics.Label.Dimension()
			aggregateRequest := p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count))
			groupRequest := p6metrics.Metrics.GroupBy(p6metrics.Metrics.GroupDimensions(label), p6metrics.Metrics.GroupMeasures(count))
			relationRequest := p6metrics.Metrics.RelationGroupBy(
				p6metrics.Metrics.RelationGroupDimensions(p6metrics.Metrics.CategoryParentName),
				p6metrics.Metrics.RelationGroupMeasures(count),
			)
			root := p6metrics.Metrics.Scope()
			scopedLabel := p6metrics.Metrics.Label.At(root)
			scopedRequest := golem.From(root).Select(scopedLabel).Take(1)
			if _, err := golemruntime.CallerCount(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerAggregate(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, groupRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerRelationGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, relationRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.CallerScoped(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, scopedRequest); err != nil {
				t.Fatal(err)
			}
			system := app.System()
			if _, err := golemruntime.SystemCount(ctx, system, p6metrics.GolemGeneratedMetricDescriptor); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.SystemAggregate(ctx, system, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.SystemGroupBy(ctx, system, p6metrics.GolemGeneratedMetricDescriptor, groupRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.SystemRelationGroupBy(ctx, system, p6metrics.GolemGeneratedMetricDescriptor, relationRequest); err != nil {
				t.Fatal(err)
			}
			if _, err := golemruntime.SystemScoped(ctx, system, p6metrics.GolemGeneratedMetricDescriptor, scopedRequest); err != nil {
				t.Fatal(err)
			}
			if err := golemruntime.CallerTransaction(ctx, caller, func(tx *golemruntime.CallerTx[p6metrics.Principal, p6metrics.Actor]) error {
				if _, err := golemruntime.CallerTxCount(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor); err != nil {
					return err
				}
				if _, err := golemruntime.CallerTxAggregate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest); err != nil {
					return err
				}
				if _, err := golemruntime.CallerTxGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, groupRequest); err != nil {
					return err
				}
				if _, err := golemruntime.CallerTxRelationGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, relationRequest); err != nil {
					return err
				}
				_, err := golemruntime.CallerTxScoped(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, scopedRequest)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := golemruntime.SystemTransaction(ctx, system, func(tx *golemruntime.SystemTx[p6metrics.Principal, p6metrics.Actor]) error {
				if _, err := golemruntime.SystemTxCount(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor); err != nil {
					return err
				}
				if _, err := golemruntime.SystemTxAggregate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest); err != nil {
					return err
				}
				if _, err := golemruntime.SystemTxGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, groupRequest); err != nil {
					return err
				}
				if _, err := golemruntime.SystemTxRelationGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, relationRequest); err != nil {
					return err
				}
				_, err := golemruntime.SystemTxScoped(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, scopedRequest)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for model := range hookCalls {
				for operation := range hookCalls[model] {
					for phase := range hookCalls[model][operation] {
						if got := hookCalls[model][operation][phase].Load(); got != 0 {
							t.Fatalf("Count/analytics/scoped invoked ordinary read hook model=%d operation=%d phase=%d calls=%d", model, operation, phase, got)
						}
					}
				}
			}
		})
	}
}
