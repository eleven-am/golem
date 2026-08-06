package graphql

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

// ComputedArgument is one already-coerced argument supplied to generated
// application glue. Value has the exact declared Go scalar/list representation.
type ComputedArgument struct {
	Name  string
	Value any
}

// ComputedRequest contains only the masked dependency row and typed arguments.
// It deliberately carries no caller, database, system, or transaction handle.
type ComputedRequest struct {
	Parent    golem.RuntimeModelRow
	Arguments []ComputedArgument
}

type ComputedBatchParent struct {
	key    string
	parent golem.RuntimeModelRow
}

func (value ComputedBatchParent) CacheKey() string              { return value.key }
func (value ComputedBatchParent) Parent() golem.RuntimeModelRow { return value.parent }

type ComputedBatchResult struct {
	Value any
	Err   error
}

type ComputedResolveFunc func(context.Context, ComputedRequest) (any, error)
type ComputedCacheKeyFunc func(context.Context, ComputedRequest) (key string, present bool, err error)
type ComputedBatchFunc func(context.Context, []ComputedBatchParent, []ComputedArgument) (map[string]ComputedBatchResult, error)

// ComputedBinding is an opaque generated binding. Constructors are the only
// way to create one, keeping the executor-facing capability closed and small.
type ComputedBinding struct {
	extensionID string
	resolve     ComputedResolveFunc
	cacheKey    ComputedCacheKeyFunc
	batch       ComputedBatchFunc
}

func BindComputed(extensionID string, resolve ComputedResolveFunc) (ComputedBinding, error) {
	if extensionID == "" || resolve == nil {
		return ComputedBinding{}, fmt.Errorf("GraphQL computed binding requires identity and resolver")
	}
	return ComputedBinding{extensionID: extensionID, resolve: resolve}, nil
}

func BindBatchedComputed(extensionID string, cacheKey ComputedCacheKeyFunc, batch ComputedBatchFunc) (ComputedBinding, error) {
	if extensionID == "" || cacheKey == nil || batch == nil {
		return ComputedBinding{}, fmt.Errorf("GraphQL batched-computed binding requires identity, cache-key codec, and loader")
	}
	return ComputedBinding{extensionID: extensionID, cacheKey: cacheKey, batch: batch}, nil
}

func (binding ComputedBinding) batched() bool { return binding.batch != nil }
