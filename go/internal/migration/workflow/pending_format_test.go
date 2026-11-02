package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestPendingBackfillFromOlderContractFailsWithRegenerate(t *testing.T) {
	compiled, allProviders := socialResult(t)
	providers := providerSubset(t, allProviders, ir.PostgreSQL)
	module := t.TempDir()
	publish(t, module, prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil))
	previous := compiled.Compilation.Model
	current, nextProviders, _, _ := addRequiredPostSlug(t, previous, providers)
	fingerprint, err := ir.ModelFingerprint(current)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := PrepareNew(context.Background(), NewRequest{ModuleDir: module, Root: DefaultRoot, Name: "add_post_slug", Model: current, PreviousModel: &previous, ModelFingerprint: fingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: nextProviders})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := WritePendingBackfill(module, *pending.Pending)
	if err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(module, filepath.FromSlash(draft))
	written, err := os.ReadFile(absolute)
	if err != nil {
		t.Fatal(err)
	}
	attach := func() error {
		_, err := PrepareBackfillAttach(context.Background(), BackfillAttachRequest{
			ModuleDir: module, Root: DefaultRoot, MigrationID: pending.MigrationID, Field: "Post.Slug",
			SQL: []byte("UPDATE posts SET slug = title WHERE slug IS NULL;\n"), Render: nextProviders[0].Render,
		})
		return err
	}
	withFormat := func(version uint16) {
		field := func(value uint16) []byte { return []byte(fmt.Sprintf("\"formatVersion\": %d,", value)) }
		if !bytes.Contains(written, field(PendingBackfillFormatVersion)) {
			t.Fatalf("pending draft does not carry its format version: %.60s", written)
		}
		if err := os.WriteFile(absolute, bytes.Replace(written, field(PendingBackfillFormatVersion), field(version), 1), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	withFormat(PendingBackfillFormatVersion - 1)
	const older = "pending backfill format 1 predates this golem (format 2): regenerate with golem generate, delete .golem-pending-backfill.json, and run golem migration new again"
	if err := attach(); err == nil || err.Error() != older {
		t.Fatalf("attach of a v0.7.x draft = %v, want %q", err, older)
	}
	withFormat(PendingBackfillFormatVersion + 1)
	const newer = "pending backfill format 3 is newer than this golem (format 2): upgrade golem"
	if err := attach(); err == nil || err.Error() != newer {
		t.Fatalf("attach of a newer draft = %v, want %q", err, newer)
	}
	withFormat(PendingBackfillFormatVersion)
	if err := attach(); err != nil {
		t.Fatalf("attach of a current draft failed: %v", err)
	}
}
