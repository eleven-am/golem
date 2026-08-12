package p8mutation

import "time"

// physicalV1V3UpgradeMutations remains isolated until the reviewed direct
// upgrade has completed its independent source review.
func physicalV1V3UpgradeMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/migration", Test: test}
	}
	return []Mutation{
		{
			Label: "PHYSICAL_V1_V3_UPGRADE_OMITS_DISPATCH", Summary: "refuse the retained direct upgrade instead of selecting its composition",
			Patches: []Patch{{Path: "go/internal/migration/diff.go",
				Before: "\tcase before.Version == 1 && before.CanonicalVersion == 1 && after.Version == 3 && after.CanonicalVersion == 3:\n\t\treturn diffHistoricalV1ToV3Composed(before, after)\n",
				After:  ""}},
			Gate: gate("TestHistoricalV1ToV3CompositionOwnsOnePublicationAndPreservesFrozenLegs"), Timeout: 2 * time.Minute,
		},
		{
			Label: "PHYSICAL_V1_V3_UPGRADE_SKIPS_V2_PROJECTION", Summary: "leave the before-derived intermediate at physical v1",
			Patches: []Patch{{Path: "go/internal/migration/historical_v1_to_v3_composition.go",
				Before: "\tprojected.Version, projected.CanonicalVersion = 2, 2",
				After:  "\tprojected.Version, projected.CanonicalVersion = 1, 1"}},
			Gate: gate("TestHistoricalV1HeadProjectionReproducesPublishedV2BoundaryExactly"), Timeout: 2 * time.Minute,
		},
		{
			Label: "PHYSICAL_V1_V3_UPGRADE_RETAINS_LEG_PUBLICATIONS", Summary: "retain each frozen leg schema-version operation inside the single reviewed entry",
			Patches: []Patch{{Path: "go/internal/migration/historical_v1_to_v3_composition.go",
				Before: "\t\tif operation.Kind == RecordSchemaVersion {",
				After:  "\t\tif false && operation.Kind == RecordSchemaVersion {"}},
			Gate: gate("TestHistoricalV1ToV3CompositionOwnsOnePublicationAndPreservesFrozenLegs"), Timeout: 2 * time.Minute,
		},
		{
			Label: "PHYSICAL_V1_V3_UPGRADE_INTERLEAVES_FROZEN_LEGS", Summary: "remove the explicit dependency boundary between the two frozen planner legs",
			Patches: []Patch{{Path: "go/internal/migration/historical_v1_to_v3_composition.go",
				Before: "\t\tsecondOperations[index].Dependencies = append(secondOperations[index].Dependencies, firstLeaves...)",
				After:  "\t\tsecondOperations[index].Dependencies = append(secondOperations[index].Dependencies, firstLeaves[:0]...)"}},
			Gate: gate("TestHistoricalV1ToV3CompositionOrdersNonemptyFrozenLegs"), Timeout: 2 * time.Minute,
		},
		{
			Label: "PHYSICAL_V1_V3_UPGRADE_RENDERER_FORGETS_REPRESENTATION_LEG", Summary: "recognize retained text-to-varchar representation only in standalone v1-to-v2 entries",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/migrate.go",
				Before: "\treturn after.Version == 2 && after.CanonicalVersion == 2 || after.Version == 3 && after.CanonicalVersion == 3",
				After:  "\treturn after.Version == 2 && after.CanonicalVersion == 2"}},
			Gate: Gate{Directory: "go", Package: "./internal/provider/postgresql", Test: "TestHistoricalV1ToV3PostgreSQLRendererRetainsExactV1RepresentationLeg"}, Timeout: 2 * time.Minute,
		},
	}
}
