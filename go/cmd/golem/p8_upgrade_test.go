package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/migration"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestP8P7ToReleaseUpgradePostgreSQLProfiles(t *testing.T) {
	profiles := []struct {
		name, environment, fallback string
	}{
		{name: "postgresql-c", environment: "GOLEM_TEST_POSTGRES_DSN", fallback: "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable"},
		{name: "postgresql-linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", fallback: "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable"},
	}
	resolved := make([]string, len(profiles))
	for index, profile := range profiles {
		resolved[index] = strings.TrimSpace(os.Getenv(profile.environment))
		if resolved[index] == "" {
			resolved[index] = profile.fallback
		}
	}
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" && resolved[0] == resolved[1] {
		t.Fatal("mandatory PostgreSQL upgrade profiles must use distinct DSNs")
	}
	for index, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			ctx := context.Background()
			module := p8P7UpgradeModule(t)
			disposable := newDocumentationPostgreSQLDatabase(t, resolved[index])
			dsn := disposable.dataSourceName
			provider := internalpostgresql.New()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			p8InstallP7PostgreSQL(t, database, filepath.Join(module, "p8-corpus-migrations"))
			event := p8ReadFrozenEvent(t)
			p8SeedPostgreSQLUpgradeState(t, database, event)
			before := p8SnapshotPostgreSQLUpgradeState(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			p8RunGolem(t, module, "migration", "new", "--name", "p8_upgrade", "--schema", "./cmd/golem/testdata/social", "--migrations", "p8-corpus-migrations")
			for _, providerName := range []string{"postgresql", "sqlite"} {
				content, err := os.ReadFile(filepath.Join(module, "p8-corpus-migrations", providerName, "0002_p8_upgrade.sql"))
				if err != nil || len(content) != 0 {
					t.Fatalf("metadata-only %s migration bytes=%q err=%v", providerName, content, err)
				}
			}
			p8RunGolem(t, module, "generate", "--schema", "./cmd/golem/testdata/social", "--app-out", "./cmd/golem/testdata/social", "--migrations", "p8-corpus-migrations")
			p8RunGolem(t, module, "migration", "apply", "--provider", "postgresql", "--dsn", dsn, "--migrations", "p8-corpus-migrations")
			database, _, err = provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			after := p8SnapshotPostgreSQLUpgradeState(t, database)
			if !reflect.DeepEqual(before.Data, after.Data) || !reflect.DeepEqual(before.Event, after.Event) || before.FirstMigration != after.FirstMigration || after.MigrationCount != 2 || after.PendingCount != 1 {
				t.Fatalf("upgrade changed durable state: before=%#v after=%#v", before, after)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			p8WriteUpgradeRuntimeOracle(t, module)
			p8RunUpgradeRuntimeOracle(t, module, "postgresql", dsn)
		})
	}
}

func p8InstallP7PostgreSQL(t *testing.T, database interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, migrationRoot string) {
	t.Helper()
	ctx := context.Background()
	script, err := os.ReadFile(filepath.Join(migrationRoot, "postgresql", "0001_initial.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for index, statement := range strings.Split(string(script), ";\n") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("install P7 PostgreSQL statement %d: %v", index, err)
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(migrationRoot, "postgresql", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	history, err := migration.ParseManifest(manifestBytes)
	if err != nil || len(history.Entries) != 1 {
		t.Fatalf("decode P7 PostgreSQL history: entries=%d err=%v", len(history.Entries), err)
	}
	entry := history.Entries[0]
	files, err := json.Marshal(entry.Files)
	if err != nil {
		t.Fatal(err)
	}
	type phase struct {
		Ordinal          uint32                `json:"ordinal"`
		Status           migration.PhaseStatus `json:"status"`
		AfterFingerprint migration.Digest      `json:"afterFingerprint"`
	}
	phases := make([]phase, len(entry.Phases))
	for index, value := range entry.Phases {
		phases[index] = phase{Ordinal: value.Ordinal, Status: migration.PhaseApplied, AfterFingerprint: value.AfterFingerprint}
	}
	phaseBytes, err := json.Marshal(phases)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `INSERT INTO "_golem"."_golem_migrations" (migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8)`, string(entry.ID), string(entry.ParentChainHash), string(entry.ChainHash), files, string(entry.BeforePhysical), string(entry.AfterPhysical), phaseBytes, time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func p8SeedPostgreSQLUpgradeState(t *testing.T, database interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Rebind(string) string
}, event mutationfact.OutboxRow) {
	t.Helper()
	ctx := context.Background()
	exec := func(statement string, args ...any) {
		if _, err := database.ExecContext(ctx, database.Rebind(statement), args...); err != nil {
			t.Fatal(err)
		}
	}
	userID := "11111111-1111-4111-8111-111111111111"
	exec(`INSERT INTO users(id,handle,email,created_at) VALUES(?,?,?,?)`, userID, "p7-user", "p7@example.invalid", time.UnixMicro(1700000000000000).UTC())
	exec(`INSERT INTO posts(id,author_id,title,body,created_at) VALUES(?,?,?,?,?)`, "22222222-2222-4222-8222-222222222222", userID, "P7 title", "P7 body", time.UnixMicro(1700000001000000).UTC())
	exec(`INSERT INTO _golem._golem_outbox(event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.FactVersion, event.CodecIdentity, event.GenerationFingerprint, event.ModelID, event.Action, event.BeforeIdentity, event.AfterIdentity, event.CausationID, event.TransactionOrdinal, event.Metadata, event.DeleteSnapshot, event.RecordedAt)
	exec(`INSERT INTO _golem._golem_outbox_delivery(causation_id,status,first_recorded_at,attempt_count,available_at,updated_at) VALUES(?,?,?,?,?,?)`, event.CausationID, "pending", event.RecordedAt, 0, event.RecordedAt, event.RecordedAt)
}

func TestP8P7ToReleaseUpgradeSQLite(t *testing.T) {
	ctx := context.Background()
	module := p8P7UpgradeModule(t)
	dsn := "file:" + filepath.Join(module, "upgrade.sqlite")
	p8RunGolem(t, module, "migration", "apply", "--provider", "sqlite", "--dsn", dsn, "--migrations", "p8-corpus-migrations")

	provider := internalsqlite.New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	event := p8ReadFrozenEvent(t)
	userID := "11111111-1111-4111-8111-111111111111"
	postID := "22222222-2222-4222-8222-222222222222"
	if _, err := database.ExecContext(ctx, `INSERT INTO users(id,handle,email,created_at) VALUES(?,?,?,?)`, userID, "p7-user", "p7@example.invalid", int64(1700000000000000)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO posts(id,author_id,title,body,created_at) VALUES(?,?,?,?,?)`, postID, userID, "P7 title", "P7 body", int64(1700000001000000)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO _golem_outbox(event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.FactVersion, event.CodecIdentity, event.GenerationFingerprint, event.ModelID, event.Action, event.BeforeIdentity, event.AfterIdentity, event.CausationID, event.TransactionOrdinal, event.Metadata, event.DeleteSnapshot, event.RecordedAt.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	recorded := event.RecordedAt.UnixMicro()
	if _, err := database.ExecContext(ctx, `INSERT INTO _golem_outbox_delivery(causation_id,status,first_recorded_at,attempt_count,available_at,updated_at) VALUES(?,?,?,?,?,?)`, event.CausationID, "pending", recorded, 0, recorded, recorded); err != nil {
		t.Fatal(err)
	}
	before := p8SnapshotUpgradeState(t, database)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	p8RunGolem(t, module, "migration", "new", "--name", "p8_upgrade", "--schema", "./cmd/golem/testdata/social", "--migrations", "p8-corpus-migrations")
	for _, providerName := range []string{"postgresql", "sqlite"} {
		content, err := os.ReadFile(filepath.Join(module, "p8-corpus-migrations", providerName, "0002_p8_upgrade.sql"))
		if err != nil || len(content) != 0 {
			t.Fatalf("metadata-only %s migration bytes=%q err=%v", providerName, content, err)
		}
	}
	p8RunGolem(t, module, "generate", "--schema", "./cmd/golem/testdata/social", "--app-out", "./cmd/golem/testdata/social", "--migrations", "p8-corpus-migrations")
	p8RunGolem(t, module, "migration", "apply", "--provider", "sqlite", "--dsn", dsn, "--migrations", "p8-corpus-migrations")

	database, _, err = provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	after := p8SnapshotUpgradeState(t, database)
	if !reflect.DeepEqual(before.Data, after.Data) || !reflect.DeepEqual(before.Event, after.Event) || before.FirstMigration != after.FirstMigration || after.MigrationCount != 2 || after.PendingCount != 1 {
		t.Fatalf("upgrade changed durable state: before=%#v after=%#v", before, after)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	p8WriteUpgradeRuntimeOracle(t, module)
	p8RunUpgradeRuntimeOracle(t, module, "sqlite", dsn)
}

func p8WriteUpgradeRuntimeOracle(t *testing.T, module string) {
	t.Helper()
	const source = `package social

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestP8UpgradedRuntimeAuthorization(t *testing.T) {
	ctx := context.Background()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_UPGRADE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_UPGRADE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_UPGRADE_DSN")})
	default:
		t.Fatal("unknown upgrade provider")
	}
	if err != nil { t.Fatal(err) }
	defer database.Close()
	application, err := Open(ctx, Config[Actor]{
		Database: database,
		ResolvePrincipal: func(_ context.Context, actor Actor) (Actor, error) { return actor, nil },
	})
	if err != nil { t.Fatal(err) }
	ownerID, _ := golem.ParseUUID("11111111-1111-4111-8111-111111111111")
	postID, _ := golem.ParseUUID("22222222-2222-4222-8222-222222222222")
	owner, err := application.ForPrincipal(ctx, Actor{UserID: ownerID})
	if err != nil { t.Fatal(err) }
	row, err := owner.Posts.FindUnique(ctx, Posts.ByID.Value(postID), Posts.Select(Posts.ID, Posts.Title))
	if err != nil { t.Fatal(err) }
	gotID, idPresent := golem.Value(row, Posts.ID).Get()
	gotTitle, titlePresent := golem.Value(row, Posts.Title).Get()
	if !idPresent || !titlePresent || gotID != postID || gotTitle != "P7 title" {
		t.Fatalf("authorized persisted row changed: id=%v/%t title=%q/%t", gotID, idPresent, gotTitle, titlePresent)
	}
	strangerID, _ := golem.ParseUUID("33333333-3333-4333-8333-333333333333")
	stranger, err := application.ForPrincipal(ctx, Actor{UserID: strangerID})
	if err != nil { t.Fatal(err) }
	_, err = stranger.Posts.FindUnique(ctx, Posts.ByID.Value(postID), Posts.Select(Posts.ID))
	var publicError *golem.Error
	if !errors.As(err, &publicError) || publicError.Code != golem.CodeNotFound {
		t.Fatalf("unauthorized historical row error=%v", err)
	}
}
`
	path := filepath.Join(module, "cmd", "golem", "testdata", "social", "p8_upgrade_runtime_test.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func p8RunUpgradeRuntimeOracle(t *testing.T, module, providerName, dsn string) {
	t.Helper()
	command := exec.Command("go", "test", "-mod=mod", "./cmd/golem/testdata/social", "-run", "^TestP8UpgradedRuntimeAuthorization$", "-count=1")
	command.Dir = module
	command.Env = append(os.Environ(), "GOWORK=off", "P8_UPGRADE_PROVIDER="+providerName, "P8_UPGRADE_DSN="+dsn)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("upgraded generated runtime oracle failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}

type p8UpgradeSnapshot struct {
	Data           []string
	Event          mutationfact.OutboxRow
	FirstMigration string
	MigrationCount int
	PendingCount   int
}

func p8SnapshotUpgradeState(t *testing.T, database interface {
	SelectContext(context.Context, any, string, ...any) error
	GetContext(context.Context, any, string, ...any) error
}) p8UpgradeSnapshot {
	t.Helper()
	ctx := context.Background()
	var data []string
	if err := database.SelectContext(ctx, &data, `SELECT id || '|' || handle || '|' || email FROM users UNION ALL SELECT id || '|' || author_id || '|' || title || '|' || body FROM posts ORDER BY 1`); err != nil {
		t.Fatal(err)
	}
	var row struct {
		EventID               string `db:"event_id"`
		CodecIdentity         string `db:"codec_identity"`
		GenerationFingerprint string `db:"generation_fingerprint"`
		ModelID               string `db:"model_id"`
		Action                string `db:"action"`
		CausationID           string `db:"causation_id"`
		FactVersion           int64  `db:"fact_version"`
		TransactionOrdinal    int64  `db:"transaction_ordinal"`
		RecordedAt            int64  `db:"recorded_at"`
		BeforeIdentity        []byte `db:"before_identity"`
		AfterIdentity         []byte `db:"after_identity"`
		Metadata              []byte `db:"metadata"`
		DeleteSnapshot        []byte `db:"delete_snapshot"`
	}
	if err := database.GetContext(ctx, &row, `SELECT event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at FROM _golem_outbox`); err != nil {
		t.Fatal(err)
	}
	var migrations []string
	if err := database.SelectContext(ctx, &migrations, `SELECT migration_id || '|' || parent_chain_hash || '|' || chain_hash || '|' || file_checksums FROM _golem_migrations ORDER BY migration_id`); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := database.GetContext(ctx, &pending, `SELECT COUNT(*) FROM _golem_outbox_delivery WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	result := p8UpgradeSnapshot{Data: data, MigrationCount: len(migrations), PendingCount: pending}
	if len(migrations) != 0 {
		result.FirstMigration = migrations[0]
	}
	result.Event = mutationfact.OutboxRow{EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity, GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action, BeforeIdentity: row.BeforeIdentity, AfterIdentity: row.AfterIdentity, CausationID: row.CausationID, TransactionOrdinal: row.TransactionOrdinal, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot, RecordedAt: time.UnixMicro(row.RecordedAt).UTC()}
	return result
}

func p8SnapshotPostgreSQLUpgradeState(t *testing.T, database interface {
	SelectContext(context.Context, any, string, ...any) error
	GetContext(context.Context, any, string, ...any) error
}) p8UpgradeSnapshot {
	t.Helper()
	ctx := context.Background()
	var data []string
	if err := database.SelectContext(ctx, &data, `SELECT id::text || '|' || handle || '|' || email FROM users UNION ALL SELECT id::text || '|' || author_id::text || '|' || title || '|' || body FROM posts ORDER BY 1`); err != nil {
		t.Fatal(err)
	}
	var row struct {
		EventID               string    `db:"event_id"`
		CodecIdentity         string    `db:"codec_identity"`
		GenerationFingerprint string    `db:"generation_fingerprint"`
		ModelID               string    `db:"model_id"`
		Action                string    `db:"action"`
		CausationID           string    `db:"causation_id"`
		FactVersion           int64     `db:"fact_version"`
		TransactionOrdinal    int64     `db:"transaction_ordinal"`
		RecordedAt            time.Time `db:"recorded_at"`
		BeforeIdentity        []byte    `db:"before_identity"`
		AfterIdentity         []byte    `db:"after_identity"`
		Metadata              []byte    `db:"metadata"`
		DeleteSnapshot        []byte    `db:"delete_snapshot"`
	}
	if err := database.GetContext(ctx, &row, `SELECT event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at FROM _golem._golem_outbox`); err != nil {
		t.Fatal(err)
	}
	var migrations []string
	if err := database.SelectContext(ctx, &migrations, `SELECT migration_id || '|' || parent_chain_hash || '|' || chain_hash || '|' || file_checksums FROM _golem._golem_migrations ORDER BY migration_id`); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := database.GetContext(ctx, &pending, `SELECT COUNT(*) FROM _golem._golem_outbox_delivery WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	result := p8UpgradeSnapshot{Data: data, MigrationCount: len(migrations), PendingCount: pending}
	if len(migrations) != 0 {
		result.FirstMigration = migrations[0]
	}
	result.Event = mutationfact.OutboxRow{EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity, GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action, BeforeIdentity: row.BeforeIdentity, AfterIdentity: row.AfterIdentity, CausationID: row.CausationID, TransactionOrdinal: row.TransactionOrdinal, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot, RecordedAt: row.RecordedAt.UTC().Truncate(time.Microsecond)}
	return result
}

func p8ReadFrozenEvent(t *testing.T) mutationfact.OutboxRow {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(commandModuleRoot(t), "internal", "compatibility", "testdata", "p7", "event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		FormatVersion uint16                 `json:"formatVersion"`
		SourceCommit  string                 `json:"sourceCommit"`
		Row           mutationfact.OutboxRow `json:"row"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.FormatVersion != 1 || envelope.SourceCommit != "aafa3e965abe53deed887181b675d667574ea954" {
		t.Fatalf("decode P7 event corpus: %v", err)
	}
	return envelope.Row
}

func p8P7UpgradeModule(t *testing.T) string {
	t.Helper()
	root := commandModuleRoot(t)
	corpus := filepath.Join(root, "internal", "compatibility", "testdata", "p7")
	module := t.TempDir()
	app := filepath.Join(module, "cmd", "golem", "testdata", "social")
	p8CopyTree(t, filepath.Join(corpus, "source"), app)
	p8CopyTree(t, filepath.Join(corpus, "generated"), app, ".golem")
	p8CopyTree(t, filepath.Join(corpus, "generated", ".golem"), filepath.Join(module, ".golem"))
	p8CopyTree(t, filepath.Join(corpus, "p8-corpus-migrations"), filepath.Join(module, "p8-corpus-migrations"))
	goMod := fmt.Sprintf("module example.com/p8upgrade\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", filepath.ToSlash(root))
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	return module
}

func p8CopyTree(t *testing.T, source, target string, excludedRoots ...string) {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relativeErr := filepath.Rel(source, path)
		if relativeErr != nil {
			return relativeErr
		}
		first := strings.Split(filepath.ToSlash(relative), "/")[0]
		for _, excluded := range excludedRoots {
			if first == excluded {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		relative, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func p8RunGolem(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), directory, arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("golem %s failed code=%d diagnostic=%s", arguments[0], code, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes()
}
