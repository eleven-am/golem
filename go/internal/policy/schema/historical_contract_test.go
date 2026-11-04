package schema

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestRetiredHistoricalContractFormatsAreRefusedWithTheDrainInstruction(t *testing.T) {
	const drain = "deliver or drain undelivered events before upgrading, then remove this bundle from HistoricalEventBundles"
	for _, test := range []struct {
		version uint32
		payload string
	}{
		{4, `{"formatVersion":4,"graphqlAbiVersion":3,"models":[],"enums":[],"methods":[],"customOperations":[]}`},
		{5, `{"formatVersion":5,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`},
	} {
		payload := []byte(test.payload)
		_, err := decodeContract(historicalContractDocument(test.version, payload, historicalContractDigest(payload)), true)
		want := fmt.Sprintf("historical contract format %d is not supported by this golem: %s", test.version, drain)
		if !isSchemaFailure(err, CodeDocument, "contract.formatVersion", want) {
			t.Fatalf("historical contract v%d error = %v, want %q", test.version, err, want)
		}
	}
}

func TestHistoricalContractAcceptsOnlyTheExactCurrentFormat(t *testing.T) {
	canonical := []byte(`{"formatVersion":7,"graphqlAbiVersion":5,"models":[],"enums":[],"methods":[],"customOperations":[]}`)
	document := historicalContractDocument(7, canonical, historicalContractDigest(canonical))
	if decoded, err := decodeContract(document, true); err != nil || decoded.FormatVersion != compilerir.ContractFormatVersion {
		t.Fatalf("historical path rejected exact current contract: decode=%#v err=%v", decoded, err)
	}
	future := []byte(`{"formatVersion":8,"graphqlAbiVersion":6,"models":[],"enums":[],"methods":[],"customOperations":[]}`)
	if _, err := decodeContract(historicalContractDocument(8, future, historicalContractDigest(future)), true); err == nil {
		t.Fatal("historical contract path accepted a future version")
	}
}

func historicalContractDocument(version uint32, payload []byte, digest golem.SchemaDigest) golem.SchemaDocument {
	return golem.GeneratedSchemaDocument(version, uint32(compilerir.CanonicalFormatVersion), digest, payload)
}

func historicalContractDigest(payload []byte) golem.SchemaDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:contract-fingerprint:v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	var result golem.SchemaDigest
	copy(result[:], hash.Sum(nil))
	return result
}
