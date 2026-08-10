package events

import (
	"context"
	"sync"
)

type memoryTransport struct {
	mu      sync.Mutex
	buffer  int
	nextID  uint64
	streams map[uint64]*memoryStream
}

type memoryStream struct {
	transport *memoryTransport
	id        uint64
	filter    Subscription
	queue     chan Notice
	stopMu    sync.Mutex
	stop      func() bool
	closed    bool
	once      sync.Once
	err       ErrorCode
}

func NewMemoryTransport(limits MemoryLimits) (EventTransport, error) {
	normalized, err := normalizeMemoryLimits(limits)
	if err != nil {
		return nil, err
	}
	return &memoryTransport{buffer: normalized.Buffer, streams: make(map[uint64]*memoryStream)}, nil
}

func (transport *memoryTransport) TransportCapabilities() TransportCapabilities {
	capabilities, err := NewTransportCapabilities("golem.memory.v1", TransportScopeProcessLocal, false)
	if err != nil {
		panic("events: built-in memory transport has invalid capabilities")
	}
	return capabilities
}

func (transport *memoryTransport) Subscribe(ctx context.Context, subscription Subscription) (Stream, error) {
	if ctx == nil || subscription == (Subscription{}) || !subscription.Valid() {
		return nil, Failure(CodeSubscriptionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, Failure(CodeSubscriptionCancelled)
	}
	transport.mu.Lock()
	transport.nextID++
	stream := &memoryStream{transport: transport, id: transport.nextID, filter: subscription, queue: make(chan Notice, transport.buffer)}
	transport.streams[stream.id] = stream
	transport.mu.Unlock()
	stream.installStop(context.AfterFunc(ctx, func() { _ = stream.closeWith(CodeSubscriptionCancelled) }))
	return stream, nil
}

func (transport *memoryTransport) Publish(ctx context.Context, batch EventBatch) error {
	if ctx == nil || !batch.Valid() {
		return Failure(CodeEventTransport)
	}
	if err := ctx.Err(); err != nil {
		return Failure(CodeSubscriptionCancelled)
	}
	events := batch.Events()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	pending := make(map[*memoryStream][]Notice)
	for _, notice := range events {
		for _, stream := range transport.streams {
			if stream.filter.EventSchemaDigest() == notice.EventSchemaDigest() && stream.filter.ModelID() == notice.ModelID() {
				pending[stream] = append(pending[stream], notice)
			}
		}
	}
	for stream, notices := range pending {
		if len(stream.queue)+len(notices) > cap(stream.queue) {
			return Failure(CodeEventTransport)
		}
	}
	for stream, notices := range pending {
		for _, notice := range notices {
			stream.queue <- notice
		}
	}
	return nil
}

func (stream *memoryStream) Recv(ctx context.Context) (Notice, error) {
	if ctx == nil {
		return Notice{}, Failure(CodeSubscriptionCancelled)
	}
	select {
	case <-ctx.Done():
		return Notice{}, Failure(CodeSubscriptionCancelled)
	case notice, ok := <-stream.queue:
		if !ok {
			return Notice{}, Failure(stream.err)
		}
		return notice, nil
	}
}

func (stream *memoryStream) Close() error { return stream.closeWith(CodeEventSourceClosed) }

func (stream *memoryStream) installStop(stop func() bool) {
	if stop == nil {
		return
	}
	stream.stopMu.Lock()
	if stream.closed {
		stream.stopMu.Unlock()
		stop()
		return
	}
	stream.stop = stop
	stream.stopMu.Unlock()
}

func (stream *memoryStream) closeStop() func() bool {
	stream.stopMu.Lock()
	stream.closed = true
	stop := stream.stop
	stream.stop = nil
	stream.stopMu.Unlock()
	return stop
}

func (stream *memoryStream) closeWith(code ErrorCode) error {
	stream.once.Do(func() {
		if stop := stream.closeStop(); stop != nil {
			stop()
		}
		stream.transport.mu.Lock()
		delete(stream.transport.streams, stream.id)
		stream.err = code
		close(stream.queue)
		stream.transport.mu.Unlock()
	})
	return nil
}
