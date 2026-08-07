package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	typedvalue "github.com/eleven-am/golem/go/internal/event/typedvalue"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policynormalize "github.com/eleven-am/golem/go/internal/policy/normalize"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type ValidatedEvent = typedvalue.ValidatedEvent

type EventFactory interface {
	ModelID() golem.ModelID
	EventSchemaDigest() golem.EventSchemaDigest
	Build(ValidatedEvent) (any, error)
}

type PackageEventFactories struct {
	generation golem.SchemaDigest
	factories  []EventFactory
}

type EventFactoryRegistry struct {
	generation golem.SchemaDigest
	factories  map[golem.ModelID]EventFactory
}

func GeneratedPackageEventFactories(generation golem.SchemaDigest, factories ...EventFactory) PackageEventFactories {
	return PackageEventFactories{generation: generation, factories: append([]EventFactory(nil), factories...)}
}

func GeneratedEventFactoryRegistry(expected golem.SchemaDigest, packages ...PackageEventFactories) (EventFactoryRegistry, error) {
	if expected == (golem.SchemaDigest{}) {
		return EventFactoryRegistry{}, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event factory generation is absent")
	}
	result := EventFactoryRegistry{generation: expected, factories: make(map[golem.ModelID]EventFactory)}
	for packageIndex, pkg := range packages {
		if pkg.generation != expected {
			return EventFactoryRegistry{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event factory package %d has a different generation", packageIndex)
		}
		for _, factory := range pkg.factories {
			if factory == nil || factory.ModelID() == (golem.ModelID{}) || factory.EventSchemaDigest() == (golem.EventSchemaDigest{}) {
				return EventFactoryRegistry{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event factory package %d is incomplete", packageIndex)
			}
			if _, duplicate := result.factories[factory.ModelID()]; duplicate {
				return EventFactoryRegistry{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event factory model is duplicated")
			}
			result.factories[factory.ModelID()] = factory
		}
	}
	return result, nil
}

func (registry EventFactoryRegistry) GenerationDigest() golem.SchemaDigest {
	return registry.generation
}
func (registry EventFactoryRegistry) modelIDs() []golem.ModelID {
	result := make([]golem.ModelID, 0, len(registry.factories))
	for model := range registry.factories {
		result = append(result, model)
	}
	return result
}
func (registry EventFactoryRegistry) build(model golem.ModelID, input ValidatedEvent) (any, error) {
	factory := registry.factories[model]
	if factory == nil {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: generated event factory is unavailable")
	}
	return factory.Build(input)
}

// RuntimeBuildValidatedEvent is the model-erased handoff used by P7's
// evaluator and generated adapters. The input is sealed by an internal
// constructor; callers cannot supply raw model IDs, identity components, rows,
// or bytes through this function.
func RuntimeBuildValidatedEvent(registry EventFactoryRegistry, input ValidatedEvent) (any, error) {
	metadata := input.Metadata()
	if metadata.EventID() == (golem.EventID{}) || metadata.ModelID() == (golem.ModelID{}) {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: validated event value is absent")
	}
	return registry.build(metadata.ModelID(), input)
}

type callerEventSubscription[P any] struct {
	context        context.Context
	principal      P
	model          golem.ModelID
	readRequest    golem.FrozenReadRequest
	originalWhere  *policyir.Condition
	metadata       golem.EventModelMetadata
	entitySelected bool
}

type callerEventStream[E any] struct{ stream *subscription.Stream[any] }

func (stream *callerEventStream[E]) Recv(ctx context.Context) (E, error) {
	var zero E
	if stream == nil || stream.stream == nil {
		return zero, events.Failure(events.CodeSubscriptionCancelled)
	}
	value, err := stream.stream.Recv(ctx)
	if err != nil {
		return zero, err
	}
	typed, ok := value.(E)
	if !ok {
		_ = stream.stream.Close()
		return zero, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	return typed, nil
}

func (stream *callerEventStream[E]) Close() error {
	if stream == nil || stream.stream == nil {
		return nil
	}
	return stream.stream.Close()
}

// CallerEvents is the generated caller-only typed stream entry point. It
// performs all fallible request binding and the model-read gate before hub
// registration. The retained state contains only an owned principal snapshot
// and immutable, schema-bound request data; no caller execution survives.
func CallerEvents[P, A, M, E any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.EventOption[M]) (golem.EventStream[E], error) {
	if ctx == nil || caller == nil || caller.app == nil || caller.execution == 0 {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: caller execution is unavailable")
	}
	request, err := golem.RuntimeFreezeEventOptions(descriptor, options...)
	if err != nil {
		return nil, err
	}
	return CallerFrozenEvents[P, A, E](ctx, caller, request)
}

// CallerFrozenEvents is the model-erased generated GraphQL handoff. The
// request must already have crossed Golem's sealed event-option/operation
// binder; this function shares every admission, authorization, hub, and typed
// factory step with the ordinary generated Go client entry point.
func CallerFrozenEvents[P, A, E any](ctx context.Context, caller *Caller[P, A], request golem.FrozenEventRequest) (golem.EventStream[E], error) {
	if ctx == nil || caller == nil || caller.app == nil || caller.execution == 0 || request.ModelID() == (golem.ModelID{}) {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: caller execution or frozen request is unavailable")
	}
	metadata, ok := caller.app.eventRegistry.Lookup(request.ModelID())
	if !ok || metadata.ModelID() != request.ModelID() || caller.app.eventTransport == nil {
		return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event stream is unavailable")
	}
	readRequest, entitySelected, err := eventReadRequest(caller.app.registry, request, metadata)
	if err != nil {
		return nil, err
	}
	return CallerFrozenReadEvents[P, A, E](ctx, caller, readRequest, entitySelected)
}

// CallerFrozenReadEvents is the full-selection P5/P7 handoff used by the
// generated GraphQL subscription adapter. The request is the ordinary sealed,
// compiler-produced P3 read request and may contain relations and counts. Its
// bound where predicate is retained separately and re-conjoined after every
// read hook, so hooks cannot remove the subscription filter. This seam
// fresh-reads the complete P3 dependency/hydration row. For each received
// event, P7-F creates one fresh P5 computedExecution, encodes through the
// existing operation encoder, and closes it before the next frame; computed
// loaders never live in this hub or across events.
func CallerFrozenReadEvents[P, A, E any](ctx context.Context, caller *Caller[P, A], readRequest golem.FrozenReadRequest, entitySelected bool) (golem.EventStream[E], error) {
	if ctx == nil || caller == nil || caller.app == nil || caller.execution == 0 || readRequest.ModelID() == (golem.ModelID{}) || readRequest.Operation() != golem.ReadFindMany {
		return nil, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: caller execution or frozen event read is unavailable")
	}
	metadata, ok := caller.app.eventRegistry.Lookup(readRequest.ModelID())
	if !ok || metadata.ModelID() != readRequest.ModelID() || caller.app.eventTransport == nil {
		return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event stream is unavailable")
	}
	if err := validateEventMetadata(caller.app.registry, metadata); err != nil {
		return nil, err
	}
	principal, err := snapshotEventPrincipal(caller.principal, caller.app.snapshotPrincipal)
	if err != nil {
		return nil, err
	}
	initial, err := caller.app.ForPrincipal(ctx, principal)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	prepared, err := initial.Prepare(readRequest)
	if err != nil {
		return nil, err
	}
	model := readRequest.ModelID()
	policy, present := initial.policies.Policy(policyir.ModelID(model))
	if !present {
		return nil, golem.RuntimeReadError(golem.CodeForbidden, "events", model, golem.FieldID{}, "read is not permitted", nil)
	}
	rowConstraint, err := policyresolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(model))
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeForbidden, "events", model, golem.FieldID{}, "read is not permitted", err)
	}
	if truth, constant := rowConstraint.Constant(); constant && !truth {
		return nil, golem.RuntimeReadError(golem.CodeForbidden, "events", model, golem.FieldID{}, "read is not permitted", nil)
	}
	planned, err := preparePlan(prepared, caller.app.registry, caller.app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	var originalWhere *policyir.Condition
	if bound, exists := prepared.request.Where(); exists {
		originalWhere = &bound
	}
	key, err := caller.app.eventSubscriberKey(model, metadata, originalWhere, planned)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	state := &callerEventSubscription[P]{
		context: ctx, principal: principal, model: model, readRequest: readRequest,
		originalWhere: originalWhere, metadata: metadata, entitySelected: entitySelected,
	}
	hub, err := caller.app.eventHub(model)
	if err != nil {
		return nil, err
	}
	stream, err := hub.SubscribeWithState(ctx, key, state, nil)
	if err != nil {
		return nil, err
	}
	return &callerEventStream[E]{stream: stream}, nil
}

func eventReadRequest(registry *schema.Registry, request golem.FrozenEventRequest, metadata golem.EventModelMetadata) (golem.FrozenReadRequest, bool, error) {
	if err := validateEventMetadata(registry, metadata); err != nil || metadata.ModelID() != request.ModelID() {
		return golem.FrozenReadRequest{}, false, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event identity and active schema differ")
	}
	selected := request.Selection()
	entitySelected := len(selected) != 0
	if len(selected) == 0 {
		selected = metadata.IdentityFields()
	}
	selection := make([]golem.RuntimeReadSelectionInput, len(selected))
	for index, fieldID := range selected {
		field, present := registry.Field(request.ModelID(), fieldID)
		if !present || field.Kind() == compilerir.FieldRelation {
			return golem.FrozenReadRequest{}, false, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: event selection field %d is not a stored scalar", index)
		}
		selection[index] = golem.RuntimeReadSelectionInput{Kind: golem.RuntimeReadScalar, Field: fieldID}
	}
	input := golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: request.ModelID(), Selection: selection,
		Projection: golem.ProjectionSelect,
	}
	if where, present := request.Where(); present {
		input.Where = &where
	}
	readRequest, err := golem.RuntimeFreezeReadRequest(input)
	if err != nil {
		return golem.FrozenReadRequest{}, false, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: %w", err)
	}
	return readRequest, entitySelected, nil
}

func validateEventMetadata(registry *schema.Registry, metadata golem.EventModelMetadata) error {
	if registry == nil {
		return fmt.Errorf("GOLEM_EVENT_CONFIG: active schema is unavailable")
	}
	model, ok := registry.Model(metadata.ModelID())
	if !ok || !model.SubscriptionsEnabled() || !equalPublicFields(model.PrimaryKey(), metadata.IdentityFields()) {
		return fmt.Errorf("GOLEM_EVENT_CONFIG: generated event identity and active schema differ")
	}
	return nil
}

func equalPublicFields(left, right []golem.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (app *App[P, A]) eventSubscriberKey(model golem.ModelID, metadata golem.EventModelMetadata, where *policyir.Condition, planned readplan.Plan) (subscription.SubscriberKey, error) {
	sequence := app.nextSubscription.Add(1)
	var unique [8]byte
	binary.BigEndian.PutUint64(unique[:], sequence)
	principal, err := subscription.NewCanonicalIdentity("golem.subscription.principal.v1", unique[:])
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	membership, err := subscription.NewCanonicalIdentity("golem.subscription.membership.v1", unique[:])
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	generation := app.registry.GenerationDigest()
	policy, err := subscription.NewCanonicalIdentity("golem.subscription.policy.v1", generation[:])
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	filterBytes, err := eventFilterCanonical(model, where)
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	filter, err := subscription.NewCanonicalIdentity("golem.subscription.filter.v1", filterBytes)
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	selection, err := subscription.NewCanonicalIdentity("golem.subscription.selection.v1", joinFieldIdentities(metadata.IdentityFields(), plannedPublicFields(planned)))
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	dependencies, err := subscription.NewCanonicalIdentity("golem.subscription.dependencies.v1", planDependencyIdentity(planned))
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	eventSchema := metadata.EventSchemaDigest()
	encoder, err := subscription.NewCanonicalIdentity("golem.subscription.encoder.v1", eventSchema[:])
	if err != nil {
		return subscription.SubscriberKey{}, err
	}
	return subscription.NewSubscriberKey(subscription.SubscriberKeyInput{
		Generation: app.registry.GenerationDigest(), Model: model, Principal: principal,
		PolicyGeneration: policy, Filter: filter, Selection: selection,
		Dependencies: dependencies, EncoderShape: encoder, Membership: membership,
		Shareable: false,
	})
}

func eventFilterCanonical(model golem.ModelID, where *policyir.Condition) ([]byte, error) {
	if where != nil {
		return policyir.CanonicalCondition(*where)
	}
	truth, err := policyir.NewConstant(policyir.ModelID(model), true)
	if err != nil {
		return nil, err
	}
	return policyir.CanonicalCondition(truth)
}

func plannedPublicFields(plan readplan.Plan) []golem.FieldID {
	result := make([]golem.FieldID, 0)
	for _, field := range plan.Fields() {
		if field.Public() {
			result = append(result, golem.FieldID(field.FieldID()))
		}
	}
	return result
}

func joinFieldIdentities(groups ...[]golem.FieldID) []byte {
	var result []byte
	for _, fields := range groups {
		for _, field := range fields {
			result = append(result, field[:]...)
		}
		result = append(result, 0xff)
	}
	return result
}

func planDependencyIdentity(plan readplan.Plan) []byte {
	var result []byte
	var appendPlan func(readplan.Plan)
	appendPlan = func(item readplan.Plan) {
		model := golem.ModelID(item.ModelID())
		result = append(result, model[:]...)
		for _, field := range item.Fields() {
			fieldID := golem.FieldID(field.FieldID())
			result = append(result, fieldID[:]...)
			if field.Public() {
				result = append(result, 1)
			} else {
				result = append(result, 0)
			}
		}
		for _, relation := range item.Hydrations() {
			fieldID := golem.FieldID(relation.FieldID())
			result = append(result, fieldID[:]...)
			appendPlan(relation.Child())
		}
	}
	appendPlan(plan)
	if len(result) == 0 {
		return []byte{0}
	}
	return result
}

func (app *App[P, A]) eventHub(model golem.ModelID) (*subscription.ModelHub[any], error) {
	app.eventMu.Lock()
	defer app.eventMu.Unlock()
	if hub := app.eventHubs[model]; hub != nil {
		return hub, nil
	}
	metadata, ok := app.eventRegistry.Lookup(model)
	if !ok || metadata.EventSchemaDigest() == (golem.EventSchemaDigest{}) {
		return nil, events.Failure(events.CodeEventConfig)
	}
	hub, err := subscription.NewModelHub(subscription.Config[any]{
		Generation: app.registry.GenerationDigest(), EventSchema: metadata.EventSchemaDigest(), Model: model, Limits: app.eventLimits,
		Source: func(ctx context.Context, request events.Subscription) (events.Stream, error) {
			return app.eventTransport.Subscribe(ctx, request)
		},
		EvaluateState: func(ctx context.Context, notice events.Notice, _ subscription.SubscriberKey, retained any) (subscription.Evaluation[any], error) {
			state, ok := retained.(*callerEventSubscription[P])
			if !ok || state == nil {
				return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
			}
			return app.evaluateEvent(ctx, notice, state)
		},
		Clone:    func(value any) (any, error) { return value, nil },
		Observer: app.eventObserver,
	})
	if err != nil {
		return nil, err
	}
	app.eventHubs[model] = hub
	return hub, nil
}

func (app *App[P, A]) evaluateEvent(ctx context.Context, notice events.Notice, state *callerEventSubscription[P]) (subscription.Evaluation[any], error) {
	if ctx == nil || state == nil || state.context == nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	eventContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.context, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if state.context.Err() != nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionCancelled)
	}
	envelope, err := eventcodec.Decode(notice.Encoded(), app.eventSchemas, eventcodec.Limits{MaxEncodedBytes: app.eventLimits.MaxEncodedEventBytes})
	if err != nil || !eventNoticeMatchesEnvelope(notice, envelope) {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	if envelope.ResolvedEventSchemaDigest() != state.metadata.EventSchemaDigest() || envelope.ModelID() != state.metadata.ModelID() {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	fresh, err := app.ForPrincipal(eventContext, state.principal)
	if err != nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionRevalidation)
	}
	fact := envelope.Fact()
	identity, ok := fact.AfterIdentity()
	if envelope.Action() == golem.EventDeleted {
		identity, ok = fact.BeforeIdentity()
	}
	if !ok || !eventIdentityMatches(identity, state.metadata.IdentityFields()) {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	identityValues, err := eventIdentityValues(app, identity)
	if err != nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	var entity *golem.RuntimeModelRow
	switch envelope.Action() {
	case golem.EventCreated, golem.EventUpdated:
		row, delivered, readErr := app.readEventEntity(eventContext, fresh, state, identity)
		if readErr != nil {
			return subscription.Evaluation[any]{}, readErr
		}
		if !delivered {
			return subscription.Suppress[any](events.SuppressionFiltered), nil
		}
		if state.entitySelected {
			entity = &row
		}
	case golem.EventDeleted:
		if state.originalWhere != nil {
			return subscription.Suppress[any](events.SuppressionDeleteFiltered), nil
		}
		switch app.authorizeDeletedEvent(eventContext, fresh, fact) {
		case deleteAuthorizationAllowed:
		case deleteAuthorizationDenied:
			return subscription.Suppress[any](events.SuppressionUnauthorized), nil
		case deleteAuthorizationUnverifiable:
			return subscription.Suppress[any](events.SuppressionDeletionUnverifiable), nil
		default:
			return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
		}
	default:
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	wireSchema, hasWireSchema := envelope.EventSchemaDigest()
	validated, err := typedvalue.New(typedvalue.Metadata{
		EventID: envelope.EventID(), Action: envelope.Action(), CausationID: envelope.CausationID(),
		Ordinal: envelope.TransactionOrdinal(), RecordedAt: envelope.RecordedAt(),
		Generation: envelope.GenerationDigest(), EventSchema: wireSchema, HasEventSchema: hasWireSchema,
		ResolvedEventSchema: envelope.ResolvedEventSchemaDigest(), ModelID: envelope.ModelID(),
	}, identityValues, entity)
	if err != nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	value, err := RuntimeBuildValidatedEvent(app.eventFactories, validated)
	if err != nil {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	return subscription.Deliver(value), nil
}

func eventNoticeMatchesEnvelope(notice events.Notice, envelope eventcodec.Envelope) bool {
	return notice.Valid() && notice.EventID() == envelope.EventID() &&
		notice.GenerationDigest() == envelope.GenerationDigest() && notice.ModelID() == envelope.ModelID() &&
		notice.EventSchemaDigest() == envelope.ResolvedEventSchemaDigest() &&
		notice.Action() == envelope.Action() && notice.CausationID() == envelope.CausationID() &&
		notice.TransactionOrdinal() == envelope.TransactionOrdinal()
}

func eventIdentityMatches(identity mutationdecode.Identity, expected []golem.FieldID) bool {
	components := identity.Components()
	if len(components) != len(expected) {
		return false
	}
	for index, component := range components {
		if component.IsNull() || golem.FieldID(component.FieldID()) != expected[index] {
			return false
		}
	}
	return true
}

func eventIdentityValues[P, A any](app *App[P, A], identity mutationdecode.Identity) ([]any, error) {
	components := identity.Components()
	result := make([]any, len(components))
	for index, component := range components {
		value, present := component.PolicyValue()
		if !present {
			return nil, fmt.Errorf("event identity is NULL")
		}
		public, err := runtimePublicPolicyValue(app, value)
		if err != nil {
			return nil, err
		}
		result[index] = public
	}
	return result, nil
}

func (app *App[P, A]) readEventEntity(ctx context.Context, caller *Caller[P, A], state *callerEventSubscription[P], identity mutationdecode.Identity) (golem.RuntimeModelRow, bool, error) {
	policy, present := caller.policies.Policy(policyir.ModelID(state.model))
	if !present {
		return golem.RuntimeModelRow{}, false, nil
	}
	constraint, err := policyresolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(state.model))
	if err != nil {
		return golem.RuntimeModelRow{}, false, nil
	}
	if truth, constant := constraint.Constant(); constant && !truth {
		return golem.RuntimeModelRow{}, false, nil
	}
	envelope, err := golem.RuntimeReadHookRequestFromFrozen(state.readRequest)
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	transformed, err := golem.RuntimeInvokeReadBeforeHooks(hookContext, app.bindings, envelope, func(value golem.RuntimeReadHookRequest) error {
		if value.Request().ModelID() != state.model || value.Request().Operation() != golem.ReadFindMany {
			return fmt.Errorf("event read hook changed operation identity")
		}
		_, prepareErr := caller.Prepare(value.Request())
		return prepareErr
	})
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	prepared, err := caller.Prepare(transformed.Request())
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	request, err := eventRequestWithOriginalWhere(prepared.request, state.originalWhere)
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	prepared.request = request
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return golem.RuntimeModelRow{}, false, nil
	}
	identityCondition, err := eventIdentityCondition(app, policyir.ModelID(state.model), identity)
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	planned, err = readplan.WithAdditionalWhere(planned, identityCondition)
	if err == nil {
		planned, err = readplan.WithMaximumTake(planned, 1)
	}
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	rows, err := executePlan(ctx, app, caller.executor, golem.ReadFindMany, planned)
	if err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if len(rows) > 1 {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	publicRows := make([]golem.RuntimeModelRow, len(rows))
	for index := range rows {
		publicRows[index] = rows[index].row
	}
	if err := golem.RuntimeInvokeReadResultHooks(hookContext, app.bindings, golem.RuntimeReadHookRows(transformed, publicRows, len(rows) != 0)); err != nil {
		return golem.RuntimeModelRow{}, false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if len(rows) == 0 {
		return golem.RuntimeModelRow{}, false, nil
	}
	return rows[0].row, true, nil
}

func eventRequestWithOriginalWhere(request readir.Request, original *policyir.Condition) (readir.Request, error) {
	where, hasWhere := request.Where()
	if original != nil {
		if hasWhere {
			merged, err := policyir.NewLogical(request.ModelID(), policyir.LogicalAnd, []policyir.Condition{where, *original})
			if err != nil {
				return readir.Request{}, err
			}
			where, err = policynormalize.Condition(merged)
			if err != nil {
				return readir.Request{}, err
			}
		} else {
			where, hasWhere = *original, true
		}
	}
	input := readir.RequestInput{
		Operation: request.Operation(), Model: request.ModelID(), OrderBy: request.OrderBy(),
		Distinct: request.Distinct(), Selection: request.Selection(), Projection: request.ProjectionMode(),
		Omit: request.Omitted(),
	}
	if hasWhere {
		input.Where = &where
	}
	if take, present := request.Take(); present {
		input.Take = &take
	}
	if skip, present := request.Skip(); present {
		input.Skip = &skip
	}
	if selector, present := request.Selector(); present {
		input.Selector = &selector
	}
	if cursor, present := request.Cursor(); present {
		input.Cursor = &cursor
	}
	return readir.NewRequest(input)
}

func eventIdentityCondition[P, A any](app *App[P, A], modelID policyir.ModelID, identity mutationdecode.Identity) (policyir.Condition, error) {
	modelFields := identity.Components()
	if len(modelFields) == 0 {
		return policyir.Condition{}, fmt.Errorf("event identity is empty")
	}
	return equalityCondition(app.registry, app.provider, modelID, modelFields)
}

func equalityCondition(registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, components []mutationdecode.IdentityComponent) (policyir.Condition, error) {
	resolver := policysql.SchemaResolver(registry)
	conditions := make([]policyir.Condition, 0, len(components))
	for _, component := range components {
		value, present := component.PolicyValue()
		if !present {
			return policyir.Condition{}, fmt.Errorf("event identity component is NULL")
		}
		field, present := resolver.Field(provider, model, component.FieldID())
		if !present {
			return policyir.Condition{}, fmt.Errorf("event identity field is absent")
		}
		operand, err := policyir.OneOperand(value)
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{
			Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand,
			Mode: policyir.ComparisonSensitive, Providers: resolver.Providers(),
		})
		if err != nil {
			return policyir.Condition{}, err
		}
		condition, err := policyir.NewScalar(model, component.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return policyir.Condition{}, err
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(model, policyir.LogicalAnd, conditions)
}

type deleteAuthorizationDisposition uint8

const (
	deleteAuthorizationAllowed deleteAuthorizationDisposition = iota + 1
	deleteAuthorizationDenied
	deleteAuthorizationUnverifiable
)

func (app *App[P, A]) authorizeDeletedEvent(ctx context.Context, caller *Caller[P, A], factEnvelope interface {
	PrivateDeleteSnapshot() (mutationdecode.Row, bool)
	DeleteSnapshotState() mutationir.DeleteSnapshotState
}) deleteAuthorizationDisposition {
	if factEnvelope.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
		return deleteAuthorizationUnverifiable
	}
	snapshot, present := factEnvelope.PrivateDeleteSnapshot()
	if !present {
		return deleteAuthorizationUnverifiable
	}
	complete, err := snapshot.IsComplete(app.registry)
	if err != nil || !complete {
		return deleteAuthorizationUnverifiable
	}
	policy, ok := caller.policies.Policy(snapshot.ModelID())
	if !ok {
		return deleteAuthorizationDenied
	}
	constraint, err := policyresolve.RowConstraint(policy, policyir.ActionRead, snapshot.ModelID())
	if err != nil {
		return deleteAuthorizationUnverifiable
	}
	record, verifiable, err := app.deleteSnapshotRecord(ctx, caller, snapshot, constraint)
	if err != nil || !verifiable {
		return deleteAuthorizationUnverifiable
	}
	allowed, err := evaluate.Condition(constraint, record, app.providers)
	if err != nil {
		return deleteAuthorizationUnverifiable
	}
	if !allowed {
		return deleteAuthorizationDenied
	}
	return deleteAuthorizationAllowed
}

func (app *App[P, A]) deleteSnapshotRecord(ctx context.Context, caller *Caller[P, A], snapshot mutationdecode.Row, constraint policyir.Condition) (evaluate.Record, bool, error) {
	fields := make([]evaluate.Field, 0, len(snapshot.Cells()))
	for _, cell := range snapshot.Cells() {
		field, err := eventEvaluateField(cell)
		if err != nil {
			return evaluate.Record{}, false, err
		}
		fields = append(fields, field)
	}
	dependencies, err := dependency.Collect(constraint)
	if err != nil {
		return evaluate.Record{}, false, err
	}
	for _, entry := range dependencies.Dependencies().Entries() {
		if entry.Kind() != dependency.Relation {
			continue
		}
		relationField, ok := app.registry.Field(golem.ModelID(snapshot.ModelID()), golem.FieldID(entry.FieldID()))
		relationID, relationOK := relationField.RelationID()
		endpoint, forward := app.registry.ForwardToOneRelation(golem.ModelID(snapshot.ModelID()), relationID)
		if !ok || !relationOK || !forward || endpoint.FieldID() != golem.FieldID(entry.FieldID()) {
			return evaluate.Record{}, false, nil
		}
		children := entry.Children()
		for _, child := range children.Entries() {
			if child.Kind() != dependency.Scalar {
				return evaluate.Record{}, false, nil
			}
		}
		related, verifiable, err := app.hydrateDeletedForwardRelation(ctx, caller, snapshot, endpoint, children)
		if err != nil || !verifiable {
			return evaluate.Record{}, false, err
		}
		fields = append(fields, related)
	}
	record, err := evaluate.NewRecord(snapshot.ModelID(), fields...)
	return record, err == nil, err
}

func eventEvaluateField(cell mutationdecode.Cell) (evaluate.Field, error) {
	if cell.IsNull() {
		return evaluate.NullField(cell.FieldID()), nil
	}
	value, present := cell.PolicyValue()
	if !present {
		return evaluate.Field{}, fmt.Errorf("delete snapshot value is absent")
	}
	if values, list := value.List(); list {
		elements := make([]evaluate.ListElement, len(values))
		for index, item := range values {
			var err error
			elements[index], err = evaluate.ValidListElement(item)
			if err != nil {
				return evaluate.Field{}, err
			}
		}
		return evaluate.ListField(cell.FieldID(), elements...)
	}
	return evaluate.ValueField(cell.FieldID(), value)
}

func (app *App[P, A]) hydrateDeletedForwardRelation(ctx context.Context, caller *Caller[P, A], snapshot mutationdecode.Row, endpoint schema.RelationEndpoint, children dependency.Tree) (evaluate.Field, bool, error) {
	if endpoint.Cardinality() != compilerir.RelationOne || policyir.ModelID(endpoint.TargetModelID()) != children.ModelID() {
		return evaluate.Field{}, false, nil
	}
	components := make([]mutationdecode.IdentityComponent, 0, len(endpoint.Correlation()))
	nullCorrelation := false
	for _, pair := range endpoint.Correlation() {
		cell, present := snapshot.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !present {
			return evaluate.Field{}, false, nil
		}
		if cell.IsNull() {
			nullCorrelation = true
			continue
		}
		if nullCorrelation {
			return evaluate.Field{}, false, nil
		}
		value, present := cell.PolicyValue()
		if !present {
			return evaluate.Field{}, false, nil
		}
		component, err := mutationdecode.IdentityValue(policyir.FieldID(pair.ChildFieldID()), value)
		if err != nil {
			return evaluate.Field{}, false, err
		}
		components = append(components, component)
	}
	if nullCorrelation {
		if len(components) != 0 {
			return evaluate.Field{}, false, nil
		}
		field, err := evaluate.ToOneField(policyir.FieldID(endpoint.FieldID()), policyir.ModelID(endpoint.TargetModelID()))
		return field, err == nil, err
	}
	if len(components) == 0 {
		return evaluate.Field{}, false, nil
	}
	selected := make([]golem.FieldID, 0, len(children.Entries()))
	for _, child := range children.Entries() {
		selected = append(selected, golem.FieldID(child.FieldID()))
	}
	if len(selected) == 0 {
		target, ok := app.registry.Model(endpoint.TargetModelID())
		if !ok || len(target.PrimaryKey()) == 0 {
			return evaluate.Field{}, false, nil
		}
		selected = target.PrimaryKey()
	}
	selection := make([]golem.RuntimeReadSelectionInput, len(selected))
	for index, fieldID := range selected {
		field, ok := app.registry.Field(endpoint.TargetModelID(), fieldID)
		if !ok || field.Kind() == compilerir.FieldRelation {
			return evaluate.Field{}, false, nil
		}
		selection[index] = golem.RuntimeReadSelectionInput{Kind: golem.RuntimeReadScalar, Field: fieldID}
	}
	request, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: endpoint.TargetModelID(),
		Selection: selection, Projection: golem.ProjectionSelect,
	})
	if err != nil {
		return evaluate.Field{}, false, err
	}
	prepared, err := caller.Prepare(request)
	if err != nil {
		return evaluate.Field{}, false, nil
	}
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return evaluate.Field{}, false, nil
	}
	correlation, err := equalityCondition(app.registry, app.provider, policyir.ModelID(endpoint.TargetModelID()), components)
	if err != nil {
		return evaluate.Field{}, false, err
	}
	planned, err = readplan.WithAdditionalWhere(planned, correlation)
	if err == nil {
		planned, err = readplan.WithMaximumTake(planned, 1)
	}
	if err != nil {
		return evaluate.Field{}, false, err
	}
	rows, err := executePlan(ctx, app, caller.executor, golem.ReadFindMany, planned)
	if err != nil || len(rows) != 1 {
		return evaluate.Field{}, false, nil
	}
	field, err := evaluate.ToOneField(policyir.FieldID(endpoint.FieldID()), policyir.ModelID(endpoint.TargetModelID()), rows[0].record)
	return field, err == nil, err
}

func snapshotEventPrincipal[P any](principal P, snapshot func(P) (P, error)) (P, error) {
	if snapshot != nil {
		value, err := snapshot(principal)
		if err != nil {
			var zero P
			return zero, events.Failure(events.CodeSubscriptionRevalidation)
		}
		return value, nil
	}
	if err := validateImmutablePrincipal(reflect.ValueOf(principal), "principal"); err != nil {
		var zero P
		return zero, events.Failure(events.CodeSubscriptionRevalidation)
	}
	return principal, nil
}

func validateImmutablePrincipal(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateImmutablePrincipal(value.Elem(), path+".(dynamic)")
	case reflect.Struct:
		typ := value.Type()
		for index := 0; index < value.NumField(); index++ {
			if err := validateImmutablePrincipal(value.Field(index), path+"."+typ.Field(index).Name); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateImmutablePrincipal(value.Index(index), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return fmt.Errorf("GOLEM_SUBSCRIPTION_REVALIDATION: %s has mutable or aliasing kind %s; configure SnapshotPrincipal", path, value.Kind())
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return nil
	default:
		return fmt.Errorf("GOLEM_SUBSCRIPTION_REVALIDATION: %s has unsupported kind %s; configure SnapshotPrincipal", path, value.Kind())
	}
}
