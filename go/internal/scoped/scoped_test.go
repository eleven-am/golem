package scoped

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type scopedSQLPost struct{}
type scopedSQLUser struct{}
type policyMap map[policyir.ModelID]policyir.Policy

func (values policyMap) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := values[model]
	return value, ok
}

func TestP6ScopedAuthorizedInnerAndLeftJoinOracle(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	authorRelation := golem.GeneratedToOne[scopedSQLPost, scopedSQLUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))

	for _, joinKind := range []string{"inner", "left"} {
		t.Run(joinKind, func(t *testing.T) {
			var author golem.Scope[scopedSQLUser]
			if joinKind == "inner" {
				author = golem.InnerJoin(posts, authorRelation)
			} else {
				author = golem.LeftJoin(posts, authorRelation)
			}
			name := golem.GeneratedScopedTextField(author, golem.GeneratedTextField[scopedSQLUser, string](fixture.UserName))
			frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Join(author).Select(title, name))
			if err != nil {
				t.Fatal(err)
			}
			policies := policyMap{policyir.ModelID(fixture.Post): readPolicy(t, fixture.Post, true), policyir.ModelID(fixture.User): alternativeStringReadPolicy(t, fixture.User, fixture.UserName, "visible", "also-visible")}
			planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			for occurrence, item := range planned.occurrences {
				if validateErr := item.authorized.Where().Validate(); validateErr != nil {
					t.Fatalf("occurrence %d invalid authorized where: %v", occurrence, validateErr)
				}
			}
			for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
				proof, proofErr := scopedProof(provider, fixture)
				if proofErr != nil {
					t.Fatal(proofErr)
				}
				statement, renderErr := Render(planned, fixture.Registry, provider, proof)
				if renderErr != nil {
					t.Fatal(renderErr)
				}
				sql := statement.SQL()
				keyword := " INNER JOIN "
				if joinKind == "left" {
					keyword = " LEFT JOIN "
				}
				joinAt, whereAt := strings.Index(sql, keyword), strings.Index(sql, " WHERE ")
				if joinAt < 0 || whereAt < joinAt || !strings.Contains(sql[joinAt:whereAt], "name") {
					t.Fatalf("%s target policy was not retained in ON before outer WHERE: %s", joinKind, sql)
				}
				assertJoinPolicyOrGrouped(t, sql[joinAt:whereAt])
			}
		})
	}
}

func assertJoinPolicyOrGrouped(t *testing.T, joinSQL string) {
	t.Helper()
	depth := 0
	for index := 0; index < len(joinSQL); index++ {
		switch joinSQL[index] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if strings.HasPrefix(joinSQL[index:], " OR ") {
			if depth < 2 {
				t.Fatalf("joined OR policy escaped correlation operand at depth %d: %s", depth, joinSQL)
			}
			return
		}
	}
	t.Fatalf("joined alternative policy has no OR: %s", joinSQL)
}

func TestP6ScopedJoinCorrelationKeysRequireDischargedClassification(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	author := golem.InnerJoin(posts, golem.GeneratedToOne[scopedSQLPost, scopedSQLUser](fixture.PostAuthor, fixture.Authorship, fixture.User))
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	name := golem.GeneratedScopedTextField(author, golem.GeneratedTextField[scopedSQLUser, string](fixture.UserName))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Join(author).Select(title, name))
	if err != nil {
		t.Fatal(err)
	}

	conditional := policyMap{
		policyir.ModelID(fixture.Post): conditionalFieldByStringReadPolicy(t, fixture.Post, fixture.AuthorID, fixture.PostTitle, "visible"),
		policyir.ModelID(fixture.User): readPolicy(t, fixture.User, true),
	}
	if planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), conditional, readplan.DefaultLimits()); err == nil || !strings.Contains(err.Error(), "P6_SCOPED_PLAN_CLASSIFICATION") {
		t.Fatalf("undischarged conditional join key error=%v classification=%#v", err, planned.occurrences[0].classified[fixture.AuthorID])
	}

	readable := policyMap{
		policyir.ModelID(fixture.Post): readPolicy(t, fixture.Post, true),
		policyir.ModelID(fixture.User): readPolicy(t, fixture.User, true),
	}
	planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), readable, readplan.DefaultLimits())
	if err != nil {
		t.Fatalf("always-readable join keys: %v", err)
	}
	for occurrence, field := range map[uint32]golem.FieldID{0: fixture.AuthorID, 1: fixture.UserID} {
		classified, present := planned.occurrences[occurrence].classified[field]
		if !present || classified.Conditional() {
			t.Fatalf("join key occurrence=%d field=%x classification=%#v present=%t", occurrence, field, classified, present)
		}
	}
}

func conditionalFieldByStringReadPolicy(t *testing.T, model golem.ModelID, guarded, conditionField golem.FieldID, expected string) policyir.Policy {
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
	requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{
		Node: policyir.ConditionScalar, FieldType: typ, Operand: operand,
		Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(conditionField), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
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
	denyRule, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectDeny, policyir.ModelID(model), &all, policyir.FieldID(guarded), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	fieldRule, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &condition, policyir.FieldID(guarded), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{modelRule, denyRule, fieldRule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func conditionalStringReadPolicy(t *testing.T, model golem.ModelID, field golem.FieldID, expected string) policyir.Policy {
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
	requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{
		Node:      policyir.ConditionScalar,
		FieldType: typ,
		Operand:   operand,
		Mode:      policyir.ComparisonSensitive,
		Providers: policyir.PortableProviders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(field), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &condition, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func alternativeStringReadPolicy(t *testing.T, model golem.ModelID, field golem.FieldID, first, second string) policyir.Policy {
	t.Helper()
	typ, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	conditions := make([]policyir.Condition, 0, 2)
	for _, raw := range []string{first, second} {
		value, valueErr := policyir.StringValue(raw)
		if valueErr != nil {
			t.Fatal(valueErr)
		}
		operand, operandErr := policyir.OneOperand(value)
		if operandErr != nil {
			t.Fatal(operandErr)
		}
		requirements, requirementsErr := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{
			Node:      policyir.ConditionScalar,
			FieldType: typ,
			Operand:   operand,
			Mode:      policyir.ComparisonSensitive,
			Providers: policyir.PortableProviders(),
		})
		if requirementsErr != nil {
			t.Fatal(requirementsErr)
		}
		condition, conditionErr := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(field), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if conditionErr != nil {
			t.Fatal(conditionErr)
		}
		conditions = append(conditions, condition)
	}
	alternative, err := policyir.NewLogical(policyir.ModelID(model), policyir.LogicalOr, conditions)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &alternative, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestP6ScopedToManyJoinCountsAuthorizedPairsWithoutImplicitDeduplication(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	users := golem.GeneratedScope[scopedSQLUser](fixture.User)
	posts := golem.InnerJoin(users, golem.GeneratedToMany[scopedSQLUser, scopedSQLPost](fixture.UserPosts, fixture.Authorship, fixture.Post))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(users).Join(posts).Select(users.Count()))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := scopedProof(policyir.ProviderSQLite, fixture)
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement.SQL(), "COUNT(*)") || strings.Contains(strings.ToUpper(statement.SQL()), "DISTINCT") {
		t.Fatalf("to-many pair count was deduplicated or lost: %s", statement.SQL())
	}
}

func readPolicy(t *testing.T, model golem.ModelID, truth bool) policyir.Policy {
	t.Helper()
	condition, err := policyir.NewConstant(policyir.ModelID(model), truth)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &condition, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
func scopedProof(provider policyir.Provider, fixture schematest.Fixture) (policysql.CapabilityProof, error) {
	return policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
}

func TestP6ScopedProviderSQLIsDeterministic(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Where(title.Contains("golem")).Select(title).Take(3))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			proof, _ := scopedProof(provider, fixture)
			first, err := Render(planned, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Render(planned, fixture.Registry, provider, proof)
			if err != nil || first.SQL() != second.SQL() {
				t.Fatalf("nondeterministic scoped SQL: %v", err)
			}
		})
	}
}

func TestP8ScopedRebasesNumberedPlaceholdersAcrossProviders(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		provider          policyir.Provider
	}{
		{name: "sqlite", provider: policyir.ProviderSQLite, input: "x=?1 AND y=?12", want: "x=?4 AND y=?15"},
		{name: "postgresql", provider: policyir.ProviderPostgreSQL, input: "x=$1 AND y=$12", want: "x=$4 AND y=$15"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rebase(test.input, 3, test.provider); got != test.want {
				t.Fatalf("rebase=%q want=%q", got, test.want)
			}
		})
	}
}

func TestP6ScopedClassificationPositionSpy(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Where(title.Eq("visible")).GroupBy(title).Select(title, posts.Count()).OrderBy(title.Asc()))
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{policyir.ModelID(fixture.Post): conditionalStringFieldReadPolicy(t, fixture.Post, fixture.PostTitle, "visible")}
	planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	field, present := planned.occurrences[0].classified[fixture.PostTitle]
	if !present {
		t.Fatal("where/dimension/selection/order field was not classified")
	}
	if !field.Conditional() || !field.DischargedByConstraint() {
		t.Fatalf("classification=%#v", field)
	}
}

func conditionalStringFieldReadPolicy(t *testing.T, model golem.ModelID, field golem.FieldID, expected string) policyir.Policy {
	t.Helper()
	conditional := conditionalStringReadPolicy(t, model, field, expected)
	rules := conditional.Rules()
	condition, present := rules[0].Condition()
	if !present {
		t.Fatal("conditional helper lost condition")
	}
	all, err := policyir.NewConstant(policyir.ModelID(model), true)
	if err != nil {
		t.Fatal(err)
	}
	modelRule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &all, 0)
	if err != nil {
		t.Fatal(err)
	}
	fieldRule, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(model), &condition, policyir.FieldID(field), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := policyir.NewPolicy(policyir.ModelID(model), []policyir.Rule{modelRule, fieldRule})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestP6ScopedExactNumericHavingOrderAndLimitGuards(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	big := golem.GeneratedScopedIntegerField(posts, golem.GeneratedOrderedField[scopedSQLPost, int64](fixture.PostBigInt))
	total := big.Sum()
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).GroupBy(big).Having(total.GT(golem.MustParseExactInteger("2"))).Select(big, total).OrderBy(total.Asc()))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, _ := scopedProof(provider, fixture)
		statement, renderErr := Render(planned, fixture.Registry, provider, proof, RenderOptions{MaxContributionRows: 10, MaxIntermediateGroups: 5, MaxResultRows: 3})
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		sql := statement.SQL()
		if provider == policyir.ProviderSQLite {
			if !strings.Contains(sql, "COLLATE "+"golem_analytics_numeric_v1") {
				t.Fatalf("SQLite exact numeric comparison/order is lexical: %s", sql)
			}
		} else if !strings.Contains(sql, "CAST(") || !strings.Contains(sql, " AS NUMERIC)") {
			t.Fatalf("PostgreSQL exact numeric comparison/order is lexical: %s", sql)
		}
		if !statement.Guarded() || !statement.HasGuardRow() || statement.ScanColumnCount() != len(statement.Columns())+3 || !strings.Contains(sql, "golem_contribution_count") || !strings.Contains(sql, "golem_intermediate_count") {
			t.Fatalf("scoped limits are not guarded: %s", sql)
		}
		for _, fragment := range []string{"golem_scoped_contributions", "golem_limited_scoped_contributions", "golem_limited_scoped_groups", "LIMIT"} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("scoped guard does not bound source/group work before aggregation; missing %q: %s", fragment, sql)
			}
		}
		if detail := statement.LimitOverflow(11, 1, 1); !strings.Contains(detail, "contribution") {
			t.Fatalf("contribution detail=%q", detail)
		}
		if detail := statement.LimitOverflow(1, 6, 1); !strings.Contains(detail, "intermediate") {
			t.Fatalf("intermediate detail=%q", detail)
		}
		if detail := statement.LimitOverflow(1, 1, 4); !strings.Contains(detail, "result") {
			t.Fatalf("result detail=%q", detail)
		}
	}
}
