package compatibility

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestObservationManifestSemanticComparison(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join(compatibilityModuleRoot(t), "observe", "telemetry-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := ParseObservationInventory(encoded)
	if err != nil || CompareObservation(frozen, frozen) != LayerUnchanged {
		t.Fatalf("parse checked observation manifest: %v", err)
	}
	additive := frozen
	additive.Attributes = append(append([]ObservationAttribute(nil), frozen.Attributes...), ObservationAttribute{Name: "golem.future", Type: "int64"})
	if CompareObservation(frozen, additive) != LayerAdditive {
		t.Fatal("optional observation attribute addition was not additive")
	}
	removed := frozen
	removed.Coverage = append([]ObservationCoverage(nil), frozen.Coverage...)
	removed.Coverage[0].Operations = nil
	if CompareObservation(frozen, removed) != LayerBreaking {
		t.Fatal("observation operation removal was not breaking")
	}
	changed := frozen
	changed.OTel.Metrics = append([]ObservationMetric(nil), frozen.OTel.Metrics...)
	changed.OTel.Metrics[0].Unit = "{future}"
	if CompareObservation(frozen, changed) != LayerBreaking {
		t.Fatal("observation signal mutation was not breaking")
	}
	for _, hostile := range [][]byte{
		append(append([]byte(nil), encoded...), []byte(`{"foreign":true}`)...),
		[]byte(`{"formatVersion":1,"formatVersion":1}`),
		bytes.Replace(encoded, []byte("{\n  \"formatVersion\": 1,"), []byte("{\n  \"formatVersion\": 1,\n  \"formatVersion\": 1,"), 1),
	} {
		if _, err := ParseObservationInventory(hostile); err == nil {
			t.Fatal("observation parser accepted hostile inventory")
		}
	}
}
