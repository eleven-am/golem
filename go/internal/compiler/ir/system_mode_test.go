package ir

import (
	"bytes"
	"strings"
	"testing"
)

func systemModeContractPayload(t *testing.T) []byte {
	t.Helper()
	payload, err := CanonicalContract(ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 5, Models: []ModelContractIR{{
		ModelID: "10000000000000000000000000000000",
		Fields: []FieldContractIR{
			{FieldID: "12000000000000000000000000000000", Modes: []FieldMode{ModeSystem}},
			{FieldID: "13000000000000000000000000000000", Modes: []FieldMode{ModeSystem, ModeImmutable}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestCurrentContractVersionCarriesSystemModeWithoutABump(t *testing.T) {
	payload := systemModeContractPayload(t)
	decoded, err := decodeCurrentContractCanonicalFraming(payload)
	if err != nil {
		t.Fatalf("current contract version rejected the system mode: %v", err)
	}
	if decoded.FormatVersion != ContractFormatVersion {
		t.Fatalf("format version = %d; want %d", decoded.FormatVersion, ContractFormatVersion)
	}
	if !HasMode(decoded.Models[0].Fields[0].Modes, ModeSystem) {
		t.Fatalf("system mode did not survive canonical round trip: %#v", decoded.Models[0].Fields)
	}
	if !HasMode(decoded.Models[0].Fields[1].Modes, ModeSystem) || !HasMode(decoded.Models[0].Fields[1].Modes, ModeImmutable) {
		t.Fatalf("system immutable modes did not survive canonical round trip: %#v", decoded.Models[0].Fields)
	}
}

func TestHistoricalV5ReplayRefusesTheSystemMode(t *testing.T) {
	payload, err := CanonicalContract(ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 4, Models: []ModelContractIR{{
		ModelID: "10000000000000000000000000000000",
		Fields:  []FieldContractIR{{FieldID: "12000000000000000000000000000000", Modes: []FieldMode{ModeSystem}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	relabelled := bytes.Replace(payload, []byte(`"formatVersion":6`), []byte(`"formatVersion":5`), 1)
	_, err = CanonicalDecodeContractV5(relabelled)
	if err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("historical v5 replay accepted the system mode: %v", err)
	}
}

func TestSystemModeDoesNotDisturbTheFingerprintOfSchemasWithoutIt(t *testing.T) {
	contract := ContractIR{FormatVersion: ContractFormatVersion, GraphQLABIVersion: 5, Models: []ModelContractIR{{
		ModelID: "10000000000000000000000000000000",
		Fields:  []FieldContractIR{{FieldID: "12000000000000000000000000000000", Modes: []FieldMode{ModeVisible}}},
	}}}
	payload, err := CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"formatVersion":6,"graphqlAbiVersion":5,"models":[{"modelId":"10000000000000000000000000000000","graphqlName":"","graphqlPlural":"","roots":{"findOne":"","findMany":"","create":"","update":"","upsert":"","delete":"","updateMany":"","deleteMany":"","aggregate":"","groupBy":"","relationGroupBy":"","events":""},"fields":[{"fieldId":"12000000000000000000000000000000","graphqlName":"","modes":["visible"]}],"hookOwnedCreateFields":[],"selectors":[],"operations":[],"subscriptions":false,"scopedReads":false,"limits":{},"computed":[],"exposed":false}],"enums":[],"methods":[],"customOperations":[]}`
	if string(payload) != want {
		t.Fatalf("canonical contract shape changed for a schema without any system field:\n%s", payload)
	}
}
