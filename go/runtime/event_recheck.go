package runtime

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type eventRecheck struct {
	interval time.Duration
	check    func(context.Context) error
}

func frozenEventRecheck(request golem.FrozenEventRequest) eventRecheck {
	interval, check, present := request.Recheck()
	if !present {
		return eventRecheck{}
	}
	return eventRecheck{interval: interval, check: check}
}

func (app *App[P, A]) startEventRecheck(stream *subscription.Stream[any], state *callerEventSubscription[P], custom eventRecheck) {
	if every := app.eventLimits.RevalidateEvery; every > 0 {
		stream.Recheck(every, func(ctx context.Context) error {
			checkContext, release := subscriptionCheckContext(ctx, state.context)
			defer release()
			return app.revalidateEventSubscription(checkContext, state)
		})
	}
	if custom.check != nil && custom.interval > 0 {
		stream.Recheck(custom.interval, func(ctx context.Context) error {
			checkContext, release := subscriptionCheckContext(ctx, state.context)
			defer release()
			return custom.check(checkContext)
		})
	}
}

func subscriptionCheckContext(ctx, subscriber context.Context) (context.Context, func()) {
	derived, cancel := context.WithCancel(context.WithoutCancel(subscriber))
	stop := context.AfterFunc(ctx, cancel)
	return derived, func() {
		stop()
		cancel()
	}
}

func (app *App[P, A]) revalidateEventSubscription(ctx context.Context, state *callerEventSubscription[P]) error {
	fresh, err := app.ForPrincipal(ctx, state.principal)
	if err != nil {
		return events.Failure(events.CodeSubscriptionRevalidation)
	}
	model := policyir.ModelID(state.model)
	if fresh.policies.PolicyError(model) != nil {
		return events.Failure(events.CodeSubscriptionRevalidation)
	}
	policy, present := fresh.policies.Policy(model)
	if !present {
		return events.Failure(events.CodeSubscriptionForbidden)
	}
	constraint, err := policyresolve.RowConstraint(policy, policyir.ActionRead, model)
	if err != nil {
		return events.Failure(events.CodeSubscriptionForbidden)
	}
	if truth, constant := constraint.Constant(); constant && !truth {
		return events.Failure(events.CodeSubscriptionForbidden)
	}
	if _, _, err := app.eventHookedRead(ctx, fresh, state); err != nil {
		if code, _ := events.CodeOf(err); code == events.CodeSubscriptionRevalidation {
			return events.Failure(events.CodeSubscriptionForbidden)
		}
		return err
	}
	return nil
}
