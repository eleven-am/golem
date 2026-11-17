package golem

// BoundFact is a generated physical predicate with a static capture inventory.
// It has no caller predicate or selection methods. BindFact creates a reference
// owned by the receiving policy, after which ordinary policy algebra applies.
type BoundFact[M any] struct {
	name      string
	predicate Predicate[M]
	binding   *frozenTemplateBinding
}

func GeneratedBoundFact[M any](model ModelID, templateID string, fingerprint SchemaDigest, name string, predicate Predicate[M], arguments ...TemplateArgument) BoundFact[M] {
	return BoundFact[M]{name: name, predicate: predicate, binding: generatedTemplateBinding(model, templateID, fingerprint, arguments)}
}

func (rules *Rules[M]) BindFact(fact BoundFact[M]) Predicate[M] {
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.facts = append(rules.state.facts, factBuilder{name: fact.name, body: fact.predicate.node, binding: fact.binding})
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionFactReference, factName: fact.name, factOwner: &rules.state, operand: noOperand()}}
}
