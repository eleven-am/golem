package schema

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestHistoricalContractV7AdmissionPreservesVersionAndFingerprint(t *testing.T) {
	payload := []byte(`{"formatVersion":7,"graphqlAbiVersion":6,"models":[],"enums":[],"methods":[],"customOperations":[]}`)
	document := historicalContractDocument(7, payload, historicalContractDigest(payload))
	retained, err := decodeContract(document, true)
	if err != nil || retained.FormatVersion != 7 || retained.GraphQLABIVersion != 6 {
		t.Fatalf("retained contract=%+v error=%v", retained, err)
	}
	if _, err := decodeContract(document, false); err == nil {
		t.Fatal("active decoder admitted historical contract")
	}
	wrong := historicalContractDigest(payload)
	wrong[0] ^= 1
	if _, err := decodeContract(historicalContractDocument(7, payload, wrong), true); err == nil {
		t.Fatal("historical decoder accepted rewritten fingerprint")
	}
	if _, err := decodeContract(golem.GeneratedSchemaDocument(7, 2, historicalContractDigest(payload), payload), true); err == nil {
		t.Fatal("historical decoder accepted future canonical framing")
	}
}
