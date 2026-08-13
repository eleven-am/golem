package plan

// QueryPlanFrame is a narrow immutable cursor over one typed read-plan frame.
// It exists so query-plan diagnostics can walk adversarially deep relation
// graphs iteratively without invoking the ordinary recursively deep-copying
// application accessors. It exposes no SQL, provider, policy, or mutation
// authority.
type QueryPlanFrame struct{ plan *Plan }

// QueryPlanTraversal starts an immediate-frame traversal over an already
// authorized immutable plan. The shallow root copy owns the slice headers;
// every reachable field remains private to this package.
func QueryPlanTraversal(value Plan) QueryPlanFrame {
	root := value
	return QueryPlanFrame{plan: &root}
}

// Plan returns a shallow immutable view for existing typed planning helpers.
// Its fields remain package-private and its public collection accessors retain
// their ordinary defensive-copy behavior.
func (frame QueryPlanFrame) Plan() Plan {
	if frame.plan == nil {
		return Plan{}
	}
	return *frame.plan
}

type QueryPlanRelation struct{ relation *Relation }

func (frame QueryPlanFrame) Relations() []QueryPlanRelation {
	if frame.plan == nil {
		return nil
	}
	return immediateQueryPlanRelations(frame.plan.relations)
}

func (frame QueryPlanFrame) Hydrations() []QueryPlanRelation {
	if frame.plan == nil {
		return nil
	}
	return immediateQueryPlanRelations(frame.plan.hydrations)
}

func immediateQueryPlanRelations(values []Relation) []QueryPlanRelation {
	result := make([]QueryPlanRelation, len(values))
	for index := range values {
		result[index] = QueryPlanRelation{relation: &values[index]}
	}
	return result
}

func (value QueryPlanRelation) Relation() Relation {
	if value.relation == nil {
		return Relation{}
	}
	return *value.relation
}

func (value QueryPlanRelation) Child() QueryPlanFrame {
	if value.relation == nil || value.relation.child == nil {
		return QueryPlanFrame{}
	}
	return QueryPlanFrame{plan: value.relation.child}
}
