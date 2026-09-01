// Package graphql provides Golem's bounded caller-only GraphQL transport. The
// generated application supplies the identity-aware executor; this package
// owns HTTP, request isolation, principal extraction, limits, and safe errors.
package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/lexer"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	_ "github.com/vektah/gqlparser/v2/validator/rules"
)

type Limits struct {
	MaxRequestBytes, MaxVariableBytes, MaxTokens, MaxASTNodes, MaxFragments                int
	MaxDepth, MaxSelectedFields, MaxAliases, MaxInputDepth, MaxInputNodes                  int
	MaxListItems, MaxComplexity, MaxPageSize, MaxResolverConcurrency, MaxComputedBatchSize int
	MaxGroups                                                                              int
}

var defaultLimits = Limits{
	MaxRequestBytes:        1 << 20,
	MaxVariableBytes:       512 << 10,
	MaxTokens:              16_384,
	MaxASTNodes:            8_192,
	MaxFragments:           64,
	MaxDepth:               12,
	MaxSelectedFields:      256,
	MaxAliases:             128,
	MaxInputDepth:          16,
	MaxInputNodes:          8_192,
	MaxListItems:           1_000,
	MaxComplexity:          10_000,
	MaxPageSize:            500,
	MaxResolverConcurrency: 64,
	MaxComputedBatchSize:   256,
	MaxGroups:              10_000,
}

var hardLimits = Limits{
	MaxRequestBytes:        8 << 20,
	MaxVariableBytes:       4 << 20,
	MaxTokens:              65_536,
	MaxASTNodes:            32_768,
	MaxFragments:           256,
	MaxDepth:               32,
	MaxSelectedFields:      2_048,
	MaxAliases:             1_024,
	MaxInputDepth:          32,
	MaxInputNodes:          32_768,
	MaxListItems:           10_000,
	MaxComplexity:          100_000,
	MaxPageSize:            1_000,
	MaxResolverConcurrency: 256,
	MaxComputedBatchSize:   4_096,
	MaxGroups:              10_000,
}

type limitBound struct {
	name  string
	field func(*Limits) *int
}

var limitBounds = []limitBound{
	{"MaxRequestBytes", func(limits *Limits) *int { return &limits.MaxRequestBytes }},
	{"MaxVariableBytes", func(limits *Limits) *int { return &limits.MaxVariableBytes }},
	{"MaxTokens", func(limits *Limits) *int { return &limits.MaxTokens }},
	{"MaxASTNodes", func(limits *Limits) *int { return &limits.MaxASTNodes }},
	{"MaxFragments", func(limits *Limits) *int { return &limits.MaxFragments }},
	{"MaxDepth", func(limits *Limits) *int { return &limits.MaxDepth }},
	{"MaxSelectedFields", func(limits *Limits) *int { return &limits.MaxSelectedFields }},
	{"MaxAliases", func(limits *Limits) *int { return &limits.MaxAliases }},
	{"MaxInputDepth", func(limits *Limits) *int { return &limits.MaxInputDepth }},
	{"MaxInputNodes", func(limits *Limits) *int { return &limits.MaxInputNodes }},
	{"MaxListItems", func(limits *Limits) *int { return &limits.MaxListItems }},
	{"MaxComplexity", func(limits *Limits) *int { return &limits.MaxComplexity }},
	{"MaxPageSize", func(limits *Limits) *int { return &limits.MaxPageSize }},
	{"MaxResolverConcurrency", func(limits *Limits) *int { return &limits.MaxResolverConcurrency }},
	{"MaxComputedBatchSize", func(limits *Limits) *int { return &limits.MaxComputedBatchSize }},
	{"MaxGroups", func(limits *Limits) *int { return &limits.MaxGroups }},
}

func NormalizeLimits(input Limits) (Limits, error) {
	result := input
	defaults, maximums := defaultLimits, hardLimits
	for _, bound := range limitBounds {
		value := bound.field(&result)
		maximum := *bound.field(&maximums)
		if *value == 0 {
			*value = *bound.field(&defaults)
		}
		if *value < 1 || *value > maximum {
			return Limits{}, fmt.Errorf("GraphQL limit %s is %d, outside the portable range 1..%d", bound.name, *value, maximum)
		}
	}
	return result, nil
}

type Config[P any] struct {
	PrincipalFromContext func(context.Context) (P, bool)
	Limits               Limits
	Introspection        bool
	ContractFingerprint  golem.SchemaDigest
	ReportInternalError  func(context.Context, error)
	ExecutableSchema     gqlgengraphql.ExecutableSchema
	EventLimits          events.Limits
	Observer             observe.Observer
	Provider             golem.Provider
	WebSocketInit        func(context.Context, json.RawMessage) (context.Context, error)
}

type Operation struct {
	Document   *ast.QueryDocument
	Definition *ast.OperationDefinition
	Variables  map[string]any
}

type Error struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions"`
}
type Response struct {
	Data   any     `json:"data,omitempty"`
	Errors []Error `json:"errors,omitempty"`
}

type Executor[P any] interface {
	Execute(context.Context, P, Operation) Response
}

type Server[P any] struct {
	schema              *ast.Schema
	sdl                 string
	config              Config[P]
	limits              Limits
	executor            Executor[P]
	concurrency         chan struct{}
	contractFingerprint golem.SchemaDigest
	executable          gqlgengraphql.ExecutableSchema
	eventLimits         events.Limits
	lifecycle           context.Context
	cancel              context.CancelFunc
	connections         sync.WaitGroup
	lifecycleMu         sync.Mutex
	shuttingDown        bool
}

func NewServer[P any](sdl string, config Config[P], executor Executor[P]) (*Server[P], error) {
	if config.PrincipalFromContext == nil {
		return nil, fmt.Errorf("GraphQL PrincipalFromContext is required")
	}
	if config.ReportInternalError == nil {
		return nil, fmt.Errorf("GraphQL ReportInternalError is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("GraphQL executor is required")
	}
	limits, err := NormalizeLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	eventLimits, err := events.NormalizeLimits(config.EventLimits)
	if err != nil {
		return nil, err
	}
	schema, err := validator.LoadSchema(validator.Prelude, &ast.Source{Name: "golem.graphql", Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("invalid generated GraphQL schema: %w", err)
	}
	if config.ExecutableSchema != nil && config.ExecutableSchema.Schema() == nil {
		return nil, fmt.Errorf("GraphQL executable schema is incomplete")
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &Server[P]{schema: schema, sdl: sdl, config: config, limits: limits, executor: executor, concurrency: make(chan struct{}, limits.MaxResolverConcurrency), contractFingerprint: config.ContractFingerprint, executable: config.ExecutableSchema, eventLimits: eventLimits, lifecycle: lifecycle, cancel: cancel}, nil
}

func (server *Server[P]) SDL() string {
	if server == nil {
		return ""
	}
	return server.sdl
}
func (server *Server[P]) ContractFingerprint() golem.SchemaDigest {
	if server == nil {
		return golem.SchemaDigest{}
	}
	return server.contractFingerprint
}
func (server *Server[P]) Handler() http.Handler { return http.HandlerFunc(server.serveHTTP) }

type requestEnvelope struct {
	Query         string          `json:"query"`
	OperationName string          `json:"operationName"`
	Variables     json.RawMessage `json:"variables"`
}

type Request struct {
	Query         string
	OperationName string
	Variables     map[string]any
}

func (server *Server[P]) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if isWebSocketUpgrade(request) {
		server.serveWebSocket(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if strings.Contains(strings.ToLower(request.Header.Get("Accept")), "text/event-stream") {
		writer.WriteHeader(http.StatusNotAcceptable)
		writeResponse(writer, Response{Errors: []Error{publicError("SUBSCRIPTION_TRANSPORT_UNSUPPORTED", "GraphQL over SSE is unsupported")}})
		return
	}
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		writeResponse(writer, Response{Errors: []Error{publicError("METHOD_NOT_ALLOWED", "GraphQL requires POST")}})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, int64(server.limits.MaxRequestBytes))
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var envelope requestEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		writeResponse(writer, Response{Errors: []Error{publicError("BAD_REQUEST", "invalid GraphQL request")}})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeResponse(writer, Response{Errors: []Error{publicError("BAD_REQUEST", "invalid GraphQL request")}})
		return
	}
	if envelope.Query == "" || exceedsLexicalTokenLimit(envelope.Query, server.limits.MaxTokens) {
		writeResponse(writer, Response{Errors: []Error{publicError("QUERY_LIMIT_EXCEEDED", "GraphQL query exceeds a configured limit")}})
		return
	}
	variables := map[string]any{}
	if len(envelope.Variables) != 0 && string(envelope.Variables) != "null" {
		if len(envelope.Variables) > server.limits.MaxVariableBytes {
			writeResponse(writer, Response{Errors: []Error{publicError("INPUT_LIMIT_EXCEEDED", "GraphQL variables exceed a configured limit")}})
			return
		}
		variableDecoder := json.NewDecoder(strings.NewReader(string(envelope.Variables)))
		variableDecoder.UseNumber()
		if err := variableDecoder.Decode(&variables); err != nil {
			writeResponse(writer, Response{Errors: []Error{publicError("BAD_USER_INPUT", "invalid GraphQL variables")}})
			return
		}
	}
	principal, ok := server.config.PrincipalFromContext(request.Context())
	if !ok {
		writeResponse(writer, Response{Errors: []Error{publicError("UNAUTHENTICATED", "authentication is required")}})
		return
	}
	writeResponse(writer, server.execute(request.Context(), principal, Request{Query: envelope.Query, OperationName: envelope.OperationName, Variables: variables}, true))
}

// Execute applies the same parse, validation, coercion, limit, isolation, and
// panic boundary used by Handler without re-resolving an already trusted
// principal. It is the direct-execution surface used by tests and embedding.
func (server *Server[P]) Execute(ctx context.Context, principal P, request Request) (response Response) {
	return server.execute(ctx, principal, request, false)
}

// execute receives byteLimitsChecked=true only from serveHTTP, after the raw
// request body and raw variables have already been bounded. Re-encoding those
// values can add canonical JSON envelope/escaping bytes and must not turn an
// accepted HTTP boundary into a second, representation-dependent refusal.
func (server *Server[P]) execute(ctx context.Context, principal P, request Request, byteLimitsChecked bool) (response Response) {
	if server == nil || ctx == nil {
		return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
	}
	var operationSpan *observeexec.Span
	ctx, operationSpan = observeexec.Begin(ctx, server.config.Observer, server.config.Provider, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLRequest, observe.PhaseFinish)
	defer func() {
		if recovered := recover(); recovered != nil {
			server.config.ReportInternalError(ctx, fmt.Errorf("GraphQL executor panic: %v\n%s", recovered, debug.Stack()))
			response = Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
		if operationSpan != nil {
			outcome, reason := graphQLObservationResult(response)
			observeexec.Finish(operationSpan, outcome, reason)
		}
	}()
	preparedOperation, failure := server.prepareRequest(request, byteLimitsChecked)
	if failure != nil {
		return *failure
	}
	if preparedOperation.Operation.Definition.Operation == ast.Subscription {
		return Response{Errors: []Error{publicError("SUBSCRIPTION_TRANSPORT_REQUIRED", "GraphQL subscriptions require graphql-transport-ws")}}
	}
	operation := observe.OperationGraphQLQuery
	if preparedOperation.Operation.Definition.Operation == ast.Mutation {
		operation = observe.OperationGraphQLMutation
	}
	operationSpan.SetOperation(operation)
	select {
	case server.concurrency <- struct{}{}:
		defer func() { <-server.concurrency }()
	case <-ctx.Done():
		return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "request was cancelled")}}
	}
	prepared := server.executor.Execute(ctx, principal, preparedOperation.Operation)
	if server.executable == nil || prepared.Data == nil {
		return prepared
	}
	return server.executePrepared(ctx, request, preparedOperation.Operation.Document, preparedOperation.Operation.Definition, preparedOperation.Operation.Variables, prepared)
}

func graphQLObservationResult(response Response) (observe.Outcome, observe.Reason) {
	if len(response.Errors) == 0 {
		return observe.OutcomeSuccess, observe.ReasonNone
	}
	for _, failure := range response.Errors {
		code, _ := failure.Extensions["code"].(string)
		switch code {
		case "INTERNAL_SERVER_ERROR":
			return observe.OutcomeFailure, observe.ReasonProvider
		case "UNAUTHENTICATED", "FORBIDDEN":
			return observe.OutcomeRefused, observe.ReasonAuthorization
		case "NOT_FOUND":
			return observe.OutcomeRefused, observe.ReasonNotFound
		case "CONFLICT":
			return observe.OutcomeRefused, observe.ReasonConflict
		case "QUERY_LIMIT_EXCEEDED", "INPUT_LIMIT_EXCEEDED":
			return observe.OutcomeRefused, observe.ReasonLimit
		}
	}
	return observe.OutcomeRefused, observe.ReasonInvalidInput
}

type preparedRequest struct{ Operation Operation }

func (server *Server[P]) prepareRequest(request Request, byteLimitsChecked bool) (preparedRequest, *Response) {
	fail := func(code, message string) (preparedRequest, *Response) {
		response := Response{Errors: []Error{publicError(code, message)}}
		return preparedRequest{}, &response
	}
	encodedVariables, err := json.Marshal(request.Variables)
	if err != nil {
		return fail("BAD_USER_INPUT", "GraphQL variables are invalid")
	}
	if !byteLimitsChecked && len(encodedVariables) > server.limits.MaxVariableBytes {
		return fail("INPUT_LIMIT_EXCEEDED", "GraphQL variables exceed a configured limit")
	}
	ownedVariables := map[string]any{}
	if string(encodedVariables) != "null" {
		decoder := json.NewDecoder(bytes.NewReader(encodedVariables))
		decoder.UseNumber()
		if err := decoder.Decode(&ownedVariables); err != nil {
			return fail("BAD_USER_INPUT", "GraphQL variables are invalid")
		}
	}
	request.Variables = ownedVariables
	if !byteLimitsChecked {
		encodedEnvelope, err := json.Marshal(requestEnvelope{Query: request.Query, OperationName: request.OperationName, Variables: encodedVariables})
		if err != nil || len(encodedEnvelope) > server.limits.MaxRequestBytes {
			return fail("QUERY_LIMIT_EXCEEDED", "GraphQL request exceeds a configured limit")
		}
	}
	if request.Query == "" || exceedsLexicalTokenLimit(request.Query, server.limits.MaxTokens) {
		return fail("QUERY_LIMIT_EXCEEDED", "GraphQL query exceeds a configured limit")
	}
	document, err := parser.ParseQuery(&ast.Source{Name: "request.graphql", Input: request.Query})
	if err != nil {
		return fail("GRAPHQL_PARSE_FAILED", "GraphQL parsing failed")
	}
	if validationErrors := validator.Validate(server.schema, document); len(validationErrors) != 0 {
		return fail("GRAPHQL_VALIDATION_FAILED", "GraphQL validation failed")
	}
	if len(document.Fragments) > server.limits.MaxFragments || documentASTNodesExceed(document, server.limits.MaxASTNodes) {
		return fail("QUERY_LIMIT_EXCEEDED", "GraphQL query exceeds a configured limit")
	}
	definition := document.Operations.ForName(request.OperationName)
	if definition == nil {
		return fail("GRAPHQL_VALIDATION_FAILED", "GraphQL operation was not found")
	}
	shape := boundedOperationShape(definition.SelectionSet, document.Fragments, server.limits)
	if shape.exceeded {
		return fail("QUERY_LIMIT_EXCEEDED", "GraphQL query exceeds a configured limit")
	}
	if !server.config.Introspection && shape.introspection {
		return fail("GRAPHQL_VALIDATION_FAILED", "GraphQL validation failed")
	}
	inputNodes, inputDepth, listItems, inputBytes := inputShape(request.Variables, 1)
	if inputNodes > server.limits.MaxInputNodes || inputDepth > server.limits.MaxInputDepth || listItems > server.limits.MaxListItems || inputBytes > server.limits.MaxVariableBytes {
		return fail("INPUT_LIMIT_EXCEEDED", "GraphQL input exceeds a configured limit")
	}
	coerced, err := validator.VariableValues(server.schema, definition, request.Variables)
	if err != nil {
		return fail("BAD_USER_INPUT", "GraphQL variables are invalid")
	}
	complexity, err := operationComplexity(definition.SelectionSet, document.Fragments, coerced, server.limits.MaxPageSize, server.limits.MaxComplexity, map[string]bool{})
	if err != nil || complexity > server.limits.MaxComplexity {
		return fail("QUERY_LIMIT_EXCEEDED", "GraphQL query exceeds a configured limit")
	}
	return preparedRequest{Operation: Operation{Document: document, Definition: definition, Variables: coerced}}, nil
}

func (server *Server[P]) executePrepared(ctx context.Context, request Request, document *ast.QueryDocument, definition *ast.OperationDefinition, variables map[string]any, prepared Response) Response {
	ctx, err := withPreparedOperation(ctx, prepared)
	if err != nil {
		return Response{Errors: []Error{PresentError(ctx, err, nil, server.config.ReportInternalError)}}
	}
	recoverExecution := func(recoverCtx context.Context, recovered any) error {
		server.config.ReportInternalError(recoverCtx, fmt.Errorf("gqlgen executable panic: %v", recovered))
		return errors.New("internal GraphQL execution error")
	}
	opCtx := &gqlgengraphql.OperationContext{
		RawQuery: request.Query, Variables: variables, OperationName: request.OperationName,
		Doc: document, Operation: definition, DisableIntrospection: !server.config.Introspection,
		RecoverFunc: recoverExecution,
		ResolverMiddleware: func(middlewareCtx context.Context, next gqlgengraphql.Resolver) (any, error) {
			return next(middlewareCtx)
		},
		RootResolverMiddleware: func(middlewareCtx context.Context, next gqlgengraphql.RootResolver) gqlgengraphql.Marshaler {
			return next(middlewareCtx)
		},
	}
	ctx = gqlgengraphql.WithOperationContext(ctx, opCtx)
	ctx = gqlgengraphql.WithResponseContext(ctx, gqlgengraphql.DefaultErrorPresenter, recoverExecution)
	handler := server.executable.Exec(ctx)
	if handler == nil {
		return Response{Errors: []Error{PresentError(ctx, errors.New("gqlgen executable returned no response handler"), nil, server.config.ReportInternalError)}}
	}
	result := handler(ctx)
	if result == nil {
		return Response{Errors: []Error{PresentError(ctx, errors.New("gqlgen executable returned no response"), nil, server.config.ReportInternalError)}}
	}
	response := Response{Errors: append([]Error(nil), prepared.Errors...)}
	preparedErrorPaths := make(map[string]struct{}, len(prepared.Errors))
	for _, failure := range prepared.Errors {
		if key, pathErr := preparedPathKey(failure.Path); pathErr == nil && len(failure.Path) != 0 {
			preparedErrorPaths[key] = struct{}{}
		}
	}
	if len(result.Data) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(result.Data))
		decoder.UseNumber()
		if err := decoder.Decode(&response.Data); err != nil {
			return Response{Errors: []Error{PresentError(ctx, fmt.Errorf("decode gqlgen executable response: %w", err), nil, server.config.ReportInternalError)}}
		}
	}
	for _, executableError := range result.Errors {
		path := make([]any, 0, len(executableError.Path))
		for _, element := range executableError.Path {
			switch value := element.(type) {
			case ast.PathName:
				path = append(path, string(value))
			case ast.PathIndex:
				path = append(path, int(value))
			}
		}
		if key, pathErr := preparedPathKey(path); pathErr == nil {
			if _, authoritative := preparedErrorPaths[key]; authoritative {
				continue
			}
		}
		response.Errors = append(response.Errors, PresentError(ctx, fmt.Errorf("gqlgen executable: %s", executableError.Message), path, server.config.ReportInternalError))
	}
	return response
}

func publicError(code, message string) Error {
	return Error{Message: message, Extensions: map[string]any{"code": code}}
}

// PresentError maps Golem's transport-neutral public failures without exposing
// wrapped policy, SQL, driver, file, or stack details. Unknown failures are
// reported through the trusted callback and presented as INTERNAL_SERVER_ERROR.
func PresentError(ctx context.Context, err error, path []any, report func(context.Context, error)) Error {
	var failure *golem.Error
	if errors.As(err, &failure) {
		return Error{Message: failure.Message, Path: append([]any(nil), path...), Extensions: map[string]any{"code": string(failure.Code)}}
	}
	if code, ok := events.CodeOf(err); ok {
		return Error{Message: string(code), Path: append([]any(nil), path...), Extensions: map[string]any{"code": string(code)}}
	}
	if report != nil && err != nil {
		report(ctx, err)
	}
	result := publicError("INTERNAL_SERVER_ERROR", "internal server error")
	result.Path = append([]any(nil), path...)
	return result
}
func writeResponse(writer io.Writer, response Response) { _ = json.NewEncoder(writer).Encode(response) }
func exceedsLexicalTokenLimit(query string, limit int) bool {
	scanner := lexer.New(&ast.Source{Name: "request.graphql", Input: query})
	count := 0
	for {
		token, err := scanner.ReadToken()
		if err != nil || token.Kind == lexer.EOF {
			return false
		}
		if token.Kind == lexer.Comment {
			continue
		}
		count++
		if count > limit {
			return true
		}
	}
}

func inputShape(value any, depth int) (nodes, maximumDepth, listItems, bytes int) {
	nodes, maximumDepth = 1, depth
	switch value := value.(type) {
	case string:
		bytes += len(value)
	case json.Number:
		bytes += len(value)
	case []any:
		listItems = len(value)
		for _, child := range value {
			childNodes, childDepth, childItems, childBytes := inputShape(child, depth+1)
			nodes += childNodes
			if childItems > listItems {
				listItems = childItems
			}
			bytes += childBytes
			if childDepth > maximumDepth {
				maximumDepth = childDepth
			}
		}
	case map[string]any:
		for key, child := range value {
			bytes += len(key)
			childNodes, childDepth, childItems, childBytes := inputShape(child, depth+1)
			nodes += childNodes
			if childItems > listItems {
				listItems = childItems
			}
			bytes += childBytes
			if childDepth > maximumDepth {
				maximumDepth = childDepth
			}
		}
	}
	return
}

func operationComplexity(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, defaultPage, limit int, stack map[string]bool) (int, error) {
	total := 0
	for _, selection := range set {
		switch value := selection.(type) {
		case *ast.Field:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			child, err := operationComplexity(value.SelectionSet, fragments, variables, defaultPage, limit, stack)
			if err != nil {
				return 0, err
			}
			multiplier := 1
			if value.Definition != nil && value.Definition.Type != nil && value.Definition.Type.Elem != nil {
				multiplier = defaultPage
				if multiplier < 1 {
					multiplier = 1
				}
				if take, ok := exactComplexityTake(value.ArgumentMap(variables)["take"]); ok {
					multiplier = take
				}
			}
			cost := 1
			if child != 0 {
				if child > limit/multiplier {
					return limit + 1, nil
				}
				cost += child * multiplier
			}
			if cost > limit-total {
				return limit + 1, nil
			}
			total += cost
		case *ast.InlineFragment:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			cost, err := operationComplexity(value.SelectionSet, fragments, variables, defaultPage, limit-total, stack)
			if err != nil {
				return 0, err
			}
			if cost > limit-total {
				return limit + 1, nil
			}
			total += cost
		case *ast.FragmentSpread:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			fragment := fragments.ForName(value.Name)
			if fragment == nil || stack[value.Name] {
				return 0, fmt.Errorf("GraphQL fragment expansion is invalid")
			}
			stack[value.Name] = true
			cost, err := operationComplexity(fragment.SelectionSet, fragments, variables, defaultPage, limit-total, stack)
			delete(stack, value.Name)
			if err != nil {
				return 0, err
			}
			if cost > limit-total {
				return limit + 1, nil
			}
			total += cost
		default:
			return 0, fmt.Errorf("GraphQL selection kind is invalid")
		}
	}
	return total, nil
}

func graphqlIncluded(directives ast.DirectiveList, variables map[string]any) bool {
	if directive := directives.ForName("skip"); directive != nil {
		if value, ok := directive.ArgumentMap(variables)["if"].(bool); ok && value {
			return false
		}
	}
	if directive := directives.ForName("include"); directive != nil {
		if value, ok := directive.ArgumentMap(variables)["if"].(bool); ok && !value {
			return false
		}
	}
	return true
}

func exactComplexityTake(value any) (int, bool) {
	var result int64
	switch value := value.(type) {
	case int:
		result = int64(value)
	case int32:
		result = int64(value)
	case int64:
		result = value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		result = parsed
	default:
		return 0, false
	}
	if result < 0 {
		result = -result
	}
	if result < 1 || result > int64(^uint(0)>>1) {
		return 0, false
	}
	return int(result), true
}

type operationShapeStats struct {
	nodes, depth, fields, aliases int
	introspection, exceeded       bool
}

func boundedOperationShape(set ast.SelectionSet, fragments ast.FragmentDefinitionList, limits Limits) operationShapeStats {
	stats := operationShapeStats{}
	var walk func(ast.SelectionSet, int, map[string]bool)
	walk = func(current ast.SelectionSet, depth int, stack map[string]bool) {
		if stats.exceeded {
			return
		}
		if depth > stats.depth {
			stats.depth = depth
		}
		if stats.depth > limits.MaxDepth {
			stats.exceeded = true
			return
		}
		for _, selection := range current {
			stats.nodes++
			if stats.nodes > limits.MaxASTNodes {
				stats.exceeded = true
				return
			}
			switch value := selection.(type) {
			case *ast.Field:
				stats.fields++
				if value.Alias != "" && value.Alias != value.Name {
					stats.aliases++
				}
				if value.Name == "__schema" || value.Name == "__type" {
					stats.introspection = true
				}
				if stats.fields > limits.MaxSelectedFields || stats.aliases > limits.MaxAliases {
					stats.exceeded = true
					return
				}
				walk(value.SelectionSet, depth+1, stack)
			case *ast.InlineFragment:
				walk(value.SelectionSet, depth, stack)
			case *ast.FragmentSpread:
				if stack[value.Name] {
					stats.exceeded = true
					return
				}
				fragment := fragments.ForName(value.Name)
				if fragment == nil {
					stats.exceeded = true
					return
				}
				stack[value.Name] = true
				walk(fragment.SelectionSet, depth, stack)
				delete(stack, value.Name)
			default:
				stats.exceeded = true
			}
			if stats.exceeded {
				return
			}
		}
	}
	walk(set, 1, map[string]bool{})
	return stats
}

type astNodeBudget struct{ count, limit int }

func (budget *astNodeBudget) add() bool {
	budget.count++
	return budget.count > budget.limit
}

func documentASTNodesExceed(document *ast.QueryDocument, limit int) bool {
	budget := astNodeBudget{limit: limit}
	for _, operation := range document.Operations {
		if budget.add() || countVariableDefinitions(operation.VariableDefinitions, &budget) || countDirectives(operation.Directives, &budget) || countSelectionAST(operation.SelectionSet, &budget) {
			return true
		}
	}
	for _, fragment := range document.Fragments {
		if budget.add() || countVariableDefinitions(fragment.VariableDefinition, &budget) || countDirectives(fragment.Directives, &budget) || countSelectionAST(fragment.SelectionSet, &budget) {
			return true
		}
	}
	return false
}

func countVariableDefinitions(definitions ast.VariableDefinitionList, budget *astNodeBudget) bool {
	for _, definition := range definitions {
		if budget.add() || countTypeAST(definition.Type, budget) || countValueAST(definition.DefaultValue, budget) || countDirectives(definition.Directives, budget) {
			return true
		}
	}
	return false
}

func countTypeAST(value *ast.Type, budget *astNodeBudget) bool {
	if value == nil {
		return false
	}
	return budget.add() || countTypeAST(value.Elem, budget)
}

func countSelectionAST(set ast.SelectionSet, budget *astNodeBudget) bool {
	for _, selection := range set {
		if budget.add() {
			return true
		}
		switch value := selection.(type) {
		case *ast.Field:
			if countArguments(value.Arguments, budget) || countDirectives(value.Directives, budget) || countSelectionAST(value.SelectionSet, budget) {
				return true
			}
		case *ast.InlineFragment:
			if countDirectives(value.Directives, budget) || countSelectionAST(value.SelectionSet, budget) {
				return true
			}
		case *ast.FragmentSpread:
			if countDirectives(value.Directives, budget) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func countDirectives(directives ast.DirectiveList, budget *astNodeBudget) bool {
	for _, directive := range directives {
		if budget.add() || countArguments(directive.Arguments, budget) {
			return true
		}
	}
	return false
}

func countArguments(arguments ast.ArgumentList, budget *astNodeBudget) bool {
	for _, argument := range arguments {
		if budget.add() || countValueAST(argument.Value, budget) {
			return true
		}
	}
	return false
}

func countValueAST(value *ast.Value, budget *astNodeBudget) bool {
	if value == nil {
		return false
	}
	if budget.add() {
		return true
	}
	for _, child := range value.Children {
		if budget.add() || countValueAST(child.Value, budget) {
			return true
		}
	}
	return false
}
