package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
)

const activeGeneratedFixtureUpdateEnvironment = "GOLEM_UPDATE_ACTIVE_GENERATED_FIXTURES"

var activeGeneratedFixtureNames = []string{"p5extensions", "p5social", "p6metrics"}

var activeGeneratedFixtureFiles = []string{
	"golemgqlgen/zz_golem_graphql_exec.gen.go",
	"golemgqlgen/zz_golem_graphql_models.gen.go",
	"golemgqlgen/zz_golem_graphql_resolvers.gen.go",
	"zz_golem_bindings.gen.go",
	"zz_golem_graphql.gen.go",
	"zz_golem_graphql.schema.graphqls",
	"zz_golem_models.gen.go",
	"zz_golem_registry.gen.go",
}

var activeGeneratedMetadataAllowlist = []string{
	".golem/generated/contract.metadata.json",
	".golem/generated/model.snapshot.json",
	".golem/generated/postgresql.physical.snapshot.json",
	".golem/generated/sqlite.physical.snapshot.json",
}

type activeGeneratedFixtureFile struct {
	path       string
	content    []byte
	original   []byte
	permission os.FileMode
}

type activeGeneratedFixturePlan struct {
	fixture string
	files   []activeGeneratedFixtureFile
}

type activeGeneratedFixtureStagedFile struct {
	target    string
	temporary string
}

func TestUpdateActiveGeneratedFixtures(t *testing.T) {
	if !activeGeneratedFixtureUpdateEnabled(os.Getenv(activeGeneratedFixtureUpdateEnvironment)) {
		return
	}
	moduleRoot := p5ExtensionModuleRoot(t)
	plans := make([]activeGeneratedFixturePlan, 0, len(activeGeneratedFixtureNames))
	for _, fixture := range activeGeneratedFixtureNames {
		request, err := activeGeneratedFixtureGenerationRequest(moduleRoot, fixture)
		if err != nil {
			t.Fatal(err)
		}
		first, err := pipeline.Build(context.Background(), request)
		if err != nil {
			t.Fatalf("build active generated fixture %s first pass: %v", fixture, err)
		}
		second, err := pipeline.Build(context.Background(), request)
		if err != nil {
			t.Fatalf("build active generated fixture %s second pass: %v", fixture, err)
		}
		plan, err := prepareActiveGeneratedFixturePlan(moduleRoot, fixture, first, second)
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	changed, err := writeActiveGeneratedFixturePlans(plans, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, changedPath := range changed {
		t.Logf("updated %s", changedPath)
	}
}

func activeGeneratedFixtureUpdateEnabled(value string) bool { return value == "1" }

func activeGeneratedFixtureAllowlist(fixture string) []string {
	if !activeGeneratedFixtureName(fixture) {
		return nil
	}
	return append([]string(nil), activeGeneratedFixtureFiles...)
}

func activeGeneratedFixtureName(fixture string) bool {
	for _, candidate := range activeGeneratedFixtureNames {
		if fixture == candidate {
			return true
		}
	}
	return false
}

func activeGeneratedFixtureGenerationRequest(moduleRoot, fixture string) (pipeline.Request, error) {
	if !activeGeneratedFixtureName(fixture) {
		return pipeline.Request{}, fmt.Errorf("active generated fixture %q is not updateable", fixture)
	}
	directory := filepath.Join(moduleRoot, "runtime", "testdata", fixture)
	switch fixture {
	case "p5extensions":
		return p5ExtensionGenerationRequest(directory, "github.com/eleven-am/golem/go/runtime/testdata/p5extensions"), nil
	case "p5social":
		return p5SocialGenerationRequest(directory), nil
	case "p6metrics":
		return p6MetricsGenerationRequest(directory), nil
	default:
		panic("active generated fixture name validation and request ownership diverged")
	}
}

func prepareActiveGeneratedFixturePlan(moduleRoot, fixture string, first, second pipeline.Result) (activeGeneratedFixturePlan, error) {
	if !activeGeneratedFixtureName(fixture) {
		return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %q is not updateable", fixture)
	}
	if err := requireActiveGeneratedFixtureDeterminism(first, second); err != nil {
		return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %s is nondeterministic: %w", fixture, err)
	}
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		return activeGeneratedFixturePlan{}, err
	}
	fixtureRoot := filepath.Join(root, "runtime", "testdata", fixture)
	if err := requireActiveGeneratedFixtureTree(root, fixtureRoot, fixture); err != nil {
		return activeGeneratedFixturePlan{}, err
	}

	prefix := "runtime/testdata/" + fixture + "/"
	allowed := make(map[string]string, len(activeGeneratedFixtureFiles))
	for _, relative := range activeGeneratedFixtureFiles {
		allowed[prefix+relative] = relative
	}
	metadata := make(map[string]bool, len(activeGeneratedMetadataAllowlist))
	for _, candidate := range activeGeneratedMetadataAllowlist {
		metadata[candidate] = true
	}
	selected := make(map[string]codegenmanifest.Artifact, len(activeGeneratedFixtureFiles))
	for _, artifact := range first.Prospective.Artifacts {
		if artifact.Path == "" || path.IsAbs(artifact.Path) || path.Clean(artifact.Path) != artifact.Path || strings.HasPrefix(artifact.Path, "../") {
			return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %s produced unsafe path %q", fixture, artifact.Path)
		}
		if relative, ok := allowed[artifact.Path]; ok {
			if _, duplicate := selected[relative]; duplicate {
				return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %s duplicated %q", fixture, artifact.Path)
			}
			selected[relative] = artifact
			continue
		}
		if metadata[artifact.Path] {
			continue
		}
		return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %s produced unlisted path %q", fixture, artifact.Path)
	}
	if len(selected) != len(activeGeneratedFixtureFiles) {
		return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture %s produced %d of %d updateable artifacts", fixture, len(selected), len(activeGeneratedFixtureFiles))
	}

	plan := activeGeneratedFixturePlan{fixture: fixture, files: make([]activeGeneratedFixtureFile, 0, len(activeGeneratedFixtureFiles))}
	for _, relative := range activeGeneratedFixtureFiles {
		absolute, err := activeGeneratedFixtureTarget(root, fixtureRoot, relative)
		if err != nil {
			return activeGeneratedFixturePlan{}, err
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return activeGeneratedFixturePlan{}, fmt.Errorf("inspect active generated fixture target %s: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return activeGeneratedFixturePlan{}, fmt.Errorf("active generated fixture target %s is not a regular file", relative)
		}
		current, err := os.ReadFile(absolute)
		if err != nil {
			return activeGeneratedFixturePlan{}, err
		}
		plan.files = append(plan.files, activeGeneratedFixtureFile{
			path: absolute, content: append([]byte(nil), selected[relative].Content...),
			original: append([]byte(nil), current...), permission: info.Mode().Perm(),
		})
	}
	return plan, nil
}

func requireActiveGeneratedFixtureDeterminism(first, second pipeline.Result) error {
	if first.Prospective.GenerationDigest == "" || first.Prospective.GenerationDigest != second.Prospective.GenerationDigest {
		return fmt.Errorf("generation digest differs: %q != %q", first.Prospective.GenerationDigest, second.Prospective.GenerationDigest)
	}
	if !bytes.Equal(first.Prospective.Bytes, second.Prospective.Bytes) || !reflect.DeepEqual(first.Prospective.Manifest, second.Prospective.Manifest) {
		return fmt.Errorf("prospective manifest inventory differs")
	}
	if len(first.Prospective.Artifacts) != len(second.Prospective.Artifacts) {
		return fmt.Errorf("artifact inventory length differs: %d != %d", len(first.Prospective.Artifacts), len(second.Prospective.Artifacts))
	}
	for index := range first.Prospective.Artifacts {
		left, right := first.Prospective.Artifacts[index], second.Prospective.Artifacts[index]
		if left.Path != right.Path || left.Kind != right.Kind || left.GeneratedHeader != right.GeneratedHeader || left.Immutable != right.Immutable {
			return fmt.Errorf("artifact inventory differs at index %d", index)
		}
		if !bytes.Equal(left.Content, right.Content) {
			return fmt.Errorf("artifact content differs at %q", left.Path)
		}
	}
	return nil
}

func requireActiveGeneratedFixtureTree(moduleRoot, fixtureRoot, fixture string) error {
	moduleInfo, err := os.Lstat(moduleRoot)
	if err != nil {
		return err
	}
	if !moduleInfo.IsDir() || moduleInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("active generated fixture module root is not a real directory")
	}
	rootRelative, err := filepath.Rel(moduleRoot, fixtureRoot)
	if err != nil || rootRelative == ".." || strings.HasPrefix(rootRelative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("active generated fixture %s is outside the module root", fixture)
	}
	for current := filepath.Join(moduleRoot, "runtime"); ; current = filepath.Join(current, nextActiveFixtureDirectory(current, fixtureRoot)) {
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("active generated fixture directory %s is not a real directory", current)
		}
		if current == fixtureRoot {
			break
		}
	}
	allowed := make(map[string]bool, len(activeGeneratedFixtureFiles))
	for _, relative := range activeGeneratedFixtureFiles {
		allowed[relative] = true
	}
	found := make(map[string]bool, len(activeGeneratedFixtureFiles))
	if err := filepath.WalkDir(fixtureRoot, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if entry.IsDir() {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("active generated fixture contains symlink directory %s", candidate)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("active generated fixture contains nonregular path %s", candidate)
		}
		relative, relativeErr := filepath.Rel(fixtureRoot, candidate)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if allowed[relative] {
			found[relative] = true
			return nil
		}
		base := filepath.Base(candidate)
		if strings.HasPrefix(base, "zz_golem_") || strings.HasPrefix(base, ".golem-active-fixture-") {
			return fmt.Errorf("active generated fixture contains stale or unlisted generated path %s", relative)
		}
		return nil
	}); err != nil {
		return err
	}
	if len(found) != len(activeGeneratedFixtureFiles) {
		return fmt.Errorf("active generated fixture %s contains %d of %d allowlisted files", fixture, len(found), len(activeGeneratedFixtureFiles))
	}
	return nil
}

func nextActiveFixtureDirectory(current, target string) string {
	relative, err := filepath.Rel(current, target)
	if err != nil || relative == "." {
		return ""
	}
	return strings.Split(relative, string(filepath.Separator))[0]
}

func activeGeneratedFixtureTarget(moduleRoot, fixtureRoot, relative string) (string, error) {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("active generated fixture target %q is unsafe", relative)
	}
	absolute := filepath.Join(fixtureRoot, filepath.FromSlash(relative))
	withinFixture, err := filepath.Rel(fixtureRoot, absolute)
	if err != nil || withinFixture == ".." || strings.HasPrefix(withinFixture, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("active generated fixture target %q escapes its fixture", relative)
	}
	withinModule, err := filepath.Rel(moduleRoot, absolute)
	if err != nil || withinModule == ".." || strings.HasPrefix(withinModule, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("active generated fixture target %q escapes the module", relative)
	}
	return absolute, nil
}

func writeActiveGeneratedFixturePlans(plans []activeGeneratedFixturePlan, dryRun bool) ([]string, error) {
	var files []activeGeneratedFixtureFile
	seen := map[string]bool{}
	for _, plan := range plans {
		if !activeGeneratedFixtureName(plan.fixture) {
			return nil, fmt.Errorf("active generated fixture plan names unsupported fixture %q", plan.fixture)
		}
		for _, file := range plan.files {
			if seen[file.path] {
				return nil, fmt.Errorf("active generated fixture update duplicates target %s", file.path)
			}
			seen[file.path] = true
			current, err := os.ReadFile(file.path)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(current, file.original) {
				return nil, fmt.Errorf("active generated fixture target changed after validation: %s", file.path)
			}
			if bytes.Equal(current, file.content) {
				continue
			}
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	changed := make([]string, len(files))
	for index, file := range files {
		changed[index] = file.path
	}
	if dryRun || len(files) == 0 {
		return changed, nil
	}

	staged := make([]activeGeneratedFixtureStagedFile, 0, len(files))
	defer func() {
		for _, file := range staged {
			_ = os.Remove(file.temporary)
		}
	}()
	for _, file := range files {
		info, err := os.Lstat(file.path)
		if err != nil || !info.Mode().IsRegular() {
			return changed, fmt.Errorf("active generated fixture target is no longer regular: %s", file.path)
		}
		temporary, err := os.CreateTemp(filepath.Dir(file.path), ".golem-active-fixture-*")
		if err != nil {
			return changed, err
		}
		temporaryPath := temporary.Name()
		staged = append(staged, activeGeneratedFixtureStagedFile{target: file.path, temporary: temporaryPath})
		if err := temporary.Chmod(file.permission); err != nil {
			_ = temporary.Close()
			return changed, err
		}
		if _, err := temporary.Write(file.content); err != nil {
			_ = temporary.Close()
			return changed, err
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			return changed, err
		}
		if err := temporary.Close(); err != nil {
			return changed, err
		}
	}
	for _, file := range staged {
		if err := os.Rename(file.temporary, file.target); err != nil {
			return changed, err
		}
		file.temporary = ""
	}
	for _, directory := range activeGeneratedFixtureChangedDirectories(files) {
		handle, err := os.Open(directory)
		if err != nil {
			return changed, err
		}
		syncErr := handle.Sync()
		closeErr := handle.Close()
		if syncErr != nil {
			return changed, syncErr
		}
		if closeErr != nil {
			return changed, closeErr
		}
	}
	return changed, nil
}

func activeGeneratedFixtureChangedDirectories(files []activeGeneratedFixtureFile) []string {
	seen := map[string]bool{}
	for _, file := range files {
		seen[filepath.Dir(file.path)] = true
	}
	result := make([]string, 0, len(seen))
	for directory := range seen {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}
