package p8mutation

import "time"

// optimisticConcurrencyContractMutations freezes the compiler/ContractIR
// boundary independently of the later physical, runtime, and generated slices.
// Every mutant is compiler-only and compiling.
func optimisticConcurrencyContractMutations() []Mutation {
	return []Mutation{
		{
			Label:   "CONCURRENCY_CONTRACT_OMITS_V6_PROJECTION",
			Summary: "omit the optimistic-concurrency projection from canonical ContractIR v6",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/canonical.go",
				Before: "\tfor index := range input.Models {\n\t\tif len(input.Models[index].Fields) == 0",
				After:  "\tfor index := range input.Models {\n\t\tinput.Models[index].OptimisticConcurrency = nil\n\t\tif len(input.Models[index].Fields) == 0",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestCanonicalContractSortsAndFingerprintsOptimisticConcurrencyProjection"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CURRENT_FRAMING_ACCEPTS_V5",
			Summary: "let the private current canonical-framing seam fall back to historical v5",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/contract_decode.go",
				Before: "\tif err := validateContractJSONEnvelope(payload, ContractFormatVersion); err != nil {\n\t\treturn ContractIR{}, err\n\t}",
				After:  "\tif err := validateContractJSONEnvelope(payload, ContractFormatVersion); err != nil {\n\t\tif historical, historicalErr := CanonicalDecodeContractV5(payload); historicalErr == nil { return historical, nil }\n\t\treturn ContractIR{}, err\n\t}",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestContractV5HistoricalDecoderPreservesExactReleasedBytesAndFingerprint"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_V5_DECODER_ACCEPTS_V6",
			Summary: "let the frozen v5 decoder fall back to current v6 canonical framing",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/historical_contract_v5.go",
				Before: "\tif err := validateContractV5JSONEnvelope(payload); err != nil {\n\t\treturn nil, err\n\t}",
				After:  "\tif err := validateContractV5JSONEnvelope(payload); err != nil {\n\t\tif _, currentErr := decodeCurrentContractCanonicalFraming(payload); currentErr == nil { return append([]byte(nil), payload...), nil }\n\t\treturn nil, err\n\t}",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestContractDecodersAreVersionExactAndHistoricalV5CannotConsumeV6Projection"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_V5_ROUTES_THROUGH_CURRENT_FRAMING",
			Summary: "require frozen v5 bytes to pass the mutable current v6 framing seam first",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/historical_contract_v5.go",
				Before: "func CanonicalDecodeContractV5(payload []byte) (ContractIR, error) {\n\tcanonical, err := decodeContractV5(payload)",
				After:  "func CanonicalDecodeContractV5(payload []byte) (ContractIR, error) {\n\tif _, err := decodeCurrentContractCanonicalFraming(payload); err != nil { return ContractIR{}, err }\n\tcanonical, err := decodeContractV5(payload)",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestContractV5HistoricalDecoderPreservesExactReleasedBytesAndFingerprint"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CURRENT_FRAMING_ACCEPTS_NONCANONICAL",
			Summary: "accept mixed or trailing noncanonical bytes as current ContractIR",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/contract_decode.go",
				Before: "\tif !bytes.Equal(canonical, payload) {",
				After:  "\tif false && !bytes.Equal(canonical, payload) {",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestContractDecodersAreVersionExactAndHistoricalV5CannotConsumeV6Projection"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CONTRACT_ACCEPTS_MODEL_DISAGREEMENT",
			Summary: "accept different optimistic-concurrency fields in ModelIR and ContractIR",
			Patches: []Patch{{
				Path:   "go/internal/compiler/concurrency/normalize.go",
				Before: "case modelField != nil && contractField != nil && *modelField != *contractField:",
				After:  "case modelField != nil && contractField != nil && false:",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/compile", Test: "TestValidateCompleteRejectsOptimisticConcurrencyContractProjectionDrift"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_EXPORTS_NONAUTHORITATIVE_CURRENT_FRAMING",
			Summary: "export the private non-authoritative current ContractIR framing seam",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/contract_decode.go",
				Before: "func decodeCurrentContractCanonicalFraming(payload []byte) (ContractIR, error) {",
				After:  "func CanonicalDecodeContract(payload []byte) (ContractIR, error) { return decodeCurrentContractCanonicalFraming(payload) }\n\nfunc decodeCurrentContractCanonicalFraming(payload []byte) (ContractIR, error) {",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/compiler/ir", Test: "TestCurrentContractCanonicalFramingIsPrivateAndHasNoAuthorityConsumer"}, Timeout: 2 * time.Minute,
		},
	}
}
