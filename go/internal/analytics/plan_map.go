package analytics

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// analyticsAliasMatcher is the deliberately narrow handoff from the renderer
// to a later provider-plan sanitizer. It supports equality only; an alias
// cannot be extracted and reused to construct SQL or exposed as a name.
type analyticsAliasMatcher interface {
	Matches(string) bool
}

type analyticsRendererAlias struct{ value string }

func (alias analyticsRendererAlias) Matches(candidate string) bool {
	return alias.value != "" && candidate == alias.value
}

// AnalyticsPlanAliasRole classifies only the renderer-owned alias, not a
// provider plan node. A sanitizer must combine this closed provenance with the
// provider node kind and may never turn a derived alias into physical access.
type AnalyticsPlanAliasRole uint8

const (
	AnalyticsPlanAliasPhysicalAccess AnalyticsPlanAliasRole = iota + 1
	AnalyticsPlanAliasCorrelatedRelation
	AnalyticsPlanAliasAggregate
	AnalyticsPlanAliasMaterialize
	AnalyticsPlanAliasStructural
)

func validAnalyticsPlanAliasRole(role AnalyticsPlanAliasRole) bool {
	return role >= AnalyticsPlanAliasPhysicalAccess && role <= AnalyticsPlanAliasStructural
}

// AnalyticsPlanAliasFact is one renderer-owned identity for an alias that a
// provider plan can name. It retains only an opaque matcher and stable schema
// identities. It never retains SQL, binds, predicates, actor data, or physical
// table, column, index, and schema names.
type AnalyticsPlanAliasFact struct {
	matcher    analyticsAliasMatcher
	modelID    policyir.ModelID
	relationID policyir.RelationID
	fieldIDs   []policyir.FieldID
	role       AnalyticsPlanAliasRole
}

func newAnalyticsPlanAliasFact(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role AnalyticsPlanAliasRole) AnalyticsPlanAliasFact {
	return AnalyticsPlanAliasFact{
		matcher:    analyticsRendererAlias{value: alias},
		modelID:    model,
		relationID: relation,
		fieldIDs:   append([]policyir.FieldID(nil), fields...),
		role:       role,
	}
}

func newAnalyticsPolicyAliasFact(fact policysql.PolicyRelationAliasFact) AnalyticsPlanAliasFact {
	return AnalyticsPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: AnalyticsPlanAliasCorrelatedRelation}
}

// Matches compares an untrusted provider-plan alias with the renderer-owned
// token. Zero and incomplete facts always fail closed.
func (fact AnalyticsPlanAliasFact) Matches(candidate string) bool {
	return candidate != "" && fact.matcher != nil && fact.modelID != (policyir.ModelID{}) && validAnalyticsPlanAliasRole(fact.role) && fact.matcher.Matches(candidate)
}

func (fact AnalyticsPlanAliasFact) ModelID() policyir.ModelID    { return fact.modelID }
func (fact AnalyticsPlanAliasFact) Role() AnalyticsPlanAliasRole { return fact.role }

func (fact AnalyticsPlanAliasFact) RelationID() (policyir.RelationID, bool) {
	return fact.relationID, fact.relationID != (policyir.RelationID{})
}

func (fact AnalyticsPlanAliasFact) FieldIDs() []policyir.FieldID {
	return append([]policyir.FieldID(nil), fact.fieldIDs...)
}

func cloneAnalyticsPlanAliasFact(fact AnalyticsPlanAliasFact) AnalyticsPlanAliasFact {
	fact.fieldIDs = fact.FieldIDs()
	return fact
}

// AnalyticsPlanMap is the immutable alias identity map for one exact rendered
// analytics statement. Every returned slice is caller-owned. A missing match
// remains missing; the map never guesses from an alias naming convention.
type AnalyticsPlanMap struct {
	aliases []AnalyticsPlanAliasFact
}

func (plan AnalyticsPlanMap) AliasFacts() []AnalyticsPlanAliasFact {
	result := make([]AnalyticsPlanAliasFact, len(plan.aliases))
	for index, fact := range plan.aliases {
		result[index] = cloneAnalyticsPlanAliasFact(fact)
	}
	return result
}

func (plan AnalyticsPlanMap) MatchingAliasFacts(candidate string) []AnalyticsPlanAliasFact {
	if candidate == "" {
		return nil
	}
	result := make([]AnalyticsPlanAliasFact, 0, 1)
	for _, fact := range plan.aliases {
		if fact.Matches(candidate) {
			result = append(result, cloneAnalyticsPlanAliasFact(fact))
		}
	}
	return result
}

func (plan AnalyticsPlanMap) clone() AnalyticsPlanMap {
	return AnalyticsPlanMap{aliases: plan.AliasFacts()}
}

type analyticsPlanMapBuilder struct {
	aliases       []AnalyticsPlanAliasFact
	owned         map[string]struct{}
	policyAliases []policysql.PolicyRelationAliasFact
}

func (builder *analyticsPlanMapBuilder) add(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role AnalyticsPlanAliasRole) error {
	if alias == "" || model == (policyir.ModelID{}) || !validAnalyticsPlanAliasRole(role) {
		return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: renderer alias identity is incomplete")
	}
	if builder.owned == nil {
		builder.owned = map[string]struct{}{}
	}
	if _, duplicate := builder.owned[alias]; duplicate {
		return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: renderer alias identity is ambiguous")
	}
	for _, policy := range builder.policyAliases {
		if policy.Matches(alias) {
			return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: renderer alias identity is ambiguous")
		}
	}
	fact := newAnalyticsPlanAliasFact(alias, model, relation, fields, role)
	if !fact.Matches(alias) {
		return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: renderer alias identity is incomplete")
	}
	builder.owned[alias] = struct{}{}
	builder.aliases = append(builder.aliases, fact)
	return nil
}

func (builder *analyticsPlanMapBuilder) mergePolicy(facts []policysql.PolicyRelationAliasFact) error {
	for _, fact := range facts {
		if fact.ModelID() == (policyir.ModelID{}) || fact.RelationID() == (policyir.RelationID{}) {
			return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: policy alias identity is incomplete")
		}
		for alias := range builder.owned {
			if fact.Matches(alias) {
				return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: renderer alias identity is ambiguous")
			}
		}
		for _, existing := range builder.policyAliases {
			if fact == existing {
				return fmt.Errorf("P6_ANALYTICS_PLAN_MAP: policy alias identity is ambiguous")
			}
		}
		builder.policyAliases = append(builder.policyAliases, fact)
		builder.aliases = append(builder.aliases, newAnalyticsPolicyAliasFact(fact))
	}
	return nil
}

func (builder *analyticsPlanMapBuilder) freeze() AnalyticsPlanMap {
	return AnalyticsPlanMap{aliases: AnalyticsPlanMap{aliases: builder.aliases}.AliasFacts()}
}

func analyticsAuthorizedPlanFieldIDs(plan readplan.Plan) []policyir.FieldID {
	fields := plan.Fields()
	result := make([]policyir.FieldID, len(fields))
	for index, field := range fields {
		result[index] = field.FieldID()
	}
	return result
}

func analyticsResultPlanFieldIDs(plan Plan) []policyir.FieldID {
	seen := map[policyir.FieldID]struct{}{}
	result := make([]policyir.FieldID, 0)
	add := func(termField policyir.FieldID) {
		if termField == (policyir.FieldID{}) {
			return
		}
		if _, duplicate := seen[termField]; duplicate {
			return
		}
		seen[termField] = struct{}{}
		result = append(result, termField)
	}
	request := plan.Request()
	for _, term := range request.Dimensions() {
		add(policyir.FieldID(term.Field))
	}
	for _, term := range request.Measures() {
		add(policyir.FieldID(term.Field))
	}
	if having, present := request.Having(); present {
		analyticsHavingFieldIDs(having, add)
	}
	for _, order := range request.OrderBy() {
		add(policyir.FieldID(order.Term.Field))
	}
	return result
}

func analyticsHavingFieldIDs(value golem.FrozenGroupPredicate, add func(policyir.FieldID)) {
	add(policyir.FieldID(value.Term.Field))
	for _, child := range value.Children {
		analyticsHavingFieldIDs(child, add)
	}
}
