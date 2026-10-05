package p8oracle

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalFuzzRunsEverySeedAndMutationsDespiteCachedCorpus(t *testing.T) {
	const (
		target    = "FuzzProbe"
		seeds     = 3
		mutations = 20
		cached    = 120
	)
	module := "p8oracle.fuzzprobe" + hex.EncodeToString(randomTestBytes(t))
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "go.mod"), "module "+module+"\n\ngo 1.25.0\n")
	writeTestFile(t, filepath.Join(directory, "probe_test.go"), `package probe

import "testing"

func FuzzProbe(f *testing.F) {
	f.Add([]byte{1})
	f.Add([]byte{2})
	f.Add([]byte{3})
	f.Fuzz(func(t *testing.T, input []byte) {
		_ = len(input)
	})
}
`)
	environment := setEnvironment(os.Environ(), "GOWORK", "off")
	cache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(strings.TrimSpace(string(cache)), "fuzz", module)
	t.Cleanup(func() { _ = os.RemoveAll(moduleCache) })
	targetCache := filepath.Join(moduleCache, target)
	if err := os.MkdirAll(targetCache, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := range cached {
		writeTestFile(t, filepath.Join(targetCache, fmt.Sprintf("cached%03d", index)),
			fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", fmt.Sprintf("cached-%03d", index)))
	}

	output := runExternalFuzzProcess(t, directory, environment, target, seeds, mutations)
	corpus, executions, err := externalFuzzExecutions(output)
	if err != nil {
		t.Fatal(err)
	}
	if corpus != seeds || executions-corpus < mutations {
		t.Fatalf("corpus=%d mutations=%d", corpus, executions-corpus)
	}
}

func TestExternalFuzzBudgetIsCountedAndSpentOnlyOnSeedsAndMutations(t *testing.T) {
	arguments := externalFuzzArguments("FuzzTarget", 10, 100, "corpus")
	flags := map[string]string{}
	for index := 0; index+1 < len(arguments); index++ {
		flags[arguments[index]] = arguments[index+1]
	}
	for flag, want := range map[string]string{
		"-fuzz":             "^FuzzTarget$",
		"-fuzztime":         "110x",
		"-fuzzminimizetime": "0x",
		"-parallel":         "1",
	} {
		if flags[flag] != want {
			t.Fatalf("%s = %q, want %q in %q", flag, flags[flag], want, arguments)
		}
	}
	if arguments[len(arguments)-1] != "-test.fuzzcachedir=corpus" {
		t.Fatalf("the disposable corpus must be the final child flag so it overrides the go command's cache: %q", arguments)
	}
}

func TestExternalFuzzExecutionsRejectsRunsThatNeverFuzz(t *testing.T) {
	for name, output := range map[string]string{
		"baseline consumed the budget": "fuzz: elapsed: 0s, gathering baseline coverage: 0/200 completed\n" +
			"fuzz: elapsed: 9s, gathering baseline coverage: 110/200 completed\nPASS",
		"no coordinator report": "PASS",
	} {
		if _, _, err := externalFuzzExecutions(output); err == nil {
			t.Fatalf("%s: accepted %q", name, output)
		}
	}
	corpus, executions, err := externalFuzzExecutions("    fuzz: elapsed: 0s, gathering baseline coverage: 0/10 completed\n" +
		"    fuzz: elapsed: 4s, gathering baseline coverage: 10/10 completed, now fuzzing with 1 workers\n" +
		"    fuzz: elapsed: 6s, execs: 110 (34/sec), new interesting: 1 (total: 11)\n")
	if err != nil || corpus != 10 || executions != 110 {
		t.Fatalf("corpus=%d executions=%d err=%v", corpus, executions, err)
	}
}

func randomTestBytes(t *testing.T) []byte {
	t.Helper()
	value := make([]byte, 8)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
