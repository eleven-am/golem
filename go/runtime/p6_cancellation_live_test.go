package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestP6AnalyticsCancelledContextReturnsNoResultAfterOneStatementAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
			audits := &p5SocialAuditSink{}
			app, err := p6metrics.Open(context.Background(), p6metrics.Config[p6metrics.Principal]{
				Database:          harness.handle,
				AuditPrincipal:    func(p6metrics.Principal) string { return "p6-cancellation" },
				ReportScopedQuery: audits.report,
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
			system := app.System()
			aggregateState := func(result golem.AggregateResult[p6metrics.Metric], err error) (int, error) {
				if _, present := golem.AggregateValue(result, count).Get(); present {
					return 1, err
				}
				return 0, err
			}
			cases := []struct {
				name    string
				audited bool
				run     func(context.Context) (int, error)
			}{
				{name: "caller-aggregate", run: func(ctx context.Context) (int, error) {
					result, err := caller.Metrics.Aggregate(ctx, aggregateRequest)
					return aggregateState(result, err)
				}},
				{name: "caller-groupBy", run: func(ctx context.Context) (int, error) {
					rows, err := caller.Metrics.GroupBy(ctx, groupRequest)
					return len(rows), err
				}},
				{name: "caller-relationGroupBy", run: func(ctx context.Context) (int, error) {
					rows, err := caller.Metrics.RelationGroupBy(ctx, relationRequest)
					return len(rows), err
				}},
				{name: "caller-scoped", audited: true, run: func(ctx context.Context) (int, error) {
					rows, err := caller.Metrics.Scoped(ctx, scopedRequest)
					return len(rows), err
				}},
				{name: "system-aggregate", run: func(ctx context.Context) (int, error) {
					result, err := system.Metrics.Aggregate(ctx, aggregateRequest)
					return aggregateState(result, err)
				}},
				{name: "system-groupBy", run: func(ctx context.Context) (int, error) {
					rows, err := system.Metrics.GroupBy(ctx, groupRequest)
					return len(rows), err
				}},
				{name: "system-relationGroupBy", run: func(ctx context.Context) (int, error) {
					rows, err := system.Metrics.RelationGroupBy(ctx, relationRequest)
					return len(rows), err
				}},
				{name: "system-scoped", audited: true, run: func(ctx context.Context) (int, error) {
					rows, err := system.Metrics.Scoped(ctx, scopedRequest)
					return len(rows), err
				}},
				{name: "callerTx-aggregate", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = caller.Transaction(context.Background(), func(tx *p6metrics.CallerTx[p6metrics.Principal]) error {
						result, err := tx.Metrics.Aggregate(ctx, aggregateRequest)
						rows, _ = aggregateState(result, err)
						return err
					})
					return rows, resultErr
				}},
				{name: "callerTx-groupBy", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = caller.Transaction(context.Background(), func(tx *p6metrics.CallerTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.GroupBy(ctx, groupRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
				{name: "callerTx-relationGroupBy", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = caller.Transaction(context.Background(), func(tx *p6metrics.CallerTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.RelationGroupBy(ctx, relationRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
				{name: "callerTx-scoped", audited: true, run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = caller.Transaction(context.Background(), func(tx *p6metrics.CallerTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.Scoped(ctx, scopedRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
				{name: "systemTx-aggregate", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = system.Transaction(context.Background(), func(tx *p6metrics.SystemTx[p6metrics.Principal]) error {
						result, err := tx.Metrics.Aggregate(ctx, aggregateRequest)
						rows, _ = aggregateState(result, err)
						return err
					})
					return rows, resultErr
				}},
				{name: "systemTx-groupBy", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = system.Transaction(context.Background(), func(tx *p6metrics.SystemTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.GroupBy(ctx, groupRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
				{name: "systemTx-relationGroupBy", run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = system.Transaction(context.Background(), func(tx *p6metrics.SystemTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.RelationGroupBy(ctx, relationRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
				{name: "systemTx-scoped", audited: true, run: func(ctx context.Context) (rows int, resultErr error) {
					resultErr = system.Transaction(context.Background(), func(tx *p6metrics.SystemTx[p6metrics.Principal]) error {
						values, err := tx.Metrics.Scoped(ctx, scopedRequest)
						rows = len(values)
						return err
					})
					return rows, resultErr
				}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					audits.reset()
					harness.trace.reset()
					// Cancel after the driver boundary records the one analytics
					// statement but before it delegates to the provider.
					harness.trace.afterNextStatement(cancel)
					rows, err := test.run(ctx)
					if err == nil || !errors.Is(err, context.Canceled) || rows != 0 {
						t.Fatalf("cancelled operation rows=%d error=%v", rows, err)
					}
					if statements := harness.trace.snapshot(); len(statements) != 1 {
						t.Fatalf("cancelled operation statements=%d want one", len(statements))
					}
					records := audits.snapshot()
					if !test.audited {
						if len(records) != 0 {
							t.Fatalf("non-scoped cancellation produced %d scoped audit records", len(records))
						}
						return
					}
					if len(records) != 1 || records[0].Outcome() != golem.ScopedOutcomeCancelled || records[0].RowCount() != 0 {
						t.Fatalf("scoped cancellation audit=%v", records)
					}
				})
			}
		})
	}
}
