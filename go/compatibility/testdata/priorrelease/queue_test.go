package priorrelease

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

func TestReleasedQueueStartup(t *testing.T) {
	dir := os.Getenv("GOLEM_COMPAT_FIXTURES")
	if dir == "" {
		t.Fatal("run through the parent compatibility test")
	}
	ctx := context.Background()
	provider := sqlite.New()
	// Control: real v0.8.8 creates, admits and services its own fresh layout.
	control, _, err := provider.Open(ctx, filepath.Join(dir, "released-queue-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	legacy, err := provider.QueueStore(control, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.EnsureSchema(ctx); err != nil {
		t.Fatalf("released legacy control startup failed: %v", err)
	}
	if _, err := legacy.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: "control", Type: "compat.control", Payload: []byte(`{"control":true}`), MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	jobs, err := legacy.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"compat.control"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(jobs) != 1 || jobs[0].ID != "control" || jobs[0].AttemptCount != 1 {
		t.Fatalf("released legacy control could not service a job: jobs=%+v error=%v", jobs, err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "current-queue.db")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("missing current queue fixture: %v", err)
	}
	database, _, err := provider.Open(ctx, path)
	if err != nil {
		t.Fatalf("released provider could not open the fixture: %v", err)
	}
	defer database.Close()
	store, err := provider.QueueStore(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.EnsureSchema(ctx)
	if err == nil || !strings.Contains(err.Error(), "QUEUE_SQLITE_STORE: existing golem_queue_claim index") || !strings.Contains(err.Error(), "does not match") || !strings.Contains(err.Error(), `\"priority\"`) {
		t.Fatalf("released startup must refuse the actual priority claim-index shape before servicing jobs, got %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedPostgreSQLQueueStartup(t *testing.T) {
	dir := os.Getenv("GOLEM_COMPAT_FIXTURES")
	if dir == "" {
		t.Fatal("run through the parent compatibility test")
	}
	path := filepath.Join(dir, "postgres-queue.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 16<<10 {
		t.Fatal("missing or invalid private PostgreSQL queue fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private PostgreSQL queue fixture")
	}
	var fixture map[string]string
	if err := json.Unmarshal(data, &fixture); err != nil || len(fixture) != 2 || fixture["target"] == "" || fixture["control"] == "" || fixture["target"] == fixture["control"] {
		t.Fatal("invalid private PostgreSQL queue fixture identities")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	provider := postgresql.New()
	control, _, err := provider.Open(ctx, fixture["control"])
	if err != nil {
		t.Fatal("released PostgreSQL control open failed")
	}
	defer control.Close()
	legacy, err := provider.QueueStoreAt(control, "public", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.EnsureSchema(ctx); err != nil {
		t.Fatalf("released PostgreSQL control startup failed: %v", err)
	}
	if _, err := legacy.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: "control", Type: "compat.control", Payload: []byte(`{"control":true}`), MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	jobs, err := legacy.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"compat.control"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(jobs) != 1 || jobs[0].ID != "control" || jobs[0].AttemptCount != 1 {
		t.Fatalf("released PostgreSQL control could not service a job: jobs=%+v error=%v", jobs, err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	database, _, err := provider.Open(ctx, fixture["target"])
	if err != nil {
		t.Fatal("released PostgreSQL provider could not open the target")
	}
	defer database.Close()
	store, err := provider.QueueStoreAt(database, "public", nil)
	if err != nil {
		t.Fatal(err)
	}
	// v0.8.8 queue_store.go verifies columns status,available_at,type.
	// The current index adds priority and id; no other failure is acceptance.
	err = store.EnsureSchema(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "QUEUE_POSTGRESQL_STORE: existing golem_queue_claim index ") ||
		!strings.Contains(err.Error(), "columns=status,priority,available_at,id,type ") ||
		!strings.Contains(err.Error(), "does not match golem's (method=btree unique=false columns=status,available_at,type ") {
		t.Fatalf("released startup must refuse the actual PostgreSQL priority claim-index shape before servicing jobs, got %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
