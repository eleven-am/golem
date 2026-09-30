package analytics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestAnalyticsRelationHopDisjunctivePolicyStaysInsideJoinOperand(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	rootEndpoint, present := fixture.Registry.ForwardToOneRelation(fixture.Post, fixture.Authorship)
	if !present {
		t.Fatal("post-author endpoint is absent")
	}
	rootWhere := analyticsRelationCondition(t, rootEndpoint, policyir.RelationToOne)
	targetWhere := analyticsStringAlternative(t, fixture.User, fixture.UserName, "visible", "also-visible")
	rootRead := analyticsSystemReadPlan(t, fixture.Registry, fixture.Post, rootWhere, fixture.AuthorID)
	targetRead := analyticsSystemReadPlan(t, fixture.Registry, fixture.User, targetWhere, fixture.UserID, fixture.UserName)
	dimension := golem.GeneratedRelationDimension[analyticsRendererPost, string](fixture.Post, "authorName", []golem.RelationID{fixture.Authorship}, fixture.UserName, true)
	count := golem.GeneratedCountAll[analyticsRendererPost](fixture.Post)
	frozen, err := golem.RuntimeFreezeRelationGroupRequest(golem.GeneratedRelationGroupBy(fixture.Post,
		golem.GeneratedRelationGroupDimensions[analyticsRendererPost](dimension),
		golem.GeneratedRelationGroupMeasures[analyticsRendererPost](count),
	))
	if err != nil {
		t.Fatal(err)
	}
	terminal, present := fixture.Registry.Field(fixture.User, fixture.UserName)
	if !present {
		t.Fatal("terminal relation field is absent")
	}
	planned := Plan{
		request:  frozen,
		read:     rootRead,
		fields:   map[golem.FieldID]schema.Field{fixture.UserName: terminal},
		relation: []RelationHop{{Endpoint: rootEndpoint, Authorized: targetRead}},
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			statement, err := Render(planned, fixture.Registry, provider, analyticsPlanMapProof(t, fixture.Registry, provider))
			if err != nil {
				t.Fatal(err)
			}
			sql := statement.SQL()
			joinAt := strings.Index(sql, " INNER JOIN ")
			whereAt := strings.Index(sql, " WHERE ")
			if joinAt < 0 || whereAt < joinAt {
				t.Fatalf("relation hop join is absent: %s", sql)
			}
			onAt := strings.Index(sql[joinAt:whereAt], " ON ")
			if onAt < 0 {
				t.Fatalf("relation hop has no ON clause: %s", sql)
			}
			assertEveryDisjunctionNested(t, sql[joinAt+onAt+len(" ON "):whereAt], 2)
		})
	}
}

func assertEveryDisjunctionNested(t *testing.T, fragment string, minimum int) {
	t.Helper()
	depth, found := 0, false
	for index := 0; index < len(fragment); index++ {
		switch fragment[index] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if strings.HasPrefix(fragment[index:], " OR ") {
			found = true
			if depth < minimum {
				t.Fatalf("disjunction escaped its operand at depth %d (want >= %d): %s", depth, minimum, fragment)
			}
		}
	}
	if !found {
		t.Fatalf("fragment has no disjunction to check: %s", fragment)
	}
}

func analyticsStringAlternative(t *testing.T, model golem.ModelID, field golem.FieldID, first, second string) policyir.Condition {
	t.Helper()
	typ, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	conditions := make([]policyir.Condition, 0, 2)
	for _, raw := range []string{first, second} {
		value, err := policyir.StringValue(raw)
		if err != nil {
			t.Fatal(err)
		}
		operand, err := policyir.OneOperand(value)
		if err != nil {
			t.Fatal(err)
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
		if err != nil {
			t.Fatal(err)
		}
		condition, err := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(field), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			t.Fatal(err)
		}
		conditions = append(conditions, condition)
	}
	alternative, err := policyir.NewLogical(policyir.ModelID(model), policyir.LogicalOr, conditions)
	if err != nil {
		t.Fatal(err)
	}
	return alternative
}
