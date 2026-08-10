package subscription

import (
	"context"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
)

type SourceFactory func(context.Context, events.Subscription) (events.Stream, error)

type SourceDisposition uint8

const (
	SourceTransient SourceDisposition = iota + 1
	SourceTerminal
)

type SourceClassifier func(error) SourceDisposition

type Evaluator[T any] func(context.Context, events.Notice, SubscriberKey) (Evaluation[T], error)
type StatefulEvaluator[T any] func(context.Context, events.Notice, SubscriberKey, any) (Evaluation[T], error)
type Cloner[T any] func(T) (T, error)

type Evaluation[T any] struct {
	value   T
	deliver bool
	reason  events.SuppressionReason
}

func Deliver[T any](value T) Evaluation[T] { return Evaluation[T]{value: value, deliver: true} }
func Suppress[T any](reason events.SuppressionReason) Evaluation[T] {
	return Evaluation[T]{reason: reason}
}

type Config[T any] struct {
	Generation     golem.SchemaDigest
	EventSchema    golem.EventSchemaDigest
	Model          golem.ModelID
	Limits         events.Limits
	Source         SourceFactory
	ClassifySource SourceClassifier
	Evaluate       Evaluator[T]
	EvaluateState  StatefulEvaluator[T]
	Clone          Cloner[T]
	Observer       events.Observer
}

type ModelHub[T any] struct {
	mu      sync.Mutex
	config  Config[T]
	limits  events.Limits
	members map[uint64]*member[T]
	nextID  uint64
	nextRun uint64
	runs    map[uint64]*hubRun[T]
	closed  bool
	wg      sync.WaitGroup
}

type hubRun[T any] struct {
	id     uint64
	ctx    context.Context
	cancel context.CancelFunc
	input  chan events.Notice
	jobs   chan evaluationJob[T]
}

type member[T any] struct {
	id    uint64
	runID uint64
	key   SubscriberKey
	queue chan T
	done  chan struct{}
	once  sync.Once
	err   error
	state any
	drop  func(any)
}

type Stream[T any] struct {
	hub    *ModelHub[T]
	id     uint64
	member *member[T]
	stopMu sync.Mutex
	stop   func() bool
	closed bool
	once   sync.Once
}

type evaluationJob[T any] struct {
	notice  events.Notice
	key     SubscriberKey
	state   any
	members []uint64
	runID   uint64
	done    chan struct{}
}

func NewModelHub[T any](config Config[T]) (*ModelHub[T], error) {
	if config.EventSchema == (golem.EventSchemaDigest{}) {
		config.EventSchema = golem.EventSchemaDigest(config.Generation)
	}
	limits, err := events.NormalizeLimits(config.Limits)
	if err != nil || config.Generation == (golem.SchemaDigest{}) || config.EventSchema == (golem.EventSchemaDigest{}) || config.Model == (golem.ModelID{}) || config.Source == nil || (config.Evaluate == nil) == (config.EvaluateState == nil) || config.Clone == nil {
		return nil, events.Failure(events.CodeEventConfig)
	}
	if config.ClassifySource == nil {
		config.ClassifySource = defaultSourceClassifier
	}
	return &ModelHub[T]{config: config, limits: limits, members: make(map[uint64]*member[T]), runs: make(map[uint64]*hubRun[T])}, nil
}

func (hub *ModelHub[T]) Subscribe(ctx context.Context, key SubscriberKey) (*Stream[T], error) {
	return hub.subscribe(ctx, key, nil, nil)
}

// SubscribeWithState transfers one opaque, runtime-owned subscriber state into
// the membership. Stateful evaluation is deliberately restricted to an
// unshareable key: without a security-equivalence proof one member's retained
// principal/request must never evaluate another member's notice. drop runs
// exactly once when membership is removed for any reason.
func (hub *ModelHub[T]) SubscribeWithState(ctx context.Context, key SubscriberKey, state any, drop func(any)) (*Stream[T], error) {
	if state == nil || hub.config.EvaluateState == nil || key.shareable {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	return hub.subscribe(ctx, key, state, drop)
}

func (hub *ModelHub[T]) subscribe(ctx context.Context, key SubscriberKey, state any, drop func(any)) (*Stream[T], error) {
	if ctx == nil || ctx.Err() != nil || key.generation != hub.config.Generation || key.model != hub.config.Model {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	run := hub.activeRunLocked()
	created := false
	if run == nil {
		run = hub.newRunLocked()
		created = true
	}
	hub.nextID++
	item := &member[T]{id: hub.nextID, runID: run.id, key: key, queue: make(chan T, hub.limits.SubscriberQueue), done: make(chan struct{}), state: state, drop: drop}
	hub.members[item.id] = item
	hub.mu.Unlock()
	if created {
		hub.launchRun(run)
	}
	stream := &Stream[T]{hub: hub, id: item.id, member: item}
	stream.installStop(context.AfterFunc(ctx, func() { stream.closeWith(events.CodeSubscriptionCancelled) }))
	events.Observe(hub.config.Observer, ctx, hub.config.Model, "", events.ObservationHubMembership, events.OutcomeSuccess, "", 0, 0, hub.limits.SubscriberQueue, 0, 1)
	return stream, nil
}

func (hub *ModelHub[T]) activeRunLocked() *hubRun[T] {
	for _, item := range hub.members {
		if run := hub.runs[item.runID]; run != nil {
			return run
		}
	}
	return nil
}

func (hub *ModelHub[T]) newRunLocked() *hubRun[T] {
	hub.nextRun++
	ctx, cancel := context.WithCancel(context.Background())
	run := &hubRun[T]{id: hub.nextRun, ctx: ctx, cancel: cancel, input: make(chan events.Notice, hub.limits.HubInputQueue), jobs: make(chan evaluationJob[T], hub.limits.EvaluationConcurrency)}
	hub.runs[run.id] = run
	hub.wg.Add(2 + hub.limits.EvaluationConcurrency)
	return run
}

func (hub *ModelHub[T]) launchRun(run *hubRun[T]) {
	go hub.sourceLoop(run)
	go hub.dispatchLoop(run)
	for index := 0; index < hub.limits.EvaluationConcurrency; index++ {
		go hub.evaluationWorker(run)
	}
}

func (hub *ModelHub[T]) sourceLoop(run *hubRun[T]) {
	defer hub.wg.Done()
	subscription, err := eventvalue.NewRoutedSubscription(hub.config.Generation, hub.config.EventSchema, hub.config.Model)
	if err != nil {
		hub.terminateRun(run.id, events.CodeSubscriptionSourceClosed)
		return
	}
	backoff := hub.limits.RetryBase
	for {
		if run.ctx.Err() != nil {
			return
		}
		stream, err := hub.config.Source(run.ctx, subscription)
		if err != nil {
			if hub.config.ClassifySource(err) == SourceTerminal {
				hub.terminateRun(run.id, events.CodeSubscriptionSourceClosed)
				return
			}
			if !waitContext(run.ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, hub.limits.RetryCap)
			continue
		}
		if stream == nil {
			hub.terminateRun(run.id, events.CodeSubscriptionSourceClosed)
			return
		}
		backoff = hub.limits.RetryBase
		stopClose := context.AfterFunc(run.ctx, func() { _ = stream.Close() })
		for {
			notice, receiveErr := stream.Recv(run.ctx)
			if receiveErr != nil {
				stopClose()
				_ = stream.Close()
				if run.ctx.Err() != nil {
					return
				}
				if hub.config.ClassifySource(receiveErr) == SourceTerminal {
					hub.terminateRun(run.id, events.CodeSubscriptionSourceClosed)
					return
				}
				events.Observe(hub.config.Observer, run.ctx, hub.config.Model, "", events.ObservationTransportReconnect, events.OutcomeFailure, "", 0, len(run.input), cap(run.input), 0, 1)
				if !waitContext(run.ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff, hub.limits.RetryCap)
				break
			}
			if notice.EventSchemaDigest() != hub.config.EventSchema || notice.ModelID() != hub.config.Model {
				stopClose()
				_ = stream.Close()
				hub.terminateRun(run.id, events.CodeSubscriptionSourceClosed)
				return
			}
			select {
			case <-run.ctx.Done():
				stopClose()
				_ = stream.Close()
				return
			case run.input <- notice:
				events.Observe(hub.config.Observer, run.ctx, notice.ModelID(), notice.Action(), events.ObservationTransportReceive, events.OutcomeSuccess, "", 0, len(run.input), cap(run.input), 0, 1)
			}
		}
	}
}

func (hub *ModelHub[T]) dispatchLoop(run *hubRun[T]) {
	defer hub.wg.Done()
	defer close(run.jobs)
	for {
		select {
		case <-run.ctx.Done():
			return
		case notice := <-run.input:
			groups := hub.groupsFor(run.id, notice)
			pending := make([]chan struct{}, 0, len(groups))
			for _, job := range groups {
				select {
				case <-run.ctx.Done():
					return
				case run.jobs <- job:
					pending = append(pending, job.done)
				}
			}
			for _, done := range pending {
				select {
				case <-run.ctx.Done():
					return
				case <-done:
				}
			}
		}
	}
}

func (hub *ModelHub[T]) groupsFor(runID uint64, notice events.Notice) []evaluationJob[T] {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	groups := make(map[evaluationKey]*evaluationJob[T])
	for id, item := range hub.members {
		if item.runID != runID {
			continue
		}
		identity := item.key.forEvent(notice.EventID())
		job := groups[identity]
		if job == nil {
			job = &evaluationJob[T]{notice: notice, key: item.key, state: item.state, runID: runID, done: make(chan struct{})}
			groups[identity] = job
		}
		job.members = append(job.members, id)
	}
	result := make([]evaluationJob[T], 0, len(groups))
	for _, job := range groups {
		result = append(result, *job)
	}
	return result
}

func (hub *ModelHub[T]) evaluationWorker(run *hubRun[T]) {
	defer hub.wg.Done()
	for job := range run.jobs {
		if run.ctx.Err() != nil {
			close(job.done)
			continue
		}
		started := time.Now()
		result, err := hub.evaluate(run.ctx, job)
		if err != nil {
			code := normalizedEvaluationCode(err)
			for _, id := range job.members {
				hub.remove(id, job.runID, code)
			}
			events.Observe(hub.config.Observer, run.ctx, job.notice.ModelID(), job.notice.Action(), events.ObservationEvaluation, events.OutcomeFailure, "", 0, 0, hub.limits.EvaluationConcurrency, time.Since(started), 1)
			close(job.done)
			continue
		}
		if !result.deliver {
			events.Observe(hub.config.Observer, run.ctx, job.notice.ModelID(), job.notice.Action(), events.ObservationSuppression, events.OutcomeSuppressed, result.reason, 0, 0, 0, time.Since(started), int64(len(job.members)))
			close(job.done)
			continue
		}
		for _, id := range job.members {
			owned, cloneErr := hub.clone(result.value)
			if cloneErr != nil {
				hub.remove(id, job.runID, events.CodeSubscriptionSourceClosed)
				continue
			}
			hub.enqueue(id, job.runID, owned, job.notice)
		}
		events.Observe(hub.config.Observer, run.ctx, job.notice.ModelID(), job.notice.Action(), events.ObservationEvaluation, events.OutcomeSuccess, "", 0, 0, hub.limits.EvaluationConcurrency, time.Since(started), 1)
		close(job.done)
	}
}

// evaluate is the process-safety boundary around per-subscriber application
// work. In production EvaluateState rebuilds the caller and invokes generated
// policy factories, so a panic here is an untrusted policy-build failure, not
// permission to terminate the process or strand the hub worker.
func (hub *ModelHub[T]) evaluate(ctx context.Context, job evaluationJob[T]) (result Evaluation[T], err error) {
	defer func() {
		if recover() != nil {
			result = Evaluation[T]{}
			err = events.Failure(events.CodeSubscriptionSourceClosed)
		}
	}()
	if hub.config.EvaluateState != nil {
		return hub.config.EvaluateState(ctx, job.notice, job.key, job.state)
	}
	return hub.config.Evaluate(ctx, job.notice, job.key)
}

// clone isolates one member from another after shared evaluation. A cloner is
// still application/runtime code; its panic closes only that member with the
// same sanitized terminal code used for an ordinary clone error.
func (hub *ModelHub[T]) clone(value T) (owned T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			owned = zero
			err = events.Failure(events.CodeSubscriptionSourceClosed)
		}
	}()
	return hub.config.Clone(value)
}

func (hub *ModelHub[T]) enqueue(id, runID uint64, value T, notice events.Notice) {
	hub.mu.Lock()
	item := hub.members[id]
	if item == nil || item.runID != runID {
		hub.mu.Unlock()
		return
	}
	select {
	case item.queue <- value:
		depth := len(item.queue)
		hub.mu.Unlock()
		events.Observe(hub.config.Observer, context.Background(), notice.ModelID(), notice.Action(), events.ObservationDelivery, events.OutcomeSuccess, "", 0, depth, cap(item.queue), 0, 1)
	default:
		cleanup := hub.removeLocked(item, events.CodeSubscriptionOverflow)
		hub.mu.Unlock()
		runCleanup(cleanup)
		events.Observe(hub.config.Observer, context.Background(), notice.ModelID(), notice.Action(), events.ObservationOverflow, events.OutcomeFailure, "", 0, cap(item.queue), cap(item.queue), 0, 1)
	}
}

func (hub *ModelHub[T]) remove(id, runID uint64, code events.ErrorCode) {
	hub.mu.Lock()
	cleanup := func() {}
	if item := hub.members[id]; item != nil && item.runID == runID {
		cleanup = hub.removeLocked(item, code)
	}
	hub.mu.Unlock()
	runCleanup(cleanup)
}

// removeLocked severs every hub-owned reference while the mutex is held and
// returns application cleanup to run after unlock. A drop callback may block,
// reenter Stream.Close/Shutdown, or acquire unrelated application locks; none
// of those behaviors may stall the hub mutex or deadlock cancellation.
func (hub *ModelHub[T]) removeLocked(item *member[T], code events.ErrorCode) func() {
	cleanup := func() {}
	delete(hub.members, item.id)
	item.once.Do(func() {
		item.err = events.Failure(code)
		state, drop := item.state, item.drop
		item.state, item.drop = nil, nil
		if drop != nil {
			cleanup = func() { drop(state) }
		}
		close(item.done)
	})
	for _, other := range hub.members {
		if other.runID == item.runID {
			return cleanup
		}
	}
	if run := hub.runs[item.runID]; run != nil {
		delete(hub.runs, item.runID)
		run.cancel()
	}
	return cleanup
}

func (hub *ModelHub[T]) terminateRun(runID uint64, code events.ErrorCode) {
	hub.mu.Lock()
	cleanups := make([]func(), 0)
	for _, item := range hub.members {
		if item.runID == runID {
			cleanups = append(cleanups, hub.removeLocked(item, code))
		}
	}
	if run := hub.runs[runID]; run != nil {
		delete(hub.runs, runID)
		run.cancel()
	}
	hub.mu.Unlock()
	for _, cleanup := range cleanups {
		runCleanup(cleanup)
	}
}

// State release is best-effort application cleanup. It runs after hub-owned
// references have already been severed, and its panic must not undo removal or
// escape a worker, cancellation callback, or shutdown path.
func runCleanup(cleanup func()) {
	defer func() { _ = recover() }()
	cleanup()
}

func (stream *Stream[T]) Recv(ctx context.Context) (T, error) {
	var zero T
	if ctx == nil {
		return zero, events.Failure(events.CodeSubscriptionCancelled)
	}
	select {
	case <-stream.member.done:
		return zero, stream.member.err
	default:
	}
	select {
	case <-ctx.Done():
		return zero, events.Failure(events.CodeSubscriptionCancelled)
	case <-stream.member.done:
		return zero, stream.member.err
	case value := <-stream.member.queue:
		return value, nil
	}
}

func (stream *Stream[T]) Close() error {
	stream.closeWith(events.CodeSubscriptionCancelled)
	return nil
}

func (stream *Stream[T]) installStop(stop func() bool) {
	if stop == nil {
		return
	}
	stream.stopMu.Lock()
	if stream.closed {
		stream.stopMu.Unlock()
		stop()
		return
	}
	stream.stop = stop
	stream.stopMu.Unlock()
}

func (stream *Stream[T]) closeStop() func() bool {
	stream.stopMu.Lock()
	stream.closed = true
	stop := stream.stop
	stream.stop = nil
	stream.stopMu.Unlock()
	return stop
}

func (stream *Stream[T]) closeWith(code events.ErrorCode) {
	remove := false
	stream.once.Do(func() {
		if stop := stream.closeStop(); stop != nil {
			stop()
		}
		remove = true
	})
	if remove {
		stream.hub.remove(stream.id, stream.member.runID, code)
	}
}

func (hub *ModelHub[T]) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return events.Failure(events.CodeSubscriptionCancelled)
	}
	hub.mu.Lock()
	cleanups := make([]func(), 0)
	if !hub.closed {
		hub.closed = true
		for _, item := range hub.members {
			cleanups = append(cleanups, hub.removeLocked(item, events.CodeSubscriptionCancelled))
		}
		for id, run := range hub.runs {
			delete(hub.runs, id)
			run.cancel()
		}
	}
	hub.mu.Unlock()
	for _, cleanup := range cleanups {
		runCleanup(cleanup)
	}
	done := make(chan struct{})
	go func() { hub.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return events.Failure(events.CodeSubscriptionCancelled)
	case <-done:
		return nil
	}
}

func defaultSourceClassifier(err error) SourceDisposition {
	code, ok := events.CodeOf(err)
	if ok && code == events.CodeEventTransport {
		return SourceTransient
	}
	return SourceTerminal
}

func normalizedEvaluationCode(err error) events.ErrorCode {
	code, ok := events.CodeOf(err)
	if !ok {
		return events.CodeSubscriptionSourceClosed
	}
	switch code {
	case events.CodeSubscriptionRevalidation, events.CodeSubscriptionCancelled, events.CodeSubscriptionSourceClosed:
		return code
	default:
		return events.CodeSubscriptionSourceClosed
	}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}
