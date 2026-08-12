package p8mutation

import "time"

// optimisticConcurrencyModelMutations is intentionally isolated from the
// release-wide catalog until the coordinated compatibility publication. These
// mutants exercise only the ModelIR v2/frozen-v1 persistence boundary.
func optimisticConcurrencyModelMutations() []Mutation {
	gate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test}
	}
	return []Mutation{
		{
			Label:   "CONCURRENCY_MODEL_CURRENT_FRAMING_ACCEPTS_V1",
			Summary: "let the current ModelIR-v2 framing seam fall back to released v1 bytes",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/model_decode.go",
				Before: "\tif err := validateModelJSONEnvelope(payload, ModelFormatVersion); err != nil {\n\t\treturn ModelIR{}, err\n\t}",
				After:  "\tif err := validateModelJSONEnvelope(payload, ModelFormatVersion); err != nil {\n\t\tif historical, historicalErr := CanonicalDecodeModelV1(payload); historicalErr == nil { return historical, nil }\n\t\treturn ModelIR{}, err\n\t}",
			}},
			Gate: gate("./internal/compiler/ir", "TestModelIRV2CurrentAndV1HistoricalBoundariesAreExact"), Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_MODEL_V1_ACCEPTS_V2_MEMBER",
			Summary: "add the current optimistic-concurrency member to the frozen ModelIR-v1 DTO",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/historical_model_v1.go",
				Before: "\tEqualityIndexes   []v1EqualityIndex `json:\"equalityIndexes\"`\n}",
				After:  "\tEqualityIndexes       []v1EqualityIndex `json:\"equalityIndexes\"`\n\tOptimisticConcurrency *FieldID         `json:\"optimisticConcurrency,omitempty\"`\n}",
			}},
			Gate: gate("./internal/compiler/ir", "TestModelV1HistoricalDecoderCannotConsumeCurrentOnlyFieldsEvenAtZeroValue"), Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_MODEL_V1_FINGERPRINTS_PROJECTED_V2",
			Summary: "fingerprint relabelled current bytes instead of the exact released ModelIR-v1 document",
			Patches: []Patch{{
				Path:   "go/internal/compiler/ir/historical_model_v1.go",
				Before: "\treturn fingerprint(\"golem:model-fingerprint:v1\", canonical), nil",
				After:  "\tprojected := bytes.Replace(canonical, []byte(`\"formatVersion\":1`), []byte(`\"formatVersion\":2`), 1)\n\treturn fingerprint(\"golem:model-fingerprint:v1\", projected), nil",
			}},
			Gate: gate("./internal/compiler/ir", "TestModelIRV2CurrentAndV1HistoricalBoundariesAreExact"), Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_POLICY_ACTIVE_ACCEPTS_MODEL_V1",
			Summary: "route active schema bundles through the historical ModelIR-v1 decoder",
			Patches: []Patch{{
				Path:   "go/internal/policy/schema/bootstrap.go",
				Before: "\tif historical && document.FormatVersion() == 1 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {",
				After:  "\tif document.FormatVersion() == 1 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {",
			}},
			Gate: gate("./internal/policy/schema", "TestHistoricalBundleRoutesModelV1ThroughFrozenDecoderOnly"), Timeout: 2 * time.Minute,
		},
	}
}
