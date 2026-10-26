package graphql

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/events"
)

type recheckedResponseStream struct {
	source    ResponseStream
	cancel    context.CancelFunc
	failed    context.Context
	markFail  context.CancelFunc
	mu        sync.Mutex
	failure   error
	closeOnce sync.Once
	closeErr  error
}

func validateSubscriptionRecheck[P any](recheck SubscriptionRecheck[P]) error {
	if recheck.Interval < 0 {
		return fmt.Errorf("GraphQL SubscriptionRecheck.Interval must not be negative, got %s", recheck.Interval)
	}
	if (recheck.Check == nil) != (recheck.Interval == 0) {
		return fmt.Errorf("GraphQL SubscriptionRecheck requires both Interval and Check, or neither")
	}
	return nil
}

func (server *Server[P]) subscribe(executor SubscriptionExecutor[P], ctx context.Context, principal P, operation Operation) (ResponseStream, error) {
	source, err := subscribeSafely(executor, ctx, principal, operation)
	if err != nil {
		return nil, err
	}
	recheck := server.config.SubscriptionRecheck
	if recheck.Check == nil {
		return source, nil
	}
	checkContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	failed, markFail := context.WithCancel(context.Background())
	stream := &recheckedResponseStream{source: source, cancel: cancel, failed: failed, markFail: markFail}
	report := server.config.ReportInternalError
	go stream.watch(checkContext, recheck.Interval, func(ctx context.Context) error {
		return runSubscriptionCheck(ctx, report, func(ctx context.Context) error {
			return recheck.Check(ctx, principal, operation)
		})
	})
	return stream, nil
}

func (stream *recheckedResponseStream) watch(ctx context.Context, interval time.Duration, check func(context.Context) error) {
	for {
		timer := time.NewTimer(jitterInterval(interval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		err := check(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			stream.fail(events.Failure(subscriptionRecheckCode(err)))
			return
		}
	}
}

func runSubscriptionCheck(ctx context.Context, report func(context.Context, error), check func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			report(ctx, errors.New("GraphQL subscription recheck panicked"))
			err = events.Failure(events.CodeSubscriptionForbidden)
		}
	}()
	return check(ctx)
}

func subscriptionRecheckCode(err error) events.ErrorCode {
	code, ok := events.CodeOf(err)
	if !ok {
		return events.CodeSubscriptionForbidden
	}
	switch code {
	case events.CodeSubscriptionForbidden, events.CodeSubscriptionRevalidation, events.CodeSubscriptionResync:
		return code
	default:
		return events.CodeSubscriptionForbidden
	}
}

func jitterInterval(interval time.Duration) time.Duration {
	floor := interval / 2
	if interval-floor <= 0 {
		return interval
	}
	return floor + time.Duration(rand.Int64N(int64(interval-floor)+1))
}

func (stream *recheckedResponseStream) fail(failure error) {
	stream.mu.Lock()
	if stream.failure == nil {
		stream.failure = failure
	}
	stream.mu.Unlock()
	stream.markFail()
	defer func() { _ = recover() }()
	_ = stream.closeSource()
}

func (stream *recheckedResponseStream) terminal() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.failure
}

func (stream *recheckedResponseStream) Recv(ctx context.Context) (Response, error) {
	if failure := stream.terminal(); failure != nil {
		return Response{}, failure
	}
	receive, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(stream.failed, cancel)
	response, err := stream.source.Recv(receive)
	stop()
	cancel()
	if failure := stream.terminal(); failure != nil {
		return Response{}, failure
	}
	return response, err
}

func (stream *recheckedResponseStream) Close() error {
	stream.cancel()
	stream.markFail()
	return stream.closeSource()
}

func (stream *recheckedResponseStream) closeSource() error {
	stream.closeOnce.Do(func() { stream.closeErr = stream.source.Close() })
	return stream.closeErr
}
