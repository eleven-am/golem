package runtime

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventoutbox "github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/observeexec"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/observe"
)

type eventPublisherRunner interface {
	Run(context.Context) error
}

type eventCDCWorker struct {
	adapter events.CDCAdapter
	emitter events.CDCEmitter
}

func (app *App[P, A]) initializeEventRuntime(adapters []events.CDCAdapter, audit events.OperatorAudit) error {
	if app == nil || app.registry == nil {
		return events.Failure(events.CodeEventConfig)
	}
	metadata := app.eventRegistry.Models()
	if len(metadata) == 0 {
		return nil
	}

	coordinator, err := app.eventCoordinator()
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	binding, err := newRuntimeEventBinding(app.eventSchemas, app.eventLimits.MaxEncodedEventBytes)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	publisher, err := eventoutbox.NewPublisherObserved(coordinator, app.eventSchemas, app.eventTransport, eventoutbox.Limits{
		ClaimGroups: app.eventLimits.ClaimRows, Concurrency: app.eventLimits.PublisherConcurrency,
		LeaseDuration: app.eventLimits.LeaseDuration, PublishTimeout: app.eventLimits.PublishTimeout,
		RetryBase: app.eventLimits.RetryBase, RetryCap: app.eventLimits.RetryCap,
		ShutdownGrace: app.eventLimits.ShutdownGrace, MaxEncodedBytes: app.eventLimits.MaxEncodedEventBytes,
		MaxBatchBytes: app.eventLimits.MaxEncodedBatchBytes,
		RetentionAge:  app.eventLimits.RetentionAge, RetentionEvery: app.eventLimits.RetentionEvery,
		RetentionRows: app.eventLimits.RetentionDeleteRows,
	}, app.eventObserver)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	operator, err := newRuntimeEventOperator(coordinator, audit, app.eventObserver)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	encoder, err := newCDCEventEncoder(app.registry, app.eventSchemas, app.eventLimits.MaxEncodedEventBytes)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}

	workers := make([]eventCDCWorker, len(adapters))
	for index, adapter := range adapters {
		emitter, emitterErr := eventcdc.NewEmitter(eventcdc.Config{Adapter: adapter, Transport: app.eventTransport, Encoder: encoder, Observer: app.eventObserver, MaxBatchBytes: app.eventLimits.MaxEncodedBatchBytes})
		if emitterErr != nil {
			return events.Failure(events.CodeEventConfig)
		}
		workers[index] = eventCDCWorker{adapter: adapter, emitter: emitter}
	}
	models := make([]golem.ModelID, len(metadata))
	for index, model := range metadata {
		models[index] = model.ModelID()
	}
	sort.Slice(models, func(left, right int) bool { return bytes.Compare(models[left][:], models[right][:]) < 0 })
	names, _, err := events.CDCAdapterCapabilities(app.eventProvider, adapters)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	sort.Strings(names)
	transportCapabilities, err := validateEventTransportTopology(app.eventProvider, app.eventTransport)
	if err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	if !events.AvailabilityOf(app.eventTransport) {
		return events.Failure(events.CodeEventConfig)
	}
	if err := validateEventTransportPayloadLimit(transportCapabilities, app.eventTransport, app.eventLimits.MaxEncodedEventBytes); err != nil {
		return events.Failure(events.CodeEventConfig)
	}
	bindable, bindableOK := app.eventTransport.(events.RuntimeBindableTransport)
	if transportCapabilities.Scope() == events.TransportScopeCrossProcess && !bindableOK {
		return events.Failure(events.CodeEventConfig)
	}
	// Binding is deliberately the final fallible Open action: a transport can
	// never retain a live decoder capability for an App that later fails.
	if bindableOK {
		if err := bindable.BindEventRuntime(binding); err != nil {
			return events.Failure(events.CodeEventConfig)
		}
	}

	app.eventPublisher = publisher
	app.eventOperator = operator
	app.eventAdapters = append([]events.CDCAdapter(nil), adapters...)
	app.eventCDCWorkers = workers
	app.eventAdapterNames = names
	app.eventModels = models
	app.eventTransportABI = transportCapabilities
	return nil
}

func (app *App[P, A]) eventCoordinator() (eventprovider.Coordinator, error) {
	namespace, ok := app.registry.PhysicalSystemNamespace(app.eventProvider)
	if !ok || namespace == "" {
		return nil, fmt.Errorf("event system namespace is absent")
	}
	switch app.eventProvider {
	case golem.SQLite:
		if namespace != "main" {
			return nil, fmt.Errorf("SQLite event system namespace is not main")
		}
		return sqliteprovider.New().EventCoordinator(app.database)
	case golem.PostgreSQL:
		return postgresprovider.New().EventCoordinatorAt(app.database, namespace)
	default:
		return nil, fmt.Errorf("event provider is unsupported")
	}
}

// EventLimits returns the normalized, app-owned event limits used by the
// publisher, subscriptions, and generated GraphQL transport.
func (app *App[P, A]) EventLimits() events.Limits {
	if app == nil {
		return events.Limits{}
	}
	return app.eventLimits
}

// EventCapabilities returns a representation-closed operational snapshot.
func (app *App[P, A]) EventCapabilities() events.Capabilities {
	if app == nil {
		return events.Capabilities{}
	}
	return events.RuntimeCapabilitiesWithTransportAvailability(
		app.eventProvider,
		[]string{"golem.fact.v1", "golem.fact.v2"},
		[]string{"golem.event.v1"},
		app.eventTransportABI,
		events.AvailabilityOf(app.eventTransport),
		app.eventPublisher != nil,
		app.eventRunning.Load(),
		app.eventModels,
		app.eventAdapterNames,
		len(app.eventAdapterNames) != 0,
	)
}

func (app *App[P, A]) EventOperator() events.Operator {
	if app == nil {
		return nil
	}
	return app.eventOperator
}

type eventRunResult struct {
	cdc bool
	err error
}

// RunEventPublisher owns the durable outbox publisher and every configured CDC
// adapter for the lifetime of ctx. Open performs no background work.
func (app *App[P, A]) RunEventPublisher(ctx context.Context) (resultErr error) {
	if app == nil || ctx == nil || app.eventPublisher == nil {
		return events.Failure(events.CodeEventConfig)
	}
	if !app.eventRunning.CompareAndSwap(false, true) {
		events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationLifecycleFailure, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
		return events.Failure(events.CodeEventPublisherRunning)
	}
	ctx, shutdownObservation := observeexec.Begin(ctx, app.observer, app.eventProvider, golem.ModelID{}, observe.KindShutdown, observe.OperationShutdownPublisher, observe.PhaseClose)
	defer func() {
		if shutdownObservation != nil {
			finishObservation(shutdownObservation, resultErr)
		}
	}()
	clearRunning := true
	defer func() {
		if clearRunning {
			app.eventRunning.Store(false)
		}
	}()
	if ctx.Err() != nil {
		events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationCancellation, events.OutcomeCancelled, "", 0, 0, 0, 0, 1)
		return nil
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	count := 1 + len(app.eventCDCWorkers)
	results := make(chan eventRunResult, count)
	runEventComponent(runContext, results, false, app.eventPublisher.Run)
	for _, worker := range app.eventCDCWorkers {
		owned := worker
		runEventComponent(runContext, results, true, func(componentContext context.Context) error {
			return owned.adapter.Run(componentContext, owned.emitter)
		})
	}

	select {
	case <-ctx.Done():
		cancel()
		remaining := waitEventComponents(results, count, app.eventLimits.ShutdownGrace)
		if remaining != 0 {
			clearRunning = false
			deferredObservation := shutdownObservation
			shutdownObservation = nil
			go clearEventRunningWhenDone(app, results, remaining, deferredObservation, nil)
		}
		events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationCancellation, events.OutcomeCancelled, "", 0, 0, 0, 0, 1)
		return nil
	case result := <-results:
		cancel()
		remaining := count - 1
		remaining = waitEventComponents(results, remaining, app.eventLimits.ShutdownGrace)
		var runErr error
		if result.cdc {
			runErr = events.Failure(events.CodeCDCUnavailable)
		} else {
			runErr = events.Failure(events.CodeEventSourceClosed)
		}
		if ctx.Err() != nil {
			runErr = nil
		}
		if remaining != 0 {
			clearRunning = false
			deferredObservation := shutdownObservation
			shutdownObservation = nil
			go clearEventRunningWhenDone(app, results, remaining, deferredObservation, runErr)
		}
		if ctx.Err() != nil {
			events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationCancellation, events.OutcomeCancelled, "", 0, 0, 0, 0, 1)
			return nil
		}
		events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationLifecycleFailure, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
		return runErr
	}
}

func runEventComponent(ctx context.Context, results chan<- eventRunResult, cdc bool, run func(context.Context) error) {
	go func() {
		result := eventRunResult{cdc: cdc}
		defer func() {
			if recover() != nil {
				result.err = events.Failure(events.CodeEventSourceClosed)
			}
			results <- result
		}()
		result.err = run(ctx)
	}()
}

func waitEventComponents(results <-chan eventRunResult, remaining int, grace time.Duration) int {
	if remaining == 0 {
		return 0
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case <-results:
			remaining--
		case <-timer.C:
			return remaining
		}
	}
	return 0
}

func clearEventRunningWhenDone[P, A any](app *App[P, A], results <-chan eventRunResult, remaining int, observation *observeexec.Span, resultErr error) {
	for remaining > 0 {
		<-results
		remaining--
	}
	app.eventRunning.Store(false)
	finishObservation(observation, resultErr)
}
