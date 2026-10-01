package golem

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
)

// OperationID identifies one generated custom mutation resolver. It is
// comparable and carries no capability of its own.
type OperationID struct{ entry uintptr }

type operationReference struct {
	entry uintptr
	name  string
}

func operationReferenceOf(resolver any) operationReference {
	value := reflect.ValueOf(resolver)
	if !value.IsValid() {
		return operationReference{name: "nil value"}
	}
	if value.Kind() != reflect.Func {
		return operationReference{name: fmt.Sprintf("%T value", resolver)}
	}
	if value.IsNil() {
		return operationReference{name: fmt.Sprintf("nil %T", resolver)}
	}
	entry := value.Pointer()
	name := fmt.Sprintf("%T", resolver)
	if function := runtime.FuncForPC(entry); function != nil {
		name = function.Name()
	}
	return operationReference{entry: entry, name: name}
}

// OperationRules records grants that apply only while the named custom
// mutation resolver is running. It offers write grants alone: an operation
// never widens reads and never adds a denial.
type OperationRules[M any] struct {
	rules     *Rules[M]
	operation operationReference
}

// Within scopes the grants recorded through the returned value to the custom
// mutation whose resolver is passed. Inside that operation, a write is
// allowed when a Within grant covering it allows it or when the caller's own
// policy allows it; every other write, including one to a field a Within field
// grant does not name, is decided by the caller's policy alone. The resolver
// must be a generated custom mutation; any other value fails the policy build.
func Within[M, R any](rules *Rules[M], resolver R) *OperationRules[M] {
	return &OperationRules[M]{rules: rules, operation: operationReferenceOf(resolver)}
}

func (rules *OperationRules[M]) CanCreate(value Predicate[M]) {
	rules.appendModelGrant(frozenActionCreate, value)
}
func (rules *OperationRules[M]) CanUpdate(value Predicate[M]) {
	rules.appendModelGrant(frozenActionUpdate, value)
}
func (rules *OperationRules[M]) CanDelete(value Predicate[M]) {
	rules.appendModelGrant(frozenActionDelete, value)
}
func (rules *OperationRules[M]) CanCreateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldGrant(frozenActionCreate, value, first, rest)
}
func (rules *OperationRules[M]) CanUpdateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldGrant(frozenActionUpdate, value, first, rest)
}

func (rules *OperationRules[M]) appendModelGrant(action FrozenAction, value Predicate[M]) {
	operation := rules.operation
	rules.rules.appendRule(ruleBuilder{action: action, effect: frozenEffectGrant, condition: value.node, operation: &operation})
}

func (rules *OperationRules[M]) appendFieldGrant(action FrozenAction, value Predicate[M], first Field[M], rest []Field[M]) {
	operation := rules.operation
	rules.rules.appendRule(ruleBuilder{action: action, effect: frozenEffectGrant, condition: value.node, fields: ruleFieldIdentities(first, rest), operation: &operation})
}

type frozenOperationRules struct {
	reference operationReference
	builders  []ruleBuilder
	rules     []frozenRule
}

func mergeOperationPolicy(base FrozenPolicy, operation frozenOperationRules) (FrozenPolicy, error) {
	rules := make([]frozenRule, 0, len(base.rules)+len(operation.rules))
	for _, rule := range base.rules {
		rules = append(rules, cloneFrozenRule(rule))
	}
	for _, rule := range operation.rules {
		merged := cloneFrozenRule(rule)
		merged.position = uint32(len(rules))
		rules = append(rules, merged)
	}
	canonical, err := encodeFrozenPolicy(base.model, rules)
	if err != nil {
		return FrozenPolicy{}, err
	}
	return FrozenPolicy{model: base.model, rules: rules, canonical: canonical}, nil
}

// GeneratedOperation is one generated custom mutation registration: the
// resolver identity Within grants name, and its contract extension identity.
type GeneratedOperation struct {
	reference   operationReference
	extensionID string
}

// GeneratedCustomMutationOperation registers resolver as the custom mutation
// with the given contract extension identity.
func GeneratedCustomMutationOperation[R any](extensionID string, resolver R) GeneratedOperation {
	return GeneratedOperation{reference: operationReferenceOf(resolver), extensionID: extensionID}
}

type generatedOperation struct {
	name        string
	extensionID string
}

// GeneratedApplicationOperations attaches the generated custom mutation table
// to stamped application bindings. Only a registered resolver can scope a
// Within grant or enter an operation.
func GeneratedApplicationOperations[A any](bindings ApplicationBindings[A], operations ...GeneratedOperation) (ApplicationBindings[A], error) {
	if bindings.generation == (SchemaDigest{}) {
		return ApplicationBindings[A]{}, fmt.Errorf("generated operations: application bindings are unstamped")
	}
	table := make(map[uintptr]generatedOperation, len(operations))
	identities := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if operation.reference.entry == 0 || operation.extensionID == "" {
			return ApplicationBindings[A]{}, fmt.Errorf("generated operations: operation %d has no resolver function or extension identity", index)
		}
		if _, duplicate := table[operation.reference.entry]; duplicate {
			return ApplicationBindings[A]{}, fmt.Errorf("generated operations: resolver %s is registered twice", operation.reference.name)
		}
		if _, duplicate := identities[operation.extensionID]; duplicate {
			return ApplicationBindings[A]{}, fmt.Errorf("generated operations: extension %s is registered twice", operation.extensionID)
		}
		identities[operation.extensionID] = struct{}{}
		table[operation.reference.entry] = generatedOperation{name: operation.reference.name, extensionID: operation.extensionID}
	}
	result := bindings
	result.packages = append([]PackageBindings[A](nil), bindings.packages...)
	result.operations = table
	return result, nil
}

// Operation reports the identity of resolver when it is a generated custom
// mutation of these bindings.
func (bindings ApplicationBindings[A]) Operation(resolver any) (OperationID, bool) {
	reference := operationReferenceOf(resolver)
	if reference.entry == 0 {
		return OperationID{}, false
	}
	if _, ok := bindings.operations[reference.entry]; !ok {
		return OperationID{}, false
	}
	return OperationID{entry: reference.entry}, true
}

// Operations lists every operation that carries Within grants in this set.
func (set GeneratedPolicySet) Operations() []OperationID {
	result := make([]OperationID, 0, len(set.operations))
	for operation := range set.operations {
		result = append(result, operation)
	}
	slices.SortFunc(result, func(left, right OperationID) int {
		switch {
		case left.entry < right.entry:
			return -1
		case left.entry > right.entry:
			return 1
		default:
			return 0
		}
	})
	return result
}

// OperationPolicies returns, for each model that carries Within grants for
// operation, the caller policy followed by those grants. Models without such
// grants are absent and keep their caller policy.
func (set GeneratedPolicySet) OperationPolicies(operation OperationID) []FrozenPolicy {
	source := set.operations[operation]
	result := make([]FrozenPolicy, len(source))
	for index, policy := range source {
		result[index] = FrozenPolicy{model: policy.model, rules: make([]frozenRule, len(policy.rules)), canonical: append([]byte(nil), policy.canonical...)}
		for ruleIndex, rule := range policy.rules {
			result[index].rules[ruleIndex] = cloneFrozenRule(rule)
		}
	}
	return result
}

func buildOperationPolicies[A any](bindings ApplicationBindings[A], policies []FrozenPolicy) (map[OperationID][]FrozenPolicy, error) {
	result := make(map[OperationID][]FrozenPolicy)
	for _, policy := range policies {
		for _, operation := range policy.operations {
			if _, registered := bindings.operations[operation.reference.entry]; operation.reference.entry == 0 || !registered {
				return nil, fmt.Errorf("generated policy set: model %x grants Within(%s), which is not a generated custom mutation resolver", policy.model, operation.reference.name)
			}
			merged, err := mergeOperationPolicy(policy, operation)
			if err != nil {
				return nil, fmt.Errorf("generated policy set: model %x Within(%s): %w", policy.model, operation.reference.name, err)
			}
			identity := OperationID{entry: operation.reference.entry}
			result[identity] = append(result[identity], merged)
		}
	}
	return result, nil
}
