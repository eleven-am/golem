package golem

import (
	"bytes"
	"slices"
	"strings"
	"unicode/utf8"
)

const frozenFactPolicyVersion uint16 = 3

// FactScope declares the actor domain of this entire policy, including
// negative facts, field rules and operation grants. It must be declared once
// when using Fact. Scopes use scalar predicates and positive Is/Some paths;
// related paths require coherent route evidence before runtime activation. Within
// this domain a hidden dependency may conservatively invalidate source data;
// the scope does not grant access to dependency rows.
func (rules *Rules[M]) FactScope(scope Predicate[M]) {
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.scope = scope.node
	rules.state.scopeBinding = nil
	rules.state.scopeDeclarations++
}

// Fact declares a named physical policy predicate and returns its reference.
// Physical relationship hops do not require permission to read the related
// row. The reference belongs to this rules builder and may be composed only
// in its policy, never in caller query expressions.
func (rules *Rules[M]) Fact(name string, body Predicate[M]) Predicate[M] {
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.facts = append(rules.state.facts, factBuilder{name: name, body: body.node})
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionFactReference, factName: name, factOwner: &rules.state, operand: noOperand()}}
}

type factBuilder struct {
	name    string
	body    *predicateNode
	binding *frozenTemplateBinding
}

type frozenFact struct {
	name    string
	body    *frozenCondition
	binding *frozenTemplateBinding
}

// FrozenFactView is an immutable named definition owned by a frozen policy.
type FrozenFactView interface {
	sealedFrozenFactView()
	Name() string
	Body() FrozenPredicateView
	TemplateBinding() (FrozenTemplateBindingView, bool)
}

type frozenFactView struct {
	model ModelID
	fact  frozenFact
}

func (frozenFactView) sealedFrozenFactView() {}
func (view frozenFactView) Name() string     { return view.fact.name }
func (view frozenFactView) Body() FrozenPredicateView {
	return frozenConditionPredicate(view.model, view.fact.body)
}

func (view frozenFactView) TemplateBinding() (FrozenTemplateBindingView, bool) {
	if view.fact.binding == nil {
		return nil, false
	}
	return frozenTemplateBindingView{binding: view.fact.binding}, true
}

func frozenConditionPredicate(model ModelID, condition *frozenCondition) FrozenPredicateView {
	canonical, _ := encodeFrozenPredicate(model, condition)
	return frozenPredicateView{predicate: FrozenPredicate{rootModel: model, root: condition, canonical: canonical}}
}

func validFactName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && !strings.ContainsRune(name, 0) && utf8.ValidString(name)
}

func freezeFacts(owner *rulesState, model ModelID, facts []factBuilder, scope *predicateNode, declarations int, rules []ruleBuilder) ([]frozenFact, *frozenCondition, error) {
	if len(facts) == 0 {
		if declarations != 0 {
			return nil, nil, freezeFailure(FreezeInvalidRule, "fact scope requires fact definitions")
		}
		for _, rule := range rules {
			if err := checkFactOwner(rule.condition, owner); err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, nil
	}
	if declarations != 1 {
		return nil, nil, freezeFailure(FreezeInvalidRule, "fact policy requires exactly one explicit fact scope")
	}
	frozenScope, err := freezeFactDeclarationNode(scope)
	if err != nil {
		return nil, nil, err
	}
	if !positiveFactScope(scope, false) {
		return nil, nil, freezeFailure(FreezeInvalidRule, "fact scope requires scalar predicates and positive Is/Some witnesses")
	}
	seen := map[string]bool{}
	result := make([]frozenFact, len(facts))
	for index, fact := range facts {
		if !validFactName(fact.name) || seen[fact.name] {
			return nil, nil, freezeFailure(FreezeInvalidRule, "fact names must be distinct exact UTF-8 names")
		}
		seen[fact.name] = true
		if err := checkFactOwner(fact.body, owner); err != nil {
			return nil, nil, err
		}
		body, err := freezeFactDeclarationNode(fact.body)
		if err != nil {
			return nil, nil, err
		}
		binding, err := freezeTemplateBinding(fact.binding, model, factBindingDomain)
		if err != nil {
			return nil, nil, err
		}
		result[index] = frozenFact{name: fact.name, body: body, binding: binding}
	}
	for _, rule := range rules {
		if err := checkFactOwner(rule.condition, owner); err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(result, func(a, b frozenFact) int { return strings.Compare(a.name, b.name) })
	return result, frozenScope, nil
}

func checkFactOwner(root *predicateNode, owner *rulesState) error {
	seen := map[*predicateNode]bool{}
	pending := []*predicateNode{root}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil || seen[current] {
			continue
		}
		seen[current] = true
		if current.kind == FrozenConditionFactReference && current.factOwner != owner {
			return freezeFailure(FreezeInvalidRule, "fact reference belongs to another rules builder")
		}
		pending = append(pending, current.children...)
	}
	return nil
}

func positiveFactScope(root *predicateNode, negative bool) bool {
	switch root.kind {
	case FrozenConditionConstant, FrozenConditionScalar:
		return true
	case FrozenConditionLogical:
		if root.operator == FrozenOperatorNot {
			negative = !negative
		}
		for _, child := range root.children {
			if !positiveFactScope(child, negative) {
				return false
			}
		}
		return true
	case FrozenConditionRelation:
		if negative || root.operator != FrozenOperatorRelationIs && root.operator != FrozenOperatorRelationSome || len(root.children) != 1 {
			return false
		}
		return positiveFactScope(root.children[0], false)
	default:
		return false
	}
}

func frozenHasFacts(root *frozenCondition) bool {
	if root == nil {
		return false
	}
	if root.kind == FrozenConditionFactReference {
		return true
	}
	for _, child := range root.children {
		if frozenHasFacts(child) {
			return true
		}
	}
	return false
}

func frozenPredicateVersion(root *frozenCondition) uint16 {
	if frozenHasFacts(root) {
		return frozenFactPolicyVersion
	}
	return frozenPolicyVersion
}

func encodeFrozenFactPolicy(model ModelID, rules []frozenRule, scope *frozenCondition, facts []frozenFact, binding *frozenTemplateBinding) ([]byte, error) {
	if binding != nil {
		base, err := encodeFrozenFactPolicy(model, rules, scope, facts, nil)
		if err != nil {
			return nil, err
		}
		var output bytes.Buffer
		output.WriteString("golem:public-policy:scope-template:v4\x00")
		writeUint16(&output, frozenTemplatePolicyVersion)
		writeBytes(&output, base)
		writeBytes(&output, binding.canonical)
		boundCount := 0
		for _, fact := range facts {
			if fact.binding != nil {
				boundCount++
			}
		}
		writeUint32(&output, uint32(boundCount))
		for _, fact := range facts {
			if fact.binding != nil {
				writeBytes(&output, []byte(fact.name))
				writeBytes(&output, fact.binding.canonical)
			}
		}
		return output.Bytes(), nil
	}
	base, err := encodeFrozenPolicy(model, rules)
	if err != nil || len(facts) == 0 {
		return base, err
	}
	var output bytes.Buffer
	output.WriteString("golem:public-policy:facts:v3\x00")
	writeUint16(&output, frozenFactPolicyVersion)
	writeBytes(&output, base)
	if err := encodeFrozenCondition(&output, scope); err != nil {
		return nil, err
	}
	writeUint32(&output, uint32(len(facts)))
	for _, fact := range facts {
		writeBytes(&output, []byte(fact.name))
		if err := encodeFrozenCondition(&output, fact.body); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func (view frozenConditionView) FactName() (string, bool) {
	if view.condition == nil || view.condition.kind != FrozenConditionFactReference {
		return "", false
	}
	return view.condition.factName, true
}

func (view frozenPolicyView) Facts() []FrozenFactView {
	result := make([]FrozenFactView, len(view.policy.facts))
	for index, fact := range view.policy.facts {
		result[index] = frozenFactView{model: view.policy.model, fact: fact}
	}
	return result
}

func (view frozenPolicyView) FactScope() (FrozenPredicateView, bool) {
	if view.policy.scope == nil {
		return nil, false
	}
	return frozenConditionPredicate(view.policy.model, view.policy.scope), true
}
