package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestHealthEndpointSafeShape(t *testing.T) {
	const canary = "P8_HEALTH_DSN_CREDENTIAL_SCHEMA_BACKLOG_PRINCIPAL_RAW_ERROR"
	root := socialHostRoot(t)
	dsn := "file:" + filepath.Join(t.TempDir(), canary+".sqlite")
	applyReviewedSQLiteMigration(t, root, dsn)

	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
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
		t.Fatal(err)
	}
	publisherContext, stopPublisher := context.WithCancel(context.Background())
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	t.Cleanup(stopPublisher)
	deadline := time.Now().Add(3 * time.Second)
	for !application.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !application.EventCapabilities().PublisherRunning() {
		t.Fatal("publisher did not become ready")
	}

	mux := http.NewServeMux()
	registerHealthHandlers(mux, database, application)
	assertHealth := func(path string, status int, body string) {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != status || response.Body.String() != body {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
		encoded := response.Header().Get("Content-Type") + response.Body.String()
		for _, forbidden := range []string{canary, dsn, "sqlite", "_golem", "principal", "backlog", "database is closed"} {
			if strings.Contains(strings.ToLower(encoded), strings.ToLower(forbidden)) {
				t.Fatalf("%s disclosed %q in headers/body %q", path, forbidden, encoded)
			}
		}
	}
	assertHealth("/health/live", http.StatusNoContent, "")
	assertHealth("/health/ready", http.StatusNoContent, "")

	stopPublisher()
	select {
	case <-publisherDone:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher did not stop")
	}
	assertHealth("/health/live", http.StatusNoContent, "")
	assertHealth("/health/ready", http.StatusServiceUnavailable, "not ready\n")

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertHealth("/health/live", http.StatusNoContent, "")
	assertHealth("/health/ready", http.StatusServiceUnavailable, "not ready\n")
}
