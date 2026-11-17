package ir

import (
	"crypto/sha256"
	"fmt"
)

// FactScope is the actor-bound domain of an entire fact-owning policy. It
// authorizes conservative activity invalidation within that domain, never
// access to a physical dependency row. Keeping the gate outside the rule
// algebra prevents NOT, DENY and open baselines from escaping the domain.
//
// Domains may use local scalar predicates and positive correlated Is/Some
// paths. Related domains require coherent historical route evidence before
// runtime activation; an unknown relationship must never authorize activity.
type FactScope struct {
	condition Condition
	related   bool
	binding   *ScopeBinding
}

// ScopeBinding identifies the exact static template and frozen argument
// contract. The condition retains executable actor-bound truth; this metadata
// preserves capture provenance even after Boolean simplification.
type ScopeBinding struct {
	TemplateID [16]byte
	Template   Fingerprint
	Arguments  Fingerprint
}

func NewBoundFactScope(condition Condition, binding ScopeBinding) (FactScope, error) {
	if binding.TemplateID == ([16]byte{}) || binding.Template == (Fingerprint{}) || binding.Arguments == (Fingerprint{}) {
		return FactScope{}, fmt.Errorf("policy fact scope: template provenance is incomplete")
	}
	scope, err := NewFactScope(condition)
	if err != nil {
		return FactScope{}, err
	}
	scope.binding = &binding
	return scope, nil
}

func (scope FactScope) Binding() (ScopeBinding, bool) {
	if scope.binding == nil {
		return ScopeBinding{}, false
	}
	return *scope.binding, true
}

// WithCondition rebuilds an equivalent normalized condition while retaining
// the provenance established by binding against the registry.
func (scope FactScope) WithCondition(condition Condition) (FactScope, error) {
	if condition.ModelID() != scope.ModelID() {
		return FactScope{}, fmt.Errorf("policy fact scope: normalization changed its root")
	}
	result, err := NewFactScope(condition)
	if err == nil {
		result.binding = scope.binding
	}
	return result, err
}

func FactScopeFingerprint(scope FactScope) (Fingerprint, error) {
	condition, err := ConditionFingerprint(scope.condition)
	if err != nil || scope.binding == nil {
		return condition, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:bound-fact-scope:v1\x00"))
	_, _ = hash.Write(condition[:])
	_, _ = hash.Write(scope.binding.TemplateID[:])
	_, _ = hash.Write(scope.binding.Template[:])
	_, _ = hash.Write(scope.binding.Arguments[:])
	var result Fingerprint
	copy(result[:], hash.Sum(nil))
	return result, nil
}

type FactScopeEvidenceError struct{ ModelID ModelID }

func (failure *FactScopeEvidenceError) Error() string {
	return fmt.Sprintf("policy fact scope: model %x requires coherent historical route evidence", failure.ModelID)
}

func NewFactScope(condition Condition) (FactScope, error) {
	if err := condition.Validate(); err != nil {
		return FactScope{}, fmt.Errorf("policy IR: invalid fact scope: %w", err)
	}
	if err := CheckFactExpansion(condition); err != nil {
		return FactScope{}, err
	}
	type term struct {
		condition Condition
		negative  bool
	}
	pending := []term{{condition: condition}}
	related := false
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch current.condition.Kind() {
		case ConditionConstant, ConditionScalar:
		case ConditionLogical:
			operation, children, _ := current.condition.Logical()
			negative := current.negative
			if operation == LogicalNot {
				negative = !negative
			}
			for _, child := range children {
				pending = append(pending, term{condition: child, negative: negative})
			}
		case ConditionRelation:
			operation, _ := current.condition.Operator()
			if current.negative || operation != OperatorRelationIs && operation != OperatorRelationSome {
				return FactScope{}, fmt.Errorf("policy IR: related fact scope requires positive Is/Some witnesses")
			}
			_, _, _, _, child, _ := current.condition.Relation()
			if child == nil {
				return FactScope{}, fmt.Errorf("policy IR: related fact scope requires a witness condition")
			}
			related = true
			pending = append(pending, term{condition: *child})
		default:
			return FactScope{}, fmt.Errorf("policy IR: fact scope requires scalar predicates and positive relationship witnesses")
		}
	}
	return FactScope{condition: condition, related: related}, nil
}

func (scope FactScope) ModelID() ModelID     { return scope.condition.ModelID() }
func (scope FactScope) Condition() Condition { return scope.condition }
func (scope FactScope) UsesRelations() bool  { return scope.related }

// NewFactScopeCondition retains related authority and generated template
// provenance, including when a bound body becomes constant. Inline local
// domains preserve their existing encoding and cheap scalar path.
func NewFactScopeCondition(scope FactScope) (Condition, error) {
	if !scope.validFor(scope.ModelID()) {
		return Condition{}, fmt.Errorf("policy IR: fact scope is absent")
	}
	if !scope.related && scope.binding == nil {
		return scope.condition, nil
	}
	return NewFact(scope.ModelID(), factScopeIdentity(scope.ModelID()), scope.condition, scope)
}

// NewFactScopeWitness retains an owning row's observation authority when its
// definitions are unused. Its true body does not observe those definitions or
// change query truth. Bound related scopes are representable here, but event
// planners must still require coherent historical evidence for their use.
func NewFactScopeWitness(scope FactScope) (Condition, error) {
	if !scope.validFor(scope.ModelID()) {
		return Condition{}, fmt.Errorf("policy fact scope: observation witness requires a scope")
	}
	if scope.UsesRelations() && scope.binding == nil {
		return Condition{}, &FactScopeEvidenceError{ModelID: scope.ModelID()}
	}
	body, err := NewConstant(scope.ModelID(), true)
	if err != nil {
		return Condition{}, err
	}
	id := factScopeKey(scope.ModelID(), "golem:policy-fact-scope-witness:v1\x00")
	return NewFact(scope.ModelID(), id, body, scope)
}

// IsFactScope distinguishes authority gates from data facts after normalization.
// Its separate identity domain cannot alias an application-named definition.
func (condition Condition) IsFactScope() bool {
	id, _, present := condition.Fact()
	return present && id == factScopeIdentity(condition.ModelID())
}

func factScopeIdentity(model ModelID) FactID {
	return factScopeKey(model, "golem:policy-fact-scope:v1\x00")
}

func factScopeKey(model ModelID, domain string) FactID {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(model[:])
	var id FactID
	copy(id[:], hash.Sum(nil))
	return id
}

func (scope FactScope) validFor(model ModelID) bool {
	return scope.condition.node != nil && scope.ModelID() == model
}

func sameFactScope(left, right FactScope) bool {
	if (left.binding == nil) != (right.binding == nil) || left.binding != nil && *left.binding != *right.binding {
		return false
	}
	if left.condition == right.condition {
		return true
	}
	a, err := ConditionFingerprint(left.condition)
	if err != nil {
		return false
	}
	b, err := ConditionFingerprint(right.condition)
	return err == nil && a == b
}
