package ir

import (
	"crypto/sha256"
	"fmt"
)

// FactBinding preserves capture provenance independently of executable truth.
// ScopeTemplateID names the one authority inventory associated with this body.
type FactBinding struct {
	TemplateID      [16]byte
	ScopeTemplateID [16]byte
	Template        Fingerprint
	Arguments       Fingerprint
}

func (binding FactBinding) valid() bool {
	return binding.TemplateID != ([16]byte{}) && binding.ScopeTemplateID != ([16]byte{}) && binding.Template != (Fingerprint{}) && binding.Arguments != (Fingerprint{})
}

func NewBoundFactDefinition(model ModelID, name string, body Condition, binding FactBinding) (FactDefinition, error) {
	if !binding.valid() {
		return FactDefinition{}, fmt.Errorf("policy fact: template provenance is incomplete")
	}
	fact, err := NewFactDefinition(model, name, body)
	if err == nil {
		fact.binding = &binding
	}
	return fact, err
}

func (fact FactDefinition) Binding() (FactBinding, bool) {
	if fact.binding == nil {
		return FactBinding{}, false
	}
	return *fact.binding, true
}

func (fact FactDefinition) WithBody(body Condition) (FactDefinition, error) {
	if fact.binding != nil {
		return NewBoundFactDefinition(fact.model, fact.name, body, *fact.binding)
	}
	return NewFactDefinition(fact.model, fact.name, body)
}

func NewBoundFact(model ModelID, id FactID, body Condition, scope FactScope, binding FactBinding) (Condition, error) {
	authority, bound := scope.Binding()
	if !binding.valid() || !bound || authority.TemplateID != binding.ScopeTemplateID {
		return Condition{}, fmt.Errorf("policy fact: template requires its associated bound authority scope")
	}
	condition, err := NewFact(model, id, body, scope)
	if err == nil {
		condition.node.(*factNode).binding = &binding
	}
	return condition, err
}

func (condition Condition) FactBinding() (FactBinding, bool) {
	node, fact := condition.node.(*factNode)
	if !fact || node.binding == nil {
		return FactBinding{}, false
	}
	return *node.binding, true
}

func (condition Condition) WithFactBody(body Condition) (Condition, error) {
	node, present := condition.node.(*factNode)
	if !present {
		return Condition{}, fmt.Errorf("policy fact: body replacement requires a resolved fact")
	}
	if node.binding != nil {
		return NewBoundFact(node.model, node.fact, body, node.scope, *node.binding)
	}
	return NewFact(node.model, node.fact, body, node.scope)
}

func boundFactBodyFingerprint(body Fingerprint, binding *FactBinding) Fingerprint {
	if binding == nil {
		return body
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:bound-fact-body:v1\x00"))
	_, _ = hash.Write(body[:])
	_, _ = hash.Write(binding.TemplateID[:])
	_, _ = hash.Write(binding.ScopeTemplateID[:])
	_, _ = hash.Write(binding.Template[:])
	_, _ = hash.Write(binding.Arguments[:])
	var result Fingerprint
	copy(result[:], hash.Sum(nil))
	return result
}
