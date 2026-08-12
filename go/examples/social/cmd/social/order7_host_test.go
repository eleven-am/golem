package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestOrder7SocialHostTransportConfigurationIsProviderClosed(t *testing.T) {
	read := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	defaults, err := eventTransportConfiguration(golem.PostgreSQL, read(nil))
	if err != nil || defaults.kind != hostEventTransportMemory || !reflect.DeepEqual(defaults.nats, hostNATSConfig{}) {
		t.Fatalf("default event transport=%+v error=%v", defaults, err)
	}
	configuration, err := eventTransportConfiguration(golem.PostgreSQL, read(map[string]string{
		"GOLEM_EVENT_TRANSPORT":     "nats",
		"GOLEM_NATS_URLS":           " nats://one:4222 , nats://two:4222 ",
		"GOLEM_NATS_SUBJECT_PREFIX": "database_7f3a.events",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.kind != hostEventTransportNATS || !reflect.DeepEqual(configuration.nats.URLs, []string{"nats://one:4222", "nats://two:4222"}) || configuration.nats.SubjectPrefix != "database_7f3a.events" || configuration.nats.Observer == nil {
		t.Fatalf("PostgreSQL NATS configuration=%+v", configuration)
	}

	for name, test := range map[string]struct {
		provider golem.Provider
		values   map[string]string
	}{
		"SQLite NATS":                {provider: golem.SQLite, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "nats", "GOLEM_NATS_URLS": "nats://broker", "GOLEM_NATS_SUBJECT_PREFIX": "database.events"}},
		"ignored NATS memory config": {provider: golem.PostgreSQL, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "memory", "GOLEM_NATS_URLS": "nats://broker"}},
		"missing NATS URL":           {provider: golem.PostgreSQL, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "nats", "GOLEM_NATS_SUBJECT_PREFIX": "database.events"}},
		"missing NATS prefix":        {provider: golem.PostgreSQL, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "nats", "GOLEM_NATS_URLS": "nats://broker"}},
		"empty NATS URL member":      {provider: golem.PostgreSQL, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "nats", "GOLEM_NATS_URLS": "nats://one,,nats://two", "GOLEM_NATS_SUBJECT_PREFIX": "database.events"}},
		"unknown transport":          {provider: golem.PostgreSQL, values: map[string]string{"GOLEM_EVENT_TRANSPORT": "jetstream"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := eventTransportConfiguration(test.provider, read(test.values)); err == nil {
				t.Fatal("invalid host transport configuration was accepted")
			}
		})
	}
}

func TestOrder7SocialHostRejectsSQLiteNATSBeforeOpeningTransport(t *testing.T) {
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "host.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	called := false
	factories := hostEventTransportFactories{
		memory: events.NewMemoryTransport,
		nats: func(context.Context, *provider.Database, hostNATSConfig) (closeableEventTransport, error) {
			called = true
			return nil, errors.New("must not open")
		},
	}
	getenv := func(name string) string {
		return map[string]string{
			"GOLEM_EVENT_TRANSPORT":     "nats",
			"GOLEM_NATS_URLS":           "nats://secret:credential@127.0.0.1:1",
			"GOLEM_NATS_SUBJECT_PREFIX": "database.events",
		}[name]
	}
	if _, err := openEventTransportWith(context.Background(), database, getenv, factories); err == nil {
		t.Fatal("SQLite NATS topology was accepted")
	}
	if called {
		t.Fatal("SQLite topology rejection reached the NATS opener")
	}
}

func TestOrder7SocialHostPostgreSQLNATSOpensOnceThroughVerifiedDatabaseLive(t *testing.T) {
	dataSourceName := os.Getenv("GOLEM_P8_SOCIAL_POSTGRES_DSN")
	if dataSourceName == "" {
		t.Skip("GOLEM_P8_SOCIAL_POSTGRES_DSN is not configured")
	}
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	opened := 0
	owned := &hostCloseableTransport{EventTransport: mustMemoryTransport(t)}
	factories := hostEventTransportFactories{
		memory: events.NewMemoryTransport,
		nats: func(_ context.Context, received *provider.Database, config hostNATSConfig) (closeableEventTransport, error) {
			opened++
			if received != database || !reflect.DeepEqual(config.URLs, []string{"nats://one:4222", "nats://two:4222"}) || config.SubjectPrefix != "database.events" {
				t.Fatalf("NATS open database/config=%p %+v", received, config)
			}
			return owned, nil
		},
	}
	getenv := func(name string) string {
		return map[string]string{
			"GOLEM_EVENT_TRANSPORT":     "nats",
			"GOLEM_NATS_URLS":           "nats://one:4222,nats://two:4222",
			"GOLEM_NATS_SUBJECT_PREFIX": "database.events",
		}[name]
	}
	transport, err := openEventTransportWith(context.Background(), database, getenv, factories)
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || transport.transport != owned {
		t.Fatalf("NATS open count=%d transport=%T", opened, transport.transport)
	}
	if err := transport.Close(); err != nil || owned.closes != 1 {
		t.Fatalf("transport close error=%v closes=%d", err, owned.closes)
	}
}

func TestOrder7SocialHostStartupFailuresCloseOwnedDependencies(t *testing.T) {
	for _, stage := range []string{"transport", "application", "graphql"} {
		t.Run(stage, func(t *testing.T) {
			database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), stage+".sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			closedTransport := 0
			transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { closedTransport++; return nil }}
			runner := hostRunner{
				openDatabase: func(context.Context) (*provider.Database, error) { return database, nil },
				openEventTransport: func(context.Context, *provider.Database) (*hostOwnedEventTransport, error) {
					if stage == "transport" {
						return nil, errors.New("private transport failure")
					}
					return transport, nil
				},
				openApplication: func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error) {
					if stage == "application" {
						return nil, errors.New("private application failure")
					}
					return nil, nil
				},
				openGraph: func(*social.App[social.Principal]) (hostGraph, error) {
					return nil, errors.New("private GraphQL failure")
				},
				runPublisher:  func(context.Context, *social.App[social.Principal]) error { return nil },
				ready:         func(context.Context, *provider.Database, *social.App[social.Principal]) bool { return true },
				newServer:     func(http.Handler) hostHTTPServer { return nil },
				startupGrace:  time.Second,
				shutdownGrace: time.Second,
			}
			if err := runHostWith(context.Background(), func() {}, runner); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("startup error=%v", err)
			}
			wantTransportCloses := 1
			if stage == "transport" {
				wantTransportCloses = 0
			}
			if closedTransport != wantTransportCloses {
				t.Fatalf("transport closes=%d want %d", closedTransport, wantTransportCloses)
			}
			if databaseIsOpen(database) {
				t.Fatal("startup failure left the database open")
			}
		})
	}
}

func TestOrder7SocialHostPropagatesTerminalHTTPAndPublisherFailures(t *testing.T) {
	for _, terminal := range []string{"http", "http_closed", "publisher"} {
		t.Run(terminal, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), terminal+".sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			closedTransport := 0
			serverCreated := false
			transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { closedTransport++; return nil }}
			server := newHostTestServer()
			if terminal == "http" {
				server.listenError = errors.New("private HTTP failure")
			} else if terminal == "http_closed" {
				server.listenError = http.ErrServerClosed
			}
			runner := hostRunner{
				openDatabase:       func(context.Context) (*provider.Database, error) { return database, nil },
				openEventTransport: func(context.Context, *provider.Database) (*hostOwnedEventTransport, error) { return transport, nil },
				openApplication: func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error) {
					return nil, nil
				},
				openGraph: func(*social.App[social.Principal]) (hostGraph, error) { return hostTestGraph{}, nil },
				runPublisher: func(publisherContext context.Context, _ *social.App[social.Principal]) error {
					if terminal == "publisher" {
						return errors.New("private publisher failure")
					}
					<-publisherContext.Done()
					return nil
				},
				ready: func(context.Context, *provider.Database, *social.App[social.Principal]) bool {
					return terminal != "publisher"
				},
				newServer: func(http.Handler) hostHTTPServer {
					serverCreated = true
					return server
				},
				startupGrace:  time.Second,
				shutdownGrace: time.Second,
			}
			err = runHostWith(ctx, cancel, runner)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("terminal %s error=%v", terminal, err)
			}
			want := "HTTP server stopped"
			if terminal == "publisher" {
				want = "event publisher stopped"
			}
			if !strings.Contains(err.Error(), want) || terminal == "publisher" && strings.Contains(err.Error(), "host readiness timed out") {
				t.Fatalf("terminal %s error=%v want %q", terminal, err, want)
			}
			if closedTransport != 1 || databaseIsOpen(database) {
				t.Fatalf("terminal cleanup transport=%d database still open", closedTransport)
			}
			if serverCreated != (terminal != "publisher") {
				t.Fatalf("terminal %s serverCreated=%t", terminal, serverCreated)
			}
		})
	}
}

func TestOrder7SocialHostStartsListeningOnlyAfterBoundedReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "admission.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	transportCloses := 0
	transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { transportCloses++; return nil }}
	var ready atomic.Bool
	serverCreated := make(chan struct{})
	server := newHostTestServer()
	server.listenError = errors.New("bounded terminal HTTP failure")
	runner := hostRunner{
		openDatabase:       func(context.Context) (*provider.Database, error) { return database, nil },
		openEventTransport: func(context.Context, *provider.Database) (*hostOwnedEventTransport, error) { return transport, nil },
		openApplication: func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error) {
			return nil, nil
		},
		openGraph: func(*social.App[social.Principal]) (hostGraph, error) { return hostTestGraph{}, nil },
		runPublisher: func(publisherContext context.Context, _ *social.App[social.Principal]) error {
			<-publisherContext.Done()
			return nil
		},
		ready: func(context.Context, *provider.Database, *social.App[social.Principal]) bool { return ready.Load() },
		newServer: func(http.Handler) hostHTTPServer {
			close(serverCreated)
			return server
		},
		startupGrace:  time.Second,
		shutdownGrace: time.Second,
	}
	result := make(chan error, 1)
	go func() { result <- runHostWith(ctx, cancel, runner) }()
	select {
	case <-serverCreated:
		t.Fatal("HTTP server was created before readiness")
	case <-time.After(50 * time.Millisecond):
	}
	ready.Store(true)
	select {
	case <-serverCreated:
	case <-time.After(time.Second):
		t.Fatal("HTTP server was not created after readiness")
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "HTTP server stopped") {
		t.Fatalf("terminal server error=%v", err)
	}
	if transportCloses != 1 || databaseIsOpen(database) {
		t.Fatal("readiness admission path did not close owned dependencies")
	}
}

func TestOrder7SocialHostPropagatesPublisherExitAfterReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "post-ready-publisher.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	transportCloses := 0
	transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { transportCloses++; return nil }}
	server := newHostTestServer()
	runner := hostRunner{
		openDatabase:       func(context.Context) (*provider.Database, error) { return database, nil },
		openEventTransport: func(context.Context, *provider.Database) (*hostOwnedEventTransport, error) { return transport, nil },
		openApplication: func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error) {
			return nil, nil
		},
		openGraph: func(*social.App[social.Principal]) (hostGraph, error) { return hostTestGraph{}, nil },
		runPublisher: func(context.Context, *social.App[social.Principal]) error {
			<-server.listened
			return errors.New("private publisher error")
		},
		ready:         func(context.Context, *provider.Database, *social.App[social.Principal]) bool { return true },
		newServer:     func(http.Handler) hostHTTPServer { return server },
		startupGrace:  time.Second,
		shutdownGrace: time.Second,
	}
	err = runHostWith(ctx, cancel, runner)
	if err == nil || !strings.Contains(err.Error(), "event publisher stopped") || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "host readiness") {
		t.Fatalf("post-readiness publisher error=%v", err)
	}
	if transportCloses != 1 || databaseIsOpen(database) {
		t.Fatal("post-readiness publisher exit did not close owned dependencies")
	}
}

func TestOrder7SocialHostReadinessTimeoutNeverCreatesServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "timeout.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	transportCloses := 0
	transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { transportCloses++; return nil }}
	serverCreated := false
	server := newHostTestServer()
	server.listenError = errors.New("bounded forbidden admission")
	runner := hostRunner{
		openDatabase:       func(context.Context) (*provider.Database, error) { return database, nil },
		openEventTransport: func(context.Context, *provider.Database) (*hostOwnedEventTransport, error) { return transport, nil },
		openApplication: func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error) {
			return nil, nil
		},
		openGraph: func(*social.App[social.Principal]) (hostGraph, error) { return hostTestGraph{}, nil },
		runPublisher: func(publisherContext context.Context, _ *social.App[social.Principal]) error {
			<-publisherContext.Done()
			return nil
		},
		ready: func(context.Context, *provider.Database, *social.App[social.Principal]) bool { return false },
		newServer: func(http.Handler) hostHTTPServer {
			serverCreated = true
			return server
		},
		startupGrace:  20 * time.Millisecond,
		shutdownGrace: time.Second,
	}
	err = runHostWith(ctx, cancel, runner)
	if err == nil || !strings.Contains(err.Error(), "host readiness") || serverCreated {
		t.Fatalf("readiness timeout error=%v serverCreated=%t", err, serverCreated)
	}
	if transportCloses != 1 || databaseIsOpen(database) {
		t.Fatal("readiness timeout did not close owned dependencies")
	}
}

func TestOrder7SocialHostObserverEmitsOnlyClosedReconnectFields(t *testing.T) {
	const canary = "NATS_URL_SECRET_RAW_ERROR_SUBJECT"
	var output bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()
	type observerContextKey struct{}
	events.Observe(hostEventObserver{}, context.WithValue(context.Background(), observerContextKey{}, canary), golem.ModelID{1}, "", events.ObservationTransportReconnect, events.OutcomeFailure, "", 0, 0, 0, 0, 1)
	got := output.String()
	if got != "event_transport kind=transport_reconnect outcome=failure count=1\n" || strings.Contains(got, canary) || strings.Contains(got, "0100") {
		t.Fatalf("host reconnect observation=%q", got)
	}
}

func TestOrder7SocialReadinessTracksDynamicTransportAvailability(t *testing.T) {
	root := socialHostRoot(t)
	dsn := "file:" + filepath.Join(t.TempDir(), "ready.sqlite")
	applyReviewedSQLiteMigration(t, root, dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	transport := &hostAvailabilityTransport{EventTransport: memory}
	transport.available.Store(true)
	application, err := openApplication(context.Background(), database, transport)
	if err != nil {
		t.Fatal(err)
	}
	publisherContext, stopPublisher := context.WithCancel(context.Background())
	publisherStopped := make(chan error, 1)
	go func() {
		_ = application.RunEventPublisher(publisherContext)
		close(publisherStopped)
	}()
	defer func() {
		stopPublisher()
		select {
		case <-publisherStopped:
		case <-time.After(3 * time.Second):
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !application.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assertReady := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		readiness(database, application).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		if response.Code != want {
			t.Fatalf("readiness=%d body=%q want %d", response.Code, response.Body.String(), want)
		}
	}
	assertReady(http.StatusNoContent)
	transport.available.Store(false)
	assertReady(http.StatusServiceUnavailable)
	transport.available.Store(true)
	assertReady(http.StatusNoContent)
}

func TestOrder7SocialHostShutdownOrderIsClosed(t *testing.T) {
	var order []string
	publisherStopped := make(chan error, 1)
	resources := hostResources{
		server: hostShutdownFunc(func(context.Context) error { order = append(order, "http"); return nil }),
		graph:  hostShutdownFunc(func(context.Context) error { order = append(order, "graphql"); return nil }),
		stopPublisher: func() {
			order = append(order, "publisher-cancel")
			publisherStopped <- nil
		},
		publisherStopped: publisherStopped,
		closeTransport:   func() error { order = append(order, "transport"); return nil },
		closeDatabase:    func() error { order = append(order, "database"); return nil },
	}
	if err := resources.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"http", "graphql", "publisher-cancel", "transport", "database"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order=%v want %v", order, want)
	}
}

func TestOrder7SocialHostPublisherTimeoutDoesNotRaceOwnedDependencies(t *testing.T) {
	var order []string
	publisherStopped := make(chan error)
	resources := hostResources{
		server: hostShutdownFunc(func(context.Context) error { order = append(order, "http"); return nil }),
		graph:  hostShutdownFunc(func(context.Context) error { order = append(order, "graphql"); return nil }),
		stopPublisher: func() {
			order = append(order, "publisher-cancel")
		},
		publisherStopped: publisherStopped,
		closeTransport:   func() error { order = append(order, "transport"); return nil },
		closeDatabase:    func() error { order = append(order, "database"); return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resources.shutdown(ctx); err == nil {
		t.Fatal("publisher timeout was accepted as a completed shutdown")
	}
	want := []string{"http", "graphql", "publisher-cancel"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("timed-out shutdown order=%v want %v", order, want)
	}
}

func TestOrder7SocialHostShutdownFailuresStopAtOwnershipBarriersAndStayRedacted(t *testing.T) {
	const canary = "PRIVATE_DSN_BROKER_ERROR"
	for _, failure := range []string{"http", "graphql", "publisher", "transport", "database"} {
		t.Run(failure, func(t *testing.T) {
			var order []string
			publisherStopped := make(chan error, 1)
			resources := hostResources{
				server: hostShutdownFunc(func(context.Context) error {
					order = append(order, "http")
					if failure == "http" {
						return errors.New(canary)
					}
					return nil
				}),
				graph: hostShutdownFunc(func(context.Context) error {
					order = append(order, "graphql")
					if failure == "graphql" {
						return errors.New(canary)
					}
					return nil
				}),
				stopPublisher: func() {
					order = append(order, "publisher-cancel")
					if failure == "publisher" {
						publisherStopped <- errors.New(canary)
					} else {
						publisherStopped <- nil
					}
				},
				publisherStopped: publisherStopped,
				closeTransport: func() error {
					order = append(order, "transport")
					if failure == "transport" {
						return errors.New(canary)
					}
					return nil
				},
				closeDatabase: func() error {
					order = append(order, "database")
					if failure == "database" {
						return errors.New(canary)
					}
					return nil
				},
			}
			err := resources.shutdown(context.Background())
			if err == nil || strings.Contains(err.Error(), canary) {
				t.Fatalf("cleanup error=%v", err)
			}
			want := map[string][]string{
				"http":      {"http"},
				"graphql":   {"http", "graphql"},
				"publisher": {"http", "graphql", "publisher-cancel", "transport", "database"},
				"transport": {"http", "graphql", "publisher-cancel", "transport"},
				"database":  {"http", "graphql", "publisher-cancel", "transport", "database"},
			}[failure]
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("cleanup order=%v want %v", order, want)
			}
		})
	}
}

func TestOrder7SocialHostStartupTransportCloseFailureKeepsDatabaseOwned(t *testing.T) {
	const canary = "PRIVATE_TRANSPORT_ERROR"
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "owned.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	transport := &hostOwnedEventTransport{transport: mustMemoryTransport(t), close: func() error { return errors.New(canary) }}
	err = closeHostStartup(database, transport)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("startup cleanup error=%v", err)
	}
	if !databaseIsOpen(database) {
		t.Fatal("database closed after transport failed to relinquish ownership")
	}
}

type hostAvailabilityTransport struct {
	events.EventTransport
	available atomic.Bool
}

func (transport *hostAvailabilityTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport *hostAvailabilityTransport) TransportAvailable() bool {
	return transport.available.Load()
}

type hostShutdownFunc func(context.Context) error

func (shutdown hostShutdownFunc) Shutdown(ctx context.Context) error { return shutdown(ctx) }

type hostCloseableTransport struct {
	events.EventTransport
	closes int
}

func (transport *hostCloseableTransport) Close() error {
	transport.closes++
	return nil
}

func mustMemoryTransport(t *testing.T) events.EventTransport {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func databaseIsOpen(database *provider.Database) bool {
	if database == nil || database.UnsafeSQLX() == nil {
		return false
	}
	return database.UnsafeSQLX().PingContext(context.Background()) == nil
}

type hostTestGraph struct{}

func (hostTestGraph) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}
func (hostTestGraph) Shutdown(context.Context) error { return nil }

type hostTestServer struct {
	listenError error
	listened    chan struct{}
	stopped     chan struct{}
	listenOnce  atomic.Bool
	stopOnce    atomic.Bool
}

func newHostTestServer() *hostTestServer {
	return &hostTestServer{listened: make(chan struct{}), stopped: make(chan struct{})}
}

func (server *hostTestServer) ListenAndServe() error {
	if server.listenOnce.CompareAndSwap(false, true) {
		close(server.listened)
	}
	if server.listenError != nil {
		return server.listenError
	}
	<-server.stopped
	return http.ErrServerClosed
}

func (server *hostTestServer) Shutdown(context.Context) error {
	if server.stopOnce.CompareAndSwap(false, true) {
		close(server.stopped)
	}
	return nil
}
