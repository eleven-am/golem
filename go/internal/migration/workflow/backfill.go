package workflow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/explain"
)

const (
	PendingBackfillFormatVersion uint16 = 1
	PendingBackfillFilename             = ".golem-pending-backfill.json"
)

// PendingBackfill is an authoring draft, not reviewed migration history. It is
// deliberately stored outside the publication artifact inventory and is never
// consumed by migration apply or a manifest parser.
type PendingBackfill struct {
	FormatVersion           uint16                  `json:"formatVersion"`
	Root                    string                  `json:"root"`
	PublicationPath         string                  `json:"publicationPath"`
	ParentPublicationSHA256 string                  `json:"parentPublicationSha256"`
	MigrationID             migration.MigrationID   `json:"migrationId"`
	Provider                ir.Provider             `json:"provider"`
	ModelFingerprint        ir.Fingerprint          `json:"modelFingerprint"`
	ContractFingerprint     ir.Fingerprint          `json:"contractFingerprint"`
	ProviderFingerprint     ir.Fingerprint          `json:"providerFingerprint"`
	SystemFingerprint       ir.Fingerprint          `json:"systemFingerprint"`
	BeforeModel             ir.ModelIR              `json:"beforeModel"`
	AfterModel              ir.ModelIR              `json:"afterModel"`
	Entry                   migration.ManifestEntry `json:"entry"`
}

type BackfillAttachRequest struct {
	ModuleDir   string
	Root        string
	MigrationID migration.MigrationID
	Field       string
	SQL         []byte
	Render      func(migration.ManifestEntry) ([]byte, error)
}

type BackfillAttachResult struct {
	Prospective     codegenmanifest.Result
	PublicationPath string
	PendingPath     string
	PendingSHA256   string
	MigrationID     migration.MigrationID
	Provider        ir.Provider
	Entry           migration.ManifestEntry
	Report          explain.Report
}

func pendingBackfillPath(root string) string { return path.Join(root, PendingBackfillFilename) }

// PendingBackfillID reads and validates the sole authoring draft so read-only
// history consumers can distinguish it from sealed immutable history.
func PendingBackfillID(moduleDir, root string) (migration.MigrationID, bool, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return "", false, err
	}
	encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(pendingBackfillPath(canonical))))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.New("pending backfill could not be read")
	}
	value, err := parsePendingBackfill(encoded)
	if err != nil {
		return "", false, errors.New("pending backfill is invalid")
	}
	return value.MigrationID, true, nil
}

func encodePendingBackfill(value PendingBackfill) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func parsePendingBackfill(encoded []byte) (PendingBackfill, error) {
	var value PendingBackfill
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return PendingBackfill{}, fmt.Errorf("pending backfill: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return PendingBackfill{}, fmt.Errorf("pending backfill contains trailing JSON")
	}
	canonical, err := encodePendingBackfill(value)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return PendingBackfill{}, fmt.Errorf("pending backfill is not canonical normalized JSON")
	}
	if err := validatePendingBackfill(value); err != nil {
		return PendingBackfill{}, err
	}
	return value, nil
}

func validatePendingBackfill(value PendingBackfill) error {
	if value.FormatVersion != PendingBackfillFormatVersion {
		return fmt.Errorf("pending backfill format version %d is unsupported", value.FormatVersion)
	}
	root, err := canonicalRoot(value.Root)
	if err != nil || root != value.Root || value.PublicationPath != path.Join(root, PublicationFilename) {
		return fmt.Errorf("pending backfill root/publication path is invalid")
	}
	if value.MigrationID == "" || value.Provider != ir.PostgreSQL || value.Entry.ID != value.MigrationID || value.Entry.AfterSnapshot.Provider.Provider != value.Provider {
		return fmt.Errorf("pending backfill has an invalid migration/provider identity")
	}
	if len(value.Entry.Files) != 0 || len(value.Entry.Manual) != 0 || len(value.Entry.Approvals) != 0 || value.Entry.ChainHash != "" {
		return fmt.Errorf("pending backfill must not contain sealed history fields")
	}
	decodedDigest, digestErr := hex.DecodeString(value.ParentPublicationSHA256)
	if digestErr != nil || len(decodedDigest) != 32 {
		return fmt.Errorf("pending backfill parent publication digest is invalid")
	}
	_, beforeErr := ir.ModelFingerprint(value.BeforeModel)
	afterFingerprint, afterErr := ir.ModelFingerprint(value.AfterModel)
	if beforeErr != nil || afterErr != nil || afterFingerprint != value.ModelFingerprint || migration.Digest(afterFingerprint) != value.Entry.AfterModel {
		return fmt.Errorf("pending backfill ModelIR snapshots or fingerprints are inconsistent")
	}
	plan, err := migration.DiffReviewed(value.Entry.BeforeSnapshot, value.Entry.AfterSnapshot)
	if err != nil || !equalOperations(plan, value.Entry) {
		return fmt.Errorf("pending backfill operation graph differs from its physical snapshots")
	}
	backfills := pendingBackfillOperations(value.Entry)
	if len(backfills) != 1 {
		return fmt.Errorf("pending backfill must bind exactly one required-column backfill")
	}
	for _, operation := range value.Entry.Operations {
		if migration.RequiresManualCompanion(operation) && operation.ID != backfills[0].ID {
			return fmt.Errorf("pending backfill contains unsupported manual operation %s", operation.ID)
		}
	}
	return nil
}

func equalOperations(plan migration.Plan, entry migration.ManifestEntry) bool {
	left, _ := json.Marshal(struct {
		Operations []migration.Operation `json:"operations"`
		Phases     []migration.Phase     `json:"phases"`
	}{plan.Operations, plan.Phases})
	right, _ := json.Marshal(struct {
		Operations []migration.Operation `json:"operations"`
		Phases     []migration.Phase     `json:"phases"`
	}{entry.Operations, entry.Phases})
	return bytes.Equal(left, right)
}

func pendingBackfillOperations(entry migration.ManifestEntry) []migration.Operation {
	var result []migration.Operation
	for _, operation := range entry.Operations {
		if operation.Kind == migration.BackfillColumn {
			result = append(result, operation)
		}
	}
	return result
}

// WritePendingBackfill atomically creates the sole non-authoritative draft and
// refuses to overwrite any existing draft.
func WritePendingBackfill(moduleDir string, value PendingBackfill) (string, error) {
	encoded, err := encodePendingBackfill(value)
	if err != nil {
		return "", err
	}
	if _, err := parsePendingBackfill(encoded); err != nil {
		return "", err
	}
	relative := pendingBackfillPath(value.Root)
	absolute := filepath.Join(moduleDir, filepath.FromSlash(relative))
	if _, err := os.Stat(absolute); err == nil {
		return "", fmt.Errorf("pending backfill %s already exists", relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".golem-pending-*")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Link(temporaryName, absolute); errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("pending backfill %s already exists", relative)
	} else if err != nil {
		return "", err
	}
	return relative, nil
}

func PrepareBackfillAttach(ctx context.Context, request BackfillAttachRequest) (BackfillAttachResult, error) {
	if err := ctx.Err(); err != nil {
		return BackfillAttachResult{}, err
	}
	root, err := canonicalRoot(request.Root)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	pendingPath := pendingBackfillPath(root)
	pendingBytes, err := os.ReadFile(filepath.Join(request.ModuleDir, filepath.FromSlash(pendingPath)))
	if err != nil {
		return BackfillAttachResult{}, fmt.Errorf("read pending backfill: %w", err)
	}
	pending, err := parsePendingBackfill(pendingBytes)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	if pending.Root != root || pending.MigrationID != request.MigrationID {
		return BackfillAttachResult{}, fmt.Errorf("pending backfill does not match requested migration %s", request.MigrationID)
	}
	if request.Render == nil {
		return BackfillAttachResult{}, fmt.Errorf("pending backfill attach requires a PostgreSQL renderer")
	}
	if err := migration.ValidateReviewedBackfillArtifact(request.SQL); err != nil {
		return BackfillAttachResult{}, err
	}
	state, err := loadReviewedState(ctx, request.ModuleDir, root)
	if err != nil || state.Publication == nil {
		return BackfillAttachResult{}, fmt.Errorf("read pending backfill parent history: %w", err)
	}
	publicationBytes, err := os.ReadFile(filepath.Join(request.ModuleDir, filepath.FromSlash(pending.PublicationPath)))
	if err != nil || codegenmanifest.ContentHash(publicationBytes) != pending.ParentPublicationSHA256 {
		return BackfillAttachResult{}, fmt.Errorf("pending backfill parent publication is stale")
	}
	history, ok := state.Histories[ir.PostgreSQL]
	if !ok || len(state.Histories) != 1 {
		return BackfillAttachResult{}, fmt.Errorf("pending reviewed backfills require PostgreSQL-only history")
	}
	for _, entry := range history.Manifest.Entries {
		if entry.ID == pending.MigrationID {
			return BackfillAttachResult{}, fmt.Errorf("migration %s is already sealed", pending.MigrationID)
		}
	}
	if err := validatePendingBackfillReviewedHead(pending, state, history); err != nil {
		return BackfillAttachResult{}, fmt.Errorf("pending backfill parent chain is stale")
	}
	modelID, fieldID, err := resolvePendingField(pending, request.Field)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	backfill := pendingBackfillOperations(pending.Entry)[0]
	if ir.FieldID(backfill.ObjectID) != fieldID {
		return BackfillAttachResult{}, fmt.Errorf("field %s does not own the pending backfill operation", request.Field)
	}
	owner, exists := migration.BackfillOwner(pending.Entry.AfterSnapshot, fieldID)
	if !exists || owner != modelID {
		return BackfillAttachResult{}, fmt.Errorf("field %s does not resolve to the pending physical target", request.Field)
	}
	entry := pending.Entry
	for _, operation := range entry.Operations {
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	companionPath := path.Join(root, string(ir.PostgreSQL), string(entry.ID)+".backfill."+string(backfill.ID)+".sql")
	if _, err := os.Stat(filepath.Join(request.ModuleDir, filepath.FromSlash(companionPath))); err == nil {
		return BackfillAttachResult{}, fmt.Errorf("reviewed backfill artifact %s already exists", companionPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackfillAttachResult{}, err
	}
	entry.Manual = []migration.ManualCompanion{{OperationID: backfill.ID, File: migration.FileChecksum{Path: companionPath, SHA256: migration.Checksum(request.SQL)}, Postcondition: migration.BackfillPostcondition(modelID, fieldID)}}
	plan, err := migration.DiffReviewed(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	if err := migration.ValidatePlan(plan, entry.Approvals); err != nil {
		return BackfillAttachResult{}, err
	}
	rendered, err := request.Render(entry)
	if err != nil {
		return BackfillAttachResult{}, fmt.Errorf("render reviewed backfill migration: %w", err)
	}
	prefix := path.Join(root, string(ir.PostgreSQL), string(entry.ID))
	beforeBytes, err := historicalSnapshotBytes(entry.BeforeSnapshot)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	afterBytes, err := historicalSnapshotBytes(entry.AfterSnapshot)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	newFiles := map[string][]byte{prefix + ".sql": rendered, prefix + ".before.snapshot.json": beforeBytes, prefix + ".after.snapshot.json": afterBytes, companionPath: append([]byte(nil), request.SQL...)}
	paths := make([]string, 0, len(newFiles))
	for filename := range newFiles {
		paths = append(paths, filename)
	}
	sort.Strings(paths)
	for _, filename := range paths {
		if filename == companionPath {
			continue
		}
		entry.Files = append(entry.Files, migration.FileChecksum{Path: filename, SHA256: migration.Checksum(newFiles[filename])})
	}
	entry.ChainHash = migration.ChainHash(entry)
	report, err := explain.BuildReviewed(entry, newFiles)
	if err != nil {
		return BackfillAttachResult{}, fmt.Errorf("render reviewed backfill migration plan: %w", err)
	}
	history.Manifest.Entries = append(history.Manifest.Entries, entry)
	for filename, content := range newFiles {
		history.Files[filename] = content
	}
	manifestBytes, err := migration.EncodeManifest(history.Manifest, history.Files)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	artifactByPath := map[string]codegenmanifest.Artifact{}
	for _, artifact := range state.Artifacts {
		artifactByPath[artifact.Path] = artifact
	}
	for filename, content := range newFiles {
		artifactByPath[filename] = codegenmanifest.Artifact{Path: filename, Kind: artifactKind(filename), Content: content, Immutable: true}
	}
	manifestPath := path.Join(root, string(ir.PostgreSQL), "manifest.json")
	artifactByPath[manifestPath] = codegenmanifest.Artifact{Path: manifestPath, Kind: codegenmanifest.ArtifactMigrationManifest, Content: manifestBytes}
	modelPrefix := path.Join(root, "models", string(entry.ID))
	beforeModelBytes, err := reviewedBackfillBeforeModelSnapshotBytes(state, root, history)
	if err != nil {
		return BackfillAttachResult{}, err
	}
	afterModelBytes, _ := modelSnapshotBytes(pending.AfterModel)
	artifactByPath[modelPrefix+".before.snapshot.json"] = codegenmanifest.Artifact{Path: modelPrefix + ".before.snapshot.json", Kind: codegenmanifest.ArtifactMigrationSnapshot, Content: beforeModelBytes, Immutable: true}
	artifactByPath[modelPrefix+".after.snapshot.json"] = codegenmanifest.Artifact{Path: modelPrefix + ".after.snapshot.json", Kind: codegenmanifest.ArtifactMigrationSnapshot, Content: afterModelBytes, Immutable: true}
	artifacts := make([]codegenmanifest.Artifact, 0, len(artifactByPath))
	for _, artifact := range artifactByPath {
		artifacts = append(artifacts, artifact)
	}
	prospective, err := codegenmanifest.Build(codegenmanifest.Request{ModelFingerprint: pending.ModelFingerprint, ContractFingerprint: pending.ContractFingerprint, ProviderFingerprints: []codegenmanifest.ProviderFingerprint{{Provider: ir.PostgreSQL, Fingerprint: pending.ProviderFingerprint, SystemFingerprint: pending.SystemFingerprint}}, GeneratorVersion: GeneratorVersion, TemplateABIVersion: codegenmanifest.TemplateABIVersion, Artifacts: artifacts})
	if err != nil {
		return BackfillAttachResult{}, err
	}
	return BackfillAttachResult{Prospective: prospective, PublicationPath: pending.PublicationPath, PendingPath: pendingPath, PendingSHA256: codegenmanifest.ContentHash(pendingBytes), MigrationID: entry.ID, Provider: ir.PostgreSQL, Entry: entry, Report: report}, nil
}

func reviewedBackfillBeforeModelSnapshotBytes(state State, root string, history History) ([]byte, error) {
	// PrepareBackfillAttach appends the pending entry to its local history copy
	// before assembling artifacts; the reviewed state still owns the exact
	// previous head and its immutable bytes.
	return reviewedBeforeModelSnapshotBytes(state, root, len(history.Manifest.Entries)-1)
}

func validatePendingBackfillReviewedHead(pending PendingBackfill, state State, history History) error {
	if len(history.Manifest.Entries) == 0 || state.HeadModel == nil || state.HeadModelFingerprint == "" {
		return fmt.Errorf("reviewed pending-backfill parent is absent")
	}
	head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
	if pending.Entry.ParentID != head.ID || pending.Entry.ParentChainHash != head.ChainHash ||
		pending.Entry.BeforePhysical != head.AfterPhysical || pending.Entry.BeforeModel != head.AfterModel ||
		pending.Entry.BeforeModel != migration.Digest(state.HeadModelFingerprint) {
		return fmt.Errorf("reviewed pending-backfill parent does not match the frozen head")
	}
	pendingModel, pendingErr := ir.CanonicalModel(pending.BeforeModel)
	reviewedModel, reviewedErr := ir.CanonicalModel(*state.HeadModel)
	if pendingErr != nil || reviewedErr != nil || !bytes.Equal(pendingModel, reviewedModel) {
		return fmt.Errorf("pending backfill before-model contents do not match the reviewed head")
	}
	return nil
}

func RemovePendingBackfill(moduleDir, relative, expectedSHA256 string) error {
	absolute := filepath.Join(moduleDir, filepath.FromSlash(relative))
	content, err := os.ReadFile(absolute)
	if err != nil {
		return err
	}
	if codegenmanifest.ContentHash(content) != expectedSHA256 {
		return fmt.Errorf("pending backfill changed while sealing")
	}
	return os.Remove(absolute)
}

func resolvePendingField(pending PendingBackfill, selector string) (ir.ModelID, ir.FieldID, error) {
	parts := strings.Split(selector, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("backfill field %q must be Model.Field", selector)
	}
	var afterModel *ir.ModelDeclIR
	for index := range pending.AfterModel.Models {
		model := &pending.AfterModel.Models[index]
		if model.Go.Name == parts[0] || model.LogicalName == parts[0] {
			if afterModel != nil {
				return "", "", fmt.Errorf("backfill model %q is ambiguous", parts[0])
			}
			afterModel = model
		}
	}
	if afterModel == nil {
		return "", "", fmt.Errorf("backfill model %q does not exist", parts[0])
	}
	var afterField *ir.FieldIR
	for index := range afterModel.Fields {
		field := &afterModel.Fields[index]
		if field.GoName == parts[1] || field.LogicalName == parts[1] {
			if afterField != nil {
				return "", "", fmt.Errorf("backfill field %q is ambiguous", selector)
			}
			afterField = field
		}
	}
	if afterField == nil || afterField.Kind != ir.FieldScalar || afterField.Scalar == nil || afterField.Scalar.Nullable || afterField.Scalar.Default != nil || afterField.Scalar.Generation != nil {
		return "", "", fmt.Errorf("backfill field %q must be a new required stored scalar without a default or generated expression", selector)
	}
	for _, model := range pending.BeforeModel.Models {
		if model.ID != afterModel.ID {
			continue
		}
		for _, field := range model.Fields {
			if field.ID == afterField.ID {
				return "", "", fmt.Errorf("backfill field %q already existed in the parent model", selector)
			}
		}
	}
	return afterModel.ID, afterField.ID, nil
}

func artifactKind(filename string) codegenmanifest.ArtifactKind {
	if strings.HasSuffix(filename, ".snapshot.json") {
		return codegenmanifest.ArtifactMigrationSnapshot
	}
	return codegenmanifest.ArtifactMigrationSQL
}

func newPendingBackfill(moduleDir, root string, entry migration.ManifestEntry, model, beforeModel ir.ModelIR, modelFingerprint, contractFingerprint, providerFingerprint, systemFingerprint ir.Fingerprint) (PendingBackfill, error) {
	publicationPath := path.Join(root, PublicationFilename)
	publication, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(publicationPath)))
	if err != nil {
		return PendingBackfill{}, fmt.Errorf("required-column backfill requires an existing reviewed parent publication: %w", err)
	}
	value := PendingBackfill{FormatVersion: PendingBackfillFormatVersion, Root: root, PublicationPath: publicationPath, ParentPublicationSHA256: codegenmanifest.ContentHash(publication), MigrationID: entry.ID, Provider: ir.PostgreSQL, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, ProviderFingerprint: providerFingerprint, SystemFingerprint: systemFingerprint, BeforeModel: beforeModel, AfterModel: model, Entry: entry}
	if err := validatePendingBackfill(value); err != nil {
		return PendingBackfill{}, err
	}
	return value, nil
}
