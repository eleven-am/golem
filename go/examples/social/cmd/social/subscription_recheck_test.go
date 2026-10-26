package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/graphql"
	"github.com/eleven-am/golem/go/provider"
	"github.com/gorilla/websocket"
)

const recheckInterval = 50 * time.Millisecond

type recheckAccess struct {
	mu     sync.Mutex
	banned map[golem.UUID]bool
	broken map[golem.UUID]bool
}

func (access *recheckAccess) ban(user golem.UUID) {
	access.mu.Lock()
	defer access.mu.Unlock()
	access.banned[user] = true
}

func (access *recheckAccess) breakPrincipal(user golem.UUID) {
	access.mu.Lock()
	defer access.mu.Unlock()
	access.broken[user] = true
}

func (access *recheckAccess) resolve(_ context.Context, principal social.Principal) (social.Actor, error) {
	access.mu.Lock()
	defer access.mu.Unlock()
	if access.broken[principal.DevUserID] {
		return social.Actor{}, errors.New("principal revoked")
	}
	if !principal.Development || access.banned[principal.DevUserID] {
		return social.Actor{}, nil
	}
	return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
}

type recheckWorld struct {
	application *social.App[social.Principal]
	access      *recheckAccess
	host        *httptest.Server
}

func openRecheckWorld(t *testing.T, ctx context.Context, database *provider.Database, limits events.Limits) *recheckWorld {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	access := &recheckAccess{banned: map[golem.UUID]bool{}, broken: map[golem.UUID]bool{}}
	application, err := social.Open(ctx, social.Config[social.Principal]{
		Database: database, EventTransport: transport, EventLimits: limits,
		ResolvePrincipal:    access.resolve,
		SnapshotPrincipal:   func(principal social.Principal) (social.Principal, error) { return principal, nil },
		SnapshotActor:       func(actor social.Actor) (social.Actor, error) { return actor, nil },
		AuditPrincipal:      principalAuditID,
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
		SubscriptionPrincipalKey: func(principal social.Principal) (string, bool) {
			return principal.DevUserID.String(), principal.Development
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publisherContext, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	t.Cleanup(func() {
		stopPublisher()
		<-publisherDone
	})
	awaitPublisher(t, application)
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError:  func(context.Context, error) {},
		SubscriptionRecheck: graphql.SubscriptionRecheck[social.Principal]{
			Interval: recheckInterval,
			Check: func(ctx context.Context, _ social.Principal, operation graphql.Operation) error {
				post, err := golem.ParseUUID(fmt.Sprint(operation.Variables["post"]))
				if err != nil {
					return nil
				}
				return postStillShared(ctx, application, post)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graph.Shutdown(context.Background()) })
	host := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, err := golem.ParseUUID(request.Header.Get("X-Test-User"))
		if err != nil {
			http.Error(writer, "unknown test user", http.StatusUnauthorized)
			return
		}
		principal := social.Principal{Development: true, DevUserID: user}
		graph.Handler().ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	}))
	t.Cleanup(host.Close)
	return &recheckWorld{application: application, access: access, host: host}
}

func postStillShared(ctx context.Context, application *social.App[social.Principal], post golem.UUID) error {
	row, err := application.System().Posts.FindUnique(ctx, social.Posts.ByID.Value(post), social.Posts.Select(social.Posts.Published))
	if err != nil {
		return err
	}
	if published, _ := golem.Value(row, social.Posts.Published).Get(); !published {
		return errors.New("share revoked")
	}
	return nil
}

func (world *recheckWorld) user(t *testing.T, ctx context.Context, id, handle string) (golem.UUID, *social.Caller[social.Principal]) {
	t.Helper()
	userID := mustUUID(t, id)
	if _, err := world.application.System().Users.Create(ctx, social.Users.Create(social.Users.ID.Create(userID), social.Users.Handle.Create(handle), social.Users.Email.Create(handle+"@example.test"))); err != nil {
		t.Fatal(err)
	}
	caller, err := world.application.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	return userID, caller
}

func (world *recheckWorld) dial(t *testing.T, user golem.UUID) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(world.host.URL, "http"), http.Header{"X-Test-User": {user.String()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	readWSFrame(t, connection, "connection_ack")
	return connection
}

func subscribeWS(t *testing.T, connection *websocket.Conn, id, query string, variables map[string]any) {
	t.Helper()
	if err := connection.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": map[string]any{"query": query, "variables": variables}}); err != nil {
		t.Fatal(err)
	}
}

func awaitWSOperationError(t *testing.T, connection *websocket.Conn, id string, want events.ErrorCode) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		var frame wsFrame
		if err := connection.ReadJSON(&frame); err != nil {
			t.Fatalf("awaiting %s on %s: %v", want, id, err)
		}
		if frame.ID != id || frame.Type == "next" {
			continue
		}
		var failures []map[string]any
		if err := json.Unmarshal(frame.Payload, &failures); err != nil || frame.Type != "error" || len(failures) != 1 {
			t.Fatalf("operation %s frame=%s payload=%s", id, frame.Type, frame.Payload)
		}
		extensions, _ := failures[0]["extensions"].(map[string]any)
		if extensions["code"] != string(want) {
			t.Fatalf("operation %s code=%v want %s", id, extensions["code"], want)
		}
		return
	}
	t.Fatalf("operation %s never ended with %s", id, want)
}

func awaitWSNext(t *testing.T, connection *websocket.Conn, id string, contains string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		var frame wsFrame
		if err := connection.ReadJSON(&frame); err != nil {
			t.Fatalf("awaiting next on %s: %v", id, err)
		}
		if frame.ID != id {
			continue
		}
		if frame.Type != "next" {
			t.Fatalf("operation %s ended: %s %s", id, frame.Type, frame.Payload)
		}
		if bytes.Contains(frame.Payload, []byte(contains)) {
			return
		}
	}
	t.Fatalf("operation %s never delivered %s", id, contains)
}

func assertWSPing(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	if err := connection.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		var frame wsFrame
		if err := connection.ReadJSON(&frame); err != nil {
			t.Fatalf("connection did not survive: %v", err)
		}
		if frame.Type == "pong" {
			return
		}
	}
	t.Fatal("connection never answered ping")
}

type recheckSSE struct {
	response *http.Response
	reader   *bufio.Reader
}

func (world *recheckWorld) openSSE(t *testing.T, ctx context.Context, user golem.UUID, query string, variables map[string]any) *recheckSSE {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, world.host.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-Test-User", user.String())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return &recheckSSE{response: response, reader: bufio.NewReader(response.Body)}
}

func (stream *recheckSSE) frame(t *testing.T) (string, string) {
	t.Helper()
	var event string
	var data []string
	for {
		line, err := stream.reader.ReadString('\n')
		if err != nil {
			t.Fatalf("SSE stream ended: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "" && event != "":
			return event, strings.Join(data, "\n")
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
}

func (stream *recheckSSE) awaitTerminal(t *testing.T, want events.ErrorCode) {
	t.Helper()
	for {
		event, data := stream.frame(t)
		if event != "next" {
			t.Fatalf("SSE frame %s before the terminal error", event)
		}
		if !strings.Contains(data, `"errors"`) {
			continue
		}
		if !strings.Contains(data, `"code":"`+string(want)+`"`) {
			t.Fatalf("SSE terminal frame=%s want %s", data, want)
		}
		break
	}
	if event, _ := stream.frame(t); event != "complete" {
		t.Fatalf("SSE frame after the terminal error=%s", event)
	}
	if _, err := stream.reader.ReadString('\n'); err != io.EOF {
		t.Fatalf("SSE stream continued after complete: %v", err)
	}
}

func awaitStreamCode[E any](t *testing.T, ctx context.Context, stream golem.EventStream[E], want events.ErrorCode) {
	t.Helper()
	receive, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, err := stream.Recv(receive)
		if err == nil {
			continue
		}
		if code, ok := events.CodeOf(err); !ok || code != want {
			t.Fatalf("stream ended with %v, want %s", err, want)
		}
		return
	}
}

type refusingReadHook struct {
	refuse atomic.Bool
	calls  atomic.Int64
}

func (hook *refusingReadHook) ObservePostReadHook(_ context.Context, phase social.PostReadHookPhase, _ []golem.Row[social.Post]) error {
	if phase != social.PostHookBeforeFindMany {
		return nil
	}
	hook.calls.Add(1)
	if hook.refuse.Load() {
		return errors.New("read refused")
	}
	return nil
}

const commentSubscription = `subscription Shared($post: UUID!) { commentEvents(where: {postID: {equals: $post}}) { type id } }`
const postSubscription = `subscription Posts { postEvents { type id } }`

func TestSubscriptionRecheckEndsARevokedShareOnEveryTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	world := openRecheckWorld(t, ctx, openDeleteEventsSQLite(t), events.Limits{})
	_, alice := world.user(t, ctx, "61000000-0000-0000-0000-000000000001", "alice")
	bobID, bob := world.user(t, ctx, "61000000-0000-0000-0000-000000000002", "bob")
	post := mustUUID(t, "62000000-0000-0000-0000-000000000001")
	if _, err := alice.Posts.Create(ctx, referencePostInput(t, post, true)); err != nil {
		t.Fatal(err)
	}
	goStream, err := bob.Comments.Events(ctx,
		golem.EventWhere(social.Comments.PostID.Eq(post)),
		golem.EventRecheck[social.Comment](recheckInterval, func(ctx context.Context) error { return postStillShared(ctx, world.application, post) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer goStream.Close()
	connection := world.dial(t, bobID)
	variables := map[string]any{"post": post.String()}
	subscribeWS(t, connection, "shared", commentSubscription, variables)
	subscribeWS(t, connection, "posts", postSubscription, nil)
	sse := world.openSSE(t, ctx, bobID, commentSubscription, variables)
	if sse.response.StatusCode != http.StatusOK {
		t.Fatalf("SSE status=%d", sse.response.StatusCode)
	}
	time.Sleep(3 * recheckInterval)
	if _, err := alice.Posts.Update(ctx, social.Posts.ByID.Value(post), social.Posts.Update(social.Posts.Published.Set(false))); err != nil {
		t.Fatal(err)
	}
	revoked := time.Now()
	awaitStreamCode(t, ctx, goStream, events.CodeSubscriptionForbidden)
	awaitWSOperationError(t, connection, "shared", events.CodeSubscriptionForbidden)
	sse.awaitTerminal(t, events.CodeSubscriptionForbidden)
	if elapsed := time.Since(revoked); elapsed > 2*time.Second {
		t.Fatalf("revocation took %s to end every stream", elapsed)
	}
	assertWSPing(t, connection)
	other := mustUUID(t, "62000000-0000-0000-0000-000000000002")
	if _, err := alice.Posts.Create(ctx, referencePostInput(t, other, true)); err != nil {
		t.Fatal(err)
	}
	awaitWSNext(t, connection, "posts", other.String())
}

func TestSubscriptionRevalidationEndsConstantFalseAndRevokedPrincipals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	world := openRecheckWorld(t, ctx, openDeleteEventsSQLite(t), events.Limits{RevalidateEvery: recheckInterval})
	aliceID, alice := world.user(t, ctx, "63000000-0000-0000-0000-000000000001", "alice")
	carolID, carol := world.user(t, ctx, "63000000-0000-0000-0000-000000000003", "carol")
	_, gina := world.user(t, ctx, "63000000-0000-0000-0000-000000000007", "gina")
	gate := &refusingReadHook{}
	hooked, err := gina.Posts.Events(social.WithPostReadHookObserver(ctx, gate))
	if err != nil {
		t.Fatal(err)
	}
	defer hooked.Close()
	sessions, err := alice.Sessions.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	posts, err := carol.Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer posts.Close()
	connection := world.dial(t, carolID)
	subscribeWS(t, connection, "posts", postSubscription, nil)
	sse := world.openSSE(t, ctx, carolID, postSubscription, nil)
	if sse.response.StatusCode != http.StatusOK {
		t.Fatalf("SSE status=%d", sse.response.StatusCode)
	}
	time.Sleep(3 * recheckInterval)
	quiet, stop := context.WithTimeout(ctx, 2*recheckInterval)
	if _, err := sessions.Recv(quiet); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an authorized stream ended during revalidation: %v", err)
	}
	stop()
	world.access.ban(aliceID)
	awaitStreamCode(t, ctx, sessions, events.CodeSubscriptionForbidden)
	if gate.calls.Load() == 0 {
		t.Fatal("revalidation never ran the read-before hook")
	}
	gate.refuse.Store(true)
	awaitStreamCode(t, ctx, hooked, events.CodeSubscriptionForbidden)
	world.access.breakPrincipal(carolID)
	awaitStreamCode(t, ctx, posts, events.CodeSubscriptionRevalidation)
	awaitWSOperationError(t, connection, "posts", events.CodeSubscriptionRevalidation)
	sse.awaitTerminal(t, events.CodeSubscriptionRevalidation)
	assertWSPing(t, connection)
}

func TestSubscriptionPrincipalCapSpansGoAndGraphQL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	world := openRecheckWorld(t, ctx, openDeleteEventsSQLite(t), events.Limits{MaxSubscriptionsPerPrincipal: 2})
	daveID, dave := world.user(t, ctx, "64000000-0000-0000-0000-000000000004", "dave")
	_, erin := world.user(t, ctx, "64000000-0000-0000-0000-000000000005", "erin")
	first, err := dave.Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connection := world.dial(t, daveID)
	subscribeWS(t, connection, "admitted", postSubscription, nil)
	created := mustUUID(t, "65000000-0000-0000-0000-000000000001")
	if _, err := dave.Posts.Create(ctx, referencePostInput(t, created, true)); err != nil {
		t.Fatal(err)
	}
	awaitWSNext(t, connection, "admitted", created.String())
	subscribeWS(t, connection, "refused", postSubscription, nil)
	awaitWSOperationError(t, connection, "refused", events.CodeSubscriptionLimit)
	assertWSPing(t, connection)
	if _, err := dave.Comments.Events(ctx); err == nil {
		t.Fatal("a third stream for one principal was admitted")
	} else if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionLimit {
		t.Fatalf("over-cap Go stream error=%v", err)
	}
	sse := world.openSSE(t, ctx, daveID, postSubscription, nil)
	if sse.response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-cap SSE status=%d", sse.response.StatusCode)
	}
	other, err := erin.Posts.Events(ctx)
	if err != nil {
		t.Fatalf("another principal was refused by dave's cap: %v", err)
	}
	defer other.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	subscribeWS(t, connection, "freed", postSubscription, nil)
	next := mustUUID(t, "65000000-0000-0000-0000-000000000002")
	time.Sleep(100 * time.Millisecond)
	if _, err := dave.Posts.Create(ctx, referencePostInput(t, next, true)); err != nil {
		t.Fatal(err)
	}
	awaitWSNext(t, connection, "freed", next.String())
}

func TestSubscriptionRecheckLeavesNoGoroutinesBehind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	world := openRecheckWorld(t, ctx, openDeleteEventsSQLite(t), events.Limits{RevalidateEvery: 5 * time.Millisecond, MaxSubscriptionsPerPrincipal: 64})
	_, frank := world.user(t, ctx, "66000000-0000-0000-0000-000000000006", "frank")
	anchor, err := frank.Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()
	streams := make([]golem.EventStream[social.PostEvent], 0, 16)
	for range 16 {
		stream, err := frank.Posts.Events(ctx, golem.EventRecheck[social.Post](5*time.Millisecond, func(context.Context) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
	}
	time.Sleep(50 * time.Millisecond)
	if running := runtime.NumGoroutine(); running < baseline+32 {
		t.Fatalf("goroutines=%d; want both rechecks for 16 streams above baseline %d", running, baseline)
	}
	for _, stream := range streams {
		_ = stream.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if running := runtime.NumGoroutine(); running > baseline {
		t.Fatalf("goroutines=%d after closing every rechecked stream; baseline=%d", running, baseline)
	}
}
