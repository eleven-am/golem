package graphql

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
)

// CallerMutationExecution is the caller-only mutation capability consumed by
// generated GraphQL. runtime.CallerMutationExecution implements it; System
// deliberately cannot.
type CallerMutationExecution interface {
	ExecuteFrozenMutation(context.Context, golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error)
}

// executeMutationRoots is the narrow integration seam used by the generated
// executor after the complete operation has compiled successfully. The plain
// loop is intentional: GraphQL mutation roots run serially in document order,
// and every call enters P4 independently, so no transaction spans two roots.
func executeMutationRoots(ctx context.Context, compiler *graphqloperation.Compiler, caller CallerMutationExecution, roots []graphqloperation.MutationRoot, report func(context.Context, error)) Response {
	data := make(map[string]any, len(roots))
	response := Response{Data: data}
	for _, root := range roots {
		result, err := caller.ExecuteFrozenMutation(ctx, root.Frozen)
		if err != nil {
			response.Errors = append(response.Errors, PresentError(ctx, err, []any{root.ResponseName}, report))
			response.Data = nil
			return response
		}
		encoded, err := compiler.EncodeMutation(root, result)
		if err != nil {
			response.Errors = append(response.Errors, PresentError(ctx, err, []any{root.ResponseName}, report))
			response.Data = nil
			return response
		}
		data[root.ResponseName] = encoded
	}
	return response
}
