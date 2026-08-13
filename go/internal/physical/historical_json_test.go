package physical

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHistoricalV3SnapshotJSONMatchesItsFrozenCurrentProjection(t *testing.T) {
	schema, err := NormalizeHistoricalV3(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	got, err := MarshalHistoricalSnapshotJSON(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("historical v3 snapshot JSON differs from its frozen publication projection")
	}
	if !bytes.Contains(got, []byte(`"OptimisticConcurrency"`)) {
		t.Fatal("historical v3 snapshot JSON omitted its concurrency identity field")
	}
}

func TestHistoricalSnapshotJSONNamesDoNotUseMutableStructTags(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate historical JSON source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "historical_json.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, ".Tag.Get(") || strings.Contains(text, "field.Tag") {
		t.Fatal("historical JSON key names derive from mutable current struct tags")
	}
	for _, required := range []string{`"TypedLiteralIR"`, `"Kind": "kind"`, `"Canonical": "canonical"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("historical JSON name projection omitted %s", required)
		}
	}
}
