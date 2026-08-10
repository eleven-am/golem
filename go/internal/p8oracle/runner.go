// Package p8oracle provides external-consumer, live-provider orchestration for
// P8 independent evidence. Expected answers remain in the supplied oracle
// source; this package only provisions reviewed databases and runs it.
package p8oracle

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/provider/postgresql"
)

type liveProfile struct {
	name      string
	provider  string
	baseDSN   string
	collation string
	ctype     string
}

// RunExternalScenario compiles source as a clean consumer of the checked-in
// generated social module and executes scenario against every required live
// provider profile. source must use only public packages when interrogating the
// application; this runner contributes no production expectation helpers.
func RunExternalScenario(t *testing.T, source []byte, scenario string) {
	runExternalScenario(t, source, scenario, false)
}

// RunExternalScenarioRace applies the same independent external-consumer
// boundary while compiling and executing the application under Go's race
// detector. It is reserved for evidence rows whose completion profile
// explicitly requires race instrumentation.
func RunExternalScenarioRace(t *testing.T, source []byte, scenario string) {
	runExternalScenario(t, source, scenario, true)
}

func runExternalScenario(t *testing.T, source []byte, scenario string, race bool) {
	runExternalProfiles(t, source, func(t *testing.T, consumer string, environment []string) {
		childEnvironment := setEnvironment(environment, "P8_ORACLE_SCENARIO", scenario)
		arguments := []string{"test", "-count=1", "-run", "^TestP8ExternalOracleScenario$", "."}
		if race {
			arguments = []string{"test", "-race", "-count=1", "-run", "^TestP8ExternalOracleScenario$", "."}
		}
		runProcess(t, consumer, childEnvironment, "go", arguments...)
	})
}

// RunExternalFuzz provisions each required provider once and lets the clean
// external consumer own the fuzz loop. A single worker preserves disposable
// database isolation while still exercising multiple generated inputs without
// rebuilding the CLI or replaying migrations per input.
func RunExternalFuzz(t *testing.T, source []byte, target string, duration time.Duration) {
	t.Helper()
	if target == "" || duration <= 0 {
		t.Fatal("external fuzz target and positive duration are required")
	}
	runExternalProfiles(t, source, func(t *testing.T, consumer string, environment []string) {
		environment = setEnvironment(environment, "P8_ORACLE_SCENARIO", "external-fuzz")
		// The Go fuzz coordinator and its workers are separate processes. Give
		// them one profile-local random seed so they exercise and verify the same
		// protected canaries without putting those values in argv or output.
		var canarySeed [32]byte
		if _, err := cryptorand.Read(canarySeed[:]); err != nil {
			t.Fatalf("create external fuzz canary seed: %v", err)
		}
		environment = setEnvironment(environment, "P8_ORACLE_FUZZ_CANARY_SEED", hex.EncodeToString(canarySeed[:]))
		arguments := []string{
			"test", "-race", "-run", "^$", "-fuzz", "^" + target + "$",
			"-fuzztime", duration.String(), "-parallel", "1", ".",
		}
		output := strings.TrimSpace(runProcess(t, consumer, environment, "go", arguments...))
		if output != "" {
			t.Log(output)
		}
	})
}

// RunExternalBenchmark builds the same clean consumer once, provisions every
// mandatory provider profile, and records the child benchmark's real latency
// and allocation output. The caller should run the root benchmark with
// -benchtime=1x; each child shape uses the explicit sample count supplied here.
func RunExternalBenchmark(b *testing.B, source []byte, target string, samples int) {
	b.Helper()
	if target == "" || samples < 1 {
		b.Fatal("external benchmark target and positive sample count are required")
	}
	b.StopTimer()
	validateOracleSource(b, source)
	root, example := repositoryPaths(b)
	consumer := filepath.Join(b.TempDir(), "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		b.Fatal(err)
	}
	module := `module p8oracle.consumer

go 1.25.0

require (
	github.com/eleven-am/golem/go v0.0.0
	github.com/eleven-am/golem/go/examples/social v0.0.0
)
`
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(module), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "oracle_test.go"), source, 0o600); err != nil {
		b.Fatal(err)
	}
	canonicalConsumer := canonicalPath(b, consumer)
	canonicalRoot := canonicalPath(b, root)
	canonicalExample := canonicalPath(b, example)
	workspace := filepath.Join(b.TempDir(), "go.work")
	work := fmt.Sprintf(`go 1.25.0

use (
	%s
	%s
	%s
)

replace github.com/eleven-am/golem/go v0.0.0 => %s
replace github.com/eleven-am/golem/go/examples/social v0.0.0 => %s
`, filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample), filepath.ToSlash(canonicalConsumer), filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample))
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		b.Fatal(err)
	}
	environment := setEnvironment(os.Environ(), "GOWORK", workspace)
	cli := filepath.Join(b.TempDir(), "golem")
	runProcess(b, canonicalRoot, environment, "go", "build", "-o", cli, "./cmd/golem")
	for _, profile := range requiredProfiles() {
		profile := profile
		b.Run(profile.name, func(b *testing.B) {
			b.StopTimer()
			dsn, cleanup := provisionProfile(b, profile)
			defer cleanup()
			runProcess(b, canonicalExample, environment, cli, "migration", "apply", "--provider", profile.provider, "--dsn", dsn, "--migrations", "migrations")
			childEnvironment := setEnvironment(environment, "P8_ORACLE_PROVIDER", profile.provider)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_DSN", dsn)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_EXAMPLE", canonicalExample)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_CLI", cli)
			output := runProcess(b, canonicalConsumer, childEnvironment, "go", "test", "-v", "-run", "^$", "-bench", "^"+target+"$", "-benchtime", fmt.Sprintf("%dx", samples), "-benchmem", ".")
			b.Log(strings.TrimSpace(output))
			b.ReportMetric(float64(samples), "child-samples")
		})
	}
}

type externalProfileRun func(*testing.T, string, []string)

func runExternalProfiles(t *testing.T, source []byte, execute externalProfileRun) {
	t.Helper()
	validateOracleSource(t, source)
	root, example := repositoryPaths(t)
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	module := `module p8oracle.consumer

go 1.25.0

require (
	github.com/eleven-am/golem/go v0.0.0
	github.com/eleven-am/golem/go/examples/social v0.0.0
)
`
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "oracle_test.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalConsumer := canonicalPath(t, consumer)
	canonicalRoot := canonicalPath(t, root)
	canonicalExample := canonicalPath(t, example)
	workspace := filepath.Join(t.TempDir(), "go.work")
	work := fmt.Sprintf(`go 1.25.0

use (
	%s
	%s
	%s
)

replace github.com/eleven-am/golem/go v0.0.0 => %s
replace github.com/eleven-am/golem/go/examples/social v0.0.0 => %s
`, filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample), filepath.ToSlash(canonicalConsumer), filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample))
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := setEnvironment(os.Environ(), "GOWORK", workspace)
	cli := filepath.Join(t.TempDir(), "golem")
	runProcess(t, canonicalRoot, environment, "go", "build", "-o", cli, "./cmd/golem")

	for _, profile := range requiredProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn, cleanup := provisionProfile(t, profile)
			defer cleanup()
			runProcess(t, canonicalExample, environment, cli, "migration", "apply", "--provider", profile.provider, "--dsn", dsn, "--migrations", "migrations")
			childEnvironment := setEnvironment(environment, "P8_ORACLE_PROVIDER", profile.provider)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_DSN", dsn)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_EXAMPLE", canonicalExample)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_CLI", cli)
			execute(t, canonicalConsumer, childEnvironment)
		})
	}
}

func validateOracleSource(t testing.TB, source []byte) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "oracle_test.go", source, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse external oracle source: %v", err)
	}
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatalf("decode external oracle import: %v", err)
		}
		if strings.Contains(path, "/internal/") {
			t.Fatalf("external oracle imports internal package %q", path)
		}
	}
}

func repositoryPaths(t testing.TB) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate p8 oracle package")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filename)))
	return root, filepath.Join(root, "examples", "social")
}

func canonicalPath(t testing.TB, path string) string {
	t.Helper()
	value, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func requiredProfiles() []liveProfile {
	c := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	if c == "" {
		c = "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable"
	}
	linguistic := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))
	if linguistic == "" {
		linguistic = "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable"
	}
	return []liveProfile{
		{name: "sqlite", provider: "sqlite"},
		{name: "postgresql-c", provider: "postgresql", baseDSN: c, collation: "C", ctype: "C"},
		{name: "postgresql-linguistic", provider: "postgresql", baseDSN: linguistic, collation: "linguistic"},
	}
}

func provisionProfile(t testing.TB, profile liveProfile) (string, func()) {
	t.Helper()
	if profile.provider == "sqlite" {
		location := &url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(t.TempDir(), "oracle.sqlite"))}
		return location.String(), func() {}
	}
	admin, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: profile.baseDSN})
	if err != nil {
		if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" {
			t.Fatalf("required %s profile is unavailable", profile.name)
		}
		t.Skip(profile.name + " profile is unavailable")
	}
	var collate, ctype string
	if err := admin.UnsafeSQLX().QueryRowxContext(context.Background(), `SELECT datcollate,datctype FROM pg_database WHERE datname=current_database()`).Scan(&collate, &ctype); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	if profile.collation == "C" && (collate != "C" || ctype != "C") {
		_ = admin.Close()
		t.Fatalf("%s has collation=%q ctype=%q", profile.name, collate, ctype)
	}
	if profile.collation == "linguistic" && (collate == "C" || ctype == "C") {
		_ = admin.Close()
		t.Fatalf("%s requires non-C collation and ctype; got collation=%q ctype=%q", profile.name, collate, ctype)
	}
	name := fmt.Sprintf("p8_read_%d_%d", os.Getpid(), time.Now().UnixNano())
	statement := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 LC_COLLATE %s LC_CTYPE %s", postgresIdentifier(name), postgresLiteral(collate), postgresLiteral(ctype))
	if _, err := admin.UnsafeSQLX().ExecContext(context.Background(), statement); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(profile.baseDSN)
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed.Path, parsed.RawPath = "/"+name, ""
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.UnsafeSQLX().ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, name)
		if _, err := admin.UnsafeSQLX().ExecContext(ctx, "DROP DATABASE "+postgresIdentifier(name)); err != nil {
			t.Errorf("drop disposable PostgreSQL database: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL administrator: %v", err)
		}
	}
	return parsed.String(), cleanup
}

func postgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func postgresLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func setEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func runProcess(t testing.TB, directory string, environment []string, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external oracle command failed: %s %s: %v\n%s", filepath.Base(executable), strings.Join(redactedArguments(arguments), " "), err, output)
	}
	return string(output)
}

func redactedArguments(arguments []string) []string {
	result := append([]string(nil), arguments...)
	for index := range result {
		if index > 0 && result[index-1] == "--dsn" {
			result[index] = "<redacted>"
		}
	}
	return result
}
