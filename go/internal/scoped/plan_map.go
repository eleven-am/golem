package scoped

import (
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// scopedAliasMatcher is the deliberately narrow renderer-to-sanitizer
// boundary. It supports equality only; callers cannot recover an alias for SQL
// construction or expose it as a provider object name.
type scopedAliasMatcher interface {
	Matches(string) bool
}

type scopedRendererAlias struct{ value string }

func (alias scopedRendererAlias) Matches(candidate string) bool {
	return alias.value != "" && candidate == alias.value
}

type ScopedPlanAliasRole uint8

const (
	ScopedPlanAliasPhysicalAccess ScopedPlanAliasRole = iota + 1
	ScopedPlanAliasCorrelatedRelation
)

func validScopedPlanAliasRole(role ScopedPlanAliasRole) bool {
	return role == ScopedPlanAliasPhysicalAccess || role == ScopedPlanAliasCorrelatedRelation
}

// ScopedPlanAliasFact is one allocation-owned identity for a provider-visible
// scoped root, join, or policy traversal. It contains only an opaque matcher
// and stable schema identities: never SQL, binds, predicates, actor data, or
// physical provider names.
type ScopedPlanAliasFact struct {
	matcher    scopedAliasMatcher
	modelID    policyir.ModelID
	relationID policyir.RelationID
	fieldIDs   []policyir.FieldID
	role       ScopedPlanAliasRole
}

func newScopedPlanAliasFact(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role ScopedPlanAliasRole) ScopedPlanAliasFact {
	return ScopedPlanAliasFact{
		matcher:    scopedRendererAlias{value: alias},
		modelID:    model,
		relationID: relation,
		fieldIDs:   append([]policyir.FieldID(nil), fields...),
		role:       role,
	}
}

func newScopedPolicyAliasFact(fact policysql.PolicyRelationAliasFact) ScopedPlanAliasFact {
	return ScopedPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: ScopedPlanAliasCorrelatedRelation}
}

// Matches compares untrusted provider-plan input with the exact renderer-owned
// token. Zero and incomplete facts fail closed.
func (fact ScopedPlanAliasFact) Matches(candidate string) bool {
	return candidate != "" && fact.matcher != nil && fact.modelID != (policyir.ModelID{}) && validScopedPlanAliasRole(fact.role) && fact.matcher.Matches(candidate)
}

func (fact ScopedPlanAliasFact) ModelID() policyir.ModelID { return fact.modelID }

func (fact ScopedPlanAliasFact) RelationID() (policyir.RelationID, bool) {
	return fact.relationID, fact.relationID != (policyir.RelationID{})
}

func (fact ScopedPlanAliasFact) FieldIDs() []policyir.FieldID {
	return append([]policyir.FieldID(nil), fact.fieldIDs...)
}
func (fact ScopedPlanAliasFact) Role() ScopedPlanAliasRole { return fact.role }

func cloneScopedPlanAliasFact(fact ScopedPlanAliasFact) ScopedPlanAliasFact {
	fact.fieldIDs = fact.FieldIDs()
	return fact
}

// ScopedPlanMap is the immutable identity map for one exact scoped statement.
// Returned collections are caller-owned, and an unknown alias remains unknown.
type ScopedPlanMap struct {
	aliases []ScopedPlanAliasFact
}

func (plan ScopedPlanMap) AliasFacts() []ScopedPlanAliasFact {
	result := make([]ScopedPlanAliasFact, len(plan.aliases))
	for index, fact := range plan.aliases {
		result[index] = cloneScopedPlanAliasFact(fact)
	}
	return result
}

func (plan ScopedPlanMap) MatchingAliasFacts(candidate string) []ScopedPlanAliasFact {
	if candidate == "" {
		return nil
	}
	result := make([]ScopedPlanAliasFact, 0, 1)
	for _, fact := range plan.aliases {
		if fact.Matches(candidate) {
			result = append(result, cloneScopedPlanAliasFact(fact))
		}
	}
	return result
}

func (plan ScopedPlanMap) clone() ScopedPlanMap {
	return ScopedPlanMap{aliases: plan.AliasFacts()}
}

type scopedPlanMapBuilder struct {
	aliases       []ScopedPlanAliasFact
	owned         map[string]struct{}
	policyAliases []policysql.PolicyRelationAliasFact
}

func (builder *scopedPlanMapBuilder) add(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role ScopedPlanAliasRole) error {
	if alias == "" || model == (policyir.ModelID{}) || !validScopedPlanAliasRole(role) {
		return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer alias identity is incomplete")
	}
	seenFields := make(map[policyir.FieldID]struct{}, len(fields))
	for _, field := range fields {
		if field == (policyir.FieldID{}) {
			return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer field identity is incomplete")
		}
		if _, duplicate := seenFields[field]; duplicate {
			return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer field identity is ambiguous")
		}
		seenFields[field] = struct{}{}
	}
	if builder.owned == nil {
		builder.owned = map[string]struct{}{}
	}
	if _, duplicate := builder.owned[alias]; duplicate {
		return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer alias identity is ambiguous")
	}
	for _, policy := range builder.policyAliases {
		if policy.Matches(alias) {
			return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer alias identity is ambiguous")
		}
	}
	fact := newScopedPlanAliasFact(alias, model, relation, fields, role)
	if !fact.Matches(alias) {
		return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer alias identity is incomplete")
	}
	builder.owned[alias] = struct{}{}
	builder.aliases = append(builder.aliases, fact)
	return nil
}

func (builder *scopedPlanMapBuilder) mergePolicy(facts []policysql.PolicyRelationAliasFact) error {
	for _, fact := range facts {
		if fact.ModelID() == (policyir.ModelID{}) || fact.RelationID() == (policyir.RelationID{}) {
			return fmt.Errorf("P6_SCOPED_PLAN_MAP: policy alias identity is incomplete")
		}
		for alias := range builder.owned {
			if fact.Matches(alias) {
				return fmt.Errorf("P6_SCOPED_PLAN_MAP: renderer alias identity is ambiguous")
			}
		}
		for _, existing := range builder.policyAliases {
			if fact == existing {
				return fmt.Errorf("P6_SCOPED_PLAN_MAP: policy alias identity is ambiguous")
			}
		}
		builder.policyAliases = append(builder.policyAliases, fact)
		builder.aliases = append(builder.aliases, newScopedPolicyAliasFact(fact))
	}
	return nil
}

func (builder *scopedPlanMapBuilder) freeze() ScopedPlanMap {
	return ScopedPlanMap{aliases: ScopedPlanMap{aliases: builder.aliases}.AliasFacts()}
}

func scopedAuthorizedPlanFieldIDs(plan readplan.Plan) []policyir.FieldID {
	fields := plan.Fields()
	result := make([]policyir.FieldID, len(fields))
	for index, field := range fields {
		result[index] = field.FieldID()
	}
	return result
}
