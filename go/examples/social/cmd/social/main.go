package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eleven-am/golem/go/events"
	eventnats "github.com/eleven-am/golem/go/events/nats"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

type principalContextKey struct{}

type hostEventObserver struct{}

func (hostEventObserver) ObserveEvent(_ context.Context, observation events.Observation) {
	log.Printf("event_transport kind=%s outcome=%s count=%d", observation.Kind(), observation.Outcome(), observation.AggregateCount())
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runHost(ctx, stop); err != nil {
		log.Fatal(err)
	}
}

func runHost(ctx context.Context, stop context.CancelFunc) error {
	return runHostWith(ctx, stop, hostRunner{
		openDatabase: openDatabase, openEventTransport: openEventTransport, openApplication: openApplication,
		openGraph: func(application *social.App[social.Principal]) (hostGraph, error) {
			return application.GraphQL(social.GraphQLConfig[social.Principal]{PrincipalFromContext: principalFromContext, ReportInternalError: func(context.Context, error) { log.Print("GraphQL request failed") }})
		},
		runPublisher: func(ctx context.Context, application *social.App[social.Principal]) error {
			return application.RunEventPublisher(ctx)
		},
		ready: hostReady,
		newServer: func(handler http.Handler) hostHTTPServer {
			return &http.Server{Addr: environment("GOLEM_HTTP_ADDRESS", ":8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
		},
		startupGrace: 15 * time.Second, shutdownGrace: 15 * time.Second,
	})
}

type hostGraph interface {
	hostShutdowner
	Handler() http.Handler
}
type hostHTTPServer interface {
	hostShutdowner
	ListenAndServe() error
}
type hostRunner struct {
	openDatabase       func(context.Context) (*provider.Database, error)
	openEventTransport func(context.Context, *provider.Database) (*hostOwnedEventTransport, error)
	openApplication    func(context.Context, *provider.Database, events.EventTransport) (*social.App[social.Principal], error)
	openGraph          func(*social.App[social.Principal]) (hostGraph, error)
	runPublisher       func(context.Context, *social.App[social.Principal]) error
	ready              func(context.Context, *provider.Database, *social.App[social.Principal]) bool
	newServer          func(http.Handler) hostHTTPServer
	startupGrace       time.Duration
	shutdownGrace      time.Duration
}

func runHostWith(ctx context.Context, stop context.CancelFunc, runner hostRunner) error {
	if ctx == nil || stop == nil || runner.openDatabase == nil || runner.openEventTransport == nil || runner.openApplication == nil || runner.openGraph == nil || runner.runPublisher == nil || runner.ready == nil || runner.newServer == nil || runner.startupGrace <= 0 || runner.shutdownGrace <= 0 {
		return errors.New("host startup failed")
	}
	database, err := runner.openDatabase(ctx)
	if err != nil {
		return errors.New("database startup failed")
	}
	transport, err := runner.openEventTransport(ctx, database)
	if err != nil {
		return errors.Join(errors.New("event transport startup failed"), closeHostStartup(database, nil))
	}
	application, err := runner.openApplication(ctx, database, transport.transport)
	if err != nil {
		return errors.Join(errors.New("application startup failed"), closeHostStartup(database, transport))
	}
	graph, err := runner.openGraph(application)
	if err != nil {
		return errors.Join(errors.New("GraphQL startup failed"), closeHostStartup(database, transport))
	}
	publisherCtx, stopPublisher := context.WithCancel(ctx)
	publisherStopped := make(chan error, 1)
	go func() {
		publisherError := runner.runPublisher(publisherCtx, application)
		if publisherError != nil && publisherCtx.Err() == nil {
			publisherStopped <- errors.New("event publisher stopped")
			stop()
			return
		}
		if publisherCtx.Err() == nil {
			publisherStopped <- errors.New("event publisher stopped")
			stop()
			return
		}
		publisherStopped <- nil
	}()
	publisherAlreadyStopped, readinessError := waitForHostReadiness(ctx, database, application, publisherStopped, runner.ready, runner.startupGrace)
	if readinessError != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runner.shutdownGrace)
		defer cancel()
		stopped := (<-chan error)(publisherStopped)
		if publisherAlreadyStopped {
			stopped = nil
		}
		resources := hostResources{graph: graph, stopPublisher: stopPublisher, publisherStopped: stopped, closeTransport: transport.Close, closeDatabase: database.Close}
		return errors.Join(errors.New("host readiness failed"), readinessError, resources.shutdown(shutdownCtx))
	}
	select {
	case publisherError := <-publisherStopped:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runner.shutdownGrace)
		defer cancel()
		resources := hostResources{graph: graph, stopPublisher: stopPublisher, closeTransport: transport.Close, closeDatabase: database.Close}
		if publisherError != nil {
			return errors.Join(errors.New("event publisher stopped"), resources.shutdown(shutdownCtx))
		}
		return errors.Join(errors.New("event publisher stopped before HTTP startup"), resources.shutdown(shutdownCtx))
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runner.shutdownGrace)
		defer cancel()
		resources := hostResources{graph: graph, stopPublisher: stopPublisher, publisherStopped: publisherStopped, closeTransport: transport.Close, closeDatabase: database.Close}
		return errors.Join(errors.New("host startup cancelled"), resources.shutdown(shutdownCtx))
	default:
	}

	mux := http.NewServeMux()
	mux.Handle("/graphql", principalMiddleware(application, graph.Handler()))
	registerHealthHandlers(mux, database, application)
	server := runner.newServer(mux)
	if server == nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runner.shutdownGrace)
		defer cancel()
		return errors.Join(errors.New("HTTP startup failed"), (hostResources{graph: graph, stopPublisher: stopPublisher, publisherStopped: publisherStopped, closeTransport: transport.Close, closeDatabase: database.Close}).shutdown(shutdownCtx))
	}
	serverStopped := make(chan error, 1)
	go func() { serverStopped <- server.ListenAndServe() }()
	var terminalError error
	select {
	case <-ctx.Done():
	case <-serverStopped:
		terminalError = errors.New("HTTP server stopped")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), runner.shutdownGrace)
	defer cancelShutdown()
	resources := hostResources{server: server, graph: graph, stopPublisher: stopPublisher, publisherStopped: publisherStopped, closeTransport: transport.Close, closeDatabase: database.Close}
	return errors.Join(terminalError, resources.shutdown(shutdownCtx))
}

func waitForHostReadiness(ctx context.Context, database *provider.Database, application *social.App[social.Principal], publisherStopped <-chan error, ready func(context.Context, *provider.Database, *social.App[social.Principal]) bool, grace time.Duration) (bool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready(startupCtx, database, application) {
			return false, nil
		}
		select {
		case err := <-publisherStopped:
			if err != nil {
				return true, errors.New("event publisher stopped")
			}
			return true, errors.New("event publisher stopped before readiness")
		case <-startupCtx.Done():
			return false, errors.New("host readiness timed out")
		case <-ticker.C:
		}
	}
}

func closeHostStartup(database *provider.Database, transport *hostOwnedEventTransport) error {
	if transport != nil {
		if err := transport.Close(); err != nil {
			return errors.New("event transport shutdown failed")
		}
	}
	if database != nil {
		if err := database.Close(); err != nil {
			return errors.New("database shutdown failed")
		}
	}
	return nil
}

type hostEventTransportKind string

const (
	hostEventTransportMemory hostEventTransportKind = "memory"
	hostEventTransportNATS   hostEventTransportKind = "nats"
)

type hostNATSConfig = eventnats.Config
type hostEventTransportConfiguration struct {
	kind hostEventTransportKind
	nats hostNATSConfig
}
type closeableEventTransport interface {
	events.EventTransport
	Close() error
}
type hostOwnedEventTransport struct {
	transport events.EventTransport
	close     func() error
}

func (transport *hostOwnedEventTransport) Close() error {
	if transport == nil || transport.close == nil {
		return nil
	}
	return transport.close()
}

type hostEventTransportFactories struct {
	memory func(events.MemoryLimits) (events.EventTransport, error)
	nats   func(context.Context, *provider.Database, hostNATSConfig) (closeableEventTransport, error)
}

func eventTransportConfiguration(providerKind golem.Provider, getenv func(string) string) (hostEventTransportConfiguration, error) {
	if getenv == nil {
		return hostEventTransportConfiguration{}, errors.New("event transport configuration failed")
	}
	kind := hostEventTransportKind(strings.ToLower(strings.TrimSpace(environmentValue(getenv, "GOLEM_EVENT_TRANSPORT", string(hostEventTransportMemory)))))
	urlsValue, prefix := strings.TrimSpace(getenv("GOLEM_NATS_URLS")), strings.TrimSpace(getenv("GOLEM_NATS_SUBJECT_PREFIX"))
	switch kind {
	case hostEventTransportMemory:
		if urlsValue != "" || prefix != "" {
			return hostEventTransportConfiguration{}, errors.New("NATS configuration requires GOLEM_EVENT_TRANSPORT=nats")
		}
		return hostEventTransportConfiguration{kind: kind}, nil
	case hostEventTransportNATS:
		if providerKind != golem.PostgreSQL || urlsValue == "" || prefix == "" {
			return hostEventTransportConfiguration{}, errors.New("NATS requires PostgreSQL, URLs, and a database-unique subject prefix")
		}
		parts := strings.Split(urlsValue, ",")
		urls := make([]string, len(parts))
		for index, part := range parts {
			urls[index] = strings.TrimSpace(part)
			if urls[index] == "" {
				return hostEventTransportConfiguration{}, errors.New("NATS URL list is invalid")
			}
		}
		return hostEventTransportConfiguration{kind: kind, nats: hostNATSConfig{URLs: urls, SubjectPrefix: prefix, Observer: hostEventObserver{}}}, nil
	default:
		return hostEventTransportConfiguration{}, errors.New("GOLEM_EVENT_TRANSPORT must be memory or nats")
	}
}
func openEventTransport(ctx context.Context, database *provider.Database) (*hostOwnedEventTransport, error) {
	return openEventTransportWith(ctx, database, os.Getenv, hostEventTransportFactories{memory: events.NewMemoryTransport, nats: func(ctx context.Context, database *provider.Database, config hostNATSConfig) (closeableEventTransport, error) {
		return eventnats.Open(ctx, database, config)
	}})
}
func openEventTransportWith(ctx context.Context, database *provider.Database, getenv func(string) string, factories hostEventTransportFactories) (*hostOwnedEventTransport, error) {
	if ctx == nil || database == nil || factories.memory == nil || factories.nats == nil {
		return nil, errors.New("event transport startup failed")
	}
	configuration, err := eventTransportConfiguration(database.Provider(), getenv)
	if err != nil {
		return nil, err
	}
	switch configuration.kind {
	case hostEventTransportMemory:
		transport, err := factories.memory(events.MemoryLimits{Buffer: 256})
		if err != nil {
			return nil, err
		}
		return &hostOwnedEventTransport{transport: transport, close: func() error { return nil }}, nil
	case hostEventTransportNATS:
		transport, err := factories.nats(ctx, database, configuration.nats)
		if err != nil {
			return nil, err
		}
		return &hostOwnedEventTransport{transport: transport, close: transport.Close}, nil
	default:
		return nil, errors.New("event transport startup failed")
	}
}

type hostShutdowner interface{ Shutdown(context.Context) error }
type hostResources struct {
	server           hostShutdowner
	graph            hostShutdowner
	stopPublisher    context.CancelFunc
	publisherStopped <-chan error
	closeTransport   func() error
	closeDatabase    func() error
}

func (resources hostResources) shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("host shutdown failed")
	}
	var failures []error
	if resources.server != nil {
		if err := resources.server.Shutdown(ctx); err != nil {
			return errors.New("HTTP shutdown failed")
		}
	}
	if resources.graph != nil {
		if err := resources.graph.Shutdown(ctx); err != nil {
			return errors.New("GraphQL shutdown failed")
		}
	}
	if resources.stopPublisher != nil {
		resources.stopPublisher()
	}
	if resources.publisherStopped != nil {
		select {
		case err := <-resources.publisherStopped:
			if err != nil {
				failures = append(failures, errors.New("event publisher stopped"))
			}
		case <-ctx.Done():
			failures = append(failures, errors.New("event publisher shutdown timed out"))
			return errors.Join(failures...)
		}
	}
	if resources.closeTransport != nil {
		if err := resources.closeTransport(); err != nil {
			failures = append(failures, errors.New("event transport shutdown failed"))
			return errors.Join(failures...)
		}
	}
	if resources.closeDatabase != nil {
		if err := resources.closeDatabase(); err != nil {
			failures = append(failures, errors.New("database shutdown failed"))
		}
	}
	return errors.Join(failures...)
}

func openApplication(ctx context.Context, database *provider.Database, transport events.EventTransport) (*social.App[social.Principal], error) {
	return openApplicationWithScopedReport(ctx, database, transport, func(_ context.Context, record golem.ScopedAuditRecord) {
		log.Printf("scoped_query principal=%s provider=%s outcome=%d rows=%d duration=%s shape=%s", record.PrincipalAuditID(), record.Provider(), record.Outcome(), record.RowCount(), record.Duration(), record.ShapeFingerprint())
	})
}

func openApplicationWithScopedReport(ctx context.Context, database *provider.Database, transport events.EventTransport, report func(context.Context, golem.ScopedAuditRecord)) (*social.App[social.Principal], error) {
	return social.Open(ctx, social.Config[social.Principal]{
		Database:       database,
		EventTransport: transport,
		ResolvePrincipal: func(ctx context.Context, principal social.Principal) (social.Actor, error) {
			return resolveSessionActor(ctx, database, principal)
		},
		SnapshotPrincipal: func(principal social.Principal) (social.Principal, error) { return principal, nil },
		SnapshotActor:     func(actor social.Actor) (social.Actor, error) { return actor, nil },
		AuditPrincipal:    principalAuditID,
		ReportScopedQuery: report,
		ReportEventOperator: func(_ context.Context, record events.OperatorAuditRecord) {
			log.Printf("event_operator action=%s outcome=%s causations=%d facts=%d", record.Action(), record.Outcome(), record.Causations(), record.Facts())
		},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			log.Printf("after_commit_hook operation=%s model=%x", failure.Operation(), failure.Model())
		},
	})
}

func principalAuditID(principal social.Principal) string {
	value := make([]byte, 0, 40)
	if principal.Development {
		value = append(value, "development:"...)
		userID := principal.DevUserID.Bytes()
		value = append(value, userID[:]...)
	} else {
		value = append(value, "session:"...)
		value = append(value, principal.TokenHash[:]...)
	}
	digest := sha256.Sum256(value)
	return "principal-" + hexPrefix(digest[:8])
}

func resolveSessionActor(ctx context.Context, database *provider.Database, principal social.Principal) (social.Actor, error) {
	if principal.Development {
		return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
	}
	if principal.TokenHash == ([32]byte{}) {
		return social.Actor{}, nil
	}
	query := database.UnsafeSQLX().Rebind(`SELECT user_id,expires_at FROM sessions WHERE token_hash=?`)
	var userIDText string
	var expiresAt time.Time
	switch database.Provider() {
	case golem.SQLite:
		var microseconds int64
		if err := database.UnsafeSQLX().QueryRowxContext(ctx, query, principal.TokenHash[:]).Scan(&userIDText, &microseconds); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return social.Actor{}, nil
			}
			return social.Actor{}, errors.New("principal resolution failed")
		}
		expiresAt = time.UnixMicro(microseconds).UTC()
	case golem.PostgreSQL:
		if err := database.UnsafeSQLX().QueryRowxContext(ctx, query, principal.TokenHash[:]).Scan(&userIDText, &expiresAt); err != nil {
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
	userID, err := golem.ParseUUID(userIDText)
	if err != nil {
		return social.Actor{}, errors.New("principal resolution failed")
	}
	return social.Actor{UserID: userID, Authenticated: true}, nil
}

func openDatabase(ctx context.Context) (*provider.Database, error) {
	dataSourceName := strings.TrimSpace(os.Getenv("GOLEM_DATABASE_DSN"))
	if dataSourceName == "" {
		return nil, errors.New("GOLEM_DATABASE_DSN is required")
	}
	switch strings.ToLower(strings.TrimSpace(environment("GOLEM_PROVIDER", "sqlite"))) {
	case "sqlite":
		return sqlite.Open(ctx, sqlite.Config{DataSourceName: dataSourceName})
	case "postgresql":
		return postgresql.Open(ctx, postgresql.Config{DataSourceName: dataSourceName})
	default:
		return nil, errors.New("GOLEM_PROVIDER must be sqlite or postgresql")
	}
}

func principalMiddleware(application *social.App[social.Principal], next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := authenticate(request.Context(), application, request)
		if err != nil {
			http.Error(writer, "invalid principal", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
}

func authenticate(ctx context.Context, application *social.App[social.Principal], request *http.Request) (social.Principal, error) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if authorization != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(authorization, prefix) || len(authorization) == len(prefix) {
			return social.Principal{}, errors.New("invalid bearer token")
		}
		tokenHash := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(authorization, prefix))))
		row, err := application.System().Sessions.FindUnique(ctx,
			social.Sessions.ByTokenHash.Value(tokenHash[:]),
			social.Sessions.Select(social.Sessions.ExpiresAt),
		)
		if err != nil {
			return social.Principal{}, errors.New("invalid bearer token")
		}
		expiresAt, expiryPresent := golem.Value(row, social.Sessions.ExpiresAt).Get()
		if !expiryPresent || !expiresAt.After(time.Now().UTC()) {
			return social.Principal{}, errors.New("invalid bearer token")
		}
		return social.Principal{TokenHash: tokenHash}, nil
	}

	// This opt-in exists only for the local quickstart. It is mechanically
	// disabled in the deployment path unless an operator names it insecure.
	if value := strings.TrimSpace(request.Header.Get("X-Golem-User")); value != "" && os.Getenv("GOLEM_ALLOW_INSECURE_HEADER_AUTH") == "1" {
		id, err := golem.ParseUUID(value)
		if err != nil {
			return social.Principal{}, errors.New("invalid development principal")
		}
		return social.Principal{Development: true, DevUserID: id}, nil
	}
	return social.Principal{}, nil
}

func principalFromContext(ctx context.Context) (social.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(social.Principal)
	return principal, ok
}

func readiness(database *provider.Database, application *social.App[social.Principal]) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 500*time.Millisecond)
		defer cancel()
		if !hostReady(ctx, database, application) {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func hostReady(ctx context.Context, database *provider.Database, application *social.App[social.Principal]) bool {
	if ctx == nil || database == nil || application == nil {
		return false
	}
	pool := database.UnsafeSQLX()
	capabilities := application.EventCapabilities()
	return pool != nil && pool.PingContext(ctx) == nil && capabilities.PublisherRunning() && capabilities.TransportAvailable()
}

func registerHealthHandlers(mux *http.ServeMux, database *provider.Database, application *social.App[social.Principal]) {
	mux.HandleFunc("/health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/health/ready", readiness(database, application))
}

func environment(name, fallback string) string {
	return environmentValue(os.Getenv, name, fallback)
}

func environmentValue(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func hexPrefix(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = alphabet[current>>4]
		result[index*2+1] = alphabet[current&0x0f]
	}
	return string(result)
}
