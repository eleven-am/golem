package graphql

import (
	"context"
	"fmt"
	"sync"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
)

// GeneratedInvalidationBindingSpec identifies one statically registered binder.
// The actual function identity is retained independently by golem's binding.
type GeneratedInvalidationBindingSpec struct {
	OperationID, DependencyID, BinderPackage, BinderName string
}

func BindGeneratedInvalidationRows[M, A, Args any](bundle golem.SchemaBundle, spec GeneratedInvalidationBindingSpec, descriptor golem.ModelDescriptor[M], binder func(context.Context, A, Args) (golem.Predicate[M], error)) (golem.GeneratedInvalidationBinding[A], error) {
	operation, err := invalidationBindingContract(bundle, spec, fmt.Sprintf("%x", descriptor.Metadata().ModelID()), invalidationcontract.ReadableRows)
	if err != nil || binder == nil {
		return golem.GeneratedInvalidationBinding[A]{}, errOr(err, "invalidation rows binder is nil")
	}
	entry, err := golem.GeneratedInvalidationBinder(spec.OperationID, spec.DependencyID, binder, func(ctx context.Context, actor A, owned map[string]any) (golem.RuntimeInvalidationDependency, error) {
		args, err := invalidationBindingArguments[Args](operation, owned)
		if err != nil {
			return golem.RuntimeInvalidationDependency{}, err
		}
		predicate, err := binder(ctx, actor, args)
		if err != nil {
			return golem.RuntimeInvalidationDependency{}, err
		}
		return golem.RuntimeInvalidationRowsValue(descriptor, predicate)
	})
	if err == nil && entry.Identity().BinderName != spec.BinderPackage+"."+spec.BinderName {
		err = fmt.Errorf("GraphQL invalidation binder function differs from its registered identity")
	}
	if err != nil {
		return golem.GeneratedInvalidationBinding[A]{}, err
	}
	return entry, nil
}

func BindGeneratedInvalidationFacts[M, A, Args any](bundle golem.SchemaBundle, spec GeneratedInvalidationBindingSpec, descriptor golem.ModelDescriptor[M], binder func(context.Context, A, Args) (golem.BoundFactScope[M], error)) (golem.GeneratedInvalidationBinding[A], error) {
	operation, err := invalidationBindingContract(bundle, spec, fmt.Sprintf("%x", descriptor.Metadata().ModelID()), invalidationcontract.PolicyFacts)
	if err != nil || binder == nil {
		return golem.GeneratedInvalidationBinding[A]{}, errOr(err, "invalidation facts binder is nil")
	}
	entry, err := golem.GeneratedInvalidationBinder(spec.OperationID, spec.DependencyID, binder, func(ctx context.Context, actor A, owned map[string]any) (golem.RuntimeInvalidationDependency, error) {
		args, err := invalidationBindingArguments[Args](operation, owned)
		if err != nil {
			return golem.RuntimeInvalidationDependency{}, err
		}
		scope, err := binder(ctx, actor, args)
		if err != nil {
			return golem.RuntimeInvalidationDependency{}, err
		}
		return golem.RuntimeInvalidationFactsValue(descriptor, scope)
	})
	if err == nil && entry.Identity().BinderName != spec.BinderPackage+"."+spec.BinderName {
		err = fmt.Errorf("GraphQL invalidation binder function differs from its registered identity")
	}
	if err != nil {
		return golem.GeneratedInvalidationBinding[A]{}, err
	}
	return entry, nil
}

func invalidationBindingContract(bundle golem.SchemaBundle, spec GeneratedInvalidationBindingSpec, model, kind string) (compilerir.CustomOperationContractIR, error) {
	compilation, err := policyschema.DecodeActive(bundle)
	if err != nil {
		return compilerir.CustomOperationContractIR{}, err
	}
	for _, operation := range compilation.Contract.CustomOperations {
		if string(operation.ExtensionID) != spec.OperationID || operation.Invalidation == nil {
			continue
		}
		for _, dependency := range operation.Invalidation.Dependencies {
			if string(dependency.ID) == spec.DependencyID && string(dependency.Kind) == kind && string(dependency.ModelID) == model && dependency.Binder.PackagePath == spec.BinderPackage && dependency.Binder.Name == spec.BinderName && dependency.Binder.Kind == "invalidation" {
				return operation, nil
			}
		}
	}
	return compilerir.CustomOperationContractIR{}, fmt.Errorf("GraphQL invalidation binding differs from its registered dependency contract")
}

func invalidationBindingArguments[Args any](operation compilerir.CustomOperationContractIR, owned map[string]any) (Args, error) {
	declarations := operation.InvalidationArguments()
	if len(owned) > len(declarations) {
		var zero Args
		return zero, fmt.Errorf("GraphQL invalidation arguments exceed their declaration")
	}
	known := make(map[string]bool, len(declarations))
	for _, declaration := range declarations {
		known[declaration.Name] = true
	}
	for name := range owned {
		if !known[name] {
			var zero Args
			return zero, fmt.Errorf("GraphQL invalidation argument %q is not declared", name)
		}
	}
	ordered := make([]CustomArgument, 0, len(declarations))
	for _, declaration := range declarations {
		value, present := owned[declaration.Name]
		if !present && declaration.PresenceAware {
			value = golem.RuntimeOmittedInputValue()
		}
		ordered = append(ordered, CustomArgument{name: declaration.Name, value: value, present: present})
	}
	return generatedCustomArguments[Args](ordered, nil)
}

// CallerInvalidationExecution is deliberately independent of row-event codecs.
type CallerInvalidationExecution interface {
	GolemGraphQLSubscribeInvalidation(context.Context, string, map[string]any) (golem.InvalidationStream, error)
}

type invalidationResponseStream struct {
	source   golem.InvalidationStream
	once     sync.Once
	closeErr error
}

func (stream *invalidationResponseStream) Recv(ctx context.Context) (Response, error) {
	if stream == nil || stream.source == nil {
		return Response{}, fmt.Errorf("invalidation stream is unavailable")
	}
	if err := stream.source.Wait(ctx); err != nil {
		return Response{}, err
	}
	// Wait has no successful data outcome: even a broken adapter cannot turn
	// an operation invalidation into an observable Boolean payload.
	return Response{}, fmt.Errorf("invalidation stream ended without a terminal reason")
}
func (stream *invalidationResponseStream) Close() error {
	if stream == nil || stream.source == nil {
		return nil
	}
	stream.once.Do(func() { stream.closeErr = stream.source.Close() })
	return stream.closeErr
}
