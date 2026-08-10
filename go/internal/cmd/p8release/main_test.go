package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	releasetool "github.com/eleven-am/golem/go/internal/release"
)

func TestP8ReleaseCommandEveryFailureIsClosedVersionedJSON(t *testing.T) {
	t.Setenv("GOLEM_RELEASE_ALLOWED_SIGNERS", "")
	t.Setenv("GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256", "")
	const canary = "postgresql://token@private.example/secret-path"
	for _, arguments := range [][]string{
		{"-mode", canary},
		{"-mode", "verify", "-tag", "go/v1.2.3", "-module", canary, "-allowed-signers", canary, "-allowed-signers-sha256", strings.Repeat("0", 64), "-proxy", "https://token@private.example"},
		{"-mode", "build", "-tag", "moving-branch", "-module", ".", "-allowed-signers", canary, "-allowed-signers-sha256", strings.Repeat("0", 64), "-output", canary},
	} {
		var encoded bytes.Buffer
		if exit := run(arguments, &encoded); exit != 1 {
			t.Fatalf("exit=%d output=%s", exit, encoded.Bytes())
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded.Bytes()))
		decoder.DisallowUnknownFields()
		var value output
		if err := decoder.Decode(&value); err != nil || value.FormatVersion != releasetool.FormatVersion || value.Status != "FAIL" || value.Code == "" || value.Module != "" || value.Commit != "" || value.ManifestSHA256 != "" {
			t.Fatalf("failure=%+v error=%v", value, err)
		}
		for _, protected := range []string{canary, "secret-path", "token@", "private.example", "moving-branch"} {
			if strings.Contains(encoded.String(), protected) {
				t.Fatalf("failure disclosed %q: %s", protected, encoded.Bytes())
			}
		}
	}
}

func TestP8ReleaseCommandSuccessShapeIsClosedAndDigestBound(t *testing.T) {
	value := output{
		FormatVersion: 1, Mode: "build", Status: "PASS", Module: releasetool.ModulePath, Version: "v1.2.3", Tag: "go/v1.2.3", Commit: strings.Repeat("a", 40),
		ManifestSHA256: strings.Repeat("b", 64), SourceSHA256: strings.Repeat("c", 64), SignersSHA256: strings.Repeat("d", 64), InventorySHA: strings.Repeat("e", 64),
	}
	var encoded bytes.Buffer
	if exit := writeOutput(&encoded, value, 0); exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	var decoded output
	decoder := json.NewDecoder(bytes.NewReader(encoded.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoded != value || decoded.Status != "PASS" {
		t.Fatalf("success=%+v error=%v", decoded, err)
	}
}
