package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func TestP6AnalyticsStatementCountIsOneAndNoContributionRowsAreDecoded(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
			if err != nil {
				t.Fatal(err)
			}

			// Four authorized source rows contribute. Caller.Aggregate accepts exactly
			// one decoded row, so a source-row fallback would fail this production call.
			count, minimum := p5social.Posts.CountAll(), p5social.Posts.Title.Min()
			harness.trace.reset()
			result, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(
				p5social.Posts.AggregateSelect(count, minimum),
			))
			if err != nil {
				t.Fatal(err)
			}
			if value, present := golem.AggregateValue(result, count).Get(); !present || value != 4 {
				t.Fatalf("aggregate count=%d present=%t", value, present)
			}
			if value, present := golem.AggregateValue(result, minimum).Get(); !present || value != "a-open" {
				t.Fatalf("aggregate minimum=%q present=%t", value, present)
			}

			statements := harness.trace.snapshot()
			if len(statements) != 1 {
				t.Fatalf("analytics executed %d statements, want exactly one: %#v", len(statements), statements)
			}
			sql := statements[0]
			upper := strings.ToUpper(sql)
			// Guarded analytics intentionally starts with bounded materialized
			// contribution/group CTEs. Inspect the outer decoder projection, not
			// the first SELECT inside the contribution CTE.
			selectAt := strings.LastIndex(upper, ") SELECT ")
			if selectAt < 0 {
				t.Fatalf("analytics statement has no outer guarded SELECT: %s", sql)
			}
			projectionStart := selectAt + len(") SELECT ")
			fromRelative := strings.Index(upper[projectionStart:], " FROM ")
			from := projectionStart + fromRelative
			if fromRelative < 0 {
				t.Fatalf("analytics statement has no FROM clause: %s", sql)
			}
			projection := upper[projectionStart:from]
			if !strings.Contains(upper, "COUNT(*)") || !strings.Contains(upper, "MIN(") {
				t.Fatalf("bounded statement does not compute the requested aggregates: %s", sql)
			}
			if !strings.Contains(projection, `"GOLEM_M0"`) || !strings.Contains(projection, `"GOLEM_M1"`) {
				t.Fatalf("decoder projection omits aggregate result aliases: %s", sql[projectionStart:from])
			}
			if strings.Contains(upper, " GROUP BY ") || strings.Contains(sql, ";") {
				t.Fatalf("un-grouped aggregate was not one indivisible aggregate statement: %s", sql)
			}
			if strings.Contains(projection, `"GOLEM_A0"."ID"`) || strings.Contains(projection, `"GOLEM_A0"."AUTHOR_ID"`) || strings.Contains(projection, `"GOLEM_CONTRIBUTIONS"."GOLEM_C0" AS`) {
				t.Fatalf("contributing source data leaked into decoder projection: %s", sql[projectionStart:from])
			}
		})
	}
}
