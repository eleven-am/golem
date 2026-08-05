package publication

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestPublishCheckAndStaleRemoval(t *testing.T) {
	directory := t.TempDir()
	first := build(t, artifact("pkg/a.gen.go", "package pkg\n"), artifact("pkg/stale.gen.go", "package pkg\n"))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: first, Mode: ModeCheck}); err != nil {
		t.Fatal(err)
	}
	second := build(t, artifact("pkg/a.gen.go", "package pkg\nvar Version = 2\n"))
	checked, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: second, Mode: ModeCheck})
	if err == nil || len(checked.Changed) == 0 || len(checked.Stale) != 1 {
		t.Fatalf("check result=%#v err=%v", checked, err)
	}
	if content := read(t, directory, "pkg/a.gen.go"); strings.Contains(content, "Version = 2") {
		t.Fatal("check mode mutated generated output")
	}
	published, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: second})
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Stale) != 1 {
		t.Fatalf("stale=%v", published.Stale)
	}
	if _, err := os.Stat(filepath.Join(directory, "pkg/stale.gen.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file remains: %v", err)
	}
}

func TestStaleRemovalRequiresExactOwnedHeader(t *testing.T) {
	directory := t.TempDir()
	first := build(t, artifact("pkg/stale.gen.go", "package pkg\n"))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: first}); err != nil {
		t.Fatal(err)
	}
	write(t, directory, "pkg/stale.gen.go", "// handwritten\npackage pkg\n")
	empty := build(t)
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: empty}); err == nil {
		t.Fatal("stale removal without generated header succeeded")
	}
	if got := read(t, directory, "pkg/stale.gen.go"); !strings.HasPrefix(got, "// handwritten") {
		t.Fatal("unowned stale file was modified")
	}
}

func TestImmutableArtifactIsNeverOverwritten(t *testing.T) {
	directory := t.TempDir()
	old := artifact("migrations/001.sql", "CREATE TABLE old;\n")
	old.Immutable = true
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: build(t, old)}); err != nil {
		t.Fatal(err)
	}
	changed := artifact("migrations/001.sql", "CREATE TABLE changed;\n")
	changed.Immutable = true
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: build(t, changed)}); err == nil {
		t.Fatal("immutable migration was overwritten")
	}
	if !strings.Contains(read(t, directory, old.Path), "old") {
		t.Fatal("immutable file changed")
	}
}

func TestRewrittenOrStaleImmutableArtifactCannotBeLegitimized(t *testing.T) {
	directory := t.TempDir()
	original := artifact("migrations/001.sql", "CREATE TABLE original;\n")
	original.Immutable = true
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: build(t, original)}); err != nil {
		t.Fatal(err)
	}
	rewritten := artifact("migrations/001.sql", "CREATE TABLE rewritten;\n")
	rewritten.Immutable = true
	write(t, directory, rewritten.Path, string(rewritten.Content))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: build(t, rewritten)}); err == nil {
		t.Fatal("rewritten immutable file was legitimized by a regenerated manifest")
	}
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: build(t)}); err == nil {
		t.Fatal("stale immutable file was removed")
	}
}

func TestExistingUnmanagedTargetIsNeverAdopted(t *testing.T) {
	directory := t.TempDir()
	prospective := build(t, artifact("pkg/a.gen.go", "package pkg\n"))
	write(t, directory, "pkg/a.gen.go", string(prospective.Artifacts[0].Content))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: prospective}); err == nil {
		t.Fatal("unmanaged byte-identical target was adopted")
	}
}

func TestTamperedManifestBytesAreRejectedBeforeMutation(t *testing.T) {
	directory := t.TempDir()
	prospective := build(t, artifact("pkg/a.gen.go", "package pkg\n"))
	prospective.Bytes = bytes.Replace(prospective.Bytes, []byte(`"contentSha256": "`), []byte(`"contentSha256": "00`), 1)
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: prospective}); err == nil {
		t.Fatal("tampered manifest bytes succeeded")
	}
	if _, err := os.Stat(filepath.Join(directory, "pkg/a.gen.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tampered manifest mutated output")
	}
}

func TestInterruptedPublicationRecoversWithoutMixedFiles(t *testing.T) {
	steps := []Step{StepStaged, StepJournaled, StepBackedUp, StepInstalled, StepVerified, StepManifestInstalled}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			directory := t.TempDir()
			old := build(t, artifact("one/a.gen.go", "package one\nvar V = 1\n"), artifact("two/b.gen.go", "package two\nvar V = 1\n"))
			if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: old}); err != nil {
				t.Fatal(err)
			}
			newResult := build(t, artifact("one/a.gen.go", "package one\nvar V = 2\n"), artifact("two/b.gen.go", "package two\nvar V = 2\n"))
			injected := false
			_, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: newResult, Inject: func(current Step, _ string) error {
				if !injected && current == step {
					injected = true
					return errors.New("simulated crash")
				}
				return nil
			}})
			if err == nil || !injected {
				t.Fatalf("failure step %s was not injected: %v", step, err)
			}
			if err := Recover(context.Background(), directory, ""); err != nil {
				t.Fatal(err)
			}
			left, right := read(t, directory, "one/a.gen.go"), read(t, directory, "two/b.gen.go")
			leftNew, rightNew := strings.Contains(left, "V = 2"), strings.Contains(right, "V = 2")
			if leftNew != rightNew {
				t.Fatalf("mixed publication after recovery: left=%q right=%q", left, right)
			}
			if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(journalPath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal remains: %v", err)
			}
			if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: newResult}); err != nil {
				t.Fatalf("generation after recovery failed: %v", err)
			}
		})
	}
}

func TestApplyRoutesRecoveryAcrossManifestOwners(t *testing.T) {
	directory := t.TempDir()
	generated := build(t, artifact("generated/app.gen.go", "package generated\nvar Version = 1\n"))
	migration := build(t, artifact("migrations/sqlite/0001_initial.sql", "package migration\n"))

	injected := false
	_, err := Apply(context.Background(), Request{
		ModuleDir: directory, ManifestPath: "migrations/.golem-publication.json", Prospective: migration,
		Inject: func(step Step, _ string) error {
			if !injected && step == StepJournaled {
				injected = true
				return errors.New("simulated initial migration publication crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("initial migration interruption was not created: %v", err)
	}

	// Applying an unrelated generated-code publication must route and recover the
	// pending migration journal even though its inventory was never installed.
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: generated}); err != nil {
		t.Fatalf("cross-manifest Apply did not recover pending migration publication: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "migrations", ".golem-publication.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted initial migration inventory unexpectedly remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "migrations", "sqlite", "0001_initial.sql")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted initial migration artifact unexpectedly remains: %v", err)
	}
	if got := read(t, directory, "generated/app.gen.go"); !strings.Contains(got, "Version = 1") {
		t.Fatalf("generated publication was not installed after routed recovery: %q", got)
	}
}

func TestRecoverUsesEmbeddedManifestPathInsteadOfCallerGuess(t *testing.T) {
	directory := t.TempDir()
	old := build(t, artifact("generated/app.gen.go", "package generated\nvar Version = 1\n"))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: old}); err != nil {
		t.Fatal(err)
	}
	updated := build(t, artifact("generated/app.gen.go", "package generated\nvar Version = 2\n"))
	injected := false
	_, err := Apply(context.Background(), Request{
		ModuleDir: directory, Prospective: updated,
		Inject: func(step Step, _ string) error {
			if !injected && step == StepInstalled {
				injected = true
				return errors.New("simulated generated publication crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("generated interruption was not created: %v", err)
	}

	// The legacy manifest argument is intentionally wrong. Recovery must trust
	// only the validated canonical ManifestPath embedded in the journal.
	if err := Recover(context.Background(), directory, "migrations/.golem-publication.json"); err != nil {
		t.Fatalf("routed recovery failed: %v", err)
	}
	if got := read(t, directory, "generated/app.gen.go"); !strings.Contains(got, "Version = 1") {
		t.Fatalf("routed recovery did not restore the committed generation: %q", got)
	}
	if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(journalPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after routed recovery: %v", err)
	}
}

func TestValidationFailureAndConcurrentLockMutateNothing(t *testing.T) {
	directory := t.TempDir()
	initial := build(t, artifact("pkg/a.gen.go", "package pkg\n"))
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: initial}); err != nil {
		t.Fatal(err)
	}
	invalid := build(t, artifact("pkg/a.gen.go", "package pkg\nvar V = 2\n"))
	invalid.Artifacts[0].Content = []byte("tampered")
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: invalid}); err == nil {
		t.Fatal("inconsistent prospective set succeeded")
	}
	if strings.Contains(read(t, directory, "pkg/a.gen.go"), "V = 2") {
		t.Fatal("validation failure mutated output")
	}
	lock, err := acquireLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.close()
	if _, err := Apply(context.Background(), Request{ModuleDir: directory, Prospective: initial}); err == nil {
		t.Fatal("concurrent publication was not refused")
	}
}

func build(t *testing.T, artifacts ...manifest.Artifact) manifest.Result {
	t.Helper()
	result, err := manifest.Build(manifest.Request{ModelFingerprint: ir.Fingerprint("model"), ContractFingerprint: ir.Fingerprint("contract"), Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func artifact(path, body string) manifest.Artifact {
	return manifest.Artifact{Path: path, Kind: manifest.ArtifactModelGo, Content: []byte(manifest.GeneratedHeader + "\n" + body), GeneratedHeader: manifest.GeneratedHeader}
}
func read(t *testing.T, directory, relative string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
func write(t *testing.T, directory, relative, content string) {
	t.Helper()
	path := filepath.Join(directory, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
