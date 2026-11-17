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

func TestHistoricalContractV7RetainedSourceProvenance(t *testing.T) {
	// Exact files read from the released module cache; no current emitter was
	// used to manufacture the retained grammar or the released fixture.
	if historicalContractV7Release != "v0.8.8" || historicalContractV7TypesSHA256 != "3859f99d641200441c80de33bee5c1b4cce11c81e36ff9bca14d4fe5620acb40" || historicalContractV7CanonicalSHA256 != "307913227863ea86564a3942192b279c1a1a2c0c7383b5167c8044f37d95e454" || historicalContractV7FramingSHA256 != "cd3b37827c81e1843d7c18d51de941fe97170e0781a9f18511263a2aa9d594c5" {
		t.Fatal("released ContractIR-v7 provenance changed")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate retained decoder")
	}
	adapted, err := os.ReadFile(filepath.Join(filepath.Dir(source), "historical_contract_v7.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(adapted)); got != "7dd3451610399cd299b7fa4f09370ca54f31da52a6aae3a2bddf456b31e84826" || bytes.Count(adapted, []byte("\n")) != 509 {
		t.Fatalf("retained adaptation changed: sha256=%s lines=%d", got, bytes.Count(adapted, []byte("\n")))
	}
	// Extracted byte-for-byte from v0.8.8 runtime/testdata/p10operations/
	// zz_golem_registry.gen.go (source SHA256 below), whose GeneratedSchemaDocument
	// declares contract fingerprint 65d1ff937d319cc3acbf1790805832e85ad65a03fd73054be95190b622d2b4b8.
	const releasedGeneratedSourceSHA256 = "66d7f61531a1e49070e8562dce16948cd9436fa431eb4c179c3f726e40f4f0fa"
	if len(releasedGeneratedSourceSHA256) != 64 {
		t.Fatal("invalid fixture provenance")
	}
	fixture := releasedContractV7Fixture(t)
	if got := fmt.Sprintf("%x", sha256.Sum256(fixture)); got != "4d792aa2d09f31773bb56b7eaa431022faf80d7ffc06cf97fb6015422f35e764" || len(fixture) != 20989 {
		t.Fatalf("released fixture changed: sha256=%s bytes=%d", got, len(fixture))
	}
}
