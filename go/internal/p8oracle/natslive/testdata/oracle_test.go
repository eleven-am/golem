package oracle_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	eventnats "github.com/eleven-am/golem/go/events/nats"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	natsclient "github.com/nats-io/nats.go"
)

const livePayloadLimit = 2 << 20

func TestP8ExternalNATSOracleScenario(t *testing.T) {
	requireNATSEnvironment(t)
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "outage-reconnect-readiness":
		newNATSFixture(t).outageReconnectReadiness()
	case "duplicate-identity-core-no-replay":
		newNATSFixture(t).duplicateIdentityCoreNoReplay()
	default:
		t.Fatalf("unknown live NATS oracle scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

type natsFixture struct {
	t          *testing.T
	ctx        context.Context
	database   *provider.Database
	first      *social.App[social.Principal]
	second     *social.App[social.Principal]
	firstNATS  *eventnats.Transport
	secondNATS *eventnats.Transport
	caller     *social.Caller[social.Principal]
	userID     golem.UUID
	prefix     string
	natsURL    string
	controlURL string
	stop       context.CancelFunc
	stopped    chan error
}

func newNATSFixture(t *testing.T) *natsFixture {
	t.Helper()
	ctx := context.Background()
	database, err := postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	if err != nil {
		t.Fatal(err)
	}
	result := &natsFixture{
		t: t, ctx: ctx, database: database,
		prefix:  os.Getenv("P8_ORACLE_NATS_SUBJECT_PREFIX"),
		natsURL: os.Getenv("P8_ORACLE_NATS_URL"), controlURL: os.Getenv("P8_ORACLE_NATS_CONTROL_URL"),
		userID: mustUUID(t, "a1000000-0000-0000-0000-000000000001"),
	}
	t.Cleanup(result.close)
	result.firstNATS = openNATS(t, ctx, database, result.natsURL, result.prefix, livePayloadLimit)
	assertTransportBoundary(t, result.firstNATS)
	oversized, err := eventnats.Open(ctx, database, eventnats.Config{
		URLs: []string{result.natsURL}, SubjectPrefix: result.prefix + ".oversized",
		ConnectTimeout: 2 * time.Second, FlushTimeout: 2 * time.Second,
		ReconnectWait: 25 * time.Millisecond, MaxReconnects: 400,
		MaxInboundPayloadBytes: livePayloadLimit + 1,
	})
	if oversized != nil || eventCode(err) != events.CodeEventConfig {
		if oversized != nil {
			_ = oversized.Close()
		}
		t.Fatalf("broker accepted adapter payload boundary+1 transport=%v error=%v", oversized, err)
	}
	result.secondNATS = openNATS(t, ctx, database, result.natsURL, result.prefix, livePayloadLimit)
	result.first = openApplication(t, ctx, database, result.firstNATS, result.userID)
	result.second = openApplication(t, ctx, database, result.secondNATS, result.userID)
	if _, err := result.first.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(result.userID),
		social.Users.Handle.Create("order7-nats-user"),
		social.Users.Email.Create("order7-nats-user@p8.test"),
	)); err != nil {
		t.Fatal(err)
	}
	result.caller, err = result.second.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: result.userID})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireNATSEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"P8_ORACLE_DSN", "P8_ORACLE_NATS_URL", "P8_ORACLE_NATS_CONTROL_URL", "P8_ORACLE_NATS_SUBJECT_PREFIX",
	} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			t.Fatalf("required live NATS environment %s is absent", name)
		}
	}
	if os.Getenv("P8_ORACLE_PROVIDER") != "postgresql" {
		t.Fatalf("live NATS oracle requires PostgreSQL, got %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
}

func openNATS(t *testing.T, ctx context.Context, database *provider.Database, url, prefix string, payload int) *eventnats.Transport {
	t.Helper()
	transport, err := eventnats.Open(ctx, database, eventnats.Config{
		URLs: []string{url}, SubjectPrefix: prefix,
		ConnectTimeout: 2 * time.Second, FlushTimeout: 5 * time.Second,
		ReconnectWait: 25 * time.Millisecond, MaxReconnects: 400,
		MaxInboundPayloadBytes: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func assertTransportBoundary(t *testing.T, transport *eventnats.Transport) {
	t.Helper()
	capabilities := transport.TransportCapabilities()
	if capabilities.Identity() != "golem.nats.v1" || capabilities.Scope() != events.TransportScopeCrossProcess || capabilities.Durable() {
		t.Fatalf("live NATS capabilities=(%q,%q,%t)", capabilities.Identity(), capabilities.Scope(), capabilities.Durable())
	}
	if !transport.TransportAvailable() || transport.MaxEncodedEventBytes() != livePayloadLimit {
		t.Fatalf("live NATS available=%t payload=%d", transport.TransportAvailable(), transport.MaxEncodedEventBytes())
	}
}

func openApplication(t *testing.T, ctx context.Context, database *provider.Database, transport events.EventTransport, userID golem.UUID) *social.App[social.Principal] {
	t.Helper()
	application, err := social.Open(ctx, social.Config[social.Principal]{
		Database: database, EventTransport: transport,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			if !principal.Development || principal.DevUserID != userID {
				return social.Actor{}, nil
			}
			return social.Actor{UserID: userID, Authenticated: true}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "order7-nats-oracle" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func (fixture *natsFixture) outageReconnectReadiness() {
	id := mustUUID(fixture.t, "a2000000-0000-0000-0000-000000000001")
	stream, err := fixture.caller.Posts.Events(fixture.ctx, golem.EventWhere(social.Posts.ID.Eq(id)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	defer stream.Close()
	fixture.startPublisher()
	fixture.awaitAvailability(true)
	fixture.control("cut")
	fixture.awaitAvailability(false)
	if !fixture.first.EventCapabilities().PublisherRunning() {
		fixture.t.Fatal("publisher stopped during live NATS outage")
	}
	fixture.createPost(id, "outage-reconnect")
	fixture.control("restore")
	fixture.awaitAvailability(true)
	event := recvPost(fixture.t, stream, 10*time.Second)
	if event.ID() != id || event.Metadata().Action() != golem.EventCreated {
		fixture.t.Fatalf("reconnected event id=%s action=%s", event.ID(), event.Metadata().Action())
	}
	if !fixture.first.EventCapabilities().PublisherRunning() {
		fixture.t.Fatal("publisher did not remain running after live NATS reconnect")
	}
}

func (fixture *natsFixture) duplicateIdentityCoreNoReplay() {
	metadata := postEventMetadata(fixture.t)
	subject := fmt.Sprintf("%s.g1.%x.%x", fixture.prefix, metadata.EventSchemaDigest(), metadata.ModelID())
	generationText := fmt.Sprintf("%x", social.GolemGeneratedEventModels().GenerationDigest())
	if subject != fixture.prefix+".g1."+fmt.Sprintf("%x", metadata.EventSchemaDigest())+"."+fmt.Sprintf("%x", metadata.ModelID()) || strings.Contains(subject, generationText) {
		fixture.t.Fatalf("live NATS subject=%q", subject)
	}
	raw, err := natsclient.Connect(fixture.natsURL, natsclient.Timeout(2*time.Second), natsclient.ReconnectWait(25*time.Millisecond), natsclient.MaxReconnects(400))
	if err != nil {
		fixture.t.Fatal(err)
	}
	defer raw.Close()
	primary, err := raw.SubscribeSync(subject)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if err := raw.FlushTimeout(2 * time.Second); err != nil {
		fixture.t.Fatal(err)
	}
	firstID := mustUUID(fixture.t, "a2000000-0000-0000-0000-000000000011")
	stream, err := fixture.caller.Posts.Events(fixture.ctx, golem.EventWhere(social.Posts.ID.Eq(firstID)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	defer stream.Close()
	fixture.startPublisher()
	fixture.createPost(firstID, "duplicate-identity")
	firstRaw := nextRaw(fixture.t, primary, 10*time.Second)
	firstEvent := recvPost(fixture.t, stream, 10*time.Second)
	encoded := append([]byte(nil), firstRaw.Data...)
	if firstEvent.ID() != firstID || firstEvent.Metadata().GenerationDigest() != social.GolemGeneratedEventModels().GenerationDigest() {
		fixture.t.Fatalf("first live event identity=%s generation=%x", firstEvent.ID(), firstEvent.Metadata().GenerationDigest())
	}
	if err := raw.Publish(subject, encoded); err != nil {
		fixture.t.Fatal(err)
	}
	if err := raw.FlushTimeout(2 * time.Second); err != nil {
		fixture.t.Fatal(err)
	}
	duplicateRaw := nextRaw(fixture.t, primary, 2*time.Second)
	duplicateEvent := recvPost(fixture.t, stream, 2*time.Second)
	if !bytes.Equal(encoded, duplicateRaw.Data) || duplicateEvent.ID() != firstEvent.ID() || duplicateEvent.Metadata().EventID() != firstEvent.Metadata().EventID() {
		fixture.t.Fatalf("duplicate bytes/event identity changed bytes=%t first=%x duplicate=%x", bytes.Equal(encoded, duplicateRaw.Data), firstEvent.Metadata().EventID(), duplicateEvent.Metadata().EventID())
	}

	lateRaw, err := raw.SubscribeSync(subject)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if err := raw.FlushTimeout(2 * time.Second); err != nil {
		fixture.t.Fatal(err)
	}
	lateStream, err := fixture.caller.Posts.Events(fixture.ctx)
	if err != nil {
		fixture.t.Fatal(err)
	}
	defer lateStream.Close()
	if _, err := lateRaw.NextMsg(250 * time.Millisecond); !errors.Is(err, natsclient.ErrTimeout) {
		fixture.t.Fatalf("Core NATS late subscriber replayed history: %v", err)
	}
	quiet, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if event, err := lateStream.Recv(quiet); err == nil {
		fixture.t.Fatalf("generated late subscriber replayed event %s", event.ID())
	} else if eventCode(err) != events.CodeSubscriptionCancelled {
		fixture.t.Fatalf("generated late subscriber failed instead of remaining replay-free: %v", err)
	}
	secondID := mustUUID(fixture.t, "a2000000-0000-0000-0000-000000000012")
	fixture.createPost(secondID, "core-new-only")
	secondRaw := nextRaw(fixture.t, lateRaw, 10*time.Second)
	secondEvent := recvPost(fixture.t, lateStream, 10*time.Second)
	if secondEvent.ID() != secondID || secondEvent.Metadata().EventID() == firstEvent.Metadata().EventID() || bytes.Equal(secondRaw.Data, encoded) {
		fixture.t.Fatalf("Core NATS new event id=%s eventID=%x", secondEvent.ID(), secondEvent.Metadata().EventID())
	}
}

func postEventMetadata(t *testing.T) golem.EventModelMetadata {
	t.Helper()
	for _, metadata := range social.GolemGeneratedEventModels().Models() {
		if metadata.PayloadTypeName() == "PostEvent" {
			return metadata
		}
	}
	t.Fatal("generated Post event metadata is absent")
	return golem.EventModelMetadata{}
}

func (fixture *natsFixture) startPublisher() {
	fixture.t.Helper()
	if fixture.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	fixture.stop = cancel
	fixture.stopped = make(chan error, 1)
	go func() { fixture.stopped <- fixture.first.RunEventPublisher(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !fixture.first.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !fixture.first.EventCapabilities().PublisherRunning() {
		fixture.t.Fatal("live NATS publisher did not report running")
	}
}

func (fixture *natsFixture) awaitAvailability(want bool) {
	fixture.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		first := fixture.first.EventCapabilities()
		second := fixture.second.EventCapabilities()
		if first.TransportAvailable() == want && second.TransportAvailable() == want {
			if first.Transport().Identity() != "golem.nats.v1" || second.Transport().Identity() != "golem.nats.v1" {
				fixture.t.Fatal("application lost live NATS capability identity")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fixture.t.Fatalf("live NATS availability did not become %t", want)
}

func (fixture *natsFixture) control(action string) {
	fixture.t.Helper()
	request, err := http.NewRequestWithContext(fixture.ctx, http.MethodPost, fixture.controlURL+"/"+action, nil)
	if err != nil {
		fixture.t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		fixture.t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusNoContent {
		fixture.t.Fatalf("live NATS control %s status=%d", action, response.StatusCode)
	}
}

func (fixture *natsFixture) createPost(id golem.UUID, title string) {
	fixture.t.Helper()
	date, err := golem.ParseDate("2026-08-12")
	if err != nil {
		fixture.t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56")
	if err != nil {
		fixture.t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"source":"order7-nats"}`))
	if err != nil {
		fixture.t.Fatal(err)
	}
	if _, err := fixture.first.System().Posts.Create(fixture.ctx, social.Posts.Create(
		social.Posts.ID.Create(id), social.Posts.AuthorID.Create(fixture.userID), social.Posts.Title.Create(title), social.Posts.Body.Create("live Core NATS"),
		social.Posts.Published.Create(true), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Visibility.Create(social.VisibilityPublic), social.Posts.Topics.Create(golem.List[string]{"order7", "nats"}),
	)); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *natsFixture) close() {
	if fixture.stop != nil {
		fixture.stop()
		select {
		case err := <-fixture.stopped:
			if err != nil {
				fixture.t.Errorf("live NATS publisher shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			fixture.t.Error("live NATS publisher did not stop")
		}
		fixture.stop = nil
	}
	if fixture.secondNATS != nil {
		if err := fixture.secondNATS.Close(); err != nil {
			fixture.t.Error(err)
		}
		fixture.secondNATS = nil
	}
	if fixture.firstNATS != nil {
		if err := fixture.firstNATS.Close(); err != nil {
			fixture.t.Error(err)
		}
		fixture.firstNATS = nil
	}
	if fixture.database != nil {
		if err := fixture.database.Close(); err != nil {
			fixture.t.Error(err)
		}
		fixture.database = nil
	}
}

func recvPost(t *testing.T, stream golem.EventStream[social.PostEvent], timeout time.Duration) social.PostEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	event, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func nextRaw(t *testing.T, subscription *natsclient.Subscription, timeout time.Duration) *natsclient.Msg {
	t.Helper()
	message, err := subscription.NextMsg(timeout)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func eventCode(err error) events.ErrorCode {
	code, _ := events.CodeOf(err)
	return code
}
