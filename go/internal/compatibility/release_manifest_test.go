package compatibility

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestP8CompatibilityManifestCanonicalAndComplete(t *testing.T) {
	root := compatibilityModuleRoot(t)
	path := filepath.Join(root, "compatibility", "manifest.json")
	encoded := mustRead(t, path)
	parsed, err := Parse(encoded, TrustedManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	expected := DevelopmentManifest()
	if !reflect.DeepEqual(parsed, expected) {
		t.Fatal("checked compatibility manifest differs from compiled release inventory")
	}
	canonical, err := Encode(expected)
	if err != nil || !bytes.Equal(canonical, encoded) || len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		t.Fatalf("checked compatibility manifest is not exact canonical LF bytes: %v", err)
	}
	for name, digest := range map[string]string{
		"public-go-api.json":    parsed.Digests.PublicGoAPI,
		"generated-go-abi.json": parsed.Digests.GeneratedGoABI,
		"graphql-abi.json":      parsed.Digests.GraphQLABI,
	} {
		if actual := Digest(mustRead(t, filepath.Join(root, "internal", "compatibility", "testdata", name))); actual != digest {
			t.Fatalf("compatibility manifest digest for %s is stale", name)
		}
	}
	telemetry := mustRead(t, filepath.Join(root, "observe", "telemetry-manifest.json"))
	if actual := Digest(telemetry); actual != parsed.Digests.Observation {
		t.Fatalf("compatibility manifest observation digest=%s actual=%s", parsed.Digests.Observation, actual)
	}
	observation, err := ParseObservationInventory(telemetry)
	if err != nil || CompareObservation(observation, observation) != LayerUnchanged {
		t.Fatalf("checked observation ABI is not semantically comparable: %v", err)
	}
	if !reflect.DeepEqual(parsed.HistoricalDecode.ModelIR, []uint16{1, 2}) ||
		!reflect.DeepEqual(parsed.HistoricalDecode.ContractIR, []uint16{4, 5, 6}) ||
		!reflect.DeepEqual(parsed.HistoricalDecode.PhysicalSchema, []uint16{1, 2, 3}) ||
		!reflect.DeepEqual(parsed.HistoricalDecode.PhysicalCanonical, []uint16{1, 2, 3}) ||
		!reflect.DeepEqual(parsed.HistoricalDecode.GraphQL, []uint16{4, 5}) ||
		!reflect.DeepEqual(parsed.HistoricalDecode.GeneratedManifests, []uint16{1, 2}) {
		t.Fatal("manifest omits an executable historical decoder")
	}
	if bytes.Contains(encoded, []byte(root)) || bytes.Contains(encoded, []byte("postgresql://")) || bytes.Contains(encoded, []byte("file:")) {
		t.Fatal("compatibility manifest contains an absolute path or data source name")
	}

	unknown := append([]byte(nil), encoded[:len(encoded)-2]...)
	unknown = append(unknown, []byte(",\n  \"foreign\": true\n}\n")...)
	duplicate := bytes.Replace(encoded, []byte("{\n  \"formatVersion\": 2,"), []byte("{\n  \"formatVersion\": 2,\n  \"formatVersion\": 2,"), 1)
	if bytes.Equal(duplicate, encoded) {
		t.Fatal("duplicate-key hostile mutation did not change the active manifest bytes")
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "versions")
	missing, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	missing = append(missing, '\n')
	for _, hostile := range [][]byte{unknown, duplicate, missing, append([]byte(" "), encoded...)} {
		if _, err := Parse(hostile, Digest(hostile)); err == nil {
			t.Fatal("compatibility parser accepted noncanonical or structurally incompatible bytes")
		}
	}
	tampered := append([]byte(nil), encoded...)
	tampered[len(tampered)-2] ^= 1
	if _, err := Parse(tampered, TrustedManifestSHA256); err == nil {
		t.Fatal("trusted compatibility digest accepted tampered bytes")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
