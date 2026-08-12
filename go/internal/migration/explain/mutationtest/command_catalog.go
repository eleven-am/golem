package mutationtest

import (
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

// CommandCatalog isolates the standalone read-only boundary from the core
// report and adapter catalogs. Every mutant compiles and is owned by one exact
// command acceptance gate.
func CommandCatalog() []p8mutation.Mutation {
	gate := func(test string) p8mutation.Gate {
		return p8mutation.Gate{Directory: "go", Package: "./cmd/golem", Test: test}
	}
	mutation := func(label, summary string, patches []p8mutation.Patch, test string) p8mutation.Mutation {
		return p8mutation.Mutation{Label: label, Summary: summary, Patches: patches, Gate: gate(test), Timeout: 3 * time.Minute}
	}
	const command = "go/cmd/golem/migration_plan_command.go"
	return []p8mutation.Mutation{
		mutation("MIGRATION_PLAN_COMMAND_FILTERS_PROVIDER_BEFORE_COMPLETE_VALIDATION", "select PostgreSQL before validating its SQLite sibling", []p8mutation.Patch{
			{Path: command, Before: "\tproviderIDs, err := workflow.PublishedProviders(moduleDir, migrationRoot)\n\tif err != nil {\n\t\treturn explain.Report{}, err\n\t}\n\tproviders, err := reviewedMigrationProviders(providerIDs)\n\tif err != nil {\n\t\treturn explain.Report{}, err\n\t}\n\tstate, err := workflow.Load(ctx, moduleDir, migrationRoot, providers)\n", After: "\tstate, err := workflow.LoadReviewed(ctx, moduleDir, migrationRoot)\n\tproviderIDs := make([]ir.Provider, 0, len(state.Histories))\n\tfor providerID := range state.Histories { providerIDs = append(providerIDs, providerID) }\n"},
			{Path: command, Before: "\tfor _, providerID := range providerIDs {\n\t\thistory := state.Histories[providerID]\n", After: "\tfor _, providerID := range providerIDs {\n\t\tif providerID == ir.SQLite { continue }\n\t\thistory := state.Histories[providerID]\n"},
		}, "TestMigrationPlanReviewedVerifiesHistoryAndEveryArtifactBeforeRendering"),
		mutation("MIGRATION_PLAN_COMMAND_PROSPECTIVE_WRITES_MODULE", "write an application-tree artifact during prospective planning", []p8mutation.Patch{
			{Path: command, Before: "\t\"io\"\n\t\"regexp\"\n", After: "\t\"io\"\n\t\"os\"\n\t\"regexp\"\n"},
			{Path: command, Before: "\tpreviousModel, err := optionalMigrationHead(moduleDir, migrationRoot, false)\n", After: "\t_ = os.WriteFile(moduleDir+string(os.PathSeparator)+\".golem-migration-plan-mutant-output\", []byte(\"mutant\"), 0o600)\n\tpreviousModel, err := optionalMigrationHead(moduleDir, migrationRoot, false)\n"},
		}, "TestMigrationPlanProspectiveMatchesMigrationNewWithoutWriting"),
		mutation("MIGRATION_PLAN_COMMAND_PROSPECTIVE_LEAKS_TEMP", "leak an owned temporary node inside the application tree", []p8mutation.Patch{
			{Path: command, Before: "\t\"io\"\n\t\"regexp\"\n", After: "\t\"io\"\n\t\"os\"\n\t\"regexp\"\n"},
			{Path: command, Before: "\tpreviousModel, err := optionalMigrationHead(moduleDir, migrationRoot, false)\n", After: "\t_ = os.Mkdir(moduleDir+string(os.PathSeparator)+\".golem-migration-plan-mutant-temp\", 0o700)\n\tpreviousModel, err := optionalMigrationHead(moduleDir, migrationRoot, false)\n"},
		}, "TestMigrationPlanProspectiveMatchesMigrationNewWithoutWriting"),
		mutation("MIGRATION_PLAN_COMMAND_RENDERS_BEFORE_PROVIDER_SQL_VALIDATION", "render checksum-valid history before deterministic provider SQL verification", []p8mutation.Patch{{
			Path: command, Before: "\tproviderIDs, err := workflow.PublishedProviders(moduleDir, migrationRoot)\n\tif err != nil {\n\t\treturn explain.Report{}, err\n\t}\n\tproviders, err := reviewedMigrationProviders(providerIDs)\n\tif err != nil {\n\t\treturn explain.Report{}, err\n\t}\n\tstate, err := workflow.Load(ctx, moduleDir, migrationRoot, providers)\n", After: "\tstate, err := workflow.LoadReviewed(ctx, moduleDir, migrationRoot)\n\tproviderIDs := make([]ir.Provider, 0, len(state.Histories))\n\tfor providerID := range state.Histories { providerIDs = append(providerIDs, providerID) }\n",
		}}, "TestMigrationPlanReviewedVerifiesHistoryAndEveryArtifactBeforeRendering"),
		mutation("MIGRATION_PLAN_COMMAND_REVIEWED_FLAG_BYPASS", "accept schema and root compilation flags in reviewed mode", []p8mutation.Patch{{
			Path: command, Before: "\tif options.migrationExplicit && (options.migrationID == \"\" || options.schemaExplicit || options.rootExplicit) {\n", After: "\tif false && options.migrationExplicit && (options.migrationID == \"\" || options.schemaExplicit || options.rootExplicit) {\n",
		}}, "TestMigrationPlanRejectsTamperPendingDraftUnknownKindAndInvalidFlags"),
	}
}
