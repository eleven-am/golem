package p8mutation

import "time"

// optimisticConcurrencyMigrationHistoryMutations is intentionally isolated
// from Catalog until the coordinated compatibility publication is complete.
func optimisticConcurrencyMigrationHistoryMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/migration", Test: test}
	}
	mutation := func(label, summary, before, after, test string) Mutation {
		return Mutation{
			Label: label, Summary: summary,
			Patches: []Patch{{Path: "go/internal/migration/canonical.go", Before: before, After: after}},
			Gate:    gate(test), Timeout: 2 * time.Minute,
		}
	}
	return []Mutation{
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_CHANGES_DOMAIN",
			"change the released migration-entry-v1 domain separator",
			"out.WriteString(\"golem:migration-entry:v1\\x00\")",
			"out.WriteString(\"golem:migration-entry:v2\\x00\")",
			"TestHistoricalMigrationEntryV1ReproducesReleasedSocialChains",
		),
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_OMITS_OPERATION_KIND",
			"remove a selected nested operation field from the frozen projection",
			"\"ID\", \"Kind\", \"Stage\", \"ObjectID\", \"Before\", \"After\", \"Dependencies\", \"Capabilities\", \"Mode\", \"Risk\", \"Transform\", \"Resume\", \"LogicalPath\"",
			"\"ID\", \"Stage\", \"ObjectID\", \"Before\", \"After\", \"Dependencies\", \"Capabilities\", \"Mode\", \"Risk\", \"Transform\", \"Resume\", \"LogicalPath\"",
			"TestHistoricalMigrationEntryV1ProjectionShape",
		),
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_REOPENS_REFLECTION",
			"encode every current struct field instead of the frozen selected fields",
			"\t\tif !exists {\n\t\t\treturn fmt.Errorf(\"historical migration entry v1 projection is missing struct %s\", value.Type())\n\t\t}",
			"\t\tif !exists {\n\t\t\treturn fmt.Errorf(\"historical migration entry v1 projection is missing struct %s\", value.Type())\n\t\t}\n\t\tfields = make([]string, value.NumField())\n\t\tfor index := range fields {\n\t\t\tfields[index] = value.Type().Field(index).Name\n\t\t}",
			"TestHistoricalMigrationEntryV1FutureFieldsAreIgnoredOnlyWhileZero",
		),
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_ACCEPTS_FUTURE_AUTHORITY",
			"allow an unrepresented future migration field to carry authority",
			"if _, exists := selected[field.Name]; !exists && !value.Field(index).IsZero() {",
			"if _, exists := selected[field.Name]; false && !exists && !value.Field(index).IsZero() {",
			"TestHistoricalMigrationEntryV1FutureFieldsAreIgnoredOnlyWhileZero",
		),
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_SKIPS_PREV3_FACT_VALIDATION",
			"skip frozen physical validation before hashing a reviewed snapshot",
			"\t\tnormalized, err := physical.NormalizeHistorical(schema)\n\t\tif err != nil {\n\t\t\treturn fmt.Errorf(\"historical migration entry v1 physical snapshot: %w\", err)\n\t\t}",
			"\t\tnormalized := schema",
			"TestHistoricalMigrationEntryV1RejectsCurrentOnlyPreV3SnapshotFacts",
		),
		mutation(
			"CONCURRENCY_MIGRATION_ENTRY_OMITS_V3_CONCURRENCY_IDENTITY",
			"remove the v3 concurrency identity from migration-entry physical bytes",
			"\t\tphysicalProjection, err := physical.HistoricalStructFieldProjection(normalized)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}",
			"\t\tphysicalProjection, err := physical.HistoricalStructFieldProjection(normalized)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif normalized.Version == 3 {\n\t\t\tfields := physicalProjection[\"PhysicalTable\"]\n\t\t\tphysicalProjection[\"PhysicalTable\"] = append(fields[:2], fields[3:]...)\n\t\t}",
			"TestHistoricalMigrationEntryV1CommitsValidV3ConcurrencyIdentity",
		),
		{
			Label:   "CONCURRENCY_MIGRATION_PREVIEW_REHASHES_HISTORICAL_MODEL",
			Summary: "replace the frozen reviewed model-head fingerprint with a hash of its current projection",
			Patches: []Patch{{
				Path:   "go/internal/migration/workflow/preview.go",
				Before: "\treturn state.HeadModelFingerprint, nil",
				After:  "\treturn ir.ModelFingerprint(beforeModel)",
			}},
			Gate:    Gate{Directory: "go", Package: "./internal/migration/workflow", Test: "TestCheckedSocialProspectivePhysicalV3UsesFrozenModelHeadFingerprint"},
			Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_MIGRATION_NEW_REENCODES_HISTORICAL_MODEL",
			Summary: "regenerate an incremental before-model snapshot with the mutable current encoder",
			Patches: []Patch{{
				Path:   "go/internal/migration/workflow/workflow.go",
				Before: "\tbeforeModelBytes, err := reviewedBeforeModelSnapshotBytes(state, root, headLength)",
				After:  "\tbeforeModelBytes, err := modelSnapshotBytes(beforeModel)",
			}},
			Gate:    Gate{Directory: "go", Package: "./internal/migration/workflow", Test: "TestCheckedSocialPrepareNewCopiesExactReviewedModelHeadBytesAndReloads"},
			Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_MIGRATION_BACKFILL_REENCODES_HISTORICAL_MODEL",
			Summary: "regenerate a backfill before-model snapshot with the mutable current encoder",
			Patches: []Patch{{
				Path:   "go/internal/migration/workflow/backfill.go",
				Before: "\treturn reviewedBeforeModelSnapshotBytes(state, root, len(history.Manifest.Entries)-1)",
				After:  "\treturn modelSnapshotBytes(*state.HeadModel)",
			}},
			Gate:    Gate{Directory: "go", Package: "./internal/migration/workflow", Test: "TestCheckedSocialPendingBackfillBindsFrozenReviewedHeadAndExactBytes"},
			Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_MIGRATION_BACKFILL_TRUSTS_UNBOUND_BEFORE_MODEL",
			Summary: "accept pending before-model contents that differ from the reviewed parent head",
			Patches: []Patch{{
				Path:   "go/internal/migration/workflow/backfill.go",
				Before: "if pendingErr != nil || reviewedErr != nil || !bytes.Equal(pendingModel, reviewedModel) {",
				After:  "if pendingErr != nil || reviewedErr != nil || false && !bytes.Equal(pendingModel, reviewedModel) {",
			}},
			Gate:    Gate{Directory: "go", Package: "./internal/migration/workflow", Test: "TestCheckedSocialPendingBackfillBindsFrozenReviewedHeadAndExactBytes"},
			Timeout: 2 * time.Minute,
		},
	}
}
