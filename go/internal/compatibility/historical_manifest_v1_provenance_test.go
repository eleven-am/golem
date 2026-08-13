package compatibility

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalManifestV1RetainedAdaptationProvenance(t *testing.T) {
	const (
		wantTag          = "go/v0.0.2"
		wantCommit       = "efadc57d1da9b03e84c8cd746323fee3cc2f72c2"
		wantPath         = "go/compatibility/manifest.json"
		wantReleasedSHA  = "59bd82177890ff594f053ab0cc06f4d1a0b15567d85e673ae6ca563602062c1c"
		wantAdaptedSHA   = "846c6b5edcff131a51d0b891853f828d0d6b6b1a4355e0335caa9fc2a1c48474"
		wantAdaptedLines = 318
	)
	if historicalManifestV1SourceTag != wantTag || historicalManifestV1SourceCommit != wantCommit || historicalManifestV1SourcePath != wantPath || historicalManifestV1SourceSHA256 != wantReleasedSHA {
		t.Fatalf("historical manifest v1 source provenance=%q/%q/%q/%q", historicalManifestV1SourceTag, historicalManifestV1SourceCommit, historicalManifestV1SourcePath, historicalManifestV1SourceSHA256)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(releasedManifestV1Bytes(t))); got != wantReleasedSHA {
		t.Fatalf("released historical manifest fixture digest=%s", got)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve retained historical manifest source")
	}
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(source), "historical_manifest_v1.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != wantAdaptedSHA {
		t.Fatalf("retained historical manifest v1 adaptation changed: got %s want %s", got, wantAdaptedSHA)
	}
	if lines := bytes.Count(payload, []byte{'\n'}); lines != wantAdaptedLines {
		t.Fatalf("retained historical manifest v1 adaptation lines=%d want=%d", lines, wantAdaptedLines)
	}
	for _, forbidden := range [][]byte{
		[]byte("var value Manifest"),
		[]byte("\tif validate("),
		[]byte("Encode(value)"),
		[]byte("clone(value)"),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("retained historical manifest v1 decoder references mutable current rule/type %q", forbidden)
		}
	}
}
