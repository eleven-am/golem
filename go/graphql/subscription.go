package graphql

import (
	"context"
	"fmt"
	"sync"

	"github.com/eleven-am/golem/go/golem"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	"github.com/vektah/gqlparser/v2/ast"
)

// CallerEventExecution is the generated caller-only capability used by P7.
// System and transaction clients deliberately do not implement it.
type CallerEventExecution interface {
	SubscribeFrozenEvents(context.Context, golem.FrozenReadRequest, bool) (EventStream, error)
}

type GeneratedEvent struct {
	metadata      golem.EventMetadata
	identity      []any
	entity        golem.RuntimeModelRow
	entityPresent bool
}

func NewGeneratedEvent(metadata golem.EventMetadata, identity []any, entity *golem.RuntimeModelRow) (GeneratedEvent, error) {
	if metadata.ModelID() == (golem.ModelID{}) || len(identity) == 0 {
		return GeneratedEvent{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: generated event is incomplete")
	}
	result := GeneratedEvent{metadata: metadata, identity: append([]any(nil), identity...)}
	if entity != nil {
		result.entity, result.entityPresent = *entity, true
	}
	return result, nil
}

type EventStream interface {
	Recv(context.Context) (GeneratedEvent, error)
	Close() error
}

type generatedEventStream[E any] struct {
	source golem.EventStream[E]
	adapt  func(E) (GeneratedEvent, error)
}

func AdaptGeneratedEventStream[E any](source golem.EventStream[E], adapt func(E) (GeneratedEvent, error)) (EventStream, error) {
	if source == nil || adapt == nil {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: generated event stream adapter is incomplete")
	}
	return &generatedEventStream[E]{source: source, adapt: adapt}, nil
}

func (stream *generatedEventStream[E]) Recv(ctx context.Context) (GeneratedEvent, error) {
	if stream == nil || stream.source == nil || stream.adapt == nil {
		return GeneratedEvent{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: generated event stream is unavailable")
	}
	value, err := stream.source.Recv(ctx)
	if err != nil {
		return GeneratedEvent{}, err
	}
	return stream.adapt(value)
}

func (stream *generatedEventStream[E]) Close() error {
	if stream == nil || stream.source == nil {
		return nil
	}
	return stream.source.Close()
}

type ResponseStream interface {
	Recv(context.Context) (Response, error)
	Close() error
}

type SubscriptionExecutor[P any] interface {
	Subscribe(context.Context, P, Operation) (ResponseStream, error)
}

type generatedResponseStream[P any] struct {
	executor *generatedExecutor[P]
	root     graphqloperation.EventRoot
	source   EventStream
	once     sync.Once
	closeErr error
}

func (executor *generatedExecutor[P]) Subscribe(ctx context.Context, principal P, operation Operation) (ResponseStream, error) {
	if executor == nil || executor.compiler == nil || executor.begin == nil || operation.Definition == nil || operation.Definition.Operation != ast.Subscription {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: subscription executor is unavailable")
	}
	compiled, err := executor.compiler.Compile(operation.Document, operation.Definition, operation.Variables)
	if err != nil || compiled.Event == nil {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: GraphQL subscription could not be bound")
	}
	caller, err := executor.begin(ctx, principal)
	if err != nil || caller == nil {
		return nil, errOr(err, "caller execution is unavailable")
	}
	capability, ok := caller.(CallerEventExecution)
	if !ok {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_CONFIG: generated caller has no event capability")
	}
	source, err := capability.SubscribeFrozenEvents(ctx, compiled.Event.FrozenRead, compiled.Event.EntitySelected)
	if err != nil {
		return nil, err
	}
	return &generatedResponseStream[P]{executor: executor, root: *compiled.Event, source: source}, nil
}

func (stream *generatedResponseStream[P]) Recv(ctx context.Context) (Response, error) {
	if stream == nil || stream.executor == nil || stream.source == nil {
		return Response{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: subscription response stream is unavailable")
	}
	event, err := stream.source.Recv(ctx)
	if err != nil {
		return Response{}, err
	}
	computed, err := newComputedExecution(stream.executor.compilation, stream.executor.computed, stream.executor.maxComputedBatch)
	if err != nil {
		return Response{}, err
	}
	defer computed.close()
	var entity *golem.RuntimeModelRow
	if event.entityPresent {
		entity = &event.entity
	}
	prepared, failures, err := stream.executor.compiler.EncodeEventWithComputedPartial(ctx, stream.root, event.metadata, event.identity, entity, computed.resolve)
	if err != nil {
		return Response{}, err
	}
	response := Response{Data: map[string]any{stream.root.ResponseName: prepared}}
	response.Errors = appendComputedFailures(ctx, response.Errors, failures, stream.executor.report)
	return response, nil
}

func (stream *generatedResponseStream[P]) Close() error {
	if stream == nil {
		return nil
	}
	stream.once.Do(func() {
		if stream.source != nil {
			stream.closeErr = stream.source.Close()
		}
	})
	return stream.closeErr
}
