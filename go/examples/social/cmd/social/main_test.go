package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestP8SocialHostOpensExactConfigAndServesLifecycle(t *testing.T) {
	root := socialHostRoot(t)
	databasePath := filepath.Join(t.TempDir(), "social.sqlite")
	dataSourceName := "file:" + databasePath
	applyReviewedSQLiteMigration(t, root, dataSourceName)

	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(context.Background(), database, transport)
	if err != nil {
		t.Fatalf("open exact example application config: %v", err)
	}
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("trusted GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = graph.Shutdown(context.Background()) })

	publisherCtx, cancelPublisher := context.WithCancel(context.Background())
	publisherStopped := make(chan error, 1)
	go func() { publisherStopped <- application.RunEventPublisher(publisherCtx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !application.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !application.EventCapabilities().PublisherRunning() {
		cancelPublisher()
		t.Fatal("event publisher did not become ready")
	}

	ready := httptest.NewRecorder()
	readiness(database, application).ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusNoContent {
		t.Fatalf("readiness status=%d body=%q", ready.Code, ready.Body.String())
	}

	handler := principalMiddleware(application, graph.Handler())
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"query { posts { id } }"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"posts":[]`) || strings.Contains(response.Body.String(), `"errors"`) {
		t.Fatalf("anonymous GraphQL status=%d body=%s", response.Code, response.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"query { posts { id } }"}`))
	invalid.Header.Set("Authorization", "Bearer invalid")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized || strings.Contains(invalidResponse.Body.String(), "invalid") && strings.Contains(invalidResponse.Body.String(), "Bearer") {
		t.Fatalf("invalid bearer status=%d body=%q", invalidResponse.Code, invalidResponse.Body.String())
	}

	cancelPublisher()
	select {
	case <-publisherStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("event publisher did not stop after cancellation")
	}
	if database.UnsafeSQLX() == nil {
		t.Fatal("application lifecycle closed its borrowed database")
	}
}

func TestP8DevelopmentPrincipalAuditIDsRemainDistinct(t *testing.T) {
	first, err := golem.ParseUUID("10000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := golem.ParseUUID("10000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	firstID := principalAuditID(social.Principal{Development: true, DevUserID: first})
	secondID := principalAuditID(social.Principal{Development: true, DevUserID: second})
	if firstID == secondID || firstID == principalAuditID(social.Principal{}) {
		t.Fatalf("development/session audit domains collided: %q %q", firstID, secondID)
	}
}

func TestP8SocialHostPostgreSQLOpensReviewedApplication(t *testing.T) {
	dataSourceName := strings.TrimSpace(os.Getenv("GOLEM_P8_SOCIAL_POSTGRES_DSN"))
	if dataSourceName == "" {
		t.Skip("GOLEM_P8_SOCIAL_POSTGRES_DSN is not configured")
	}
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(context.Background(), database, transport)
	if err != nil {
		t.Fatalf("open PostgreSQL example application: %v", err)
	}
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("trusted GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if database.UnsafeSQLX() == nil {
		t.Fatal("application lifecycle closed its borrowed PostgreSQL database")
	}
}

func socialHostRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate social host source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filename)))
}

func applyReviewedSQLiteMigration(t *testing.T, exampleRoot, dataSourceName string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "golem")
	build := exec.Command("go", "build", "-o", binary, "github.com/eleven-am/golem/go/cmd/golem")
	build.Dir = exampleRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build golem CLI: %v\n%s", err, output)
	}
	apply := exec.Command(binary, "migration", "apply", "--provider", "sqlite", "--dsn", dataSourceName, "--migrations", "migrations")
	apply.Dir = exampleRoot
	apply.Env = append(os.Environ(), "GOWORK=off")
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply reviewed SQLite migration: %v\n%s", err, output)
	}
}
