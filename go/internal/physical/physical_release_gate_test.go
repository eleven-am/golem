package physical

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCompatibilityManifestCannotPublishUnfrozenPhysicalFormat(t *testing.T) {
	if LatestFrozenPhysicalFormatVersion != uint16(SchemaFormatVersion) {
		t.Fatalf("current physical format %d is not independently frozen for publication; latest frozen is %d", SchemaFormatVersion, LatestFrozenPhysicalFormatVersion)
	}
	raw, err := os.ReadFile("../../compatibility/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Versions struct {
			PhysicalSchema    uint16 `json:"physicalSchema"`
			PhysicalCanonical uint16 `json:"physicalCanonical"`
		} `json:"versions"`
		HistoricalDecode struct {
			PhysicalSchema    []uint16 `json:"physicalSchema"`
			PhysicalCanonical []uint16 `json:"physicalCanonical"`
		} `json:"historicalDecode"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Versions.PhysicalSchema > LatestFrozenPhysicalFormatVersion || manifest.Versions.PhysicalCanonical > LatestFrozenPhysicalFormatVersion {
		t.Fatalf("compatibility manifest advertises unfrozen physical format %d/%d; latest frozen is %d", manifest.Versions.PhysicalSchema, manifest.Versions.PhysicalCanonical, LatestFrozenPhysicalFormatVersion)
	}
	if !containsFrozenVersion(manifest.HistoricalDecode.PhysicalSchema, manifest.Versions.PhysicalSchema) || !containsFrozenVersion(manifest.HistoricalDecode.PhysicalCanonical, manifest.Versions.PhysicalCanonical) {
		t.Fatal("compatibility manifest current physical versions are absent from retained decode inventory")
	}
}

func containsFrozenVersion(values []uint16, want uint16) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
