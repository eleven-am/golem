package manifest

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestBuildDeterministicInventoryAndContentHashes(t *testing.T) {
	first := testRequest()
	second := testRequest()
	second.Artifacts[0], second.Artifacts[1] = second.Artifacts[1], second.Artifacts[0]
	left, err := Build(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Build(second)
	if err != nil {
		t.Fatal(err)
	}
	if left.GenerationDigest != right.GenerationDigest || !bytes.Equal(left.Bytes, right.Bytes) {
		t.Fatalf("shuffled inventory changed manifest\n%s\n%s", left.Bytes, right.Bytes)
	}
	changed := testRequest()
	changed.Artifacts[0].Content = []byte(GeneratedHeader + "\npackage changed\n")
	other, err := Build(changed)
	if err != nil {
		t.Fatal(err)
	}
	if other.GenerationDigest != left.GenerationDigest {
		t.Fatal("content-only change altered inventory GenerationDigest")
	}
	if entryHash(other.Manifest, "models/zz_golem_models.gen.go") == entryHash(left.Manifest, "models/zz_golem_models.gen.go") {
		t.Fatal("content-only change did not alter content hash")
	}
}

func entryHash(value Manifest, artifactPath string) string {
	for _, entry := range value.Artifacts {
		if entry.Path == artifactPath {
			return entry.ContentSHA256
		}
	}
	return ""
}

func TestBuildDigestDomains(t *testing.T) {
	base, _ := Build(testRequest())
	contract := testRequest()
	contract.ContractFingerprint = "different"
	contractResult, _ := Build(contract)
	if contractResult.GenerationDigest == base.GenerationDigest {
		t.Fatal("contract change did not alter GenerationDigest")
	}
	model := testRequest()
	model.ModelFingerprint = "different"
	modelResult, _ := Build(model)
	if modelResult.GenerationDigest == base.GenerationDigest {
		t.Fatal("model change did not alter GenerationDigest")
	}
	physical := testRequest()
	physical.ProviderFingerprints = []ProviderFingerprint{{Provider: ir.SQLite, Fingerprint: ir.Fingerprint(strings.Repeat("1", 64)), SystemFingerprint: ir.Fingerprint(strings.Repeat("2", 64))}}
	physicalResult, err := Build(physical)
	if err != nil {
		t.Fatal(err)
	}
	system := physical
	system.ProviderFingerprints = append([]ProviderFingerprint(nil), physical.ProviderFingerprints...)
	system.ProviderFingerprints[0].SystemFingerprint = ir.Fingerprint(strings.Repeat("3", 64))
	systemResult, err := Build(system)
	if err != nil {
		t.Fatal(err)
	}
	if systemResult.GenerationDigest == physicalResult.GenerationDigest {
		t.Fatal("system-only fingerprint change did not alter GenerationDigest")
	}
	migrationHistory := system
	migrationHistory.ProviderFingerprints = append([]ProviderFingerprint(nil), system.ProviderFingerprints...)
	migrationHistory.ProviderFingerprints[0].MigrationFingerprint = ir.Fingerprint(strings.Repeat("4", 64))
	migrationResult, err := Build(migrationHistory)
	if err != nil {
		t.Fatal(err)
	}
	if migrationResult.GenerationDigest == systemResult.GenerationDigest {
		t.Fatal("reviewed migration manifest change did not alter GenerationDigest")
	}
}

func TestBuildRejectsUnsafePathsDuplicatesAndMissingHeaders(t *testing.T) {
	for _, mutate := range []func(*Request){
		func(request *Request) { request.Artifacts[0].Path = "../escape.go" },
		func(request *Request) { request.Artifacts[1].Path = request.Artifacts[0].Path },
		func(request *Request) { request.Artifacts[0].Content = []byte("package app\n") },
	} {
		request := testRequest()
		mutate(&request)
		if _, err := Build(request); err == nil {
			t.Fatal("invalid manifest request succeeded")
		}
	}
}

func TestParseRejectsTrailingAndInventoryTampering(t *testing.T) {
	result, err := Build(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(append(append([]byte(nil), result.Bytes...), []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	tampered := bytes.Replace(result.Bytes, []byte(`"kind": "model_go"`), []byte(`"kind": "metadata"`), 1)
	if _, err := Parse(tampered); err == nil {
		t.Fatal("inventory tampering with stale GenerationDigest was accepted")
	}
	uppercaseHash := bytes.Replace(result.Bytes, []byte(entryHash(result.Manifest, "models/zz_golem_models.gen.go")), []byte(strings.ToUpper(entryHash(result.Manifest, "models/zz_golem_models.gen.go"))), 1)
	if _, err := Parse(uppercaseHash); err == nil {
		t.Fatal("non-canonical uppercase content hash was accepted")
	}
	providerRequest := testRequest()
	providerRequest.ProviderFingerprints = []ProviderFingerprint{{Provider: ir.SQLite, Fingerprint: ir.Fingerprint(strings.Repeat("1", 64)), SystemFingerprint: ir.Fingerprint(strings.Repeat("2", 64))}}
	providerResult, err := Build(providerRequest)
	if err != nil {
		t.Fatal(err)
	}
	staleSystem := bytes.Replace(providerResult.Bytes, []byte(`"systemFingerprint": "`+strings.Repeat("2", 64)+`"`), []byte(`"systemFingerprint": "`+strings.Repeat("3", 64)+`"`), 1)
	if _, err := Parse(staleSystem); err == nil || !strings.Contains(err.Error(), "GenerationDigest") {
		t.Fatalf("stale system fingerprint error=%v", err)
	}
	migrationRequest := providerRequest
	migrationRequest.ProviderFingerprints = append([]ProviderFingerprint(nil), providerRequest.ProviderFingerprints...)
	migrationRequest.ProviderFingerprints[0].MigrationFingerprint = ir.Fingerprint(strings.Repeat("4", 64))
	migrationBound, err := Build(migrationRequest)
	if err != nil {
		t.Fatal(err)
	}
	staleMigration := bytes.Replace(migrationBound.Bytes, []byte(`"migrationFingerprint": "`+strings.Repeat("4", 64)+`"`), []byte(`"migrationFingerprint": "`+strings.Repeat("5", 64)+`"`), 1)
	if _, err := Parse(staleMigration); err == nil || !strings.Contains(err.Error(), "GenerationDigest") {
		t.Fatalf("stale migration fingerprint error=%v", err)
	}
}

func TestBuildRejectsProviderWithoutSystemFingerprint(t *testing.T) {
	request := testRequest()
	request.ProviderFingerprints = []ProviderFingerprint{{Provider: ir.SQLite, Fingerprint: ir.Fingerprint(strings.Repeat("1", 64))}}
	if _, err := Build(request); err == nil || !strings.Contains(err.Error(), "incomplete provider fingerprint") {
		t.Fatalf("error=%v", err)
	}
}

func testRequest() Request {
	return Request{
		ModelFingerprint: ir.Fingerprint("model"), ContractFingerprint: ir.Fingerprint("contract"),
		Artifacts: []Artifact{
			{Path: "models/zz_golem_models.gen.go", Kind: ArtifactModelGo, Content: []byte(GeneratedHeader + "\npackage models\n"), GeneratedHeader: GeneratedHeader},
			{Path: "generated/registry.json", Kind: ArtifactMetadata, Content: []byte("{}\n")},
		},
	}
}
