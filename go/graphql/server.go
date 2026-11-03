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
	"math"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
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
	name    string
	field   func(*Limits) *int
	code    string
	subject string
}

const (
	queryLimitCode = "QUERY_LIMIT_EXCEEDED"
	inputLimitCode = "INPUT_LIMIT_EXCEEDED"
)

var limitBounds = []limitBound{
	{"MaxRequestBytes", func(limits *Limits) *int { return &limits.MaxRequestBytes }, queryLimitCode, "GraphQL request exceeds"},
	{"MaxVariableBytes", func(limits *Limits) *int { return &limits.MaxVariableBytes }, inputLimitCode, "GraphQL variables exceed"},
	{"MaxTokens", func(limits *Limits) *int { return &limits.MaxTokens }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxASTNodes", func(limits *Limits) *int { return &limits.MaxASTNodes }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxFragments", func(limits *Limits) *int { return &limits.MaxFragments }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxDepth", func(limits *Limits) *int { return &limits.MaxDepth }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxSelectedFields", func(limits *Limits) *int { return &limits.MaxSelectedFields }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxAliases", func(limits *Limits) *int { return &limits.MaxAliases }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxInputDepth", func(limits *Limits) *int { return &limits.MaxInputDepth }, inputLimitCode, "GraphQL input exceeds"},
	{"MaxInputNodes", func(limits *Limits) *int { return &limits.MaxInputNodes }, inputLimitCode, "GraphQL input exceeds"},
	{"MaxListItems", func(limits *Limits) *int { return &limits.MaxListItems }, inputLimitCode, "GraphQL input exceeds"},
	{"MaxComplexity", func(limits *Limits) *int { return &limits.MaxComplexity }, queryLimitCode, "GraphQL query exceeds"},
	{"MaxPageSize", func(limits *Limits) *int { return &limits.MaxPageSize }, "", ""},
	{"MaxResolverConcurrency", func(limits *Limits) *int { return &limits.MaxResolverConcurrency }, "", ""},
	{"MaxComputedBatchSize", func(limits *Limits) *int { return &limits.MaxComputedBatchSize }, "", ""},
	{"MaxGroups", func(limits *Limits) *int { return &limits.MaxGroups }, "", ""},
}

var (
	limitRequestBytes   = boundNamed("MaxRequestBytes")
	limitVariableBytes  = boundNamed("MaxVariableBytes")
	limitTokens         = boundNamed("MaxTokens")
	limitASTNodes       = boundNamed("MaxASTNodes")
	limitFragments      = boundNamed("MaxFragments")
	limitDepth          = boundNamed("MaxDepth")
	limitSelectedFields = boundNamed("MaxSelectedFields")
	limitAliases        = boundNamed("MaxAliases")
	limitInputDepth     = boundNamed("MaxInputDepth")
	limitInputNodes     = boundNamed("MaxInputNodes")
	limitListItems      = boundNamed("MaxListItems")
	limitComplexity     = boundNamed("MaxComplexity")
	limitSchemaRoots    = &limitBound{name: "MaxSchemaRoots", code: queryLimitCode, subject: "GraphQL query exceeds"}
	limitTypeRoots      = &limitBound{name: "MaxTypeRoots", code: queryLimitCode, subject: "GraphQL query exceeds"}
)

func boundNamed(name string) *limitBound {
	for index := range limitBounds {
		if limitBounds[index].name == name {
			return &limitBounds[index]
		}
	}
	panic("unknown GraphQL limit " + name)
}

func (limits Limits) valueOf(bound *limitBound) int { return *bound.field(&limits) }

func limitError(bound *limitBound, max int, cost *int) Error {
	failure := publicError(bound.code, fmt.Sprintf("%s %s (%d)", bound.subject, bound.name, max))
	if cost != nil {
		failure.Message = fmt.Sprintf("GraphQL query complexity %d exceeds %s (%d)", *cost, bound.name, max)
		failure.Extensions["cost"] = *cost
	}
	failure.Extensions["limit"] = bound.name
	failure.Extensions["max"] = max
	return failure
}

const (
	maxValidationErrors = 8
	maxErrorLocations   = 4
)

var validationTemplates = map[string]string{
	"FieldsOnCorrectType":          "GraphQL field does not exist on the selected type",
	"FragmentsOnCompositeTypes":    "GraphQL fragment must be on an object, interface or union type",
	"KnownArgumentNames":           "GraphQL argument is not defined for this field or directive",
	"KnownDirectives":              "GraphQL directive is unknown or not allowed here",
	"KnownFragmentNames":           "GraphQL fragment is not defined",
	"KnownRootType":                "GraphQL operation type is not supported by this schema",
	"KnownTypeNames":               "GraphQL type is unknown",
	"LoneAnonymousOperation":       "GraphQL anonymous operation must be the only operation",
	"MaxIntrospectionDepth":        "GraphQL introspection selection is nested too deeply",
	"NoFragmentCycles":             "GraphQL fragments must not form a cycle",
	"NoUndefinedVariables":         "GraphQL variable is used but not defined",
	"NoUnusedFragments":            "GraphQL fragment is defined but not used",
	"NoUnusedVariables":            "GraphQL variable is defined but not used",
	"OverlappingFieldsCanBeMerged": "GraphQL fields conflict and cannot be merged",
	"PossibleFragmentSpreads":      "GraphQL fragment cannot be spread in this position",
	"ProvidedRequiredArguments":    "GraphQL required argument is missing",
	"ScalarLeafs":                  "GraphQL field selection does not match its type",
	"SingleFieldSubscriptions":     "GraphQL subscription must select exactly one root field",
	"UniqueArgumentNames":          "GraphQL argument is provided more than once",
	"UniqueDirectivesPerLocation":  "GraphQL directive is repeated at one location",
	"UniqueFragmentNames":          "GraphQL fragment name is defined more than once",
	"UniqueInputFieldNames":        "GraphQL input object field is provided more than once",
	"UniqueOperationNames":         "GraphQL operation name is defined more than once",
	"UniqueVariableNames":          "GraphQL variable name is defined more than once",
	"ValuesOfCorrectType":          "GraphQL value has the wrong type",
	"VariablesAreInputTypes":       "GraphQL variable must have an input type",
	"VariablesInAllowedPosition":   "GraphQL variable type does not fit its position",
}

func presentValidationErrors(list gqlerror.List) []Error {
	shown := list
	if len(shown) > maxValidationErrors {
		shown = shown[:maxValidationErrors]
	}
	presented := make([]Error, 0, len(shown)+1)
	for _, item := range shown {
		failure := invalidDocumentError()
		if message, known := validationTemplates[item.Rule]; known {
			failure.Message = message
			failure.Extensions["rule"] = item.Rule
		}
		for _, location := range item.Locations {
			if len(failure.Locations) == maxErrorLocations {
				break
			}
			failure.Locations = append(failure.Locations, Location{Line: location.Line, Column: location.Column})
		}
		presented = append(presented, failure)
	}
	if omitted := len(list) - len(shown); omitted != 0 {
		noun := "errors"
		if omitted == 1 {
			noun = "error"
		}
		presented = append(presented, publicError("GRAPHQL_VALIDATION_FAILED", fmt.Sprintf("and %d more validation %s", omitted, noun)))
	}
	return presented
}

func invalidDocumentError() Error {
	return publicError("GRAPHQL_VALIDATION_FAILED", "GraphQL document is invalid")
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
	SubscriptionRecheck  SubscriptionRecheck[P]
}

type SubscriptionRecheck[P any] struct {
	Interval time.Duration
	Check    func(context.Context, P, Operation) error
}

type Operation struct {
	Document   *ast.QueryDocument
	Definition *ast.OperationDefinition
	Variables  map[string]any
}

type Location struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Error struct {
	Message    string         `json:"message"`
	Locations  []Location     `json:"locations,omitempty"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions"`
}
type Response struct {
	Data   any     `json:"data,omitempty"`
	Errors []Error `json:"errors,omitempty"`
}

type Executor[P any] interface {
	Execute(context.Context, P, Operation) Response
	ListBounds() ListBounds
}

type Server[P any] struct {
	schema              *ast.Schema
	sdl                 string
	config              Config[P]
	limits              Limits
	executor            Executor[P]
	listBounds          ListBounds
	concurrency         chan struct{}
	contractFingerprint golem.SchemaDigest
	executable          gqlgengraphql.ExecutableSchema
	eventLimits         events.Limits
	lifecycle           context.Context
	cancel              context.CancelFunc
	connections         sync.WaitGroup
	rechecks            sync.WaitGroup
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
	if err := validateSubscriptionRecheck(config.SubscriptionRecheck); err != nil {
		return nil, err
	}
	schema, err := validator.LoadSchema(validator.Prelude, &ast.Source{Name: "golem.graphql", Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("invalid generated GraphQL schema: %w", err)
	}
	listBounds := executor.ListBounds()
	if err := validateListBounds(schema, listBounds); err != nil {
		return nil, err
	}
	if config.ExecutableSchema != nil && config.ExecutableSchema.Schema() == nil {
		return nil, fmt.Errorf("GraphQL executable schema is incomplete")
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &Server[P]{schema: schema, sdl: sdl, config: config, limits: limits, executor: executor, listBounds: listBounds, concurrency: make(chan struct{}, limits.MaxResolverConcurrency), contractFingerprint: config.ContractFingerprint, executable: config.ExecutableSchema, eventLimits: eventLimits, lifecycle: lifecycle, cancel: cancel}, nil
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
	negotiated := negotiateEventStream(request.Header.Values("Accept"))
	if negotiated == eventStreamPreferred {
		server.serveSSE(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		writeResponse(writer, Response{Errors: []Error{publicError("METHOD_NOT_ALLOWED", "GraphQL requires POST")}})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, int64(server.limits.MaxRequestBytes))
	decoded, failure := decodeHTTPRequest(request.Body, server.limits)
	if failure != nil {
		writeResponse(writer, Response{Errors: []Error{*failure}})
		return
	}
	principal, ok := server.config.PrincipalFromContext(request.Context())
	if !ok {
		writeResponse(writer, Response{Errors: []Error{publicError("UNAUTHENTICATED", "authentication is required")}})
		return
	}
	if negotiated == eventStreamAcceptable {
		prepared, failure := server.prepareRequest(decoded, true)
		if failure == nil && prepared.Operation.Definition.Operation == ast.Subscription {
			server.streamSSE(writer, request, principal, decoded, prepared, refuseJSON)
			return
		}
		if failure == nil {
			writeResponse(writer, server.execute(request.Context(), principal, decoded, true, &prepared))
			return
		}
	}
	writeResponse(writer, server.execute(request.Context(), principal, decoded, true, nil))
}

func decodeHTTPRequest(body io.Reader, limits Limits) (Request, *Error) {
	fail := func(failure Error) (Request, *Error) {
		return Request{}, &failure
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var envelope requestEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fail(publicError("BAD_REQUEST", "invalid GraphQL request"))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fail(publicError("BAD_REQUEST", "invalid GraphQL request"))
	}
	if envelope.Query == "" {
		return fail(publicError("BAD_REQUEST", "GraphQL request requires a query"))
	}
	if exceedsLexicalTokenLimit(envelope.Query, limits.MaxTokens) {
		return fail(limitError(limitTokens, limits.MaxTokens, nil))
	}
	variables := map[string]any{}
	if len(envelope.Variables) != 0 && string(envelope.Variables) != "null" {
		if len(envelope.Variables) > limits.MaxVariableBytes {
			return fail(limitError(limitVariableBytes, limits.MaxVariableBytes, nil))
		}
		variableDecoder := json.NewDecoder(strings.NewReader(string(envelope.Variables)))
		variableDecoder.UseNumber()
		if err := variableDecoder.Decode(&variables); err != nil {
			return fail(publicError("BAD_USER_INPUT", "invalid GraphQL variables"))
		}
	}
	return Request{Query: envelope.Query, OperationName: envelope.OperationName, Variables: variables}, nil
}

// Execute applies the same parse, validation, coercion, limit, isolation, and
// panic boundary used by Handler without re-resolving an already trusted
// principal. It is the direct-execution surface used by tests and embedding.
// Response data holds decoded Go values, so object key order is not retained;
// Handler writes the same data in selection order.
func (server *Server[P]) Execute(ctx context.Context, principal P, request Request) (response Response) {
	return decodedResponse(server.execute(ctx, principal, request, false, nil))
}

// execute receives byteLimitsChecked=true after the raw
// request body and raw variables have already been bounded. Re-encoding those
// values can add canonical JSON envelope/escaping bytes and must not turn an
// accepted HTTP boundary into a second, representation-dependent refusal.
func (server *Server[P]) execute(ctx context.Context, principal P, request Request, byteLimitsChecked bool, admitted *preparedRequest) (response Response) {
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
	var preparedOperation preparedRequest
	if admitted != nil {
		preparedOperation = *admitted
	} else {
		var failure *Response
		preparedOperation, failure = server.prepareRequest(request, byteLimitsChecked)
		if failure != nil {
			return *failure
		}
	}
	if preparedOperation.Operation.Definition.Operation == ast.Subscription {
		return Response{Errors: []Error{publicError("SUBSCRIPTION_TRANSPORT_REQUIRED", "GraphQL subscriptions require graphql-transport-ws")}}
	}
	if server.executable == nil && preparedOperation.rootMeta {
		return Response{Errors: []Error{publicError("GRAPHQL_VALIDATION_FAILED", "__schema, __type and a root __typename need an executable schema, which this GraphQL server does not have")}}
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
		return server.selectionOrdered(prepared, preparedOperation.Operation)
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
		case "QUERY_LIMIT_EXCEEDED", "INPUT_LIMIT_EXCEEDED", string(golem.CodeLimitExceeded):
			return observe.OutcomeRefused, observe.ReasonLimit
		}
	}
	return observe.OutcomeRefused, observe.ReasonInvalidInput
}

type preparedRequest struct {
	Operation Operation
	rootMeta  bool
}

const (
	maxIntrospectionSchemaRoots = 1
	maxIntrospectionTypeRoots   = 8
)

func (server *Server[P]) prepareRequest(request Request, byteLimitsChecked bool) (preparedRequest, *Response) {
	fail := func(failures ...Error) (preparedRequest, *Response) {
		response := Response{Errors: failures}
		return preparedRequest{}, &response
	}
	limits := server.limits
	invalidVariables := publicError("BAD_USER_INPUT", "GraphQL variables are invalid")
	encodedVariables, err := json.Marshal(request.Variables)
	if err != nil {
		return fail(invalidVariables)
	}
	if !byteLimitsChecked && len(encodedVariables) > limits.MaxVariableBytes {
		return fail(limitError(limitVariableBytes, limits.MaxVariableBytes, nil))
	}
	ownedVariables := map[string]any{}
	if string(encodedVariables) != "null" {
		decoder := json.NewDecoder(bytes.NewReader(encodedVariables))
		decoder.UseNumber()
		if err := decoder.Decode(&ownedVariables); err != nil {
			return fail(invalidVariables)
		}
	}
	request.Variables = ownedVariables
	if !byteLimitsChecked {
		encodedEnvelope, err := json.Marshal(requestEnvelope{Query: request.Query, OperationName: request.OperationName, Variables: encodedVariables})
		if err != nil || len(encodedEnvelope) > limits.MaxRequestBytes {
			return fail(limitError(limitRequestBytes, limits.MaxRequestBytes, nil))
		}
	}
	if request.Query == "" {
		return fail(publicError("BAD_REQUEST", "GraphQL request requires a query"))
	}
	if exceedsLexicalTokenLimit(request.Query, limits.MaxTokens) {
		return fail(limitError(limitTokens, limits.MaxTokens, nil))
	}
	document, err := parser.ParseQuery(&ast.Source{Name: "request.graphql", Input: request.Query})
	if err != nil {
		return fail(publicError("GRAPHQL_PARSE_FAILED", "GraphQL parsing failed"))
	}
	if validationErrors := validator.Validate(server.schema, document); len(validationErrors) != 0 {
		return fail(presentValidationErrors(validationErrors)...)
	}
	if len(document.Fragments) > limits.MaxFragments {
		return fail(limitError(limitFragments, limits.MaxFragments, nil))
	}
	if documentASTNodesExceed(document, limits.MaxASTNodes) {
		return fail(limitError(limitASTNodes, limits.MaxASTNodes, nil))
	}
	definition := document.Operations.ForName(request.OperationName)
	if definition == nil {
		return fail(publicError("GRAPHQL_VALIDATION_FAILED", "GraphQL operation was not found"))
	}
	shape := boundedOperationShape(definition.SelectionSet, document.Fragments, limits)
	if shape.malformed {
		return fail(invalidDocumentError())
	}
	if shape.limit != nil {
		return fail(limitError(shape.limit, limits.valueOf(shape.limit), nil))
	}
	if !server.config.Introspection && shape.introspection {
		return fail(publicError("GRAPHQL_VALIDATION_FAILED", "GraphQL introspection is disabled"))
	}
	inputNodes, inputDepth, listItems, inputBytes := inputShape(request.Variables, 1)
	switch {
	case inputNodes > limits.MaxInputNodes:
		return fail(limitError(limitInputNodes, limits.MaxInputNodes, nil))
	case inputDepth > limits.MaxInputDepth:
		return fail(limitError(limitInputDepth, limits.MaxInputDepth, nil))
	case listItems > limits.MaxListItems:
		return fail(limitError(limitListItems, limits.MaxListItems, nil))
	case inputBytes > limits.MaxVariableBytes:
		return fail(limitError(limitVariableBytes, limits.MaxVariableBytes, nil))
	}
	coerced, err := validator.VariableValues(server.schema, definition, request.Variables)
	if err != nil {
		return fail(invalidVariables)
	}
	complexity, err := operationComplexity(definition.SelectionSet, document.Fragments, coerced, server.listBounds, map[string]bool{}, false)
	if err != nil {
		return fail(invalidDocumentError())
	}
	if complexity > limits.MaxComplexity {
		return fail(limitError(limitComplexity, limits.MaxComplexity, &complexity))
	}
	meta := rootMetaSelection{schemas: map[string]bool{}, types: map[string]bool{}}
	if err := meta.collect(definition.SelectionSet, document.Fragments, coerced, map[string]bool{}); err != nil {
		return fail(invalidDocumentError())
	}
	if len(meta.schemas) > maxIntrospectionSchemaRoots {
		return fail(limitError(limitSchemaRoots, maxIntrospectionSchemaRoots, nil))
	}
	if len(meta.types) > maxIntrospectionTypeRoots {
		return fail(limitError(limitTypeRoots, maxIntrospectionTypeRoots, nil))
	}
	return preparedRequest{Operation: Operation{Document: document, Definition: definition, Variables: coerced}, rootMeta: len(meta.schemas) != 0 || len(meta.types) != 0 || meta.typename}, nil
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
		data, err := canonicalExecutableData(result.Data)
		if err != nil {
			return Response{Errors: []Error{PresentError(ctx, fmt.Errorf("decode gqlgen executable response: %w", err), nil, server.config.ReportInternalError)}}
		}
		if string(data) != "null" {
			response.Data = data
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
	if response, rooted := compileerr.RootOf(err); rooted && path == nil {
		path = []any{response}
	}
	var compileFailure *compileerr.Error
	if errors.As(err, &compileFailure) && !compileFailure.Internal() {
		return presentCompileError(compileFailure, path)
	}
	var failure *golem.Error
	if errors.As(err, &failure) {
		if report != nil && failure.Code == golem.CodeLimitExceeded && failure.Limit == golem.LimitListItems {
			report(ctx, err)
		}
		return Error{Message: failure.Message, Path: append([]any(nil), path...), Extensions: publicErrorExtensions(failure)}
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
func presentCompileError(failure *compileerr.Error, path []any) Error {
	extensions := map[string]any{"code": "BAD_USER_INPUT"}
	if reason := failure.Reason(); reason != "" {
		extensions["reason"] = string(reason)
	}
	if argument := failure.Argument(); argument != "" {
		extensions["argument"] = argument
	}
	if limit := failure.Limit(); limit != "" {
		extensions["limit"] = limit
	}
	if maximum, ok := failure.Max(); ok {
		extensions["max"] = maximum
	}
	if minimum, ok := failure.Min(); ok {
		extensions["min"] = minimum
	}
	return Error{Message: failure.Message(), Path: append([]any(nil), path...), Extensions: extensions}
}

func publicErrorExtensions(failure *golem.Error) map[string]any {
	extensions := map[string]any{"code": string(failure.Code)}
	if failure.Reason != "" {
		extensions["reason"] = string(failure.Reason)
	}
	if failure.Limit != "" {
		extensions["limit"] = string(failure.Limit)
		extensions["max"] = failure.LimitMax
	}
	if failure.Code == golem.CodeCommittedResultUnavailable {
		extensions["committed"] = true
	}
	if failure.Unreadable != 0 {
		extensions["unreadable"] = failure.Unreadable
	}
	return extensions
}

func writeResponse(writer io.Writer, response Response) {
	buffer := responseBuffers.Get().(*bytes.Buffer)
	defer func() {
		if buffer.Cap() <= maxPooledResponseBuffer {
			buffer.Reset()
			responseBuffers.Put(buffer)
		}
	}()
	if err := encodeResponseInto(buffer, response); err != nil {
		return
	}
	buffer.WriteByte('\n')
	_, _ = writer.Write(buffer.Bytes())
}
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

func operationComplexity(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, bounds ListBounds, stack map[string]bool, introspection bool) (int, error) {
	total := 0
	for _, selection := range set {
		switch value := selection.(type) {
		case *ast.Field:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			nested := introspection || introspectionRoot(value)
			child, err := operationComplexity(value.SelectionSet, fragments, variables, bounds, stack, nested)
			if err != nil {
				return 0, err
			}
			multiplier := 1
			if !nested && value.Definition != nil && value.Definition.Type != nil && value.Definition.Type.Elem != nil {
				if take, ok := exactComplexityTake(value.ArgumentMap(variables)["take"]); ok {
					multiplier = take
				} else if bound, declared := bounds.of(value.ObjectDefinition.Name, value.Name); declared {
					multiplier = bound
				}
			}
			cost := 1
			if child != 0 {
				cost = saturatingAdd(cost, saturatingMultiply(child, multiplier))
			}
			total = saturatingAdd(total, cost)
		case *ast.InlineFragment:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			cost, err := operationComplexity(value.SelectionSet, fragments, variables, bounds, stack, introspection)
			if err != nil {
				return 0, err
			}
			total = saturatingAdd(total, cost)
		case *ast.FragmentSpread:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			fragment := fragments.ForName(value.Name)
			if fragment == nil || stack[value.Name] {
				return 0, fmt.Errorf("GraphQL fragment expansion is invalid")
			}
			stack[value.Name] = true
			cost, err := operationComplexity(fragment.SelectionSet, fragments, variables, bounds, stack, introspection)
			delete(stack, value.Name)
			if err != nil {
				return 0, err
			}
			total = saturatingAdd(total, cost)
		default:
			return 0, fmt.Errorf("GraphQL selection kind is invalid")
		}
	}
	return total, nil
}

func saturatingAdd(left, right int) int {
	if left > math.MaxInt-right {
		return math.MaxInt
	}
	return left + right
}

func saturatingMultiply(left, right int) int {
	if left == 0 || right == 0 {
		return 0
	}
	if left > math.MaxInt/right {
		return math.MaxInt
	}
	return left * right
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
	introspection, malformed      bool
	limit                         *limitBound
}

func boundedOperationShape(set ast.SelectionSet, fragments ast.FragmentDefinitionList, limits Limits) operationShapeStats {
	stats := operationShapeStats{}
	exceed := func(bound *limitBound) { stats.limit = bound }
	var walk func(ast.SelectionSet, int, map[string]bool, bool)
	walk = func(current ast.SelectionSet, depth int, stack map[string]bool, introspection bool) {
		if stats.limit != nil || stats.malformed {
			return
		}
		if !introspection && depth > stats.depth {
			stats.depth = depth
		}
		if stats.depth > limits.MaxDepth {
			exceed(limitDepth)
			return
		}
		for _, selection := range current {
			stats.nodes++
			if stats.nodes > limits.MaxASTNodes {
				exceed(limitASTNodes)
				return
			}
			switch value := selection.(type) {
			case *ast.Field:
				stats.fields++
				if value.Alias != "" && value.Alias != value.Name {
					stats.aliases++
				}
				if introspectionRoot(value) {
					stats.introspection = true
				}
				if stats.fields > limits.MaxSelectedFields {
					exceed(limitSelectedFields)
					return
				}
				if stats.aliases > limits.MaxAliases {
					exceed(limitAliases)
					return
				}
				walk(value.SelectionSet, depth+1, stack, introspection || introspectionRoot(value))
			case *ast.InlineFragment:
				walk(value.SelectionSet, depth, stack, introspection)
			case *ast.FragmentSpread:
				fragment := fragments.ForName(value.Name)
				if stack[value.Name] || fragment == nil {
					stats.malformed = true
					return
				}
				stack[value.Name] = true
				walk(fragment.SelectionSet, depth, stack, introspection)
				delete(stack, value.Name)
			default:
				stats.malformed = true
			}
			if stats.limit != nil || stats.malformed {
				return
			}
		}
	}
	walk(set, 1, map[string]bool{}, false)
	return stats
}

type rootMetaSelection struct {
	schemas, types map[string]bool
	typename       bool
}

func (meta *rootMetaSelection) collect(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, stack map[string]bool) error {
	for _, selection := range set {
		switch value := selection.(type) {
		case *ast.Field:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			responseName := value.Alias
			if responseName == "" {
				responseName = value.Name
			}
			switch value.Name {
			case "__schema":
				meta.schemas[responseName] = true
			case "__type":
				meta.types[responseName] = true
			case "__typename":
				meta.typename = true
			}
		case *ast.InlineFragment:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			if err := meta.collect(value.SelectionSet, fragments, variables, stack); err != nil {
				return err
			}
		case *ast.FragmentSpread:
			if !graphqlIncluded(value.Directives, variables) {
				continue
			}
			fragment := fragments.ForName(value.Name)
			if fragment == nil || stack[value.Name] {
				return fmt.Errorf("GraphQL fragment expansion is invalid")
			}
			stack[value.Name] = true
			err := meta.collect(fragment.SelectionSet, fragments, variables, stack)
			delete(stack, value.Name)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("GraphQL selection kind is invalid")
		}
	}
	return nil
}

func introspectionRoot(field *ast.Field) bool {
	return field.Name == "__schema" || field.Name == "__type"
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
