package golem

import "context"

// FrozenPolicy is the opaque P1 result of a freshly built model policy. P2
// owns its representation and semantics.
type FrozenPolicy struct{ _ policyMarker }
type policyMarker struct{}

// Freeze is a type-level ABI shell. P2 replaces the empty representation with
// ordered policy validation and immutable policy construction.
func (*Rules[M]) Freeze(_ ModelID) (FrozenPolicy, error) { return FrozenPolicy{}, nil }

// ActorFrom is the typed context access point used by hook source. P4 owns
// actor storage and execution semantics.
func ActorFrom[A any](_ context.Context) A {
	var actor A
	return actor
}

// Hook request and result shells intentionally contain no operation data in
// P1. Their package-level generated aliases and generic model ownership are the
// ABI; P3/P4 may add payload without renaming them.
type FindOneHookRequest[M any] struct{ _ func() M }
type FindOneHookResult[M any] struct{ _ func() M }
type FindFirstHookRequest[M any] struct{ _ func() M }
type FindFirstHookResult[M any] struct{ _ func() M }
type FindManyHookRequest[M any] struct{ _ func() M }
type FindManyHookResult[M any] struct{ _ func() M }
type CreateHookRequest[M any] struct{ _ func() M }
type CreateHookResult[M any] struct{ _ func() M }
type UpdateHookRequest[M any] struct{ _ func() M }
type UpdateHookResult[M any] struct{ _ func() M }
type DeleteHookRequest[M any] struct{ _ func() M }
type DeleteHookResult[M any] struct{ _ func() M }
type UpdateManyHookRequest[M any] struct{ _ func() M }
type UpdateManyHookResult[M any] struct{ _ func() M }
type DeleteManyHookRequest[M any] struct{ _ func() M }
type DeleteManyHookResult[M any] struct{ _ func() M }

// SetCreate preserves model and value type identity at compile time. P4 owns
// request mutation, validation, and errors.
func SetCreate[M, V any](_ *CreateHookRequest[M], _ ScalarField[M, V], _ V) error { return nil }

type HookOperation string
type HookPhase string

const (
	HookFindOne    HookOperation = "findOne"
	HookFindFirst  HookOperation = "findFirst"
	HookFindMany   HookOperation = "findMany"
	HookCreate     HookOperation = "create"
	HookUpdate     HookOperation = "update"
	HookDelete     HookOperation = "delete"
	HookUpdateMany HookOperation = "updateMany"
	HookDeleteMany HookOperation = "deleteMany"

	HookBefore      HookPhase = "before"
	HookAfter       HookPhase = "after"
	HookAfterCommit HookPhase = "afterCommit"
)

// PolicyFactory is called once per execution with the freshly resolved actor.
type PolicyFactory[A any] func(A) (FrozenPolicy, error)

// PolicyBinding and HookBinding are immutable generated descriptors. Their
// fields remain opaque so P1 exposes no policy or hook execution semantics.
type PolicyBinding[A any] struct {
	model ModelID
	build PolicyFactory[A]
}

type HookBinding[A any] struct {
	model     ModelID
	operation HookOperation
	phase     HookPhase
	invoke    func(context.Context, any) error
	_         func() A
}

type PackageBindings[A any] struct {
	generation SchemaDigest
	policies   []PolicyBinding[A]
	hooks      []HookBinding[A]
}

type ApplicationBindings[A any] struct {
	generation SchemaDigest
	packages   []PackageBindings[A]
}

func GeneratedPolicyBinding[A, M any](model ModelID, build PolicyFactory[A]) PolicyBinding[A] {
	return PolicyBinding[A]{model: model, build: build}
}

func GeneratedBeforeHookBinding[A, M, Request any](model ModelID, operation HookOperation, invoke func(context.Context, *Request) error) HookBinding[A] {
	return HookBinding[A]{model: model, operation: operation, phase: HookBefore, invoke: func(ctx context.Context, value any) error {
		request, ok := value.(*Request)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, request)
	}}
}

func GeneratedAfterHookBinding[A, M, Result any](model ModelID, operation HookOperation, invoke func(context.Context, Result) error) HookBinding[A] {
	return HookBinding[A]{model: model, operation: operation, phase: HookAfter, invoke: func(ctx context.Context, value any) error {
		result, ok := value.(Result)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, result)
	}}
}

func GeneratedAfterCommitHookBinding[A, M, Result any](model ModelID, operation HookOperation, invoke func(context.Context, Result) error) HookBinding[A] {
	binding := GeneratedAfterHookBinding[A, M](model, operation, invoke)
	binding.phase = HookAfterCommit
	return binding
}

func GeneratedPackageBindings[A any](policies []PolicyBinding[A], hooks []HookBinding[A]) PackageBindings[A] {
	return PackageBindings[A]{policies: append([]PolicyBinding[A](nil), policies...), hooks: append([]HookBinding[A](nil), hooks...)}
}

func GeneratedStampedPackageBindings[A any](generation SchemaDigest, policies []PolicyBinding[A], hooks []HookBinding[A]) PackageBindings[A] {
	result := GeneratedPackageBindings(policies, hooks)
	result.generation = generation
	return result
}

func (bindings PackageBindings[A]) GenerationDigest() SchemaDigest { return bindings.generation }

func GeneratedApplicationBindings[A any](expected SchemaDigest, packages ...PackageBindings[A]) (ApplicationBindings[A], error) {
	digests := make([]SchemaDigest, len(packages))
	for index, pkg := range packages {
		digests[index] = pkg.generation
	}
	if err := validateGenerationDigests("bindings", expected, digests); err != nil {
		return ApplicationBindings[A]{}, err
	}
	return ApplicationBindings[A]{generation: expected, packages: append([]PackageBindings[A](nil), packages...)}, nil
}

func (bindings ApplicationBindings[A]) GenerationDigest() SchemaDigest { return bindings.generation }

type generatedBindingError string

func (err generatedBindingError) Error() string { return string(err) }

const errGeneratedBindingType generatedBindingError = "generated binding received an incompatible opaque value"
