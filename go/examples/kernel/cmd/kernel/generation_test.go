package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
)

var forbiddenKernelDependencyPrefixes = []string{
	"github.com/99designs/gqlgen",
	"github.com/vektah/gqlparser",
	"github.com/eleven-am/golem/go/graphql",
	"github.com/eleven-am/golem/go/internal/graphql",
}

func TestExternalKernelApplicationGenerateCheckDoctorBuildAndRun(t *testing.T) {
	source := kernelRoot(t)
	framework := frameworkRoot(t)
	fresh := filepath.Join(t.TempDir(), "kernel")
	copyKernelAuthoredApplication(t, source, fresh, framework)
	cli := golemCLI(t)

	runKernelCommand(t, fresh, cli, "migration", "new", "--schema", "./kernel", "--name", "initial", "--migrations", "migrations")
	runKernelCommand(t, fresh, cli, "generate", "--schema", "./kernel", "--app-out", "./kernel", "--migrations", "migrations")
	checked := runKernelCommand(t, fresh, cli, "check", "--schema", "./kernel", "--app-out", "./kernel", "--migrations", "migrations")
	if !strings.Contains(checked, `"checked": true`) {
		t.Fatalf("fresh kernel application was not current: %s", checked)
	}
	assertNoGraphQLArtifacts(t, fresh)

	dependencies := runKernelCommand(t, fresh, "go", "list", "-deps", "./...")
	for _, line := range strings.Split(strings.TrimSpace(dependencies), "\n") {
		for _, prefix := range forbiddenKernelDependencyPrefixes {
			if line == prefix || strings.HasPrefix(line, prefix+"/") {
				t.Errorf("kernel application depends on %s", line)
			}
		}
	}
	runKernelCommand(t, fresh, "go", "mod", "tidy", "-diff")
	module, err := os.ReadFile(filepath.Join(fresh, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(module), "gqlgen") || strings.Contains(string(module), "gqlparser") {
		t.Fatalf("kernel go.mod requires GraphQL modules:\n%s", module)
	}

	databasePath := filepath.Join(t.TempDir(), "kernel.sqlite")
	runKernelCommand(t, fresh, cli, "migration", "apply", "--provider", "sqlite", "--dsn", databasePath, "--migrations", "migrations")
	var doctor struct {
		Generation  string `json:"generation"`
		Schema      string `json:"schema"`
		History     string `json:"history"`
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	report := runKernelCommand(t, fresh, cli, "doctor", "--schema", "./kernel", "--provider", "sqlite", "--dsn", databasePath, "--migrations", "migrations", "--json")
	if err := json.Unmarshal([]byte(report), &doctor); err != nil {
		t.Fatalf("decode doctor report: %v\n%s", err, report)
	}
	if doctor.Generation != "current" || doctor.Schema != "current" || doctor.History != "current" {
		t.Fatalf("kernel doctor report: %s", report)
	}

	binary := filepath.Join(t.TempDir(), "kernel")
	runKernelCommand(t, fresh, "go", "build", "-o", binary, "./cmd/kernel")
	run := exec.Command(binary)
	run.Dir = fresh
	run.Env = kernelEnvironment(kernelEnvironment(os.Environ(), "GOLEM_PROVIDER", "sqlite"), "GOLEM_DATABASE_DSN", "file:"+databasePath)
	output, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "kernel scenario passed") {
		t.Fatalf("kernel binary: %v\n%s", err, output)
	}
}

func assertNoGraphQLArtifacts(t *testing.T, root string) {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest.DefaultPath)))
	if err != nil {
		t.Fatal(err)
	}
	published, err := manifest.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Artifacts) == 0 {
		t.Fatal("kernel generation published no artifacts")
	}
	for _, entry := range published.Artifacts {
		if entry.Kind == manifest.ArtifactGraphQLGo || entry.Kind == manifest.ArtifactGraphQLSDL {
			t.Errorf("kernel generation published GraphQL artifact %s (%s)", entry.Path, entry.Kind)
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(strings.ToLower(entry.Name()), "graphql") || entry.Name() == "golemgqlgen" {
			t.Errorf("kernel generation left %s on disk", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyKernelAuthoredApplication(t *testing.T, source, destination, framework string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		slash := filepath.ToSlash(relative)
		if entry.IsDir() {
			if slash == ".golem" || slash == "migrations" {
				return filepath.SkipDir
			}
			return nil
		}
		authored := slash == "go.mod" || slash == "go.sum" || slash == "cmd/kernel/main.go" ||
			strings.HasPrefix(slash, "kernel/") && strings.HasSuffix(slash, ".go") && !strings.HasSuffix(slash, "_test.go") && !strings.HasPrefix(entry.Name(), "zz_golem_")
		if !authored {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if slash == "go.mod" {
			replaced := strings.Replace(string(data), "=> ../..", "=> "+filepath.ToSlash(framework), 1)
			if replaced == string(data) {
				t.Fatalf("kernel go.mod has no framework replace:\n%s", data)
			}
			data = []byte(replaced)
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
