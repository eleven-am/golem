package schema

import (
	"crypto/sha256"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestHistoricalContractV4IsCanonicalFingerprintBoundAndActiveClosed(t *testing.T) {
	canonical := []byte(`{"formatVersion":4,"graphqlAbiVersion":3,"models":[],"enums":[],"methods":[],"customOperations":[]}`)
	document := historicalContractDocument(4, canonical, historicalContractDigest(canonical))
	decoded, err := decodeContract(document, true)
	if err != nil || decoded.FormatVersion != compilerir.ContractFormatVersion || decoded.GraphQLABIVersion != 3 || len(decoded.Models) != 0 {
		t.Fatalf("historical decode=%#v err=%v", decoded, err)
	}
	if _, err := decodeContract(document, false); err == nil {
		t.Fatal("active bundle path accepted historical contract v4")
	}

	mutations := []struct {
		name    string
		version uint32
		payload []byte
		digest  golem.SchemaDigest
	}{
		{name: "unknown-field", version: 4, payload: []byte(`{"formatVersion":4,"graphqlAbiVersion":3,"models":[],"enums":[],"methods":[],"customOperations":[],"foreign":true}`)},
		{name: "duplicate-field", version: 4, payload: []byte(`{"formatVersion":4,"graphqlAbiVersion":3,"models":[],"models":[],"enums":[],"methods":[],"customOperations":[]}`)},
		{name: "noncanonical-whitespace", version: 4, payload: []byte(" " + string(canonical))},
		{name: "attempted-hook-owned-synthesis", version: 4, payload: []byte(`{"formatVersion":4,"graphqlAbiVersion":3,"models":[{"modelId":"00000000000000000000000000000001","graphqlName":"Post","graphqlPlural":"Posts","roots":{},"fields":[],"hookOwnedCreateFields":[],"selectors":[],"operations":[],"subscriptions":false,"scopedReads":false,"limits":{},"computed":[],"exposed":true}],"enums":[],"methods":[],"customOperations":[]}`)},
		{name: "wrong-graphql-abi", version: 4, payload: []byte(`{"formatVersion":4,"graphqlAbiVersion":4,"models":[],"enums":[],"methods":[],"customOperations":[]}`)},
		{name: "v3-refusal", version: 3, payload: []byte(`{"formatVersion":3,"graphqlAbiVersion":2,"models":[],"enums":[],"methods":[],"customOperations":[]}`)},
		{name: "v6-refusal", version: 6, payload: []byte(`{"formatVersion":6,"graphqlAbiVersion":5,"models":[],"enums":[],"methods":[],"customOperations":[]}`)},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			digest := historicalContractDigest(mutation.payload)
			if mutation.digest != (golem.SchemaDigest{}) {
				digest = mutation.digest
			}
			if _, err := decodeContract(historicalContractDocument(mutation.version, mutation.payload, digest), true); err == nil {
				t.Fatal("historical decoder accepted incompatible contract")
			}
		})
	}
	wrong := historicalContractDigest(canonical)
	wrong[0] ^= 0xff
	if _, err := decodeContract(historicalContractDocument(4, canonical, wrong), true); err == nil {
		t.Fatal("historical decoder accepted wrong original fingerprint")
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
