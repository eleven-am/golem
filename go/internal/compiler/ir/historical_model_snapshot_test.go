package ir

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoricalModelSnapshotJSONDispatchPreservesV1AndCurrentAuthorities(t *testing.T) {
	const v1Canonical = `{"formatVersion":1,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[],"relations":[],"extensions":[]}`
	var v1Value v1Model
	if err := json.Unmarshal([]byte(v1Canonical), &v1Value); err != nil {
		t.Fatal(err)
	}
	v1Indented, err := json.MarshalIndent(v1Value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	v1Indented = append(v1Indented, '\n')
	v1, err := DecodeHistoricalModelSnapshotJSON(v1Indented)
	if err != nil {
		t.Fatal(err)
	}
	if v1.FormatVersion != 1 || v1.Model.FormatVersion != ModelFormatVersion || !bytes.Equal(v1.Canonical, []byte(v1Canonical)) {
		t.Fatalf("historical v1 snapshot = %#v", v1)
	}
	if v1.Fingerprint != "6992d8f98da0f93040ac17317f91a65107dd916ff7af5b302e61474782de7a20" {
		t.Fatalf("historical v1 fingerprint = %s", v1.Fingerprint)
	}

	field := FieldID("12000000000000000000000000000000")
	currentModel := CanonicalEmptyModel()
	currentModel.Models = []ModelDeclIR{{
		ID: "10000000000000000000000000000000",
		Fields: []FieldIR{{ID: field, Kind: FieldScalar, Scalar: &ScalarFieldIR{
			Type: LogicalTypeIR{Kind: TypeInt64},
		}}},
		OptimisticConcurrency: &field,
	}}
	currentCanonical, err := CanonicalModel(currentModel)
	if err != nil {
		t.Fatal(err)
	}
	var currentValue ModelIR
	if err := json.Unmarshal(currentCanonical, &currentValue); err != nil {
		t.Fatal(err)
	}
	currentIndented, err := json.MarshalIndent(currentValue, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	currentIndented = append(currentIndented, '\n')
	current, err := DecodeHistoricalModelSnapshotJSON(currentIndented)
	if err != nil {
		t.Fatal(err)
	}
	if current.FormatVersion != ModelFormatVersion || !bytes.Equal(current.Canonical, currentCanonical) || current.Model.Models[0].OptimisticConcurrency == nil || *current.Model.Models[0].OptimisticConcurrency != field {
		t.Fatalf("current snapshot lost optimistic concurrency: %#v", current)
	}
}

func TestHistoricalModelV1SnapshotJSONRejectsCurrentOnlyAndNoncanonicalFields(t *testing.T) {
	const canonical = `{"formatVersion":1,"schema":{"id":"","stableName":"","packagePath":"","rootFunction":"","actor":{"packagePath":"","name":""}},"providers":[],"enums":[],"models":[],"relations":[],"extensions":[]}`
	var value v1Model
	if err := json.Unmarshal([]byte(canonical), &value); err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	indented = append(indented, '\n')

	modelWithCurrentZero := strings.Replace(string(indented), "  \"models\": [],", "  \"models\": [\n    {\n      \"id\": \"10000000000000000000000000000000\",\n      \"canonicalIdentity\": \"\",\n      \"go\": {\n        \"packagePath\": \"\",\n        \"name\": \"\"\n      },\n      \"logicalName\": \"\",\n      \"table\": {\n        \"physicalName\": \"\"\n      },\n      \"fields\": [],\n      \"primaryKey\": null,\n      \"uniques\": [],\n      \"indexes\": [],\n      \"checks\": [],\n      \"equalityIndexes\": [],\n      \"optimisticConcurrency\": null\n    }\n  ],", 1)
	for name, payload := range map[string][]byte{
		"current-only zero": []byte(modelWithCurrentZero),
		"future zero":       []byte(strings.Replace(string(indented), "  \"providers\": [],", "  \"future\": null,\n  \"providers\": [],", 1)),
		"compact":           []byte(canonical),
		"wrong version":     []byte(strings.Replace(string(indented), "\"formatVersion\": 1", "\"formatVersion\": 3", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHistoricalModelSnapshotJSON(payload); err == nil {
				t.Fatalf("historical snapshot accepted %s", payload)
			}
		})
	}
}
