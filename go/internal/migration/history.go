package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	ManifestFormatVersion    uint16 = 1
	ManifestCanonicalVersion uint16 = 1
)

func Checksum(content []byte) Digest {
	sum := sha256.Sum256(content)
	return Digest(hex.EncodeToString(sum[:]))
}

// EncodeManifest emits the stable reviewed JSON representation after proving
// that the complete immutable chain and supplied file bytes agree.
func EncodeManifest(value Manifest, files map[string][]byte) ([]byte, error) {
	if err := VerifyManifest(value, files); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode migration manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

// ParseManifest strictly decodes reviewed migration history. File and chain
// verification remains explicit because callers must supply the reviewed bytes.
func ParseManifest(encoded []byte) (Manifest, error) {
	var value Manifest
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("decode migration manifest: trailing content")
	}
	return value, nil
}

// ParseDigest accepts only the canonical lowercase hexadecimal SHA-256 form.
func ParseDigest(value string) (Digest, error) {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return "", fmt.Errorf("digest must be 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("digest must be 64 lowercase hexadecimal characters")
	}
	return Digest(value), nil
}

func validateDigest(label string, value Digest, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if _, err := ParseDigest(string(value)); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func ChainHash(entry ManifestEntry) Digest {
	encoded, err := canonicalEntry(entry)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return Digest(hex.EncodeToString(sum[:]))
}

// VerifyManifest detects rewritten files, reordered/broken history, and stale
// embedded snapshots. files contains reviewed bytes keyed by manifest path.
func VerifyManifest(manifest Manifest, files map[string][]byte) error {
	if manifest.FormatVersion != ManifestFormatVersion {
		return fmt.Errorf("unsupported migration manifest format %d", manifest.FormatVersion)
	}
	if manifest.CanonicalVersion != ManifestCanonicalVersion {
		return fmt.Errorf("unsupported migration canonical version %d", manifest.CanonicalVersion)
	}
	if manifest.HashAlgorithm != "sha256" {
		return fmt.Errorf("unsupported migration hash algorithm %q", manifest.HashAlgorithm)
	}
	if manifest.GeneratorVersion == "" {
		return fmt.Errorf("migration manifest has empty generator version")
	}
	var parentID MigrationID
	var parentHash Digest
	seenIDs := map[MigrationID]bool{}
	seenPaths := map[string]bool{}
	for index, entry := range manifest.Entries {
		if entry.ID == "" || seenIDs[entry.ID] {
			return fmt.Errorf("migration entry %d has empty or duplicate ID %q", index, entry.ID)
		}
		seenIDs[entry.ID] = true
		if entry.ParentID != parentID || entry.ParentChainHash != parentHash {
			return fmt.Errorf("migration %s has stale or rewritten parent chain", entry.ID)
		}
		if entry.BeforeModel == "" || entry.AfterModel == "" {
			return fmt.Errorf("migration %s has empty model fingerprint", entry.ID)
		}
		digests := []struct {
			label      string
			value      Digest
			allowEmpty bool
		}{
			{"parent chain hash", entry.ParentChainHash, index == 0},
			{"chain hash", entry.ChainHash, false},
			{"before model fingerprint", entry.BeforeModel, false},
			{"after model fingerprint", entry.AfterModel, false},
			{"before physical fingerprint", entry.BeforePhysical, false},
			{"after physical fingerprint", entry.AfterPhysical, false},
			{"unmanaged allowlist digest", entry.UnmanagedAllowlistDigest, false},
		}
		for _, digest := range digests {
			if err := validateDigest(digest.label, digest.value, digest.allowEmpty); err != nil {
				return fmt.Errorf("migration %s: %w", entry.ID, err)
			}
		}
		for _, file := range entry.Files {
			if err := validateDigest("file checksum", file.SHA256, false); err != nil {
				return fmt.Errorf("migration %s file %s: %w", entry.ID, file.Path, err)
			}
			if err := validateManifestPath(file.Path); err != nil {
				return fmt.Errorf("migration %s: %w", entry.ID, err)
			}
			if seenPaths[file.Path] {
				return fmt.Errorf("migration %s duplicates file path %s", entry.ID, file.Path)
			}
			seenPaths[file.Path] = true
			content, ok := files[file.Path]
			if !ok {
				return fmt.Errorf("migration %s file %s is missing", entry.ID, file.Path)
			}
			if Checksum(content) != file.SHA256 {
				return fmt.Errorf("migration %s file %s was rewritten", entry.ID, file.Path)
			}
		}
		beforeSnapshot, beforeNormalizeErr := physical.Normalize(entry.BeforeSnapshot)
		afterSnapshot, afterNormalizeErr := physical.Normalize(entry.AfterSnapshot)
		if beforeNormalizeErr != nil || afterNormalizeErr != nil {
			return fmt.Errorf("migration %s embeds invalid physical snapshot", entry.ID)
		}
		if !reflect.DeepEqual(beforeSnapshot.Provider, manifest.Provider) || !reflect.DeepEqual(afterSnapshot.Provider, manifest.Provider) {
			return fmt.Errorf("migration %s snapshot provider differs from manifest provider", entry.ID)
		}
		before, beforeErr := physical.PhysicalFingerprint(beforeSnapshot)
		after, afterErr := physical.PhysicalFingerprint(afterSnapshot)
		if beforeErr != nil || afterErr != nil || Digest(before.String()) != entry.BeforePhysical || Digest(after.String()) != entry.AfterPhysical {
			return fmt.Errorf("migration %s embeds stale physical snapshot", entry.ID)
		}
		allowlist, allowlistErr := physical.UnmanagedAllowlistFingerprint(afterSnapshot)
		if allowlistErr != nil || Digest(allowlist.String()) != entry.UnmanagedAllowlistDigest {
			return fmt.Errorf("migration %s unmanaged allowlist digest differs from embedded after snapshot", entry.ID)
		}
		beforeSystem, beforeSystemErr := physical.SystemFingerprint(beforeSnapshot.Provider, beforeSnapshot.System)
		afterSystem, afterSystemErr := physical.SystemFingerprint(afterSnapshot.Provider, afterSnapshot.System)
		if beforeSystemErr != nil || afterSystemErr != nil {
			return fmt.Errorf("migration %s embeds invalid system schema", entry.ID)
		}
		bootstrapCount := 0
		var bootstrap Operation
		for _, operation := range entry.Operations {
			if operation.Kind == BootstrapSystemSchema {
				bootstrapCount++
				bootstrap = operation
			}
		}
		if beforeSystem != afterSystem {
			beforeFragment, beforeFragmentErr := fragment(beforeSnapshot.System)
			afterFragment, afterFragmentErr := fragment(afterSnapshot.System)
			expectedPlan, expectedPlanErr := Diff(beforeSnapshot, afterSnapshot)
			var expectedBootstrap Operation
			if expectedPlanErr == nil {
				for _, operation := range expectedPlan.Operations {
					if operation.Kind == BootstrapSystemSchema {
						expectedBootstrap = operation
					}
				}
			}
			if index != 0 || !emptySystemSchema(beforeSnapshot.System) || len(beforeSnapshot.Tables) != 0 || len(beforeSnapshot.Extensions) != 0 || len(beforeSnapshot.Unmanaged) != 0 || len(afterSnapshot.System.Objects) == 0 || bootstrapCount != 1 || beforeFragmentErr != nil || afterFragmentErr != nil || bootstrap.ObjectID != "system-schema" || bootstrap.Before != beforeFragment || bootstrap.After != afterFragment || expectedPlanErr != nil || !reflect.DeepEqual(bootstrap, expectedBootstrap) {
				return fmt.Errorf("migration %s changes the P1 system schema without the exact first-entry bootstrap operation", entry.ID)
			}
		} else if bootstrapCount != 0 {
			return fmt.Errorf("migration %s contains a system bootstrap without a system transition", entry.ID)
		}
		if index > 0 {
			previous := manifest.Entries[index-1]
			beforeAllowlist, beforeAllowlistErr := physical.UnmanagedAllowlistFingerprint(beforeSnapshot)
			previousSystem, previousSystemErr := physical.SystemFingerprint(previous.AfterSnapshot.Provider, previous.AfterSnapshot.System)
			if beforeAllowlistErr != nil || Digest(beforeAllowlist.String()) != previous.UnmanagedAllowlistDigest {
				return fmt.Errorf("migration %s unmanaged allowlist history is discontinuous", entry.ID)
			}
			if previousSystemErr != nil || previousSystem != beforeSystem {
				return fmt.Errorf("migration %s system schema history is discontinuous", entry.ID)
			}
		}
		for _, companion := range entry.Manual {
			if err := validateDigest("manual file checksum", companion.File.SHA256, false); err != nil {
				return fmt.Errorf("migration %s manual companion: %w", entry.ID, err)
			}
			if err := validateDigest("manual postcondition", companion.Postcondition, false); err != nil {
				return fmt.Errorf("migration %s manual companion: %w", entry.ID, err)
			}
			if err := validateManifestPath(companion.File.Path); err != nil {
				return fmt.Errorf("migration %s: %w", entry.ID, err)
			}
			if seenPaths[companion.File.Path] {
				return fmt.Errorf("migration %s duplicates manual companion path %s", entry.ID, companion.File.Path)
			}
			seenPaths[companion.File.Path] = true
			content, ok := files[companion.File.Path]
			if !ok || Checksum(content) != companion.File.SHA256 || companion.Postcondition == "" {
				return fmt.Errorf("migration %s manual companion is missing, rewritten, or lacks postcondition", entry.ID)
			}
		}
		if err := validateEntrySemantics(entry); err != nil {
			return fmt.Errorf("migration %s: %w", entry.ID, err)
		}
		expected := ChainHash(entry)
		if expected != entry.ChainHash {
			return fmt.Errorf("migration %s chain hash mismatch", entry.ID)
		}
		if index > 0 && manifest.Entries[index-1].AfterPhysical != entry.BeforePhysical {
			return fmt.Errorf("migration %s physical history is discontinuous", entry.ID)
		}
		if index > 0 && manifest.Entries[index-1].AfterModel != entry.BeforeModel {
			return fmt.Errorf("migration %s model history is discontinuous", entry.ID)
		}
		parentID = entry.ID
		parentHash = entry.ChainHash
	}
	return nil
}

func validateManifestPath(value string) error {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("unsafe or noncanonical migration file path %q", value)
	}
	return nil
}

func validateEntrySemantics(entry ManifestEntry) error {
	ordered, err := Order(Plan{Operations: entry.Operations})
	if err != nil {
		return fmt.Errorf("invalid operation inventory: %w", err)
	}
	operations := make(map[OperationID]Operation, len(ordered))
	for _, operation := range ordered {
		if err := validateOperationDigests(operation); err != nil {
			return err
		}
		if !validRisk(operation.Risk) {
			return fmt.Errorf("operation %s has invalid risk", operation.ID)
		}
		if operation.Mode != Transactional && operation.Mode != AutocommitOnly {
			return fmt.Errorf("operation %s has invalid transaction mode", operation.ID)
		}
		if operation.Mode == AutocommitOnly && operation.Resume == nil {
			return fmt.Errorf("autocommit operation %s lacks resume metadata", operation.ID)
		}
		operations[operation.ID] = operation
	}
	var phased []OperationID
	expectedBefore := entry.BeforePhysical
	for index, phase := range entry.Phases {
		if err := validateDigest(fmt.Sprintf("phase %d before fingerprint", index), phase.BeforeFingerprint, false); err != nil {
			return err
		}
		if err := validateDigest(fmt.Sprintf("phase %d after fingerprint", index), phase.AfterFingerprint, false); err != nil {
			return err
		}
		if phase.Ordinal != uint32(index) || len(phase.Operations) == 0 {
			return fmt.Errorf("phase %d has noncanonical ordinal or is empty", index)
		}
		if phase.Mode != Transactional && phase.Mode != AutocommitOnly {
			return fmt.Errorf("phase %d has invalid transaction mode", index)
		}
		if phase.Mode == AutocommitOnly && len(phase.Operations) != 1 {
			return fmt.Errorf("autocommit phase %d must contain exactly one operation", index)
		}
		if index > 0 && phase.Mode == Transactional && entry.Phases[index-1].Mode == Transactional {
			return fmt.Errorf("adjacent transactional phases %d and %d are noncanonical", index-1, index)
		}
		if phase.BeforeFingerprint != expectedBefore || phase.AfterFingerprint == "" {
			return fmt.Errorf("phase %d fingerprint chain is discontinuous", index)
		}
		for _, operationID := range phase.Operations {
			operation, exists := operations[operationID]
			if !exists {
				return fmt.Errorf("phase %d references unknown operation %s", index, operationID)
			}
			for _, previous := range phased {
				if previous == operationID {
					return fmt.Errorf("operation %s occurs in multiple phases", operationID)
				}
			}
			if operation.Mode != phase.Mode {
				return fmt.Errorf("operation %s mode differs from phase %d", operationID, index)
			}
			phased = append(phased, operationID)
		}
		expectedBefore = phase.AfterFingerprint
	}
	if len(entry.Phases) > 0 && expectedBefore != entry.AfterPhysical {
		return fmt.Errorf("phase chain does not reach the entry after fingerprint")
	}
	if len(phased) != len(ordered) {
		return fmt.Errorf("phases do not exactly cover the operation inventory")
	}
	if len(ordered) == 0 && entry.BeforePhysical != entry.AfterPhysical {
		return fmt.Errorf("physical change has no operation inventory")
	}
	for index, operation := range ordered {
		if phased[index] != operation.ID {
			return fmt.Errorf("phased operation order is not the deterministic DAG order")
		}
	}
	if len(entry.Risks) != len(operations) {
		return fmt.Errorf("risk inventory does not exactly cover phased operations")
	}
	classified := map[OperationID]bool{}
	for _, classification := range entry.Risks {
		operation, exists := operations[classification.OperationID]
		if !exists || classified[classification.OperationID] {
			return fmt.Errorf("risk inventory references an unknown or duplicate operation %s", classification.OperationID)
		}
		if classification.Risk != operation.Risk {
			return fmt.Errorf("operation %s risk inventory differs from immutable operation", classification.OperationID)
		}
		classified[classification.OperationID] = true
	}
	approvals := map[OperationID]bool{}
	for _, approval := range entry.Approvals {
		if err := validateDigest(fmt.Sprintf("approval %s before fragment", approval.OperationID), approval.Before, true); err != nil {
			return err
		}
		if err := validateDigest(fmt.Sprintf("approval %s after fragment", approval.OperationID), approval.After, true); err != nil {
			return err
		}
		operation, exists := operations[approval.OperationID]
		if !exists || approvals[approval.OperationID] || (operation.Risk != RiskDataLoss && operation.Risk != RiskManual) || approval.Risk != operation.Risk || approval.Before != operation.Before || approval.After != operation.After {
			return fmt.Errorf("approval for %s is not exact and object-scoped", approval.OperationID)
		}
		approvals[approval.OperationID] = true
	}
	for operationID, operation := range operations {
		required := operation.Risk == RiskDataLoss || operation.Risk == RiskManual
		if approvals[operationID] != required {
			return fmt.Errorf("operation %s approval inventory is inconsistent with risk %s", operationID, operation.Risk)
		}
	}
	manual := map[OperationID]bool{}
	for _, companion := range entry.Manual {
		operation, exists := operations[companion.OperationID]
		if !exists || operation.Kind != ManualStep || manual[companion.OperationID] {
			return fmt.Errorf("manual companion for %s does not map exactly to one manual operation", companion.OperationID)
		}
		manual[companion.OperationID] = true
	}
	for operationID, operation := range operations {
		if (operation.Kind == ManualStep) != manual[operationID] {
			return fmt.Errorf("manual operation %s companion inventory is inconsistent", operationID)
		}
	}
	return nil
}

func validRisk(risk Risk) bool {
	switch risk {
	case RiskSafe, RiskLocking, RiskRewrite, RiskDataLoss, RiskManual:
		return true
	default:
		return false
	}
}

func VerifyLedger(manifest Manifest, ledger []LedgerEntry) error {
	if len(ledger) != len(manifest.Entries) {
		return fmt.Errorf("ledger does not contain the exact reviewed migration chain")
	}
	for index, record := range ledger {
		digests := []struct {
			label      string
			value      Digest
			allowEmpty bool
		}{
			{"parent chain hash", record.ParentChainHash, index == 0},
			{"chain hash", record.ChainHash, false},
			{"before physical fingerprint", record.BeforePhysical, false},
			{"after physical fingerprint", record.AfterPhysical, false},
		}
		for _, digest := range digests {
			if err := validateDigest(digest.label, digest.value, digest.allowEmpty); err != nil {
				return fmt.Errorf("ledger migration %s: %w", record.MigrationID, err)
			}
		}
		for _, file := range record.Files {
			if err := validateDigest("file checksum", file.SHA256, false); err != nil {
				return fmt.Errorf("ledger migration %s file %s: %w", record.MigrationID, file.Path, err)
			}
		}
		expected := manifest.Entries[index]
		if record.MigrationID != expected.ID || record.ParentChainHash != expected.ParentChainHash || record.ChainHash != expected.ChainHash {
			return fmt.Errorf("ledger migration %d does not match reviewed chain", index)
		}
		if record.BeforePhysical != expected.BeforePhysical || record.AfterPhysical != expected.AfterPhysical {
			return fmt.Errorf("ledger migration %s has stale physical fingerprints", record.MigrationID)
		}
		if !equalFiles(record.Files, expected.Files) {
			return fmt.Errorf("ledger migration %s has rewritten file checksums", record.MigrationID)
		}
		if len(record.Phases) != len(expected.Phases) {
			return fmt.Errorf("ledger migration %s phase inventory differs from reviewed manifest", record.MigrationID)
		}
		for phaseIndex, phase := range record.Phases {
			if err := validateDigest("ledger phase after fingerprint", phase.AfterFingerprint, false); err != nil {
				return fmt.Errorf("ledger migration %s phase %d: %w", record.MigrationID, phase.Ordinal, err)
			}
			expectedPhase := expected.Phases[phaseIndex]
			if phase.Ordinal != expectedPhase.Ordinal || phase.AfterFingerprint != expectedPhase.AfterFingerprint {
				return fmt.Errorf("ledger migration %s phase %d differs from reviewed manifest", record.MigrationID, phase.Ordinal)
			}
			if phase.Status != PhaseApplied {
				return fmt.Errorf("ledger migration %s has incomplete or failed phase %d", record.MigrationID, phase.Ordinal)
			}
		}
	}
	return nil
}

func equalFiles(left, right []FileChecksum) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]FileChecksum(nil), left...)
	b := append([]FileChecksum(nil), right...)
	sort.Slice(a, func(i, j int) bool { return a[i].Path < a[j].Path })
	sort.Slice(b, func(i, j int) bool { return b[i].Path < b[j].Path })
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
