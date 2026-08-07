package mutationverify

import (
	"bytes"
	"context"
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
	ModuleDir string
	Keep      bool
	Timeout   time.Duration
	Env       []string
}

func (runner Runner) Run(ctx context.Context, mutation Mutation) (Result, error) {
	result := Result{Label: mutation.Label}
	if !mutation.Covered() {
		result.Status, result.Detail = StatusSkipped, mutation.Remaining
		return result, nil
	}
	for _, test := range mutation.Tests {
		for _, name := range test.Env {
			if lookupEnv(runner.Env, name) == "" {
				result.Status = StatusSkipped
				result.Detail = "required environment variable is absent: " + name
				return result, nil
			}
		}
	}
	module, err := filepath.Abs(runner.ModuleDir)
	if err != nil {
		return result, err
	}
	sandbox, err := os.MkdirTemp("", "golem-p6-mutant-"+strings.ToLower(mutation.Label)+"-")
	if err != nil {
		return result, err
	}
	if runner.Keep {
		result.SandboxDir = sandbox
	} else {
		defer os.RemoveAll(sandbox)
	}
	if err := copyModule(module, sandbox); err != nil {
		return result, err
	}
	for _, patch := range mutation.Patches {
		if err := applyPatch(sandbox, patch); err != nil {
			result.Status, result.Detail = StatusInvalid, err.Error()
			return result, nil
		}
	}
	started := time.Now()
	var outputs []string
	for _, test := range mutation.Tests {
		timeout := runner.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		testCtx, cancel := context.WithTimeout(ctx, timeout)
		command := exec.CommandContext(testCtx, "go", "test", "-vet=off", "-count=1", test.Package, "-run", "^"+regexp.QuoteMeta(test.Name)+"$", "-v")
		command.Dir = sandbox
		command.Env = append(os.Environ(), runner.Env...)
		output, commandErr := command.CombinedOutput()
		contextErr := testCtx.Err()
		cancel()
		text := string(output)
		outputs = append(outputs, "$ go test -vet=off -count=1 "+test.Package+" -run ^"+test.Name+"$ -v\n"+text)
		if commandErr == nil {
			continue
		}
		if contextErr != nil {
			result.Status, result.Detail = StatusInvalid, "named test timed out: "+test.Name
			result.Test, result.Output, result.Duration = test.Name, strings.Join(outputs, "\n"), time.Since(started)
			return result, nil
		}
		marker := "--- FAIL: " + test.Name
		if strings.Contains(text, marker) {
			result.Status, result.Test, result.Output, result.Duration = StatusKilled, test.Name, strings.Join(outputs, "\n"), time.Since(started)
			return result, nil
		}
		result.Status, result.Test, result.Output, result.Duration = StatusInvalid, test.Name, strings.Join(outputs, "\n"), time.Since(started)
		result.Detail = "go test failed without the required named-test failure marker"
		return result, nil
	}
	result.Status, result.Output, result.Duration = StatusSurvived, strings.Join(outputs, "\n"), time.Since(started)
	return result, nil
}

func applyPatch(root string, patch Patch) error {
	if patch.Path == "" || filepath.IsAbs(patch.Path) || strings.Contains(filepath.ToSlash(patch.Path), "../") {
		return fmt.Errorf("unsafe mutation patch path %q", patch.Path)
	}
	path := filepath.Join(root, filepath.FromSlash(patch.Path))
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", patch.Path, err)
	}
	count := bytes.Count(content, []byte(patch.Before))
	if count != 1 {
		return fmt.Errorf("patch %s expected one source match, found %d", patch.Path, count)
	}
	updated := bytes.Replace(content, []byte(patch.Before), []byte(patch.After), 1)
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", patch.Path, err)
	}
	return nil
}

func copyModule(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".golem" || strings.HasPrefix(entry.Name(), "golemgqlgentmp")) {
			return filepath.SkipDir
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, info.Mode().Perm())
	})
}

func lookupEnv(extra []string, name string) string {
	for index := len(extra) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(extra[index], "=")
		if found && key == name {
			return value
		}
	}
	return os.Getenv(name)
}
