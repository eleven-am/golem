package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	publicprovider "github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jmoiron/sqlx"
)

func TestP8VersionHumanAndJSONGolden(t *testing.T) {
	previousVersion, previousCommit := buildVersion, buildCommit
	buildVersion = "v1.2.3"
	buildCommit = "0123456789abcdef0123456789abcdef01234567"
	t.Cleanup(func() {
		buildVersion, buildCommit = previousVersion, previousCommit
	})

	const commit = "0123456789abcdef0123456789abcdef01234567"
	humanGolden := "golem v1.2.3 (commit " + commit + ")\n"
	jsonGolden := fmt.Sprintf(`{
  "formatVersion": 1,
  "module": %q,
  "version": "v1.2.3",
  "commit": %q,
  "generatorABI": %q,
  "runtimeABI": %q
}
`, diagnosticsModule, commit, manifest.GeneratorVersion, manifest.TemplateABIVersion)

	for _, format := range []struct {
		name string
		args []string
		want string
	}{
		{name: "human", args: []string{"version"}, want: humanGolden},
		{name: "json", args: []string{"version", "--json"}, want: jsonGolden},
	} {
		t.Run(format.name, func(t *testing.T) {
			for iteration := 0; iteration < 2; iteration++ {
				var stdout, stderr bytes.Buffer
				if code := run(context.Background(), t.TempDir(), format.args, &stdout, &stderr); code != 0 {
					t.Fatalf("iteration %d code=%d stderr=%s", iteration, code, stderr.String())
				}
				if stderr.Len() != 0 || stdout.String() != format.want {
					t.Fatalf("iteration %d stdout=%q stderr=%q; want stdout=%q", iteration, stdout.String(), stderr.String(), format.want)
				}
			}
		})
	}
}

func TestVersionNormalizesUntrustedBuildMetadata(t *testing.T) {
	previousVersion, previousCommit := buildVersion, buildCommit
	buildVersion = "secret-release-value"
	buildCommit = "secret-commit-value"
	t.Cleanup(func() {
		buildVersion, buildCommit = previousVersion, previousCommit
	})

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), t.TempDir(), []string{"version", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret") {
		t.Fatalf("version disclosed untrusted build metadata: %s", stdout.String())
	}
	var output versionOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Version != "devel" || output.Commit != unknownCommit {
		t.Fatalf("normalized version = %#v", output)
	}
}

func TestVersionModuleReleaseAllowsUnknownRevision(t *testing.T) {
	previousVersion, previousCommit := buildVersion, buildCommit
	buildVersion = "v1.2.3"
	buildCommit = unknownCommit
	t.Cleanup(func() {
		buildVersion, buildCommit = previousVersion, previousCommit
	})

	if output := currentVersion(); output.Version != "v1.2.3" || output.Commit != unknownCommit {
		t.Fatalf("module release provenance = %#v", output)
	}
}

func TestVersionUsesModuleBuildInfoWithoutVCSRevision(t *testing.T) {
	previousVersion, previousCommit, previousRead := buildVersion, buildCommit, readBuildInfo
	buildVersion, buildCommit = "", ""
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Path: diagnosticsModule, Version: "v2.3.4"}}, true
	}
	t.Cleanup(func() {
		buildVersion, buildCommit, readBuildInfo = previousVersion, previousCommit, previousRead
	})
	if output := currentVersion(); output.Version != "v2.3.4" || output.Commit != unknownCommit {
		t.Fatalf("module-cache provenance = %#v", output)
	}
}

func TestVersionSubprocessLinkerProvenance(t *testing.T) {
	root := commandModuleRoot(t)
	binary := filepath.Join(t.TempDir(), "golem")
	commit := "0123456789abcdef0123456789abcdef01234567"
	linker := "-X main.buildVersion=v1.2.3 -X main.buildCommit=" + commit
	command := exec.Command("go", "build", "-o", binary, "-ldflags", linker, "./cmd/golem")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build version subprocess: %v\n%s", err, output)
	}
	encoded, err := exec.Command(binary, "version", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("run version subprocess: %v\n%s", err, encoded)
	}
	var output versionOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output.Version != "v1.2.3" || output.Commit != commit || output.Module != diagnosticsModule || output.GeneratorABI == "" || output.RuntimeABI == "" {
		t.Fatalf("subprocess version = %#v", output)
	}
}

func TestP8DoctorIsReadOnlyAndUsesPublicProviderLifecycle(t *testing.T) {
	module := writeSingleProviderModule(t)
	databasePath := filepath.Join(t.TempDir(), "read-only.db")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration new code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "apply", "--provider", "sqlite", "--dsn", databasePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration apply code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName: "file:" + databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().Exec(`INSERT INTO "users" ("id") VALUES (8675309)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, module)
	databaseBefore := snapshotSQLiteDoctorState(t, databasePath)
	fileBefore, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	originalOpen := openDoctorDatabase
	var observed []*publicprovider.Database
	openDoctorDatabase = func(ctx context.Context, provider ir.Provider, dsn string) (*publicprovider.Database, error) {
		opened, openErr := originalOpen(ctx, provider, dsn)
		if opened != nil {
			observed = append(observed, opened)
		}
		return opened, openErr
	}
	t.Cleanup(func() { openDoctorDatabase = originalOpen })

	done := make(chan struct{})
	found := make(chan string, 1)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				matches, _ := filepath.Glob(filepath.Join(module, ".golem-prospective-*.mod"))
				if len(matches) != 0 {
					select {
					case found <- matches[0]:
					default:
					}
					return
				}
			}
		}
	}()
	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), module, []string{"doctor", "--provider", "sqlite", "--dsn", databasePath, "--json"}, &stdout, &stderr)
	close(done)
	wait.Wait()
	if code != 0 {
		t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	select {
	case name := <-found:
		t.Fatalf("doctor created prospective module file %s", name)
	default:
	}
	if after := treeSnapshot(t, module); !reflect.DeepEqual(before, after) {
		t.Fatal("doctor modified the application module")
	}
	if after := snapshotSQLiteDoctorState(t, databasePath); !reflect.DeepEqual(databaseBefore, after) {
		t.Fatalf("doctor modified catalog, ledger, or user data:\nbefore=%#v\nafter=%#v", databaseBefore, after)
	}
	fileAfter, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(fileBefore, fileAfter) {
		t.Fatalf("doctor changed SQLite bytes: err=%v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("doctor public provider opens=%d want 1", len(observed))
	}
	if observed[0].Provider() != "sqlite" || observed[0].UnsafeSQLX() != nil {
		t.Fatalf("doctor did not close its public provider handle: provider=%q pool=%p", observed[0].Provider(), observed[0].UnsafeSQLX())
	}
}

func TestP8DoctorStateMatrixBothProviders(t *testing.T) {
	module := writeSingleProviderModule(t)
	canary := "doctor-secret-host-user-password"
	database := filepath.Join(t.TempDir(), canary+".db")
	if err := os.WriteFile(database, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	doctorArgs := []string{"doctor", "--provider", "sqlite", "--dsn", database, "--json"}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, doctorArgs, &stdout, &stderr); code != 1 {
		t.Fatalf("incomplete doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var incomplete doctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &incomplete); err != nil || incomplete.Capabilities != "pass" || incomplete.History != "incomplete" || incomplete.Schema != "drift" || incomplete.Generation != "incompatible" {
		t.Fatalf("incomplete doctor=%#v err=%v", incomplete, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"doctor", "--provider", "sqlite", "--dsn", missing, "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unreachable doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var unreachable doctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &unreachable); err != nil || unreachable.Capabilities != "fail" || unreachable.Schema != "unreachable" {
		t.Fatalf("unreachable doctor=%#v err=%v", unreachable, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("unreachable doctor created target: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration new code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	databaseBeforeDoctor, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	moduleBeforeDoctor := treeSnapshot(t, module)
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, doctorArgs, &stdout, &stderr); code != 1 {
		t.Fatalf("pending doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var pending doctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Capabilities != "pass" || pending.History != "pending" || pending.Schema != "drift" || pending.Generation != "incompatible" {
		t.Fatalf("pending doctor = %#v", pending)
	}
	if strings.Contains(stdout.String(), canary) || strings.Contains(stderr.String(), canary) {
		t.Fatalf("doctor disclosed DSN canary: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if moduleAfterDoctor := treeSnapshot(t, module); !reflect.DeepEqual(moduleBeforeDoctor, moduleAfterDoctor) {
		t.Fatal("doctor modified the application module")
	}
	databaseAfterDoctor, err := os.ReadFile(database)
	if err != nil || !bytes.Equal(databaseBeforeDoctor, databaseAfterDoctor) {
		t.Fatalf("doctor modified the existing SQLite database: err=%v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, doctorArgs, &stdout, &stderr); code != 1 {
		t.Fatalf("generated pending doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &pending); err != nil || pending.Generation != "current" || pending.History != "pending" || pending.Schema != "drift" {
		t.Fatalf("generated pending doctor=%#v err=%v", pending, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "apply", "--provider", "sqlite", "--dsn", database}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration apply after doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var applied migrationApplyOutput
	if err := json.Unmarshal(stdout.Bytes(), &applied); err != nil || len(applied.Applied) != 1 || applied.Applied[0] != "0001_initial" {
		t.Fatalf("doctor changed the live ledger before explicit apply: output=%#v err=%v", applied, err)
	}

	var first, second, firstErr, secondErr bytes.Buffer
	if code := run(context.Background(), module, doctorArgs, &first, &firstErr); code != 0 {
		t.Fatalf("current doctor code=%d stdout=%s stderr=%s", code, first.String(), firstErr.String())
	}
	if code := run(context.Background(), module, doctorArgs, &second, &secondErr); code != 0 {
		t.Fatalf("repeat doctor code=%d stdout=%s stderr=%s", code, second.String(), secondErr.String())
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("doctor output changed across identical runs")
	}
	var current doctorOutput
	if err := json.Unmarshal(first.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Capabilities != "pass" || current.History != "current" || current.Schema != "current" || current.Generation != "current" {
		t.Fatalf("current doctor = %#v", current)
	}
	if !reflect.DeepEqual(current.Diagnostics, secondDoctorDiagnostics(t, second.Bytes())) {
		t.Fatal("doctor diagnostic sequence is not deterministic")
	}
	if strings.Contains(first.String(), canary) || strings.Contains(firstErr.String(), canary) {
		t.Fatalf("doctor disclosed DSN canary: stdout=%s stderr=%s", first.String(), firstErr.String())
	}

	manifestBytes, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(manifest.DefaultPath)))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := manifest.Parse(manifestBytes)
	if err != nil || len(generated.Artifacts) == 0 {
		t.Fatalf("parse generated manifest: %v", err)
	}
	target := filepath.Join(module, filepath.FromSlash(generated.Artifacts[0].Path))
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(content, []byte("\n// tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	var tamperedOut, tamperedErr bytes.Buffer
	if code := run(context.Background(), module, doctorArgs, &tamperedOut, &tamperedErr); code != 1 {
		t.Fatalf("tampered doctor code=%d stdout=%s stderr=%s", code, tamperedOut.String(), tamperedErr.String())
	}
	var tampered doctorOutput
	if err := json.Unmarshal(tamperedOut.Bytes(), &tampered); err != nil || tampered.Generation != "incompatible" {
		t.Fatalf("tampered generation doctor=%#v err=%v", tampered, err)
	}

	// Corrupt only the provider history after every other SQLite state has been
	// observed. Doctor must classify immutable-history corruption as invalid; it
	// must not attempt to repair or reinterpret it.
	providerManifest := filepath.Join(module, "migrations", "sqlite", "manifest.json")
	if err := os.WriteFile(providerManifest, []byte("{not-a-valid-history"), 0o644); err != nil {
		t.Fatal(err)
	}
	var invalidOut, invalidErr bytes.Buffer
	if code := run(context.Background(), module, doctorArgs, &invalidOut, &invalidErr); code != 1 {
		t.Fatalf("invalid-history doctor code=%d stdout=%s stderr=%s", code, invalidOut.String(), invalidErr.String())
	}
	var invalid doctorOutput
	if err := json.Unmarshal(invalidOut.Bytes(), &invalid); err != nil || invalid.History != "invalid" {
		t.Fatalf("invalid-history doctor=%#v err=%v", invalid, err)
	}

	// The named evidence gate includes every configured PostgreSQL profile. Each
	// configured profile receives a collision-resistant disposable database so
	// its public and _golem schemas can exercise the same exact state machine
	// without touching a shared application database.
	for _, profile := range []struct {
		name string
		env  string
	}{
		{name: "postgresql-c", env: "GOLEM_TEST_POSTGRES_DSN"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is not configured", profile.env)
			}
			exerciseP8DoctorPostgreSQLMatrix(t, dsn)
		})
	}
}

func exerciseP8DoctorPostgreSQLMatrix(t *testing.T, administrativeDSN string) {
	t.Helper()
	missingName := p8PostgreSQLDatabaseName()
	missingDSN, err := postgreSQLDSNForDatabase(administrativeDSN, missingName)
	if err != nil {
		t.Fatal(err)
	}
	module := writePostgreSQLProviderModule(t)
	assertDoctorState(t, module, "postgresql", missingDSN, doctorState{capabilities: "fail", history: "incomplete", schema: "unreachable", generation: "incompatible"})

	databaseDSN := createP8PostgreSQLDatabase(t, administrativeDSN)
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "incomplete", schema: "drift", generation: "incompatible"})

	runP8Command(t, module, "migration", "new", "--name", "initial")
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "pending", schema: "drift", generation: "incompatible"})
	runP8Command(t, module, "generate", "--app-out", "./app")
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "pending", schema: "drift", generation: "current"})
	runP8Command(t, module, "migration", "apply", "--provider", "postgresql", "--dsn", databaseDSN)

	database, err := openDoctorDatabasePublic(context.Background(), ir.PostgreSQL, databaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `INSERT INTO "public"."users" ("id") VALUES (8675309)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	moduleBefore := treeSnapshot(t, module)
	currentBefore := snapshotPostgreSQLDoctorState(t, databaseDSN)
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "current", schema: "current", generation: "current"})
	if moduleAfter := treeSnapshot(t, module); !reflect.DeepEqual(moduleBefore, moduleAfter) {
		t.Fatal("current PostgreSQL doctor modified the application module")
	}
	if currentAfter := snapshotPostgreSQLDoctorState(t, databaseDSN); !reflect.DeepEqual(currentBefore, currentAfter) {
		t.Fatalf("current PostgreSQL doctor modified catalog, ledger, or user data:\nbefore=%#v\nafter=%#v", currentBefore, currentAfter)
	}

	database, err = openDoctorDatabasePublic(context.Background(), ir.PostgreSQL, databaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `ALTER TABLE "public"."users" ADD COLUMN "p8_doctor_drift" text`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	driftBefore := snapshotPostgreSQLDoctorState(t, databaseDSN)
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "current", schema: "drift", generation: "current"})
	if driftAfter := snapshotPostgreSQLDoctorState(t, databaseDSN); !reflect.DeepEqual(driftBefore, driftAfter) {
		t.Fatalf("drift PostgreSQL doctor modified catalog, ledger, or user data:\nbefore=%#v\nafter=%#v", driftBefore, driftAfter)
	}

	database, err = openDoctorDatabasePublic(context.Background(), ir.PostgreSQL, databaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `UPDATE "_golem"."_golem_migrations" SET "chain_hash" = '0000000000000000000000000000000000000000000000000000000000000000'`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	invalidBefore := snapshotPostgreSQLDoctorState(t, databaseDSN)
	assertDoctorState(t, module, "postgresql", databaseDSN, doctorState{capabilities: "pass", history: "invalid", schema: "drift", generation: "current"})
	if invalidAfter := snapshotPostgreSQLDoctorState(t, databaseDSN); !reflect.DeepEqual(invalidBefore, invalidAfter) {
		t.Fatalf("invalid-ledger PostgreSQL doctor modified catalog, ledger, or user data:\nbefore=%#v\nafter=%#v", invalidBefore, invalidAfter)
	}
}

type doctorState struct {
	capabilities string
	history      string
	schema       string
	generation   string
}

func assertDoctorState(t *testing.T, module, provider, dsn string, want doctorState) doctorOutput {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), module, []string{"doctor", "--provider", provider, "--dsn", dsn, "--json"}, &stdout, &stderr)
	if want.capabilities == "pass" && want.history == "current" && want.schema == "current" && want.generation == "current" {
		if code != 0 {
			t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	} else if code != 1 {
		t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var output doctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Provider != provider || output.Capabilities != want.capabilities || output.History != want.history || output.Schema != want.schema || output.Generation != want.generation {
		t.Fatalf("doctor state=%#v want=%#v", output, want)
	}
	if strings.Contains(stdout.String(), dsn) || strings.Contains(stderr.String(), dsn) {
		t.Fatal("doctor disclosed its DSN")
	}
	return output
}

func runP8Command(t *testing.T, module string, arguments ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("golem %s code=%d stdout=%s stderr=%s", strings.Join(arguments, " "), code, stdout.String(), stderr.String())
	}
}

func writePostgreSQLProviderModule(t *testing.T) string {
	t.Helper()
	module := writeSocialModule(t, false)
	filename := filepath.Join(module, "schema.go")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte("golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"), []byte("golem.Providers(schema, golem.PostgreSQL)"), 1)
	if err := os.WriteFile(filename, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return module
}

var p8PostgreSQLDatabaseNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var p8PostgreSQLDBNameParameterPattern = regexp.MustCompile(`(?i)(^|[ \t\r\n])dbname[ \t\r\n]*=[ \t\r\n]*(?:'(?:\\.|[^'])*'|(?:\\.|[^ \t\r\n])*)`)

func p8PostgreSQLDatabaseName() string {
	return fmt.Sprintf("golem_p8_%d_%d", os.Getpid(), time.Now().UnixNano())
}

func createP8PostgreSQLDatabase(t *testing.T, administrativeDSN string) string {
	t.Helper()
	name := p8PostgreSQLDatabaseName()
	if !p8PostgreSQLDatabaseNamePattern.MatchString(name) {
		t.Fatalf("invalid generated PostgreSQL database identifier %q", name)
	}
	config, err := pgx.ParseConfig(administrativeDSN)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	var encoding, collate, characterType string
	if err := connection.QueryRow(ctx, `SELECT pg_encoding_to_char(encoding), datcollate, datctype FROM pg_catalog.pg_database WHERE datname = current_database()`).Scan(&encoding, &collate, &characterType); err != nil {
		_ = connection.Close(ctx)
		t.Fatal(err)
	}
	statement := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s", quoteP8PostgreSQLIdentifier(name), quoteP8PostgreSQLLiteral(encoding), quoteP8PostgreSQLLiteral(collate), quoteP8PostgreSQLLiteral(characterType))
	if _, err := connection.Exec(ctx, statement); err != nil {
		_ = connection.Close(ctx)
		t.Fatalf("configured PostgreSQL profile cannot create its required disposable evidence database: %v", err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		admin, openErr := pgx.ConnectConfig(cleanupCtx, config)
		if openErr != nil {
			t.Errorf("open PostgreSQL profile for cleanup: %v", openErr)
			return
		}
		defer admin.Close(cleanupCtx)
		if _, terminateErr := admin.Exec(cleanupCtx, `SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); terminateErr != nil {
			t.Errorf("terminate exact disposable PostgreSQL database sessions: %v", terminateErr)
			return
		}
		if _, dropErr := admin.Exec(cleanupCtx, "DROP DATABASE "+quoteP8PostgreSQLIdentifier(name)); dropErr != nil {
			t.Errorf("drop exact disposable PostgreSQL database: %v", dropErr)
		}
	})
	derived, err := postgreSQLDSNForDatabase(administrativeDSN, name)
	if err != nil {
		t.Fatal(err)
	}
	return derived
}

func postgreSQLDSNForDatabase(value, database string) (string, error) {
	if !p8PostgreSQLDatabaseNamePattern.MatchString(database) {
		return "", fmt.Errorf("invalid generated PostgreSQL database identifier")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", err
		}
		parsed.Path = "/" + database
		parsed.RawPath = ""
		return parsed.String(), nil
	}
	location := p8PostgreSQLDBNameParameterPattern.FindStringIndex(value)
	if location == nil {
		return "", fmt.Errorf("PostgreSQL keyword DSN has no explicit dbname")
	}
	prefix := value[location[0]:location[1]]
	leading := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t\r\n"))]
	return value[:location[0]] + leading + "dbname=" + database + value[location[1]:], nil
}

func quoteP8PostgreSQLIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteP8PostgreSQLLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

type postgreSQLDoctorSnapshot struct {
	Catalog []string
	Ledger  []string
	UserIDs []int64
}

func snapshotPostgreSQLDoctorState(t *testing.T, dsn string) postgreSQLDoctorSnapshot {
	t.Helper()
	database, err := openDoctorDatabasePublic(context.Background(), ir.PostgreSQL, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool := database.UnsafeSQLX()
	result := postgreSQLDoctorSnapshot{Catalog: []string{}, Ledger: []string{}, UserIDs: []int64{}}
	catalogSQL := `SELECT n.nspname || ':' || c.relkind::text || ':' || c.relname || ':' || COALESCE(a.attnum::text, '') || ':' || COALESCE(a.attname, '') || ':' || COALESCE(pg_catalog.format_type(a.atttypid, a.atttypmod), '') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped WHERE n.nspname IN ('public', '_golem') ORDER BY n.nspname, c.relname, a.attnum`
	if err := pool.SelectContext(context.Background(), &result.Catalog, catalogSQL); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := pool.SelectContext(context.Background(), &result.Ledger, `SELECT migration_id || ':' || parent_chain_hash || ':' || chain_hash || ':' || file_checksums::text || ':' || before_physical_fingerprint || ':' || after_physical_fingerprint || ':' || phases::text || ':' || applied_at::text FROM "_golem"."_golem_migrations" ORDER BY migration_id`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := pool.SelectContext(context.Background(), &result.UserIDs, `SELECT id FROM "public"."users" ORDER BY id`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestP8DoctorOutputRedactionCanary(t *testing.T) {
	const canary = "P8_DOCTOR_SECRET_HOST_USER_PASSWORD_DATABASE_SQL_ROW"
	module := writeSocialModule(t, false)
	cases := []struct {
		name     string
		provider string
		dsn      string
	}{
		{
			name:     "sqlite-rejected-source",
			provider: "sqlite",
			dsn:      filepath.Join(t.TempDir(), canary+".db") + "#" + canary,
		},
		{
			name:     "postgresql-raw-connect-failure",
			provider: "postgresql",
			dsn:      "postgresql://" + canary + ":" + canary + "@127.0.0.1:1/" + canary + "?connect_timeout=1",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, format := range []struct {
				name string
				arg  []string
			}{
				{name: "human"},
				{name: "json", arg: []string{"--json"}},
			} {
				t.Run(format.name, func(t *testing.T) {
					arguments := []string{"doctor", "--provider", testCase.provider, "--dsn", testCase.dsn}
					arguments = append(arguments, format.arg...)
					var stdout, stderr bytes.Buffer
					if code := run(context.Background(), module, arguments, &stdout, &stderr); code != 1 {
						t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
					}
					combined := stdout.String() + stderr.String()
					for _, forbidden := range []string{canary, testCase.dsn, "127.0.0.1", "connect_timeout", "connection refused", "users", "SELECT"} {
						if strings.Contains(combined, forbidden) {
							t.Fatalf("doctor disclosed %q: stdout=%s stderr=%s", forbidden, stdout.String(), stderr.String())
						}
					}
					if format.name == "json" {
						var output doctorOutput
						if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
							t.Fatal(err)
						}
						if output.Provider != testCase.provider || output.Capabilities != "fail" || output.Schema != "unreachable" || len(output.Diagnostics) == 0 {
							t.Fatalf("closed doctor output=%#v", output)
						}
					}
				})
			}
		})
	}
}

func TestP8DoctorCloseFailureIsClosedAndRedacted(t *testing.T) {
	const canary = "P8_DOCTOR_RAW_DRIVER_CLOSE_CANARY"
	raw := sql.OpenDB(doctorCloseFailureConnector{message: canary})
	if err := raw.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	internal := providerhandle.AdoptUnverifiedForTest(sqlx.NewDb(raw, "p8-doctor-close"), providerhandle.TestMetadata{
		Provider: golem.SQLite, MaximumOpen: 1, MaximumIdle: 1,
	})
	database := (*publicprovider.Database)(internal)
	originalOpen := openDoctorDatabase
	openDoctorDatabase = func(context.Context, ir.Provider, string) (*publicprovider.Database, error) {
		return database, nil
	}
	t.Cleanup(func() { openDoctorDatabase = originalOpen })

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), writeSingleProviderModule(t), []string{"doctor", "--provider", "sqlite", "--dsn", "ignored", "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor close failure code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), canary) {
		t.Fatalf("doctor disclosed raw close failure: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	var output doctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Capabilities != "fail" || !containsDoctorDiagnostic(output.Diagnostics, "GOLEM_DOCTOR_PROVIDER_CLOSE_FAILED", "error") {
		t.Fatalf("doctor did not classify close failure: %#v", output)
	}
	if database.UnsafeSQLX() != nil {
		t.Fatal("doctor close failure retained raw-pool access")
	}
}

func containsDoctorDiagnostic(values []doctorDiagnostic, code, severity string) bool {
	for _, value := range values {
		if value.Code == code && value.Severity == severity {
			return true
		}
	}
	return false
}

type doctorCloseFailureConnector struct{ message string }

func (connector doctorCloseFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return &doctorCloseFailureConnection{message: connector.message}, nil
}
func (connector doctorCloseFailureConnector) Driver() driver.Driver {
	return doctorCloseFailureDriver{connector: connector}
}

type doctorCloseFailureDriver struct{ connector doctorCloseFailureConnector }

func (value doctorCloseFailureDriver) Open(string) (driver.Conn, error) {
	return value.connector.Connect(context.Background())
}

type doctorCloseFailureConnection struct{ message string }

func (*doctorCloseFailureConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("doctor query unavailable")
}
func (connection *doctorCloseFailureConnection) Close() error { return errors.New(connection.message) }
func (*doctorCloseFailureConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("doctor transaction unavailable")
}
func (*doctorCloseFailureConnection) Ping(context.Context) error { return nil }

var _ driver.Connector = doctorCloseFailureConnector{}
var _ driver.Pinger = (*doctorCloseFailureConnection)(nil)

type sqliteDoctorSnapshot struct {
	Catalog []string
	Ledger  []string
	UserIDs []int64
}

func snapshotSQLiteDoctorState(t *testing.T, databasePath string) sqliteDoctorSnapshot {
	t.Helper()
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName: "file:" + databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	pool := database.UnsafeSQLX()
	if pool == nil {
		t.Fatal("SQLite snapshot provider returned no pool")
	}
	result := sqliteDoctorSnapshot{Catalog: []string{}, Ledger: []string{}, UserIDs: []int64{}}
	if err := pool.SelectContext(context.Background(), &result.Catalog, `SELECT type || ':' || name || ':' || COALESCE(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`); err != nil {
		t.Fatal(err)
	}
	if err := pool.SelectContext(context.Background(), &result.Ledger, `SELECT migration_id || ':' || parent_chain_hash || ':' || chain_hash || ':' || file_checksums || ':' || before_physical_fingerprint || ':' || after_physical_fingerprint || ':' || phases || ':' || applied_at FROM _golem_migrations ORDER BY migration_id`); err != nil {
		t.Fatal(err)
	}
	if err := pool.SelectContext(context.Background(), &result.UserIDs, `SELECT id FROM users ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return result
}

func secondDoctorDiagnostics(t *testing.T, encoded []byte) []doctorDiagnostic {
	t.Helper()
	var output doctorOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output.Diagnostics
}
