package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

func queueStorageCommandLegacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	database, _, err := internalsqlite.New().Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// Frozen released v0.8.8 storage, not derived from the current constructor.
	for _, statement := range []string{
		`CREATE TABLE "golem_queue" ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`,
		`CREATE INDEX "golem_queue_claim" ON "golem_queue" ("status","available_at","type")`,
		`CREATE UNIQUE INDEX "golem_queue_dedupe" ON "golem_queue" ("dedupe_key") WHERE "status" IN ('pending','leased')`,
		`CREATE INDEX "golem_queue_exclusive" ON "golem_queue" ("exclusive_key") WHERE "status"='leased'`,
		`INSERT INTO golem_queue(id,type,payload,status,attempt_count,max_attempts,available_at,lease_token,lease_until,resource_name,resource_cost,resource_capacity,dedupe_key,exclusive_key,enqueued_at,updated_at) VALUES ('leased','gate.job',X'010203','leased',2,5,999,'token',999,'network',2,7,'dedupe','exclusive',100,200)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestQueueStorageCommandInspectsReadOnlyAndExplicitlyUpgrades(t *testing.T) {
	for _, refuseOutput := range []bool{false, true} {
		name := "success"
		if refuseOutput {
			name = "output_refusal_after_upgrade"
		}
		t.Run(name, func(t *testing.T) {
			path := queueStorageCommandLegacyDatabase(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"queue", "storage", "inspect", "--provider", "sqlite", "--dsn", path}
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), t.TempDir(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("inspect code=%d error=%s", code, stderr.String())
			}
			var state queueprovider.StorageInspection
			if err := json.Unmarshal(stdout.Bytes(), &state); err != nil || state.Version != queueprovider.StorageLegacyV088 {
				t.Fatalf("inspect=%+v error=%v", state, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("inspection changed database bytes: %v", err)
			}
			args[2] = "upgrade"
			stdout.Reset()
			stderr.Reset()
			if refuseOutput {
				if code := run(context.Background(), t.TempDir(), args, refusingWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "upgrade completed") {
					t.Fatalf("committed output refusal code=%d error=%s", code, stderr.String())
				}
			} else if code := run(context.Background(), t.TempDir(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("upgrade code=%d error=%s", code, stderr.String())
			}
			args[2] = "inspect"
			stdout.Reset()
			stderr.Reset()
			if code := run(context.Background(), t.TempDir(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("current inspect code=%d error=%s", code, stderr.String())
			}
			if err := json.Unmarshal(stdout.Bytes(), &state); err != nil || state.Version != queueprovider.StoragePriorityV1 {
				t.Fatalf("current inspect=%+v error=%v", state, err)
			}
			database, _, err := internalsqlite.New().Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var preserved int
			if err := database.Get(&preserved, `SELECT count(*) FROM golem_queue WHERE id='leased' AND priority=0 AND payload=X'010203' AND status='leased' AND attempt_count=2 AND max_attempts=5 AND available_at=999 AND lease_token='token' AND lease_until=999 AND resource_name='network' AND resource_cost=2 AND resource_capacity=7 AND dedupe_key='dedupe' AND exclusive_key='exclusive' AND enqueued_at=100 AND updated_at=200`); err != nil || preserved != 1 {
				t.Fatalf("upgrade changed live lease: preserved=%d error=%v", preserved, err)
			}
		})
	}
}

func TestQueueStorageCommandRefusesMissingDatabaseWithoutCreationOrDisclosure(t *testing.T) {
	for _, operation := range []string{"inspect", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "PRIVATE_QUEUE_STORAGE.db")
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), t.TempDir(), []string{"queue", "storage", operation, "--provider", "sqlite", "--dsn", path}, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "PRIVATE_QUEUE_STORAGE") || strings.Contains(stderr.String(), path) {
				t.Fatalf("refusal code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("missing source was created: %v", err)
			}
		})
	}
}

func TestQueueStorageCommandRejectsUnsafeArgumentsBeforeDatabaseWork(t *testing.T) {
	for _, args := range [][]string{
		{"queue", "storage", "upgrade", "--provider", "sqlite"},
		{"queue", "storage", "upgrade", "--provider", "sqlite", "--dsn", "file:PRIVATE?mode=memory"},
		{"queue", "storage", "upgrade", "--provider", "sqlite", "--dsn", "file:PRIVATE?mode=rwc"},
		{"queue", "storage", "upgrade", "--provider", "sqlite", "--dsn", "file:PRIVATE?mode=rw&mode=ro"},
		{"queue", "storage", "upgrade", "--provider", "sqlite", "--dsn", "PRIVATE", "--system-namespace", "other"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), t.TempDir(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "PRIVATE") {
			t.Fatalf("arguments refusal code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}
