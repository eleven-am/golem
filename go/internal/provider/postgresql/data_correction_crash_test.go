package postgresql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/testenv"
)

const postgresqlCorrectionCrashFixtureVariable = "GOLEM_P09_PG_CORRECTION_CRASH_FILE"

type postgresqlCorrectionCrashFixture struct {
	DSN      string `json:"dsn"`
	Boundary string `json:"boundary"`
}

type postgresqlCorrectionCrashRow struct {
	ID         int64  `db:"id"`
	Title      string `db:"title"`
	RowVersion int64  `db:"golem_version"`
	Version    int64  `db:"version"`
}

func TestPostgreSQLDataCorrectionProcessCrashBoundariesRecoverExactlyOnce(t *testing.T) {
	// Follow the existing provider child-test pattern, but exit from the actual
	// migration hook: os.Exit skips Tx rollback and all other cleanup defers.
	if path := os.Getenv(postgresqlCorrectionCrashFixtureVariable); path != "" {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(io.LimitReader(file, 8193))
		closeErr := file.Close()
		if err != nil || closeErr != nil || len(content) > 8192 {
			t.Fatal("invalid bounded correction crash fixture")
		}
		var fixture postgresqlCorrectionCrashFixture
		if err := json.Unmarshal(content, &fixture); err != nil {
			t.Fatal("invalid correction crash fixture")
		}
		if fixture.DSN == "" {
			t.Fatal("correction crash fixture has no disposable DSN")
		}
		switch fixture.Boundary {
		case "inside_transaction_before_ledger", "inside_transaction_before_commit", "after_phase_commit":
		default:
			t.Fatal("unsupported correction crash boundary")
		}
		_, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		database, _, err := New().Open(ctx, fixture.DSN)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		fault := migrationfailpoint.WithHook(ctx, func(boundary string) {
			if boundary == fixture.Boundary {
				os.Exit(17)
			}
		})
		if err := New().ApplyMigration(fault, database, manifest, files); err != nil {
			t.Fatalf("child correction failed before requested boundary: %v", err)
		}
		t.Fatal("child correction did not reach its crash boundary")
	}
	for _, boundary := range []string{"inside_transaction_before_ledger", "inside_transaction_before_commit", "after_phase_commit"} {
		t.Run(boundary, func(t *testing.T) {
			schema, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`)
			// Only this existing guarded owner creates/selects a disposable database.
			// The child receives this exact DSN; it cannot fall back to a local server.
			dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			provider := New()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,'old',10,4),(2,'fixed',11,7)`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`); err != nil {
				t.Fatal(err)
			}
			beforeIdentity, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			readRows := func() []postgresqlCorrectionCrashRow {
				t.Helper()
				var rows []postgresqlCorrectionCrashRow
				if err := database.SelectContext(ctx, &rows, `SELECT id,title,golem_version,version FROM correction_gate.articles ORDER BY id`); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			beforeRows := readRows()
			beforeLedger, err := provider.ReadLedger(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			fixturePath := filepath.Join(t.TempDir(), "child.json")
			content, err := json.Marshal(postgresqlCorrectionCrashFixture{DSN: dsn, Boundary: boundary})
			if err != nil {
				t.Fatal(err)
			}
			if len(content) > 8192 {
				t.Fatal("crash fixture exceeds bound")
			}
			if err := os.WriteFile(fixturePath, content, 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPostgreSQLDataCorrectionProcessCrashBoundariesRecoverExactlyOnce$", "-test.count=1", "-test.timeout=25s")
			command.Env = append(os.Environ(), postgresqlCorrectionCrashFixtureVariable+"="+fixturePath)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 17 {
				t.Fatalf("correction child did not abruptly exit at %s: %v\n%s", boundary, err, output)
			}
			interruptedRows := readRows()
			interruptedIdentity, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			interruptedLedger, err := provider.ReadLedger(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			if boundary != "after_phase_commit" {
				if !reflect.DeepEqual(interruptedRows, beforeRows) || interruptedIdentity != beforeIdentity || !reflect.DeepEqual(interruptedLedger, beforeLedger) {
					t.Fatalf("precommit process death changed durable state: rows=%+v identityChanged=%v ledger=%v", interruptedRows, interruptedIdentity != beforeIdentity, interruptedLedger)
				}
				// The dead backend releases the owning advisory/table locks; this bounded
				// retry must then execute the same immutable correction exactly once.
				if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
					t.Fatalf("retry after process death: %v", err)
				}
			} else if len(interruptedLedger) != 2 || interruptedIdentity == beforeIdentity {
				t.Fatal("postcommit process death lost committed ledger or identity")
			}
			committedRows := readRows()
			committedIdentity, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			committedLedger, err := provider.ReadLedger(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			if len(committedRows) != 2 || committedRows[0].ID != 1 || committedRows[0].Title != "fixed" || committedRows[0].RowVersion <= 11 || committedRows[0].Version != 5 || committedRows[1] != beforeRows[1] || committedIdentity == beforeIdentity || len(committedLedger) != 2 {
				t.Fatalf("recovered correction state invalid: rows=%+v ledger=%v", committedRows, committedLedger)
			}
			// PostgreSQL nextval gaps from killed transactions are permitted. Check the
			// committed monotone stamps rather than pretending sequence draws roll back.
			if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil || !strings.Contains(err.Error(), "no unapplied reviewed entry") {
				t.Fatalf("committed correction replay was not refused: %v", err)
			}
			afterRetryIdentity, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			afterRetryLedger, err := provider.ReadLedger(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(readRows(), committedRows) || afterRetryIdentity != committedIdentity || !reflect.DeepEqual(afterRetryLedger, committedLedger) {
				t.Fatal("retry reexecuted correction or rotated its committed identity")
			}
			if _, err := provider.Introspect(ctx, database, schema); err != nil {
				t.Fatalf("process recovery retained capture objects or catalog drift: %v", err)
			}
		})
	}
}
