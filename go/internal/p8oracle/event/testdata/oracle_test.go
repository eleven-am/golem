package oracle_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/gorilla/websocket"
)

const (
	eventUserText    = "91000000-0000-0000-0000-000000000001"
	eventSessionText = "92000000-0000-0000-0000-000000000001"
	eventToken       = "p8-event-oracle-token"
)

type principalKey struct{}

type signalTransport struct {
	events.EventTransport
	subscribed chan struct{}
	count      atomic.Int64
}

func (transport *signalTransport) Subscribe(ctx context.Context, request events.Subscription) (events.Stream, error) {
	stream, err := transport.EventTransport.Subscribe(ctx, request)
	if err == nil {
		transport.count.Add(1)
		select {
		case transport.subscribed <- struct{}{}:
		default:
		}
	}
	return stream, err
}

func (transport *signalTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

type eventObservation struct {
	overflow   chan struct{}
	once       sync.Once
	overflows  atomic.Int64
	membership atomic.Int64
	evaluation atomic.Int64
	delivery   atomic.Int64
	suppressed atomic.Int64
	filtered   atomic.Int64
	denied     atomic.Int64
}

type displayBlocker struct {
	enabled atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newDisplayBlocker() *displayBlocker {
	return &displayBlocker{entered: make(chan struct{}), release: make(chan struct{})}
}

func (blocker *displayBlocker) ObservePostDisplayCodeLoad(ctx context.Context, _ []golem.UUID, _ social.DisplayCodeArgs) error {
	if blocker == nil || !blocker.enabled.Load() {
		return nil
	}
	blocker.once.Do(func() { close(blocker.entered) })
	select {
	case <-blocker.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (observer *eventObservation) ObserveGolem(_ context.Context, value observe.Observation) {
	recordObservationCoverage(value.Provider(), value.Operation())
	if value.Operation() == observe.OperationSubscriptionMembership && value.Outcome() == observe.OutcomeSuccess {
		observer.membership.Add(1)
	}
	switch value.Operation() {
	case observe.OperationSubscriptionEvaluation:
		observer.evaluation.Add(1)
	case observe.OperationSubscriptionDelivery:
		observer.delivery.Add(1)
	case observe.OperationSubscriptionSuppression:
		observer.suppressed.Add(1)
		switch value.Reason() {
		case observe.ReasonFiltered:
			observer.filtered.Add(1)
		case observe.ReasonAuthorization:
			observer.denied.Add(1)
		}
	}
	if value.Operation() == observe.OperationSubscriptionOverflow {
		observer.overflows.Add(1)
		observer.once.Do(func() { close(observer.overflow) })
	}
}

var observationCoverageMu sync.Mutex

func recordObservationCoverage(provider golem.Provider, operation observe.Operation) {
	path := os.Getenv("P8_OBSERVATION_COVERAGE_FILE")
	if path == "" {
		return
	}
	observationCoverageMu.Lock()
	defer observationCoverageMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic("open observation coverage sink")
	}
	if _, err := fmt.Fprintln(file, provider, operation); err != nil {
		_ = file.Close()
		panic("write observation coverage sink")
	}
	if err := file.Close(); err != nil {
		panic("close observation coverage sink")
	}
}

type fixture struct {
	t          *testing.T
	ctx        context.Context
	database   *provider.Database
	transport  *signalTransport
	app        *social.App[social.Principal]
	graph      *social.GraphQLServer
	server     *httptest.Server
	principal  social.Principal
	caller     *social.Caller[social.Principal]
	userID     golem.UUID
	otherID    golem.UUID
	sessionID  golem.UUID
	tokenHash  [32]byte
	resolve    atomic.Int64
	publisher  context.CancelFunc
	publisherD chan error
	observer   *eventObservation
	display    *displayBlocker
}

type wsFrame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type cancellationCanaryRouter struct {
	canaryID                  string
	cancelledNextFrames       int
	cancelledTerminalObserved bool
}

func (router *cancellationCanaryRouter) route(frame wsFrame) (bool, error) {
	switch frame.ID {
	case "cancel":
		switch frame.Type {
		case "next":
			router.cancelledNextFrames++
			if router.cancelledTerminalObserved {
				return false, errors.New("cancelled operation emitted next after terminal")
			}
			if router.cancelledNextFrames > 1 {
				return false, errors.New("cancelled operation exceeded buffered next bound")
			}
			if _, err := decodeGraphQLIDOnlyNext(frame.Payload); err != nil {
				return false, fmt.Errorf("cancelled operation next payload: %w", err)
			}
		case "error":
			if router.cancelledTerminalObserved || !bytes.Contains(frame.Payload, []byte(`"code":"GOLEM_SUBSCRIPTION_CANCELLED"`)) {
				return false, errors.New("invalid cancelled operation error terminal")
			}
			router.cancelledTerminalObserved = true
		case "complete":
			if router.cancelledTerminalObserved || len(frame.Payload) != 0 {
				return false, errors.New("invalid cancelled operation complete terminal")
			}
			router.cancelledTerminalObserved = true
		default:
			return false, errors.New("invalid cancelled operation frame type")
		}
		return false, nil
	case "connection-canary":
		if frame.Type != "next" {
			return false, errors.New("connection canary did not emit next")
		}
		id, err := decodeGraphQLIDOnlyNext(frame.Payload)
		if err != nil {
			return false, fmt.Errorf("connection canary next payload: %w", err)
		}
		if id != router.canaryID {
			return false, errors.New("connection canary identity mismatch")
		}
		return true, nil
	default:
		return false, errors.New("unknown GraphQL operation identity")
	}
}

func decodeGraphQLIDOnlyNext(payload json.RawMessage) (string, error) {
	var envelope struct {
		Data struct {
			PostEvents struct {
				ID string `json:"id"`
			} `json:"postEvents"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", errors.New("malformed GraphQL next envelope")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing GraphQL next value")
	}
	if _, err := golem.ParseUUID(envelope.Data.PostEvents.ID); err != nil {
		return "", errors.New("invalid GraphQL event identity")
	}
	return envelope.Data.PostEvents.ID, nil
}

type graphEventEnvelope struct {
	Data struct {
		PostEvents struct {
			EventID            string `json:"eventID"`
			CausationID        string `json:"causationID"`
			TransactionOrdinal int    `json:"transactionOrdinal"`
			Type               string `json:"type"`
			ID                 string `json:"id"`
			Entity             *struct {
				ID        string  `json:"id"`
				Title     string  `json:"title"`
				Body      *string `json:"body"`
				Published bool    `json:"published"`
			} `json:"entity"`
		} `json:"postEvents"`
	} `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func TestP8ExternalOracleScenario(t *testing.T) {
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "cross-entry-point":
		newFixture(t, events.Limits{}, nil).crossEntryPoint()
	case "fresh-authorization":
		newFixture(t, events.Limits{}, nil).freshAuthorization()
	case "overflow-cancellation-identity":
		newFixture(t, events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1}, nil).overflowCancellationIdentity()
	case "cdc-released-path":
		adapter := newControlledCDC(providerFromEnvironment())
		newFixture(t, events.Limits{}, adapter).cdcReleasedPath(adapter)
	default:
		t.Fatalf("unknown P8 event oracle scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func newFixture(t *testing.T, limits events.Limits, adapter events.CDCAdapter) *fixture {
	t.Helper()
	ctx := context.Background()
	database := openDatabase(t, ctx)
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 512})
	if err != nil {
		t.Fatal(err)
	}
	transport := &signalTransport{EventTransport: memory, subscribed: make(chan struct{}, 64)}
	observer := &eventObservation{overflow: make(chan struct{})}
	result := &fixture{t: t, ctx: ctx, database: database, transport: transport, observer: observer, display: newDisplayBlocker()}
	adapters := []events.CDCAdapter(nil)
	if adapter != nil {
		adapters = append(adapters, adapter)
	}
	app, err := social.Open(ctx, social.Config[social.Principal]{
		Database: database, EventTransport: transport, EventLimits: limits, CDCAdapters: adapters, Observer: observer,
		ResolvePrincipal: func(ctx context.Context, principal social.Principal) (social.Actor, error) {
			result.resolve.Add(1)
			if principal.Development {
				return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
			}
			if principal.TokenHash == ([32]byte{}) {
				return social.Actor{}, nil
			}
			query := database.UnsafeSQLX().Rebind(`SELECT user_id,expires_at FROM sessions WHERE token_hash=?`)
			var value string
			var expiresAt time.Time
			switch database.Provider() {
			case golem.SQLite:
				var microseconds int64
				if err := database.UnsafeSQLX().QueryRowxContext(ctx, query, principal.TokenHash[:]).Scan(&value, &microseconds); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return social.Actor{}, nil
					}
					return social.Actor{}, errors.New("principal resolution failed")
				}
				expiresAt = time.UnixMicro(microseconds).UTC()
			case golem.PostgreSQL:
				if err := database.UnsafeSQLX().QueryRowxContext(ctx, query, principal.TokenHash[:]).Scan(&value, &expiresAt); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return social.Actor{}, nil
					}
					return social.Actor{}, errors.New("principal resolution failed")
				}
			default:
				return social.Actor{}, errors.New("principal resolution failed")
			}
			if !expiresAt.After(time.Now().UTC()) {
				return social.Actor{}, nil
			}
			id, err := golem.ParseUUID(value)
			if err != nil {
				return social.Actor{}, nil
			}
			return social.Actor{UserID: id, Authenticated: true}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-event-oracle-principal" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result.app = app
	result.userID = mustUUID(t, eventUserText)
	result.otherID = mustUUID(t, "91000000-0000-0000-0000-000000000002")
	result.sessionID = mustUUID(t, eventSessionText)
	result.tokenHash = sha256.Sum256([]byte(eventToken))
	seedIdentity(t, app.System(), result.userID, result.sessionID, result.tokenHash)
	if _, err := app.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(result.otherID), social.Users.Handle.Create("event-other"), social.Users.Email.Create("event-other@p8.test"),
	)); err != nil {
		result.close()
		t.Fatal(err)
	}
	result.principal = social.Principal{TokenHash: result.tokenHash}
	result.caller, err = app.ForPrincipal(ctx, result.principal)
	if err != nil {
		result.close()
		t.Fatal(err)
	}
	result.graph, err = app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(ctx context.Context) (social.Principal, bool) {
			value, ok := ctx.Value(principalKey{}).(social.Principal)
			return value, ok
		},
		ReportInternalError: func(_ context.Context, err error) {
			if code := eventCode(err); code == events.CodeSubscriptionCancelled || code == events.CodeEventSourceClosed {
				return
			}
			t.Errorf("trusted GraphQL event error: %v", err)
		},
	})
	if err != nil {
		result.close()
		t.Fatal(err)
	}
	result.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalKey{}, result.principal)
		ctx = social.WithPostDisplayCodeLoaderObserver(ctx, result.display)
		result.graph.Handler().ServeHTTP(writer, request.WithContext(ctx))
	}))
	t.Cleanup(result.close)
	return result
}

func (f *fixture) close() {
	if f.publisher != nil {
		f.publisher()
		select {
		case err := <-f.publisherD:
			if err != nil && !isCancellation(err) {
				f.t.Errorf("publisher shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			f.t.Error("publisher did not stop")
		}
		f.publisher = nil
	}
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.graph != nil {
		if err := f.graph.Shutdown(context.Background()); err != nil {
			f.t.Error(err)
		}
		f.graph = nil
	}
	if f.database != nil {
		if err := f.database.Close(); err != nil {
			f.t.Error(err)
		}
		f.database = nil
	}
}

func (f *fixture) startPublisher() {
	f.t.Helper()
	if f.publisher != nil {
		return
	}
	ctx, cancel := context.WithCancel(f.ctx)
	f.publisher = cancel
	f.publisherD = make(chan error, 1)
	go func() { f.publisherD <- f.app.RunEventPublisher(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !f.app.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !f.app.EventCapabilities().PublisherRunning() {
		f.t.Fatal("public event publisher did not report running")
	}
}

func (f *fixture) awaitSubscriptions(count int) {
	f.t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for f.transport.count.Load() < int64(count) {
		select {
		case <-f.transport.subscribed:
		case <-deadline.C:
			f.t.Fatalf("transport subscriptions=%d want=%d", f.transport.count.Load(), count)
		}
	}
}

func (f *fixture) awaitMemberships(count int) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.observer.membership.Load() < int64(count) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.observer.membership.Load() < int64(count) {
		f.t.Fatalf("subscription memberships=%d want=%d", f.observer.membership.Load(), count)
	}
}

func (f *fixture) crossEntryPoint() {
	id := mustUUID(f.t, "93000000-0000-0000-0000-000000000001")
	callerStream, err := f.caller.Posts.Events(f.ctx,
		golem.EventWhere(social.Posts.ID.Eq(id)),
		golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title, social.Posts.Body, social.Posts.Published),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	defer callerStream.Close()
	connection := f.openGraphQLSubscription("cross", `subscription Cross($id: UUID!) { postEvents(where: {id: {equals: $id}}) { eventID causationID transactionOrdinal type id entity { id title body published } } }`, map[string]any{"id": id.String()})
	defer connection.Close()
	f.awaitSubscriptions(1)
	f.awaitMemberships(2)
	f.startPublisher()
	f.createPost(f.app.System().Posts, f.otherID, id, "cross-entry", "P8_CONDITIONAL_MASK_CANARY", true)

	caller := recvCaller(f.t, callerStream)
	graph := recvGraphEvent(f.t, connection)
	if graph.Errors != nil || graph.Data.PostEvents.ID != id.String() || graph.Data.PostEvents.Entity == nil {
		f.t.Fatalf("GraphQL event=%+v", graph)
	}
	metadata := caller.Metadata()
	entity, present := caller.Entity()
	if !present {
		f.t.Fatal("caller selected entity is absent")
	}
	title, titleOK := golem.Value(entity, social.Posts.Title).Get()
	body, bodyOK := golem.Value(entity, social.Posts.Body).Get()
	published, publishedOK := golem.Value(entity, social.Posts.Published).Get()
	if caller.ID() != id || metadata.Action() != golem.EventCreated || title != "cross-entry" || !titleOK || bodyOK || !published || !publishedOK {
		f.t.Fatalf("caller event id=%s action=%s title=%q/%t body=%q/%t published=%t/%t", caller.ID(), metadata.Action(), title, titleOK, body, bodyOK, published, publishedOK)
	}
	if _, leaked := golem.Value(entity, social.Posts.Metadata).Get(); leaked {
		f.t.Fatal("caller event projection included an unselected field")
	}
	if graph.Data.PostEvents.EventID != uuidText(metadata.EventID()) || graph.Data.PostEvents.CausationID != uuidText(metadata.CausationID()) || graph.Data.PostEvents.TransactionOrdinal != int(metadata.TransactionOrdinal()) || graph.Data.PostEvents.Type != "CREATED" || graph.Data.PostEvents.Entity.ID != id.String() || graph.Data.PostEvents.Entity.Title != title || graph.Data.PostEvents.Entity.Body != nil || graph.Data.PostEvents.Entity.Published != published {
		f.t.Fatalf("caller/GraphQL normalized event mismatch caller=%x/%x/%d graph=%+v", metadata.EventID(), metadata.CausationID(), metadata.TransactionOrdinal(), graph.Data.PostEvents)
	}
}

func (f *fixture) freshAuthorization() {
	privateID := mustUUID(f.t, "93000000-0000-0000-0000-000000000011")
	publicID := mustUUID(f.t, "93000000-0000-0000-0000-000000000012")
	callerStream, err := f.caller.Posts.Events(f.ctx, golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title, social.Posts.Published))
	if err != nil {
		f.t.Fatal(err)
	}
	defer callerStream.Close()
	connection := f.openGraphQLSubscription("fresh", `subscription Fresh { postEvents { type id entity { id title published } } }`, nil)
	defer connection.Close()
	f.awaitSubscriptions(1)
	f.awaitMemberships(2)
	resolveBefore := f.resolve.Load()
	f.startPublisher()
	if _, err := f.app.System().Sessions.Delete(f.ctx, social.Sessions.ByID.Value(f.sessionID)); err != nil {
		f.t.Fatal(err)
	}
	f.createPost(f.app.System().Posts, f.userID, privateID, "suppressed-private", "P8_PRIVATE_EVENT_CANARY", false)
	f.createPost(f.app.System().Posts, f.userID, publicID, "delivered-public", "P8_PUBLIC_EVENT_CANARY", true)
	deadline := time.Now().Add(3 * time.Second)
	for (f.observer.delivery.Load() < 2 || f.observer.suppressed.Load() < 2) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	caller := recvCaller(f.t, callerStream)
	graph := recvGraphEvent(f.t, connection)
	if caller.ID() != publicID || graph.Data.PostEvents.ID != publicID.String() {
		f.t.Fatalf("private event was not suppressed caller=%s graph=%s", caller.ID(), graph.Data.PostEvents.ID)
	}
	_, present := caller.Entity()
	if !present {
		f.t.Fatal("public event entity absent")
	}
	if graph.Data.PostEvents.Entity == nil {
		f.t.Fatal("fresh anonymous authorization omitted the public GraphQL entity")
	}
	if f.resolve.Load() < resolveBefore+4 {
		f.t.Fatalf("principal was not freshly resolved for both streams and both notices: before=%d after=%d", resolveBefore, f.resolve.Load())
	}
	assertNoAdditionalCallerEvent(f.t, callerStream)
	f.assertNoAdditionalGraphEvent(connection)
}

func (f *fixture) overflowCancellationIdentity() {
	identityID := mustUUID(f.t, "93000000-0000-0000-0000-000000000021")
	identityCaller, err := f.caller.Posts.Events(f.ctx, golem.EventWhere(social.Posts.ID.Eq(identityID)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer identityCaller.Close()
	identityGraph := f.openGraphQLSubscription("identity", `subscription Identity($id: UUID!) { postEvents(where: {id: {equals: $id}}) { eventID causationID transactionOrdinal type id } }`, map[string]any{"id": identityID.String()})
	defer identityGraph.Close()
	f.awaitSubscriptions(1)
	f.awaitMemberships(2)
	f.startPublisher()
	f.createPost(f.caller.Posts, f.userID, identityID, "identity", "identity body", true)
	callerIdentity := recvCaller(f.t, identityCaller)
	graphIdentity := recvGraphEvent(f.t, identityGraph)
	metadata := callerIdentity.Metadata()
	if callerIdentity.ID() != identityID || graphIdentity.Data.PostEvents.ID != identityID.String() || graphIdentity.Data.PostEvents.EventID != uuidText(metadata.EventID()) || graphIdentity.Data.PostEvents.CausationID != uuidText(metadata.CausationID()) || graphIdentity.Data.PostEvents.TransactionOrdinal != int(metadata.TransactionOrdinal()) {
		f.t.Fatalf("event identity parity caller=%s/%x/%x/%d graph=%+v", callerIdentity.ID(), metadata.EventID(), metadata.CausationID(), metadata.TransactionOrdinal(), graphIdentity.Data.PostEvents)
	}

	overflowStream, err := f.caller.Posts.Events(f.ctx, golem.EventWhere(social.Posts.Title.StartsWith("overflow-")))
	if err != nil {
		f.t.Fatal(err)
	}
	defer overflowStream.Close()
	f.display.enabled.Store(true)
	graphOverflow := f.openGraphQLSubscription("overflow", `subscription Overflow { postEvents(where: {title: {startsWith: "overflow-"}}) { id entity { id displayCode(prefix: "overflow:") } } }`, nil)
	defer graphOverflow.Close()
	f.awaitMemberships(4)
	for index := 0; index < 16; index++ {
		id := mustUUID(f.t, fmt.Sprintf("93000000-0000-0000-0000-%012d", 30+index))
		f.createPost(f.caller.Posts, f.userID, id, fmt.Sprintf("overflow-%d", index), "overflow body", true)
		if index == 0 {
			select {
			case <-f.display.entered:
			case <-time.After(5 * time.Second):
				f.t.Fatal("GraphQL computed projection did not block its event consumer")
			}
		}
	}
	select {
	case <-f.observer.overflow:
	case <-time.After(5 * time.Second):
		f.t.Fatal("bounded subscriber queue did not report overflow")
	}
	overflowDeadline := time.Now().Add(5 * time.Second)
	for f.observer.overflows.Load() < 2 && time.Now().Before(overflowDeadline) {
		time.Sleep(time.Millisecond)
	}
	if f.observer.overflows.Load() < 2 {
		f.t.Fatalf("caller and GraphQL queues did not independently overflow: observations=%d", f.observer.overflows.Load())
	}
	overflowCtx, cancelOverflow := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancelOverflow()
	if _, err := overflowStream.Recv(overflowCtx); eventCode(err) != events.CodeSubscriptionOverflow {
		f.t.Fatalf("caller overflow code=%q error=%v", eventCode(err), err)
	}
	close(f.display.release)
	var graphOverflowFrame wsFrame
	for attempt := 0; attempt < 20; attempt++ {
		graphOverflowFrame = readFrame(f.t, graphOverflow)
		if graphOverflowFrame.Type == "error" {
			break
		}
	}
	if graphOverflowFrame.Type != "error" || !bytes.Contains(graphOverflowFrame.Payload, []byte(`"code":"GOLEM_SUBSCRIPTION_OVERFLOW"`)) || bytes.Contains(graphOverflowFrame.Payload, []byte("overflow body")) {
		f.t.Fatalf("GraphQL overflow frame=%+v", graphOverflowFrame)
	}

	cancelCtx, cancel := context.WithCancel(f.ctx)
	cancelStream, err := f.caller.Posts.Events(cancelCtx)
	if err != nil {
		f.t.Fatal(err)
	}
	f.awaitMemberships(5)
	cancel()
	if _, err := cancelStream.Recv(f.ctx); eventCode(err) != events.CodeSubscriptionCancelled {
		f.t.Fatalf("caller cancellation code=%q error=%v", eventCode(err), err)
	}

	graphCancel := f.openGraphQLSubscription("cancel", `subscription Cancel { postEvents { id } }`, nil)
	defer graphCancel.Close()
	f.awaitMemberships(6)
	if err := graphCancel.WriteJSON(map[string]any{"id": "cancel", "type": "complete"}); err != nil {
		f.t.Fatal(err)
	}
	canaryID := mustUUID(f.t, "93000000-0000-0000-0000-000000000099")
	if err := graphCancel.WriteJSON(map[string]any{
		"id": "connection-canary", "type": "subscribe",
		"payload": map[string]any{
			"query":     `subscription ConnectionCanary($id: UUID!) { postEvents(where: {id: {equals: $id}}) { id } }`,
			"variables": map[string]any{"id": canaryID.String()},
		},
	}); err != nil {
		f.t.Fatal(err)
	}
	f.awaitMemberships(7)
	f.createPost(f.caller.Posts, f.userID, canaryID, "connection-canary", "connection body", true)
	assertCancellationCanaryRouterRegression(f.t, canaryID.String())
	deadline := time.Now().Add(5 * time.Second)
	router := cancellationCanaryRouter{canaryID: canaryID.String()}
	var canary wsFrame
	for {
		frame := readFrameBefore(f.t, graphCancel, deadline)
		done, err := router.route(frame)
		if err != nil {
			f.t.Fatalf("GraphQL cancellation frame=%+v: %v", frame, err)
		}
		if done {
			canary = frame
			break
		}
	}
	if canary.Type != "next" || canary.ID != "connection-canary" || !bytes.Contains(canary.Payload, []byte(canaryID.String())) {
		f.t.Fatalf("GraphQL cancellation did not preserve connection for the canary operation: %+v", canary)
	}
	if err := graphCancel.WriteJSON(map[string]any{"id": "connection-canary", "type": "complete"}); err != nil {
		f.t.Fatal(err)
	}

	_, callerErr := f.caller.Posts.Events(f.ctx, golem.EventSelect[social.Post](social.Posts.ID), golem.EventSelect[social.Post](social.Posts.Title))
	if callerErr == nil || !strings.Contains(callerErr.Error(), "GOLEM_SUBSCRIPTION_INVALID") {
		f.t.Fatalf("caller invalid subscription error=%v", callerErr)
	}
	invalid := f.openGraphQLSubscription("invalid", `subscription Invalid { postEvents { unknownEventField } }`, nil)
	defer invalid.Close()
	frame := readFrame(f.t, invalid)
	if frame.Type != "error" || !bytes.Contains(frame.Payload, []byte("GRAPHQL_VALIDATION_FAILED")) || bytes.Contains(frame.Payload, []byte("P8_PRIVATE_EVENT_CANARY")) {
		f.t.Fatalf("GraphQL invalid subscription frame=%+v", frame)
	}
}

func assertCancellationCanaryRouterRegression(t *testing.T, canaryID string) {
	t.Helper()
	const priorID = "93000000-0000-0000-0000-000000000038"
	payload := func(id string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"data":{"postEvents":{"id":%q}}}`, id))
	}
	router := cancellationCanaryRouter{canaryID: canaryID}
	if done, err := router.route(wsFrame{ID: "cancel", Type: "next", Payload: payload(priorID)}); err != nil || done {
		t.Fatalf("route exact buffered prior-ID frame: done=%t error=%v", done, err)
	}
	if done, err := router.route(wsFrame{ID: "connection-canary", Type: "next", Payload: payload(canaryID)}); err != nil || !done {
		t.Fatalf("route exact connection canary frame: done=%t error=%v", done, err)
	}

	invalid := []struct {
		name   string
		frames []wsFrame
	}{
		{name: "more-than-one-buffered-next", frames: []wsFrame{
			{ID: "cancel", Type: "next", Payload: payload(priorID)},
			{ID: "cancel", Type: "next", Payload: payload(priorID)},
		}},
		{name: "next-after-terminal", frames: []wsFrame{
			{ID: "cancel", Type: "complete"},
			{ID: "cancel", Type: "next", Payload: payload(priorID)},
		}},
		{name: "protected-field", frames: []wsFrame{{
			ID: "cancel", Type: "next", Payload: json.RawMessage(`{"data":{"postEvents":{"id":"93000000-0000-0000-0000-000000000038","body":"P8_PRIVATE_EVENT_CANARY"}}}`),
		}}},
		{name: "malformed-payload", frames: []wsFrame{{
			ID: "cancel", Type: "next", Payload: json.RawMessage(`{"data":{"postEvents":`),
		}}},
		{name: "unknown-operation", frames: []wsFrame{{
			ID: "unexpected", Type: "next", Payload: payload(priorID),
		}}},
		{name: "wrong-canary-identity", frames: []wsFrame{{
			ID: "connection-canary", Type: "next", Payload: payload(priorID),
		}}},
	}
	for _, probe := range invalid {
		router := cancellationCanaryRouter{canaryID: canaryID}
		var err error
		for _, frame := range probe.frames {
			_, err = router.route(frame)
			if err != nil {
				break
			}
		}
		if err == nil {
			t.Fatalf("cancellation router accepted %s", probe.name)
		}
	}
}

type controlledCDC struct {
	provider golem.Provider
	started  chan struct{}
	batches  chan events.CDCBatchInput
	emitted  chan error
	once     sync.Once
}

func newControlledCDC(provider golem.Provider) *controlledCDC {
	return &controlledCDC{provider: provider, started: make(chan struct{}), batches: make(chan events.CDCBatchInput, 1), emitted: make(chan error, 1)}
}

func (adapter *controlledCDC) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "p8-row12-released", Version: "1", Provider: adapter.provider}
}

func (*controlledCDC) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}

func (adapter *controlledCDC) Run(ctx context.Context, emitter events.CDCEmitter) error {
	adapter.once.Do(func() { close(adapter.started) })
	select {
	case <-ctx.Done():
		return nil
	case batch := <-adapter.batches:
		err := emitter.Emit(ctx, batch)
		adapter.emitted <- err
		if err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (f *fixture) cdcReleasedPath(adapter *controlledCDC) {
	capabilities := f.app.EventCapabilities()
	wantIdentity := adapter.Identity().CanonicalName()
	if !capabilities.ExternalWritesObserved() || fmt.Sprint(capabilities.CDCAdapterIdentities()) != fmt.Sprint([]string{wantIdentity}) {
		f.t.Fatalf("released capabilities external=%t adapters=%v", capabilities.ExternalWritesObserved(), capabilities.CDCAdapterIdentities())
	}
	id := mustUUID(f.t, "93000000-0000-0000-0000-000000000041")
	callerStream, err := f.caller.Posts.Events(f.ctx, golem.EventWhere(social.Posts.ID.Eq(id)), golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Title, social.Posts.Published))
	if err != nil {
		f.t.Fatal(err)
	}
	defer callerStream.Close()
	connection := f.openGraphQLSubscription("cdc", `subscription CDC($id: UUID!) { postEvents(where: {id: {equals: $id}}) { eventID causationID transactionOrdinal type id entity { id title published } } }`, map[string]any{"id": id.String()})
	defer connection.Close()
	f.awaitSubscriptions(1)
	f.awaitMemberships(2)
	f.startPublisher()
	select {
	case <-adapter.started:
	case <-time.After(5 * time.Second):
		f.t.Fatal("released RunEventPublisher did not start configured CDC adapter")
	}
	row := f.insertExternalPost(id, "external-cdc")
	adapter.batches <- events.CDCBatchInput{
		SourceTransactionID: "external-tx-row12", RecordedAt: time.Date(2026, 8, 9, 12, 0, 0, 123000000, time.UTC), Cursor: []byte("cursor-row12"),
		Changes: []events.CDCChangeInput{{Ordinal: 1, Model: social.GolemGeneratedPostDescriptor.Metadata().ModelID(), Action: golem.EventCreated, After: &row}},
	}
	select {
	case err := <-adapter.emitted:
		if err != nil {
			f.t.Fatalf("released CDC emitter: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("released CDC emitter did not complete")
	}
	caller := recvCaller(f.t, callerStream)
	graph := recvGraphEvent(f.t, connection)
	if caller.ID() != id || caller.Metadata().Action() != golem.EventCreated || graph.Data.PostEvents.ID != id.String() || graph.Data.PostEvents.Type != "CREATED" || graph.Data.PostEvents.Entity == nil || graph.Data.PostEvents.Entity.Title != "external-cdc" {
		f.t.Fatalf("CDC did not enter shared released stream caller=%s/%s graph=%+v", caller.ID(), caller.Metadata().Action(), graph.Data.PostEvents)
	}
	if graph.Data.PostEvents.EventID != uuidText(caller.Metadata().EventID()) || graph.Data.PostEvents.CausationID != uuidText(caller.Metadata().CausationID()) || graph.Data.PostEvents.TransactionOrdinal != 1 {
		f.t.Fatalf("CDC caller/GraphQL identity mismatch caller=%x/%x/%d graph=%+v", caller.Metadata().EventID(), caller.Metadata().CausationID(), caller.Metadata().TransactionOrdinal(), graph.Data.PostEvents)
	}
}

type postCreator interface {
	Create(context.Context, social.PostCreateInput, ...golem.Projection[social.Post]) (golem.Row[social.Post], error)
}

func (f *fixture) createPost(client postCreator, authorID, id golem.UUID, title, body string, published bool) {
	f.t.Helper()
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		f.t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56")
	if err != nil {
		f.t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":false}`))
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = client.Create(f.ctx, social.Posts.Create(
		social.Posts.ID.Create(id), social.Posts.AuthorID.Create(authorID), social.Posts.Title.Create(title), social.Posts.Body.Create(body),
		social.Posts.Published.Create(published), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Visibility.Create(social.VisibilityPublic), social.Posts.Topics.Create(golem.List[string]{"p8", "events"}),
	))
	if err != nil {
		f.t.Fatalf("create post %s: %v", id, err)
	}
}

func (f *fixture) insertExternalPost(id golem.UUID, title string) golem.RuntimeModelRow {
	f.t.Helper()
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		f.t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56.000000")
	if err != nil {
		f.t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("0.00")
	if err != nil {
		f.t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":false}`))
	if err != nil {
		f.t.Fatal(err)
	}
	topics := golem.List[string]{"external", "cdc"}
	instant := time.Date(2026, 8, 9, 12, 0, 1, 123456000, time.UTC)
	statement := f.database.UnsafeSQLX().Rebind(`INSERT INTO posts
(id,author_id,title,body,published,reactions,priority,views,momentum,rating,budget,live_date,live_time,metadata,visibility,topics,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	arguments := []any{id.String(), f.userID.String(), title, "external CDC body", true, int16(0), int32(0), int64(0), float32(0), float64(0), "0.00", date.String(), clock.String(), string(metadata.Bytes()), string(social.VisibilityPublic), string(mustJSONBytes(f.t, topics)), instant, instant}
	if f.database.Provider() == golem.SQLite {
		arguments[10] = int64(0)
		arguments[12] = "12:34:56.000000"
		arguments[16], arguments[17] = instant.UnixMicro(), instant.UnixMicro()
	}
	if _, err := f.database.UnsafeSQLX().ExecContext(f.ctx, statement, arguments...); err != nil {
		f.t.Fatalf("external SQL insert: %v", err)
	}
	fields := social.GolemGeneratedPostDescriptor.Metadata().ScanFields()
	if len(fields) != 18 {
		f.t.Fatalf("Post scan fields=%d want=18", len(fields))
	}
	cells := []golem.RuntimeReadCell{
		golem.RuntimePresentReadCell(fields[0], id, nil),
		golem.RuntimePresentReadCell(fields[1], f.userID, nil),
		golem.RuntimePresentReadCell(fields[2], title, nil),
		golem.RuntimePresentReadCell(fields[3], "external CDC body", nil),
		golem.RuntimePresentReadCell(fields[4], true, nil),
		golem.RuntimePresentReadCell(fields[5], int16(0), nil),
		golem.RuntimePresentReadCell(fields[6], int32(0), nil),
		golem.RuntimePresentReadCell(fields[7], int64(0), nil),
		golem.RuntimePresentReadCell(fields[8], float32(0), nil),
		golem.RuntimePresentReadCell(fields[9], float64(0), nil),
		golem.RuntimePresentReadCell(fields[10], decimal, nil),
		golem.RuntimePresentReadCell(fields[11], date, nil),
		golem.RuntimePresentReadCell(fields[12], clock, nil),
		golem.RuntimePresentReadCell(fields[13], metadata, cloneJSONDocument),
		golem.RuntimePresentReadCell(fields[14], social.VisibilityPublic, nil),
		golem.RuntimePresentReadCell(fields[15], topics, func(value golem.List[string]) golem.List[string] { return append(golem.List[string](nil), value...) }),
		golem.RuntimePresentReadCell(fields[16], instant, nil),
		golem.RuntimePresentReadCell(fields[17], instant, nil),
	}
	row, err := golem.RuntimeCDCModelRow(social.GolemGeneratedPostDescriptor.Metadata().ModelID(), cells...)
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func cloneJSONDocument(value golem.JSON[any]) golem.JSON[any] {
	result, err := golem.NewJSONDocument[any](value.Bytes())
	if err != nil {
		panic(err)
	}
	return result
}

func seedIdentity(t *testing.T, system social.System[social.Principal], userID, sessionID golem.UUID, tokenHash [32]byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("event-user"), social.Users.Email.Create("event-user@p8.test"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Sessions.Create(ctx, social.Sessions.Create(
		social.Sessions.ID.Create(sessionID), social.Sessions.UserID.Create(userID), social.Sessions.TokenHash.Create(tokenHash[:]), social.Sessions.ExpiresAt.Create(time.Now().UTC().Add(time.Hour)),
	)); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) openGraphQLSubscription(id, query string, variables map[string]any) *websocket.Conn {
	f.t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http"), nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		f.t.Fatalf("dial GraphQL WebSocket status=%d: %v", status, err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		connection.Close()
		f.t.Fatal(err)
	}
	if frame := readFrame(f.t, connection); frame.Type != "connection_ack" {
		connection.Close()
		f.t.Fatalf("GraphQL connection frame=%+v", frame)
	}
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	if err := connection.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": payload}); err != nil {
		connection.Close()
		f.t.Fatal(err)
	}
	return connection
}

func readFrame(t *testing.T, connection *websocket.Conn) wsFrame {
	t.Helper()
	return readFrameBefore(t, connection, time.Now().Add(5*time.Second))
}

func readFrameBefore(t *testing.T, connection *websocket.Conn, deadline time.Time) wsFrame {
	t.Helper()
	_ = connection.SetReadDeadline(deadline)
	var frame wsFrame
	if err := connection.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func recvGraphEvent(t *testing.T, connection *websocket.Conn) graphEventEnvelope {
	t.Helper()
	frame := readFrame(t, connection)
	if frame.Type != "next" {
		t.Fatalf("GraphQL event frame=%+v", frame)
	}
	var result graphEventEnvelope
	if err := json.Unmarshal(frame.Payload, &result); err != nil {
		t.Fatalf("decode GraphQL event: %v payload=%s", err, frame.Payload)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("GraphQL event errors=%+v", result.Errors)
	}
	return result
}

func recvCaller(t *testing.T, stream golem.EventStream[social.PostEvent]) social.PostEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertNoAdditionalCallerEvent(t *testing.T, stream golem.EventStream[social.PostEvent]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if value, err := stream.Recv(ctx); err == nil {
		t.Fatalf("unexpected trailing caller event %s", value.ID())
	} else if eventCode(err) != events.CodeSubscriptionCancelled {
		t.Fatalf("caller quiet-period error=%v code=%q", err, eventCode(err))
	}
}

func (f *fixture) assertNoAdditionalGraphEvent(connection *websocket.Conn) {
	f.t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var frame wsFrame
	err := connection.ReadJSON(&frame)
	if err == nil {
		f.t.Fatalf("unexpected trailing GraphQL event frame=%+v", frame)
	}
}

func openDatabase(t *testing.T, ctx context.Context) *provider.Database {
	t.Helper()
	var (
		database *provider.Database
		err      error
	)
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	default:
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func providerFromEnvironment() golem.Provider {
	if os.Getenv("P8_ORACLE_PROVIDER") == "postgresql" {
		return golem.PostgreSQL
	}
	return golem.SQLite
}

func mustUUID(t testing.TB, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustJSONBytes(t testing.TB, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func eventCode(err error) events.ErrorCode {
	code, _ := events.CodeOf(err)
	return code
}

func isCancellation(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return true
	}
	code := eventCode(err)
	return code == events.CodeSubscriptionCancelled || code == events.CodeEventSourceClosed
}

func uuidText[T ~[16]byte](value T) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
