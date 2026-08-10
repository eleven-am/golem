package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
)

const graphqlTransportWS = "graphql-transport-ws"

var errWSProtocolMessage = errors.New("invalid graphql-transport-ws message")

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type wsOperation struct {
	cancel context.CancelFunc
	stop   func() bool
	stream ResponseStream
}

type wsConnection[P any] struct {
	server     *Server[P]
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	principal  P
	writeMu    sync.Mutex
	operations map[string]wsOperation
	mu         sync.Mutex
	ops        sync.WaitGroup
}

func isWebSocketUpgrade(request *http.Request) bool {
	return request != nil && websocket.IsWebSocketUpgrade(request)
}

func (server *Server[P]) serveWebSocket(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !containsProtocol(websocket.Subprotocols(request), graphqlTransportWS) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusBadRequest)
		writeResponse(writer, Response{Errors: []Error{publicError("WEBSOCKET_PROTOCOL_UNSUPPORTED", "graphql-transport-ws is required")}})
		return
	}
	executor, ok := server.executor.(SubscriptionExecutor[P])
	if !ok {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusBadRequest)
		writeResponse(writer, Response{Errors: []Error{publicError("SUBSCRIPTIONS_UNAVAILABLE", "GraphQL subscriptions are unavailable")}})
		return
	}
	server.lifecycleMu.Lock()
	if server.shuttingDown {
		server.lifecycleMu.Unlock()
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	server.connections.Add(1)
	server.lifecycleMu.Unlock()
	defer server.connections.Done()
	upgrader := websocket.Upgrader{Subprotocols: []string{graphqlTransportWS}}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(request.Context())
	stopLifecycle := context.AfterFunc(server.lifecycle, func() {
		cancel()
		stateDeadline := time.Now().Add(time.Second)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"), stateDeadline)
		_ = conn.Close()
	})
	defer stopLifecycle()
	defer cancel()
	state := &wsConnection[P]{server: server, conn: conn, ctx: ctx, cancel: cancel, operations: map[string]wsOperation{}}
	defer state.closeOperations()
	conn.SetReadLimit(int64(server.eventLimits.ConnectionInitBytes + 512))
	_ = conn.SetReadDeadline(time.Now().Add(server.eventLimits.ConnectionInitTimeout))
	message, err := state.read()
	if err != nil {
		if errors.Is(err, errWSProtocolMessage) {
			state.close(4400, "invalid protocol message")
		} else {
			state.close(4408, "connection initialisation timeout")
		}
		return
	}
	if !validWSInitialMessage(message, server.eventLimits.ConnectionInitBytes) {
		state.close(4400, "invalid connection initialisation")
		return
	}
	initPayload := message.Payload
	if len(initPayload) == 0 {
		initPayload = json.RawMessage("null")
	}
	derived := ctx
	if server.config.WebSocketInit != nil {
		derived, err = server.config.WebSocketInit(ctx, append(json.RawMessage(nil), initPayload...))
		if err != nil || derived == nil {
			state.close(4403, "connection initialisation forbidden")
			return
		}
	}
	principal, authenticated := server.config.PrincipalFromContext(derived)
	if !authenticated {
		state.close(4401, "authentication is required")
		return
	}
	state.ctx, state.principal = derived, principal
	_ = conn.SetReadDeadline(time.Time{})
	conn.SetReadLimit(int64(server.limits.MaxRequestBytes))
	if err := state.write(wsMessage{Type: "connection_ack"}); err != nil {
		return
	}
	for {
		message, err = state.read()
		if err != nil {
			if errors.Is(err, errWSProtocolMessage) {
				state.close(4400, "invalid protocol message")
			}
			return
		}
		if !validWSReadyMessageShape(message) {
			if message.Type == "connection_init" {
				state.close(4429, "too many initialisation requests")
			} else {
				state.close(4400, "invalid protocol message")
			}
			return
		}
		switch message.Type {
		case "ping":
			if err := state.write(wsMessage{Type: "pong", Payload: cloneRaw(message.Payload)}); err != nil {
				return
			}
		case "pong":
		case "connection_init":
			state.close(4429, "too many initialisation requests")
			return
		case "subscribe":
			state.mu.Lock()
			_, duplicate := state.operations[message.ID]
			full := len(state.operations) >= server.eventLimits.MaxSubscriptionsPerConnection
			state.mu.Unlock()
			if duplicate {
				state.close(4409, "subscriber already exists")
				return
			}
			if full {
				state.close(4400, "subscription limit exceeded")
				return
			}
			requestValue, decodeErr := decodeWSRequest(message.Payload, server.limits.MaxVariableBytes)
			if decodeErr != nil {
				state.operationError(message.ID, publicError("BAD_REQUEST", "invalid GraphQL subscription request"))
				continue
			}
			prepared, failure := server.prepareRequest(requestValue, false)
			if !validWSSubscriptionRequest(prepared, failure) {
				failureValue := publicError("GRAPHQL_VALIDATION_FAILED", "GraphQL subscription validation failed")
				if failure != nil && len(failure.Errors) != 0 {
					failureValue = failure.Errors[0]
				}
				state.operationError(message.ID, failureValue)
				continue
			}
			opCtx, opCancel := context.WithCancel(state.ctx)
			opCtx, subscriptionObservation := observeexec.Begin(opCtx, server.config.Observer, server.config.Provider, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLSubscription, observe.PhaseFinish)
			stopOperationLifecycle := context.AfterFunc(ctx, opCancel)
			stream, subscribeErr := executor.Subscribe(opCtx, principal, prepared.Operation)
			if subscribeErr != nil {
				finishGraphQLChild(subscriptionObservation, subscribeErr)
				stopOperationLifecycle()
				opCancel()
				state.operationError(message.ID, PresentError(opCtx, subscribeErr, nil, server.config.ReportInternalError))
				continue
			}
			state.mu.Lock()
			state.operations[message.ID] = wsOperation{cancel: opCancel, stop: stopOperationLifecycle, stream: stream}
			state.mu.Unlock()
			state.ops.Add(1)
			go func(id string, requestValue Request, prepared preparedRequest, stream ResponseStream, opCtx context.Context, observation *observeexec.Span) {
				defer state.ops.Done()
				state.runOperation(id, requestValue, prepared, stream, opCtx, observation)
			}(message.ID, requestValue, prepared, stream, opCtx, subscriptionObservation)
		case "complete":
			state.stopOperation(message.ID)
		}
	}
}

func validWSInitialMessage(message wsMessage, payloadLimit int) bool {
	return message.Type == "connection_init" && message.ID == "" && len(message.Payload) <= payloadLimit
}

func validWSReadyMessageShape(message wsMessage) bool {
	switch message.Type {
	case "ping", "pong":
		return message.ID == ""
	case "connection_init":
		return message.ID == ""
	case "subscribe":
		return message.ID != ""
	case "complete":
		return message.ID != "" && len(message.Payload) == 0
	default:
		return false
	}
}

func validWSSubscriptionRequest(prepared preparedRequest, failure *Response) bool {
	return failure == nil && prepared.Operation.Definition != nil && prepared.Operation.Definition.Operation == ast.Subscription
}

func (state *wsConnection[P]) runOperation(id string, request Request, prepared preparedRequest, stream ResponseStream, ctx context.Context, observation *observeexec.Span) {
	var operationErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			operationErr = errors.New("GraphQL subscription operation panicked")
			state.server.config.ReportInternalError(ctx, errors.New("GraphQL subscription operation panicked"))
			state.operationError(id, publicError("INTERNAL_SERVER_ERROR", "internal server error"))
		}
		finishGraphQLChild(observation, operationErr)
	}()
	defer state.finishOperation(id)
	for {
		response, err := stream.Recv(ctx)
		if err != nil {
			operationErr = err
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				state.operationError(id, PresentError(ctx, err, nil, state.server.config.ReportInternalError))
				return
			}
			if errors.Is(err, io.EOF) {
				operationErr = nil
				_ = state.write(wsMessage{ID: id, Type: "complete"})
			}
			return
		}
		serialized := response
		if state.server.executable != nil && response.Data != nil {
			serialized = state.server.executePrepared(ctx, request, prepared.Operation.Document, prepared.Operation.Definition, prepared.Operation.Variables, response)
		}
		payload, err := json.Marshal(serialized)
		if err != nil {
			operationErr = err
			state.operationError(id, publicError("INTERNAL_SERVER_ERROR", "internal server error"))
			return
		}
		if err := state.write(wsMessage{ID: id, Type: "next", Payload: payload}); err != nil {
			operationErr = err
			return
		}
	}
}

func (state *wsConnection[P]) read() (wsMessage, error) {
	kind, payload, err := state.conn.ReadMessage()
	if err != nil {
		return wsMessage{}, err
	}
	if kind != websocket.TextMessage {
		return wsMessage{}, errWSProtocolMessage
	}
	return decodeWSMessage(payload)
}

func decodeWSMessage(payload []byte) (wsMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message wsMessage
	if err := decoder.Decode(&message); err != nil || message.Type == "" {
		return wsMessage{}, errWSProtocolMessage
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return wsMessage{}, errWSProtocolMessage
	}
	return message, nil
}

func (state *wsConnection[P]) write(message wsMessage) error {
	state.writeMu.Lock()
	defer state.writeMu.Unlock()
	_ = state.conn.SetWriteDeadline(time.Now().Add(state.server.eventLimits.ShutdownGrace))
	return state.conn.WriteJSON(message)
}

func (state *wsConnection[P]) close(code int, reason string) {
	state.writeMu.Lock()
	defer state.writeMu.Unlock()
	_ = state.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
}

func (state *wsConnection[P]) operationError(id string, failure Error) {
	payload, _ := json.Marshal([]Error{failure})
	_ = state.write(wsMessage{ID: id, Type: "error", Payload: payload})
}

func (state *wsConnection[P]) stopOperation(id string) { state.finishOperation(id) }

func (state *wsConnection[P]) finishOperation(id string) {
	state.mu.Lock()
	operation, present := state.operations[id]
	if present {
		delete(state.operations, id)
	}
	state.mu.Unlock()
	if !present {
		return
	}
	if operation.stop != nil {
		operation.stop()
	}
	if operation.cancel != nil {
		operation.cancel()
	}
	if operation.stream != nil {
		state.closeStream(operation.stream)
	}
}

func (state *wsConnection[P]) closeOperations() {
	state.mu.Lock()
	operations := state.operations
	state.operations = map[string]wsOperation{}
	state.mu.Unlock()
	for _, operation := range operations {
		if operation.stop != nil {
			operation.stop()
		}
		if operation.cancel != nil {
			operation.cancel()
		}
		if operation.stream != nil {
			state.closeStream(operation.stream)
		}
	}
	state.ops.Wait()
}

func (state *wsConnection[P]) closeStream(stream ResponseStream) {
	defer func() {
		if recover() != nil {
			state.server.config.ReportInternalError(state.ctx, errors.New("GraphQL subscription close panicked"))
		}
	}()
	_ = stream.Close()
}

func decodeWSRequest(payload json.RawMessage, variableLimit int) (Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope requestEnvelope
	if err := decoder.Decode(&envelope); err != nil || envelope.Query == "" {
		return Request{}, errors.New("invalid request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Request{}, errors.New("invalid request")
	}
	variables := map[string]any{}
	if len(envelope.Variables) != 0 && string(envelope.Variables) != "null" {
		if len(envelope.Variables) > variableLimit {
			return Request{}, errors.New("variables exceed limit")
		}
		values := json.NewDecoder(bytes.NewReader(envelope.Variables))
		values.UseNumber()
		if err := values.Decode(&variables); err != nil {
			return Request{}, err
		}
	}
	return Request{Query: envelope.Query, OperationName: envelope.OperationName, Variables: variables}, nil
}

func containsProtocol(protocols []string, expected string) bool {
	for _, protocol := range protocols {
		if protocol == expected {
			return true
		}
	}
	return false
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

// Shutdown owns WebSocket subscriptions only. It never closes the application
// event transport or publisher.
func (server *Server[P]) Shutdown(ctx context.Context) (resultErr error) {
	if server == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("GraphQL shutdown context is required")
	}
	_, observation := observeexec.Begin(ctx, server.config.Observer, server.config.Provider, golem.ModelID{}, observe.KindShutdown, observe.OperationShutdownHTTP, observe.PhaseClose)
	defer func() { finishGraphQLChild(observation, resultErr) }()
	server.lifecycleMu.Lock()
	if !server.shuttingDown {
		server.shuttingDown = true
		server.cancel()
	}
	server.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { server.connections.Wait(); close(done) }()
	deadlineCtx := ctx
	var cancel context.CancelFunc
	if _, present := ctx.Deadline(); !present {
		deadlineCtx, cancel = context.WithTimeout(ctx, server.eventLimits.ShutdownGrace)
		defer cancel()
	}
	select {
	case <-done:
		return nil
	case <-deadlineCtx.Done():
		return deadlineCtx.Err()
	}
}
