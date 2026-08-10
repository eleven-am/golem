package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
)

// TestP8MigrationInterruptionProcess is invoked only as a subprocess by the
// row-17 recovery oracle. The exit occurs from the production provider apply
// path at a context-local boundary; no environment check exists in production.
func TestP8MigrationInterruptionProcess(t *testing.T) {
	if os.Getenv("GOLEM_P8_MIGRATION_HELPER") != "1" {
		t.Skip("row-17 subprocess helper")
	}
	boundary := os.Getenv("GOLEM_P8_MIGRATION_BOUNDARY")
	providerID := os.Getenv("GOLEM_P8_MIGRATION_PROVIDER")
	dsn := os.Getenv("GOLEM_P8_MIGRATION_DSN")
	example := os.Getenv("GOLEM_P8_MIGRATION_EXAMPLE")
	if boundary == "" || providerID == "" || dsn == "" || example == "" {
		t.Fatal("row-17 migration helper environment is incomplete")
	}
	reached := false
	ctx := migrationfailpoint.WithHook(context.Background(), func(current string) {
		if current == boundary {
			reached = true
			os.Exit(97)
		}
	})
	var stderr bytes.Buffer
	code := runMigrationApply(ctx, example, []string{"--provider", providerID, "--dsn", dsn, "--migrations", "migrations"}, io.Discard, &stderr)
	if !reached {
		t.Fatalf("migration boundary %q was not reached: code=%d stderr=%s", boundary, code, stderr.String())
	}
}
