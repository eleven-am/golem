package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

type p10ScopedRefusalGate struct {
	armed   atomic.Bool
	arrived chan struct{}
	hold    func()
}

func (gate *p10ScopedRefusalGate) report(_ context.Context, record golem.ScopedAuditRecord) {
	if record.Outcome() != golem.ScopedOutcomeRefused || !gate.armed.CompareAndSwap(true, false) {
		return
	}
	close(gate.arrived)
	gate.hold()
}

func openP10ScopedRefusalApp(t *testing.T, profile p5ExtensionProviderProfile, gate *p10ScopedRefusalGate) *golemruntime.App[p6metrics.Principal, p6metrics.Actor] {
	t.Helper()
	harness := newP6MetricsHarness(t, profile, golemruntime.AnalyticsLimits{})
	bundle := p6metrics.GolemGeneratedSchemaBundle()
	metricModel := p6metrics.GolemGeneratedMetricDescriptor.Metadata().ModelID()
	categoryModel := p6metrics.GolemGeneratedCategoryDescriptor.Metadata().ModelID()
	metricPolicy := golem.GeneratedPolicyBinding[p6metrics.Actor, p6metrics.Metric](metricModel, func(p6metrics.Actor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p6metrics.Metric]()
		rules.CanRead(golem.All[p6metrics.Metric]())
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
		AuditPrincipal:    func(p6metrics.Principal) string { return "scoped-refusal" },
		ReportScopedQuery: gate.report,
		ResolvePrincipal: func(_ context.Context, principal p6metrics.Principal) (p6metrics.Actor, error) {
			return p6metrics.Actor{CategoryPrefix: principal.CategoryPrefix}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestRefusedScopedQueryIsOneAdmittedOperationAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			returned := make(chan struct{})
			gate := &p10ScopedRefusalGate{arrived: make(chan struct{})}
			gate.hold = func() {
				p10AwaitCondition(func() bool { return p10Closed(returned) || p10UsageCloseIsWaiting() })
			}
			app := openP10ScopedRefusalApp(t, profile, gate)
			gate.armed.Store(true)
			refusal := make(chan error, 1)
			finishedFirst := make(chan bool, 1)
			var overlapErr error
			var err error
			p10FinishesWithin(t, 20e9, func() {
				err = golemruntime.SystemTransaction(context.Background(), app.System(), func(tx *golemruntime.SystemTx[p6metrics.Principal, p6metrics.Actor]) error {
					go func() {
						_, scopedErr := golemruntime.SystemTxScoped(context.Background(), tx, p6metrics.GolemGeneratedMetricDescriptor, golem.ScopedQuery[p6metrics.Metric]{})
						finishedFirst <- !p10Closed(returned)
						refusal <- scopedErr
					}()
					select {
					case <-gate.arrived:
					case scopedErr := <-refusal:
						refusal <- scopedErr
						t.Errorf("the refused scoped query never reported its refusal: %v", scopedErr)
						return nil
					}
					_, overlapErr = golemruntime.SystemTxCount(context.Background(), tx, p6metrics.GolemGeneratedMetricDescriptor)
					return nil
				})
				close(returned)
			})
			if err != nil {
				t.Fatalf("transaction = %v", err)
			}
			if !p10ConcurrentUse(overlapErr) {
				t.Fatalf("call overlapping a refused scoped query = %v, want the concurrent-use error", overlapErr)
			}
			if !<-finishedFirst {
				t.Fatal("the transaction finished while a refused scoped query was still in flight")
			}
			if scopedErr := <-refusal; scopedErr == nil {
				t.Fatal("an invalid scoped query was not refused")
			}
		})
	}
}
