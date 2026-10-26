package subscription

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/events"
)

func (stream *Stream[T]) Recheck(interval time.Duration, check func(context.Context) error) bool {
	if stream == nil || stream.hub == nil || interval <= 0 || check == nil {
		return false
	}
	hub := stream.hub
	ctx, cancel := context.WithCancel(context.Background())
	hub.mu.Lock()
	if hub.members[stream.id] != stream.member {
		hub.mu.Unlock()
		cancel()
		return false
	}
	stream.member.cancels = append(stream.member.cancels, cancel)
	hub.wg.Add(1)
	hub.mu.Unlock()
	go hub.recheckLoop(ctx, stream, interval, check)
	return true
}

func (stream *Stream[T]) Terminate(code events.ErrorCode) {
	if stream == nil || stream.hub == nil {
		return
	}
	stream.closeWith(events.Failure(code))
}

func (hub *ModelHub[T]) recheckLoop(ctx context.Context, stream *Stream[T], interval time.Duration, check func(context.Context) error) {
	defer hub.wg.Done()
	for waitContext(ctx, jitterBackoff(interval)) {
		err := runRecheck(ctx, check)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			stream.Terminate(recheckCode(err))
			events.Observe(hub.config.Observer, context.Background(), hub.config.Model, "", events.ObservationEvaluation, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
			return
		}
	}
}

func runRecheck(ctx context.Context, check func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = events.Failure(events.CodeSubscriptionSourceClosed)
		}
	}()
	return check(ctx)
}

func recheckCode(err error) events.ErrorCode {
	code, ok := events.CodeOf(err)
	if !ok {
		return events.CodeSubscriptionForbidden
	}
	switch code {
	case events.CodeSubscriptionRevalidation, events.CodeSubscriptionForbidden, events.CodeSubscriptionResync, events.CodeSubscriptionSourceClosed:
		return code
	default:
		return events.CodeSubscriptionForbidden
	}
}
