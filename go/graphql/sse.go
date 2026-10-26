package graphql

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
	"github.com/vektah/gqlparser/v2/ast"
)

const eventStreamMediaType = "text/event-stream"

var (
	sseKeepAliveFrame = []byte(":\n\n")
	errSSEClosed      = errors.New("GraphQL event stream is closed")
)

type sseStream[P any] struct {
	server  *Server[P]
	writer  http.ResponseWriter
	control *http.ResponseController
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
}

type eventStreamNegotiation int

const (
	eventStreamRefused eventStreamNegotiation = iota
	eventStreamAcceptable
	eventStreamPreferred
)

type acceptedMediaRange struct {
	kind, subtype string
	quality       float64
}

func negotiateEventStream(values []string) eventStreamNegotiation {
	ranges := parseAccept(values)
	eventStream := 0.0
	for _, candidate := range ranges {
		if candidate.kind == "text" && candidate.subtype == "event-stream" && candidate.quality > eventStream {
			eventStream = candidate.quality
		}
	}
	if eventStream <= 0 {
		return eventStreamRefused
	}
	for _, subtype := range []string{"json", "graphql-response+json", "graphql+json"} {
		if acceptedQuality(ranges, "application", subtype) >= eventStream {
			return eventStreamAcceptable
		}
	}
	return eventStreamPreferred
}

func parseAccept(values []string) []acceptedMediaRange {
	var ranges []acceptedMediaRange
	for _, value := range values {
		for _, element := range strings.Split(value, ",") {
			parameters := strings.Split(element, ";")
			kind, subtype, found := strings.Cut(strings.ToLower(strings.TrimSpace(parameters[0])), "/")
			kind, subtype = strings.TrimSpace(kind), strings.TrimSpace(subtype)
			if !found || kind == "" || subtype == "" || kind == "*" && subtype != "*" {
				continue
			}
			quality, valid := 1.0, true
			for _, parameter := range parameters[1:] {
				name, raw, _ := strings.Cut(strings.TrimSpace(parameter), "=")
				if !strings.EqualFold(strings.TrimSpace(name), "q") {
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					valid = false
					break
				}
				quality = parsed
			}
			if valid {
				ranges = append(ranges, acceptedMediaRange{kind: kind, subtype: subtype, quality: quality})
			}
		}
	}
	return ranges
}

func acceptedQuality(ranges []acceptedMediaRange, kind, subtype string) float64 {
	quality, specificity := 0.0, -1
	for _, candidate := range ranges {
		rank := -1
		switch {
		case candidate.kind == kind && candidate.subtype == subtype:
			rank = 2
		case candidate.kind == kind && candidate.subtype == "*":
			rank = 1
		case candidate.kind == "*" && candidate.subtype == "*":
			rank = 0
		}
		if rank > specificity || rank == specificity && rank >= 0 && candidate.quality > quality {
			quality, specificity = candidate.quality, rank
		}
	}
	return quality
}

func (server *Server[P]) serveSSE(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		refuseSSE(writer, publicError("METHOD_NOT_ALLOWED", "GraphQL over SSE requires POST"))
		return
	}
	principal, authenticated := server.config.PrincipalFromContext(request.Context())
	if !authenticated {
		refuseSSE(writer, publicError("UNAUTHENTICATED", "authentication is required"))
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, int64(server.limits.MaxRequestBytes))
	decoded, failure := decodeHTTPRequest(request.Body, server.limits)
	if failure != nil {
		refuseSSE(writer, *failure)
		return
	}
	prepared, rejected := server.prepareRequest(decoded, true)
	if rejected != nil {
		refuseSSE(writer, rejected.Errors...)
		return
	}
	server.streamSSE(writer, request, principal, decoded, prepared, refuseSSE)
}

func (server *Server[P]) streamSSE(writer http.ResponseWriter, request *http.Request, principal P, decoded Request, prepared preparedRequest, refuse func(http.ResponseWriter, ...Error)) {
	server.lifecycleMu.Lock()
	if server.shuttingDown {
		server.lifecycleMu.Unlock()
		refuseSSE(writer, publicError("SERVICE_UNAVAILABLE", "GraphQL server is shutting down"))
		return
	}
	server.connections.Add(1)
	server.lifecycleMu.Unlock()
	defer server.connections.Done()
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	stopLifecycle := context.AfterFunc(server.lifecycle, cancel)
	defer stopLifecycle()
	if prepared.Operation.Definition.Operation != ast.Subscription {
		stream := server.openSSE(ctx, cancel, writer)
		defer stream.release()
		if stream.next(server.execute(ctx, principal, decoded, true, &prepared)) == nil {
			_ = stream.complete()
		}
		return
	}
	executor, subscribable := server.executor.(SubscriptionExecutor[P])
	if !subscribable {
		refuse(writer, publicError("SUBSCRIPTIONS_UNAVAILABLE", "GraphQL subscriptions are unavailable"))
		return
	}
	opCtx, observation := observeexec.Begin(ctx, server.config.Observer, server.config.Provider, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLSubscription, observe.PhaseFinish)
	source, err := subscribeSafely(executor, opCtx, principal, prepared.Operation)
	if err != nil {
		finishGraphQLChild(observation, err)
		refuse(writer, PresentError(opCtx, err, nil, server.config.ReportInternalError))
		return
	}
	defer server.closeResponseStream(opCtx, source)
	stream := server.openSSE(opCtx, cancel, writer)
	defer stream.release()
	stopKeepAlive := stream.keepAlive()
	defer stopKeepAlive()
	finishGraphQLChild(observation, stream.relay(source, decoded, prepared))
}

func refuseSSE(writer http.ResponseWriter, failures ...Error) {
	status := http.StatusBadRequest
	if len(failures) != 0 {
		switch failures[0].Extensions["code"] {
		case "METHOD_NOT_ALLOWED":
			status = http.StatusMethodNotAllowed
		case "UNAUTHENTICATED":
			status = http.StatusUnauthorized
		case "FORBIDDEN":
			status = http.StatusForbidden
		case "INTERNAL_SERVER_ERROR":
			status = http.StatusInternalServerError
		case "SERVICE_UNAVAILABLE":
			status = http.StatusServiceUnavailable
		}
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	writeResponse(writer, Response{Errors: failures})
}

func refuseJSON(writer http.ResponseWriter, failures ...Error) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeResponse(writer, Response{Errors: failures})
}

func subscribeSafely[P any](executor SubscriptionExecutor[P], ctx context.Context, principal P, operation Operation) (stream ResponseStream, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stream = nil
			err = errors.New("GraphQL subscription admission panicked")
		}
	}()
	stream, err = executor.Subscribe(ctx, principal, operation)
	if err == nil && stream == nil {
		err = errors.New("GraphQL subscription admission returned no stream")
	}
	return stream, err
}

func (server *Server[P]) closeResponseStream(ctx context.Context, stream ResponseStream) {
	defer func() {
		if recover() != nil {
			server.config.ReportInternalError(ctx, errors.New("GraphQL subscription close panicked"))
		}
	}()
	_ = stream.Close()
}

func (server *Server[P]) openSSE(ctx context.Context, cancel context.CancelFunc, writer http.ResponseWriter) *sseStream[P] {
	stream := &sseStream[P]{server: server, writer: writer, control: http.NewResponseController(writer), ctx: ctx, cancel: cancel}
	header := writer.Header()
	header.Set("Content-Type", eventStreamMediaType)
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	_ = stream.write(nil)
	return stream
}

func (stream *sseStream[P]) relay(source ResponseStream, request Request, prepared preparedRequest) (relayErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			relayErr = errors.New("GraphQL subscription operation panicked")
			stream.server.config.ReportInternalError(stream.ctx, relayErr)
			stream.terminate(publicError("INTERNAL_SERVER_ERROR", "internal server error"))
		}
	}()
	for {
		response, err := source.Recv(stream.ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return stream.complete()
			}
			if stream.ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			stream.terminate(PresentError(stream.ctx, err, nil, stream.server.config.ReportInternalError))
			return err
		}
		serialized := stream.server.selectionOrdered(response, prepared.Operation)
		if stream.server.executable != nil && response.Data != nil {
			serialized = stream.server.executePrepared(stream.ctx, request, prepared.Operation.Document, prepared.Operation.Definition, prepared.Operation.Variables, response)
		}
		if err := stream.next(serialized); err != nil {
			return err
		}
	}
}

func (stream *sseStream[P]) next(response Response) error {
	payload, err := encodeResponse(response)
	if err != nil {
		stream.terminate(publicError("INTERNAL_SERVER_ERROR", "internal server error"))
		return err
	}
	return stream.write(sseEventFrame("next", payload))
}

func (stream *sseStream[P]) terminate(failure Error) {
	payload, err := encodeResponse(Response{Errors: []Error{failure}})
	if err != nil || stream.write(sseEventFrame("next", payload)) != nil {
		return
	}
	_ = stream.complete()
}

func (stream *sseStream[P]) complete() error {
	return stream.write(sseEventFrame("complete", nil))
}

func (stream *sseStream[P]) keepAlive() func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(stream.server.eventLimits.KeepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-stream.ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if stream.write(sseKeepAliveFrame) != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

func (stream *sseStream[P]) write(frame []byte) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return errSSEClosed
	}
	_ = stream.control.SetWriteDeadline(time.Now().Add(stream.server.eventLimits.ShutdownGrace))
	if len(frame) != 0 {
		if _, err := stream.writer.Write(frame); err != nil {
			stream.fail()
			return err
		}
	}
	if err := stream.control.Flush(); err != nil {
		stream.fail()
		return err
	}
	return nil
}

func (stream *sseStream[P]) fail() {
	stream.closed = true
	stream.cancel()
}

func (stream *sseStream[P]) release() {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.closed = true
	_ = stream.control.SetWriteDeadline(time.Time{})
}

func sseEventFrame(event string, data []byte) []byte {
	var frame bytes.Buffer
	frame.Grow(len(event) + len(data) + 16)
	frame.WriteString("event: ")
	frame.WriteString(event)
	frame.WriteByte('\n')
	if len(data) != 0 {
		for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
			frame.WriteString("data: ")
			frame.Write(line)
			frame.WriteByte('\n')
		}
	}
	frame.WriteByte('\n')
	return frame.Bytes()
}
