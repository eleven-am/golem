package p8mutation

import "time"

// compatibilityManifestHistoryMutations is intentionally isolated from the
// global catalog until the compatibility-manifest v2 publication is complete.
func compatibilityManifestHistoryMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/compatibility", Test: test}
	}
	mutation := func(label, summary, path, before, after, test string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: path, Before: before, After: after}}, Gate: gate(test), Timeout: 2 * time.Minute}
	}
	return []Mutation{
		mutation("COMPATIBILITY_MANIFEST_CURRENT_RELABELED_V1", "emit the active manifest with the released v1 label", "go/internal/compatibility/manifest.go",
			"\treturn append(encoded, '\\n'), nil", "\tencoded = bytes.Replace(encoded, []byte(`\"formatVersion\": 2`), []byte(`\"formatVersion\": 1`), 1)\n\treturn append(encoded, '\\n'), nil", "TestCompatibilityManifestV2IsCurrentOnlyAndHistoricalEntryAcceptsCurrent"),
		mutation("COMPATIBILITY_MANIFEST_ACTIVE_ACCEPTS_V1", "let the active parser fall back to historical v1", "go/internal/compatibility/manifest.go",
			"\treturn parseCurrentManifest(encoded)\n}", "\tif historical, historicalErr := ParseHistorical(encoded, expectedDigest); historicalErr == nil {\n\t\treturn historical, nil\n\t}\n\treturn parseCurrentManifest(encoded)\n}", "TestHistoricalManifestV1ExactReleasedBytesProjectToCurrent"),
		mutation("COMPATIBILITY_MANIFEST_V1_ACCEPTS_CURRENT_GRAPHQL_MEMBER", "add the v2 GraphQL history member to the frozen v1 DTO", "go/internal/compatibility/historical_manifest_v1.go",
			"\tGeneratedManifests      []uint16 `json:\"generatedManifests\"`\n\tModelIR", "\tGeneratedManifests      []uint16 `json:\"generatedManifests\"`\n\tGraphQL                 []uint16 `json:\"graphQL\"`\n\tModelIR", "TestHistoricalManifestV1RejectsFutureRelabelNullDuplicateUnknownTrailingAndNoncanonical"),
		mutation("COMPATIBILITY_MANIFEST_V1_OMITS_CURRENT_ONLY_GRAPHQL_PROJECTION", "project absent v1 GraphQL history as empty instead of released current-only", "go/internal/compatibility/historical_manifest_v1.go",
			"GraphQL: []uint16{value.Versions.GraphQL}", "GraphQL: []uint16{}", "TestHistoricalManifestV1ExactReleasedBytesProjectToCurrent"),
		mutation("COMPATIBILITY_MANIFEST_V1_SKIPS_CANONICAL_BYTES", "accept noncanonical v1 JSON after semantic validation", "go/internal/compatibility/historical_manifest_v1.go",
			"\tif !bytes.Equal(canonical, encoded) {", "\tif false && !bytes.Equal(canonical, encoded) {", "TestHistoricalManifestV1RejectsFutureRelabelNullDuplicateUnknownTrailingAndNoncanonical"),
		{
			Label:   "COMPATIBILITY_MANIFEST_V1_ACCEPTS_NULL_EMPTY_INVENTORY",
			Summary: "treat explicit null as an empty v1 required-action inventory",
			Patches: []Patch{
				{Path: "go/internal/compatibility/historical_manifest_v1.go", Before: " || value.RequiredActions == nil || value.KnownBoundaries == nil {", After: " || value.KnownBoundaries == nil {"},
				{Path: "go/internal/compatibility/historical_manifest_v1.go", Before: "\tif values == nil || len(values) == 0 && !emptyOK {", After: "\tif false && values == nil || len(values) == 0 && !emptyOK {"},
			},
			Gate: gate("TestHistoricalManifestV1RejectsFutureRelabelNullDuplicateUnknownTrailingAndNoncanonical"), Timeout: 2 * time.Minute,
		},
		mutation("COMPATIBILITY_MANIFEST_V1_SKIPS_TRUST_DIGEST", "accept v1 bytes without the separately trusted digest", "go/internal/compatibility/historical_manifest_v1.go",
			"\tif !validDigest(expectedDigest) || digest(encoded) != expectedDigest {", "\tif false && (!validDigest(expectedDigest) || digest(encoded) != expectedDigest) {", "TestHistoricalManifestV1RejectsFutureRelabelNullDuplicateUnknownTrailingAndNoncanonical"),
		mutation("COMPATIBILITY_MANIFEST_V1_REUSES_CURRENT_VALIDATOR", "route the frozen v1 DTO through mutable current validation", "go/internal/compatibility/historical_manifest_v1.go",
			"\tif !validHistoricalManifestV1(value) {", "\tif validate(projectHistoricalManifestV1(value)) != nil {", "TestHistoricalManifestV1RetainedAdaptationProvenance"),
		mutation("COMPATIBILITY_MANIFEST_V1_MISPROJECTS_OBSERVATION_DIGEST", "project the v1 CLI digest as its observation digest", "go/internal/compatibility/historical_manifest_v1.go",
			"Observation: value.Digests.Observation", "Observation: value.Digests.CLIJSON", "TestHistoricalManifestV1ExactReleasedBytesProjectToCurrent"),
	}
}
