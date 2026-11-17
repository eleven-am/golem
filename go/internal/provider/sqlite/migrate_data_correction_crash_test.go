package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

const sqliteCorrectionCrashEnvironment = "GOLEM_SQLITE_CORRECTION_CRASH"

type sqliteCorrectionCrashArtifacts struct {
	Manifest json.RawMessage
	Files    map[string][]byte
}

// The parent kills this exact test executable while the real provider owns its
// migration transaction. No deferred Rollback/Close can run in the killed child.
func TestSQLiteDataCorrectionCrashChild(t *testing.T) {
	if os.Getenv(sqliteCorrectionCrashEnvironment) != "1" {
		t.Skip("owned crash child only")
	}
	path := os.Getenv("GOLEM_SQLITE_CORRECTION_ARTIFACTS")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(content) > 4<<20 {
		t.Fatal("invalid bounded child artifacts", err, closeErr)
	}
	var artifacts sqliteCorrectionCrashArtifacts
	if err := json.Unmarshal(content, &artifacts); err != nil {
		t.Fatal(err)
	}
	manifest, err := migration.ParseManifest(artifacts.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.VerifyManifest(manifest, artifacts.Files); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	database, _, err := New().Open(ctx, os.Getenv("GOLEM_SQLITE_CORRECTION_DATABASE"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	boundary := os.Getenv("GOLEM_SQLITE_CORRECTION_BOUNDARY")
	reached := false
	ctx = migrationfailpoint.WithHook(ctx, func(actual string) {
		if actual != boundary {
			return
		}
		reached = true
		ready := os.NewFile(3, "correction-boundary")
		if ready == nil {
			t.Fatal("missing parent boundary pipe")
		}
		_, writeErr := fmt.Fprintln(ready, actual)
		closeErr := ready.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("boundary announcement failed", writeErr, closeErr)
		}
		<-ctx.Done()
		t.Fatal("parent failed to kill child at migration boundary")
	})
	if err := New().ApplyMigration(ctx, database, manifest, artifacts.Files); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("child escaped expected crash boundary %q (reached=%t)", boundary, reached)
}

func TestSQLiteDataCorrectionProcessCrashRecoversExactlyOnce(t *testing.T) {
	for _, boundary := range []string{"inside_transaction_before_ledger", "inside_transaction_before_commit", "after_phase_commit"} {
		t.Run(boundary, func(t *testing.T) {
			database, schema, manifest, files := sqliteCorrectionFixture(t)
			before := sqliteCorrectionCrashState(t, database, schema)
			manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, "UPDATE items SET name='corrected' WHERE id=7;\n")
			encoded, err := migration.EncodeManifest(manifest, files)
			if err != nil {
				t.Fatal(err)
			}
			content, err := json.Marshal(sqliteCorrectionCrashArtifacts{Manifest: encoded, Files: files})
			if err != nil || len(content) > 4<<20 {
				t.Fatal("crash artifact envelope too large", err)
			}
			artifacts := filepath.Join(t.TempDir(), "reviewed.json")
			if err := os.WriteFile(artifacts, content, 0600); err != nil {
				t.Fatal(err)
			}
			var databaseFiles []struct {
				Seq        int
				Name, File string
			}
			if err := database.Select(&databaseFiles, `PRAGMA database_list`); err != nil {
				t.Fatal(err)
			}
			var path string
			for _, file := range databaseFiles {
				if file.Name == "main" {
					path = file.File
				}
			}
			if path == "" {
				t.Fatal("crash fixture requires a real file-backed database")
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			sqliteKillCorrectionAtBoundary(t, path, artifacts, boundary)
			reopened, _, err := New().Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.SetMaxOpenConns(1)
			reopened.SetMaxIdleConns(1)
			if err := New().Verify(context.Background(), reopened, schema); err != nil {
				t.Fatalf("crash recovered catalog drift: %v", err)
			}
			recovered := sqliteCorrectionCrashState(t, reopened, schema)
			if boundary != "after_phase_commit" {
				if !reflect.DeepEqual(before, recovered) {
					t.Fatalf("uncommitted process crash retained changes: before=%#v after=%#v", before, recovered)
				}
				assertSQLiteBackfillFTS(t, reopened, "retired", 1)
				assertSQLiteBackfillFTS(t, reopened, "corrected", 0)
				if err := New().ApplyMigration(context.Background(), reopened, manifest, files); err != nil {
					t.Fatalf("crash retry: %v", err)
				}
				recovered = sqliteCorrectionCrashState(t, reopened, schema)
			}
			if len(recovered.Rows) != 3 || recovered.Rows[0].Name.String != "corrected" || recovered.Rows[0].GolemVersion != 58 || recovered.Rows[0].Version != 4 || !reflect.DeepEqual(recovered.Rows[1:], before.Rows[1:]) || recovered.Counter != 58 || recovered.Identity == before.Identity {
				t.Fatalf("committed correction was not applied exactly once: %#v", recovered)
			}
			if !reflect.DeepEqual(before.Facts, recovered.Facts) || !reflect.DeepEqual(before.Children, recovered.Children) {
				t.Fatal("process crash recovery changed immutable facts or FK data")
			}
			if len(recovered.Ledger) != 2 || !reflect.DeepEqual(before.Ledger, recovered.Ledger[:1]) {
				t.Fatal("process crash recovery rewrote ledger prefix")
			}
			if err := migration.VerifyLedger(manifest, recovered.Ledger); err != nil {
				t.Fatal(err)
			}
			assertSQLiteBackfillFTS(t, reopened, "retired", 0)
			assertSQLiteBackfillFTS(t, reopened, "corrected", 1)
			assertSQLiteBackfillFTS(t, reopened, "mailbox", 1)
			if err := New().ApplyMigration(context.Background(), reopened, manifest, files); err == nil || !strings.Contains(err.Error(), "no unapplied reviewed entry") {
				t.Fatalf("committed retry was not refused: %v", err)
			}
			if stable := sqliteCorrectionCrashState(t, reopened, schema); !reflect.DeepEqual(recovered, stable) {
				t.Fatal("committed retry changed identity/data/counter/history")
			}
		})
	}
}

func sqliteKillCorrectionAtBoundary(t *testing.T, path, artifacts, boundary string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteDataCorrectionCrashChild$", "-test.timeout=50s")
	command.Env = append(os.Environ(), sqliteCorrectionCrashEnvironment+"=1", "GOLEM_SQLITE_CORRECTION_DATABASE="+path, "GOLEM_SQLITE_CORRECTION_ARTIFACTS="+artifacts, "GOLEM_SQLITE_CORRECTION_BOUNDARY="+boundary)
	command.ExtraFiles = []*os.File{write}
	// Child diagnostics go straight to the owning test; no unbounded capture or
	// persistent subprocess log is retained.
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		write.Close()
		t.Fatal(err)
	}
	write.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			<-done
		}
	}()
	ready := make(chan string, 1)
	go func() {
		content, _ := io.ReadAll(io.LimitReader(read, 128))
		ready <- strings.TrimSpace(string(content))
	}()
	select {
	case actual := <-ready:
		if actual != boundary {
			t.Fatalf("child failed to reach boundary: got %q want %q", actual, boundary)
		}
	case err := <-done:
		waited = true
		t.Fatalf("child exited before crash boundary: %v", err)
	case <-ctx.Done():
		t.Fatal("child boundary timed out")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = <-done
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ProcessState.Success() {
		t.Fatalf("child was not killed: %v", err)
	}
}

type sqliteCorrectionCrashRow struct {
	ID           int64
	Name         sql.NullString
	GolemVersion int64 `db:"golem_version"`
	Version      int64
}

type sqliteCorrectionCrashEvidence struct {
	Rows     []sqliteCorrectionCrashRow
	Children []int64
	Counter  int64
	Identity string
	Facts    []sqliteFactRow
	Ledger   []migration.LedgerEntry
}

func sqliteCorrectionCrashState(t *testing.T, database *sqlx.DB, schema physical.PhysicalSchema) sqliteCorrectionCrashEvidence {
	t.Helper()
	var result sqliteCorrectionCrashEvidence
	if err := database.Select(&result.Rows, `SELECT id,name,golem_version,version FROM items ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if err := database.Select(&result.Children, `SELECT item_id FROM children ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&result.Counter, `SELECT value FROM golem_row_version`); err != nil {
		t.Fatal(err)
	}
	if err := database.Select(&result.Facts, `SELECT * FROM _golem_outbox ORDER BY event_id`); err != nil {
		t.Fatal(err)
	}
	identity, err := New().ReadDataIncarnation(context.Background(), database, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	result.Identity = identity.String()
	result.Ledger, err = New().ReadLedger(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	var residue int
	if err := database.Get(&residue, `SELECT count(*) FROM sqlite_temp_schema WHERE name LIKE '_golem_data_correction_%'`); err != nil || residue != 0 {
		t.Fatalf("process recovery retained temporary work: %d %v", residue, err)
	}
	return result
}
