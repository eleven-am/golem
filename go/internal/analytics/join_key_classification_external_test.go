package analytics_test

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	analytics "github.com/eleven-am/golem/go/internal/analytics"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

type analyticsJoinPolicyMap map[policyir.ModelID]policyir.Policy

func (values analyticsJoinPolicyMap) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := values[model]
	return value, ok
}

func TestRelationJoinCorrelationKeysRequireDischargedClassificationBeforeSQL(t *testing.T) {
	registry, err := schema.New(p5social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatal(err)
	}
	request, err := golem.RuntimeFreezeRelationGroupRequest(p5social.Posts.RelationGroupBy(
		p5social.Posts.RelationGroupDimensions(p5social.Posts.AuthorName),
		p5social.Posts.RelationGroupMeasures(p5social.Posts.CountAll()),
	))
	if err != nil {
		t.Fatal(err)
	}
	dimension := request.Dimensions()[0]
	endpoint, present := registry.ForwardToOneRelation(request.ModelID(), dimension.RelationPath[0])
	if !present || len(endpoint.Correlation()) != 1 {
		t.Fatal("generated relation correlation is absent")
	}
	rootKey := endpoint.Correlation()[0].ParentFieldID()
	rootTitle := fieldByGraphQLName(t, registry, request.ModelID(), "title")

	policies := analyticsJoinPolicyMap{
		policyir.ModelID(request.ModelID()):        conditionalAnalyticsJoinFieldPolicy(t, request.ModelID(), rootKey, rootTitle, "a-open"),
		policyir.ModelID(endpoint.TargetModelID()): analyticsReadAllPolicy(t, endpoint.TargetModelID()),
	}
	if _, err := analytics.Caller(request, registry, policyir.PortableProviders(), policies, readplan.DefaultLimits()); err == nil || !strings.Contains(err.Error(), "conditional") {
		t.Fatalf("undischarged root join key reached SQL planning: %v", err)
	}

	policies[policyir.ModelID(request.ModelID())] = analyticsReadAllPolicy(t, request.ModelID())
	planned, err := analytics.Caller(request, registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatalf("always-readable join keys: %v", err)
	}
	rootClassified := false
	for _, field := range planned.AuthorizedRead().Fields() {
		if golem.FieldID(field.FieldID()) == rootKey {
			rootClassified = true
		}
	}
	if !rootClassified {
		t.Fatal("root correlation key is absent from authorized classification")
	}
	hops := planned.RelationPath()
	targetKey := endpoint.Correlation()[0].ChildFieldID()
	targetClassified := false
	for _, field := range hops[0].Authorized.Fields() {
		if golem.FieldID(field.FieldID()) == targetKey {
			targetClassified = true
		}
	}
	if !targetClassified {
		t.Fatal("target correlation key is absent from authorized classification")
	}
}

func fieldByGraphQLName(t *testing.T, registry *schema.Registry, model golem.ModelID, name string) golem.FieldID {
	t.Helper()
	descriptor, present := registry.Model(model)
	if !present {
		t.Fatal("model is absent")
	}
	for _, id := range descriptor.Fields() {
		field, _ := registry.Field(model, id)
		if field.GraphQLName() == name {
			return id
		}
	}
	t.Fatalf("field %q is absent", name)
	return golem.FieldID{}
}

func analyticsReadAllPolicy(t *testing.T, model golem.ModelID) policyir.Policy {
	t.Helper()
	all, err := policyir.NewConstant(policyir.ModelID(model), true)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &all, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func conditionalAnalyticsJoinFieldPolicy(t *testing.T, model golem.ModelID, guarded, conditionField golem.FieldID, expected string) policyir.Policy {
	t.Helper()
	typ, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, err := policyir.StringValue(expected)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(conditionField), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	all, err := policyir.NewConstant(policyir.ModelID(model), true)
	if err != nil {
		t.Fatal(err)
	}
	modelRule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &all, 0)
	if err != nil {
		t.Fatal(err)
	}
	deny, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectDeny, policyir.ModelID(model), &all, policyir.FieldID(guarded), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &condition, policyir.FieldID(guarded), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{modelRule, deny, grant})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
