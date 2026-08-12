package p8mutation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestP8MutationEngineKillsExactNamedGateAndRejectsNoOp(t *testing.T) {
	repository := t.TempDir()
	writeMutationFixture(t, repository, "go/go.mod", "module example.test/mutant\n\ngo 1.25.0\n")
	writeMutationFixture(t, repository, "go/sample.go", "package sample\nconst answer = 42\n")
	writeMutationFixture(t, repository, "go/sample_test.go", "package sample\nimport \"testing\"\nfunc TestExactGate(t *testing.T) { if answer != 42 { t.Fatalf(\"answer=%d\", answer) } }\n")
	writeMutationFixture(t, repository, "docs/golem-go/placeholder", "docs\n")
	writeMutationFixture(t, repository, ".github/workflows/placeholder.yml", "name: placeholder\n")
	writeMutationFixture(t, repository, "README.md", "readme\n")
	writeMutationFixture(t, repository, "RELEASE_NOTES.md", "notes\n")
	mutation := Mutation{
		Label: "SYNTHETIC_KILL", Summary: "prove isolated runner semantics",
		Patches: []Patch{{Path: "go/sample.go", Before: "answer = 42", After: "answer = 41"}},
		Gate:    Gate{Directory: "go", Package: ".", Test: "TestExactGate"}, Timeout: time.Minute,
	}
	if err := ValidateCatalog([]Mutation{mutation}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(repository, []Mutation{mutation}); err != nil {
		t.Fatal(err)
	}
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusKilled || result.Test != "TestExactGate" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	mutation.Patches[0].After = mutation.Patches[0].Before
	if err := ValidateCatalog([]Mutation{mutation}); err == nil {
		t.Fatal("no-op mutation passed manifest validation")
	}
}

func TestP8MutationEngineRefusesAbsentProfileAndUnknownPatchSite(t *testing.T) {
	mutation := Mutation{
		Label: "PROFILE_REQUIRED", Summary: "prove required profile refusal",
		Patches: []Patch{{Path: "go/missing.go", Before: "before", After: "after"}},
		Gate:    Gate{Directory: "go", Package: ".", Test: "TestExactGate", Required: []string{"GOLEM_P8_ABSENT_PROFILE"}}, Timeout: time.Minute,
	}
	result, err := (Runner{Repository: t.TempDir()}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusInvalid {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestP8MutationCompilationFailureIsInvalidAndOutputIsDigestOnly(t *testing.T) {
	repository := t.TempDir()
	writeMutationFixture(t, repository, "go/go.mod", "module example.test/mutant\n\ngo 1.25.0\n")
	writeMutationFixture(t, repository, "go/sample.go", "package sample\nconst answer = 42\n")
	writeMutationFixture(t, repository, "go/sample_test.go", "package sample\nimport \"testing\"\nfunc TestExactGate(t *testing.T) { if answer != 42 { t.Fatal(\"private-driver-token\") } }\n")
	writeMutationFixture(t, repository, "docs/golem-go/placeholder", "docs\n")
	writeMutationFixture(t, repository, ".github/workflows/placeholder.yml", "name: placeholder\n")
	writeMutationFixture(t, repository, "README.md", "readme\n")
	writeMutationFixture(t, repository, "RELEASE_NOTES.md", "notes\n")
	mutation := Mutation{Label: "BREAKS_COMPILE", Summary: "prove compile failure classification", Patches: []Patch{{Path: "go/sample.go", Before: "answer = 42", After: "answer ="}}, Gate: Gate{Directory: "go", Package: ".", Test: "TestExactGate"}, Timeout: time.Minute}
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusInvalid || len(result.OutputSHA256) != 64 || len(result.BaselineEventSHA256) != 64 || len(result.MutantEventSHA256) != 64 || result.FormatVersion != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestP8MutationEngineRejectsAlreadyFailingBaseline(t *testing.T) {
	repository := t.TempDir()
	writeMutationFixture(t, repository, "go/go.mod", "module example.test/mutant\n\ngo 1.25.0\n")
	writeMutationFixture(t, repository, "go/sample.go", "package sample\nconst answer = 41\n")
	writeMutationFixture(t, repository, "go/sample_test.go", "package sample\nimport \"testing\"\nfunc TestExactGate(t *testing.T) { if answer != 42 { t.Fatal(\"already broken private token\") } }\n")
	writeMutationFixture(t, repository, "docs/golem-go/placeholder", "docs\n")
	writeMutationFixture(t, repository, ".github/workflows/placeholder.yml", "name: placeholder\n")
	writeMutationFixture(t, repository, "README.md", "readme\n")
	writeMutationFixture(t, repository, "RELEASE_NOTES.md", "notes\n")
	mutation := Mutation{Label: "BASELINE_FAILS", Summary: "prove baseline failure classification", Patches: []Patch{{Path: "go/sample.go", Before: "answer = 41", After: "answer = 40"}}, Gate: Gate{Directory: "go", Package: ".", Test: "TestExactGate"}, Timeout: time.Minute}
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusInvalid || result.MutantEventSHA256 != "" || len(result.BaselineEventSHA256) != 64 || len(result.OutputSHA256) != 64 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestP8MutationEngineClearsHostileAmbientWorkspaceAndFlags(t *testing.T) {
	repository := mutationPassingFixture(t)
	workspace := filepath.Join(t.TempDir(), "hostile.work")
	if err := os.WriteFile(workspace, []byte("not a workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutation := Mutation{Label: "HOSTILE_AMBIENT", Summary: "prove ambient Go configuration isolation", Patches: []Patch{{Path: "go/sample.go", Before: "answer = 42", After: "answer = 41"}}, Gate: Gate{Directory: "go", Package: ".", Test: "TestExactGate"}, Timeout: time.Minute}
	t.Setenv("GOWORK", workspace)
	t.Setenv("GOFLAGS", "-this-flag-does-not-exist")
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusKilled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestP8MutationEngineOwnsBuildCacheInsideSandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "mutant")
	hostCache := filepath.Join(t.TempDir(), "host-cache")
	environment := mutationGoEnvironment(
		[]string{"GOCACHE=" + hostCache, "GOWORK=hostile.work", "GOFLAGS=-hostile"},
		[]string{"GOCACHE=" + filepath.Join(t.TempDir(), "caller-cache")},
		sandbox,
	)
	if cache := lookupEnv(environment, "GOCACHE"); cache != filepath.Join(sandbox, ".gocache") {
		t.Fatalf("mutation cache=%q want sandbox-owned cache", cache)
	}
	if work := lookupEnv(environment, "GOWORK"); work != "off" {
		t.Fatalf("mutation workspace=%q want off", work)
	}
	if flags := lookupEnv(environment, "GOFLAGS"); flags != "" {
		t.Fatalf("mutation flags=%q want empty", flags)
	}
}

func TestP8MutationEngineRunsExactSocialNestedModuleFromSandboxWorkspace(t *testing.T) {
	repository := t.TempDir()
	writeMutationFixture(t, repository, "go/go.mod", "module github.com/eleven-am/golem/go\n\ngo 1.25.0\n")
	writeMutationFixture(t, repository, "go/root.go", "package golem\nconst RootAnswer = 42\n")
	writeMutationFixture(t, repository, "go/examples/social/go.mod", "module github.com/eleven-am/golem/go/examples/social\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n")
	writeMutationFixture(t, repository, "go/examples/social/sample.go", "package social\nimport golem \"github.com/eleven-am/golem/go\"\nconst localAnswer = 42\nvar rootAnswer = golem.RootAnswer\n")
	writeMutationFixture(t, repository, "go/examples/social/sample_test.go", "package social\nimport \"testing\"\nfunc TestNestedGate(t *testing.T) { if localAnswer != 42 || rootAnswer != 42 { t.Fatal(\"wrong workspace\") } }\n")
	writeMutationFixture(t, repository, "docs/golem-go/placeholder", "docs\n")
	writeMutationFixture(t, repository, ".github/workflows/placeholder.yml", "name: placeholder\n")
	writeMutationFixture(t, repository, "README.md", "readme\n")
	writeMutationFixture(t, repository, "RELEASE_NOTES.md", "notes\n")
	mutation := Mutation{
		Label: "NESTED_SOCIAL_KILL", Summary: "prove the closed social workspace runs from its nested module",
		Patches: []Patch{{Path: "go/examples/social/sample.go", Before: "localAnswer = 42", After: "localAnswer = 41"}},
		Gate: Gate{
			Directory: "go/examples/social", Package: ".", Test: "TestNestedGate",
			WorkspaceModules: []string{"go", "go/examples/social"},
		},
		Timeout: time.Minute,
	}
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusKilled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestP8MutationEngineRejectsEveryOtherGateWorkspace(t *testing.T) {
	base := Mutation{
		Label: "CLOSED_WORKSPACE", Summary: "prove mutation gate workspace scope remains closed",
		Patches: []Patch{{Path: "go/sample.go", Before: "before", After: "after"}},
		Gate:    Gate{Directory: "go", Package: ".", Test: "TestExactGate"},
		Timeout: time.Minute,
	}
	tests := []struct {
		name      string
		directory string
		modules   []string
	}{
		{name: "other nested module", directory: "go/examples/other", modules: []string{"go", "go/examples/other"}},
		{name: "social without workspace", directory: "go/examples/social"},
		{name: "social unknown module", directory: "go/examples/social", modules: []string{"go", "go/examples/unknown"}},
		{name: "social reversed modules", directory: "go/examples/social", modules: []string{"go/examples/social", "go"}},
		{name: "directory traversal", directory: "go/examples/social/../social", modules: []string{"go", "go/examples/social"}},
		{name: "module traversal", directory: "go/examples/social", modules: []string{"go", "go/examples/../social"}},
		{name: "root with workspace", directory: "go", modules: []string{"go", "go/examples/social"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := base
			mutation.Gate.Directory = test.directory
			mutation.Gate.WorkspaceModules = test.modules
			if err := ValidateCatalog([]Mutation{mutation}); err == nil {
				t.Fatal("unsupported mutation workspace passed validation")
			}
		})
	}
}

func TestP8MutationEngineRejectsSymlinkEscapeWithoutChangingOriginalTree(t *testing.T) {
	repository := mutationPassingFixture(t)
	external := filepath.Join(t.TempDir(), "outside.go")
	before := []byte("outside-private-canary\n")
	if err := os.WriteFile(external, before, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repository, "go", "escape.go")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	mutation := Mutation{Label: "SYMLINK_ESCAPE", Summary: "prove sandbox symlinks fail closed", Patches: []Patch{{Path: "go/escape.go", Before: "outside-private-canary", After: "mutated"}}, Gate: Gate{Directory: "go", Package: ".", Test: "TestExactGate"}, Timeout: time.Minute}
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
	if err != nil || result.Status != StatusInvalid || result.Detail != "P8_MUTATION_SYMLINK_REJECTED" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	after, readErr := os.ReadFile(external)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("external file changed: %q err=%v", after, readErr)
	}
	source, readErr := os.ReadFile(filepath.Join(repository, "go", "sample.go"))
	if readErr != nil || string(source) != "package sample\nconst answer = 42\n" {
		t.Fatalf("source tree changed: %q err=%v", source, readErr)
	}
}

func mutationPassingFixture(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	writeMutationFixture(t, repository, "go/go.mod", "module example.test/mutant\n\ngo 1.25.0\n")
	writeMutationFixture(t, repository, "go/sample.go", "package sample\nconst answer = 42\n")
	writeMutationFixture(t, repository, "go/sample_test.go", "package sample\nimport \"testing\"\nfunc TestExactGate(t *testing.T) { if answer != 42 { t.Fatalf(\"answer=%d\", answer) } }\n")
	writeMutationFixture(t, repository, "docs/golem-go/placeholder", "docs\n")
	writeMutationFixture(t, repository, ".github/workflows/placeholder.yml", "name: placeholder\n")
	writeMutationFixture(t, repository, "README.md", "readme\n")
	writeMutationFixture(t, repository, "RELEASE_NOTES.md", "notes\n")
	return repository
}

func writeMutationFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
