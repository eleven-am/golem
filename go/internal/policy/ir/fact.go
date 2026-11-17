package ir

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// These limits bound work after shared definitions are expanded by an
// evaluator or SQL renderer. Canonical encoding remains compact independently.
// The depth ceiling matches policy binding and implication proof depth.
const MaxFactExpansionNodes = 16 * 1024
const MaxFactDepth = 256

type FactLimit string

const (
	FactExpansionNodes FactLimit = "fact_expansion_nodes"
	FactDepth          FactLimit = "fact_depth"
)

type FactLimitError struct {
	Limit   FactLimit
	Maximum int
}

func (failure *FactLimitError) Error() string {
	return fmt.Sprintf("policy IR: %s exceeds %d", failure.Limit, failure.Maximum)
}

// FactDefinition owns the actor-bound body of one named, model-rooted policy
// fact. References stay separate from bodies so shared definitions are encoded
// once and a reference graph cannot cause exponential tree expansion.
type FactDefinition struct {
	id      FactID
	model   ModelID
	name    string
	body    Condition
	binding *FactBinding
}

func FactIdentity(model ModelID, name string) (FactID, error) {
	if model == (ModelID{}) || name == "" || strings.TrimSpace(name) != name || strings.ContainsRune(name, 0) || !utf8.ValidString(name) {
		return FactID{}, fmt.Errorf("policy IR: fact model and exact UTF-8 name are required")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:policy-fact:v1\x00"))
	_, _ = hash.Write(model[:])
	_, _ = hash.Write([]byte(name))
	var id FactID
	copy(id[:], hash.Sum(nil))
	return id, nil
}

func NewFactDefinition(model ModelID, name string, body Condition) (FactDefinition, error) {
	id, err := FactIdentity(model, name)
	if err != nil {
		return FactDefinition{}, err
	}
	if err := body.Validate(); err != nil {
		return FactDefinition{}, err
	}
	if body.ModelID() != model {
		return FactDefinition{}, fmt.Errorf("policy IR: fact body belongs to another model root")
	}
	return FactDefinition{id: id, model: model, name: name, body: body}, nil
}

func (fact FactDefinition) ID() FactID       { return fact.id }
func (fact FactDefinition) ModelID() ModelID { return fact.model }
func (fact FactDefinition) Name() string     { return fact.name }
func (fact FactDefinition) Body() Condition  { return fact.body }

type factReferenceNode struct {
	nodeState
	model ModelID
	fact  FactID
}

type factNode struct {
	nodeState
	model         ModelID
	fact          FactID
	body          Condition
	scope         FactScope
	binding       *FactBinding
	expandedNodes int
	depth         int
}

func (*factReferenceNode) kind() ConditionKind           { return ConditionFactReference }
func (node *factReferenceNode) modelID() ModelID         { return node.model }
func (*factReferenceNode) requirementSet() []Requirement { return nil }
func (*factNode) kind() ConditionKind                    { return ConditionFact }
func (node *factNode) modelID() ModelID                  { return node.model }
func (node *factNode) requirementSet() []Requirement     { return node.body.node.requirementSet() }

func NewFactReference(model ModelID, fact FactID) (Condition, error) {
	if model == (ModelID{}) || fact == (FactID{}) {
		return Condition{}, fmt.Errorf("policy IR: fact reference identities are required")
	}
	return Condition{node: &factReferenceNode{model: model, fact: fact}}, nil
}

// NewFact preserves the boundary of a resolved physical fact. The boundary is
// retained after reference resolution so relation policy expansion and event
// dependency planning cannot confuse it with an ordinary readable relation.
func NewFact(model ModelID, fact FactID, body Condition, scope FactScope) (Condition, error) {
	if model == (ModelID{}) || fact == (FactID{}) || body.Validate() != nil || body.ModelID() != model {
		return Condition{}, fmt.Errorf("policy IR: resolved fact identities and body root must agree")
	}
	if !scope.validFor(model) {
		return Condition{}, fmt.Errorf("policy IR: resolved fact requires an explicit scope at its model root")
	}
	nodes, depth, err := factComplexity(body)
	if err != nil {
		return Condition{}, err
	}
	if nodes >= MaxFactExpansionNodes {
		return Condition{}, &FactLimitError{Limit: FactExpansionNodes, Maximum: MaxFactExpansionNodes}
	}
	if depth >= MaxFactDepth {
		return Condition{}, &FactLimitError{Limit: FactDepth, Maximum: MaxFactDepth}
	}
	references, err := factReferences(body, model, true)
	if err != nil {
		return Condition{}, err
	}
	if len(references) != 0 {
		return Condition{}, fmt.Errorf("policy IR: resolved fact body contains an unresolved reference")
	}
	pending := []Condition{body}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch node := current.node.(type) {
		case *factNode:
			if !sameFactScope(scope, node.scope) {
				return Condition{}, fmt.Errorf("policy IR: nested facts must share their policy scope")
			}
		case *logicalNode:
			pending = append(pending, node.children...)
		case *relationNode:
			if node.child.node != nil {
				pending = append(pending, node.child)
			}
		}
	}
	return Condition{node: &factNode{model: model, fact: fact, body: body, scope: scope, expandedNodes: nodes + 1, depth: depth + 1}}, nil
}

// CheckFactExpansion also bounds a completed constraint that combines several
// individually valid facts. It must run before recursive validation/rendering;
// shared fact costs are read from their immutable constructor-derived totals.
func CheckFactExpansion(condition Condition) error {
	_, _, err := factComplexity(condition)
	return err
}

// HasFacts identifies policy-only provenance, including unresolved references.
// Caller binders and query planners use it before ordinary relation expansion.
func HasFacts(condition Condition) bool {
	pending := []Condition{condition}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch node := current.node.(type) {
		case *factNode, *factReferenceNode:
			return true
		case *logicalNode:
			pending = append(pending, node.children...)
		case *relationNode:
			if node.child.node != nil {
				pending = append(pending, node.child)
			}
		}
	}
	return false
}

func factComplexity(condition Condition) (int, int, error) {
	nodes, depth := 0, 0
	pending := []Condition{condition}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch node := current.node.(type) {
		case *factNode:
			nodes += node.expandedNodes
			depth = max(depth, node.depth)
		case *constantNode, *scalarNode, *listNode, *jsonNode, *factReferenceNode:
			nodes++
		case *logicalNode:
			nodes++
			if len(node.children) > MaxFactExpansionNodes-nodes-len(pending) {
				return 0, 0, &FactLimitError{Limit: FactExpansionNodes, Maximum: MaxFactExpansionNodes}
			}
			for index := len(node.children) - 1; index >= 0; index-- {
				pending = append(pending, node.children[index])
			}
		case *relationNode:
			nodes++
			if node.child.node != nil {
				pending = append(pending, node.child)
			}
		default:
			return 0, 0, fmt.Errorf("policy IR: unknown fact expansion condition")
		}
		if nodes > MaxFactExpansionNodes {
			return 0, 0, &FactLimitError{Limit: FactExpansionNodes, Maximum: MaxFactExpansionNodes}
		}
	}
	return nodes, depth, nil
}

func (condition Condition) FactReference() (FactID, bool) {
	node, ok := condition.node.(*factReferenceNode)
	if !ok {
		return FactID{}, false
	}
	return node.fact, true
}

func (condition Condition) Fact() (FactID, Condition, bool) {
	node, ok := condition.node.(*factNode)
	if !ok {
		return FactID{}, Condition{}, false
	}
	return node.fact, node.body, true
}

func (condition Condition) FactScope() (FactScope, bool) {
	node, ok := condition.node.(*factNode)
	if !ok {
		return FactScope{}, false
	}
	return node.scope, true
}

func NewPolicyWithFacts(model ModelID, rules []Rule, facts []FactDefinition, scope FactScope) (Policy, error) {
	if len(facts) != 0 && !scope.validFor(model) {
		return Policy{}, fmt.Errorf("policy IR: fact policy requires an explicit scope at its model root")
	}
	if len(facts) == 0 && scope.condition.node != nil {
		return Policy{}, fmt.Errorf("policy IR: fact scope requires fact definitions")
	}
	owned := append([]FactDefinition(nil), facts...)
	if len(owned) > 1 {
		sort.Slice(owned, func(i, j int) bool { return owned[i].name < owned[j].name })
	}
	return newPolicy(model, rules, owned, scope)
}

func (policy Policy) FactScope() FactScope { return policy.factScope }

func (policy Policy) Facts() []FactDefinition {
	return append([]FactDefinition(nil), policy.facts...)
}

func (policy Policy) Fact(id FactID) (FactDefinition, bool) {
	for _, fact := range policy.facts {
		if fact.id == id {
			return fact, true
		}
	}
	return FactDefinition{}, false
}

// factReferences walks the authored structure without expanding a definition.
// Fact references are root-scoped: traversing physical relationships is allowed,
// but borrowing another root's fact definition is not.
func factReferences(condition Condition, root ModelID, resolved bool) ([]FactID, error) {
	var references []FactID
	pending := []Condition{condition}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if err := current.Validate(); err != nil {
			return nil, err
		}
		switch node := current.node.(type) {
		case *constantNode, *scalarNode, *listNode, *jsonNode:
		case *logicalNode:
			for i := len(node.children) - 1; i >= 0; i-- {
				pending = append(pending, node.children[i])
			}
		case *relationNode:
			if node.child.node != nil {
				pending = append(pending, node.child)
			}
		case *factReferenceNode:
			if node.model != root {
				return nil, fmt.Errorf("policy IR: fact reference belongs to another model root")
			}
			references = append(references, node.fact)
		case *factNode:
			if !resolved {
				return nil, fmt.Errorf("policy IR: authored policies must use a declared fact reference")
			}
			// NewFact has already proved this immutable boundary contains no
			// unresolved references. Rewalking shared bodies here would expand
			// a compact definition graph once per incoming edge.
		default:
			return nil, fmt.Errorf("policy IR: unknown fact dependency condition")
		}
	}
	return references, nil
}

func validateFactGraph(policy Policy) error {
	definitions := make(map[FactID]FactDefinition, len(policy.facts))
	edges := make(map[FactID][]FactID, len(policy.facts))
	for _, definition := range policy.facts {
		id, err := FactIdentity(definition.model, definition.name)
		if err != nil || definition.model != policy.model || id != definition.id {
			return fmt.Errorf("policy IR: fact definition identity or model root is invalid")
		}
		if definition.body.ModelID() != policy.model {
			return fmt.Errorf("policy IR: fact body belongs to another model root")
		}
		if definition.binding != nil {
			scope, bound := policy.factScope.Binding()
			if !definition.binding.valid() || !bound || scope.TemplateID != definition.binding.ScopeTemplateID {
				return fmt.Errorf("policy IR: generated fact belongs to another authority scope template")
			}
		}
		if _, exists := definitions[id]; exists {
			return fmt.Errorf("policy IR: duplicate fact definition")
		}
		definitions[id] = definition
		references, err := factReferences(definition.body, policy.model, false)
		if err != nil {
			return err
		}
		edges[id] = references
	}
	check := func(references []FactID) error {
		for _, reference := range references {
			if _, exists := definitions[reference]; !exists {
				return fmt.Errorf("policy IR: fact reference has no definition at its model root")
			}
		}
		return nil
	}
	for _, definition := range policy.facts {
		if err := check(edges[definition.id]); err != nil {
			return err
		}
	}
	for _, rule := range policy.rules {
		if condition, present := rule.Condition(); present {
			references, err := factReferences(condition, policy.model, false)
			if err != nil {
				return err
			}
			if err := check(references); err != nil {
				return err
			}
		}
	}
	// Iterative depth-first traversal validates even unused definitions without
	// recursing on an application-authored fact chain or expanding shared DAGs.
	type visit struct {
		id   FactID
		next int
	}
	state := make(map[FactID]uint8, len(definitions))
	for _, definition := range policy.facts {
		if state[definition.id] != 0 {
			continue
		}
		state[definition.id] = 1
		pending := []visit{{id: definition.id}}
		for len(pending) != 0 {
			current := &pending[len(pending)-1]
			children := edges[current.id]
			if current.next == len(children) {
				state[current.id] = 2
				pending = pending[:len(pending)-1]
				continue
			}
			child := children[current.next]
			current.next++
			if state[child] == 1 {
				return fmt.Errorf("policy IR: recursive fact definitions are not supported")
			}
			if state[child] == 0 {
				state[child] = 1
				pending = append(pending, visit{id: child})
			}
		}
	}
	return nil
}
