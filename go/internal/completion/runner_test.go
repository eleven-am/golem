package completion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerRequiresEveryTestAndRejectsEverySkip(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		root := completionTestModule(t, `package completiontest
import (
    "os"
    "testing"
)
func TestP8Pass(t *testing.T) {
    if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") != "1" || os.Getenv("P8_COMPLETION_PROBE") != "present" || os.Getenv("GOWORK") != "off" {
        t.Fatal("completion environment missing")
    }
}`)
		evidence, err := Run(context.Background(), completionTestSpec(root, []string{"TestP8Pass"}, time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if evidence.FormatVersion != 1 || evidence.Command != "p8docs" || evidence.Status != "PASS" || evidence.RequiredTests != 1 || evidence.PassedTests != 1 || evidence.PassedPackages != 1 || len(evidence.TestEventSHA256) != 64 || len(evidence.TreeSHA256) != 64 {
			t.Fatalf("evidence = %#v", evidence)
		}
	})

	t.Run("skip", func(t *testing.T) {
		root := completionTestModule(t, `package completiontest
import "testing"
func TestP8Skip(t *testing.T) { t.Skip("must not become completion") }`)
		_, err := Run(context.Background(), completionTestSpec(root, []string{"TestP8Skip"}, time.Minute))
		assertCompletionCode(t, err, CodeSkip)
	})

	t.Run("missing", func(t *testing.T) {
		root := completionTestModule(t, `package completiontest
import "testing"
func TestP8Present(t *testing.T) {}`)
		_, err := Run(context.Background(), completionTestSpec(root, []string{"TestP8Missing"}, time.Minute))
		assertCompletionCode(t, err, CodeMissing)
	})
}

func TestRunnerRejectsTimeoutAndReadOnlyTreeMutation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		root := completionTestModule(t, `package completiontest
import (
    "testing"
    "time"
)
func TestP8Timeout(t *testing.T) { time.Sleep(5*time.Second) }`)
		_, err := Run(context.Background(), completionTestSpec(root, []string{"TestP8Timeout"}, 100*time.Millisecond))
		assertCompletionCode(t, err, CodeTimeout)
	})

	t.Run("tree mutation", func(t *testing.T) {
		root := completionTestModule(t, `package completiontest
import (
    "os"
    "testing"
)
func TestP8Mutation(t *testing.T) {
    if err := os.WriteFile("mutation-canary", []byte("changed"), 0o600); err != nil { t.Fatal(err) }
}`)
		_, err := Run(context.Background(), completionTestSpec(root, []string{"TestP8Mutation"}, time.Minute))
		assertCompletionCode(t, err, CodeTreeChanged)
	})
}

func TestRunnerEvidenceAndFailuresAreClosed(t *testing.T) {
	root := completionTestModule(t, `package completiontest
import "testing"
func TestP8Pass(t *testing.T) {}`)
	spec := completionTestSpec(root, []string{"TestP8Pass"}, time.Minute)
	spec.Env = append(spec.Env, "P8_COMPLETION_SECRET=postgresql://credential-canary@private/database")
	evidence, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{root, "postgresql://", "credential-canary", "private/database"} {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("evidence disclosed %q: %s", canary, encoded)
		}
	}

	failure := (&Error{Code: CodeTestFailure}).Error()
	if failure != string(CodeTestFailure) || strings.Contains(failure, root) {
		t.Fatalf("failure is not closed: %q", failure)
	}
}

func completionTestSpec(root string, tests []string, timeout time.Duration) Spec {
	return Spec{
		Command:   "p8docs",
		ModuleDir: root,
		Packages: []Package{{
			Path:       "./...",
			ImportPath: "example.com/completion",
			Tests:      tests,
		}},
		Profiles:   []string{"postgresql-c", "postgresql-linguistic", "sqlite"},
		Timeout:    timeout,
		WatchPaths: []string{root},
		Env:        []string{"P8_COMPLETION_PROBE=present"},
	}
}

func completionTestModule(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/completion\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "completion_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertCompletionCode(t *testing.T, err error, code Code) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code || err.Error() != string(code) {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}
