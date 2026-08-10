package publication

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
)

const journalPath = ".golem/generation-journal.json"
const lockPath = ".golem/generation.lock"

type journal struct {
	FormatVersion   uint16        `json:"formatVersion"`
	ManifestPath    string        `json:"manifestPath"`
	NewManifestHash string        `json:"newManifestHash"`
	Items           []journalItem `json:"items"`
}

type journalItem struct {
	Path        string `json:"path"`
	Temporary   string `json:"temporary,omitempty"`
	Backup      string `json:"backup"`
	NewHash     string `json:"newHash,omitempty"`
	HadOriginal bool   `json:"hadOriginal"`
	Manifest    bool   `json:"manifest,omitempty"`
}

func apply(ctx context.Context, request Request) (Result, error) {
	if request.ModuleDir == "" {
		return Result{}, fmt.Errorf("publication requires a module directory")
	}
	moduleDir, err := filepath.Abs(request.ModuleDir)
	if err != nil {
		return Result{}, err
	}
	manifestPath := request.ManifestPath
	if manifestPath == "" {
		manifestPath = manifest.DefaultPath
	}
	manifestPath, err = safeRelative(manifestPath)
	if err != nil {
		return Result{}, err
	}
	if request.Mode == "" {
		request.Mode = ModePublish
	}
	if request.Mode != ModePublish && request.Mode != ModeCheck {
		return Result{}, fmt.Errorf("unsupported publication mode %q", request.Mode)
	}
	if request.FileMode == 0 {
		request.FileMode = 0o644
	}
	lock, err := acquireLock(moduleDir)
	if err != nil {
		return Result{}, err
	}
	defer lock.close()
	if err := recoverJournal(moduleDir); err != nil {
		return Result{}, err
	}
	previous, previousBytes, err := readManifest(moduleDir, manifestPath)
	if err != nil {
		return Result{}, err
	}
	plan, result, err := makePlan(moduleDir, manifestPath, previous, previousBytes, request)
	if err != nil {
		return Result{}, err
	}
	if request.Mode == ModeCheck {
		result.Checked = true
		if len(result.Changed) != 0 || len(result.Stale) != 0 {
			return result, fmt.Errorf("generated artifacts are stale: changed=%v stale=%v", result.Changed, result.Stale)
		}
		return result, nil
	}
	if len(result.Changed) == 0 && len(result.Stale) == 0 {
		return result, nil
	}
	if err := publishPlan(ctx, moduleDir, plan, request); err != nil {
		return result, err
	}
	return result, nil
}

type publicationPlan struct {
	journal journal
	content map[string][]byte
	modes   map[string]os.FileMode
}

func makePlan(moduleDir, manifestPath string, previous *manifest.Manifest, previousBytes []byte, request Request) (publicationPlan, Result, error) {
	prospective := request.Prospective
	if prospective.Manifest.FormatVersion != manifest.FormatVersion || len(prospective.Bytes) == 0 {
		return publicationPlan{}, Result{}, fmt.Errorf("publication requires a built prospective manifest")
	}
	if parsed, err := manifest.Parse(prospective.Bytes); err != nil || !reflect.DeepEqual(parsed, prospective.Manifest) {
		return publicationPlan{}, Result{}, fmt.Errorf("prospective manifest bytes do not match the supplied manifest")
	}
	artifactByPath := map[string]manifest.Artifact{}
	for _, artifact := range prospective.Artifacts {
		artifactByPath[artifact.Path] = artifact
	}
	for _, entry := range prospective.Manifest.Artifacts {
		artifact, ok := artifactByPath[entry.Path]
		if !ok || manifest.ContentHash(artifact.Content) != entry.ContentSHA256 {
			return publicationPlan{}, Result{}, fmt.Errorf("prospective artifact %q does not match its manifest entry", entry.Path)
		}
	}
	oldEntries := map[string]manifest.Entry{}
	if previous != nil {
		for _, entry := range previous.Artifacts {
			oldEntries[entry.Path] = entry
		}
	}
	newEntries := map[string]manifest.Entry{}
	for _, entry := range prospective.Manifest.Artifacts {
		newEntries[entry.Path] = entry
	}
	result := Result{}
	plan := publicationPlan{content: map[string][]byte{}, modes: map[string]os.FileMode{}}
	for _, entry := range prospective.Manifest.Artifacts {
		artifact := artifactByPath[entry.Path]
		absolute := filepath.Join(moduleDir, filepath.FromSlash(entry.Path))
		current, readErr := os.ReadFile(absolute)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return publicationPlan{}, Result{}, readErr
		}
		old, managed := oldEntries[entry.Path]
		if readErr == nil {
			if !managed {
				return publicationPlan{}, Result{}, fmt.Errorf("refusing to overwrite unmanaged path %q", entry.Path)
			}
			if old.Immutable && (manifest.ContentHash(current) != old.ContentSHA256 || entry.ContentSHA256 != old.ContentSHA256) {
				return publicationPlan{}, Result{}, fmt.Errorf("immutable generated artifact %q differs; use the migration-history workflow", entry.Path)
			}
			if old.GeneratedHeader != "" && !fileHasHeader(current, old.GeneratedHeader) {
				return publicationPlan{}, Result{}, fmt.Errorf("manifest-owned path %q lost its generated ownership header", entry.Path)
			}
			if manifest.ContentHash(current) == entry.ContentSHA256 {
				continue
			}
		} else if managed && old.Immutable {
			return publicationPlan{}, Result{}, fmt.Errorf("immutable generated artifact %q is missing; use the migration-history workflow", entry.Path)
		}
		result.Changed = append(result.Changed, entry.Path)
		plan.content[entry.Path], plan.modes[entry.Path] = artifact.Content, os.FileMode(request.FileMode)
	}
	for path, old := range oldEntries {
		if _, retained := newEntries[path]; retained {
			continue
		}
		if old.Immutable {
			return publicationPlan{}, Result{}, fmt.Errorf("immutable generated artifact %q cannot be removed by ordinary generation", path)
		}
		absolute := filepath.Join(moduleDir, filepath.FromSlash(path))
		content, readErr := os.ReadFile(absolute)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return publicationPlan{}, Result{}, readErr
		}
		if old.GeneratedHeader == "" || !fileHasHeader(content, old.GeneratedHeader) {
			return publicationPlan{}, Result{}, fmt.Errorf("refusing to remove stale path %q without its exact generated ownership header", path)
		}
		result.Stale = append(result.Stale, path)
	}
	manifestAbsolute := filepath.Join(moduleDir, filepath.FromSlash(manifestPath))
	if !equalBytes(previousBytes, prospective.Bytes) {
		result.Changed = append(result.Changed, manifestPath)
		plan.content[manifestPath], plan.modes[manifestPath] = prospective.Bytes, os.FileMode(request.FileMode)
	}
	sort.Strings(result.Changed)
	sort.Strings(result.Stale)
	paths := append([]string(nil), result.Changed...)
	paths = removeString(paths, manifestPath)
	paths = append(paths, result.Stale...)
	sort.Strings(paths)
	items := make([]journalItem, 0, len(paths)+1)
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		_, statErr := os.Stat(filepath.Join(moduleDir, filepath.FromSlash(path)))
		item := journalItem{Path: path, Backup: sibling(path, ".golem-old"), HadOriginal: statErr == nil}
		if content, installs := plan.content[path]; installs {
			item.Temporary, item.NewHash = sibling(path, ".golem-new"), manifest.ContentHash(content)
		}
		items = append(items, item)
	}
	manifestItem := journalItem{Path: manifestPath, Temporary: sibling(manifestPath, ".golem-new"), Backup: sibling(manifestPath, ".golem-old"), NewHash: manifest.ContentHash(prospective.Bytes), HadOriginal: previous != nil, Manifest: true}
	items = append(items, manifestItem)
	plan.content[manifestPath], plan.modes[manifestPath] = prospective.Bytes, os.FileMode(request.FileMode)
	plan.journal = journal{FormatVersion: 1, ManifestPath: manifestPath, NewManifestHash: manifestItem.NewHash, Items: items}
	_ = manifestAbsolute
	return plan, result, nil
}

func publishPlan(ctx context.Context, moduleDir string, plan publicationPlan, request Request) error {
	for _, item := range plan.journal.Items {
		if item.Temporary == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeSibling(moduleDir, item.Temporary, plan.content[item.Path], plan.modes[item.Path]); err != nil {
			return err
		}
		if err := inject(request.Inject, StepStaged, item.Path); err != nil {
			return err
		}
	}
	if err := writeJournal(moduleDir, plan.journal); err != nil {
		return err
	}
	if err := inject(request.Inject, StepJournaled, ""); err != nil {
		return err
	}
	for _, item := range plan.journal.Items {
		if item.Manifest {
			continue
		}
		if item.HadOriginal {
			if err := renameRelative(moduleDir, item.Path, item.Backup); err != nil {
				return err
			}
		}
		if err := inject(request.Inject, StepBackedUp, item.Path); err != nil {
			return err
		}
		if item.Temporary != "" {
			if err := renameRelative(moduleDir, item.Temporary, item.Path); err != nil {
				return err
			}
		}
		if err := inject(request.Inject, StepInstalled, item.Path); err != nil {
			return err
		}
	}
	for _, item := range plan.journal.Items {
		if item.Manifest || item.NewHash == "" {
			continue
		}
		if err := verifyRelative(moduleDir, item.Path, item.NewHash); err != nil {
			return err
		}
		if err := inject(request.Inject, StepVerified, item.Path); err != nil {
			return err
		}
	}
	manifestItem := plan.journal.Items[len(plan.journal.Items)-1]
	if manifestItem.HadOriginal {
		if err := renameRelative(moduleDir, manifestItem.Path, manifestItem.Backup); err != nil {
			return err
		}
	}
	if err := renameRelative(moduleDir, manifestItem.Temporary, manifestItem.Path); err != nil {
		return err
	}
	if err := verifyRelative(moduleDir, manifestItem.Path, manifestItem.NewHash); err != nil {
		return err
	}
	if err := inject(request.Inject, StepManifestInstalled, manifestItem.Path); err != nil {
		return err
	}
	return finishCommitted(moduleDir, plan.journal)
}

func recoverOnly(ctx context.Context, moduleDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	absolute, err := filepath.Abs(moduleDir)
	if err != nil {
		return err
	}
	// A journal-free preflight is genuinely read-only. The lock is only needed
	// when recovery work exists; this preserves diagnostics-with-no-writes while
	// retaining the same locked recovery semantics for interrupted publication.
	if _, statErr := os.Stat(filepath.Join(absolute, filepath.FromSlash(journalPath))); errors.Is(statErr, os.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	lock, err := acquireLock(absolute)
	if err != nil {
		return err
	}
	defer lock.close()
	return recoverJournal(absolute)
}

func recoverJournal(moduleDir string) error {
	encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(journalPath)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var value journal
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("cannot recover invalid generation journal")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("cannot recover invalid generation journal")
	}
	manifestPath, pathErr := safeRelative(value.ManifestPath)
	if value.FormatVersion != 1 || pathErr != nil || manifestPath != value.ManifestPath || !validContentHash(value.NewManifestHash) {
		return fmt.Errorf("cannot recover invalid generation journal")
	}
	manifestCount := 0
	seenPaths := make(map[string]bool, len(value.Items))
	for index, item := range value.Items {
		if seenPaths[item.Path] {
			return fmt.Errorf("cannot recover journal with duplicate path %q", item.Path)
		}
		seenPaths[item.Path] = true
		for _, candidate := range []string{item.Path, item.Backup} {
			if normalized, err := safeRelative(candidate); err != nil || normalized != candidate {
				return fmt.Errorf("cannot recover journal with unsafe path %q", candidate)
			}
		}
		if item.Temporary != "" {
			if normalized, err := safeRelative(item.Temporary); err != nil || normalized != item.Temporary {
				return fmt.Errorf("cannot recover journal with unsafe path %q", item.Temporary)
			}
			if item.Temporary != sibling(item.Path, ".golem-new") {
				return fmt.Errorf("cannot recover journal with invalid temporary path")
			}
		}
		if (item.NewHash == "") != (item.Temporary == "") || item.NewHash != "" && !validContentHash(item.NewHash) {
			return fmt.Errorf("cannot recover journal with invalid install metadata for %q", item.Path)
		}
		if item.Backup != sibling(item.Path, ".golem-old") {
			return fmt.Errorf("cannot recover journal with invalid backup path")
		}
		if item.Manifest {
			manifestCount++
			if item.Path != value.ManifestPath || index != len(value.Items)-1 || item.NewHash != value.NewManifestHash {
				return fmt.Errorf("cannot recover journal with invalid manifest item")
			}
		}
	}
	if manifestCount != 1 {
		return fmt.Errorf("cannot recover journal without one manifest item")
	}
	manifestMatches := verifyRelative(moduleDir, value.ManifestPath, value.NewManifestHash) == nil
	if manifestMatches {
		for _, item := range value.Items {
			if item.NewHash != "" && verifyRelative(moduleDir, item.Path, item.NewHash) != nil {
				manifestMatches = false
				break
			}
			if item.NewHash == "" {
				if _, err := os.Stat(filepath.Join(moduleDir, filepath.FromSlash(item.Path))); !errors.Is(err, os.ErrNotExist) {
					manifestMatches = false
					break
				}
			}
		}
	}
	if manifestMatches {
		return finishCommitted(moduleDir, value)
	}
	return rollback(moduleDir, value)
}

func validContentHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func rollback(moduleDir string, value journal) error {
	for index := len(value.Items) - 1; index >= 0; index-- {
		item := value.Items[index]
		target := filepath.Join(moduleDir, filepath.FromSlash(item.Path))
		backup := filepath.Join(moduleDir, filepath.FromSlash(item.Backup))
		if _, err := os.Stat(backup); err == nil {
			if content, readErr := os.ReadFile(target); readErr == nil && item.NewHash != "" && manifest.ContentHash(content) != item.NewHash {
				return fmt.Errorf("refusing recovery because installed path %q changed after interruption", item.Path)
			}
			_ = os.Remove(target)
			if err := os.Rename(backup, target); err != nil {
				return err
			}
		} else if !item.HadOriginal && item.NewHash != "" {
			if content, readErr := os.ReadFile(target); readErr == nil {
				if manifest.ContentHash(content) != item.NewHash {
					return fmt.Errorf("refusing recovery because installed path %q changed after interruption", item.Path)
				}
				if err := os.Remove(target); err != nil {
					return err
				}
			}
		}
		if item.Temporary != "" {
			_ = os.Remove(filepath.Join(moduleDir, filepath.FromSlash(item.Temporary)))
		}
	}
	return removeJournal(moduleDir)
}

func finishCommitted(moduleDir string, value journal) error {
	for _, item := range value.Items {
		_ = os.Remove(filepath.Join(moduleDir, filepath.FromSlash(item.Backup)))
		if item.Temporary != "" {
			_ = os.Remove(filepath.Join(moduleDir, filepath.FromSlash(item.Temporary)))
		}
	}
	return removeJournal(moduleDir)
}

type generationLock struct {
	file   *os.File
	unlock func() error
}

func acquireLock(moduleDir string) (*generationLock, error) {
	path := filepath.Join(moduleDir, filepath.FromSlash(lockPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	unlock, err := lockGenerationFile(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("another generation process holds %s", lockPath)
	}
	return &generationLock{file: file, unlock: unlock}, nil
}
func (lock *generationLock) close() {
	_ = lock.unlock()
	_ = lock.file.Close()
}

func writeSibling(moduleDir, relative string, content []byte, mode os.FileMode) error {
	absolute := filepath.Join(moduleDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return err
	}
	_ = os.Remove(absolute)
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func writeJournal(moduleDir string, value journal) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := journalPath + ".new"
	if err := writeSibling(moduleDir, temporary, encoded, 0o600); err != nil {
		return err
	}
	return renameRelative(moduleDir, temporary, journalPath)
}
func renameRelative(moduleDir, from, to string) error {
	target := filepath.Join(moduleDir, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(moduleDir, filepath.FromSlash(from)), target)
}
func verifyRelative(moduleDir, relative, expected string) error {
	content, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(relative)))
	if err != nil {
		return err
	}
	if manifest.ContentHash(content) != expected {
		return fmt.Errorf("installed artifact %q has an unexpected content hash", relative)
	}
	return nil
}
func readManifest(moduleDir, relative string) (*manifest.Manifest, []byte, error) {
	encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(relative)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	value, err := manifest.ParseHistorical(encoded)
	if err != nil {
		return nil, nil, err
	}
	return &value, encoded, nil
}
func removeJournal(moduleDir string) error {
	err := os.Remove(filepath.Join(moduleDir, filepath.FromSlash(journalPath)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func inject(injector FailureInjector, step Step, path string) error {
	if injector == nil {
		return nil
	}
	return injector(step, path)
}
func sibling(path, suffix string) string {
	directory, base := filepath.ToSlash(filepath.Dir(path)), filepath.Base(path)
	value := "." + base + suffix
	if directory == "." {
		return value
	}
	return directory + "/" + value
}
func safeRelative(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, `\`) {
		return "", fmt.Errorf("publication path %q must be module-relative slash form", value)
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("publication path %q escapes the module", value)
	}
	return value, nil
}
func fileHasHeader(content []byte, header string) bool {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	return scanner.Scan() && strings.TrimSuffix(scanner.Text(), "\r") == header
}
func equalBytes(left, right []byte) bool { return string(left) == string(right) }
func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
