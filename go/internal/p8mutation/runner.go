package p8mutation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Runner struct {
	Repository string
	Timeout    time.Duration
	Keep       bool
	Env        []string
}

func (runner Runner) Run(ctx context.Context, mutation Mutation) (Result, error) {
	result := Result{FormatVersion: 1, Mutation: mutation.Label, Test: mutation.Gate.Test}
	if err := validateMutation(mutation); err != nil {
		result.Status, result.Detail = StatusInvalid, err.Error()
		return result, nil
	}
	for _, required := range mutation.Gate.Required {
		if lookupEnv(runner.Env, required) == "" {
			result.Status, result.Detail = StatusInvalid, "required profile is absent: "+required
			return result, nil
		}
	}
	repository, err := filepath.Abs(runner.Repository)
	if err != nil {
		return result, err
	}
	sandbox, err := os.MkdirTemp("", "golem-p8-mutant-"+strings.ToLower(mutation.Label)+"-")
	if err != nil {
		return result, err
	}
	if !runner.Keep {
		defer os.RemoveAll(sandbox)
	}
	for _, subtree := range []string{"go", "docs/golem-go", ".github/workflows"} {
		if err := copyTree(filepath.Join(repository, filepath.FromSlash(subtree)), filepath.Join(sandbox, filepath.FromSlash(subtree))); err != nil {
			if err.Error() == "P8_MUTATION_SYMLINK_REJECTED" {
				result.Status, result.Detail = StatusInvalid, err.Error()
				return result, nil
			}
			return result, err
		}
	}
	for _, name := range []string{"README.md", "RELEASE_NOTES.md"} {
		if err := copyFile(filepath.Join(repository, name), filepath.Join(sandbox, name)); err != nil {
			return result, err
		}
	}
	timeout := mutation.Timeout
	if runner.Timeout > 0 && (timeout <= 0 || runner.Timeout < timeout) {
		timeout = runner.Timeout
	}
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	started := time.Now()
	baseline, err := runner.runGate(ctx, sandbox, mutation, timeout)
	result.BaselineEventSHA256 = baseline.digest
	if err != nil {
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		result.Status, result.Detail = StatusInvalid, err.Error()
		result.OutputSHA256 = baseline.digest
		return result, nil
	}
	if !baseline.exactPass || !baseline.packagePass || baseline.exactFail || baseline.exactSkip || baseline.packageFail {
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		result.Status, result.Detail = StatusInvalid, "unmutated baseline did not pass the exact named gate"
		result.OutputSHA256 = baseline.digest
		return result, nil
	}
	for _, patch := range mutation.Patches {
		if err := applyPatch(sandbox, patch); err != nil {
			result.Duration = time.Since(started).Round(time.Millisecond).String()
			result.Status, result.Detail = StatusInvalid, err.Error()
			return result, nil
		}
	}
	mutant, err := runner.runGate(ctx, sandbox, mutation, timeout)
	result.MutantEventSHA256 = mutant.digest
	result.Duration = time.Since(started).Round(time.Millisecond).String()
	if err != nil {
		result.Status, result.Detail = StatusInvalid, err.Error()
		result.OutputSHA256 = mutant.digest
		return result, nil
	}
	if mutant.exactPass && mutant.packagePass && !mutant.exactFail && !mutant.packageFail {
		result.Status = StatusSurvived
		return result, nil
	}
	if mutant.exactFail && mutant.packageFail && !mutant.exactSkip {
		result.Status = StatusKilled
		return result, nil
	}
	result.Status, result.Detail = StatusInvalid, "mutant gate did not produce exact test and package failure events"
	result.OutputSHA256 = mutant.digest
	return result, nil
}

type gateOutcome struct {
	digest      string
	exactPass   bool
	exactFail   bool
	exactSkip   bool
	packagePass bool
	packageFail bool
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func (runner Runner) runGate(ctx context.Context, sandbox string, mutation Mutation, timeout time.Duration) (gateOutcome, error) {
	testContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(testContext, "go", "test", "-json", "-vet=off", "-count=1", mutation.Gate.Package, "-run", "^"+regexp.QuoteMeta(mutation.Gate.Test)+"$")
	command.Dir = filepath.Join(sandbox, filepath.FromSlash(mutation.Gate.Directory))
	// A mutation sandbox has a unique source path. Letting those builds enter the
	// host cache retains a nearly complete compilation graph for every catalog
	// entry and can exhaust the release worker. Keep the cache inside the owned
	// sandbox so the baseline and mutant share it and normal sandbox cleanup
	// removes it after the result is recorded.
	command.Env = mutationGoEnvironment(os.Environ(), runner.Env, sandbox)
	if len(mutation.Gate.WorkspaceModules) != 0 {
		workspace := filepath.Join(sandbox, "go.work")
		contents := "go 1.25.0\n\nuse (\n\t./go\n\t./go/examples/social\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => ./go\n"
		if err := os.WriteFile(workspace, []byte(contents), 0o600); err != nil {
			return gateOutcome{}, err
		}
		canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return gateOutcome{}, err
		}
		command.Env = replaceEnvironmentValue(command.Env, "GOWORK", canonicalWorkspace)
	}
	output, commandErr := command.CombinedOutput()
	outcome := gateOutcome{digest: outputDigest(output)}
	if testContext.Err() != nil {
		return outcome, fmt.Errorf("named mutation gate timed out")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for decoder.More() {
		var event goTestEvent
		if err := decoder.Decode(&event); err != nil {
			return outcome, fmt.Errorf("mutation gate produced invalid structured test events")
		}
		if event.Test == mutation.Gate.Test {
			switch event.Action {
			case "pass":
				outcome.exactPass = true
			case "fail":
				outcome.exactFail = true
			case "skip":
				outcome.exactSkip = true
			}
		}
		if event.Package != "" && event.Test == "" {
			switch event.Action {
			case "pass":
				outcome.packagePass = true
			case "fail":
				outcome.packageFail = true
			}
		}
	}
	if commandErr != nil && !outcome.packageFail {
		return outcome, fmt.Errorf("mutation gate command failed without a structured package failure")
	}
	return outcome, nil
}

func ValidateCatalog(values []Mutation) error {
	seen := map[string]bool{}
	for _, mutation := range values {
		if seen[mutation.Label] {
			return fmt.Errorf("duplicate mutation %s", mutation.Label)
		}
		seen[mutation.Label] = true
		if err := validateMutation(mutation); err != nil {
			return fmt.Errorf("%s: %w", mutation.Label, err)
		}
	}
	return nil
}

func ValidatePatchSites(repository string, values []Mutation) error {
	for _, mutation := range values {
		for _, patch := range mutation.Patches {
			contents, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(patch.Path)))
			if err != nil {
				return fmt.Errorf("%s: P8_MUTATION_PATCH_READ", mutation.Label)
			}
			if count := bytes.Count(contents, []byte(patch.Before)); count != 1 {
				return fmt.Errorf("%s: P8_MUTATION_PATCH_MATCH count=%d", mutation.Label, count)
			}
			if bytes.Equal(contents, bytes.Replace(contents, []byte(patch.Before), []byte(patch.After), 1)) {
				return fmt.Errorf("%s: P8_MUTATION_PATCH_NOOP", mutation.Label)
			}
		}
	}
	return nil
}

func validateMutation(mutation Mutation) error {
	if !regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`).MatchString(mutation.Label) || mutation.Summary == "" || len(mutation.Patches) == 0 || !validGateWorkspace(mutation.Gate) || mutation.Gate.Package == "" || mutation.Gate.Test == "" || mutation.Timeout <= 0 {
		return fmt.Errorf("P8_MUTATION_MANIFEST_INVALID")
	}
	for _, patch := range mutation.Patches {
		path := filepath.ToSlash(patch.Path)
		if path == "" || filepath.IsAbs(path) || strings.Contains(path, "../") || patch.Before == "" || patch.Before == patch.After {
			return fmt.Errorf("P8_MUTATION_PATCH_INVALID")
		}
	}
	return nil
}

func validGateWorkspace(gate Gate) bool {
	if gate.Directory == "go" {
		return len(gate.WorkspaceModules) == 0
	}
	return gate.Directory == "go/examples/social" &&
		len(gate.WorkspaceModules) == 2 &&
		gate.WorkspaceModules[0] == "go" &&
		gate.WorkspaceModules[1] == "go/examples/social"
}

func replaceEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	result := append([]string(nil), environment...)
	for index := range result {
		if strings.HasPrefix(result[index], prefix) {
			result[index] = prefix + value
			return result
		}
	}
	return append(result, prefix+value)
}

func applyPatch(root string, patch Patch) error {
	path := filepath.Join(root, filepath.FromSlash(patch.Path))
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("P8_MUTATION_PATCH_READ: %s", patch.Path)
	}
	if count := bytes.Count(contents, []byte(patch.Before)); count != 1 {
		return fmt.Errorf("P8_MUTATION_PATCH_MATCH: %s count=%d", patch.Path, count)
	}
	updated := bytes.Replace(contents, []byte(patch.Before), []byte(patch.After), 1)
	if bytes.Equal(contents, updated) {
		return fmt.Errorf("P8_MUTATION_PATCH_NOOP: %s", patch.Path)
	}
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("P8_MUTATION_PATCH_WRITE: %s", patch.Path)
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "golemgqlgentmp") {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("P8_MUTATION_SYMLINK_REJECTED")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	})
}

func isolatedGoEnvironment(base, extra []string) []string {
	overrides := map[string]bool{"GOWORK": true, "GOFLAGS": true}
	for _, value := range extra {
		if key, _, ok := strings.Cut(value, "="); ok {
			overrides[key] = true
		}
	}
	result := make([]string, 0, len(base)+len(extra)+2)
	for _, value := range base {
		key, _, ok := strings.Cut(value, "=")
		if ok && !overrides[key] {
			result = append(result, value)
		}
	}
	result = append(result, "GOWORK=off", "GOFLAGS=")
	return append(result, extra...)
}

func mutationGoEnvironment(base, extra []string, sandbox string) []string {
	overrides := append([]string(nil), extra...)
	overrides = append(overrides, "GOCACHE="+filepath.Join(sandbox, ".gocache"))
	return isolatedGoEnvironment(base, overrides)
}

func copyFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, contents, info.Mode().Perm())
}

func lookupEnv(extra []string, name string) string {
	for index := len(extra) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(extra[index], "=")
		if ok && key == name {
			return value
		}
	}
	return os.Getenv(name)
}

func outputDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
