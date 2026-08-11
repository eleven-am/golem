package golemtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

var externalDatabasePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

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
	if testing.Short() {
		t.Skip("external generated application build")
	}
	mandatory := os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1"
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
			if mandatory {
				t.Fatalf("%s is required when mandatory PostgreSQL evidence is enabled", profile.variable)
			}
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

	runExternalCommand(t, consumer, environment, "go", "test", "./gate", "-race", "-count=1", "-run", "^"+externalApplicationGate+"$", "-v")
}

func externalApplicationConsumer(t *testing.T, moduleRoot string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Join(root, "consumer")
	source := filepath.Join(packageDirectory(t), "testdata", "externalapp")
	entries := []string{"tools.go", filepath.Join("policy", "schema.go"), filepath.Join("policy", "policies.go"), filepath.Join("gate", "gate_test.go")}
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
	var encoding, collate, characterType string
	if err := connection.QueryRow(ctx, `SELECT pg_encoding_to_char(encoding), datcollate, datctype FROM pg_catalog.pg_database WHERE datname = current_database()`).Scan(&encoding, &collate, &characterType); err != nil {
		_ = connection.Close(ctx)
		t.Fatal(err)
	}
	if profile.collation == "C" && (collate != "C" || characterType != "C") {
		_ = connection.Close(ctx)
		t.Fatalf("the %s profile has collation=%q ctype=%q", profile.name, collate, characterType)
	}
	if profile.collation == "linguistic" && (collate == "C" || characterType == "C") {
		_ = connection.Close(ctx)
		t.Fatalf("the %s profile requires a non-C collation and ctype; got collation=%q ctype=%q", profile.name, collate, characterType)
	}
	name := fmt.Sprintf("golem_kit_gate_%s_%d_%d", profile.name, os.Getpid(), time.Now().UnixNano())
	if !externalDatabasePattern.MatchString(name) {
		_ = connection.Close(ctx)
		t.Fatalf("invalid generated PostgreSQL database name %q", name)
	}
	statement := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s",
		externalPostgreSQLIdentifier(name), externalPostgreSQLLiteral(encoding), externalPostgreSQLLiteral(collate), externalPostgreSQLLiteral(characterType))
	if _, err := connection.Exec(ctx, statement); err != nil {
		_ = connection.Close(ctx)
		t.Fatalf("create disposable PostgreSQL database: %v", err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		admin, openErr := pgx.ConnectConfig(cleanupContext, configuration)
		if openErr != nil {
			t.Errorf("open PostgreSQL cleanup connection: %v", openErr)
			return
		}
		defer admin.Close(cleanupContext)
		if _, terminateErr := admin.Exec(cleanupContext, `SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); terminateErr != nil {
			t.Errorf("terminate disposable PostgreSQL sessions: %v", terminateErr)
			return
		}
		if _, dropErr := admin.Exec(cleanupContext, "DROP DATABASE "+externalPostgreSQLIdentifier(name)); dropErr != nil {
			t.Errorf("drop disposable PostgreSQL database: %v", dropErr)
		}
	})
	parsed, err := url.Parse(administrative)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path, parsed.RawPath = "/"+name, ""
	return parsed.String()
}

func externalPostgreSQLIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func externalPostgreSQLLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
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
