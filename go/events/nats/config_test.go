package nats

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	natsclient "github.com/nats-io/nats.go"
)

func TestOrder7OpenRejectsAbsentProviderBeforeDial(t *testing.T) {
	transport, err := Open(context.Background(), (*provider.Database)(nil), Config{URLs: []string{"nats://secret:credential@127.0.0.1:1"}, SubjectPrefix: "deployment"})
	if transport != nil {
		t.Fatal("nil provider returned a transport")
	}
	if code, ok := events.CodeOf(err); !ok || code != events.CodeEventConfig {
		t.Fatalf("error=%v code=%q", err, code)
	}
	if got := err.Error(); !strings.Contains(got, "database must not be nil") || strings.Contains(got, "secret") || strings.Contains(got, "credential") {
		t.Fatalf("error is not actionable and sealed: %q", got)
	}
}

func TestOrder7OpenRejectsNilContextBeforeConfiguration(t *testing.T) {
	transport, err := Open(nil, nil, Config{URLs: []string{"nats://secret:credential@127.0.0.1:1"}, SubjectPrefix: "deployment"})
	if transport != nil || eventCode(err) != events.CodeEventConfig {
		t.Fatalf("transport=%v error=%v", transport, err)
	}
	if got := err.Error(); !strings.Contains(got, "context must not be nil") || strings.Contains(got, "secret") || strings.Contains(got, "credential") {
		t.Fatalf("error is not actionable and sealed: %q", got)
	}
}

func TestOrder7OpenRejectsSQLiteBeforeDial(t *testing.T) {
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "nats-proof.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()
	transport, err := Open(context.Background(), database, Config{URLs: []string{"nats://secret:credential@" + listener.Addr().String()}, SubjectPrefix: "deployment", ConnectTimeout: 30 * time.Second})
	if transport != nil || eventCode(err) != events.CodeEventConfig {
		t.Fatalf("transport=%v error=%v", transport, err)
	}
	if got := err.Error(); !strings.Contains(got, "database provider must be PostgreSQL") || strings.Contains(got, "secret") || strings.Contains(got, "credential") {
		t.Fatalf("error is not actionable and sealed: %q", got)
	}
	select {
	case <-accepted:
		t.Fatal("SQLite rejection dialed NATS")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOrder7ConfigIsClosedBoundedAndRedacted(t *testing.T) {
	for name, config := range map[string]Config{
		"absent URLs":       {SubjectPrefix: "deployment"},
		"absent prefix":     {URLs: []string{"nats://broker"}},
		"injected URL list": {URLs: []string{"nats://one,nats://two"}},
		"unknown scheme":    {URLs: []string{"http://broker"}},
		"wildcard prefix":   {URLs: []string{"nats://broker"}, SubjectPrefix: "golem.>"},
		"negative queue":    {URLs: []string{"nats://broker"}, StreamBuffer: -1},
		"oversized payload": {URLs: []string{"nats://broker"}, MaxInboundPayloadBytes: maximumInboundPayload + 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeConfig(config)
			if eventCode(err) != events.CodeEventConfig {
				t.Fatalf("error=%v", err)
			}
			for _, secret := range []string{"nats://one,nats://two", "http://broker", "golem.>"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q echoed a configured value", err)
				}
			}
		})
	}
}

func TestOrder7InitialConnectBudgetHonorsContextAndWholeURLSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if got, ok := boundedConnectTimeout(ctx, 2*time.Minute, 3); !ok || got > 30*time.Second || got < 29*time.Second {
		t.Fatalf("per-server context budget=%s ok=%t", got, ok)
	}
	if got, ok := boundedConnectTimeout(context.Background(), 12*time.Second, 3); !ok || got != 4*time.Second {
		t.Fatalf("per-server configured budget=%s ok=%t", got, ok)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, ok := boundedConnectTimeout(cancelled, time.Second, 1); ok {
		t.Fatal("cancelled context produced a connect budget")
	}
}

func TestOrder7CancellationDuringProtocolHandshakeReturnsPromptlyAndClosesConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	normalized, err := normalizeConfig(Config{URLs: []string{"nats://" + listener.Addr().String()}, SubjectPrefix: "deployment", ConnectTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := &Transport{config: normalized, streams: make(map[*stream]struct{})}
	type result struct {
		client *natsclient.Conn
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client, connectErr := connectCore(ctx, normalized, transport, normalized.ConnectTimeout)
		done <- result{client: client, err: connectErr}
	}()
	var serverConnection net.Conn
	select {
	case serverConnection = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("initial dial did not reach test server")
	}
	defer serverConnection.Close()
	cancel()
	select {
	case connected := <-done:
		if connected.client != nil || connected.err == nil || transport.client != nil {
			t.Fatalf("client=%v error=%v installed=%v", connected.client, connected.err, transport.client)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt NATS handshake")
	}
	_ = serverConnection.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := serverConnection.Read(buffer); err == nil {
		t.Fatal("cancelled initial NATS connection remained open")
	}
}

func TestOrder7CapabilitiesAreCoreNATSFanout(t *testing.T) {
	transport := &Transport{}
	capabilities := transport.TransportCapabilities()
	if capabilities.Identity() != "golem.nats.v1" || capabilities.Scope() != events.TransportScopeCrossProcess || capabilities.Durable() {
		t.Fatalf("capabilities=(%q,%q,%t)", capabilities.Identity(), capabilities.Scope(), capabilities.Durable())
	}
}
