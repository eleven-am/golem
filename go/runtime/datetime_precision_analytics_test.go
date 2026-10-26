package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestAnalyticsPrecisionRefusalIsPublicBadUserInputNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	limits, err := normalizeAnalyticsLimits(AnalyticsLimits{})
	if err != nil {
		t.Fatal(err)
	}
	readLimits, err := normalizeReadLimits(ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	app := &App[struct{}, struct{}]{registry: fixture.Registry, providers: policyir.PortableProviders(), analyticsLimits: limits, readLimits: readLimits}
	stamps := golem.GeneratedNullableListField[parityPost, time.Time](fixture.PostStamps)
	where := stamps.Has(time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC))
	count := golem.GeneratedCountAll[parityPost](fixture.Post)
	title := golem.GeneratedDimension[parityPost](fixture.Post, golem.GeneratedTextField[parityPost, string](fixture.PostTitle), true)
	aggregate, err := golem.RuntimeFreezeAggregateRequest(golem.GeneratedAggregate(fixture.Post, golem.GeneratedAggregateWhere(where), golem.GeneratedAggregateSelect[parityPost](count)))
	if err != nil {
		t.Fatal(err)
	}
	group, err := golem.RuntimeFreezeGroupRequest(golem.GeneratedGroupBy(fixture.Post, golem.GeneratedGroupDimensions[parityPost](title), golem.GeneratedGroupMeasures[parityPost](count), golem.GeneratedGroupWhere(where)))
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]golem.FrozenAnalyticsRequest{"aggregate": aggregate, "groupBy": group} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareAnalyticsStatement(app, nil, true, fixture.Post, request, true)
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Reason != golem.ReasonValuePrecision || failure.Field != fixture.PostStamps || failure.Message != "value for Post.Stamps exceeds the declared precision" {
				t.Fatalf("analytics precision refusal=%#v", err)
			}
		})
	}
}
