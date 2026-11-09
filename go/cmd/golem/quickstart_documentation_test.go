package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/testenv"
)

const quickstartGolemCommands = 4

var (
	notesDocumentedFiles   = []string{"notes/schema.go", "notes/policies.go", "cmd/notes/main.go"}
	durableDocumentedFiles = []string{"durable/schema.go", "durable/exports.go", "cmd/durable/main.go"}
	durablePrefixes        = []string{"durable/", "cmd/durable/"}
)

func TestQuickstartFromEmptyDirectory(t *testing.T) {
	t.Parallel()
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

	application := writeDocumentedModule(t, "notes", files)

	for index, command := range commands {
		arguments := strings.Fields(command)[1:]
		for position := range arguments {
			arguments[position] = strings.Trim(arguments[position], `"`)
			if arguments[position] == "file:notes.db" {
				arguments[position] = "file:" + filepath.ToSlash(filepath.Join(application, "notes.db"))
			}
		}
		var stdout, stderr bytes.Buffer
		if code := runGolem(t, application, arguments, &stdout, &stderr); code != 0 {
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

func quickstartFilesUnder(files map[string]string, prefixes ...string) map[string]string {
	selected := make(map[string]string)
	for path, body := range files {
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				selected[path] = body
			}
		}
	}
	return selected
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
	t.Parallel()
	application := buildDocumentedApplication(t, "GUIDE.md", nil, notesDocumentedFiles, "notes", "sqlite", "notes.db")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	if !strings.Contains(output, "author=2 anonymous=1 drafts=1") {
		t.Fatalf("the documented program printed %q", output)
	}
}

func TestQueueApplicationRuns(t *testing.T) {
	t.Parallel()
	application := buildDocumentedApplication(t, "QUEUE.md", []string{"notes/", "cmd/notes/"}, notesDocumentedFiles, "notes", "sqlite", "notes.db")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	for _, expected := range []string{"welcome=succeeded attempt=1", "flaky=succeeded attempt=2"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func TestDurableActionsRecipeRuns(t *testing.T) {
	t.Parallel()
	application := buildDocumentedApplication(t, "QUEUE.md", durablePrefixes, durableDocumentedFiles, "durable", "sqlite", "durable.db")
	output := quickstartGo(t, application, "run", "./cmd/durable")
	for _, expected := range []string{"export=report status=succeeded code=\n", "export=failing status=failed code=attempts_exhausted\n", "export=canceled status=canceled code=\n"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func TestDurableActionsRecipeOnPostgreSQL(t *testing.T) {
	t.Parallel()
	database := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	application := buildDocumentedApplication(t, "QUEUE.md", durablePrefixes, durableDocumentedFiles, "durable", "postgresql", database)
	child, err := os.ReadFile(filepath.Join("testdata", "durable_recipe_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(application, "durable", "recipe_test.go"), child, 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")
	command := exec.Command("go", "test", "./durable", "-run", ".", "-count=1")
	command.Dir = application
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "DURABLE_DSN="+database)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the documented recipe's tests failed: %v\n%s", err, output)
	}
}

func TestSemanticApplicationRuns(t *testing.T) {
	t.Parallel()
	application := buildDocumentedApplication(t, "SEMANTIC.md", nil, notesDocumentedFiles, "notes", "sqlite", "notes.db")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	for _, expected := range []string{"search returned 3", "similar returned 2"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("the documented program printed %q, want %q", output, expected)
		}
	}
}

func TestFullTextApplicationRuns(t *testing.T) {
	t.Parallel()
	application := buildDocumentedApplication(t, "FULLTEXT.md", nil, notesDocumentedFiles, "notes", "sqlite", "notes.db")
	output := quickstartGo(t, application, "run", "./cmd/notes")
	if !strings.Contains(output, "search returned 1: Renée's invoice") {
		t.Fatalf("the documented program printed %q", output)
	}
}

func TestRenderApplicationRuns(t *testing.T) {
	t.Parallel()
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

	application := writeDocumentedModule(t, "site", map[string]string{"cmd/site/main.go": files["cmd/site/main.go"], "public/index.html": shell})

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

func writeDocumentedModule(t *testing.T, name string, files map[string]string) string {
	t.Helper()
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
	golemModule, err := filepath.Abs(commandModuleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/" + name + "\n\ngo 1.26.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n"
	if err := os.WriteFile(filepath.Join(application, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	quickstartGo(t, application, "mod", "tidy")
	return application
}

func buildDocumentedApplication(t *testing.T, document string, prefixes, required []string, schema, provider, database string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(commandModuleRoot(t), "..", "docs", "golem-go", document))
	if err != nil {
		t.Fatal(err)
	}
	files := quickstartDocumentedFiles(t, string(source))
	if len(prefixes) > 0 {
		files = quickstartFilesUnder(files, prefixes...)
	}
	for _, path := range required {
		if files[path] == "" {
			t.Fatalf("%s documents no %s block", document, path)
		}
	}
	application := writeDocumentedModule(t, "notes", files)
	if provider == "sqlite" {
		database = "file:" + filepath.ToSlash(filepath.Join(application, database))
	}
	for _, arguments := range [][]string{
		{"migration", "new", "--schema", "./" + schema, "--name", "init"},
		{"generate", "--schema", "./" + schema, "--app-out", "./" + schema},
		{"migration", "apply", "--provider", provider, "--dsn", database},
	} {
		var stdout, stderr bytes.Buffer
		if code := runGolem(t, application, arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, code, stdout.String(), stderr.String())
		}
	}
	quickstartGo(t, application, "mod", "tidy")
	return application
}
