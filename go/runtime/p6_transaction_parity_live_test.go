package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestAnalyticsTransactionFamiliesBindToTxAndRollbackAcrossProviders(t *testing.T) {
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
			metricPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Metric](metricModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Metric]()
				rules.CanRead(golem.All[p6metrics.Metric]())
				rules.CanCreate(golem.All[p6metrics.Metric]())
				return rules.Freeze(metricModel)
			})
			categoryPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Category](categoryModel, func(actor p6metrics.Actor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Category]()
				p6metrics.Category{}.DefinePolicy(rules, actor)
				return rules.Freeze(categoryModel)
			})
			bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(
				bundle.GenerationDigest(), []golem.PolicyBinding[p6metrics.Actor]{metricPolicy, categoryPolicy}, nil,
			))
			if err != nil {
				t.Fatal(err)
			}
			descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
			if err != nil {
				t.Fatal(err)
			}
			app, err := golemruntime.Open(context.Background(), golemruntime.Config[p6metrics.Principal, p6metrics.Actor]{
				Database: harness.handle, Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
				AuditPrincipal:    func(p6metrics.Principal) string { return "tx-parity" },
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
			system := app.System()
			ctx := context.Background()
			count := p6metrics.Metrics.CountAll()
			label := p6metrics.Metrics.Label.Dimension()
			relationDimension := p6metrics.Metrics.CategoryParentName
			aggregateRequest := p6metrics.Metrics.Aggregate(p6metrics.Metrics.AggregateSelect(count))
			groupRequest := p6metrics.Metrics.GroupBy(p6metrics.Metrics.GroupDimensions(label), p6metrics.Metrics.GroupMeasures(count))
			relationRequest := p6metrics.Metrics.RelationGroupBy(p6metrics.Metrics.RelationGroupDimensions(relationDimension), p6metrics.Metrics.RelationGroupMeasures(count))

			var categoryText string
			categoryQuery := `SELECT "id" FROM ` + harness.table("p6_categories") + ` WHERE "name" = ?`
			if err := harness.database.GetContext(ctx, &categoryText, harness.database.Rebind(categoryQuery), "public-child"); err != nil {
				t.Fatal(err)
			}
			categoryID, err := golem.ParseUUID(categoryText)
			if err != nil {
				t.Fatal(err)
			}

			assertAggregate := func(t *testing.T, result golem.AggregateResult[p6metrics.Metric], want int64) {
				t.Helper()
				value, present := golem.AggregateValue(result, count).Get()
				if !present || value != want {
					t.Fatalf("aggregate count=%d/%t want=%d", value, present, want)
				}
			}
			assertGroupContains := func(t *testing.T, rows []golem.GroupRow[p6metrics.Metric], want string, present bool) {
				t.Helper()
				found := false
				for _, row := range rows {
					value, ok := golem.GroupValue(row, label).Get()
					found = found || ok && value == want
				}
				if found != present {
					t.Fatalf("group label %q found=%t want=%t", want, found, present)
				}
			}
			assertRelationCount := func(t *testing.T, rows []golem.RelationGroupRow[p6metrics.Metric], want int64) {
				t.Helper()
				for _, row := range rows {
					name, nameOK := golem.RelationGroupValue(row, relationDimension).Get()
					value, valueOK := golem.RelationGroupValue(row, count).Get()
					if nameOK && valueOK && name == "public-root" {
						if value != want {
							t.Fatalf("public relation count=%d want=%d", value, want)
						}
						return
					}
				}
				t.Fatal("public relation group is absent")
			}
			assertOutside := func(t *testing.T, labelValue string) {
				t.Helper()
				result, err := golemruntime.CallerAggregate(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest)
				if err != nil {
					t.Fatal(err)
				}
				assertAggregate(t, result, 6)
				groups, err := golemruntime.CallerGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, groupRequest)
				if err != nil {
					t.Fatal(err)
				}
				assertGroupContains(t, groups, labelValue, false)
				relations, err := golemruntime.CallerRelationGroupBy(ctx, caller, p6metrics.GolemGeneratedMetricDescriptor, relationRequest)
				if err != nil {
					t.Fatal(err)
				}
				assertRelationCount(t, relations, 2)
			}

			type txCase struct {
				name string
				run  func(string, golem.UUID) error
			}
			cases := []txCase{
				{name: "caller", run: func(labelValue string, id golem.UUID) error {
					return golemruntime.CallerTransaction(ctx, caller, func(tx *golemruntime.CallerTx[p6metrics.Principal, p6metrics.Actor]) error {
						if _, err := golemruntime.CallerTxCreate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, p6MetricTxInput(t, id, categoryID, labelValue)); err != nil {
							return err
						}
						result, err := golemruntime.CallerTxAggregate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest)
						if err != nil {
							return err
						}
						assertAggregate(t, result, 7)
						groups, err := golemruntime.CallerTxGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, groupRequest)
						if err != nil {
							return err
						}
						assertGroupContains(t, groups, labelValue, true)
						relations, err := golemruntime.CallerTxRelationGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, relationRequest)
						if err != nil {
							return err
						}
						assertRelationCount(t, relations, 3)
						root := p6metrics.Metrics.Scope()
						scopedLabel := p6metrics.Metrics.Label.At(root)
						rows, err := golemruntime.CallerTxScoped(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, golem.From(root).Where(scopedLabel.Eq(labelValue)).Select(scopedLabel))
						if err != nil || len(rows) != 1 {
							return fmt.Errorf("caller tx scoped rows=%d: %w", len(rows), err)
						}
						return errors.New("rollback caller analytics fixture")
					})
				}},
				{name: "system", run: func(labelValue string, id golem.UUID) error {
					return golemruntime.SystemTransaction(ctx, system, func(tx *golemruntime.SystemTx[p6metrics.Principal, p6metrics.Actor]) error {
						if _, err := golemruntime.SystemTxCreate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, p6MetricTxInput(t, id, categoryID, labelValue)); err != nil {
							return err
						}
						result, err := golemruntime.SystemTxAggregate(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, aggregateRequest)
						if err != nil {
							return err
						}
						assertAggregate(t, result, 7)
						groups, err := golemruntime.SystemTxGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, groupRequest)
						if err != nil {
							return err
						}
						assertGroupContains(t, groups, labelValue, true)
						relations, err := golemruntime.SystemTxRelationGroupBy(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, relationRequest)
						if err != nil {
							return err
						}
						assertRelationCount(t, relations, 4)
						root := p6metrics.Metrics.Scope()
						scopedLabel := p6metrics.Metrics.Label.At(root)
						rows, err := golemruntime.SystemTxScoped(ctx, tx, p6metrics.GolemGeneratedMetricDescriptor, golem.From(root).Where(scopedLabel.Eq(labelValue)).Select(scopedLabel))
						if err != nil || len(rows) != 1 {
							return fmt.Errorf("system tx scoped rows=%d: %w", len(rows), err)
						}
						return errors.New("rollback system analytics fixture")
					})
				}},
			}
			for index, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					labelValue := "tx-" + test.name
					assertOutside(t, labelValue)
					err := test.run(labelValue, golem.UUID{12: 9, 14: byte(index + 1), 15: 99})
					if err == nil || err.Error() != "rollback "+test.name+" analytics fixture" {
						t.Fatalf("rollback error=%v", err)
					}
					assertOutside(t, labelValue)
				})
			}
		})
	}
}

func p6MetricTxInput(t *testing.T, id, categoryID golem.UUID, label string) p6metrics.MetricCreateInput {
	t.Helper()
	return p6metrics.Metrics.Create(
		p6metrics.Metrics.ID.Create(id),
		p6metrics.Metrics.Flag.Create(true),
		p6metrics.Metrics.Small.Create(1),
		p6metrics.Metrics.Integer.Create(1),
		p6metrics.Metrics.Big.Create(1),
		p6metrics.Metrics.Float.Create(1),
		p6metrics.Metrics.Double.Create(1),
		p6metrics.Metrics.Amount.Create(p6MustDecimal(t, "1.0000")),
		p6metrics.Metrics.Label.Create(label),
		p6metrics.Metrics.Reference.Create(golem.UUID{15: 44}),
		p6metrics.Metrics.Day.Create(p6MustDate(t, "2026-08-07")),
		p6metrics.Metrics.Clock.Create(p6MustTime(t, "12:34:56")),
		p6metrics.Metrics.OccurredAt.Create(time.Date(2026, 8, 7, 12, 34, 56, 0, time.UTC)),
		p6metrics.Metrics.State.Create(p6metrics.StatusAlpha),
		p6metrics.Metrics.OptionalBig.CreateNull(),
		p6metrics.Metrics.OptionalAmount.CreateNull(),
		p6metrics.Metrics.OptionalLabel.CreateNull(),
		p6metrics.Metrics.OptionalDay.CreateNull(),
		p6metrics.Metrics.OptionalClock.CreateNull(),
		p6metrics.Metrics.OptionalInstant.CreateNull(),
		p6metrics.Metrics.CategoryID.Create(categoryID),
	)
}
