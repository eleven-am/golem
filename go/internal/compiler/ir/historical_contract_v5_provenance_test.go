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

func TestHistoricalContractV5DecoderSourceProvenance(t *testing.T) {
	const (
		wantTypesSHA256     = "20a0d1bd5f3e1fbbd991d8abbca226a33d72afae71bd51703b2c4a88a2cab5b9"
		wantTypesLines      = 848
		wantCanonicalSHA256 = "fdcd49abd6935e67cda3e0f7a4d81e1a06622aac9ac3af4a1c5a68dfb56abbcf"
		wantCanonicalLines  = 770
		wantAdaptedSHA256   = "839920d795804e937177513b0810b00dd9e6f9aa56741d5dc4971b6823a4f936"
	)
	if historicalContractV5TypesUpstreamSHA256 != wantTypesSHA256 || historicalContractV5TypesUpstreamLines != wantTypesLines {
		t.Fatalf("v5 type provenance changed: sha=%s lines=%d", historicalContractV5TypesUpstreamSHA256, historicalContractV5TypesUpstreamLines)
	}
	if historicalContractV5CanonicalUpstreamSHA256 != wantCanonicalSHA256 || historicalContractV5CanonicalUpstreamLines != wantCanonicalLines {
		t.Fatalf("v5 canonical provenance changed: sha=%s lines=%d", historicalContractV5CanonicalUpstreamSHA256, historicalContractV5CanonicalUpstreamLines)
	}
	_, current, _, _ := runtime.Caller(0)
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(current), "historical_contract_v5.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != wantAdaptedSHA256 {
		t.Fatalf("retained v5 decoder adaptation changed: got %s want %s", got, wantAdaptedSHA256)
	}
	assertHistoricalContractV5HasNoMutableCurrentDependencies(t, payload)
}

func TestHistoricalContractV5HasNoMutableCurrentDependencies(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(current), "historical_contract_v5.go"))
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalContractV5HasNoMutableCurrentDependencies(t, payload)
}

func assertHistoricalContractV5HasNoMutableCurrentDependencies(t *testing.T, payload []byte) {
	t.Helper()
	for _, forbidden := range [][]byte{
		[]byte("decodeCurrentContractCanonicalFraming"),
		[]byte("validateContractJSONEnvelope"),
		[]byte("decodeExactContractJSON"),
		[]byte("validateCurrentContract"),
		[]byte("normalizeContract("),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("retained v5 decoder references mutable current rule %q", forbidden)
		}
	}
}
