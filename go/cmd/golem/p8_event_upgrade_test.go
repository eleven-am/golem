package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents(t *testing.T) {
	p8RunUpgradePreservesAuthorizationMigrationChainAndPendingEvents(t)
}

func p8RunUpgradePreservesAuthorizationMigrationChainAndPendingEvents(t *testing.T) {
	t.Helper()
	profiles := []struct {
		name, provider, dsn string
	}{
		{name: "sqlite", provider: "sqlite"},
		{name: "postgresql-c", provider: "postgresql", dsn: p8UpgradePostgreSQLDSN("GOLEM_TEST_POSTGRES_DSN", "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable")},
		{name: "postgresql-linguistic", provider: "postgresql", dsn: p8UpgradePostgreSQLDSN("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable")},
	}
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" && profiles[1].dsn == profiles[2].dsn {
		t.Fatal("mandatory PostgreSQL event-upgrade profiles must use distinct DSNs")
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			module := p8P7EventUpgradeModule(t)
			dsn := profile.dsn
			if profile.provider == "sqlite" {
				dsn = "file:" + filepath.Join(module, "event-upgrade.sqlite")
			} else {
				disposable := newDocumentationPostgreSQLDatabase(t, dsn)
				dsn = disposable.dataSourceName
			}
			migrationRoot := "internal/compatibility/testdata/p7-event/migrations"
			p8RunGolem(t, module, "migration", "apply", "--provider", profile.provider, "--dsn", dsn, "--migrations", migrationRoot)
			database := p8OpenEventUpgradeDatabase(t, profile.provider, dsn)
			frozen := p8ReadFrozenSubscribedEvent(t)
			p8SeedEventUpgradeState(t, database, profile.provider, frozen)
			before := p8SnapshotEventUpgradeState(t, database, profile.provider)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			p8RunMigrationNewArgumentsWithExactApprovals(t, module, []string{"migration", "new", "--name", "p8_upgrade", "--schema", "./internal/compatibility/testdata/p7-event/source", "--migrations", migrationRoot})
			for _, providerName := range []string{"sqlite", "postgresql"} {
				content, err := os.ReadFile(filepath.Join(module, migrationRoot, providerName, "0002_p8_upgrade.sql"))
				if err != nil {
					t.Fatal(err)
				}
				switch providerName {
				case "sqlite":
					if len(content) != 0 {
						t.Fatalf("SQLite event upgrade must be format-only: %q", content)
					}
				case "postgresql":
					p8AssertEventUpgradePostgreSQLRepresentationSQL(t, string(content))
				}
			}
			p8RunGolem(t, module, "generate", "--schema", "./internal/compatibility/testdata/p7-event/source", "--app-out", "./internal/compatibility/testdata/p7-event/source", "--migrations", migrationRoot)
			p8RunGolem(t, module, "migration", "apply", "--provider", profile.provider, "--dsn", dsn, "--migrations", migrationRoot)
			p8WriteEventUpgradeRuntimeOracle(t, module)
			p8RunEventUpgradeRuntimeOracle(t, module, profile.provider, dsn, frozen)

			database = p8OpenEventUpgradeDatabase(t, profile.provider, dsn)
			defer database.Close()
			after := p8SnapshotEventUpgradeState(t, database, profile.provider)
			if !reflect.DeepEqual(before.Data, after.Data) || !reflect.DeepEqual(before.Event, after.Event) || before.FirstMigration != after.FirstMigration || after.MigrationCount != 2 || after.DeliveryStatus != "delivered" {
				t.Fatalf("event upgrade changed durable identity/history: before=%#v after=%#v", before, after)
			}
		})
	}
}

func p8AssertEventUpgradePostgreSQLRepresentationSQL(t *testing.T, sql string) {
	t.Helper()
	const (
		table      = `"p7_event_posts"`
		column     = `"title"`
		constraint = `"ck_max_length_9df162382f90"`
	)
	statements := strings.Split(strings.TrimSuffix(strings.TrimSpace(sql), ";"), ";\n")
	if len(statements) != 2 {
		t.Fatalf("PostgreSQL event upgrade statements=%d want=2:\n%s", len(statements), sql)
	}
	want := []string{
		`ALTER TABLE "public".` + table + ` DROP CONSTRAINT ` + constraint,
		`ALTER TABLE "public".` + table + ` ALTER COLUMN ` + column + ` TYPE character varying(80) USING ` + column + `::character varying(80)`,
	}
	for index := range want {
		if statements[index] != want[index] {
			t.Fatalf("PostgreSQL event upgrade statement %d=%q want=%q", index, statements[index], want[index])
		}
	}
}

func p8UpgradePostgreSQLDSN(environment, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
		return value
	}
	return fallback
}

func p8P7EventUpgradeModule(t *testing.T) string {
	t.Helper()
	root := commandModuleRoot(t)
	corpus := filepath.Join(root, "internal", "compatibility", "testdata", "p7-event")
	module := t.TempDir()
	relative := filepath.Join("internal", "compatibility", "testdata", "p7-event")
	app := filepath.Join(module, relative, "source")
	p8CopyTree(t, filepath.Join(corpus, "source"), app)
	p8CopyTree(t, filepath.Join(corpus, "generated"), app, ".golem")
	p8CopyTree(t, filepath.Join(corpus, "generated", ".golem"), filepath.Join(module, ".golem"))
	p8CopyTree(t, filepath.Join(corpus, "migrations"), filepath.Join(module, relative, "migrations"))
	p8WriteHistoricalBundlePackage(t, filepath.Join(corpus, "generated", "zz_golem_registry.gen.go"), filepath.Join(module, relative, "historical"))
	goMod := fmt.Sprintf("module example.com/p8eventupgrade\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", filepath.ToSlash(root))
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	return module
}

func p8WriteHistoricalBundlePackage(t *testing.T, registryPath, target string) {
	t.Helper()
	content, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	digestStart := bytes.Index(content, []byte("func golemGeneratedGenerationDigest() golem.SchemaDigest {"))
	digestEnd := bytes.Index(content, []byte("\nfunc GolemGeneratedApplicationBindings"))
	bundleStart := bytes.Index(content, []byte("func GolemGeneratedSchemaBundle() golem.SchemaBundle {"))
	bundleEnd := bytes.Index(content, []byte("\nfunc GolemGeneratedApplicationDescriptors"))
	if digestStart < 0 || digestEnd <= digestStart || bundleStart < 0 || bundleEnd <= bundleStart {
		t.Fatal("frozen P7 event registry does not contain one extractable schema bundle")
	}
	function := append([]byte(nil), content[digestStart:digestEnd]...)
	function = append(function, '\n', '\n')
	function = append(function, content[bundleStart:bundleEnd]...)
	function = bytes.Replace(function, []byte("func GolemGeneratedSchemaBundle()"), []byte("func Bundle()"), 1)
	source := append([]byte("package historical\n\nimport golem \"github.com/eleven-am/golem/go/golem\"\n\n"), function...)
	source = append(source, '\n')
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "bundle.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
}

func p8OpenEventUpgradeDatabase(t *testing.T, providerName, dsn string) *sqlx.DB {
	t.Helper()
	var database *sqlx.DB
	var err error
	if providerName == "sqlite" {
		database, _, err = internalsqlite.New().Open(context.Background(), dsn)
	} else {
		database, _, err = internalpostgresql.New().Open(context.Background(), dsn)
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func p8ReadFrozenSubscribedEvent(t *testing.T) mutationfact.OutboxRow {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(commandModuleRoot(t), "internal", "compatibility", "testdata", "p7-event", "event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		FormatVersion uint16                 `json:"formatVersion"`
		SourceCommit  string                 `json:"sourceCommit"`
		Row           mutationfact.OutboxRow `json:"row"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.FormatVersion != 1 || envelope.SourceCommit != "aafa3e965abe53deed887181b675d667574ea954" {
		t.Fatalf("decode frozen subscribed event: %v", err)
	}
	return envelope.Row
}

func p8SeedEventUpgradeState(t *testing.T, database *sqlx.DB, providerName string, event mutationfact.OutboxRow) {
	t.Helper()
	ctx := context.Background()
	exec := func(statement string, arguments ...any) {
		if _, err := database.ExecContext(ctx, database.Rebind(statement), arguments...); err != nil {
			t.Fatal(err)
		}
	}
	recordedAt := any(event.RecordedAt.UnixMicro())
	if providerName == "postgresql" {
		recordedAt = event.RecordedAt
	}
	exec(`INSERT INTO p7_event_posts(id,owner_id,title) VALUES(?,?,?)`, "71000000-0000-4000-8000-000000000001", "71000000-0000-4000-8000-000000000002", "P7 pending event")
	prefix := ""
	if providerName == "postgresql" {
		prefix = `_golem.`
	}
	exec(`INSERT INTO `+prefix+`_golem_outbox(event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.FactVersion, event.CodecIdentity, event.GenerationFingerprint, event.ModelID, event.Action, event.BeforeIdentity, event.AfterIdentity, event.CausationID, event.TransactionOrdinal, event.Metadata, event.DeleteSnapshot, recordedAt)
	exec(`INSERT INTO `+prefix+`_golem_outbox_delivery(causation_id,status,first_recorded_at,attempt_count,available_at,updated_at) VALUES(?,?,?,?,?,?)`, event.CausationID, "pending", recordedAt, 0, recordedAt, recordedAt)
}

type p8EventUpgradeSnapshot struct {
	Data, FirstMigration, DeliveryStatus string
	Event                                mutationfact.OutboxRow
	MigrationCount                       int
}

func p8SnapshotEventUpgradeState(t *testing.T, database *sqlx.DB, providerName string) p8EventUpgradeSnapshot {
	t.Helper()
	ctx := context.Background()
	prefix := ""
	if providerName == "postgresql" {
		prefix = `_golem.`
	}
	var result p8EventUpgradeSnapshot
	dataQuery := `SELECT id || '|' || owner_id || '|' || title FROM p7_event_posts`
	if providerName == "postgresql" {
		dataQuery = `SELECT id::text || '|' || owner_id::text || '|' || title FROM p7_event_posts`
	}
	if err := database.GetContext(ctx, &result.Data, dataQuery); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		EventID, CodecIdentity, GenerationFingerprint, ModelID, Action, CausationID string
		FactVersion, TransactionOrdinal                                             int64
		RecordedAt                                                                  any
		BeforeIdentity, AfterIdentity, Metadata, DeleteSnapshot                     []byte
	}
	query := `SELECT event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at FROM ` + prefix + `_golem_outbox`
	rows, err := database.QueryxContext(ctx, query)
	if err != nil || !rows.Next() {
		t.Fatalf("read frozen upgraded event: %v", err)
	}
	if err := rows.Scan(&stored.EventID, &stored.FactVersion, &stored.CodecIdentity, &stored.GenerationFingerprint, &stored.ModelID, &stored.Action, &stored.BeforeIdentity, &stored.AfterIdentity, &stored.CausationID, &stored.TransactionOrdinal, &stored.Metadata, &stored.DeleteSnapshot, &stored.RecordedAt); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	recordedAt, ok := stored.RecordedAt.(time.Time)
	if !ok {
		micros, integerOK := stored.RecordedAt.(int64)
		if !integerOK {
			t.Fatalf("unexpected recorded_at type %T", stored.RecordedAt)
		}
		recordedAt = time.UnixMicro(micros)
	}
	result.Event = mutationfact.OutboxRow{EventID: stored.EventID, FactVersion: stored.FactVersion, CodecIdentity: stored.CodecIdentity, GenerationFingerprint: stored.GenerationFingerprint, ModelID: stored.ModelID, Action: stored.Action, BeforeIdentity: stored.BeforeIdentity, AfterIdentity: stored.AfterIdentity, CausationID: stored.CausationID, TransactionOrdinal: stored.TransactionOrdinal, Metadata: stored.Metadata, DeleteSnapshot: stored.DeleteSnapshot, RecordedAt: recordedAt.UTC().Truncate(time.Microsecond)}
	var migrations []string
	if err := database.SelectContext(ctx, &migrations, `SELECT migration_id || '|' || parent_chain_hash || '|' || chain_hash || '|' || file_checksums FROM `+prefix+`_golem_migrations ORDER BY migration_id`); err != nil {
		t.Fatal(err)
	}
	result.MigrationCount = len(migrations)
	if len(migrations) != 0 {
		result.FirstMigration = migrations[0]
	}
	if err := database.GetContext(ctx, &result.DeliveryStatus, database.Rebind(`SELECT status FROM `+prefix+`_golem_outbox_delivery WHERE causation_id=?`), result.Event.CausationID); err != nil {
		t.Fatal(err)
	}
	return result
}

func p8WriteEventUpgradeRuntimeOracle(t *testing.T, module string) {
	t.Helper()
	const source = `package p7event

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	historical "example.com/p8eventupgrade/internal/compatibility/testdata/p7-event/historical"
)

func TestP8UpgradedHistoricalEventRuntime(t *testing.T) {
	ctx := context.Background()
	var database *provider.Database
	var err error
	if os.Getenv("P8_UPGRADE_PROVIDER") == "sqlite" {
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_UPGRADE_DSN")})
	} else {
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_UPGRADE_DSN")})
	}
	if err != nil { t.Fatal(err) }
	defer database.Close()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	if err != nil { t.Fatal(err) }
	application, err := Open(ctx, Config[Actor]{
		Database: database, EventTransport: transport,
		HistoricalEventBundles: []golem.SchemaBundle{historical.Bundle()},
		ResolvePrincipal: func(_ context.Context, actor Actor) (Actor, error) { return actor, nil },
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
	})
	if err != nil { t.Fatal(err) }
	postID, _ := golem.ParseUUID("71000000-0000-4000-8000-000000000001")
	ownerID, _ := golem.ParseUUID("71000000-0000-4000-8000-000000000002")
	owner, err := application.ForPrincipal(ctx, Actor{UserID: ownerID})
	if err != nil { t.Fatal(err) }
	row, err := owner.Posts.FindUnique(ctx, Posts.ByID.Value(postID), Posts.Select(Posts.ID, Posts.Title))
	if err != nil { t.Fatal(err) }
	gotID, idPresent := golem.Value(row, Posts.ID).Get()
	gotTitle, titlePresent := golem.Value(row, Posts.Title).Get()
	if !idPresent || !titlePresent || gotID != postID || gotTitle != "P7 pending event" { t.Fatal("authorized P7 event row changed") }
	strangerID, _ := golem.ParseUUID("71000000-0000-4000-8000-000000000003")
	stranger, err := application.ForPrincipal(ctx, Actor{UserID: strangerID})
	if err != nil { t.Fatal(err) }
	_, err = stranger.Posts.FindUnique(ctx, Posts.ByID.Value(postID), Posts.Select(Posts.ID))
	var publicError *golem.Error
	if !errors.As(err, &publicError) || publicError.Code != golem.CodeNotFound { t.Fatalf("historical authorization error=%v", err) }
	stream, err := owner.Posts.Events(ctx, golem.EventWhere(Posts.ID.Eq(postID)), golem.EventSelect[Post](Posts.ID, Posts.Title))
	if err != nil { t.Fatal(err) }
	defer stream.Close()
	publisherContext, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	receiveContext, stopReceive := context.WithTimeout(ctx, 10*time.Second)
	event, err := stream.Recv(receiveContext)
	stopReceive()
	stopPublisher()
	select { case <-publisherDone: case <-time.After(3*time.Second): t.Fatal("historical publisher did not stop") }
	if err != nil { t.Fatal(err) }
	expectedEvent, _ := golem.ParseUUID(os.Getenv("P8_EXPECTED_EVENT_ID"))
	expectedCausation, _ := golem.ParseUUID(os.Getenv("P8_EXPECTED_CAUSATION_ID"))
	metadata := event.Metadata()
	if metadata.EventID() != golem.EventID(expectedEvent) || metadata.CausationID() != golem.CausationID(expectedCausation) || metadata.TransactionOrdinal() != 1 || metadata.Action() != golem.EventCreated || event.ID() != postID {
		t.Fatalf("historical event identity changed: %#v", metadata)
	}
	entity, present := event.Entity()
	if !present { t.Fatal("authorized historical event lost entity") }
	entityTitle, titlePresent := golem.Value(entity, Posts.Title).Get()
	if !titlePresent || entityTitle != "P7 pending event" { t.Fatal("historical event payload changed") }
}
`
	path := filepath.Join(module, "internal", "compatibility", "testdata", "p7-event", "source", "p8_upgrade_runtime_test.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func p8RunEventUpgradeRuntimeOracle(t *testing.T, module, providerName, dsn string, event mutationfact.OutboxRow) {
	t.Helper()
	command := exec.Command("go", "test", "-mod=mod", "./internal/compatibility/testdata/p7-event/source", "-run", "^TestP8UpgradedHistoricalEventRuntime$", "-count=1")
	command.Dir = module
	command.Env = append(os.Environ(), "GOWORK=off", "P8_UPGRADE_PROVIDER="+providerName, "P8_UPGRADE_DSN="+dsn, "P8_EXPECTED_EVENT_ID="+event.EventID, "P8_EXPECTED_CAUSATION_ID="+event.CausationID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("upgraded historical event runtime failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}
