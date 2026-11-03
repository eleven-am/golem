package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/loadmix/loadmix"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/jackc/pgx/v5/pgconn"
)

type Role int

const (
	RoleReader Role = iota
	RoleListener
	RoleChatter
)

type simUser struct {
	id      golem.UUID
	role    Role
	caller  *loadmix.Caller[loadmix.Principal]
	article golem.UUID
	audio   golem.UUID
	thread  golem.UUID
	message atomic.Value
	counter atomic.Int64

	articles *subscription
	messages *subscription
}

type regenTarget struct {
	caller *loadmix.Caller[loadmix.Principal]
	audio  golem.UUID
}

type Harness struct {
	environment Environment
	database    *Database
	handle      *provider.Database
	app         *loadmix.App[loadmix.Principal]
	counters    *Counters
	tracker     *tracker
	users       []*simUser
	regen       []regenTarget
	readers     []*simUser
	listeners   []*simUser
	chatters    []*simUser

	root      context.Context
	ctx       context.Context
	cancel    context.CancelFunc
	drainCtx  context.Context
	stopDrain context.CancelFunc
	faults    []string
	origin    time.Time
	sequence  atomic.Int64

	mu            sync.Mutex
	samples       []Sample
	arrivals      map[Class][]time.Duration
	lags          []lagSample
	inflight      map[Class]*atomic.Int64
	background    sync.WaitGroup
	drainers      sync.WaitGroup
	streams       []golem.EventStream[loadmix.ArticleEvent]
	chatStreams   []golem.EventStream[loadmix.ChatMessageEvent]
	publisher     chan error
	stopPublisher context.CancelFunc
}

type lagSample struct {
	at  time.Duration
	lag time.Duration
}

func uuidFrom(namespace byte, index int) golem.UUID {
	var id golem.UUID
	id[0] = namespace
	id[4] = byte(index >> 24)
	id[5] = byte(index >> 16)
	id[6] = byte(index >> 8)
	id[7] = byte(index)
	id[8] = 0x80
	return id
}

func NewHarness(ctx context.Context, environment Environment, users int, regenerate int) (*Harness, error) {
	harness := &Harness{environment: environment, root: ctx, counters: newCounters(), arrivals: map[Class][]time.Duration{}, inflight: map[Class]*atomic.Int64{}}
	harness.drainCtx, harness.stopDrain = context.WithCancel(ctx)
	harness.tracker = newTracker(harness.abort, harness.noteLag)
	for _, class := range allClasses {
		harness.inflight[class] = &atomic.Int64{}
	}
	database, err := environment.CreateDatabase(ctx)
	if err != nil {
		return nil, err
	}
	harness.database = database
	handle, err := postgresql.Open(ctx, postgresql.Config{DataSourceName: database.DSN, Pool: postgresql.PoolConfig{MaximumOpen: environment.Pool, MaximumIdle: environment.Pool}})
	if err != nil {
		return nil, errors.Join(err, harness.Close())
	}
	harness.handle = handle
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8192})
	if err != nil {
		return nil, errors.Join(err, harness.Close())
	}
	app, err := loadmix.Open(ctx, loadmix.Config[loadmix.Principal]{
		Database:       handle,
		EventTransport: transport,
		ResolvePrincipal: func(_ context.Context, principal loadmix.Principal) (loadmix.Actor, error) {
			return loadmix.Actor{UserID: principal.UserID, Authenticated: true}, nil
		},
		SnapshotPrincipal:   func(principal loadmix.Principal) (loadmix.Principal, error) { return principal, nil },
		SnapshotActor:       func(actor loadmix.Actor) (loadmix.Actor, error) { return actor, nil },
		AuditPrincipal:      func(principal loadmix.Principal) string { return principal.UserID.String() },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
		Observer:            harness.counters,
	})
	if err != nil {
		return nil, errors.Join(err, harness.Close())
	}
	harness.app = app
	if err := harness.seed(ctx, users, regenerate); err != nil {
		return nil, errors.Join(err, harness.Close())
	}
	return harness, nil
}

func (harness *Harness) seed(ctx context.Context, users int, regenerate int) error {
	system := harness.app.System()
	readers, listeners := users*50/100, users*30/100
	for index := 0; index < users; index++ {
		user := &simUser{id: uuidFrom(1, index), article: uuidFrom(2, index), audio: uuidFrom(3, index), thread: uuidFrom(4, index)}
		switch {
		case index < readers:
			user.role = RoleReader
		case index < readers+listeners:
			user.role = RoleListener
		default:
			user.role = RoleChatter
		}
		message := uuidFrom(5, index)
		user.message.Store(message)
		harness.tracker.register(message, user.id)
		if _, err := system.Users.Create(ctx, loadmix.Users.Create(loadmix.Users.ID.Create(user.id), loadmix.Users.Email.Create(fmt.Sprintf("user%d@loadmix.test", index)))); err != nil {
			return fmt.Errorf("seed user %d: %w", index, err)
		}
		if err := harness.seedArticle(ctx, user.id, user.article, user.audio); err != nil {
			return fmt.Errorf("seed article %d: %w", index, err)
		}
		if _, err := system.ChatThreads.Create(ctx, loadmix.ChatThreads.Create(loadmix.ChatThreads.ID.Create(user.thread), loadmix.ChatThreads.ArticleID.Create(user.article), loadmix.ChatThreads.UserID.Create(user.id))); err != nil {
			return fmt.Errorf("seed thread %d: %w", index, err)
		}
		if _, err := system.ChatMessages.Create(ctx, loadmix.ChatMessages.Create(loadmix.ChatMessages.ID.Create(message), loadmix.ChatMessages.ThreadID.Create(user.thread), loadmix.ChatMessages.Role.Create("assistant"), loadmix.ChatMessages.Content.Create("seed"))); err != nil {
			return fmt.Errorf("seed message %d: %w", index, err)
		}
		caller, err := harness.app.ForPrincipal(ctx, loadmix.Principal{UserID: user.id})
		if err != nil {
			return err
		}
		user.caller = caller
		harness.users = append(harness.users, user)
		switch user.role {
		case RoleReader:
			harness.readers = append(harness.readers, user)
		case RoleListener:
			harness.listeners = append(harness.listeners, user)
		default:
			harness.chatters = append(harness.chatters, user)
		}
	}
	for index := 0; index < regenerate; index++ {
		owner := harness.users[index%len(harness.users)]
		article, audio := uuidFrom(6, index), uuidFrom(7, index)
		if err := harness.seedArticle(ctx, owner.id, article, audio); err != nil {
			return fmt.Errorf("seed regeneration target %d: %w", index, err)
		}
		harness.regen = append(harness.regen, regenTarget{caller: owner.caller, audio: audio})
	}
	return nil
}

func (harness *Harness) seedArticle(ctx context.Context, owner, article, audio golem.UUID) error {
	harness.tracker.register(article, owner)
	system := harness.app.System()
	if _, err := system.Articles.Create(ctx, loadmix.Articles.Create(loadmix.Articles.ID.Create(article), loadmix.Articles.UserID.Create(owner), loadmix.Articles.URL.Create("https://loadmix.test/"+article.String()), loadmix.Articles.Title.Create("seed"))); err != nil {
		return err
	}
	_, err := system.ArticleAudios.Create(ctx, loadmix.ArticleAudios.Create(loadmix.ArticleAudios.ID.Create(audio), loadmix.ArticleAudios.ArticleID.Create(article), loadmix.ArticleAudios.UserID.Create(owner), loadmix.ArticleAudios.SourceContentHash.Create("seed"), loadmix.ArticleAudios.Voice.Create("seed"), loadmix.ArticleAudios.TTSModel.Create("seed")))
	return err
}

func (harness *Harness) undelivered(ctx context.Context) (int, error) {
	connection, err := sql.Open("pgx", harness.database.DSN)
	if err != nil {
		return 0, err
	}
	defer func() { _ = connection.Close() }()
	var pending int
	err = connection.QueryRowContext(ctx, `SELECT count(*) FROM "_golem"."_golem_outbox_delivery" WHERE status IN ('pending','leased')`).Scan(&pending)
	return pending, err
}

func (harness *Harness) StartEvents(ctx context.Context) error {
	publisherContext, stop := context.WithCancel(ctx)
	harness.stopPublisher = stop
	harness.publisher = make(chan error, 1)
	go func() { harness.publisher <- harness.app.RunEventPublisher(publisherContext) }()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		pending, err := harness.undelivered(ctx)
		if err != nil {
			return err
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("seed events were not delivered: %d still pending", pending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	for _, user := range harness.users {
		articles, err := user.caller.Articles.Events(ctx)
		if err != nil {
			return fmt.Errorf("subscribe articles: %w", err)
		}
		messages, err := user.caller.ChatMessages.Events(ctx)
		if err != nil {
			return fmt.Errorf("subscribe chat messages: %w", err)
		}
		harness.streams = append(harness.streams, articles)
		harness.chatStreams = append(harness.chatStreams, messages)
		user.articles = harness.tracker.subscribe(user.id, ClassArticle)
		user.messages = harness.tracker.subscribe(user.id, ClassChatMessage)
		harness.drainers.Add(2)
		go func() {
			defer harness.drainers.Done()
			drain(harness.drainCtx, harness.tracker, user.articles, articles, func(event loadmix.ArticleEvent) eventKey {
				return eventKey{row: event.ID(), id: event.Metadata().EventID(), recordedAt: event.Metadata().RecordedAt()}
			})
		}()
		go func() {
			defer harness.drainers.Done()
			drain(harness.drainCtx, harness.tracker, user.messages, messages, func(event loadmix.ChatMessageEvent) eventKey {
				return eventKey{row: event.ID(), id: event.Metadata().EventID(), recordedAt: event.Metadata().RecordedAt()}
			})
		}()
	}
	return nil
}

func (harness *Harness) noteLag(recordedAt time.Time) {
	now := time.Now()
	harness.mu.Lock()
	if harness.origin.IsZero() {
		harness.mu.Unlock()
		return
	}
	harness.lags = append(harness.lags, lagSample{at: now.Sub(harness.origin), lag: now.Sub(recordedAt)})
	harness.mu.Unlock()
}

func (harness *Harness) abort() {
	harness.mu.Lock()
	cancel := harness.cancel
	harness.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (harness *Harness) Close() error {
	var failures []error
	harness.tracker.stopListening()
	if harness.cancel != nil {
		harness.cancel()
	}
	harness.stopDrain()
	for _, stream := range harness.streams {
		failures = append(failures, stream.Close())
	}
	for _, stream := range harness.chatStreams {
		failures = append(failures, stream.Close())
	}
	if harness.stopPublisher != nil {
		harness.stopPublisher()
		select {
		case <-harness.publisher:
		case <-time.After(15 * time.Second):
			failures = append(failures, errors.New("event publisher did not stop"))
		}
	}
	harness.background.Wait()
	harness.drainers.Wait()
	if harness.handle != nil {
		failures = append(failures, harness.handle.Close())
	}
	if harness.database != nil {
		failures = append(failures, harness.database.Drop())
	}
	return errors.Join(failures...)
}

func classify(err error) Failure {
	if err == nil {
		return FailureNone
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && postgres.Code == "40P01" {
		return FailureDeadlock
	}
	var public *golem.Error
	if errors.As(err, &public) && public.Code == golem.CodeConflict {
		return FailureConflict
	}
	return FailureOther
}

func (harness *Harness) arrive(class Class, at time.Duration) {
	harness.mu.Lock()
	harness.arrivals[class] = append(harness.arrivals[class], at)
	harness.mu.Unlock()
}

func (harness *Harness) perform(class Class, arrived time.Duration, run func(context.Context) error) {
	counter := harness.inflight[class]
	counter.Add(1)
	err := run(harness.root)
	done := time.Since(harness.origin)
	counter.Add(-1)
	harness.mu.Lock()
	harness.samples = append(harness.samples, Sample{Class: class, Arrived: arrived, Done: done, Failure: classify(err)})
	harness.mu.Unlock()
}

func (harness *Harness) submit(class Class, at time.Duration, run func(context.Context) error) {
	harness.arrive(class, at)
	harness.background.Add(1)
	go func() {
		defer harness.background.Done()
		harness.perform(class, at, run)
	}()
}

func (harness *Harness) sleepUntil(at time.Duration) bool {
	delay := at - time.Since(harness.origin)
	if delay <= 0 {
		return harness.ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-harness.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (harness *Harness) poisson(rate float64, seed int64, fire func(rng *rand.Rand, at time.Duration)) {
	if rate <= 0 {
		return
	}
	harness.background.Add(1)
	go func() {
		defer harness.background.Done()
		rng := rand.New(rand.NewSource(seed))
		next := time.Duration(0)
		for {
			next += time.Duration(rng.ExpFloat64() / rate * float64(time.Second))
			if !harness.sleepUntil(next) {
				return
			}
			fire(rng, next)
		}
	}()
}

func (harness *Harness) at(offset time.Duration, run func(at time.Duration)) {
	harness.background.Add(1)
	go func() {
		defer harness.background.Done()
		if harness.sleepUntil(offset) {
			run(offset)
		}
	}()
}

func (harness *Harness) Samples() ([]Sample, map[Class][]time.Duration, []lagSample) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	arrivals := map[Class][]time.Duration{}
	for class, list := range harness.arrivals {
		arrivals[class] = append([]time.Duration(nil), list...)
	}
	return append([]Sample(nil), harness.samples...), arrivals, append([]lagSample(nil), harness.lags...)
}
