package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var (
	commandSchemaLoad         = regexp.MustCompile(` :: list -e -json \S+$`)
	commandGQLGenWorkspace    = regexp.MustCompile(`/golemgqlgentmp[0-9]+ :: `)
	commandProspectiveModfile = regexp.MustCompile(`\.golem-prospective-[^ ]*\.mod`)
)

type commandStageCounts struct {
	schemaLoads         int
	gqlgenRuns          int
	prospectiveCompiles int
}

func TestGenerateCheckAndDoctorAnalyseOnceAndEmitOnlyTheReviewedGraph(t *testing.T) {
	module := writeSocialModule(t, false)
	createInitialReviewedMigration(t, module)
	log := recordCommandGoInvocations(t)

	single := runCountedGolem(t, module, log, []string{"migration", "plan"}, 0)
	if single.schemaLoads == 0 || single.gqlgenRuns != 1 {
		t.Fatalf("single-Build migration plan ran stages %+v", single)
	}
	for _, test := range []struct {
		name        string
		args        []string
		prospective int
		output      string
	}{
		{name: "generate", args: []string{"generate", "--schema", ".", "--app-out", "./app"}, prospective: 1, output: `"changed": [`},
		{name: "check", args: []string{"check", "--schema", ".", "--app-out", "./app"}, prospective: 1, output: `"checked": true`},
		{name: "doctor", args: []string{"doctor", "--provider", "sqlite", "--dsn", "file:" + filepath.Join(t.TempDir(), "doctor.db"), "--schema", "."}, prospective: 0, output: "generation: current"},
	} {
		var stdout, stderr bytes.Buffer
		code := runGolem(t, module, test.args, &stdout, &stderr)
		if test.name != "doctor" && code != 0 || !strings.Contains(stdout.String(), test.output) {
			t.Fatalf("%s code=%d stdout=%s stderr=%s", test.name, code, stdout.String(), stderr.String())
		}
		counts := commandInvocations(t, log)
		registry, err := os.ReadFile(filepath.Join(module, "app", "zz_golem_registry.gen.go"))
		if err != nil || strings.Count(string(registry), "GeneratedMigrationManifestDocument(") != 2 {
			t.Fatalf("%s left a registry without both reviewed migration histories: %v", test.name, err)
		}
		if counts.schemaLoads != single.schemaLoads || counts.gqlgenRuns != 1 || counts.prospectiveCompiles != test.prospective {
			t.Fatalf("%s ran stages %+v, want one analysis (%d schema loads), one gqlgen run, and %d prospective compiles", test.name, counts, single.schemaLoads, test.prospective)
		}
	}
}

func TestGenerateAndCheckReportBuildFailuresBeforeMissingHistory(t *testing.T) {
	module := writeSocialModule(t, false)
	if err := os.MkdirAll(filepath.Join(module, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "app", "broken.go"), []byte("package app\n\nvar Broken = UndefinedSymbol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"generate", "check"} {
		var stdout, stderr bytes.Buffer
		code := runGolem(t, module, []string{command, "--schema", ".", "--app-out", "./app"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "prospective generated graph does not compile") || strings.Contains(stderr.String(), "migration history") {
			t.Fatalf("%s code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
		}
	}
}

func recordCommandGoInvocations(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the go invocation recorder is a POSIX shell script")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "invocations.log")
	script := "#!/bin/sh\necho \"$(pwd) :: $*\" >> " + strconv.Quote(log) + "\nexec " + strconv.Quote(goBinary) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func runCountedGolem(t *testing.T, module, log string, args []string, want int) commandStageCounts {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := runGolem(t, module, args, &stdout, &stderr); code != want {
		t.Fatalf("%s code=%d stdout=%s stderr=%s", strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
	return commandInvocations(t, log)
}

func commandInvocations(t *testing.T, log string) commandStageCounts {
	t.Helper()
	content, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := map[string]bool{}
	modfiles := map[string]bool{}
	counts := commandStageCounts{}
	for _, line := range strings.Split(string(content), "\n") {
		if location := commandGQLGenWorkspace.FindStringIndex(line); location != nil {
			workspaces[line[:location[1]]] = true
		}
		if commandSchemaLoad.MatchString(line) {
			counts.schemaLoads++
		}
		if modfile := commandProspectiveModfile.FindString(line); modfile != "" {
			modfiles[modfile] = true
		}
	}
	counts.gqlgenRuns, counts.prospectiveCompiles = len(workspaces), len(modfiles)
	return counts
}
