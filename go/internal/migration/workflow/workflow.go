// Package workflow composes reviewed provider migration history in memory. It
// performs no publication or database work.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	DefaultRoot         = "migrations"
	PublicationFilename = ".golem-publication.json"
	GeneratorVersion    = "golem-p1-migration-v1"
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Provider struct {
	Result pipeline.ProviderResult
	Render func(migration.ManifestEntry) ([]byte, error)
}

type NewRequest struct {
	ModuleDir           string
	Root                string
	Name                string
	ModelFingerprint    ir.Fingerprint
	ContractFingerprint ir.Fingerprint
	Model               ir.ModelIR
	PreviousModel       *ir.ModelIR
	Providers           []Provider
	Approvals           []migration.OperationID
}

type NewResult struct {
	Prospective     codegenmanifest.Result
	PublicationPath string
	MigrationID     migration.MigrationID
	Providers       []ir.Provider
}

type History struct {
	Provider physical.ProviderManifest
	Manifest migration.Manifest
	Files    map[string][]byte
}

type State struct {
	Publication     *codegenmanifest.Manifest
	PublicationPath string
	Artifacts       []codegenmanifest.Artifact
	Histories       map[ir.Provider]History
	HeadModel       *ir.ModelIR
}

func PublishedProviders(moduleDir, root string) ([]ir.Provider, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(path.Join(canonical, PublicationFilename))))
	if err != nil {
		return nil, err
	}
	value, err := codegenmanifest.ParseHistorical(encoded)
	if err != nil {
		return nil, err
	}
	providers := make([]ir.Provider, len(value.ProviderFingerprints))
	for index, fingerprint := range value.ProviderFingerprints {
		providers[index] = fingerprint.Provider
	}
	return providers, nil
}

// ReadModelHead returns the exact reviewed logical head before compilation.
// It performs every history check independent of a provider renderer.
func ReadModelHead(moduleDir, root string) (*ir.ModelIR, error) {
	state, err := loadReviewedState(context.Background(), moduleDir, root)
	if err != nil || state.Publication == nil {
		return nil, err
	}
	return state.HeadModel, nil
}

func PrepareNew(ctx context.Context, request NewRequest) (NewResult, error) {
	if err := ctx.Err(); err != nil {
		return NewResult{}, err
	}
	if !slugPattern.MatchString(request.Name) {
		return NewResult{}, fmt.Errorf("migration name %q must match [a-z][a-z0-9_]{0,62}", request.Name)
	}
	root, err := canonicalRoot(request.Root)
	if err != nil {
		return NewResult{}, err
	}
	providers, err := canonicalProviders(request.Providers)
	if err != nil {
		return NewResult{}, err
	}
	state, err := Load(ctx, request.ModuleDir, root, providers)
	if err != nil {
		return NewResult{}, err
	}
	headLength := -1
	for _, provider := range providers {
		length := len(state.Histories[provider.Result.Provider.Provider].Manifest.Entries)
		if headLength < 0 {
			headLength = length
		} else if length != headLength {
			return NewResult{}, fmt.Errorf("declared provider migration heads are not in lockstep")
		}
	}
	if headLength < 0 || headLength >= 9999 {
		return NewResult{}, fmt.Errorf("migration history cannot allocate the next four-digit sequence")
	}
	id := migration.MigrationID(fmt.Sprintf("%04d_%s", headLength+1, request.Name))
	if fingerprint, fingerprintErr := ir.ModelFingerprint(request.Model); fingerprintErr != nil || fingerprint != request.ModelFingerprint {
		return NewResult{}, fmt.Errorf("current ModelIR does not match its migration fingerprint")
	}
	beforeModel := ir.CanonicalEmptyModel()
	if headLength != 0 {
		if state.HeadModel == nil || request.PreviousModel == nil {
			return NewResult{}, fmt.Errorf("incremental migration requires the exact previous reviewed ModelIR")
		}
		stateBytes, _ := ir.CanonicalModel(*state.HeadModel)
		requestBytes, previousErr := ir.CanonicalModel(*request.PreviousModel)
		if previousErr != nil || !bytes.Equal(stateBytes, requestBytes) {
			return NewResult{}, fmt.Errorf("incremental migration previous ModelIR differs from the reviewed head")
		}
		beforeModel = *request.PreviousModel
	} else if request.PreviousModel != nil {
		return NewResult{}, fmt.Errorf("initial migration must start from the canonical empty ModelIR")
	}
	beforeModelFingerprint, _ := ir.ModelFingerprint(beforeModel)
	beforeModelBytes, _ := modelSnapshotBytes(beforeModel)
	afterModelBytes, _ := modelSnapshotBytes(request.Model)
	approved := map[migration.OperationID]bool{}
	for _, operationID := range request.Approvals {
		if operationID == "" || approved[operationID] {
			return NewResult{}, fmt.Errorf("migration approval contains an empty or duplicate operation ID %q", operationID)
		}
		approved[operationID] = true
	}
	usedApprovals := map[migration.OperationID]bool{}
	physicalChanged := false

	artifactByPath := map[string]codegenmanifest.Artifact{}
	for _, artifact := range state.Artifacts {
		artifactByPath[artifact.Path] = artifact
	}
	var changedProviders []ir.Provider
	for _, provider := range providers {
		providerID := provider.Result.Provider.Provider
		history := state.Histories[providerID]
		before, parentID, parentChain := canonicalEmpty(provider.Result.Schema), migration.MigrationID(""), migration.Digest("")
		if len(history.Manifest.Entries) != 0 {
			head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
			before, parentID, parentChain = head.AfterSnapshot, head.ID, head.ChainHash
			if head.AfterModel != migration.Digest(beforeModelFingerprint) {
				return NewResult{}, fmt.Errorf("provider %s model head differs from the reviewed ModelIR", providerID)
			}
		}
		after, err := physical.Normalize(provider.Result.Schema)
		if err != nil {
			return NewResult{}, err
		}
		plan, err := migration.Diff(before, after)
		if err != nil {
			return NewResult{}, fmt.Errorf("plan provider %s: %w", providerID, err)
		}
		var approvals []migration.Approval
		var risks []migration.OperationRisk
		for _, operation := range plan.Operations {
			risks = append(risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
			if migration.RequiresApproval(operation) {
				if !approved[operation.ID] {
					return NewResult{}, fmt.Errorf("provider %s operation %s risk %s requires --approve %s", providerID, operation.ID, operation.Risk, operation.ID)
				}
				usedApprovals[operation.ID] = true
				approvals = append(approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
			}
		}
		if err := migration.ValidatePlan(plan, approvals); err != nil {
			return NewResult{}, fmt.Errorf("provider %s: %w", providerID, err)
		}
		beforeFingerprint, _ := physical.PhysicalFingerprint(before)
		afterFingerprint, _ := physical.PhysicalFingerprint(after)
		physicalChanged = physicalChanged || beforeFingerprint != afterFingerprint
		allowlist, _ := physical.UnmanagedAllowlistFingerprint(after)
		entry := migration.ManifestEntry{
			ID: id, ParentID: parentID, ParentChainHash: parentChain,
			Operations: plan.Operations, Phases: plan.Phases, Risks: risks, Approvals: approvals,
			BeforeModel: migration.Digest(beforeModelFingerprint), AfterModel: migration.Digest(request.ModelFingerprint),
			BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()),
			BeforeSnapshot: before, AfterSnapshot: after, UnmanagedAllowlistDigest: migration.Digest(allowlist.String()),
		}
		sql, err := provider.Render(entry)
		if err != nil {
			return NewResult{}, fmt.Errorf("render provider %s migration %s: %w", providerID, id, err)
		}
		prefix := path.Join(root, string(providerID), string(id))
		beforeBytes, err := snapshotBytes(before)
		if err != nil {
			return NewResult{}, err
		}
		afterBytes, err := snapshotBytes(after)
		if err != nil {
			return NewResult{}, err
		}
		newFiles := map[string][]byte{
			prefix + ".sql":                  sql,
			prefix + ".before.snapshot.json": beforeBytes,
			prefix + ".after.snapshot.json":  afterBytes,
		}
		paths := make([]string, 0, len(newFiles))
		for filename := range newFiles {
			paths = append(paths, filename)
		}
		sort.Strings(paths)
		for _, filename := range paths {
			entry.Files = append(entry.Files, migration.FileChecksum{Path: filename, SHA256: migration.Checksum(newFiles[filename])})
			kind := codegenmanifest.ArtifactMigrationSnapshot
			if strings.HasSuffix(filename, ".sql") {
				kind = codegenmanifest.ArtifactMigrationSQL
			}
			artifactByPath[filename] = codegenmanifest.Artifact{Path: filename, Kind: kind, Content: newFiles[filename], Immutable: true}
		}
		entry.ChainHash = migration.ChainHash(entry)
		history.Manifest.Entries = append(history.Manifest.Entries, entry)
		if len(history.Manifest.Entries) == 1 {
			history.Manifest = migration.Manifest{
				FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
				HashAlgorithm: "sha256", GeneratorVersion: GeneratorVersion, Provider: provider.Result.Provider,
				Entries: history.Manifest.Entries,
			}
		} else {
			// The manifest is the mutable head of an immutable entry chain. Record
			// an explicitly supported provider-runtime transition there while old
			// entry snapshots retain their provider identity byte-for-byte.
			history.Manifest.Provider = provider.Result.Provider
		}
		for filename, content := range newFiles {
			if history.Files == nil {
				history.Files = map[string][]byte{}
			}
			history.Files[filename] = content
		}
		manifestBytes, err := migration.EncodeManifest(history.Manifest, history.Files)
		if err != nil {
			return NewResult{}, fmt.Errorf("encode provider %s history: %w", providerID, err)
		}
		manifestPath := path.Join(root, string(providerID), "manifest.json")
		artifactByPath[manifestPath] = codegenmanifest.Artifact{Path: manifestPath, Kind: codegenmanifest.ArtifactMigrationManifest, Content: manifestBytes}
		changedProviders = append(changedProviders, providerID)
	}
	if beforeModelFingerprint == request.ModelFingerprint && !physicalChanged {
		return NewResult{}, fmt.Errorf("migration has no logical or physical schema changes; contract-only changes belong to generate")
	}
	modelPrefix := path.Join(root, "models", string(id))
	artifactByPath[modelPrefix+".before.snapshot.json"] = codegenmanifest.Artifact{Path: modelPrefix + ".before.snapshot.json", Kind: codegenmanifest.ArtifactMigrationSnapshot, Content: beforeModelBytes, Immutable: true}
	artifactByPath[modelPrefix+".after.snapshot.json"] = codegenmanifest.Artifact{Path: modelPrefix + ".after.snapshot.json", Kind: codegenmanifest.ArtifactMigrationSnapshot, Content: afterModelBytes, Immutable: true}
	for operationID := range approved {
		if !usedApprovals[operationID] {
			return NewResult{}, fmt.Errorf("approval %s does not name a current DataLoss or Manual operation", operationID)
		}
	}
	artifacts := make([]codegenmanifest.Artifact, 0, len(artifactByPath))
	for _, artifact := range artifactByPath {
		artifacts = append(artifacts, artifact)
	}
	fingerprints := make([]codegenmanifest.ProviderFingerprint, len(providers))
	for index, provider := range providers {
		fingerprints[index] = codegenmanifest.ProviderFingerprint{Provider: provider.Result.Provider.Provider, Fingerprint: provider.Result.Fingerprint, SystemFingerprint: provider.Result.SystemFingerprint}
	}
	prospective, err := codegenmanifest.Build(codegenmanifest.Request{
		ModelFingerprint: request.ModelFingerprint, ContractFingerprint: request.ContractFingerprint,
		ProviderFingerprints: fingerprints, GeneratorVersion: GeneratorVersion,
		TemplateABIVersion: codegenmanifest.TemplateABIVersion, Artifacts: artifacts,
	})
	if err != nil {
		return NewResult{}, err
	}
	return NewResult{Prospective: prospective, PublicationPath: path.Join(root, PublicationFilename), MigrationID: id, Providers: changedProviders}, nil
}

func Load(ctx context.Context, moduleDir, root string, providers []Provider) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	state, err := loadReviewedState(ctx, moduleDir, root)
	if err != nil {
		return State{}, err
	}
	if state.Publication == nil {
		state.Histories = emptyHistories(providers)
		return state, nil
	}
	expected := map[ir.Provider]Provider{}
	for _, provider := range providers {
		expected[provider.Result.Provider.Provider] = provider
	}
	for providerID, provider := range expected {
		history, exists := state.Histories[providerID]
		if !exists {
			return State{}, fmt.Errorf("migration publication is missing provider %s manifest", providerID)
		}
		if !physical.CompatibleProviderHistory(history.Provider, provider.Result.Provider) {
			return State{}, fmt.Errorf("migration provider %s manifest identity differs from the current provider", providerID)
		}
		for _, entry := range history.Manifest.Entries {
			if provider.Render != nil {
				rendered, renderErr := provider.Render(entry)
				if renderErr != nil || !bytes.Equal(rendered, history.Files[path.Join(path.Dir(state.PublicationPath), string(providerID), string(entry.ID))+".sql"]) {
					return State{}, fmt.Errorf("provider %s migration %s SQL differs from deterministic rendering", providerID, entry.ID)
				}
			}
		}
	}
	for providerID := range state.Histories {
		if _, ok := expected[providerID]; !ok {
			return State{}, fmt.Errorf("migration publication contains undeclared provider %s", providerID)
		}
	}
	return state, nil
}

func loadReviewedState(ctx context.Context, moduleDir, root string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return State{}, err
	}
	publicationPath := path.Join(canonical, PublicationFilename)
	encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(publicationPath)))
	if errors.Is(err, os.ErrNotExist) {
		return State{PublicationPath: publicationPath, Histories: map[ir.Provider]History{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	publication, err := codegenmanifest.ParseHistorical(encoded)
	if err != nil {
		return State{}, err
	}
	state := State{Publication: &publication, PublicationPath: publicationPath, Histories: map[ir.Provider]History{}}
	contents := map[string][]byte{}
	referenced := map[string]bool{}
	for _, entry := range publication.Artifacts {
		if !strings.HasPrefix(entry.Path, canonical+"/") || !migrationArtifactKind(entry.Kind) {
			return State{}, fmt.Errorf("migration publication contains out-of-scope artifact %q", entry.Path)
		}
		content, readErr := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(entry.Path)))
		if readErr != nil {
			return State{}, fmt.Errorf("read migration artifact %s: %w", entry.Path, readErr)
		}
		if codegenmanifest.ContentHash(content) != entry.ContentSHA256 {
			return State{}, fmt.Errorf("migration artifact %s was rewritten", entry.Path)
		}
		if entry.Kind != codegenmanifest.ArtifactMigrationManifest && !entry.Immutable {
			return State{}, fmt.Errorf("reviewed migration artifact %s is not immutable", entry.Path)
		}
		state.Artifacts = append(state.Artifacts, codegenmanifest.Artifact{Path: entry.Path, Kind: entry.Kind, Content: content, GeneratedHeader: entry.GeneratedHeader, Immutable: entry.Immutable})
		contents[entry.Path] = content
	}
	for _, artifact := range state.Artifacts {
		if artifact.Kind != codegenmanifest.ArtifactMigrationManifest {
			continue
		}
		value, parseErr := migration.ParseManifest(artifact.Content)
		if parseErr != nil {
			return State{}, parseErr
		}
		providerID := value.Provider.Provider
		if _, duplicate := state.Histories[providerID]; duplicate {
			return State{}, fmt.Errorf("migration publication contains duplicate provider manifest %s", providerID)
		}
		expectedPath := path.Join(canonical, string(providerID), "manifest.json")
		if artifact.Path != expectedPath {
			return State{}, fmt.Errorf("provider %s migration manifest is not at canonical path %s", providerID, expectedPath)
		}
		files := map[string][]byte{}
		for _, entry := range value.Entries {
			for _, file := range entry.Files {
				content, ok := contents[file.Path]
				if !ok {
					return State{}, fmt.Errorf("migration %s file %s is absent from publication", entry.ID, file.Path)
				}
				files[file.Path] = content
				referenced[file.Path] = true
			}
			if err := verifyEntryArtifacts(canonical, providerID, entry, files); err != nil {
				return State{}, err
			}
		}
		if err := migration.VerifyManifest(value, files); err != nil {
			return State{}, fmt.Errorf("verify provider %s migration history: %w", providerID, err)
		}
		referenced[artifact.Path] = true
		state.Histories[providerID] = History{Provider: value.Provider, Manifest: value, Files: files}
	}
	if len(state.Histories) == 0 {
		return State{}, fmt.Errorf("migration publication contains no provider manifests")
	}
	if err := loadModelHistory(canonical, contents, state.Histories, &state); err != nil {
		return State{}, err
	}
	if state.HeadModel == nil {
		return State{}, fmt.Errorf("migration publication has no reviewed ModelIR head")
	}
	var entries []migration.ManifestEntry
	for _, history := range state.Histories {
		entries = history.Manifest.Entries
		break
	}
	for _, entry := range entries {
		prefix := path.Join(canonical, "models", string(entry.ID))
		referenced[prefix+".before.snapshot.json"] = true
		referenced[prefix+".after.snapshot.json"] = true
	}
	for _, artifact := range state.Artifacts {
		if !referenced[artifact.Path] {
			return State{}, fmt.Errorf("migration publication contains unreferenced artifact %q", artifact.Path)
		}
	}
	headModelFingerprint, _ := ir.ModelFingerprint(*state.HeadModel)
	if publication.ModelFingerprint != headModelFingerprint {
		return State{}, fmt.Errorf("migration publication ModelFingerprint differs from the immutable ModelIR head")
	}
	publicationProviders := map[ir.Provider]codegenmanifest.ProviderFingerprint{}
	for _, fingerprint := range publication.ProviderFingerprints {
		publicationProviders[fingerprint.Provider] = fingerprint
	}
	for providerID, history := range state.Histories {
		head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
		published, exists := publicationProviders[providerID]
		if !exists || published.Fingerprint != ir.Fingerprint(head.AfterPhysical) {
			return State{}, fmt.Errorf("migration publication provider %s fingerprint differs from the immutable physical head", providerID)
		}
		headSystem, systemErr := physical.SystemFingerprint(head.AfterSnapshot.Provider, head.AfterSnapshot.System)
		if systemErr != nil || published.SystemFingerprint != ir.Fingerprint(headSystem.String()) {
			return State{}, fmt.Errorf("migration publication provider %s system fingerprint differs from the immutable system head", providerID)
		}
		delete(publicationProviders, providerID)
	}
	if len(publicationProviders) != 0 {
		return State{}, fmt.Errorf("migration publication contains unbound provider fingerprints")
	}
	return state, nil
}

func canonicalProviders(values []Provider) ([]Provider, error) {
	result := append([]Provider(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].Result.Provider.Provider < result[j].Result.Provider.Provider })
	for index, provider := range result {
		if provider.Result.Provider.Provider == "" || provider.Render == nil {
			return nil, fmt.Errorf("migration provider registry contains an incomplete provider")
		}
		physicalFingerprint, physicalErr := physical.PhysicalFingerprint(provider.Result.Schema)
		if physicalErr != nil || provider.Result.Fingerprint != ir.Fingerprint(physicalFingerprint.String()) {
			return nil, fmt.Errorf("migration provider %s has a stale physical fingerprint", provider.Result.Provider.Provider)
		}
		systemFingerprint, systemErr := physical.SystemFingerprint(provider.Result.Schema.Provider, provider.Result.Schema.System)
		if systemErr != nil || provider.Result.SystemFingerprint != ir.Fingerprint(systemFingerprint.String()) {
			return nil, fmt.Errorf("migration provider %s has a stale system fingerprint", provider.Result.Provider.Provider)
		}
		if index > 0 && result[index-1].Result.Provider.Provider == provider.Result.Provider.Provider {
			return nil, fmt.Errorf("migration provider registry contains duplicate provider %s", provider.Result.Provider.Provider)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("migration workflow requires at least one declared provider")
	}
	return result, nil
}

func emptyHistories(providers []Provider) map[ir.Provider]History {
	result := map[ir.Provider]History{}
	for _, provider := range providers {
		result[provider.Result.Provider.Provider] = History{Provider: provider.Result.Provider, Manifest: migration.Manifest{
			FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
			HashAlgorithm: "sha256", GeneratorVersion: GeneratorVersion, Provider: provider.Result.Provider,
		}, Files: map[string][]byte{}}
	}
	return result
}

func canonicalEmpty(desired physical.PhysicalSchema) physical.PhysicalSchema {
	value, _ := physical.Normalize(physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: desired.Provider, Namespace: desired.Namespace,
	})
	return value
}

func snapshotBytes(value physical.PhysicalSchema) ([]byte, error) {
	normalized, err := physical.Normalize(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func modelSnapshotBytes(value ir.ModelIR) ([]byte, error) {
	canonical, err := ir.CanonicalModel(value)
	if err != nil {
		return nil, err
	}
	var normalized ir.ModelIR
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func loadModelHistory(root string, contents map[string][]byte, histories map[ir.Provider]History, state *State) error {
	var reference []migration.ManifestEntry
	for _, history := range histories {
		if reference == nil {
			reference = history.Manifest.Entries
			continue
		}
		if len(reference) != len(history.Manifest.Entries) {
			return fmt.Errorf("declared provider migration heads are not in lockstep")
		}
		for index := range reference {
			other := history.Manifest.Entries[index]
			if reference[index].ID != other.ID || reference[index].BeforeModel != other.BeforeModel || reference[index].AfterModel != other.AfterModel {
				return fmt.Errorf("declared provider model histories are not in lockstep")
			}
		}
	}
	previous := ir.CanonicalEmptyModel()
	previousFingerprint := ir.EmptyModelFingerprint()
	for _, entry := range reference {
		prefix := path.Join(root, "models", string(entry.ID))
		before, err := parseModelSnapshot(contents[prefix+".before.snapshot.json"])
		if err != nil {
			return fmt.Errorf("migration %s before ModelIR snapshot: %w", entry.ID, err)
		}
		after, err := parseModelSnapshot(contents[prefix+".after.snapshot.json"])
		if err != nil {
			return fmt.Errorf("migration %s after ModelIR snapshot: %w", entry.ID, err)
		}
		beforeBytes, _ := ir.CanonicalModel(before)
		previousBytes, _ := ir.CanonicalModel(previous)
		beforeFingerprint, _ := ir.ModelFingerprint(before)
		afterFingerprint, _ := ir.ModelFingerprint(after)
		if !bytes.Equal(beforeBytes, previousBytes) || beforeFingerprint != previousFingerprint || migration.Digest(beforeFingerprint) != entry.BeforeModel || migration.Digest(afterFingerprint) != entry.AfterModel {
			return fmt.Errorf("migration %s ModelIR snapshot chain is stale or discontinuous", entry.ID)
		}
		previous, previousFingerprint = after, afterFingerprint
	}
	if len(reference) != 0 {
		copy := previous
		state.HeadModel = &copy
	}
	return nil
}

func parseModelSnapshot(encoded []byte) (ir.ModelIR, error) {
	if len(encoded) == 0 {
		return ir.ModelIR{}, fmt.Errorf("snapshot is missing")
	}
	var value ir.ModelIR
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return ir.ModelIR{}, err
	}
	expected, err := modelSnapshotBytes(value)
	if err != nil || !bytes.Equal(expected, encoded) {
		return ir.ModelIR{}, fmt.Errorf("snapshot is not canonical normalized JSON")
	}
	return value, nil
}

func verifyEntryArtifacts(root string, provider ir.Provider, entry migration.ManifestEntry, files map[string][]byte) error {
	prefix := path.Join(root, string(provider), string(entry.ID))
	expected := []string{prefix + ".after.snapshot.json", prefix + ".before.snapshot.json", prefix + ".sql"}
	actual := make([]string, len(entry.Files))
	for index, file := range entry.Files {
		actual[index] = file.Path
	}
	sort.Strings(actual)
	if !equalStrings(actual, expected) {
		return fmt.Errorf("provider %s migration %s does not contain the exact SQL/snapshot artifact set", provider, entry.ID)
	}
	before, err := snapshotBytes(entry.BeforeSnapshot)
	if err != nil || !bytes.Equal(before, files[prefix+".before.snapshot.json"]) {
		return fmt.Errorf("provider %s migration %s before snapshot artifact differs from embedded snapshot", provider, entry.ID)
	}
	after, err := snapshotBytes(entry.AfterSnapshot)
	if err != nil || !bytes.Equal(after, files[prefix+".after.snapshot.json"]) {
		return fmt.Errorf("provider %s migration %s after snapshot artifact differs from embedded snapshot", provider, entry.ID)
	}
	return nil
}

func canonicalRoot(value string) (string, error) {
	if value == "" {
		value = DefaultRoot
	}
	if filepath.IsAbs(value) || strings.Contains(value, `\`) {
		return "", fmt.Errorf("migration root %q must be canonical and module-relative", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", fmt.Errorf("migration root %q must be canonical and module-relative", value)
	}
	return cleaned, nil
}

func migrationArtifactKind(kind codegenmanifest.ArtifactKind) bool {
	return kind == codegenmanifest.ArtifactMigrationManifest || kind == codegenmanifest.ArtifactMigrationSQL || kind == codegenmanifest.ArtifactMigrationSnapshot
}

func equalProvider(left, right physical.ProviderManifest) bool {
	return reflect.DeepEqual(left, right)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
