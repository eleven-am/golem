package runtime_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func TestP6ScopedAggregateAndGroupProviderOracle(t *testing.T) {
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
			metrics := p6metrics.Metrics.Scope()
			big := p6metrics.Metrics.Big.At(metrics)
			amount := p6metrics.Metrics.Amount.At(metrics)
			label := p6metrics.Metrics.Label.At(metrics)
			bigSum, amountSum, amountAvg := big.Sum(), amount.Sum(), amount.Avg()
			amountMin, amountMax := amount.Min(), amount.Max()
			h.trace.reset()
			aggregate, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).
				Having(bigSum.GT(golem.NewExactInteger(10))).
				Select(bigSum, amountSum, amountAvg, amountMin, amountMax).
				OrderBy(bigSum.Asc()))
			if err != nil || len(aggregate) != 1 {
				t.Fatalf("aggregate rows=%d err=%v", len(aggregate), err)
			}
			if value, ok := golem.ScopedValue(aggregate[0], bigSum).Get(); !ok || value.String() != "18446744073709551612" {
				t.Fatalf("big sum=%q/%v", value.String(), ok)
			}
			if value, ok := golem.ScopedValue(aggregate[0], amountSum).Get(); !ok || value.String() != "199999999999999.9998" {
				t.Fatalf("amount sum=%q/%v", value.String(), ok)
			}
			if value, ok := golem.ScopedValue(aggregate[0], amountAvg).Get(); !ok || value.String() != "33333333333333.3333" {
				t.Fatalf("amount avg=%q/%v", value.String(), ok)
			}
			if value, ok := golem.ScopedValue(aggregate[0], amountMin).Get(); !ok || value.String() != "-1" {
				t.Fatalf("amount min=%q/%v", value.String(), ok)
			}
			if value, ok := golem.ScopedValue(aggregate[0], amountMax).Get(); !ok || value.String() != "99999999999999.9999" {
				t.Fatalf("amount max=%q/%v", value.String(), ok)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("aggregate statements=%d", len(statements))
			}

			h.trace.reset()
			groups, err := caller.Metrics.Scoped(context.Background(), golem.From(metrics).
				GroupBy(label).Select(label, bigSum).OrderBy(bigSum.Asc(), label.Asc()))
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(groups))
			for index, row := range groups {
				value, ok := golem.ScopedValue(row, label).Get()
				if !ok {
					t.Fatalf("group label[%d] is null", index)
				}
				got[index] = value
			}
			want := []string{"z", "a", "e\u0301", "é", "A", "Z"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("exact numeric/binary group order=%q want=%q", got, want)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("group statements=%d", len(statements))
			}
		})
	}
}
