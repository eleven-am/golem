package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/provider"
	providerpostgresql "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestP8DocumentationCommandCorpus(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	quickstartPath := filepath.Join(moduleRoot, "..", "docs", "golem-go", "QUICKSTART.md")
	quickstart, err := os.ReadFile(quickstartPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := documentedGolemCommands(string(quickstart))
	want := []string{
		"golem inspect --schema ./social",
		"golem migration new --schema ./social --migrations migrations --name initial",
		"golem generate --schema ./social --app-out ./social --migrations migrations",
		"golem check --schema ./social --app-out ./social --migrations migrations",
		"golem migration apply --provider sqlite --dsn file:social.sqlite --migrations migrations",
		"golem doctor --schema ./social --provider sqlite --dsn file:social.sqlite --migrations migrations",
		"golem migration apply --provider postgresql --dsn \"$DATABASE_URL\" --migrations migrations",
		"golem doctor --schema ./social --provider postgresql --dsn \"$DATABASE_URL\" --migrations migrations",
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("quickstart command corpus = %#v; want %#v", commands, want)
	}
	if !bytes.Contains(quickstart, []byte("go install github.com/eleven-am/golem/go/cmd/golem@vX.Y.Z")) {
		t.Fatal("quickstart omitted the frozen nested-module installation command")
	}
	for _, required := range []string{
		"github.com/99designs/gqlgen v0.17.70",
		"github.com/vektah/gqlparser/v2 v2.5.23",
		`_ "github.com/99designs/gqlgen/graphql"`,
		`_ "github.com/vektah/gqlparser/v2/ast"`,
		"not silently rewrite `go.mod` or `go.sum`",
	} {
		if !bytes.Contains(quickstart, []byte(required)) {
			t.Fatalf("quickstart omitted required clean-module setup %q", required)
		}
	}
}

func TestP8QuickstartFromEmptyDirectory(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	quickstartPath := filepath.Join(moduleRoot, "..", "docs", "golem-go", "QUICKSTART.md")
	quickstart, err := os.ReadFile(quickstartPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := documentedGolemCommands(string(quickstart))
	if len(commands) != 8 {
		t.Fatalf("documented commands=%d want=8", len(commands))
	}
	exampleSource := filepath.Join(moduleRoot, "examples", "social")
	example := filepath.Join(t.TempDir(), "social")
	copyDocumentationTree(t, exampleSource, example)
	removeDocumentationGeneratedState(t, example)

	workspace := documentationWorkspace(t, moduleRoot, example)
	executionExample, err := filepath.EvalSymlinks(example)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", workspace)
	for index, command := range commands[:6] {
		arguments := strings.Fields(command)[1:]
		for argumentIndex := range arguments {
			if arguments[argumentIndex] == "file:social.sqlite" {
				arguments[argumentIndex] = "file:" + filepath.ToSlash(filepath.Join(executionExample, "social.db"))
			}
		}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), executionExample, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("documented command %d (%s) exited %d\nstdout:\n%s\nstderr:\n%s", index+1, command, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(example, "social.db")); err != nil {
		t.Fatalf("documented SQLite journey did not create its database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(example, "go.mod")); err != nil {
		t.Fatalf("documented journey removed the consumer module: %v", err)
	}
	if output := runDocumentationGo(t, workspace, executionExample, "test", "./..."); output != "" {
		t.Log(output)
	}
}

func TestP8EveryPublicSnippetTypeChecks(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	exampleSource := filepath.Join(moduleRoot, "examples", "social")
	moduleFile, err := os.ReadFile(filepath.Join(exampleSource, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(moduleFile, []byte("replace ")) || bytes.Contains(moduleFile, []byte("replace(")) {
		t.Fatal("the public example must remain a clean consumer without a replace directive")
	}
	for _, path := range []string{
		filepath.Join(exampleSource, "social", "models.go"),
		filepath.Join(exampleSource, "social", "schema.go"),
		filepath.Join(exampleSource, "social", "policies.go"),
		filepath.Join(exampleSource, "social", "hooks.go"),
		filepath.Join(exampleSource, "social", "extensions.go"),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(content, []byte("github.com/eleven-am/golem/go/internal/")) {
			t.Fatalf("public documentation source imports an internal package: %s", path)
		}
	}
	var snippets []string
	for _, path := range []string{
		filepath.Join(moduleRoot, "..", "docs", "golem-go", "QUICKSTART.md"),
		filepath.Join(moduleRoot, "..", "docs", "golem-go", "PRODUCTION.md"),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		snippets = append(snippets, documentedGoSnippets(string(content))...)
	}
	if len(snippets) == 0 {
		t.Fatal("public documentation contains no executable Go snippets")
	}
	example := filepath.Join(t.TempDir(), "social")
	copyDocumentationTree(t, exampleSource, example)
	for index, snippet := range snippets {
		directory := filepath.Join(example, "doctest", "snippet"+string(rune('a'+index)))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "snippet.go"), []byte(snippet), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := documentationWorkspace(t, moduleRoot, example)
	if output := runDocumentationGo(t, workspace, example, "test", "-tags=tools", "./doctest/..."); output != "" {
		t.Log(output)
	}
}

func TestP8DeploymentAndRecoveryRunbookDrills(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	example := filepath.Join(t.TempDir(), "social")
	copyDocumentationTree(t, filepath.Join(moduleRoot, "examples", "social"), example)
	var err error
	example, err = filepath.EvalSymlinks(example)
	if err != nil {
		t.Fatal(err)
	}
	workspace := documentationWorkspace(t, moduleRoot, example)
	t.Setenv("GOWORK", workspace)
	recoveryFixture := buildDocumentationRecoveryFixture(t, example)

	t.Run("sqlite-backup-drift-restore", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "social.sqlite")
		dataSourceName := "file:" + filepath.ToSlash(databasePath)
		runDocumentationGolem(t, example, "migration", "apply", "--provider", "sqlite", "--dsn", dataSourceName, "--migrations", "migrations")
		runDocumentationGolem(t, example, "doctor", "--schema", "./social", "--provider", "sqlite", "--dsn", dataSourceName, "--migrations", "migrations")
		seedSnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "sqlite", dataSourceName, "seed")
		if verifySnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "sqlite", dataSourceName, "verify"); !bytes.Equal(seedSnapshot, verifySnapshot) {
			t.Fatalf("SQLite recovery snapshot changed before backup")
		}
		checkpointDocumentationSQLite(t, dataSourceName, databasePath)
		backup, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(databasePath, []byte("deliberate P8 recovery drill corruption"), 0o600); err != nil {
			t.Fatal(err)
		}
		runDocumentationGolemMustFail(t, example, "doctor", "--schema", "./social", "--provider", "sqlite", "--dsn", dataSourceName, "--migrations", "migrations")
		if err := os.WriteFile(databasePath, backup, 0o600); err != nil {
			t.Fatal(err)
		}
		runDocumentationGolem(t, example, "doctor", "--schema", "./social", "--provider", "sqlite", "--dsn", dataSourceName, "--migrations", "migrations")
		if restoredSnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "sqlite", dataSourceName, "verify"); !bytes.Equal(seedSnapshot, restoredSnapshot) {
			t.Fatalf("SQLite restore changed managed rows, ledger, or pending outbox evidence")
		}
		drained := runDocumentationRecoveryFixture(t, example, recoveryFixture, "sqlite", dataSourceName, "drain")
		if !bytes.Contains(drained, []byte(`"status":"delivered"`)) {
			t.Fatalf("SQLite restored publisher did not deliver pending fact")
		}
	})

	profiles := []struct {
		name        string
		environment string
		fallback    string
		resolved    string
	}{
		{name: "postgresql-c", environment: "GOLEM_TEST_POSTGRES_DSN", fallback: "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable"},
		{name: "postgresql-linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", fallback: "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable"},
	}
	for index := range profiles {
		profiles[index].resolved = strings.TrimSpace(os.Getenv(profiles[index].environment))
		if profiles[index].resolved == "" {
			profiles[index].resolved = profiles[index].fallback
		}
	}
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" && profiles[0].resolved == profiles[1].resolved {
		t.Fatal("mandatory PostgreSQL documentation profiles must use distinct DSNs")
	}
	for _, profile := range profiles {
		t.Run(profile.name+"-backup-drift-restore", func(t *testing.T) {
			database := newDocumentationPostgreSQLDatabase(t, profile.resolved)
			if profile.name == "postgresql-c" && (database.collation != "C" || database.characterType != "C") {
				t.Fatalf("C profile collation=%q ctype=%q", database.collation, database.characterType)
			}
			if profile.name == "postgresql-linguistic" && (database.collation == "C" || database.characterType == "C") {
				t.Fatalf("linguistic profile collation=%q ctype=%q", database.collation, database.characterType)
			}
			runDocumentationGolem(t, example, "migration", "apply", "--provider", "postgresql", "--dsn", database.dataSourceName, "--migrations", "migrations")
			runDocumentationGolem(t, example, "doctor", "--schema", "./social", "--provider", "postgresql", "--dsn", database.dataSourceName, "--migrations", "migrations")
			seedSnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "postgresql", database.dataSourceName, "seed")
			if verifySnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "postgresql", database.dataSourceName, "verify"); !bytes.Equal(seedSnapshot, verifySnapshot) {
				t.Fatalf("PostgreSQL recovery snapshot changed before backup")
			}

			pgDump, dumpErr := exec.LookPath("pg_dump")
			pgRestore, restoreErr := exec.LookPath("pg_restore")
			if dumpErr != nil || restoreErr != nil {
				if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" {
					t.Fatalf("PostgreSQL recovery drill requires pg_dump and pg_restore")
				}
				t.Skip("PostgreSQL client recovery tools are not installed")
			}
			backup := filepath.Join(t.TempDir(), "social.dump")
			runDocumentationProcess(t, example, pgDump, "--format=custom", "--file="+backup, database.dataSourceName)
			target, err := providerpostgresql.Open(context.Background(), providerpostgresql.Config{DataSourceName: database.dataSourceName})
			if err != nil {
				t.Fatal("open disposable PostgreSQL recovery target")
			}
			if _, err := target.UnsafeSQLX().ExecContext(context.Background(), `DROP TABLE "posts" CASCADE`); err != nil {
				_ = target.Close()
				t.Fatal(err)
			}
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			runDocumentationGolemMustFail(t, example, "doctor", "--schema", "./social", "--provider", "postgresql", "--dsn", database.dataSourceName, "--migrations", "migrations")
			database.recreate(t)
			runDocumentationProcess(t, example, pgRestore, "--dbname="+database.dataSourceName, "--exit-on-error", backup)
			runDocumentationGolem(t, example, "doctor", "--schema", "./social", "--provider", "postgresql", "--dsn", database.dataSourceName, "--migrations", "migrations")
			if restoredSnapshot := runDocumentationRecoveryFixture(t, example, recoveryFixture, "postgresql", database.dataSourceName, "verify"); !bytes.Equal(seedSnapshot, restoredSnapshot) {
				t.Fatalf("PostgreSQL restore changed managed rows, ledger, or pending outbox evidence")
			}
			drained := runDocumentationRecoveryFixture(t, example, recoveryFixture, "postgresql", database.dataSourceName, "drain")
			if !bytes.Contains(drained, []byte(`"status":"delivered"`)) {
				t.Fatalf("PostgreSQL restored publisher did not deliver pending fact")
			}
		})
	}
}

type documentationPostgreSQLDatabase struct {
	admin          *provider.Database
	name           string
	dataSourceName string
	collation      string
	characterType  string
}

func newDocumentationPostgreSQLDatabase(t *testing.T, baseDataSourceName string) *documentationPostgreSQLDatabase {
	t.Helper()
	admin, err := providerpostgresql.Open(context.Background(), providerpostgresql.Config{DataSourceName: baseDataSourceName})
	if err != nil {
		if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" {
			t.Fatal("required PostgreSQL documentation profile is unavailable")
		}
		t.Skip("PostgreSQL documentation profile is unavailable")
	}
	var collation, characterType string
	if err := admin.UnsafeSQLX().QueryRowxContext(context.Background(), `SELECT datcollate, datctype FROM pg_database WHERE datname = current_database()`).Scan(&collation, &characterType); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(baseDataSourceName)
	if err != nil {
		_ = admin.Close()
		t.Fatal("parse PostgreSQL documentation profile")
	}
	name := fmt.Sprintf("p8_docs_%d_%d", os.Getpid(), time.Now().UnixNano())
	parsed.Path = "/" + name
	parsed.RawPath = ""
	database := &documentationPostgreSQLDatabase{
		admin: admin, name: name, dataSourceName: parsed.String(), collation: collation, characterType: characterType,
	}
	database.recreate(t)
	t.Cleanup(func() {
		database.drop(t)
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL documentation administrator: %v", err)
		}
	})
	return database
}

func (database *documentationPostgreSQLDatabase) recreate(t *testing.T) {
	t.Helper()
	database.drop(t)
	statement := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 LC_COLLATE %s LC_CTYPE %s", postgresIdentifier(database.name), postgresLiteral(database.collation), postgresLiteral(database.characterType))
	if _, err := database.admin.UnsafeSQLX().ExecContext(context.Background(), statement); err != nil {
		t.Fatal(err)
	}
}

func (database *documentationPostgreSQLDatabase) drop(t *testing.T) {
	t.Helper()
	if _, err := database.admin.UnsafeSQLX().ExecContext(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, database.name); err != nil {
		t.Fatal(err)
	}
	if _, err := database.admin.UnsafeSQLX().ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+postgresIdentifier(database.name)); err != nil {
		t.Fatal(err)
	}
}

func postgresIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func postgresLiteral(value string) string    { return `'` + strings.ReplaceAll(value, `'`, `''`) + `'` }

func runDocumentationGolem(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), directory, arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("golem %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(redactedDocumentationArguments(arguments), " "), code, stdout.String(), stderr.String())
	}
}

func runDocumentationGolemMustFail(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), directory, arguments, &stdout, &stderr); code == 0 {
		t.Fatalf("golem %s unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", strings.Join(redactedDocumentationArguments(arguments), " "), stdout.String(), stderr.String())
	}
}

func runDocumentationProcess(t *testing.T, directory, executable string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	command.Env = os.Environ()
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s recovery command failed: %v", filepath.Base(executable), err)
	}
}

func buildDocumentationRecoveryFixture(t *testing.T, directory string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "social-recovery-fixture")
	command := exec.Command("go", "build", "-o", binary, "./cmd/social-recovery-fixture")
	command.Dir = directory
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build social recovery fixture: %v\n%s", err, output)
	}
	return binary
}

func runDocumentationRecoveryFixture(t *testing.T, directory, binary, providerName, dataSourceName, mode string) []byte {
	t.Helper()
	command := exec.Command(binary, mode)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOLEM_PROVIDER="+providerName, "GOLEM_DATABASE_DSN="+dataSourceName)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("social recovery fixture %s failed: %v\n%s", mode, err, output)
	}
	return bytes.TrimSpace(output)
}

func checkpointDocumentationSQLite(t *testing.T, dataSourceName, databasePath string) {
	t.Helper()
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal("open SQLite recovery target for checkpoint")
	}
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, statErr := os.Stat(databasePath + suffix)
		if statErr == nil && info.Size() != 0 {
			t.Fatalf("SQLite backup began with nonempty %s sidecar", suffix)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			t.Fatal(statErr)
		}
	}
}

func redactedDocumentationArguments(arguments []string) []string {
	result := append([]string(nil), arguments...)
	for index := range result {
		if index > 0 && result[index-1] == "--dsn" {
			result[index] = "<redacted>"
		}
		if strings.HasPrefix(result[index], "--dbname=") {
			result[index] = "--dbname=<redacted>"
		}
	}
	return result
}

func documentedGolemCommands(markdown string) []string {
	lines := strings.Split(markdown, "\n")
	commands := make([]string, 0, 8)
	inConsole := false
	for _, line := range lines {
		switch line {
		case "```console":
			inConsole = true
		case "```":
			inConsole = false
		default:
			if inConsole && strings.HasPrefix(line, "$ golem ") {
				commands = append(commands, strings.TrimPrefix(line, "$ "))
			}
		}
	}
	return commands
}

func documentedGoSnippets(markdown string) []string {
	lines := strings.Split(markdown, "\n")
	result := make([]string, 0, 4)
	var current []string
	for _, line := range lines {
		switch {
		case line == "```go" && current == nil:
			current = []string{}
		case line == "```" && current != nil:
			result = append(result, strings.Join(current, "\n")+"\n")
			current = nil
		case current != nil:
			current = append(current, line)
		}
	}
	return result
}

func copyDocumentationTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, info.Mode())
	}); err != nil {
		t.Fatal(err)
	}
}

func removeDocumentationGeneratedState(t *testing.T, example string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(example, "migrations"),
		filepath.Join(example, ".golem"),
		filepath.Join(example, "social", "golemgqlgen"),
	} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	generated, err := filepath.Glob(filepath.Join(example, "social", "zz_golem*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range generated {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func documentationWorkspace(t *testing.T, moduleRoot, example string) string {
	t.Helper()
	canonicalRoot, err := filepath.EvalSymlinks(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalExample, err := filepath.EvalSymlinks(example)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "go.work")
	content := "go 1.25.0\n\nuse (\n\t" + filepath.ToSlash(canonicalRoot) + "\n\t" + filepath.ToSlash(canonicalExample) + "\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => " + filepath.ToSlash(canonicalRoot) + "\n"
	if err := os.WriteFile(workspace, []byte(content), 0o600); err != nil {
		t.Fatalf("create documentation workspace: %v", err)
	}
	return workspace
}

func runDocumentationGo(t *testing.T, workspace, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK="+workspace)
	output, err := command.CombinedOutput()
	if err != nil {
		workspaceContent, _ := os.ReadFile(workspace)
		probe := exec.Command("go", "env", "GOMOD", "GOWORK")
		probe.Dir = directory
		probe.Env = append(os.Environ(), "GOWORK="+workspace)
		probeOutput, _ := probe.CombinedOutput()
		t.Fatalf("go %s: %v\n%s\nworkspace:\n%s\ngo env:\n%s", strings.Join(arguments, " "), err, output, workspaceContent, probeOutput)
	}
	return string(output)
}
