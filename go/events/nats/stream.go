package nats

import (
	"context"
	"sync"

	"github.com/eleven-am/golem/go/events"
	natsclient "github.com/nats-io/nats.go"
)

type stream struct {
	transport *Transport
	requested events.Subscription
	subject   string
	queue     chan []byte
	done      chan struct{}

	mu          sync.Mutex
	upstream    subscription
	stop        func() bool
	closed      bool
	err         events.ErrorCode
	queuedBytes int
	once        sync.Once
}

func newStream(transport *Transport, requested events.Subscription, capacity int) *stream {
	return &stream{transport: transport, requested: requested, subject: transport.subject(requested.EventSchemaDigest(), requested.ModelID()), queue: make(chan []byte, capacity), done: make(chan struct{})}
}

func (stream *stream) install(upstream subscription, stop func() bool) {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		_ = upstream.Unsubscribe()
		if stop != nil {
			stop()
		}
		return
	}
	stream.upstream = upstream
	stream.stop = stop
	stream.mu.Unlock()
}

func (stream *stream) matches(upstream *natsclient.Subscription) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	actual, ok := stream.upstream.(*natsclient.Subscription)
	return ok && actual == upstream
}

func (stream *stream) deliver(message *natsclient.Msg) {
	if message == nil || message.Subject != stream.subject || len(message.Data) == 0 || len(message.Data) > stream.transport.config.MaxInboundPayloadBytes {
		_ = stream.closeWith(events.CodeEventTransport)
		return
	}
	payload := append([]byte(nil), message.Data...)
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return
	}
	if stream.queuedBytes+len(payload) > stream.transport.config.PendingBytes {
		stream.mu.Unlock()
		_ = stream.closeWith(events.CodeEventTransport)
		return
	}
	select {
	case stream.queue <- payload:
		stream.queuedBytes += len(payload)
		stream.mu.Unlock()
	default:
		stream.mu.Unlock()
		_ = stream.closeWith(events.CodeEventTransport)
	}
}

func (stream *stream) Recv(ctx context.Context) (events.Notice, error) {
	if ctx == nil {
		return events.Notice{}, events.Failure(events.CodeSubscriptionCancelled)
	}
	select {
	case <-stream.done:
		return events.Notice{}, events.Failure(stream.failure())
	default:
	}
	select {
	case <-ctx.Done():
		return events.Notice{}, events.Failure(events.CodeSubscriptionCancelled)
	case <-stream.done:
		return events.Notice{}, events.Failure(stream.failure())
	case payload := <-stream.queue:
		stream.mu.Lock()
		stream.queuedBytes -= len(payload)
		stream.mu.Unlock()
		select {
		case <-stream.done:
			return events.Notice{}, events.Failure(stream.failure())
		default:
		}
		stream.transport.mu.Lock()
		binding := stream.transport.binding
		closed := stream.transport.closed
		stream.transport.mu.Unlock()
		if closed || binding == nil {
			return events.Notice{}, events.Failure(events.CodeEventSourceClosed)
		}
		notice, err := binding.DecodeNotice(ctx, payload)
		if err != nil {
			return events.Notice{}, err
		}
		if notice.EventSchemaDigest() != stream.requested.EventSchemaDigest() || notice.ModelID() != stream.requested.ModelID() {
			_ = stream.closeWith(events.CodeEventTransport)
			return events.Notice{}, events.Failure(events.CodeEventTransport)
		}
		return notice, nil
	}
}

func (stream *stream) failure() events.ErrorCode {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.err == "" {
		return events.CodeEventSourceClosed
	}
	return stream.err
}

func (stream *stream) Close() error { return stream.closeWith(events.CodeEventSourceClosed) }

func (stream *stream) closeWith(code events.ErrorCode) error {
	stream.once.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		stream.err = code
		upstream := stream.upstream
		stop := stream.stop
		stream.upstream = nil
		stream.stop = nil
		close(stream.done)
		stream.mu.Unlock()
		if stop != nil {
			stop()
		}
		if upstream != nil {
			_ = upstream.Unsubscribe()
		}
		stream.transport.unregister(stream)
	})
	return nil
}

var _ events.Stream = (*stream)(nil)
