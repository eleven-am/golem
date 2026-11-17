package ir

import (
	"slices"
	"testing"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
)

func TestDataIncarnationClientRequirementsComposeWithInvalidation(t *testing.T) {
	for _, invalidation := range []bool{false, true} {
		for _, incarnation := range []bool{false, true} {
			contract := ContractIR{}
			version := BaseClientManifestVersion
			var capabilities []string
			if incarnation {
				contract.DataIncarnation = &DataIncarnationContractIR{Kind: "provider", Version: 1}
				version = PageClientManifestVersion
				capabilities = append(capabilities, dataincarnation.Capability)
			}
			if invalidation {
				contract.CustomOperations = []CustomOperationContractIR{{Invalidation: &CustomInvalidationIR{}}, {Invalidation: &CustomInvalidationIR{}}}
				version = PageClientManifestVersion
				capabilities = append(capabilities, invalidationcontract.Capability)
			}
			if got := contract.RequiredClientManifestVersion(); got != version {
				t.Fatalf("incarnation=%t invalidation=%t manifest=%d, want %d", incarnation, invalidation, got, version)
			}
			if got := contract.RequiredClientCapabilities(); !slices.Equal(got, capabilities) {
				t.Fatalf("incarnation=%t invalidation=%t capabilities=%v, want %v", incarnation, invalidation, got, capabilities)
			}
		}
	}
}
