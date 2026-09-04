package registry

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestGeneratedSystemEscapeSurfaceIsExactAndDocumented(t *testing.T) {
	source := string(buildReadSurfaceArtifacts(t).registry)
	for _, fragment := range []string{
		"func SystemEscape[P any](transaction *CallerTx[P]) *SystemTx[P] {",
		"golemruntime.CallerTxSystem(transaction.runtime)",
		"result.Users = SystemTxUserClient[P]{runtime: inner}",
		"// SystemEscape leaves the authorized path.",
		"// Nothing performed through them is policy-checked.",
		"// is observed as transaction.system_escape.",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated registry missing %q", fragment)
		}
	}
	// The bootstrap shell type-checks declaration sources against the caller ABI
	// and cannot name a system type, so the escape must never become a CallerTx
	// method. TestEmitShellMatchesFinalCallerABI fails the moment it does.
	if strings.Contains(source, "func (transaction *CallerTx[P]) System(") {
		t.Error("system escape was emitted as a CallerTx method the bootstrap shell cannot mirror")
	}
}

func TestGeneratedSystemEscapeBypassesPolicyInsideTheCallerTransaction(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "generated-system-escape.db")
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyMigration(ctx, database, artifacts.sqliteManifest, artifacts.sqliteFiles); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	files := cloneSourceFiles(artifacts.files)
	files["generated/"+Filename] = string(artifacts.registry)
	files["acceptance/system_escape_test.go"] = fmt.Sprintf(`package acceptance_test

import (
	"context"
	"errors"
	"testing"

	"example.test/app/generated"
	"example.test/app/models"
	"example.test/app/security"
	"github.com/eleven-am/golem/go/golem"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestSystemEscapeBypassesPolicyAndSharesTheCallerTransaction(t *testing.T) {
	ctx := context.Background()
	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: %q})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = database.Close() })
	application, err := generated.Open(ctx, generated.Config[string]{
		Database: database,
		ResolvePrincipal: func(context.Context, string) (security.Actor, error) {
			return security.Actor{Prefix: "a"}, nil
		},
	})
	if err != nil { t.Fatal(err) }
	caller, err := application.ForPrincipal(ctx, "principal")
	if err != nil { t.Fatal(err) }
	escapedID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000031")
	if err != nil { t.Fatal(err) }
	rollback := errors.New("rollback")

	if err := caller.Transaction(ctx, func(tx *generated.CallerTx[string]) error {
		authorized, err := tx.Users.Count(ctx)
		if err != nil || authorized != 1 { t.Fatalf("caller count=%%d err=%%v", authorized, err) }
		escape := generated.SystemEscape(tx)
		unrestricted, err := escape.Users.Count(ctx)
		if err != nil || unrestricted != 2 { t.Fatalf("escape count=%%d err=%%v", unrestricted, err) }
		if _, err := escape.Users.Create(ctx, models.Users.Create(models.Users.ID.Create(escapedID), models.Users.Name.Create("zulu"))); err != nil {
			t.Fatalf("escape create: %%v", err)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("transaction err=%%v want rollback", err)
	}

	system := application.System()
	remaining, err := system.Users.Count(ctx)
	if err != nil || remaining != 2 { t.Fatalf("rolled-back count=%%d err=%%v", remaining, err) }

	if err := caller.Transaction(ctx, func(tx *generated.CallerTx[string]) error {
		_, err := generated.SystemEscape(tx).Users.Create(ctx, models.Users.Create(models.Users.ID.Create(escapedID), models.Users.Name.Create("zulu")))
		return err
	}); err != nil { t.Fatal(err) }
	committed, err := system.Users.Count(ctx)
	if err != nil || committed != 3 { t.Fatalf("committed count=%%d err=%%v", committed, err) }
	visible, err := caller.Users.Count(ctx)
	if err != nil || visible != 1 { t.Fatalf("caller visible count=%%d err=%%v", visible, err) }
}
`, "file:"+databasePath)
	runFreshReadSurfaceModule(t, files, false, nil)
}
