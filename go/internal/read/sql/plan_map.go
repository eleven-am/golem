package sql

import (
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// planAliasMatcher is the deliberately narrow boundary between renderer-owned
// aliases and the provider-plan sanitizer. Implementations expose equality
// only; no downstream caller can recover an identifier for SQL construction.
type planAliasMatcher interface {
	Matches(string) bool
}

type readAlias struct{ value string }

func (alias readAlias) Matches(candidate string) bool {
	return alias.value != "" && candidate == alias.value
}

// PlanAliasRole is allocation provenance. Relation-bearing joins can still be
// ordinary physical accesses; only aliases allocated for correlated reads or
// policy traversals carry the correlated role.
type PlanAliasRole uint8

const (
	PlanAliasPhysicalAccess PlanAliasRole = iota + 1
	PlanAliasCorrelatedRelation
)

func validPlanAliasRole(role PlanAliasRole) bool {
	return role == PlanAliasPhysicalAccess || role == PlanAliasCorrelatedRelation
}

// readPlanAliasAllocator owns aliases whose helpers can be entered more than
// once while composing a statement. Static singletons and relation-indexed
// correlated roots are already unique; relation-count helpers need a shared
// counter because they can occur at both root and correlated-child scopes.
type readPlanAliasAllocator struct {
	nextRelationCount uint64
}

func (allocator *readPlanAliasAllocator) relationCount() string {
	alias := fmt.Sprintf("golem_rc%d", allocator.nextRelationCount)
	allocator.nextRelationCount++
	return alias
}

// PlanAliasFact is one allocation-scoped alias identity retained by the read
// renderer. It contains no SQL, binds, values, provider names, diagnostics, or
// public query-plan types.
type PlanAliasFact struct {
	matcher    planAliasMatcher
	modelID    policyir.ModelID
	relationID policyir.RelationID
	fieldIDs   []policyir.FieldID
	role       PlanAliasRole
}

func newPlanAliasFact(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role PlanAliasRole) PlanAliasFact {
	return PlanAliasFact{matcher: readAlias{value: alias}, modelID: model, relationID: relation, fieldIDs: append([]policyir.FieldID(nil), fields...), role: role}
}

func newPolicyPlanAliasFact(fact policysql.PolicyRelationAliasFact) PlanAliasFact {
	return PlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: PlanAliasCorrelatedRelation}
}

// Matches compares an untrusted provider alias with the renderer-owned token.
// Zero and incomplete facts always fail closed.
func (fact PlanAliasFact) Matches(candidate string) bool {
	return candidate != "" && fact.matcher != nil && fact.modelID != (policyir.ModelID{}) && validPlanAliasRole(fact.role) && fact.matcher.Matches(candidate)
}

func (fact PlanAliasFact) ModelID() policyir.ModelID { return fact.modelID }
func (fact PlanAliasFact) RelationID() (policyir.RelationID, bool) {
	return fact.relationID, fact.relationID != (policyir.RelationID{})
}
func (fact PlanAliasFact) FieldIDs() []policyir.FieldID {
	return append([]policyir.FieldID(nil), fact.fieldIDs...)
}
func (fact PlanAliasFact) Role() PlanAliasRole { return fact.role }

func clonePlanAliasFact(fact PlanAliasFact) PlanAliasFact {
	fact.fieldIDs = fact.FieldIDs()
	return fact
}

// PlanMap is the immutable alias-identity map attached to one exact rendered
// statement. AliasFacts and MatchingAliasFacts always return caller-owned
// snapshots. Matching preserves all facts for a repeated alias; downstream
// sanitizers must use the stable identities to reject ambiguity, never guess.
type PlanMap struct {
	aliases []PlanAliasFact
}

func (plan PlanMap) AliasFacts() []PlanAliasFact {
	result := make([]PlanAliasFact, len(plan.aliases))
	for index, fact := range plan.aliases {
		result[index] = clonePlanAliasFact(fact)
	}
	return result
}

func (plan PlanMap) MatchingAliasFacts(candidate string) []PlanAliasFact {
	if candidate == "" {
		return nil
	}
	result := make([]PlanAliasFact, 0)
	for _, fact := range plan.aliases {
		if fact.Matches(candidate) {
			result = append(result, clonePlanAliasFact(fact))
		}
	}
	return result
}

func (plan PlanMap) clone() PlanMap { return PlanMap{aliases: plan.AliasFacts()} }

type planMapBuilder struct {
	aliases []PlanAliasFact
}

func (builder *planMapBuilder) add(alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role PlanAliasRole) {
	fact := newPlanAliasFact(alias, model, relation, fields, role)
	if fact.Matches(alias) {
		builder.aliases = append(builder.aliases, fact)
	}
}

func (builder *planMapBuilder) mergePolicy(facts []policysql.PolicyRelationAliasFact) {
	for _, fact := range facts {
		converted := newPolicyPlanAliasFact(fact)
		if converted.ModelID() != (policyir.ModelID{}) {
			builder.aliases = append(builder.aliases, converted)
		}
	}
}

func (builder *planMapBuilder) merge(plan PlanMap) {
	for _, fact := range plan.aliases {
		builder.aliases = append(builder.aliases, clonePlanAliasFact(fact))
	}
}

func (builder *planMapBuilder) freeze() PlanMap {
	return PlanMap{aliases: PlanMap{aliases: builder.aliases}.AliasFacts()}
}
