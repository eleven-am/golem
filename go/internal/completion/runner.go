// Package completion executes the closed, read-only P8 completion command
// corpora. It records only bounded identities and digests; subprocess output,
// environment values, paths, and provider diagnostics never enter evidence.
package completion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const FormatVersion uint16 = 1

type Code string

const (
	CodeInvalidConfig Code = "P8_COMPLETION_CONFIG"
	CodeSnapshot      Code = "P8_COMPLETION_SNAPSHOT"
	CodeStart         Code = "P8_COMPLETION_START"
	CodeTimeout       Code = "P8_COMPLETION_TIMEOUT"
	CodeEvents        Code = "P8_COMPLETION_EVENTS"
	CodeTestFailure   Code = "P8_COMPLETION_TEST_FAILURE"
	CodeSkip          Code = "P8_COMPLETION_SKIP"
	CodeMissing       Code = "P8_COMPLETION_MISSING"
	CodeTreeChanged   Code = "P8_COMPLETION_TREE_CHANGED"
)

type Error struct{ Code Code }

func (failure *Error) Error() string {
	if failure == nil {
		return string(CodeInvalidConfig)
	}
	return string(failure.Code)
}

type Package struct {
	Path       string
	ImportPath string
	Tests      []string
}

type Spec struct {
	Command    string
	ModuleDir  string
	Packages   []Package
	Profiles   []string
	Timeout    time.Duration
	WatchPaths []string
	Env        []string
}

type Evidence struct {
	FormatVersion   uint16   `json:"formatVersion"`
	Command         string   `json:"command"`
	Status          string   `json:"status"`
	Profiles        []string `json:"profiles"`
	RequiredTests   int      `json:"requiredTests"`
	PassedTests     int      `json:"passedTests"`
	PassedPackages  int      `json:"passedPackages"`
	TestEventSHA256 string   `json:"testEventSHA256"`
	TreeSHA256      string   `json:"treeSHA256"`
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

var (
	closedCommand = regexp.MustCompile(`^p8(?:docs|compat|failure)$`)
	closedProfile = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	closedPackage = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?:/[A-Za-z0-9_.-]+)+$`)
	closedTest    = regexp.MustCompile(`^TestP8[A-Za-z0-9]+(?:/[a-z0-9-]+)*$`)
)

func Run(ctx context.Context, spec Spec) (Evidence, error) {
	evidence := Evidence{
		FormatVersion: FormatVersion,
		Command:       spec.Command,
		Status:        "FAIL",
		Profiles:      append([]string(nil), spec.Profiles...),
	}
	if !validSpec(spec) {
		return evidence, &Error{Code: CodeInvalidConfig}
	}
	before, err := snapshot(spec.WatchPaths)
	if err != nil {
		return evidence, &Error{Code: CodeSnapshot}
	}

	required := map[string]struct{}{}
	packagePaths := make([]string, 0, len(spec.Packages))
	testNames := make([]string, 0, 32)
	for _, pkg := range spec.Packages {
		packagePaths = append(packagePaths, pkg.Path)
		for _, name := range pkg.Tests {
			required[pkg.ImportPath+":"+name] = struct{}{}
			testNames = append(testNames, strings.Split(name, "/")[0])
		}
	}
	testNames = uniqueSorted(testNames)
	evidence.RequiredTests = len(required)
	parts := make([]string, len(testNames))
	for index, name := range testNames {
		parts[index] = regexp.QuoteMeta(name)
	}
	arguments := []string{"test", "-json", "-count=1", "-mod=readonly", "-timeout", spec.Timeout.String(), "-run", "^(?:" + strings.Join(parts, "|") + ")(?:/.*)?$"}
	arguments = append(arguments, packagePaths...)

	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, "go", arguments...)
	command.Dir = spec.ModuleDir
	command.Env = completionEnvironment(spec.Env)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return evidence, &Error{Code: CodeStart}
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return evidence, &Error{Code: CodeStart}
	}

	eventHash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(stdout, eventHash))
	passed := map[string]struct{}{}
	passedPackages := map[string]struct{}{}
	skipped := false
	failed := false
	invalidEvents := false
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			invalidEvents = true
			_, _ = io.Copy(io.Discard, stdout)
			break
		}
		switch event.Action {
		case "pass":
			if event.Test == "" {
				passedPackages[event.Package] = struct{}{}
			} else {
				passed[event.Package+":"+event.Test] = struct{}{}
			}
		case "skip":
			skipped = true
		case "fail":
			failed = true
		}
	}
	waitErr := command.Wait()
	after, snapshotErr := snapshot(spec.WatchPaths)
	evidence.TestEventSHA256 = hex.EncodeToString(eventHash.Sum(nil))
	evidence.TreeSHA256 = before
	evidence.PassedPackages = len(passedPackages)
	for identity := range required {
		if _, ok := passed[identity]; ok {
			evidence.PassedTests++
		}
	}
	if snapshotErr != nil {
		return evidence, &Error{Code: CodeSnapshot}
	}
	if before != after {
		return evidence, &Error{Code: CodeTreeChanged}
	}
	if runCtx.Err() != nil {
		return evidence, &Error{Code: CodeTimeout}
	}
	if invalidEvents {
		return evidence, &Error{Code: CodeEvents}
	}
	if skipped {
		return evidence, &Error{Code: CodeSkip}
	}
	if failed || waitErr != nil {
		return evidence, &Error{Code: CodeTestFailure}
	}
	if evidence.PassedTests != evidence.RequiredTests || evidence.PassedPackages != len(spec.Packages) {
		return evidence, &Error{Code: CodeMissing}
	}
	evidence.Status = "PASS"
	return evidence, nil
}

func validSpec(spec Spec) bool {
	if !closedCommand.MatchString(spec.Command) || spec.Timeout <= 0 || spec.Timeout > 60*time.Minute || len(spec.Packages) == 0 || len(spec.Profiles) == 0 || len(spec.WatchPaths) == 0 {
		return false
	}
	if info, err := os.Stat(filepath.Join(spec.ModuleDir, "go.mod")); err != nil || info.IsDir() {
		return false
	}
	if !sort.StringsAreSorted(spec.Profiles) {
		return false
	}
	for index, profile := range spec.Profiles {
		if !closedProfile.MatchString(profile) || index > 0 && spec.Profiles[index-1] == profile {
			return false
		}
	}
	seenPackages := map[string]bool{}
	seenTests := map[string]bool{}
	for _, pkg := range spec.Packages {
		if !strings.HasPrefix(pkg.Path, "./") || !closedPackage.MatchString(pkg.ImportPath) || len(pkg.Tests) == 0 || seenPackages[pkg.ImportPath] {
			return false
		}
		seenPackages[pkg.ImportPath] = true
		for _, name := range pkg.Tests {
			identity := pkg.ImportPath + ":" + name
			if !closedTest.MatchString(name) || seenTests[identity] {
				return false
			}
			seenTests[identity] = true
		}
	}
	for _, path := range spec.WatchPaths {
		if !filepath.IsAbs(path) {
			return false
		}
	}
	return true
}

func completionEnvironment(extra []string) []string {
	values := append([]string(nil), os.Environ()...)
	values = setEnvironment(values, "GOWORK", "off")
	values = setEnvironment(values, "GOFLAGS", "")
	values = setEnvironment(values, "GOLEM_P8_REQUIRE_POSTGRESQL", "1")
	for _, assignment := range extra {
		name, value, ok := strings.Cut(assignment, "=")
		if ok && name != "" && name != "GOWORK" && name != "GOLEM_P8_REQUIRE_POSTGRESQL" {
			values = setEnvironment(values, name, value)
		}
	}
	return values
}

func setEnvironment(values []string, name, value string) []string {
	prefix := name + "="
	result := values[:0]
	for _, entry := range values {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func snapshot(paths []string) (string, error) {
	hash := sha256.New()
	for rootIndex, root := range paths {
		info, err := os.Lstat(root)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			if err := hashEntry(hash, strconv.Itoa(rootIndex), root, info); err != nil {
				return "", err
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == root {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return hashEntry(hash, strconv.Itoa(rootIndex)+"/"+filepath.ToSlash(relative), path, info)
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashEntry(hash io.Writer, identity, path string, info fs.FileInfo) error {
	_, _ = io.WriteString(hash, identity)
	_, _ = io.WriteString(hash, "\x00"+info.Mode().String()+"\x00")
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, target)
		_, _ = io.WriteString(hash, "\x00")
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsupported watched file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	_, _ = io.WriteString(hash, "\x00")
	return errors.Join(copyErr, closeErr)
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
