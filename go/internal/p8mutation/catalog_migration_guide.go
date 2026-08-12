package p8mutation

import "time"

// migrationGuideMutations remains isolated from the global catalog until the
// compatibility-manifest v2 and release-guide publication is complete.
func migrationGuideMutations() []Mutation {
	gate := func(pkg, test string) Gate { return Gate{Directory: "go", Package: pkg, Test: test} }
	mutation := func(label, summary, path, before, after, pkg, test string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: path, Before: before, After: after}}, Gate: gate(pkg, test), Timeout: 15 * time.Minute}
	}
	return []Mutation{
		mutation("MIGRATION_GUIDE_MANIFEST_AUTHORITY_ABSENT", "allow a required guide action without manifest authority", "go/internal/compatibility/manifest.go",
			"guideRequired != (value.MigrationGuide != nil)", "false && guideRequired != (value.MigrationGuide != nil)", "./internal/compatibility", "TestManifestMigrationGuideAuthorityIsExactAndActionCoupled"),
		mutation("MIGRATION_GUIDE_TRUST_DIGEST_SKIPPED", "accept guide bytes without the manifest-bound digest", "go/internal/compatibility/migration_guide.go",
			"if !validDigest(expectedDigest) || digest(encoded) != expectedDigest {", "if false && (!validDigest(expectedDigest) || digest(encoded) != expectedDigest) {", "./internal/compatibility", "TestMigrationGuideRejectsAbsentTrustTamperRelabelNullUnknownDuplicateTrailingAndNoncanonical"),
		mutation("MIGRATION_GUIDE_FROM_TAG_RELABELED", "relabel the exact released source tag", "go/internal/compatibility/migration_guide.go",
			"MigrationGuidePath                 = \"compatibility/migration-guide-go-v0.0.2-to-v1.json\"", "MigrationGuidePath                 = \"compatibility/migration-guide-go-v0.0.3-to-v1.json\"", "./internal/compatibility", "TestGoV002ToV1MigrationGuideIsCanonicalTrustedAndTransitionBound"),
		mutation("MIGRATION_GUIDE_ACTION_ONLY_ACCEPTED", "accept guide authority after removing its required action", "go/internal/compatibility/manifest.go",
			"guideRequired != (value.MigrationGuide != nil)", "guideRequired && value.MigrationGuide == nil", "./internal/compatibility", "TestManifestMigrationGuideAuthorityIsExactAndActionCoupled"),
		mutation("MIGRATION_GUIDE_CORPUS_TREE_DRIFT", "replace the exact P7 corpus tree authority", "go/compatibility/migration-guide-go-v0.0.2-to-v1.json",
			"dce564e9e3aff0b0f96ae8b3278e75d588d2f71c", "ace564e9e3aff0b0f96ae8b3278e75d588d2f71c", "./internal/compatibility", "TestGoV002ToV1MigrationGuideIsCanonicalTrustedAndTransitionBound"),
		mutation("MIGRATION_GUIDE_V1_FUTURE_HISTORY_REJECTED", "hardcode manifest v2 authority to the first released guide path", "go/internal/compatibility/migration_guide.go",
			"return strings.HasPrefix(value.Path, \"compatibility/\") && safeGuidePath(value.Path)", "return value.Path == MigrationGuidePath && strings.HasPrefix(value.Path, \"compatibility/\") && safeGuidePath(value.Path)", "./internal/compatibility", "TestMigrationGuideV1AndManifestV2RemainGenericForFutureMajorTransition"),
	}
}
