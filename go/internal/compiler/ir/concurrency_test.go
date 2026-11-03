package ir

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalModelRetainsOptimisticConcurrencyStableFieldIdentity(t *testing.T) {
	fieldID := FieldID("12000000000000000000000000000000")
	model := CanonicalEmptyModel()
	model.Models = []ModelDeclIR{{
		ID:                    "10000000000000000000000000000000",
		Fields:                []FieldIR{{ID: fieldID, GoName: "Version", DeclarationOrder: 9, Kind: FieldScalar, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeInt64}}}},
		OptimisticConcurrency: &fieldID,
	}}
	encoded, err := CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"optimisticConcurrency":"12000000000000000000000000000000"`)) {
		t.Fatalf("canonical ModelIR omitted concurrency ownership: %s", encoded)
	}

	model.Models[0].Fields[0].GoName = "Revision"
	model.Models[0].Fields[0].DeclarationOrder = 1
	renameEncoded, err := CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(renameEncoded, []byte(`"optimisticConcurrency":"12000000000000000000000000000000"`)) {
		t.Fatalf("rename/reorder changed concurrency identity: %s", renameEncoded)
	}
}

func TestCanonicalContractSortsAndFingerprintsOptimisticConcurrencyProjection(t *testing.T) {
	fieldID := FieldID("12000000000000000000000000000000")
	first := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{
		{ModelID: "20000000000000000000000000000000", Fields: []FieldContractIR{}},
		{ModelID: "10000000000000000000000000000000", Fields: []FieldContractIR{
			{FieldID: "13000000000000000000000000000000", Modes: []FieldMode{ModeVisible}},
			{FieldID: fieldID, Modes: []FieldMode{ModeVisible}},
		}, OptimisticConcurrency: &fieldID},
	}}
	second := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{
		{ModelID: "10000000000000000000000000000000", Fields: []FieldContractIR{
			{FieldID: fieldID, Modes: []FieldMode{ModeVisible}},
			{FieldID: "13000000000000000000000000000000", Modes: []FieldMode{ModeVisible}},
		}, OptimisticConcurrency: &fieldID},
		{ModelID: "20000000000000000000000000000000", Fields: []FieldContractIR{}},
	}}
	firstEncoded, err := CanonicalContract(first)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, err := CanonicalContract(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstEncoded, secondEncoded) {
		t.Fatalf("projection canonicalization depends on declaration order:\n%s\n%s", firstEncoded, secondEncoded)
	}
	if !bytes.Contains(firstEncoded, []byte(`"optimisticConcurrency":"12000000000000000000000000000000"`)) {
		t.Fatalf("canonical ContractIR omitted projection: %s", firstEncoded)
	}
	withProjection, err := ContractFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	first.Models[1].OptimisticConcurrency = nil
	withoutProjection, err := ContractFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	if withProjection == withoutProjection {
		t.Fatal("ContractFingerprint ignored optimistic concurrency projection")
	}
}

func TestOptimisticConcurrencyContractFormatIsV6AndCurrentIsV7(t *testing.T) {
	if OptimisticConcurrencyContractFormatVersionRequired != 6 {
		t.Fatalf("required optimistic concurrency ContractIR version = %d, want reviewed v6", OptimisticConcurrencyContractFormatVersionRequired)
	}
	if ContractFormatVersion != 7 {
		t.Fatalf("current ContractIR version = %d, want v7", ContractFormatVersion)
	}
}

func TestContractV7CanonicalEmptyBytesAndFingerprintAreExact(t *testing.T) {
	const (
		wantBytes       = `{"formatVersion":7,"graphqlAbiVersion":5,"models":[],"enums":[],"methods":[],"customOperations":[]}`
		wantFingerprint = Fingerprint("3562ed2ecea99b14b957f0657bfb8856a0b25fd32ab473d5c9a0417e755feb3a")
	)
	contract := ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 5}
	encoded, err := CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != wantBytes {
		t.Fatalf("canonical v7 bytes changed:\n got %s\nwant %s", encoded, wantBytes)
	}
	fingerprint, err := ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != wantFingerprint {
		t.Fatalf("canonical v7 fingerprint changed: got %s want %s", fingerprint, wantFingerprint)
	}
}

func TestCurrentContractCanonicalFramingIsPrivateAndHasNoAuthorityConsumer(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	directory := filepath.Dir(current)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	productionReferences := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		productionReferences += bytes.Count(payload, []byte("decodeCurrentContractCanonicalFraming("))
		if bytes.Contains(payload, []byte("func CanonicalDecodeContract(")) {
			t.Fatalf("current ContractIR framing became an exported authority in %s", entry.Name())
		}
	}
	if productionReferences != 1 {
		t.Fatalf("private current framing production references = %d, want declaration only", productionReferences)
	}
}

func TestCurrentContractDecoderIsVersionExact(t *testing.T) {
	fieldID := FieldID("12000000000000000000000000000000")
	v6, err := CanonicalContract(ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 5, Models: []ModelContractIR{{
		ModelID:               "10000000000000000000000000000000",
		Fields:                []FieldContractIR{{FieldID: fieldID, Modes: []FieldMode{ModeVisible}}},
		OptimisticConcurrency: &fieldID,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCurrentContractCanonicalFraming(v6)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Models[0].OptimisticConcurrency == nil || *decoded.Models[0].OptimisticConcurrency != fieldID {
		t.Fatalf("current decoder omitted v6 projection: %#v", decoded.Models)
	}
	for _, payload := range [][]byte{
		[]byte(`{"formatVersion":99,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`),
		[]byte(`{"formatVersion":5,"formatVersion":6,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`),
		append(append([]byte(nil), v6...), '\n'),
	} {
		if _, err := decodeCurrentContractCanonicalFraming(payload); err == nil {
			t.Fatalf("current framing seam accepted unknown/mixed/noncanonical payload %s", payload)
		}
	}
}
