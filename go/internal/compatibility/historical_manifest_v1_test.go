package compatibility

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const releasedManifestV1SHA256 = "59bd82177890ff594f053ab0cc06f4d1a0b15567d85e673ae6ca563602062c1c"

func TestHistoricalManifestV1ExactReleasedBytesProjectToCurrent(t *testing.T) {
	encoded := releasedManifestV1Bytes(t)
	if actual := Digest(encoded); actual != releasedManifestV1SHA256 {
		t.Fatalf("released go/v0.0.2 manifest digest=%s", actual)
	}
	if _, err := Parse(encoded, releasedManifestV1SHA256); reasonOf(err) != ReasonUnsupportedFormat {
		t.Fatalf("active parser accepted released v1 bytes: %v", err)
	}
	projected, err := ParseHistorical(encoded, releasedManifestV1SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var expected Manifest
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	expected.FormatVersion = FormatVersion
	expected.HistoricalDecode.GraphQL = []uint16{expected.Versions.GraphQL}
	if !reflect.DeepEqual(projected, expected) {
		t.Fatalf("historical v1 projection differs:\n got=%#v\nwant=%#v", projected, expected)
	}
	if projected.FormatVersion != 2 || !reflect.DeepEqual(projected.HistoricalDecode.GraphQL, []uint16{4}) {
		t.Fatalf("historical projection format/GraphQL=%d/%v", projected.FormatVersion, projected.HistoricalDecode.GraphQL)
	}
	projectedBytes, err := Encode(projected)
	if err != nil {
		t.Fatalf("historical projection is not a valid current manifest: %v", err)
	}
	if reparsed, parseErr := Parse(projectedBytes, Digest(projectedBytes)); parseErr != nil || !reflect.DeepEqual(reparsed, projected) {
		t.Fatalf("historical projection current roundtrip=%#v err=%v", reparsed, parseErr)
	}

	projected.Providers[0].VerificationProfiles[0] = "mutated"
	projected.HistoricalDecode.ModelIR[0] = 99
	again, err := ParseHistorical(encoded, releasedManifestV1SHA256)
	if err != nil || again.Providers[0].VerificationProfiles[0] != "c" || !reflect.DeepEqual(again.HistoricalDecode.ModelIR, []uint16{1}) {
		t.Fatalf("historical projection aliases caller mutation: %#v err=%v", again, err)
	}
}

func TestCompatibilityManifestV2IsCurrentOnlyAndHistoricalEntryAcceptsCurrent(t *testing.T) {
	if FormatVersion != 2 {
		t.Fatalf("compatibility manifest current format=%d; want 2", FormatVersion)
	}
	value := compatibilityFixture()
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	active, err := Parse(encoded, Digest(encoded))
	if err != nil || !reflect.DeepEqual(active, value) {
		t.Fatalf("active v2 roundtrip=%#v err=%v", active, err)
	}
	historical, err := ParseHistorical(encoded, Digest(encoded))
	if err != nil || !reflect.DeepEqual(historical, value) {
		t.Fatalf("historical entry current-v2 roundtrip=%#v err=%v", historical, err)
	}
}

func TestHistoricalManifestV1RejectsFutureRelabelNullDuplicateUnknownTrailingAndNoncanonical(t *testing.T) {
	released := releasedManifestV1Bytes(t)
	current, err := Encode(compatibilityFixture())
	if err != nil {
		t.Fatal(err)
	}
	relabelledCurrent := bytes.Replace(current, []byte(`"formatVersion": 2`), []byte(`"formatVersion": 1`), 1)
	tests := []struct {
		name   string
		bytes  []byte
		digest string
		reason Reason
	}{
		{name: "untrusted", bytes: released, digest: Digest([]byte("different")), reason: ReasonUntrustedDigest},
		{name: "future", bytes: bytes.Replace(released, []byte(`"formatVersion": 1`), []byte(`"formatVersion": 3`), 1), reason: ReasonUnsupportedFormat},
		{name: "current relabelled v1", bytes: relabelledCurrent, reason: ReasonInvalidEncoding},
		{name: "historical GraphQL smuggled into v1", bytes: bytes.Replace(released, []byte(`    "modelIR": [`), []byte("    \"graphQL\": [\n      4\n    ],\n    \"modelIR\": ["), 1), reason: ReasonInvalidEncoding},
		{name: "null required actions", bytes: bytes.Replace(released, []byte(`"requiredActions": []`), []byte(`"requiredActions": null`), 1), reason: ReasonInvalidManifest},
		{name: "null historical decode", bytes: replaceJSONObjectWithNull(t, released, "historicalDecode"), reason: ReasonInvalidManifest},
		{name: "duplicate module", bytes: bytes.Replace(released, []byte(`  "module":`), []byte("  \"module\": \"github.com/eleven-am/golem/go\",\n  \"module\":"), 1), reason: ReasonNoncanonical},
		{name: "unknown root", bytes: bytes.Replace(released, []byte(`  "module":`), []byte("  \"future\": true,\n  \"module\":"), 1), reason: ReasonInvalidEncoding},
		{name: "trailing", bytes: append(append([]byte(nil), released...), []byte("{}\n")...), reason: ReasonInvalidEncoding},
		{name: "noncanonical whitespace", bytes: bytes.Replace(released, []byte(`  "module":`), []byte(`    "module":`), 1), reason: ReasonNoncanonical},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			digest := test.digest
			if digest == "" {
				digest = Digest(test.bytes)
			}
			_, err := ParseHistorical(test.bytes, digest)
			if actual := reasonOf(err); actual != test.reason {
				t.Fatalf("reason=%q want=%q err=%v", actual, test.reason, err)
			}
		})
	}
}

func releasedManifestV1Bytes(t *testing.T) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve historical manifest fixture")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", "go-v0.0.2-compatibility-manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func reasonOf(err error) Reason {
	reason, _ := CodeOf(err)
	return reason
}

func replaceJSONObjectWithNull(t *testing.T, encoded []byte, name string) []byte {
	t.Helper()
	needle := []byte(`  "` + name + `": {`)
	start := bytes.Index(encoded, needle)
	if start < 0 {
		t.Fatalf("object %s is absent", name)
	}
	open := start + len(needle) - 1
	depth := 0
	end := -1
	for index := open; index < len(encoded); index++ {
		switch encoded[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = index + 1
				index = len(encoded)
			}
		}
	}
	if end < 0 {
		t.Fatalf("object %s is unterminated", name)
	}
	result := append([]byte(nil), encoded[:start]...)
	result = append(result, []byte(`  "`+name+`": null`)...)
	result = append(result, encoded[end:]...)
	return result
}
