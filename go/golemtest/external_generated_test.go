package golemtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/eleven-am/golem/go/internal/testenv"
)

const externalApplicationModule = `module example.com/golempolicykit

go 1.25.0

require (
	github.com/eleven-am/golem/go v0.0.0
	github.com/99designs/gqlgen v0.17.70
	github.com/vektah/gqlparser/v2 v2.5.23
)
`

const externalApplicationGate = "TestExternalGeneratedApplicationPolicyKitMatchesCallerBehaviour"
const externalQueryPlanApplicationGate = "TestExternalGeneratedApplicationQueryPlanIsCallerOnlyTypedAndRedacted"
const externalOptimisticConcurrencyApplicationGate = "TestExternalGeneratedApplicationOptimisticConcurrencyRaces"
const externalQueueApplicationGate = "TestExternalGeneratedApplicationQueueIsUsable"

type externalProfile struct {
	name      string
	variable  string
	collation string
	target    string
}

func externalProfiles() []externalProfile {
	return []externalProfile{
		{name: "c", variable: "GOLEM_TEST_POSTGRES_DSN", collation: "C", target: "GOLEM_KIT_POSTGRES_C_DSN"},
		{name: "linguistic", variable: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", collation: "linguistic", target: "GOLEM_KIT_POSTGRES_LINGUISTIC_DSN"},
	}
}

func TestPolicyTestKitExternalGeneratedApplicationCompilesAndRuns(t *testing.T) {
	runExternalGeneratedApplication(t, externalApplicationGate)
}

func TestQueryPlanSQLiteAndPostgreSQLExternalGeneratedApplication(t *testing.T) {
	runExternalGeneratedApplication(t, externalQueryPlanApplicationGate)
}

func TestOptimisticConcurrencySQLiteAndPostgreSQLExternalGeneratedApplication(t *testing.T) {
	runExternalGeneratedApplication(t, externalOptimisticConcurrencyApplicationGate)
}

func TestQueueSQLiteAndPostgreSQLExternalGeneratedApplication(t *testing.T) {
	runExternalGeneratedApplication(t, externalQueueApplicationGate)
}

func runExternalGeneratedApplication(t *testing.T, gate string) {
	t.Helper()
	if testing.Short() {
		t.Skip("external generated application build")
	}
	mandatory := testenv.PostgreSQLRequired()
	moduleRoot := filepath.Dir(packageDirectory(t))
	consumer := externalApplicationConsumer(t, moduleRoot)
	environment := append(os.Environ(), "GOWORK=off", "GOFLAGS=")

	binary := filepath.Join(t.TempDir(), "golem")
	runExternalCommand(t, moduleRoot, environment, "go", "build", "-o", binary, "./cmd/golem")
	runExternalCommand(t, consumer, environment, "go", "mod", "tidy")
	runExternalCommand(t, consumer, environment, binary, "migration", "new", "--name", "initial", "--schema", "./policy")
	runExternalCommand(t, consumer, environment, binary, "generate", "--schema", "./policy", "--app-out", "./policy")
	runExternalCommand(t, consumer, environment, "go", "mod", "tidy")

	requireExternalPublicSources(t, consumer)

	database := filepath.Join(consumer, "kit.sqlite")
	runExternalCommand(t, consumer, environment, binary, "migration", "apply", "--provider", "sqlite", "--dsn", database)
	environment = append(environment, "GOLEM_KIT_SQLITE_DSN=file:"+filepath.ToSlash(database))

	profiles := 0
	for _, profile := range externalProfiles() {
		administrative := strings.TrimSpace(os.Getenv(profile.variable))
		if administrative == "" {
			testenv.FailMissingPostgreSQLIfRequired(t, profile.variable+" is not configured")
			continue
		}
		dsn := externalPostgreSQLDatabase(t, administrative, profile)
		runExternalCommand(t, consumer, environment, binary, "migration", "apply", "--provider", "postgresql", "--dsn", dsn)
		environment = append(environment, profile.target+"="+dsn)
		profiles++
	}
	if mandatory && profiles != len(externalProfiles()) {
		t.Fatalf("mandatory PostgreSQL evidence resolved %d of %d profiles", profiles, len(externalProfiles()))
	}

	runExternalCommand(t, consumer, environment, "go", "test", "./gate", "-race", "-count=1", "-run", "^"+gate+"$", "-v")
}

func externalApplicationConsumer(t *testing.T, moduleRoot string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Join(root, "consumer")
	source := filepath.Join(packageDirectory(t), "testdata", "externalapp")
	entries := []string{
		"tools.go",
		filepath.Join("policy", "schema.go"),
		filepath.Join("policy", "policies.go"),
		filepath.Join("gate", "gate_test.go"),
		filepath.Join("gate", "queryplan_test.go"),
		filepath.Join("gate", "optimistic_concurrency_test.go"),
		filepath.Join("gate", "queue_test.go"),
	}
	for _, entry := range entries {
		content, readErr := os.ReadFile(filepath.Join(source, entry))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(content), "/internal/") {
			t.Fatalf("external application source %s names an internal package", entry)
		}
		writeExternalFile(t, consumer, entry, string(content))
	}
	if strings.Contains(externalApplicationModule, "replace") {
		t.Fatal("external application module text must declare its replacements explicitly below")
	}
	manifest := externalApplicationModule + "\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(moduleRoot) + "\n"
	writeExternalFile(t, consumer, "go.mod", manifest)
	return consumer
}

func requireExternalPublicSources(t *testing.T, consumer string) {
	t.Helper()
	err := filepath.WalkDir(consumer, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), "github.com/eleven-am/golem/go/internal/") {
			return fmt.Errorf("external application source %s imports a Golem internal package", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(consumer, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(manifest), "replace") != 1 {
		t.Fatalf("external application resolved through unexpected replacements:\n%s", manifest)
	}
}

func externalPostgreSQLDatabase(t *testing.T, administrative string, profile externalProfile) string {
	t.Helper()
	configuration, err := pgx.ParseConfig(administrative)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, configuration)
	if err != nil {
		t.Fatalf("open the %s PostgreSQL profile: %v", profile.name, err)
	}
	var collate, characterType string
	if err := connection.QueryRow(ctx, `SELECT datcollate, datctype FROM pg_catalog.pg_database WHERE datname = current_database()`).Scan(&collate, &characterType); err != nil {
		_ = connection.Close(ctx)
		t.Fatal(err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if profile.collation == "C" && (collate != "C" || characterType != "C") {
		t.Fatalf("the %s profile has collation=%q ctype=%q", profile.name, collate, characterType)
	}
	if profile.collation == "linguistic" && (collate == "C" || characterType == "C") {
		t.Fatalf("the %s profile requires a non-C collation and ctype; got collation=%q ctype=%q", profile.name, collate, characterType)
	}
	return testenv.DisposablePostgreSQLFrom(t, administrative)
}
func writeExternalFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runExternalCommand(t *testing.T, directory string, environment []string, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(name), strings.Join(redactedArguments(arguments), " "), err, output)
	}
}

func redactedArguments(arguments []string) []string {
	result := append([]string(nil), arguments...)
	for index := range result {
		if index > 0 && result[index-1] == "--dsn" {
			result[index] = "<dsn>"
		}
	}
	return result
}
