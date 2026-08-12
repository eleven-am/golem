package p8mutation

import "time"

// optimisticConcurrencyContractHistoryMutations is intentionally isolated
// from Catalog until the ContractIR-v6 compatibility inventory is published.
func optimisticConcurrencyContractHistoryMutations() []Mutation {
	const (
		irGate       = "TestContractV5HistoricalDecoderPreservesExactReleasedBytesAndFingerprint"
		frozenGate   = "TestHistoricalContractV5HasNoMutableCurrentDependencies"
		schemaGate   = "TestHistoricalContractV5IsOriginalFingerprintBoundAndActiveClosed"
		releasedGate = "TestHistoricalRegistryLoadsExactReleasedSocialV1V4PhysicalV1BundleOnly"
		v1Gate       = "TestHistoricalProviderDocumentV1DispatchesFrozenProfile"
		v3Gate       = "TestHistoricalProviderDocumentV3DispatchesFrozenProfile"
	)
	mutation := func(label, summary, path, before, after, pkg, test string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: path, Before: before, After: after}}, Gate: Gate{Directory: "go", Package: pkg, Test: test}, Timeout: 2 * time.Minute}
	}
	return []Mutation{
		mutation("CONCURRENCY_CONTRACT_V5_RETAINS_OLD_IN_MEMORY_VERSION", "leave validated v5 bytes labelled as v5 in current memory", "go/internal/compiler/ir/historical_contract_v5.go",
			"\tcurrent.FormatVersion = ContractFormatVersion", "\tcurrent.FormatVersion = historicalContractV5FormatVersion", "./internal/compiler/ir", irGate),
		mutation("CONCURRENCY_CONTRACT_V5_REUSES_MUTABLE_EXACT_JSON", "route frozen v5 roots through the mutable current exact-JSON helper", "go/internal/compiler/ir/historical_contract_v5.go",
			"\tif err := decodeExactContractV5JSON(payload, &decoded); err != nil {", "\tif err := decodeExactContractJSON(payload, &decoded); err != nil {", "./internal/compiler/ir", frozenGate),
		mutation("CONCURRENCY_CONTRACT_V5_ACCEPTS_CURRENT_ONLY_FIELD", "add the v6 concurrency projection to the retained v5 DTO", "go/internal/compiler/ir/historical_contract_v5.go",
			"\tHookOwnedCreateFields []FieldID         `json:\"hookOwnedCreateFields\"`", "\tOptimisticConcurrency *FieldID         `json:\"optimisticConcurrency,omitempty\"`\n\tHookOwnedCreateFields []FieldID         `json:\"hookOwnedCreateFields\"`", "./internal/policy/schema", schemaGate),
		mutation("CONCURRENCY_CONTRACT_V5_BOOTSTRAP_ROUTE_OMITTED", "remove ContractIR-v5 from historical registry bootstrap", "go/internal/policy/schema/bootstrap.go",
			"\tif historical && document.FormatVersion() == 5 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {", "\tif false && historical && document.FormatVersion() == 5 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {", "./internal/policy/schema", releasedGate),
		mutation("CONCURRENCY_CONTRACT_V5_ACTIVE_ROUTE_OPENS", "allow active registry bootstrap to consume ContractIR-v5", "go/internal/policy/schema/bootstrap.go",
			"\tif historical && document.FormatVersion() == 5 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {", "\tif document.FormatVersion() == 5 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {", "./internal/policy/schema", schemaGate),
		mutation("CONCURRENCY_CONTRACT_V5_SKIPS_ORIGINAL_FINGERPRINT", "accept a v5 contract whose outer fingerprint is forged", "go/internal/policy/schema/bootstrap.go",
			"\t\tfingerprint, err := compilerir.ContractFingerprintV5(document.Bytes())\n\t\tif err != nil {\n\t\t\treturn compilerir.ContractIR{}, fail(CodeFingerprint, \"contract\", \"%v\", err)\n\t\t}\n\t\tif fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {", "\t\tfingerprint, err := compilerir.ContractFingerprintV5(document.Bytes())\n\t\tif err != nil {\n\t\t\treturn compilerir.ContractIR{}, fail(CodeFingerprint, \"contract\", \"%v\", err)\n\t\t}\n\t\tif false && fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {", "./internal/policy/schema", schemaGate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_V2_ROUTES_CURRENT", "decode released physical-v2 bytes through the mutable current decoder", "go/internal/policy/schema/bootstrap.go",
			"\tcase 2:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV2(document.Bytes())", "\tcase 2:\n\t\tdecoded, err = physical.CanonicalDecode(document.Bytes())", "./internal/policy/schema", releasedGate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_SKIPS_ORIGINAL_FINGERPRINT", "accept a historical physical document whose outer fingerprint is forged", "go/internal/policy/schema/bootstrap.go",
			"\tif physicalFingerprint != physical.Digest(document.Fingerprint()) {", "\tif false && physicalFingerprint != physical.Digest(document.Fingerprint()) {", "./internal/policy/schema", releasedGate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_ACCEPTS_RELABELLED_V2", "decode physical-v2 bytes whose outer document claims v3", "go/internal/policy/schema/bootstrap.go",
			"\tcase 3:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV3(document.Bytes())", "\tcase 3:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV2(document.Bytes())", "./internal/policy/schema", releasedGate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_V1_ROUTE_BYPASSED", "route physical-v1 bytes through the retained v2 decoder", "go/internal/policy/schema/bootstrap.go",
			"\tcase 1:\n\t\tdecoded, err = physical.CanonicalDecodeHistorical(document.Bytes())", "\tcase 1:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV2(document.Bytes())", "./internal/policy/schema", v1Gate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_V3_ROUTES_CURRENT", "route physical-v3 bytes through the mutable current decoder", "go/internal/policy/schema/bootstrap.go",
			"\tcase 3:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV3(document.Bytes())", "\tcase 3:\n\t\tdecoded, err = physical.CanonicalDecode(document.Bytes())", "./internal/policy/schema", v3Gate),
		mutation("CONCURRENCY_HISTORICAL_PHYSICAL_SKIPS_SYSTEM_FINGERPRINT", "accept a historical provider document whose system fingerprint is forged", "go/internal/policy/schema/bootstrap.go",
			"\tif systemFingerprint != physical.Digest(system) {", "\tif false && systemFingerprint != physical.Digest(system) {", "./internal/policy/schema", releasedGate),
	}
}
