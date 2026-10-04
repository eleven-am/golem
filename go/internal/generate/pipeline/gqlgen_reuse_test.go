package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	graphqlcodegen "github.com/eleven-am/golem/go/internal/graphql/codegen"
)

var gqlgenWorkspace = regexp.MustCompile(`/golemgqlgentmp[0-9]+ :: `)

func TestBuildRunsPinnedGQLGenOncePerBuildAndOncePerSharedCache(t *testing.T) {
	log := recordGoInvocations(t)

	alone, err := Build(context.Background(), multipackageRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if runs := gqlgenRuns(t, log); runs != 1 {
		t.Fatalf("one Build ran gqlgen %d times, want 1", runs)
	}

	cache := &graphqlcodegen.ExecutableCache{}
	shared := multipackageRequest(t)
	shared.GraphQLExecutables = cache
	first, err := Build(context.Background(), shared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), shared)
	if err != nil {
		t.Fatal(err)
	}
	if runs := gqlgenRuns(t, log); runs != 1 {
		t.Fatalf("two Builds sharing a cache ran gqlgen %d times, want 1", runs)
	}
	if got := cache.Generations(); got != 1 {
		t.Fatalf("shared cache generations = %d, want 1", got)
	}
	assertManifestResultsEqual(t, alone.Prospective, first.Prospective)
	assertManifestResultsEqual(t, alone.Prospective, second.Prospective)
}

func recordGoInvocations(t *testing.T) string {
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

func gqlgenRuns(t *testing.T, log string) int {
	t.Helper()
	return goInvocations(t, log).gqlgenRuns
}

type goInvocationCounts struct {
	gqlgenRuns          int
	schemaLoads         int
	prospectiveCompiles int
}

var (
	schemaLoad         = regexp.MustCompile(` :: list -e -json \S+$`)
	prospectiveModfile = regexp.MustCompile(`\.golem-prospective-[^ ]*\.mod`)
)

func goInvocations(t *testing.T, log string) goInvocationCounts {
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
	counts := goInvocationCounts{}
	for _, line := range strings.Split(string(content), "\n") {
		if location := gqlgenWorkspace.FindStringIndex(line); location != nil {
			workspaces[line[:location[1]]] = true
		}
		if schemaLoad.MatchString(line) {
			counts.schemaLoads++
		}
		if modfile := prospectiveModfile.FindString(line); modfile != "" {
			modfiles[modfile] = true
		}
	}
	counts.gqlgenRuns, counts.prospectiveCompiles = len(workspaces), len(modfiles)
	return counts
}
