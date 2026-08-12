package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compatibility"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestP8StaleArtifactAndCompatibilityManifestRejection(t *testing.T) {
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

	t.Run("trusted compatibility manifest", func(t *testing.T) {
		path := filepath.Join("..", "..", "compatibility", "manifest.json")
		encoded, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compatibility.Parse(encoded, compatibility.TrustedManifestSHA256); err != nil {
			t.Fatalf("parse checked compatibility manifest: %v", err)
		}

		// Release preflight is deliberately modeled as a hard boundary: database
		// and worker callbacks become reachable only after the separately compiled
		// trust root and the complete canonical document have both been accepted.
		var databaseTouches, workerStarts int
		preflight := func(document []byte, trustedDigest string) error {
			if _, err := compatibility.Parse(document, trustedDigest); err != nil {
				return err
			}
			databaseTouches++
			workerStarts++
			return nil
		}
		assertRejectedBeforeWork := func(name string, document []byte, trustedDigest string, reason compatibility.Reason) {
			t.Helper()
			databaseTouches, workerStarts = 0, 0
			err := preflight(document, trustedDigest)
			var failure *compatibility.Error
			if !errors.As(err, &failure) || failure.Reason != reason || err.Error() != "P8_COMPATIBILITY: "+string(reason) {
				t.Fatalf("%s error = %#v", name, err)
			}
			if databaseTouches != 0 || workerStarts != 0 {
				t.Fatalf("%s reached database=%d worker=%d after preflight rejection", name, databaseTouches, workerStarts)
			}
			if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "credential-canary") || strings.Contains(err.Error(), string(document)) {
				t.Fatalf("%s diagnostic disclosed untrusted details: %v", name, err)
			}
		}

		assertRejectedBeforeWork(
			"stale trust root",
			encoded,
			strings.Repeat("f", 64),
			compatibility.ReasonUntrustedDigest,
		)

		tampered := append([]byte(nil), encoded...)
		offset := bytes.Index(tampered, []byte(`"module": "github.com/eleven-am/golem/go"`))
		if offset < 0 {
			t.Fatal("checked compatibility manifest has no module identity")
		}
		tampered[offset+len(`"module": "`)] = 'x'
		assertRejectedBeforeWork(
			"tampered artifact",
			tampered,
			compatibility.TrustedManifestSHA256,
			compatibility.ReasonUntrustedDigest,
		)

		unsupported := bytes.Replace(encoded, []byte(`"formatVersion": 2`), []byte(`"formatVersion": 3`), 1)
		if bytes.Equal(unsupported, encoded) {
			t.Fatal("checked compatibility manifest has no format version")
		}
		assertRejectedBeforeWork(
			"unsupported self-digested artifact",
			unsupported,
			compatibility.Digest(unsupported),
			compatibility.ReasonUnsupportedFormat,
		)

		duplicate := bytes.Replace(encoded, []byte("{\n  \"formatVersion\": 2,"), []byte("{\n  \"formatVersion\": 2,\n  \"formatVersion\": 2,"), 1)
		if bytes.Equal(duplicate, encoded) {
			t.Fatal("duplicate-key hostile mutation did not change the active manifest bytes")
		}
		assertRejectedBeforeWork(
			"duplicate self-digested artifact",
			duplicate,
			compatibility.Digest(duplicate),
			compatibility.ReasonNoncanonical,
		)
	})
}
