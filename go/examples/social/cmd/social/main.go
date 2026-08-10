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
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

type principalContextKey struct{}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := openDatabase(ctx)
	if err != nil {
		log.Fatal("database startup failed")
	}
	defer database.Close()

	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 256})
	if err != nil {
		log.Fatal("event transport startup failed")
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		log.Fatal("application startup failed")
	}
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError:  func(context.Context, error) { log.Print("GraphQL request failed") },
	})
	if err != nil {
		log.Fatal("GraphQL startup failed")
	}
	defer graph.Shutdown(context.Background())

	publisherCtx, stopPublisher := context.WithCancel(ctx)
	defer stopPublisher()
	publisherStopped := make(chan struct{})
	go func() {
		defer close(publisherStopped)
		if err := application.RunEventPublisher(publisherCtx); err != nil && publisherCtx.Err() == nil {
			log.Print("event publisher stopped")
			stop()
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/graphql", principalMiddleware(application, graph.Handler()))
	registerHealthHandlers(mux, database, application)

	server := &http.Server{
		Addr:              environment("GOLEM_HTTP_ADDRESS", ":8080"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverStopped := make(chan error, 1)
	go func() { serverStopped <- server.ListenAndServe() }()

	select {
	case <-ctx.Done():
	case err := <-serverStopped:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print("HTTP server stopped")
		}
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownCtx)
	_ = graph.Shutdown(shutdownCtx)
	stopPublisher()
	select {
	case <-publisherStopped:
	case <-shutdownCtx.Done():
	}
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
		pool := database.UnsafeSQLX()
		if pool == nil || pool.PingContext(ctx) != nil || !application.EventCapabilities().PublisherRunning() {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func registerHealthHandlers(mux *http.ServeMux, database *provider.Database, application *social.App[social.Principal]) {
	mux.HandleFunc("/health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/health/ready", readiness(database, application))
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
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
