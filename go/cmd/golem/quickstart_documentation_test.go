package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const quickstartGolemCommands = 4

func TestQuickstartFromEmptyDirectory(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	source, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "golem-go", "QUICKSTART.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(source)

	files := quickstartDocumentedFiles(t, document)
	for _, required := range []string{"notes/schema.go", "notes/policies.go", "cmd/notes/main.go"} {
		if files[required] == "" {
			t.Fatalf("QUICKSTART.md documents no %s block", required)
		}
	}
	commands := quickstartDocumentedCommands(document)
	if len(commands) != quickstartGolemCommands {
		t.Fatalf("documented golem commands=%d want=%d: %v", len(commands), quickstartGolemCommands, commands)
	}

	application := t.TempDir()
	for path, body := range files {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golemModule, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/notes\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")

	for index, command := range commands {
		arguments := strings.Fields(command)[1:]
		for position := range arguments {
			arguments[position] = strings.Trim(arguments[position], `"`)
			if arguments[position] == "file:notes.db" {
				arguments[position] = "file:" + filepath.ToSlash(filepath.Join(application, "notes.db"))
			}
		}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), application, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("documented command %d (%s) exited %d\nstdout:\n%s\nstderr:\n%s", index+1, command, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(application, "notes.db")); err != nil {
		t.Fatalf("the documented journey created no database: %v", err)
	}

	quickstartGo(t, application, "mod", "tidy")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	if !strings.Contains(output, `first title="first note" present=true`) {
		t.Fatalf("the documented program printed %q", output)
	}
}

func quickstartDocumentedFiles(t *testing.T, document string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(document))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "```go" {
			continue
		}
		var body []string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "```" {
				break
			}
			body = append(body, line)
		}
		if len(body) == 0 || !strings.HasPrefix(strings.TrimSpace(body[0]), "// ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[0]), "// "))
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		files[path] = strings.Join(body[1:], "\n") + "\n"
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return files
}

func quickstartDocumentedCommands(document string) []string {
	var commands []string
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "golem ") {
			commands = append(commands, trimmed)
		}
	}
	return commands
}

func quickstartGo(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func TestGuideApplicationRuns(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	source, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "golem-go", "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	files := quickstartDocumentedFiles(t, string(source))
	for _, required := range []string{"notes/schema.go", "notes/policies.go", "cmd/notes/main.go"} {
		if files[required] == "" {
			t.Fatalf("GUIDE.md documents no %s block", required)
		}
	}

	application := t.TempDir()
	for path, body := range files {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golemModule, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/notes\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")

	database := "file:" + filepath.ToSlash(filepath.Join(application, "notes.db"))
	for _, arguments := range [][]string{
		{"migration", "new", "--schema", "./notes", "--name", "init"},
		{"generate", "--schema", "./notes", "--app-out", "./notes"},
		{"migration", "apply", "--provider", "sqlite", "--dsn", database},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), application, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, code, stdout.String(), stderr.String())
		}
	}
	quickstartGo(t, application, "mod", "tidy")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	if !strings.Contains(output, "author=2 anonymous=1 drafts=1") {
		t.Fatalf("the documented program printed %q", output)
	}
}

func TestQueueApplicationRuns(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	source, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "golem-go", "QUEUE.md"))
	if err != nil {
		t.Fatal(err)
	}
	files := quickstartDocumentedFiles(t, string(source))
	for _, required := range []string{"notes/schema.go", "notes/policies.go", "cmd/notes/main.go"} {
		if files[required] == "" {
			t.Fatalf("QUEUE.md documents no %s block", required)
		}
	}

	application := t.TempDir()
	for path, body := range files {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golemModule, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/notes\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")

	database := "file:" + filepath.ToSlash(filepath.Join(application, "notes.db"))
	for _, arguments := range [][]string{
		{"migration", "new", "--schema", "./notes", "--name", "init"},
		{"generate", "--schema", "./notes", "--app-out", "./notes"},
		{"migration", "apply", "--provider", "sqlite", "--dsn", database},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), application, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, code, stdout.String(), stderr.String())
		}
	}
	quickstartGo(t, application, "mod", "tidy")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	for _, expected := range []string{"welcome=succeeded attempt=1", "flaky=succeeded attempt=2"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func TestSemanticApplicationRuns(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	source, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "golem-go", "SEMANTIC.md"))
	if err != nil {
		t.Fatal(err)
	}
	files := quickstartDocumentedFiles(t, string(source))
	for _, required := range []string{"notes/schema.go", "notes/policies.go", "cmd/notes/main.go"} {
		if files[required] == "" {
			t.Fatalf("SEMANTIC.md documents no %s block", required)
		}
	}

	application := t.TempDir()
	for path, body := range files {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golemModule, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/notes\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")

	database := "file:" + filepath.ToSlash(filepath.Join(application, "notes.db"))
	for _, arguments := range [][]string{
		{"migration", "new", "--schema", "./notes", "--name", "init"},
		{"generate", "--schema", "./notes", "--app-out", "./notes"},
		{"migration", "apply", "--provider", "sqlite", "--dsn", database},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), application, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, code, stdout.String(), stderr.String())
		}
	}
	quickstartGo(t, application, "mod", "tidy")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	for _, expected := range []string{"search returned 3", "similar returned 2"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func TestRenderApplicationRuns(t *testing.T) {
	moduleRoot := commandModuleRoot(t)
	source, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "golem-go", "RENDER.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(source)
	files := quickstartDocumentedFiles(t, document)
	if files["cmd/site/main.go"] == "" {
		t.Fatal("RENDER.md documents no cmd/site/main.go block")
	}
	shell := quickstartDocumentedShell(t, document)

	application := t.TempDir()
	for path, body := range map[string]string{"cmd/site/main.go": files["cmd/site/main.go"], "public/index.html": shell} {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golemModule, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/site\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")

	output := quickstartGo(t, application, "run", "./cmd/site")
	for _, expected := range []string{`/n/42 title="Note 42" cache="no-cache"`, `/n/missing title="Notes" cache="no-cache"`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func quickstartDocumentedShell(t *testing.T, document string) string {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(document))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "```html" {
			continue
		}
		var body []string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "```" {
				return strings.Join(body, "\n") + "\n"
			}
			body = append(body, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("the page documents no html shell block")
	return ""
}
