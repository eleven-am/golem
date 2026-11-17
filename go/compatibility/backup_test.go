package compatibility

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type releasedBackupDocument struct {
	FormatVersion    uint32 `json:"formatVersion"`
	CanonicalVersion uint32 `json:"canonicalVersion"`
	Fingerprint      string `json:"fingerprint"`
	Bytes            []byte `json:"bytes"`
}

type releasedBackupProvider struct {
	Provider          string                 `json:"provider"`
	SystemFingerprint string                 `json:"systemFingerprint"`
	Schema            releasedBackupDocument `json:"schema"`
	Manifest          []byte                 `json:"manifest,omitempty"`
}

type releasedBackupWire struct {
	FormatVersion      uint16                      `json:"formatVersion"`
	GenerationDigest   string                      `json:"generationDigest"`
	GeneratorVersion   string                      `json:"generatorVersion"`
	TemplateABIVersion string                      `json:"templateABIVersion"`
	Model              releasedBackupDocument      `json:"model"`
	Contract           releasedBackupDocument      `json:"contract"`
	Providers          []releasedBackupProvider    `json:"providers"`
	MigrationFiles     map[string][]byte           `json:"migrationFiles,omitempty"`
	Evidence           map[string][]map[string]any `json:"evidence,omitempty"`
}

func compatibilityDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || strings.ToLower(value) != value {
		t.Fatal("released metadata contains a noncanonical digest")
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	return digest
}

func releasedBackupDocumentOf(t *testing.T, value releasedBackupDocument) golem.SchemaDocument {
	t.Helper()
	return golem.GeneratedSchemaDocument(value.FormatVersion, value.CanonicalVersion, compatibilityDigest(t, value.Fingerprint), value.Bytes)
}

func readReleasedBackup(t *testing.T, directory string) (releasedBackupWire, golem.SchemaBundle) {
	t.Helper()
	file, err := os.Open(filepath.Join(directory, "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(payload) > 2<<20 {
		t.Fatal("released backup metadata is unreadable or exceeds 2 MiB")
	}
	var wire releasedBackupWire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || wire.FormatVersion != 1 {
		t.Fatal("released backup metadata has trailing data or unsupported framing")
	}
	generation := compatibilityDigest(t, wire.GenerationDigest)
	var providers []golem.ProviderSchemaDocument
	for _, value := range wire.Providers {
		provider := golem.Provider(value.Provider)
		document := golem.GeneratedProviderSchemaDocument(provider, compatibilityDigest(t, value.SystemFingerprint), releasedBackupDocumentOf(t, value.Schema))
		if len(value.Manifest) != 0 {
			document = golem.GeneratedProviderSchemaDocumentWithMigration(provider, document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(generation, provider, value.Manifest))
		}
		providers = append(providers, document)
	}
	return wire, golem.GeneratedSchemaBundle(generation, wire.GeneratorVersion, wire.TemplateABIVersion, releasedBackupDocumentOf(t, wire.Model), releasedBackupDocumentOf(t, wire.Contract), providers...)
}

func TestReleasedV088BackupRestore(t *testing.T) {
	directory := preparePriorRelease(t)
	runPriorRelease(t, directory, "TestReleasedBackup")
	wire, bundle := readReleasedBackup(t, directory)
	if wire.Contract.FormatVersion != 7 {
		t.Fatal("fixture did not come from the released contract format")
	}
	if _, err := schema.NewHistorical(bundle); err != nil {
		t.Fatalf("current historical reader rejected actual released backup documents: %v", err)
	}
	if _, err := schema.New(bundle); err == nil {
		t.Fatal("active application admitted a historical generated contract")
	}
	exerciseReleasedRestore(t, directory, wire, bundle)
}
