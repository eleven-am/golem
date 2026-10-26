package runtime

import (
	"context"
	"slices"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	providerapi "github.com/eleven-am/golem/go/provider"
)

const processLocalPublisherReacquireAttempts = 3

type processLocalPublisherGuard struct {
	delivery  eventprovider.Coordinator
	observer  events.Observer
	strict    bool
	namespace string
	base      time.Duration
	cap       time.Duration
	release   func()
	lost      <-chan struct{}
	err       error
}

func (app *App[P, A]) processLocalPublisherGuarded() bool {
	if app.eventDelivery == nil || app.databaseHandle == nil || app.eventTransportABI.Scope() != events.TransportScopeProcessLocal {
		return false
	}
	return slices.Contains(app.databaseHandle.Capabilities().Features(), providerapi.FeatureAdvisoryLocks)
}

func (app *App[P, A]) startProcessLocalPublisherGuard(ctx context.Context) (*processLocalPublisherGuard, error) {
	if !app.processLocalPublisherGuarded() {
		return nil, nil
	}
	namespace, _ := app.registry.PhysicalSystemNamespace(app.eventProvider)
	guard := &processLocalPublisherGuard{
		delivery: app.eventDelivery, observer: app.eventObserver,
		strict: app.eventLimits.RequireExclusiveProcessLocalPublisher, namespace: string(namespace),
		base: app.eventLimits.RetryBase, cap: app.eventLimits.RetryCap,
	}
	if guard.acquire(ctx) {
		return guard, nil
	}
	guard.report(ctx)
	if guard.strict {
		return nil, guard.failure()
	}
	return guard, nil
}

func (guard *processLocalPublisherGuard) acquire(ctx context.Context) bool {
	release, lost, held, err := guard.delivery.HoldExclusivePublisher(ctx)
	guard.err = err
	if err != nil || !held {
		if release != nil {
			release()
		}
		return false
	}
	guard.release, guard.lost = release, lost
	return true
}

func (guard *processLocalPublisherGuard) drop() {
	if guard.release != nil {
		guard.release()
	}
	guard.release, guard.lost = nil, nil
}

func (guard *processLocalPublisherGuard) report(ctx context.Context) {
	events.Observe(guard.observer, ctx, golem.ModelID{}, "", events.ObservationTopology, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
}

func (guard *processLocalPublisherGuard) failure() error {
	if guard.err != nil {
		return events.Failf(events.CodeEventConfig, "the process-local publisher lock for namespace %s is unavailable", guard.namespace)
	}
	return events.Failf(events.CodeEventConfig, "another process-local publisher holds namespace %s", guard.namespace)
}

func (guard *processLocalPublisherGuard) delay(attempt int) time.Duration {
	delay := guard.base
	for index := 0; index < attempt && delay < guard.cap; index++ {
		delay *= 2
	}
	return min(delay, guard.cap)
}

func (guard *processLocalPublisherGuard) run(ctx context.Context) error {
	defer guard.drop()
	reported := guard.release == nil
	attempt := 0
	for {
		if guard.release != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-guard.lost:
				guard.drop()
				reported = false
				attempt = 0
			}
		}
		timer := time.NewTimer(guard.delay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		attempt++
		if guard.acquire(ctx) {
			attempt = 0
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if guard.strict && attempt >= processLocalPublisherReacquireAttempts {
			guard.report(ctx)
			return guard.failure()
		}
		if !guard.strict && !reported {
			guard.report(ctx)
			reported = true
		}
	}
}

func (app *App[P, A]) reportSubscriberWithoutPublisher(ctx context.Context) {
	if app.eventTransportABI.Scope() != events.TransportScopeProcessLocal || app.eventRunning.Load() {
		return
	}
	if app.eventTopologyReported.CompareAndSwap(false, true) {
		events.Observe(app.eventObserver, ctx, golem.ModelID{}, "", events.ObservationTopology, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
	}
}
