package runtime_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
)

func TestActiveGeneratedFixtureUpdaterIsDisabledByDefault(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", " 1", "1 "} {
		if activeGeneratedFixtureUpdateEnabled(value) {
			t.Fatalf("update opt-in accepted %q", value)
		}
	}
	if !activeGeneratedFixtureUpdateEnabled("1") {
		t.Fatal("exact update opt-in was refused")
	}
}

func TestActiveGeneratedFixtureUpdaterRequiresExactDeterminism(t *testing.T) {
	root := t.TempDir()
	prepareActiveGeneratedFixtureTree(t, root, "p5extensions", []byte("old"))
	baseline := syntheticActiveGeneratedFixtureResult("p5extensions", []byte("new"))

	t.Run("generation-digest", func(t *testing.T) {
		changed := baseline
		changed.Prospective.GenerationDigest = "different"
		if _, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", baseline, changed); err == nil {
			t.Fatal("different repeat generation digest was accepted")
		}
	})
	t.Run("inventory-order", func(t *testing.T) {
		changed := cloneActiveGeneratedFixtureResult(baseline)
		changed.Prospective.Artifacts[0], changed.Prospective.Artifacts[1] = changed.Prospective.Artifacts[1], changed.Prospective.Artifacts[0]
		if _, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", baseline, changed); err == nil {
			t.Fatal("different repeat artifact inventory was accepted")
		}
	})
	t.Run("content", func(t *testing.T) {
		changed := cloneActiveGeneratedFixtureResult(baseline)
		changed.Prospective.Artifacts[0].Content = []byte("different")
		if _, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", baseline, changed); err == nil {
			t.Fatal("different repeat artifact content was accepted")
		}
	})
}

func TestActiveGeneratedFixtureUpdaterRejectsUnlistedUnsafeAndOutsideTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, *pipeline.Result)
	}{
		{name: "unlisted-prospective", mutate: func(_ *testing.T, _ string, result *pipeline.Result) {
			result.Prospective.Artifacts = append(result.Prospective.Artifacts, codegenmanifest.Artifact{Path: "runtime/testdata/p5extensions/zz_golem_unlisted.gen.go", Kind: codegenmanifest.ArtifactModelGo, Content: []byte("unlisted")})
		}},
		{name: "outside-active-fixture", mutate: func(_ *testing.T, _ string, result *pipeline.Result) {
			result.Prospective.Artifacts = append(result.Prospective.Artifacts, codegenmanifest.Artifact{Path: "runtime/testdata/p7/zz_golem_models.gen.go", Kind: codegenmanifest.ArtifactModelGo, Content: []byte("outside")})
		}},
		{name: "stale-existing", mutate: func(t *testing.T, root string, _ *pipeline.Result) {
			writeActiveGeneratedFixtureTestFile(t, root, "runtime/testdata/p5extensions/zz_golem_stale.gen.go", []byte(codegenmanifest.GeneratedHeader+"\n"))
		}},
		{name: "symlink", mutate: func(t *testing.T, root string, _ *pipeline.Result) {
			target := filepath.Join(root, "runtime", "testdata", "p5extensions", filepath.FromSlash(activeGeneratedFixtureAllowlist("p5extensions")[0]))
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "outside"), target); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "nonregular", mutate: func(t *testing.T, root string, _ *pipeline.Result) {
			target := filepath.Join(root, "runtime", "testdata", "p5extensions", filepath.FromSlash(activeGeneratedFixtureAllowlist("p5extensions")[0]))
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			prepareActiveGeneratedFixtureTree(t, root, "p5extensions", []byte("old"))
			first := syntheticActiveGeneratedFixtureResult("p5extensions", []byte("new"))
			test.mutate(t, root, &first)
			second := cloneActiveGeneratedFixtureResult(first)
			if _, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", first, second); err == nil {
				t.Fatal("unsafe fixture update was accepted")
			}
		})
	}
	if _, err := prepareActiveGeneratedFixturePlan(t.TempDir(), "p7", pipeline.Result{}, pipeline.Result{}); err == nil {
		t.Fatal("non-active fixture family was accepted")
	}
}

func TestActiveGeneratedFixtureUpdaterDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	prepareActiveGeneratedFixtureTree(t, root, "p5extensions", []byte("old"))
	result := syntheticActiveGeneratedFixtureResult("p5extensions", []byte("new"))
	plan, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", result, cloneActiveGeneratedFixtureResult(result))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := writeActiveGeneratedFixturePlans([]activeGeneratedFixturePlan{plan}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != len(activeGeneratedFixtureAllowlist("p5extensions")) {
		t.Fatalf("dry-run changed paths=%d", len(changed))
	}
	for _, relative := range activeGeneratedFixtureAllowlist("p5extensions") {
		content, err := os.ReadFile(filepath.Join(root, "runtime", "testdata", "p5extensions", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, []byte("old")) {
			t.Fatalf("dry-run wrote %s", relative)
		}
	}
}

func TestActiveGeneratedFixtureUpdaterAtomicallyReplacesOnlyAllowlistedFiles(t *testing.T) {
	root := t.TempDir()
	prepareActiveGeneratedFixtureTree(t, root, "p5extensions", []byte("old"))
	result := syntheticActiveGeneratedFixtureResult("p5extensions", []byte("new"))
	plan, err := prepareActiveGeneratedFixturePlan(root, "p5extensions", result, cloneActiveGeneratedFixtureResult(result))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := writeActiveGeneratedFixturePlans([]activeGeneratedFixturePlan{plan}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != len(activeGeneratedFixtureAllowlist("p5extensions")) {
		t.Fatalf("updated paths=%d", len(changed))
	}
	for _, relative := range activeGeneratedFixtureAllowlist("p5extensions") {
		content, err := os.ReadFile(filepath.Join(root, "runtime", "testdata", "p5extensions", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, []byte("new")) {
			t.Fatalf("atomic update omitted %s", relative)
		}
	}
	source, err := os.ReadFile(filepath.Join(root, "runtime", "testdata", "p5extensions", "fixture.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, []byte("package p5extensions\n")) {
		t.Fatal("atomic update changed an unlisted source file")
	}
	err = filepath.WalkDir(filepath.Join(root, "runtime", "testdata", "p5extensions"), func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".golem-active-fixture-") {
			return fmt.Errorf("staged file remained at %s", candidate)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func prepareActiveGeneratedFixtureTree(t *testing.T, root, fixture string, content []byte) {
	t.Helper()
	writeActiveGeneratedFixtureTestFile(t, root, "runtime/testdata/"+fixture+"/fixture.go", []byte("package "+fixture+"\n"))
	for _, relative := range activeGeneratedFixtureAllowlist(fixture) {
		writeActiveGeneratedFixtureTestFile(t, root, "runtime/testdata/"+fixture+"/"+relative, content)
	}
}

func writeActiveGeneratedFixtureTestFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func syntheticActiveGeneratedFixtureResult(fixture string, content []byte) pipeline.Result {
	artifacts := make([]codegenmanifest.Artifact, 0, len(activeGeneratedFixtureAllowlist(fixture))+4)
	for _, relative := range activeGeneratedFixtureAllowlist(fixture) {
		artifacts = append(artifacts, codegenmanifest.Artifact{
			Path: "runtime/testdata/" + fixture + "/" + relative, Kind: codegenmanifest.ArtifactModelGo,
			Content: append([]byte(nil), content...),
		})
	}
	for _, relative := range activeGeneratedMetadataAllowlist {
		artifacts = append(artifacts, codegenmanifest.Artifact{Path: relative, Kind: codegenmanifest.ArtifactMetadata, Content: []byte("metadata")})
	}
	return pipeline.Result{Prospective: codegenmanifest.Result{GenerationDigest: "digest", Artifacts: artifacts}}
}

func cloneActiveGeneratedFixtureResult(value pipeline.Result) pipeline.Result {
	result := value
	result.Prospective.Artifacts = make([]codegenmanifest.Artifact, len(value.Prospective.Artifacts))
	for index, artifact := range value.Prospective.Artifacts {
		result.Prospective.Artifacts[index] = artifact
		result.Prospective.Artifacts[index].Content = append([]byte(nil), artifact.Content...)
	}
	return result
}
