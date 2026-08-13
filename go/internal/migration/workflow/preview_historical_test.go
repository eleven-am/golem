package workflow

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestCheckedSocialProspectivePhysicalV3UsesFrozenModelHeadFingerprint(t *testing.T) {
	social, state := checkedSocialPreV3Fixture(t)
	if state.HeadModel == nil || state.HeadModelFingerprint == "" {
		t.Fatal("checked social reviewed ModelIR head is absent")
	}
	currentProjectionFingerprint, err := ir.ModelFingerprint(*state.HeadModel)
	if err != nil {
		t.Fatal(err)
	}
	if currentProjectionFingerprint == state.HeadModelFingerprint {
		t.Fatal("checked-social fixture no longer distinguishes current projection from its frozen reviewed fingerprint")
	}

	providers := make([]Provider, 0, 2)
	for _, providerID := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		history := state.Histories[providerID]
		if len(history.Manifest.Entries) == 0 {
			t.Fatalf("checked social provider %s history is empty", providerID)
		}
		schema := history.Manifest.Entries[len(history.Manifest.Entries)-1].AfterSnapshot
		schema.Version, schema.CanonicalVersion = 3, 3
		schema, err = physical.NormalizeHistoricalV3(schema)
		if err != nil {
			t.Fatal(err)
		}
		physicalFingerprint, err := physical.PhysicalFingerprint(schema)
		if err != nil {
			t.Fatal(err)
		}
		systemFingerprint, err := physical.SystemFingerprint(schema.Provider, schema.System)
		if err != nil {
			t.Fatal(err)
		}
		render := func(entry migration.ManifestEntry) ([]byte, error) {
			if providerID == ir.SQLite {
				script, renderErr := sqlite.New().RenderMigration(entry)
				return []byte(script.SQL()), renderErr
			}
			script, renderErr := postgresql.New().RenderMigration(entry)
			return []byte(script.SQL()), renderErr
		}
		providers = append(providers, Provider{
			Result: pipeline.ProviderResult{
				Provider: schema.Provider, Schema: schema,
				Fingerprint: ir.Fingerprint(physicalFingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String()),
			},
			Render: render,
		})
	}

	preview, err := Preview(context.Background(), PreviewRequest{
		ModuleDir: social, Root: DefaultRoot,
		Model: *state.HeadModel, ModelFingerprint: currentProjectionFingerprint, PreviousModel: state.HeadModel,
		Providers: providers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.BeforeModelFingerprint != state.HeadModelFingerprint {
		t.Fatalf("prospective before-model fingerprint=%s want frozen %s", preview.BeforeModelFingerprint, state.HeadModelFingerprint)
	}
	for _, provider := range preview.Providers {
		if len(provider.Plan.Operations) != 1 || provider.Plan.Operations[0].Kind != migration.RecordSchemaVersion ||
			provider.Plan.Operations[0].Risk != migration.RiskSafe || len(provider.Plan.Phases) != 1 {
			t.Fatalf("provider %s format-only plan=%#v", provider.Provider.Result.Provider.Provider, provider.Plan)
		}
	}
}

func TestCheckedSocialPrepareNewCopiesExactReviewedModelHeadBytesAndReloads(t *testing.T) {
	social, state := checkedSocialPreV3Fixture(t)
	if state.HeadModel == nil || state.Publication == nil {
		t.Fatal("checked social reviewed head is absent")
	}
	currentProjectionFingerprint, err := ir.ModelFingerprint(*state.HeadModel)
	if err != nil {
		t.Fatal(err)
	}
	providers := checkedSocialPhysicalV3Providers(t, state)
	prepared, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: social, Root: DefaultRoot, Name: "ir_physical_v3",
		Model: *state.HeadModel, PreviousModel: state.HeadModel,
		ModelFingerprint: currentProjectionFingerprint, ContractFingerprint: state.Publication.ContractFingerprint,
		Providers: providers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Pending != nil || prepared.MigrationID != "0004_ir_physical_v3" {
		t.Fatalf("format-only preparation=%#v", prepared)
	}

	priorPath := filepath.ToSlash(filepath.Join(DefaultRoot, "models", "0003_physical_v2.after.snapshot.json"))
	newPath := filepath.ToSlash(filepath.Join(DefaultRoot, "models", "0004_ir_physical_v3.before.snapshot.json"))
	var prior, next []byte
	for _, artifact := range state.Artifacts {
		if artifact.Path == priorPath {
			prior = artifact.Content
		}
	}
	for _, artifact := range prepared.Prospective.Artifacts {
		if artifact.Path == newPath {
			next = artifact.Content
		}
	}
	if len(prior) == 0 || !bytes.Equal(next, prior) {
		t.Fatal("incremental before-model snapshot is not the exact prior reviewed after-model bytes")
	}
	currentReencoding, err := modelSnapshotBytes(*state.HeadModel)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(currentReencoding, prior) {
		t.Fatal("checked-social fixture no longer kills mutable current ModelIR re-encoding")
	}

	temporary := t.TempDir()
	if _, err := publication.Apply(context.Background(), publication.Request{
		ModuleDir: temporary, ManifestPath: prepared.PublicationPath,
		Prospective: prepared.Prospective, Mode: publication.ModePublish,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadReviewedState(context.Background(), temporary, DefaultRoot)
	if err != nil {
		t.Fatalf("reload prospective format-only history: %v", err)
	}
	if reloaded.HeadModelFingerprint != currentProjectionFingerprint {
		t.Fatalf("reloaded head fingerprint=%s want %s", reloaded.HeadModelFingerprint, currentProjectionFingerprint)
	}
}

func TestCheckedSocialPendingBackfillBindsFrozenReviewedHeadAndExactBytes(t *testing.T) {
	_, state := checkedSocialPreV3Fixture(t)
	history := state.Histories[ir.PostgreSQL]
	if len(history.Manifest.Entries) == 0 {
		t.Fatal("checked social PostgreSQL history is empty")
	}
	head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
	pending := PendingBackfill{BeforeModel: *state.HeadModel, Entry: migration.ManifestEntry{
		ParentID: head.ID, ParentChainHash: head.ChainHash,
		BeforePhysical: head.AfterPhysical, BeforeModel: migration.Digest(state.HeadModelFingerprint),
	}}
	if err := validatePendingBackfillReviewedHead(pending, state, history); err != nil {
		t.Fatal(err)
	}
	tampered := pending
	tampered.Entry.BeforeModel = migration.Digest(ir.Fingerprint(strings.Repeat("f", 64)))
	if err := validatePendingBackfillReviewedHead(tampered, state, history); err == nil {
		t.Fatal("pending backfill accepted a forged reviewed-head fingerprint")
	}
	tampered = pending
	tampered.Entry.ParentID = "forged_parent"
	if err := validatePendingBackfillReviewedHead(tampered, state, history); err == nil {
		t.Fatal("pending backfill accepted a forged reviewed-head migration ID")
	}
	tampered = pending
	tampered.BeforeModel = cloneModel(t, pending.BeforeModel)
	tampered.BeforeModel.Models[0].LogicalName += "_forged"
	if err := validatePendingBackfillReviewedHead(tampered, state, history); err == nil {
		t.Fatal("pending backfill accepted before-model contents from outside the reviewed head")
	}

	localHistory := history
	localHistory.Manifest.Entries = append(append([]migration.ManifestEntry(nil), history.Manifest.Entries...), migration.ManifestEntry{ID: "pending"})
	got, err := reviewedBackfillBeforeModelSnapshotBytes(state, DefaultRoot, localHistory)
	if err != nil {
		t.Fatal(err)
	}
	expectedPath := filepath.ToSlash(filepath.Join(DefaultRoot, "models", string(head.ID)+".after.snapshot.json"))
	var want []byte
	for _, artifact := range state.Artifacts {
		if artifact.Path == expectedPath {
			want = artifact.Content
		}
	}
	if len(want) == 0 || !bytes.Equal(got, want) {
		t.Fatal("pending backfill did not resolve exact verified reviewed-head model bytes")
	}
	currentBytes, err := modelSnapshotBytes(*state.HeadModel)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(currentBytes, got) {
		t.Fatal("checked-social pending fixture no longer distinguishes frozen bytes from current re-encoding")
	}
}

func checkedSocialPreV3Fixture(t *testing.T) (string, State) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate checked social module")
	}
	social := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "..", "examples", "social"))
	released, err := loadReviewedState(context.Background(), social, DefaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	retainedPaths := map[string]struct{}{}
	manifestArtifacts := make([]codegenmanifest.Artifact, 0, 2)
	var modelFingerprint ir.Fingerprint
	providerFingerprints := make([]codegenmanifest.ProviderFingerprint, 0, 2)
	for _, providerID := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		history := released.Histories[providerID]
		if len(history.Manifest.Entries) < 3 || history.Manifest.Entries[2].ID != "0003_physical_v2" {
			t.Fatalf("checked social provider %s lacks released pre-v3 head", providerID)
		}
		history.Manifest.Entries = append([]migration.ManifestEntry(nil), history.Manifest.Entries[:3]...)
		files := map[string][]byte{}
		for _, entry := range history.Manifest.Entries {
			retainedPaths[filepath.ToSlash(filepath.Join(DefaultRoot, "models", string(entry.ID)+".after.snapshot.json"))] = struct{}{}
			retainedPaths[filepath.ToSlash(filepath.Join(DefaultRoot, "models", string(entry.ID)+".before.snapshot.json"))] = struct{}{}
			for _, file := range entry.Files {
				files[file.Path] = history.Files[file.Path]
				retainedPaths[file.Path] = struct{}{}
			}
			for _, companion := range entry.Manual {
				files[companion.File.Path] = history.Files[companion.File.Path]
				retainedPaths[companion.File.Path] = struct{}{}
			}
		}
		manifestBytes, err := migration.EncodeManifest(history.Manifest, files)
		if err != nil {
			t.Fatal(err)
		}
		manifestArtifacts = append(manifestArtifacts, codegenmanifest.Artifact{
			Path: filepath.ToSlash(filepath.Join(DefaultRoot, string(providerID), "manifest.json")),
			Kind: codegenmanifest.ArtifactMigrationManifest, Content: manifestBytes,
		})
		head := history.Manifest.Entries[2]
		systemFingerprint, err := physical.HistoricalSystemFingerprint(head.AfterSnapshot)
		if err != nil {
			t.Fatal(err)
		}
		providerFingerprints = append(providerFingerprints, codegenmanifest.ProviderFingerprint{
			Provider: providerID, Fingerprint: ir.Fingerprint(head.AfterPhysical), SystemFingerprint: ir.Fingerprint(systemFingerprint.String()),
		})
		if modelFingerprint == "" {
			modelFingerprint = ir.Fingerprint(head.AfterModel)
		}
	}
	artifacts := checkedSocialPreV3Artifacts(t, released.Artifacts, retainedPaths)
	artifacts = append(artifacts, manifestArtifacts...)
	prospective, err := codegenmanifest.Build(codegenmanifest.Request{
		ModelFingerprint: modelFingerprint, ContractFingerprint: released.Publication.ContractFingerprint,
		ProviderFingerprints: providerFingerprints, GeneratorVersion: GeneratorVersion,
		TemplateABIVersion: codegenmanifest.TemplateABIVersion, Artifacts: artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	if _, err := publication.Apply(context.Background(), publication.Request{
		ModuleDir: temporary, ManifestPath: filepath.ToSlash(filepath.Join(DefaultRoot, PublicationFilename)),
		Prospective: prospective, Mode: publication.ModePublish,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := loadReviewedState(context.Background(), temporary, DefaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	return temporary, state
}

func checkedSocialPreV3Artifacts(t *testing.T, released []codegenmanifest.Artifact, retainedPaths map[string]struct{}) []codegenmanifest.Artifact {
	t.Helper()
	migrationPrefix := filepath.ToSlash(DefaultRoot) + "/"
	found := make(map[string]struct{}, len(retainedPaths))
	artifacts := make([]codegenmanifest.Artifact, 0, len(retainedPaths))
	for _, artifact := range released {
		if artifact.Kind == codegenmanifest.ArtifactMigrationManifest {
			continue
		}
		if !strings.HasPrefix(artifact.Path, migrationPrefix) {
			artifacts = append(artifacts, artifact)
			continue
		}
		if _, keep := retainedPaths[artifact.Path]; !keep {
			continue
		}
		found[artifact.Path] = struct{}{}
		artifacts = append(artifacts, artifact)
	}
	for path := range retainedPaths {
		if _, ok := found[path]; !ok {
			t.Fatalf("checked social pre-v3 projection omitted intended immutable artifact %q", path)
		}
	}
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Path, migrationPrefix) {
			continue
		}
		if _, ok := retainedPaths[artifact.Path]; !ok {
			t.Fatalf("checked social pre-v3 projection retained later migration artifact %q", artifact.Path)
		}
	}
	return artifacts
}

func checkedSocialPhysicalV3Providers(t *testing.T, state State) []Provider {
	t.Helper()
	providers := make([]Provider, 0, 2)
	for _, providerID := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		history := state.Histories[providerID]
		if len(history.Manifest.Entries) == 0 {
			t.Fatalf("checked social provider %s history is empty", providerID)
		}
		schema := history.Manifest.Entries[len(history.Manifest.Entries)-1].AfterSnapshot
		schema.Version, schema.CanonicalVersion = 3, 3
		var err error
		schema, err = physical.NormalizeHistoricalV3(schema)
		if err != nil {
			t.Fatal(err)
		}
		physicalFingerprint, err := physical.PhysicalFingerprint(schema)
		if err != nil {
			t.Fatal(err)
		}
		systemFingerprint, err := physical.SystemFingerprint(schema.Provider, schema.System)
		if err != nil {
			t.Fatal(err)
		}
		render := func(entry migration.ManifestEntry) ([]byte, error) {
			if providerID == ir.SQLite {
				script, renderErr := sqlite.New().RenderMigration(entry)
				return []byte(script.SQL()), renderErr
			}
			script, renderErr := postgresql.New().RenderMigration(entry)
			return []byte(script.SQL()), renderErr
		}
		providers = append(providers, Provider{
			Result: pipeline.ProviderResult{
				Provider: schema.Provider, Schema: schema,
				Fingerprint: ir.Fingerprint(physicalFingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String()),
			},
			Render: render,
		})
	}
	return providers
}

func TestInitialPreviewFingerprintUsesCanonicalEmptyModel(t *testing.T) {
	fingerprint, err := previewBeforeModelFingerprint(State{}, 0, ir.ModelIR{})
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != ir.EmptyModelFingerprint() {
		t.Fatalf("initial before-model fingerprint=%s want %s", fingerprint, ir.EmptyModelFingerprint())
	}
}

func TestPreviewHistoricalFingerprintSourceHasOneFrozenAuthority(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate preview source")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "preview.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Count(source, "return state.HeadModelFingerprint, nil") != 1 ||
		strings.Contains(source, "ir.ModelFingerprint(beforeModel)") {
		t.Fatal("preview historical model head is not owned solely by the frozen reviewed fingerprint")
	}
	workflowSource, err := os.ReadFile(filepath.Join(filepath.Dir(current), "workflow.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflowSource)
	if strings.Count(text, "reviewedBeforeModelSnapshotBytes(state, root, headLength)") != 1 ||
		strings.Contains(text, "modelSnapshotBytes(beforeModel)") {
		t.Fatal("incremental authoring re-encodes the historical ModelIR head instead of copying reviewed bytes")
	}
	backfillSource, err := os.ReadFile(filepath.Join(filepath.Dir(current), "backfill.go"))
	if err != nil {
		t.Fatal(err)
	}
	backfillText := string(backfillSource)
	if strings.Count(backfillText, "reviewedBackfillBeforeModelSnapshotBytes(state, root, history)") != 1 ||
		strings.Contains(backfillText, "modelSnapshotBytes(pending.BeforeModel)") {
		t.Fatal("backfill attach re-encodes the historical ModelIR head instead of copying reviewed bytes")
	}
}
