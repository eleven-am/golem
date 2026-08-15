package main

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestStaleArtifactRejection(t *testing.T) {
	t.Run("tampered generated publication manifest", func(t *testing.T) {
		fingerprint := compilerir.Fingerprint(strings.Repeat("1", 64))
		built, err := manifest.Build(manifest.Request{
			ModelFingerprint:    fingerprint,
			ContractFingerprint: fingerprint,
			GeneratorVersion:    "p8-stale-oracle",
			TemplateABIVersion:  "p8-stale-abi",
			Artifacts: []manifest.Artifact{{
				Path: "app/zz_golem_models.gen.go", Kind: manifest.ArtifactModelGo,
				Content:         []byte(manifest.GeneratedHeader + "\npackage app\n"),
				GeneratedHeader: manifest.GeneratedHeader,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(built.Bytes, &value); err != nil {
			t.Fatal(err)
		}
		value["generationDigest"] = strings.Repeat("2", 64)
		tampered, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		_, err = manifest.Parse(tampered)
		if err == nil || err.Error() != "generated manifest artifact \"app/zz_golem_models.gen.go\" has inconsistent metadata" {
			t.Fatalf("tampered generated manifest error = %#v", err)
		}
	})

	t.Run("unsupported generated publication manifest version", func(t *testing.T) {
		_, err := manifest.Parse([]byte(`{
  "formatVersion": 65535,
  "modelFingerprint": "1111111111111111111111111111111111111111111111111111111111111111",
  "contractFingerprint": "1111111111111111111111111111111111111111111111111111111111111111",
  "generationDigest": "1111111111111111111111111111111111111111111111111111111111111111",
  "generatorVersion": "p8-stale-oracle",
  "templateAbiVersion": "p8-stale-abi",
  "providerFingerprints": [],
  "artifacts": []
}`))
		if err == nil || err.Error() != "generated manifest format 65535 is unsupported" {
			t.Fatalf("unsupported generated manifest error = %#v", err)
		}
	})

	t.Run("malformed release metadata is closed", func(t *testing.T) {
		oldVersion, oldCommit, oldRead := buildVersion, buildCommit, readBuildInfo
		t.Cleanup(func() {
			buildVersion, buildCommit, readBuildInfo = oldVersion, oldCommit, oldRead
		})

		const releaseCanary = "v1.2.3+credential-canary"
		const commitCanary = "credential-canary"
		buildVersion, buildCommit = releaseCanary, commitCanary
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			panic("explicit malformed linker metadata fell back to ambient build metadata")
		}
		version, commit := normalizedBuildProvenance()
		if version != "devel" || commit != unknownCommit {
			t.Fatalf("malformed release provenance normalized to %q/%q", version, commit)
		}
		if strings.Contains(version, releaseCanary) || strings.Contains(commit, commitCanary) {
			t.Fatal("malformed release provenance disclosed untrusted metadata")
		}
	})
}
