package golem

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/eleven-am/golem/go/internal/invalidationcontract"
)

// InvalidationOption declares an operation-owned observation dependency. The
// compiler accepts these options on Query and PageQuery, never Mutation.
type InvalidationOption interface {
	CustomOption
	PageOption
}

type invalidationOption struct{}

func (invalidationOption) customOption() {}
func (invalidationOption) pageOption()   {}

// InvalidateRows observes activity on caller-readable source rows. Binder must
// be func(context.Context, Actor, Args) (Predicate[M], error); its predicate
// narrows ordinary caller authorization and never grants access.
func InvalidateRows[M any](_ string, _ any, _ Field[M], _ ...Field[M]) InvalidationOption {
	return invalidationOption{}
}

// InvalidateFacts observes declared policy facts under their existing actor
// scope. Binder must return BoundFactScope[M] for the named scope template;
// runtime requires exact equality with the active policy's bound scope.
func InvalidateFacts[M any](_ string, _ string, _ any, _ string, _ ...string) InvalidationOption {
	return invalidationOption{}
}

// InvalidationStream carries no rows, identities or data packets. Wait blocks
// until an authorized invalidation, revocation, failure or cancellation ends it.
type InvalidationStream interface {
	Wait(context.Context) error
	Close() error
}

// RuntimeInvalidationDependency is the immutable generated-binding result.
// Freezing this value grants no observation authority; runtime validates it
// against the registered dependency and the freshly admitted actor policy.
type RuntimeInvalidationDependency struct {
	kind         string
	model        ModelID
	predicate    FrozenPredicate
	scopeBinding *frozenTemplateBinding
}

func (value RuntimeInvalidationDependency) Kind() string     { return value.kind }
func (value RuntimeInvalidationDependency) ModelID() ModelID { return value.model }
func (value RuntimeInvalidationDependency) Predicate() (FrozenPredicate, bool) {
	return value.predicate, value.kind == invalidationcontract.ReadableRows
}
func (value RuntimeInvalidationDependency) FactScope() (FrozenPredicate, FrozenTemplateBindingView, bool) {
	if value.kind != invalidationcontract.PolicyFacts || value.scopeBinding == nil {
		return FrozenPredicate{}, nil, false
	}
	return value.predicate, frozenTemplateBindingView{binding: value.scopeBinding}, true
}

func RuntimeInvalidationRowsValue[M any](descriptor ModelDescriptor[M], predicate Predicate[M]) (RuntimeInvalidationDependency, error) {
	frozen, err := predicate.Freeze(descriptor)
	if err != nil {
		return RuntimeInvalidationDependency{}, err
	}
	return RuntimeInvalidationDependency{kind: invalidationcontract.ReadableRows, model: descriptor.Metadata().ModelID(), predicate: frozen}, nil
}

func RuntimeInvalidationFactsValue[M any](descriptor ModelDescriptor[M], scope BoundFactScope[M]) (RuntimeInvalidationDependency, error) {
	model := descriptor.Metadata().ModelID()
	if model == (ModelID{}) || scope.binding == nil {
		return RuntimeInvalidationDependency{}, fmt.Errorf("invalidation requires an explicit generated positive fact scope")
	}
	root, err := freezeFactDeclarationNode(scope.predicate.node)
	if err != nil {
		return RuntimeInvalidationDependency{}, err
	}
	if !positiveFactScope(scope.predicate.node, false) {
		return RuntimeInvalidationDependency{}, fmt.Errorf("invalidation requires a positive fact scope")
	}
	canonical, err := encodeFrozenPredicate(model, root)
	if err != nil {
		return RuntimeInvalidationDependency{}, err
	}
	binding, err := freezeTemplateBinding(scope.binding, model, scopeBindingDomain)
	if err != nil {
		return RuntimeInvalidationDependency{}, err
	}
	return RuntimeInvalidationDependency{kind: invalidationcontract.PolicyFacts, model: model, predicate: FrozenPredicate{rootModel: model, root: root, canonical: canonical}, scopeBinding: binding}, nil
}

type RuntimeInvalidationBindingIdentity struct {
	OperationID  string
	DependencyID string
	BinderName   string
}

type GeneratedInvalidationBinding[A any] struct {
	identity  RuntimeInvalidationBindingIdentity
	reference operationReference
	bind      func(context.Context, A, map[string]any) (RuntimeInvalidationDependency, error)
}

func GeneratedInvalidationBinder[A any](operationID, dependencyID string, binder any, bind func(context.Context, A, map[string]any) (RuntimeInvalidationDependency, error)) (GeneratedInvalidationBinding[A], error) {
	reference := operationReferenceOf(binder)
	if operationID == "" || dependencyID == "" || reference.entry == 0 || bind == nil {
		return GeneratedInvalidationBinding[A]{}, fmt.Errorf("invalidation binding requires operation, dependency and named binder identities")
	}
	return GeneratedInvalidationBinding[A]{identity: RuntimeInvalidationBindingIdentity{OperationID: operationID, DependencyID: dependencyID, BinderName: reference.name}, reference: reference, bind: bind}, nil
}

func (binding GeneratedInvalidationBinding[A]) Identity() RuntimeInvalidationBindingIdentity {
	return binding.identity
}
func (binding GeneratedInvalidationBinding[A]) Bind(ctx context.Context, actor A, ownedArguments map[string]any) (RuntimeInvalidationDependency, error) {
	if binding.reference.entry == 0 || binding.bind == nil {
		return RuntimeInvalidationDependency{}, fmt.Errorf("invalidation binding is unavailable")
	}
	return binding.bind(ctx, actor, ownedArguments)
}

type invalidationBindingKey struct{ operation, dependency string }

func GeneratedApplicationInvalidations[A any](bindings ApplicationBindings[A], entries ...GeneratedInvalidationBinding[A]) (ApplicationBindings[A], error) {
	if bindings.generation == (SchemaDigest{}) {
		return ApplicationBindings[A]{}, fmt.Errorf("generated invalidation bindings are unstamped")
	}
	table := make(map[invalidationBindingKey]GeneratedInvalidationBinding[A], len(entries))
	counts := map[string]int{}
	for _, entry := range entries {
		id := entry.identity
		key := invalidationBindingKey{id.OperationID, id.DependencyID}
		if id.OperationID == "" || id.DependencyID == "" || id.BinderName != entry.reference.name || entry.reference.entry == 0 || entry.bind == nil {
			return ApplicationBindings[A]{}, fmt.Errorf("generated invalidation binding is incomplete")
		}
		if _, exists := table[key]; exists {
			return ApplicationBindings[A]{}, fmt.Errorf("generated invalidation binding repeats an operation/dependency pair")
		}
		counts[id.OperationID]++
		if counts[id.OperationID] > invalidationcontract.MaxDependencies {
			return ApplicationBindings[A]{}, fmt.Errorf("generated invalidation dependency inventory exceeds its bound")
		}
		table[key] = entry
	}
	result := bindings
	result.invalidations = table
	return result, nil
}

func (bindings ApplicationBindings[A]) Invalidation(operationID, dependencyID string) (GeneratedInvalidationBinding[A], bool) {
	entry, present := bindings.invalidations[invalidationBindingKey{operationID, dependencyID}]
	return entry, present
}

func (bindings ApplicationBindings[A]) InvalidationInventory() []RuntimeInvalidationBindingIdentity {
	result := make([]RuntimeInvalidationBindingIdentity, 0, len(bindings.invalidations))
	for _, entry := range bindings.invalidations {
		result = append(result, entry.identity)
	}
	slices.SortFunc(result, func(a, b RuntimeInvalidationBindingIdentity) int {
		if order := strings.Compare(a.OperationID, b.OperationID); order != 0 {
			return order
		}
		return strings.Compare(a.DependencyID, b.DependencyID)
	})
	return result
}
