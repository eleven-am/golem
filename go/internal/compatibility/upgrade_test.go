package compatibility

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/migration/workflow"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

const p7CompatibilityCorpusSHA256 = "577d6d144c8eb3211124f519e5323242c092cb97e3dca6aa4f85b400f2c7fd66"
const p7EventCompatibilityCorpusSHA256 = "21c9018fb5bce7a46bb40bbcc4e042805dcb378625c13a0d320c038b2dc47d72"

type p7Provenance struct {
	FormatVersion            uint16 `json:"formatVersion"`
	SourceCommit             string `json:"sourceCommit"`
	SourcePackage            string `json:"sourcePackage"`
	GeneratorCommand         string `json:"generatorCommand"`
	MigrationCommand         string `json:"migrationCommand"`
	GenerationDigest         string `json:"generationDigest"`
	EventSHA256              string `json:"eventSHA256"`
	GeneratedManifestSHA256  string `json:"generatedManifestSHA256"`
	SQLiteManifestSHA256     string `json:"sqliteManifestSHA256"`
	PostgreSQLManifestSHA256 string `json:"postgresqlManifestSHA256"`
}

type p7EventProvenance struct {
	FormatVersion                     uint16   `json:"formatVersion"`
	SourceCommit                      string   `json:"sourceCommit"`
	GeneratorVersion                  string   `json:"generatorVersion"`
	TemplateABIVersion                string   `json:"templateABIVersion"`
	GenerationDigest                  string   `json:"generationDigest"`
	SourceSHA256                      string   `json:"sourceSHA256"`
	GeneratedManifestSHA256           string   `json:"generatedManifestSHA256"`
	SQLiteMigrationManifestSHA256     string   `json:"sqliteMigrationManifestSHA256"`
	PostgreSQLMigrationManifestSHA256 string   `json:"postgresqlMigrationManifestSHA256"`
	EventSHA256                       string   `json:"eventSHA256"`
	Commands                          []string `json:"commands"`
	FactProducer                      string   `json:"factProducer"`
	PostID                            string   `json:"postID"`
	OwnerID                           string   `json:"ownerID"`
}

func TestP8FrozenCompatibilityCorpusLoads(t *testing.T) {
	root := filepath.Join(compatibilityModuleRoot(t), "internal", "compatibility", "testdata", "p7")
	provenanceBytes := mustRead(t, filepath.Join(root, "PROVENANCE.json"))
	decoder := json.NewDecoder(bytes.NewReader(provenanceBytes))
	decoder.DisallowUnknownFields()
	var provenance p7Provenance
	if err := decoder.Decode(&provenance); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || provenance.FormatVersion != 1 || provenance.SourceCommit != "aafa3e965abe53deed887181b675d667574ea954" {
		t.Fatal("P7 compatibility provenance is not exact")
	}
	if digestFile(t, filepath.Join(root, "generated", ".golem", "generated-manifest.json")) != provenance.GeneratedManifestSHA256 ||
		digestFile(t, filepath.Join(root, "event.json")) != provenance.EventSHA256 ||
		digestFile(t, filepath.Join(root, "p8-corpus-migrations", "sqlite", "manifest.json")) != provenance.SQLiteManifestSHA256 ||
		digestFile(t, filepath.Join(root, "p8-corpus-migrations", "postgresql", "manifest.json")) != provenance.PostgreSQLManifestSHA256 {
		t.Fatal("P7 compatibility provenance digest mismatch")
	}

	generatedBytes := mustRead(t, filepath.Join(root, "generated", ".golem", "generated-manifest.json"))
	if _, err := codegenmanifest.Parse(generatedBytes); err == nil {
		t.Fatal("ordinary parser accepted a historical generated manifest")
	}
	generated, err := codegenmanifest.ParseHistorical(generatedBytes)
	if err != nil || generated.FormatVersion != 1 || generated.GenerationDigest != provenance.GenerationDigest {
		t.Fatalf("load historical generated manifest: version=%d err=%v", generated.FormatVersion, err)
	}
	if _, err := workflow.ReadModelHead(root, "p8-corpus-migrations"); err != nil {
		t.Fatalf("load frozen reviewed migration history: %v", err)
	}

	actual, count := digestP7Corpus(t, root)
	if count != 28 || actual != p7CompatibilityCorpusSHA256 {
		t.Fatalf("P7 compatibility corpus identity count=%d digest=%s", count, actual)
	}

	eventRoot := filepath.Join(compatibilityModuleRoot(t), "internal", "compatibility", "testdata", "p7-event")
	eventProvenanceBytes := mustRead(t, filepath.Join(eventRoot, "PROVENANCE.json"))
	eventDecoder := json.NewDecoder(bytes.NewReader(eventProvenanceBytes))
	eventDecoder.DisallowUnknownFields()
	var eventProvenance p7EventProvenance
	if err := eventDecoder.Decode(&eventProvenance); err != nil {
		t.Fatal(err)
	}
	if err := eventDecoder.Decode(&trailing); err != io.EOF || eventProvenance.FormatVersion != 1 || eventProvenance.SourceCommit != provenance.SourceCommit || eventProvenance.FactProducer != "P7 generated System.Posts.Create" {
		t.Fatal("P7 event compatibility provenance is not exact")
	}
	if digestFile(t, filepath.Join(eventRoot, "source", "model.go")) != eventProvenance.SourceSHA256 ||
		digestFile(t, filepath.Join(eventRoot, "generated", ".golem", "generated-manifest.json")) != eventProvenance.GeneratedManifestSHA256 ||
		digestFile(t, filepath.Join(eventRoot, "event.json")) != eventProvenance.EventSHA256 ||
		digestFile(t, filepath.Join(eventRoot, "migrations", "sqlite", "manifest.json")) != eventProvenance.SQLiteMigrationManifestSHA256 ||
		digestFile(t, filepath.Join(eventRoot, "migrations", "postgresql", "manifest.json")) != eventProvenance.PostgreSQLMigrationManifestSHA256 {
		t.Fatal("P7 event compatibility provenance digest mismatch")
	}
	var eventEnvelope struct {
		FormatVersion uint16                 `json:"formatVersion"`
		SourceCommit  string                 `json:"sourceCommit"`
		Row           mutationfact.OutboxRow `json:"row"`
	}
	eventBytes := mustRead(t, filepath.Join(eventRoot, "event.json"))
	if err := json.Unmarshal(eventBytes, &eventEnvelope); err != nil || eventEnvelope.FormatVersion != 1 || eventEnvelope.SourceCommit != eventProvenance.SourceCommit || eventEnvelope.Row.GenerationFingerprint != eventProvenance.GenerationDigest || eventEnvelope.Row.EventID == "" || eventEnvelope.Row.CausationID == "" || eventEnvelope.Row.TransactionOrdinal != 1 {
		t.Fatalf("P7 generated event is not exact: %#v err=%v", eventEnvelope, err)
	}
	eventGenerated := mustRead(t, filepath.Join(eventRoot, "generated", ".golem", "generated-manifest.json"))
	if _, err := codegenmanifest.Parse(eventGenerated); err == nil {
		t.Fatal("ordinary parser accepted the historical event generated manifest")
	}
	if historical, err := codegenmanifest.ParseHistorical(eventGenerated); err != nil || historical.FormatVersion != 1 || historical.GenerationDigest != eventProvenance.GenerationDigest {
		t.Fatalf("load historical event generated manifest: %#v err=%v", historical, err)
	}
	if _, err := workflow.ReadModelHead(compatibilityModuleRoot(t), "internal/compatibility/testdata/p7-event/migrations"); err != nil {
		t.Fatalf("load frozen event reviewed migration history: %v", err)
	}
	eventActual, eventCount := digestP7Corpus(t, eventRoot)
	if eventCount != 28 || eventActual != p7EventCompatibilityCorpusSHA256 {
		t.Fatalf("P7 event compatibility corpus identity count=%d digest=%s", eventCount, eventActual)
	}
}

func digestP7Corpus(t *testing.T, root string) (string, int) {
	t.Helper()
	paths := make([]string, 0, 32)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return relativeErr
			}
			relative = filepath.ToSlash(relative)
			lower := strings.ToLower(relative)
			if strings.HasSuffix(lower, "generation.lock") || strings.Contains(lower, "journal") || strings.HasSuffix(lower, "-wal") || strings.HasSuffix(lower, "-shm") || strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".tmp") {
				t.Fatalf("compatibility corpus contains transient artifact %q", relative)
			}
			content := mustRead(t, path)
			if bytes.Contains(content, []byte("postgresql://")) || bytes.Contains(content, []byte("file:/tmp/")) {
				t.Fatalf("compatibility corpus contains a data source name in %q", relative)
			}
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		content := mustRead(t, filepath.Join(root, filepath.FromSlash(relative)))
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), len(paths)
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256(mustRead(t, path))
	return hex.EncodeToString(sum[:])
}
