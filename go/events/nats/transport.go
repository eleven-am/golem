package nats

import (
	"context"
	"encoding/hex"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	natsclient "github.com/nats-io/nats.go"
)

type connection interface {
	Publish(string, []byte) error
	FlushWithContext(context.Context) error
	Subscribe(string, natsclient.MsgHandler) (subscription, error)
	MaxPayload() int64
	IsConnected() bool
	Close()
}

type subscription interface {
	SetPendingLimits(int, int) error
	Unsubscribe() error
}

type coreConnection struct{ connection *natsclient.Conn }

func (connection coreConnection) Publish(subject string, payload []byte) error {
	return connection.connection.Publish(subject, payload)
}
func (connection coreConnection) FlushWithContext(ctx context.Context) error {
	return connection.connection.FlushWithContext(ctx)
}
func (connection coreConnection) Subscribe(subject string, callback natsclient.MsgHandler) (subscription, error) {
	return connection.connection.Subscribe(subject, callback)
}
func (connection coreConnection) MaxPayload() int64 { return connection.connection.MaxPayload() }
func (connection coreConnection) IsConnected() bool { return connection.connection.IsConnected() }
func (connection coreConnection) Close()            { connection.connection.Close() }

// Transport is a caller-owned Core NATS connection. Close is idempotent.
type Transport struct {
	config normalizedConfig
	client connection

	mu            sync.Mutex
	binding       events.RuntimeBinding
	bound         bool
	closed        bool
	streams       map[*stream]struct{}
	publish       sync.Mutex
	once          sync.Once
	available     atomic.Bool
	callbackMu    sync.Mutex
	observationMu sync.Mutex
	observations  []events.Outcome
	observing     bool
}

func (transport *Transport) queueReconnectObservation(outcome events.Outcome) bool {
	transport.observationMu.Lock()
	defer transport.observationMu.Unlock()
	if len(transport.observations) >= maximumReconnectObservations {
		return false
	}
	transport.observations = append(transport.observations, outcome)
	if transport.observing {
		return false
	}
	transport.observing = true
	return true
}

func (transport *Transport) drainReconnectObservations() {
	for {
		transport.observationMu.Lock()
		if len(transport.observations) == 0 {
			transport.observing = false
			transport.observationMu.Unlock()
			return
		}
		outcome := transport.observations[0]
		transport.observations = transport.observations[1:]
		transport.observationMu.Unlock()
		events.Observe(transport.config.Observer, context.Background(), golem.ModelID{}, "", events.ObservationTransportReconnect, outcome, "", 0, 0, 0, 0, 1)
	}
}

func (transport *Transport) markDisconnected() {
	transport.callbackMu.Lock()
	transport.mu.Lock()
	if transport.closed || !transport.available.Load() {
		transport.mu.Unlock()
		transport.callbackMu.Unlock()
		return
	}
	transport.available.Store(false)
	transport.mu.Unlock()
	drain := transport.queueReconnectObservation(events.OutcomeFailure)
	transport.callbackMu.Unlock()
	if drain {
		transport.drainReconnectObservations()
	}
}
func (transport *Transport) markReconnected(maxPayload int64) bool {
	transport.callbackMu.Lock()
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		transport.callbackMu.Unlock()
		return false
	}
	if maxPayload < int64(transport.config.MaxInboundPayloadBytes) {
		transport.closed = true
		transport.mu.Unlock()
		transport.closeStreams(events.CodeEventTransport)
		drain := transport.queueReconnectObservation(events.OutcomeFailure)
		transport.callbackMu.Unlock()
		if drain {
			transport.drainReconnectObservations()
		}
		return false
	}
	if transport.available.Load() {
		transport.mu.Unlock()
		transport.callbackMu.Unlock()
		return true
	}
	transport.available.Store(true)
	transport.mu.Unlock()
	drain := transport.queueReconnectObservation(events.OutcomeSuccess)
	transport.callbackMu.Unlock()
	if drain {
		transport.drainReconnectObservations()
	}
	return true
}
func (transport *Transport) markClosed() {
	transport.callbackMu.Lock()
	transport.mu.Lock()
	alreadyClosed := transport.closed
	transport.closed = true
	transport.mu.Unlock()
	transport.closeStreams(events.CodeEventTransport)
	drain := false
	if !alreadyClosed {
		drain = transport.queueReconnectObservation(events.OutcomeFailure)
	}
	transport.callbackMu.Unlock()
	if drain {
		transport.drainReconnectObservations()
	}
}

type initialDialer struct {
	ctx    context.Context
	dialer net.Dialer

	mu       sync.Mutex
	complete bool
	stops    []func() bool
}

func newInitialDialer(ctx context.Context, timeout time.Duration) *initialDialer {
	return &initialDialer{ctx: ctx, dialer: net.Dialer{Timeout: timeout}}
}

func (dialer *initialDialer) Dial(network, address string) (net.Conn, error) {
	dialer.mu.Lock()
	complete := dialer.complete
	dialer.mu.Unlock()
	if complete {
		return dialer.dialer.Dial(network, address)
	}
	connection, err := dialer.dialer.DialContext(dialer.ctx, network, address)
	if err != nil {
		return nil, err
	}
	dialer.mu.Lock()
	if dialer.complete {
		dialer.mu.Unlock()
		return connection, nil
	}
	stop := context.AfterFunc(dialer.ctx, func() {
		dialer.mu.Lock()
		defer dialer.mu.Unlock()
		if !dialer.complete {
			_ = connection.Close()
		}
	})
	dialer.stops = append(dialer.stops, stop)
	dialer.mu.Unlock()
	return connection, nil
}

func (dialer *initialDialer) finish() {
	dialer.mu.Lock()
	dialer.complete = true
	stops := dialer.stops
	dialer.stops = nil
	for _, stop := range stops {
		stop()
	}
	dialer.mu.Unlock()
}

// Open proves that the supplied provider handle is PostgreSQL before it
// validates or dials any NATS endpoint.
func Open(ctx context.Context, database *provider.Database, config Config) (*Transport, error) {
	if ctx == nil || database == nil || database.Provider() != golem.PostgreSQL || database.UnsafeSQLX() == nil {
		return nil, events.Failure(events.CodeEventConfig)
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	connectTimeout, ok := boundedConnectTimeout(ctx, normalized.ConnectTimeout, len(normalized.URLs))
	if !ok {
		return nil, events.Failure(events.CodeEventTransport)
	}
	transport := &Transport{config: normalized, streams: make(map[*stream]struct{})}
	client, err := connectCore(ctx, normalized, transport, connectTimeout)
	if err != nil {
		return nil, events.Failure(events.CodeEventTransport)
	}
	if ctx.Err() != nil {
		transport.abortOpen(client)
		return nil, events.Failure(events.CodeEventTransport)
	}
	if client.MaxPayload() < int64(normalized.MaxInboundPayloadBytes) {
		transport.abortOpen(client)
		return nil, payloadCeilingFailure(client.MaxPayload(), normalized.MaxInboundPayloadBytes)
	}
	if !transport.installConnectedClient(coreConnection{connection: client}) {
		client.Close()
		return nil, events.Failure(events.CodeEventTransport)
	}
	return transport, nil
}

func connectCore(ctx context.Context, normalized normalizedConfig, transport *Transport, connectTimeout time.Duration) (*natsclient.Conn, error) {
	dialer := newInitialDialer(ctx, connectTimeout)
	client, err := natsclient.Connect(normalized.serverList,
		natsclient.Name("golem.nats.v1"),
		natsclient.Timeout(connectTimeout),
		natsclient.FlusherTimeout(normalized.FlushTimeout),
		natsclient.SkipHostLookup(),
		natsclient.SetCustomDialer(dialer),
		natsclient.ReconnectWait(normalized.ReconnectWait),
		natsclient.MaxReconnects(normalized.MaxReconnects),
		natsclient.ReconnectBufSize(normalized.ReconnectBufferBytes),
		natsclient.DisconnectErrHandler(func(_ *natsclient.Conn, _ error) { transport.markDisconnected() }),
		natsclient.ReconnectHandler(func(connection *natsclient.Conn) {
			if !transport.markReconnected(connection.MaxPayload()) {
				connection.Close()
			}
		}),
		natsclient.ClosedHandler(func(_ *natsclient.Conn) { transport.markClosed() }),
		natsclient.ErrorHandler(transport.handleAsyncError),
	)
	dialer.finish()
	return client, err
}

func (transport *Transport) abortOpen(client *natsclient.Conn) {
	transport.callbackMu.Lock()
	defer transport.callbackMu.Unlock()
	transport.mu.Lock()
	transport.closed = true
	transport.mu.Unlock()
	client.Close()
}

func (transport *Transport) installConnectedClient(client connection) bool {
	transport.callbackMu.Lock()
	defer transport.callbackMu.Unlock()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed || client == nil || !client.IsConnected() {
		return false
	}
	transport.client = client
	transport.available.Store(true)
	return true
}

func boundedConnectTimeout(ctx context.Context, configured time.Duration, servers int) (time.Duration, bool) {
	if ctx == nil || ctx.Err() != nil || configured <= 0 || servers <= 0 {
		return 0, false
	}
	total := configured
	if deadline, present := ctx.Deadline(); present {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, false
		}
		if remaining < total {
			total = remaining
		}
	}
	perServer := total / time.Duration(servers)
	if perServer <= 0 {
		return 0, false
	}
	return perServer, true
}

func payloadCeilingFailure(negotiated int64, configured int) error {
	return events.Failf(events.CodeEventConfig, "MaxInboundPayloadBytes must not exceed the negotiated maximum payload of %d bytes, got %d", negotiated, configured)
}

func newTransportForTest(config Config, client connection) (*Transport, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if client == nil || !client.IsConnected() {
		return nil, events.Failure(events.CodeEventConfig)
	}
	if client.MaxPayload() < int64(normalized.MaxInboundPayloadBytes) {
		return nil, payloadCeilingFailure(client.MaxPayload(), normalized.MaxInboundPayloadBytes)
	}
	transport := &Transport{config: normalized, client: client, streams: make(map[*stream]struct{})}
	transport.available.Store(true)
	return transport, nil
}

func (transport *Transport) TransportCapabilities() events.TransportCapabilities {
	capabilities, err := events.NewTransportCapabilities("golem.nats.v1", events.TransportScopeCrossProcess, false)
	if err != nil {
		panic("events/nats: invalid built-in capabilities")
	}
	return capabilities
}

func (transport *Transport) TransportAvailable() bool {
	return transport != nil && transport.available.Load()
}

func (transport *Transport) MaxEncodedEventBytes() int {
	if transport == nil {
		return 0
	}
	return transport.config.MaxInboundPayloadBytes
}

func (transport *Transport) BindEventRuntime(binding events.RuntimeBinding) error {
	if transport == nil {
		return events.Failure(events.CodeEventConfig)
	}
	if binding == nil {
		return events.Failf(events.CodeEventConfig, "binding must not be nil")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.bound {
		return events.Failf(events.CodeEventConfig, "a RuntimeBinding is already installed")
	}
	if transport.closed {
		return events.Failure(events.CodeEventConfig)
	}
	transport.binding = binding
	transport.bound = true
	return nil
}

func (transport *Transport) Publish(ctx context.Context, batch events.EventBatch) error {
	if transport == nil || ctx == nil || !batch.Valid() || ctx.Err() != nil {
		return events.Failure(events.CodeEventTransport)
	}
	transport.publish.Lock()
	defer transport.publish.Unlock()
	if ctx.Err() != nil {
		return events.Failure(events.CodeEventTransport)
	}
	transport.mu.Lock()
	client, closed := transport.client, transport.closed
	transport.mu.Unlock()
	if closed || client == nil {
		return events.Failure(events.CodeEventTransport)
	}
	for _, notice := range batch.Events() {
		payload := notice.Encoded()
		if len(payload) == 0 || len(payload) > transport.config.MaxInboundPayloadBytes {
			return events.Failure(events.CodeEventTransport)
		}
		if err := client.Publish(transport.subject(notice.EventSchemaDigest(), notice.ModelID()), payload); err != nil {
			return events.Failure(events.CodeEventTransport)
		}
	}
	flushCtx, cancel := context.WithTimeout(ctx, transport.config.FlushTimeout)
	defer cancel()
	if err := client.FlushWithContext(flushCtx); err != nil {
		return events.Failure(events.CodeEventTransport)
	}
	return nil
}

func (transport *Transport) Subscribe(ctx context.Context, requested events.Subscription) (events.Stream, error) {
	if transport == nil || ctx == nil || !requested.Valid() {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	if ctx.Err() != nil {
		return nil, events.Failure(events.CodeSubscriptionCancelled)
	}
	transport.mu.Lock()
	if transport.closed || !transport.bound || transport.client == nil {
		transport.mu.Unlock()
		return nil, events.Failure(events.CodeEventConfig)
	}
	result := newStream(transport, requested, transport.config.StreamBuffer)
	transport.streams[result] = struct{}{}
	client := transport.client
	transport.mu.Unlock()
	upstream, err := client.Subscribe(result.subject, result.deliver)
	if err != nil {
		_ = result.closeWith(events.CodeEventTransport)
		return nil, events.Failure(events.CodeEventTransport)
	}
	result.install(upstream, nil)
	if err := upstream.SetPendingLimits(transport.config.PendingMessages, transport.config.PendingBytes); err != nil {
		_ = upstream.Unsubscribe()
		_ = result.closeWith(events.CodeEventTransport)
		return nil, events.Failure(events.CodeEventTransport)
	}
	result.install(upstream, context.AfterFunc(ctx, func() { _ = result.closeWith(events.CodeSubscriptionCancelled) }))
	flushCtx, cancel := context.WithTimeout(ctx, transport.config.FlushTimeout)
	defer cancel()
	if err := client.FlushWithContext(flushCtx); err != nil {
		_ = result.closeWith(events.CodeEventTransport)
		return nil, events.Failure(events.CodeEventTransport)
	}
	return result, nil
}

func (transport *Transport) subject(eventSchema golem.EventSchemaDigest, model golem.ModelID) string {
	return transport.config.SubjectPrefix + ".g1." + hex.EncodeToString(eventSchema[:]) + "." + hex.EncodeToString(model[:])
}

func (transport *Transport) unregister(target *stream) {
	transport.mu.Lock()
	delete(transport.streams, target)
	transport.mu.Unlock()
}

func (transport *Transport) closeStreams(code events.ErrorCode) {
	if transport == nil {
		return
	}
	transport.available.Store(false)
	transport.mu.Lock()
	streams := make([]*stream, 0, len(transport.streams))
	for active := range transport.streams {
		streams = append(streams, active)
	}
	transport.mu.Unlock()
	for _, active := range streams {
		_ = active.closeWith(code)
	}
}

func (transport *Transport) handleAsyncError(_ *natsclient.Conn, upstream *natsclient.Subscription, _ error) {
	if upstream == nil {
		return
	}
	transport.mu.Lock()
	streams := make([]*stream, 0, len(transport.streams))
	for candidate := range transport.streams {
		streams = append(streams, candidate)
	}
	transport.mu.Unlock()
	var target *stream
	for _, candidate := range streams {
		if candidate.matches(upstream) {
			target = candidate
			break
		}
	}
	if target != nil {
		_ = target.closeWith(events.CodeEventTransport)
	}
}

func (transport *Transport) Close() error {
	if transport == nil {
		return nil
	}
	transport.once.Do(func() {
		transport.callbackMu.Lock()
		transport.mu.Lock()
		transport.closed = true
		client := transport.client
		transport.mu.Unlock()
		transport.closeStreams(events.CodeEventSourceClosed)
		transport.callbackMu.Unlock()
		if client != nil {
			client.Close()
		}
	})
	return nil
}

var _ events.EventTransport = (*Transport)(nil)
var _ events.RuntimeBindableTransport = (*Transport)(nil)
var _ events.CapabilityReporter = (*Transport)(nil)
var _ events.AvailabilityReporter = (*Transport)(nil)
var _ events.PayloadLimitReporter = (*Transport)(nil)
