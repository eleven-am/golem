package ir

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func releasedContractV7Fixture(t *testing.T) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate fixture")
	}
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", "contract_v7_v088_p10operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestHistoricalContractV7ReleasedV088CanonicalBytesAndFingerprint(t *testing.T) {
	payload := releasedContractV7Fixture(t)
	contract, err := DecodeHistoricalContractV7(payload)
	if err != nil {
		t.Fatal(err)
	}
	if contract.FormatVersion != 7 || contract.GraphQLABIVersion != 6 || len(contract.Models) != 2 || len(contract.Methods) != 8 || len(contract.CustomOperations) != 19 || len(contract.TxOperations) != 2 {
		t.Fatalf("released contract projection lost structure: %+v", contract)
	}
	computed, events := 0, 0
	for _, model := range contract.Models {
		computed += len(model.Computed)
		if model.Event != nil {
			events++
		}
		if len(model.ScopeTemplates) != 0 || len(model.FactTemplates) != 0 {
			t.Fatal("historical projection manufactured policy templates")
		}
	}
	if computed != 5 || events != 2 || contract.DataIncarnation != nil || len(contract.GraphQLTypes) != 0 {
		t.Fatal("historical projection lost released members or invented current members")
	}
	fingerprint, err := FingerprintHistoricalContractV7(payload)
	if err != nil || fingerprint != "65d1ff937d319cc3acbf1790805832e85ad65a03fd73054be95190b622d2b4b8" {
		t.Fatalf("original released fingerprint=%s error=%v", fingerprint, err)
	}
	if _, err := CanonicalContract(contract); err == nil {
		t.Fatal("retained projection masquerades as active current contract")
	}
}

func TestHistoricalContractV7RetainsOptimisticHookOwnedAndOriginalABI(t *testing.T) {
	var frozen contractV7ContractIR
	if err := decodeExactContractJSON(releasedContractV7Fixture(t), &frozen); err != nil {
		t.Fatal(err)
	}
	field := frozen.Models[0].Fields[0].FieldID
	frozen.Models[0].OptimisticConcurrency = &field
	frozen.Models[0].HookOwnedCreateFields = []FieldID{field, field}
	// The released canonical owner did not rewrite or require a fixed ABI.
	frozen.GraphQLABIVersion = 5
	normalizeContractV7(&frozen)
	payload, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := DecodeHistoricalContractV7(payload)
	if err != nil {
		t.Fatal(err)
	}
	if projected.FormatVersion != 7 || projected.GraphQLABIVersion != 5 || projected.Models[0].OptimisticConcurrency == nil || *projected.Models[0].OptimisticConcurrency != field || len(projected.Models[0].HookOwnedCreateFields) != 1 || projected.Models[0].HookOwnedCreateFields[0] != field {
		t.Fatal("released fields or canonical hook de-duplication changed")
	}
}

func TestHistoricalContractV7RejectsRelabelledCurrentVocabularyAndMalformedBytes(t *testing.T) {
	original := releasedContractV7Fixture(t)
	for _, test := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"current root member", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`{"formatVersion":7`), []byte(`{"dataIncarnation":null,"formatVersion":7`), 1)
		}},
		{"current object vocabulary", func(p []byte) []byte {
			return append(append([]byte(nil), p[:len(p)-1]...), []byte(`,"graphqlTypes":[]}`)...)
		}},
		{"scope templates", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"hookOwnedCreateFields":`), []byte(`"scopeTemplates":[],"hookOwnedCreateFields":`), 1)
		}},
		{"fact templates", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"hookOwnedCreateFields":`), []byte(`"factTemplates":[],"hookOwnedCreateFields":`), 1)
		}},
		{"future graphql kind", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"kind":"scalar"`), []byte(`"kind":"object"`), 1)
		}},
		{"future operation", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"operations":[`), []byte(`"operations":["future",`), 1)
		}},
		{"nested unknown", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"roots":{`), []byte(`"roots":{"future":null,`), 1)
		}},
		{"duplicate", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"formatVersion":7`), []byte(`"formatVersion":7,"formatVersion":7`), 1)
		}},
		{"noncanonical whitespace", func(p []byte) []byte { return append([]byte(" "), p...) }},
		{"trailing value", func(p []byte) []byte { return append(p, []byte(` {}`)...) }},
		{"wrong format", func(p []byte) []byte {
			return bytes.Replace(p, []byte(`"formatVersion":7`), []byte(`"formatVersion":8`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := test.change(append([]byte(nil), original...))
			if bytes.Equal(payload, original) {
				t.Fatal("mutation did not change fixture")
			}
			if _, err := DecodeHistoricalContractV7(payload); err == nil {
				t.Fatal("invalid historical bytes accepted")
			}
			if _, err := FingerprintHistoricalContractV7(payload); err == nil {
				t.Fatal("invalid historical bytes fingerprinted")
			}
		})
	}
	for _, payload := range [][]byte{nil, []byte(strings.Repeat(" ", maxCanonicalContractBytes+1)), []byte(`{"formatVersion":7,"models":` + strings.Repeat("[", 1025) + strings.Repeat("]", 1025) + `}`)} {
		if _, err := DecodeHistoricalContractV7(payload); err == nil {
			t.Fatal("unbounded or absent historical document accepted")
		}
	}
	var frozen contractV7ContractIR
	if err := decodeExactContractJSON(original, &frozen); err != nil {
		t.Fatal(err)
	}
	frozen.Models[0], frozen.Models[1] = frozen.Models[1], frozen.Models[0]
	unordered, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeHistoricalContractV7(unordered); err == nil {
		t.Fatal("noncanonical model inventory accepted")
	}
}
