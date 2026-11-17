package operationargs

import compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"

// PrepareInvalidation shares bounded typed preparation with the query. Page
// traversal inputs are not subscription arguments; default take exists only to
// satisfy the page's closed preparation schema and must not enter the binder
// arguments or invalidation fingerprint. Ordinary Query take/after are retained.
func (registry *Registry) PrepareInvalidation(operationID compilerir.ExtensionID, supplied map[string]any) (Request, error) {
	if registry == nil {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	operation, present := registry.byID[operationID]
	if !present || operation.Invalidation == nil {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	ordinary := operation.InvalidationArguments()
	if len(supplied) > len(ordinary) || len(supplied) > MaxCanonicalArgumentNodes {
		return Request{}, ErrCanonicalArgumentsLimit
	}
	values := make(map[string]any, len(supplied)+1)
	bytesLeft := MaxCanonicalArgumentBytes
	for name, value := range supplied {
		if len(name) > bytesLeft {
			return Request{}, ErrCanonicalArgumentsLimit
		}
		bytesLeft -= len(name)
		if operation.Pagination != nil && (name == "take" || name == "after") {
			return Request{}, ErrCanonicalArgumentsInvalid
		}
		values[name] = value
	}
	if operation.Pagination != nil {
		values["take"] = int32(operation.Pagination.DefaultTake)
	}
	return registry.PrepareCanonical(operation.Operation, operation.Name, values)
}
