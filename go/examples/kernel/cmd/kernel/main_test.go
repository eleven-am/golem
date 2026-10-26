package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

var (
	cliOnce      sync.Once
	cliDirectory string
	cliBinary    string
	cliErr       error
	cliOutput    []byte
)

func TestMain(m *testing.M) {
	code := m.Run()
	if cliDirectory != "" {
		_ = os.RemoveAll(cliDirectory)
	}
	os.Exit(code)
}

func TestKernelScenarioSQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "kernel.sqlite")
	runKernelCommand(t, kernelRoot(t), golemCLI(t), "migration", "apply", "--provider", "sqlite", "--dsn", dsn, "--migrations", "migrations")
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runKernelScenario(t, database)
}

func TestKernelScenarioPostgreSQL(t *testing.T) {
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	runKernelCommand(t, kernelRoot(t), golemCLI(t), "migration", "apply", "--provider", "postgresql", "--dsn", dsn, "--migrations", "migrations")
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runKernelScenario(t, database)
}

func runKernelScenario(t *testing.T, database *provider.Database) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := runScenario(ctx, database); err != nil {
		t.Fatal(err)
	}
}

func kernelRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate kernel example source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filename)))
}

func frameworkRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(filepath.Join(kernelRoot(t), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func golemCLI(t *testing.T) string {
	t.Helper()
	framework := frameworkRoot(t)
	cliOnce.Do(func() {
		cliDirectory, cliErr = os.MkdirTemp("", "golem-kernel-cli-")
		if cliErr != nil {
			return
		}
		cliBinary = filepath.Join(cliDirectory, "golem")
		build := exec.Command("go", "build", "-o", cliBinary, "./cmd/golem")
		build.Dir = framework
		build.Env = kernelEnvironment(os.Environ(), "GOWORK", "off")
		cliOutput, cliErr = build.CombinedOutput()
	})
	if cliErr != nil {
		t.Fatalf("build golem CLI: %v\n%s", cliErr, cliOutput)
	}
	return cliBinary
}

func runKernelCommand(t *testing.T, directory, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	command.Env = kernelEnvironment(os.Environ(), "GOWORK", "off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func kernelEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
