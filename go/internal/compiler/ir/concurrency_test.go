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

func TestOptimisticConcurrencyContractFormatIsV6(t *testing.T) {
	if OptimisticConcurrencyContractFormatVersionRequired != 6 {
		t.Fatalf("required optimistic concurrency ContractIR version = %d, want reviewed v6", OptimisticConcurrencyContractFormatVersionRequired)
	}
	if ContractFormatVersion != OptimisticConcurrencyContractFormatVersionRequired {
		t.Fatalf("current ContractIR version = %d, want coordinated v6", ContractFormatVersion)
	}
}

func TestContractV5HistoricalDecoderPreservesExactReleasedBytesAndFingerprint(t *testing.T) {
	const (
		payload         = `{"formatVersion":5,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`
		wantFingerprint = Fingerprint("2d7094f2f88762e3129e946cdbf9f32b8734dd86035bbd3c753097146c232990")
	)
	contract, err := CanonicalDecodeContractV5([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if contract.FormatVersion != ContractFormatVersion || contract.GraphQLABIVersion != 4 || len(contract.Models) != 0 {
		t.Fatalf("decoded v5 contract = %#v", contract)
	}
	if got, err := ContractFingerprintV5([]byte(payload)); err != nil || got != wantFingerprint {
		t.Fatalf("released v5 fingerprint changed: got %s want %s err=%v", got, wantFingerprint, err)
	}
	if _, err := decodeCurrentContractCanonicalFraming([]byte(payload)); err == nil || !strings.Contains(err.Error(), "expected 6") {
		t.Fatalf("current framing seam accepted v5: %v", err)
	}
}

func TestContractV6CanonicalEmptyBytesAndFingerprintAreExact(t *testing.T) {
	const (
		wantBytes       = `{"formatVersion":6,"graphqlAbiVersion":5,"models":[],"enums":[],"methods":[],"customOperations":[]}`
		wantFingerprint = Fingerprint("6736c667e443dcfaf42cee3119cf431798b3e554f11945a04ce74109dea6e2d8")
	)
	contract := ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 5}
	encoded, err := CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != wantBytes {
		t.Fatalf("canonical v6 bytes changed:\n got %s\nwant %s", encoded, wantBytes)
	}
	fingerprint, err := ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != wantFingerprint {
		t.Fatalf("canonical v6 fingerprint changed: got %s want %s", fingerprint, wantFingerprint)
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

func TestContractDecodersAreVersionExactAndHistoricalV5CannotConsumeV6Projection(t *testing.T) {
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
	if _, err := CanonicalDecodeContractV5(v6); err == nil || !strings.Contains(err.Error(), "expected 5") {
		t.Fatalf("historical v5 decoder accepted v6: %v", err)
	}

	relabelled := bytes.Replace(v6, []byte(`"formatVersion":6`), []byte(`"formatVersion":5`), 1)
	if _, err := CanonicalDecodeContractV5(relabelled); err == nil || !strings.Contains(err.Error(), "optimisticConcurrency") {
		t.Fatalf("historical v5 decoder reinterpreted a relabelled v6 projection: %v", err)
	}
	for _, payload := range [][]byte{
		[]byte(`{"formatVersion":99,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`),
		[]byte(`{"formatVersion":5,"formatVersion":6,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`),
		append(append([]byte(nil), v6...), '\n'),
	} {
		if _, err := decodeCurrentContractCanonicalFraming(payload); err == nil {
			t.Fatalf("current framing seam accepted unknown/mixed/noncanonical payload %s", payload)
		}
		if _, err := CanonicalDecodeContractV5(payload); err == nil {
			t.Fatalf("historical decoder accepted unknown/mixed/noncanonical payload %s", payload)
		}
	}
}

func TestContractV5HistoricalDecoderValidatesWithFrozenV5Rules(t *testing.T) {
	const canonical = `{"formatVersion":5,"graphqlAbiVersion":4,"models":[{"modelId":"10000000000000000000000000000000","graphqlName":"Post","graphqlPlural":"Posts","roots":{"findOne":"post","findMany":"posts","create":"createPost","update":"updatePost","upsert":"upsertPost","delete":"deletePost","updateMany":"updateManyPosts","deleteMany":"deleteManyPosts","aggregate":"aggregatePosts","groupBy":"groupByPosts","relationGroupBy":"relationGroupByPosts","events":"postEvents"},"fields":[{"fieldId":"12000000000000000000000000000000","graphqlName":"version","modes":["visible"]}],"hookOwnedCreateFields":[],"selectors":[],"operations":["findOne"],"subscriptions":false,"scopedReads":false,"limits":{},"computed":[],"exposed":true}],"enums":[],"methods":[],"customOperations":[]}`
	if _, err := CanonicalDecodeContractV5([]byte(canonical)); err != nil {
		t.Fatalf("exact v5 payload failed frozen validation: %v", err)
	}
	modelStart := strings.Index(canonical, `{"modelId"`)
	modelEnd := strings.Index(canonical, `}],"enums"`)
	if modelStart < 0 || modelEnd < 0 {
		t.Fatal("v5 fixture does not expose its exact model boundary")
	}
	duplicateModel := canonical[:modelEnd] + `},` + canonical[modelStart:modelEnd+1] + canonical[modelEnd+1:]
	for name, payload := range map[string]string{
		"unknown root":    strings.Replace(canonical, `"graphqlAbiVersion":4`, `"future":true,"graphqlAbiVersion":4`, 1),
		"unknown nested":  strings.Replace(canonical, `"graphqlPlural":"Posts"`, `"graphqlPlural":"Posts","optimisticConcurrency":"12000000000000000000000000000000"`, 1),
		"open operation":  strings.Replace(canonical, `"operations":["findOne"]`, `"operations":["futureWrite"]`, 1),
		"duplicate model": duplicateModel,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalDecodeContractV5([]byte(payload)); err == nil {
				t.Fatalf("historical v5 decoder accepted %s", payload)
			}
		})
	}
}
