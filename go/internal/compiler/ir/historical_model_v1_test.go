package ir

import (
	"bytes"
	"strings"
	"testing"
)

func TestModelIRV2CurrentAndV1HistoricalBoundariesAreExact(t *testing.T) {
	if ModelFormatVersion != 2 {
		t.Fatalf("current ModelIR format = %d, want 2", ModelFormatVersion)
	}
	if OptimisticConcurrencyModelFormatVersionRequired != ModelFormatVersion {
		t.Fatalf("optimistic-concurrency ModelIR requirement = %d, current = %d", OptimisticConcurrencyModelFormatVersionRequired, ModelFormatVersion)
	}
	const (
		payload         = `{"formatVersion":1,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[],"relations":[],"extensions":[]}`
		wantFingerprint = Fingerprint("6992d8f98da0f93040ac17317f91a65107dd916ff7af5b302e61474782de7a20")
	)
	decoded, err := CanonicalDecodeModelV1([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.FormatVersion != ModelFormatVersion || len(decoded.Models) != 0 {
		t.Fatalf("historical projection = %#v", decoded)
	}
	fingerprint, err := ModelFingerprintV1([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != wantFingerprint {
		t.Fatalf("released v1 fingerprint changed: got %s want %s", fingerprint, wantFingerprint)
	}
	if _, err := decodeCurrentModelCanonicalFraming([]byte(payload)); err == nil || !strings.Contains(err.Error(), "expected 2") {
		t.Fatalf("current decoder accepted v1: %v", err)
	}
}

func TestModelIRV2CanonicalEmptyBytesAndFingerprintAreExact(t *testing.T) {
	const (
		wantBytes       = `{"formatVersion":2,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[],"relations":[],"extensions":[]}`
		wantFingerprint = Fingerprint("05d6df59ca3caad3935d17dc16eb4e5fcb63c557037bf07a11cfc18ac751e1bc")
	)
	model := CanonicalEmptyModel()
	encoded, err := CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != wantBytes {
		t.Fatalf("canonical v2 bytes changed:\n got %s\nwant %s", encoded, wantBytes)
	}
	fingerprint, err := ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != wantFingerprint {
		t.Fatalf("canonical v2 fingerprint changed: got %s want %s", fingerprint, wantFingerprint)
	}
}

func TestModelV1HistoricalDecoderCannotConsumeCurrentOnlyFieldsEvenAtZeroValue(t *testing.T) {
	fieldID := FieldID("12000000000000000000000000000000")
	current := CanonicalEmptyModel()
	current.Models = []ModelDeclIR{{
		ID:                    "10000000000000000000000000000000",
		Fields:                []FieldIR{{ID: fieldID, Kind: FieldScalar, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeInt64}}}},
		OptimisticConcurrency: &fieldID,
	}}
	payload, err := CanonicalModel(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCurrentModelCanonicalFraming(payload); err != nil {
		t.Fatal(err)
	}
	relabelled := bytes.Replace(payload, []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
	if _, err := CanonicalDecodeModelV1(relabelled); err == nil || !strings.Contains(err.Error(), "optimisticConcurrency") {
		t.Fatalf("historical v1 decoder reinterpreted relabelled v2 bytes: %v", err)
	}

	// Even an explicit JSON zero value is a v2 member, not a v1 no-op. This
	// pins the rule that future additive zero-valued fields cannot change the
	// meaning or fingerprint of released v1 bytes.
	zeroSmuggle := []byte(`{"formatVersion":1,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[{"id":"10000000000000000000000000000000","canonicalIdentity":"","go":{"packagePath":"","name":""},"logicalName":"","table":{"physicalName":""},"fields":[],"primaryKey":null,"uniques":[],"indexes":[],"checks":[],"equalityIndexes":[],"optimisticConcurrency":null}],"relations":[],"extensions":[]}`)
	if _, err := CanonicalDecodeModelV1(zeroSmuggle); err == nil || !strings.Contains(err.Error(), "optimisticConcurrency") {
		t.Fatalf("historical v1 decoder accepted current-only zero field: %v", err)
	}
}

func TestModelV1HistoricalDecoderRejectsOpenAndNoncanonicalDocuments(t *testing.T) {
	const canonical = `{"formatVersion":1,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[],"relations":[],"extensions":[]}`
	for name, payload := range map[string][]byte{
		"unknown":         []byte(strings.Replace(canonical, `"providers":[]`, `"future":null,"providers":[]`, 1)),
		"duplicate":       []byte(strings.Replace(canonical, `"providers":[]`, `"providers":[],"providers":[]`, 1)),
		"open-provider":   []byte(strings.Replace(canonical, `"providers":[]`, `"providers":["future"]`, 1)),
		"repeat-provider": []byte(strings.Replace(canonical, `"providers":[]`, `"providers":["sqlite","sqlite"]`, 1)),
		"whitespace":      append([]byte(" "), []byte(canonical)...),
		"wrong-version":   []byte(strings.Replace(canonical, `"formatVersion":1`, `"formatVersion":3`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalDecodeModelV1(payload); err == nil {
				t.Fatalf("historical decoder accepted %s", payload)
			}
		})
	}
}
