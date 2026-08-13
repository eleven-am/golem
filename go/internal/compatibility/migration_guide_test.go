package compatibility

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestGoV002ToV010MigrationGuideIsCanonicalTrustedAndTransitionBound(t *testing.T) {
	if MigrationGuidePath != "compatibility/migration-guide-go-v0.0.2-to-v0.1.0.json" || MigrationGuideSHA256 != "6fff0894d4ac402e0b2eb5bbbd722d560e1e775b661f0305467aed066d4ec142" {
		t.Fatalf("current migration guide authority=%s/%s", MigrationGuidePath, MigrationGuideSHA256)
	}
	encoded := checkedMigrationGuide(t)
	guide, err := ParseMigrationGuide(encoded, MigrationGuideSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if guide.From.Tag != "go/v0.0.2" || guide.From.Commit != "efadc57d1da9b03e84c8cd746323fee3cc2f72c2" || guide.ToVersion != "v0.1.0" {
		t.Fatalf("guide endpoints=%#v/%s", guide.From, guide.ToVersion)
	}
	wantActions := []string{"migrate.database", "migration-guide.execute", "regenerate.generated"}
	if !reflect.DeepEqual(guide.RequiredActions, wantActions) {
		t.Fatalf("guide actions=%v", guide.RequiredActions)
	}
	if err := ValidateMigrationGuideTransition(guide, MigrationGuideAuthority{
		Path: MigrationGuidePath, SHA256: MigrationGuideSHA256, FromTag: "go/v0.0.2", ToVersion: "v0.1.0",
	}, "go/v0.0.2", guide.From.Commit, "go/v0.1.0", "v0.1.0", wantActions); err != nil {
		t.Fatal(err)
	}
	reencoded, err := EncodeMigrationGuide(guide)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("guide canonical roundtrip err=%v", err)
	}
	guide.From.Tag = "go/v9.9.9"
	guide.From.Commit = "9999999999999999999999999999999999999999"
	guide.RequiredActions[0] = "mutated.action"
	guide.Corpora[0].Path = "mutated/corpus"
	guide.Corpora[0].GitTree = "9999999999999999999999999999999999999999"
	guide.Evidence[0].Identity = "github.com/eleven-am/golem/go/cmd/golem:TestMutated"
	guide.Evidence[0].Command = "go test ./cmd/golem -run '^TestMutated$' -count=1"
	again, err := ParseMigrationGuide(encoded, MigrationGuideSHA256)
	if err != nil || again.From.Tag != "go/v0.0.2" || again.From.Commit != "efadc57d1da9b03e84c8cd746323fee3cc2f72c2" ||
		again.RequiredActions[0] != "migrate.database" || again.Corpora[0].Path != "internal/compatibility/testdata/p7" || again.Corpora[0].GitTree != "dce564e9e3aff0b0f96ae8b3278e75d588d2f71c" ||
		again.Evidence[0].Identity != "github.com/eleven-am/golem/go/cmd/golem:TestP8ExecutableGoV002ToV010MigrationGuide" || again.Evidence[0].Command != "go test ./cmd/golem -run '^TestP8ExecutableGoV002ToV010MigrationGuide$' -count=1" {
		t.Fatalf("parsed guide retained caller mutation: %#v err=%v", again, err)
	}
}

func TestMigrationGuideRejectsAbsentTrustTamperRelabelNullUnknownDuplicateTrailingAndNoncanonical(t *testing.T) {
	released := checkedMigrationGuide(t)
	mutations := []struct {
		name string
		data []byte
		sha  string
	}{
		{name: "untrusted", data: released, sha: Digest([]byte("different"))},
		{name: "future", data: bytes.Replace(released, []byte(`"formatVersion": 1`), []byte(`"formatVersion": 2`), 1)},
		{name: "invalid from label", data: bytes.Replace(released, []byte(`"go/v0.0.2"`), []byte(`"v0.0.2"`), 1)},
		{name: "null actions", data: bytes.Replace(released, []byte(`"requiredActions": [`), []byte(`"requiredActions": null, "ignored": [`), 1)},
		{name: "unknown", data: bytes.Replace(released, []byte(`  "from":`), []byte("  \"unknown\": true,\n  \"from\":"), 1)},
		{name: "duplicate", data: bytes.Replace(released, []byte(`  "toVersion":`), []byte("  \"toVersion\": \"v0.1.0\",\n  \"toVersion\":"), 1)},
		{name: "trailing", data: append(append([]byte{}, released...), []byte("{}\n")...)},
		{name: "noncanonical", data: bytes.Replace(released, []byte(`  "from":`), []byte(`    "from":`), 1)},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			sha := mutation.sha
			if sha == "" {
				sha = Digest(mutation.data)
			}
			if _, err := ParseMigrationGuide(mutation.data, sha); err == nil {
				t.Fatal("hostile guide accepted")
			}
		})
	}
}

func TestManifestMigrationGuideAuthorityIsExactAndActionCoupled(t *testing.T) {
	value := compatibilityFixture()
	value.RequiredActions = []string{"migration-guide.execute"}
	value.MigrationGuide = &MigrationGuideAuthority{Path: MigrationGuidePath, SHA256: MigrationGuideSHA256, FromTag: "go/v0.0.2", ToVersion: "v0.1.0"}
	if _, err := Encode(value); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Manifest){
		func(v *Manifest) { v.MigrationGuide = nil },
		func(v *Manifest) { v.MigrationGuide.Path = "../MIGRATION.json" },
		func(v *Manifest) { v.MigrationGuide.SHA256 = "" },
		func(v *Manifest) { v.MigrationGuide.FromTag = "v0.0.2" },
		func(v *Manifest) { v.MigrationGuide.ToVersion = "v0.0.2" },
		func(v *Manifest) { v.RequiredActions = []string{} },
	} {
		candidate := clone(value)
		mutate(&candidate)
		if _, err := Encode(candidate); err == nil {
			t.Fatal("invalid guide authority encoded")
		}
	}
}

func TestMigrationGuideV1AndManifestV2RemainGenericForFutureTransition(t *testing.T) {
	guide := MigrationGuide{
		FormatVersion:   1,
		From:            MigrationGuideEndpoint{Tag: "go/v1.9.0", Commit: "1111111111111111111111111111111111111111"},
		ToVersion:       "v2.0.0",
		RequiredActions: []string{"migration-guide.execute", "regenerate.generated"},
		Corpora:         []MigrationGuideCorpus{{Path: "internal/compatibility/testdata/v1", GitTree: "2222222222222222222222222222222222222222"}},
		Evidence:        []MigrationGuideEvidence{{Identity: "github.com/eleven-am/golem/go/cmd/golem:TestFutureV1ToV2Guide", Command: "go test ./cmd/golem -run '^TestFutureV1ToV2Guide$' -count=1"}},
	}
	encoded, err := EncodeMigrationGuide(guide)
	if err != nil {
		t.Fatal(err)
	}
	guide.RequiredActions[0] = "mutated.action"
	guide.Corpora[0].Path = "mutated/corpus"
	guide.Evidence[0].Command = "go test ./cmd/golem -run '^TestMutated$' -count=1"
	if bytes.Contains(encoded, []byte("mutated")) {
		t.Fatal("encoded guide aliases input")
	}
	guide.RequiredActions[0] = "migration-guide.execute"
	guide.Corpora[0].Path = "internal/compatibility/testdata/v1"
	guide.Evidence[0].Command = "go test ./cmd/golem -run '^TestFutureV1ToV2Guide$' -count=1"
	parsed, err := ParseMigrationGuide(encoded, Digest(encoded))
	if err != nil || !reflect.DeepEqual(parsed, guide) {
		t.Fatalf("future guide=%#v err=%v", parsed, err)
	}
	authority := MigrationGuideAuthority{Path: "compatibility/migration-guide-go-v1-to-v2.json", SHA256: Digest(encoded), FromTag: "go/v1.9.0", ToVersion: "v2.0.0"}
	if err := ValidateMigrationGuideTransition(parsed, authority, "go/v1.9.0", guide.From.Commit, "go/v2.0.0", "v2.0.0", guide.RequiredActions); err != nil {
		t.Fatal(err)
	}
	manifest := compatibilityFixture()
	manifest.RequiredActions = append([]string{}, guide.RequiredActions...)
	manifest.MigrationGuide = &authority
	current, err := Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(current, Digest(current)); err != nil {
		t.Fatal(err)
	}
}

func checkedMigrationGuide(t *testing.T) []byte {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "compatibility", "migration-guide-go-v0.0.2-to-v0.1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
