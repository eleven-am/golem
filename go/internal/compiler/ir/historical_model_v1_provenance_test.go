package ir

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalModelV1RetainedAdaptationProvenance(t *testing.T) {
	const (
		wantUpstreamCommit  = "1f773a9"
		wantTypesSHA256     = "20a0d1bd5f3e1fbbd991d8abbca226a33d72afae71bd51703b2c4a88a2cab5b9"
		wantTypesLines      = 848
		wantCanonicalSHA256 = "fdcd49abd6935e67cda3e0f7a4d81e1a06622aac9ac3af4a1c5a68dfb56abbcf"
		wantCanonicalLines  = 770
		wantAdaptedSHA256   = "fafb6074334eb8d8cf699d0ae2a500ab9d5fabb85b6aa8a276c0d73c626808bc"
		wantAdaptedLines    = 808
	)
	if historicalModelV1UpstreamCommit != wantUpstreamCommit {
		t.Fatalf("ModelIR-v1 upstream commit changed: got %s want %s", historicalModelV1UpstreamCommit, wantUpstreamCommit)
	}
	if historicalModelV1TypesUpstreamSHA256 != wantTypesSHA256 || historicalModelV1TypesUpstreamLines != wantTypesLines {
		t.Fatalf("ModelIR-v1 type provenance changed: sha=%s lines=%d", historicalModelV1TypesUpstreamSHA256, historicalModelV1TypesUpstreamLines)
	}
	if historicalModelV1CanonicalUpstreamSHA256 != wantCanonicalSHA256 || historicalModelV1CanonicalUpstreamLines != wantCanonicalLines {
		t.Fatalf("ModelIR-v1 canonical provenance changed: sha=%s lines=%d", historicalModelV1CanonicalUpstreamSHA256, historicalModelV1CanonicalUpstreamLines)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve retained decoder source")
	}
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(current), "historical_model_v1.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != wantAdaptedSHA256 {
		t.Fatalf("retained ModelIR-v1 adaptation changed: got %s want %s", got, wantAdaptedSHA256)
	}
	if lines := bytes.Count(payload, []byte{'\n'}); lines != wantAdaptedLines {
		t.Fatalf("retained ModelIR-v1 adaptation lines=%d want=%d", lines, wantAdaptedLines)
	}
	for _, forbidden := range [][]byte{
		[]byte("normalizeModel("),
		[]byte("CanonicalModel("),
		[]byte("ModelDeclIR"),
		[]byte("providerRank("),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("retained ModelIR-v1 decoder references mutable current rule/type %q", forbidden)
		}
	}
}
