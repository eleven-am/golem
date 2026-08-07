package runtime

import (
	"context"

	"github.com/eleven-am/golem/go/events"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
)

// runtimeEventBinding is the representation-closed decoder capability supplied
// once to a cross-process transport during Open. It owns historical resolution,
// canonical byte validation, limits, and sealed Notice construction.
type runtimeEventBinding struct {
	history *eventSchemaHistory
	limits  eventcodec.Limits
}

func newRuntimeEventBinding(history *eventSchemaHistory, maximumEncodedBytes int) (events.RuntimeBinding, error) {
	if history == nil || maximumEncodedBytes < 0 || maximumEncodedBytes > eventcodec.HardMaxEncodedBytes {
		return nil, events.Failure(events.CodeEventConfig)
	}
	return &runtimeEventBinding{history: history, limits: eventcodec.Limits{MaxEncodedBytes: maximumEncodedBytes}}, nil
}

func (binding *runtimeEventBinding) DecodeNotice(ctx context.Context, encoded []byte) (events.Notice, error) {
	if binding == nil || binding.history == nil || ctx == nil || ctx.Err() != nil || len(encoded) == 0 {
		return events.Notice{}, events.Failure(events.CodeEventCodec)
	}
	envelope, err := eventcodec.Decode(append([]byte(nil), encoded...), binding.history, binding.limits)
	if err != nil {
		return events.Notice{}, events.Failure(events.CodeEventCodec)
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		return events.Notice{}, events.Failure(events.CodeEventCodec)
	}
	return notice, nil
}

var _ events.RuntimeBinding = (*runtimeEventBinding)(nil)
