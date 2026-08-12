package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// HistoricalModelSnapshot is the verified representation of one immutable
// reviewed ModelIR snapshot. Model is projected into the current in-memory
// vocabulary, while Canonical and Fingerprint retain the authority of the
// serialized format that owned the released bytes.
type HistoricalModelSnapshot struct {
	FormatVersion uint16
	Model         ModelIR
	Canonical     []byte
	Fingerprint   Fingerprint
}

// DecodeHistoricalModelSnapshotJSON verifies the exact indented JSON form used
// by reviewed migration artifacts. ModelIR-v1 is decoded, validated, sorted,
// and re-encoded exclusively through the independent frozen v1 DTO. Current
// ModelIR uses the current canonical owner. Neither path re-labels one format
// as the other before its bytes and fingerprint have been established.
func DecodeHistoricalModelSnapshotJSON(payload []byte) (HistoricalModelSnapshot, error) {
	version, err := modelSnapshotFormatVersion(payload)
	if err != nil {
		return HistoricalModelSnapshot{}, err
	}
	switch version {
	case historicalModelV1FormatVersion:
		return decodeHistoricalModelV1SnapshotJSON(payload)
	case ModelFormatVersion:
		return decodeCurrentModelSnapshotJSON(payload)
	default:
		return HistoricalModelSnapshot{}, fmt.Errorf("historical ModelIR snapshot version %d is unsupported", version)
	}
}

func decodeHistoricalModelV1SnapshotJSON(payload []byte) (HistoricalModelSnapshot, error) {
	if err := validateModelJSONEnvelope(payload, historicalModelV1FormatVersion); err != nil {
		return HistoricalModelSnapshot{}, err
	}
	var historical v1Model
	if err := decodeExactModelJSON(payload, &historical); err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("historical ModelIR-v1 snapshot decode: %w", err)
	}
	if err := validateModelV1(historical); err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("historical ModelIR-v1 snapshot validation: %w", err)
	}
	normalizeModelV1(&historical)
	canonical, err := json.Marshal(historical)
	if err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("historical ModelIR-v1 snapshot canonicalize: %w", err)
	}
	if err := requireHistoricalModelSnapshotBytes(payload, historical); err != nil {
		return HistoricalModelSnapshot{}, err
	}
	var current ModelIR
	if err := json.Unmarshal(canonical, &current); err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("historical ModelIR-v1 snapshot projection: %w", err)
	}
	current.FormatVersion = ModelFormatVersion
	for index := range current.Models {
		current.Models[index].OptimisticConcurrency = nil
	}
	return HistoricalModelSnapshot{
		FormatVersion: historicalModelV1FormatVersion,
		Model:         current,
		Canonical:     canonical,
		Fingerprint:   fingerprint("golem:model-fingerprint:v1", canonical),
	}, nil
}

func decodeCurrentModelSnapshotJSON(payload []byte) (HistoricalModelSnapshot, error) {
	if err := validateModelJSONEnvelope(payload, ModelFormatVersion); err != nil {
		return HistoricalModelSnapshot{}, err
	}
	var current ModelIR
	if err := decodeExactModelJSON(payload, &current); err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("current ModelIR snapshot decode: %w", err)
	}
	canonical, err := CanonicalModel(current)
	if err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("current ModelIR snapshot canonicalize: %w", err)
	}
	var normalized ModelIR
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return HistoricalModelSnapshot{}, fmt.Errorf("current ModelIR snapshot projection: %w", err)
	}
	if err := requireHistoricalModelSnapshotBytes(payload, normalized); err != nil {
		return HistoricalModelSnapshot{}, err
	}
	return HistoricalModelSnapshot{
		FormatVersion: ModelFormatVersion,
		Model:         normalized,
		Canonical:     canonical,
		Fingerprint:   fingerprint("golem:model-fingerprint:v1", canonical),
	}, nil
}

func requireHistoricalModelSnapshotBytes(payload []byte, normalized any) error {
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("historical ModelIR snapshot encode: %w", err)
	}
	encoded = append(encoded, '\n')
	if !bytes.Equal(encoded, payload) {
		return fmt.Errorf("historical ModelIR snapshot is not canonical normalized JSON")
	}
	return nil
}

func modelSnapshotFormatVersion(payload []byte) (uint16, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("historical ModelIR snapshot is missing")
	}
	if len(payload) > maxCanonicalModelBytes {
		return 0, fmt.Errorf("historical ModelIR snapshot exceeds %d bytes", maxCanonicalModelBytes)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return 0, fmt.Errorf("historical ModelIR snapshot: %w", err)
	}
	var envelope struct {
		FormatVersion uint16 `json:"formatVersion"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return 0, fmt.Errorf("historical ModelIR snapshot format version: %w", err)
	}
	return envelope.FormatVersion, nil
}
