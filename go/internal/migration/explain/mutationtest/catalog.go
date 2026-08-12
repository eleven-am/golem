// Package mutationtest owns the executable compiling-mutant inventory for the
// accepted human-readable migration-plan contract. It is intentionally not
// part of the frozen P8 catalog.
package mutationtest

import (
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

const sourceBuilder = "go/internal/migration/explain/builder.go"
const sourceRenderer = "go/internal/migration/explain/render.go"
const sourceJSONCompatibility = "go/internal/migration/explain/json_compatibility.go"
const sourceCLICompatibilityGate = "go/cmd/golem/p8_compatibility_test.go"

func Catalog() []p8mutation.Mutation {
	gate := func(test string) p8mutation.Gate {
		return p8mutation.Gate{Directory: "go", Package: "./internal/migration/explain", Test: test}
	}
	orderEffect := gate("TestMigrationExplainMutationOrderAndEffectOracle")
	facts := gate("TestMigrationExplainMutationFactCompletenessOracle")
	guarantees := gate("TestMigrationExplainMutationGuaranteeAndClosedJSONOracle")
	privacy := gate("TestMigrationExplainMutationPrivacyAndValidationOracle")
	readOnly := gate("TestMigrationExplainMutationReadOnlyAndProviderValidationOracle")
	cliCompatibility := p8mutation.Gate{Directory: "go", Package: "./cmd/golem", Test: "TestP8CLIJSONAndPersistedFormatCompatibilityGate"}
	mutation := func(label, summary string, patches []p8mutation.Patch, target p8mutation.Gate) p8mutation.Mutation {
		return p8mutation.Mutation{Label: label, Summary: summary, Patches: patches, Gate: target, Timeout: 2 * time.Minute}
	}
	privacyLeak := func(label, summary, canary string) p8mutation.Mutation {
		return mutation(label, summary, []p8mutation.Patch{{
			Path:   sourceRenderer,
			Before: "\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(`{\"formatVersion\":1,\"mode\":`)\n",
			After:  "\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(" + quotedGoString(canary) + ")\n\twriter.write(`{\"formatVersion\":1,\"mode\":`)\n",
		}}, privacy)
	}

	return []p8mutation.Mutation{
		mutation("MIGRATION_EXPLAIN_REORDER_TYPED_OPERATIONS", "reverse the authoritative typed operation order", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tbuilt.operations = append(built.operations, value)\n",
			After:  "\t\tbuilt.operations = append([]Operation{value}, built.operations...)\n",
		}}, orderEffect),
		mutation("MIGRATION_EXPLAIN_LABEL_DATA_LOSS_SAFE", "label destructive removal as schema-only", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\t\treturn EffectValueDeleted, nil, nil\n\t\t}\n\tcase migration.AlterColumnType:\n",
			After:  "\t\t\treturn EffectSchemaOnly, nil, nil\n\t\t}\n\tcase migration.AlterColumnType:\n",
		}}, orderEffect),
		mutation("MIGRATION_EXPLAIN_LABEL_REWRITE_PRESERVING", "label a typed rewrite as value-preserving", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tcase preservationRewrite:\n\t\t\treturn EffectValueRewritten, nil, nil\n",
			After:  "\t\tcase preservationRewrite:\n\t\t\treturn EffectValuePreserving, nil, nil\n",
		}}, orderEffect),
		mutation("MIGRATION_EXPLAIN_LABEL_UNKNOWN_SAFE", "label an unproved known operation effect as value-preserving", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tcase preservationUnspecified:\n\t\t\treturn EffectUnknown, []Warning{WarningManualReview}, nil\n",
			After:  "\t\tcase preservationUnspecified:\n\t\t\treturn EffectValuePreserving, nil, nil\n",
		}}, orderEffect),
		mutation("MIGRATION_EXPLAIN_OMIT_APPROVAL", "discard exact approval facts", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tbefore: input.before, after: input.after, approvalRequired: input.approvalRequired,\n\t\tapprovalPresent: input.approvalPresent,\n",
			After:  "\t\tbefore: input.before, after: input.after, approvalRequired: false,\n\t\tapprovalPresent: false,\n",
		}}, facts),
		mutation("MIGRATION_EXPLAIN_OMIT_DEPENDENCY", "discard an authoritative operation dependency", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tresult.dependencies = append(result.dependencies, dependency)\n",
			After:  "\t\t_ = dependency\n",
		}}, facts),
		mutation("MIGRATION_EXPLAIN_OMIT_BACKFILL", "discard the reviewed backfill companion", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tresult.manual = &ManualCompanion{path: input.manual.path, sha256: input.manual.sha256, postcondition: input.manual.postcondition}\n",
			After:  "\t\t_ = input.manual\n",
		}}, facts),
		mutation("MIGRATION_EXPLAIN_OMIT_POSTCONDITION", "substitute the reviewed postcondition digest", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "postcondition: input.manual.postcondition",
			After:  "postcondition: input.manual.sha256",
		}}, facts),
		mutation("MIGRATION_EXPLAIN_CLAIM_ZERO_DOWNTIME", "invent a zero-downtime guarantee", []p8mutation.Patch{{
			Path:   sourceRenderer,
			Before: `"zeroDowntime":false`,
			After:  `"zeroDowntime":true`,
		}}, guarantees),
		mutation("MIGRATION_EXPLAIN_INVENT_DURATION", "invent a duration estimate", []p8mutation.Patch{{
			Path:   sourceRenderer,
			Before: `"durationEstimated":false`,
			After:  `"durationEstimated":true`,
		}}, guarantees),
		privacyLeak("MIGRATION_EXPLAIN_LEAK_RAW_SQL", "emit raw provider SQL", "CREATE TABLE physical_secret_table"),
		privacyLeak("MIGRATION_EXPLAIN_LEAK_BOUND_VALUE", "emit a bound migration value", "bound-secret-value"),
		privacyLeak("MIGRATION_EXPLAIN_LEAK_DSN", "emit a provider DSN", "postgresql://secret@localhost/private"),
		privacyLeak("MIGRATION_EXPLAIN_LEAK_ABSOLUTE_PATH", "emit an absolute filesystem path", "/Users/royossai/private.sql"),
		privacyLeak("MIGRATION_EXPLAIN_LEAK_PHYSICAL_NAME", "emit a physical table name", "physical_secret_table"),
		mutation("MIGRATION_EXPLAIN_RENDER_BEFORE_VALIDATION", "render an immutable report before validating its closed identity and display", []p8mutation.Patch{
			{Path: sourceRenderer, Before: "\tif err := validateReportStrings(report); err != nil {\n\t\treturn nil, err\n\t}\n\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(`{\"formatVersion\":1,\"mode\":`)\n", After: "\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(`{\"formatVersion\":1,\"mode\":`)\n"},
			{Path: sourceRenderer, Before: "\tif err := validateReportStrings(report); err != nil {\n\t\treturn nil, err\n\t}\n\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(\"Migration plan: \" + string(report.mode) + \"\\n\")\n", After: "\twriter := &boundedWriter{limit: maxEncodedBytes}\n\twriter.write(\"Migration plan: \" + string(report.mode) + \"\\n\")\n"},
		}, privacy),
		mutation("MIGRATION_EXPLAIN_ACCEPT_TEXT_CONTROL", "accept control characters that can inject forged lines into operator text", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\t\tif unicode.IsControl(character) || character == '\\u2028' || character == '\\u2029' {\n",
			After:  "\t\tif false && (unicode.IsControl(character) || character == '\\u2028' || character == '\\u2029') {\n",
		}}, gate("TestMigrationExplainCountsEveryClosedBoundDuringBuildAndEncoding")),
		mutation("MIGRATION_EXPLAIN_FILTER_BEFORE_PROVIDER_VALIDATION", "filter to the first provider before validating every declared provider", []p8mutation.Patch{{
			Path:   sourceBuilder,
			Before: "\tproviders := append([]providerInput(nil), input.providers...)\n",
			After:  "\tproviders := append([]providerInput(nil), input.providers[:1]...)\n",
		}}, readOnly),
		mutation("MIGRATION_EXPLAIN_PROSPECTIVE_WRITES", "write an application-tree artifact while building a prospective report", []p8mutation.Patch{
			{Path: sourceBuilder, Before: "\t\"go/token\"\n\t\"path\"\n", After: "\t\"go/token\"\n\t\"os\"\n\t\"path\"\n"},
			{Path: sourceBuilder, Before: "func buildReport(input reportInput) (Report, error) {\n", After: "func buildReport(input reportInput) (Report, error) {\n\t_ = os.WriteFile(\"golem-migration-plan-mutant-output\", []byte(\"mutant\"), 0o600)\n"},
		}, readOnly),
		mutation("MIGRATION_EXPLAIN_PROSPECTIVE_TEMP_LEAK", "leak an owned prospective temporary directory", []p8mutation.Patch{
			{Path: sourceBuilder, Before: "\t\"go/token\"\n\t\"path\"\n", After: "\t\"go/token\"\n\t\"os\"\n\t\"path\"\n"},
			{Path: sourceBuilder, Before: "func buildReport(input reportInput) (Report, error) {\n", After: "func buildReport(input reportInput) (Report, error) {\n\t_, _ = os.MkdirTemp(\"\", \"golem-migration-plan-mutant-\")\n"},
		}, readOnly),
		mutation("MIGRATION_EXPLAIN_UNVERSIONED_JSON", "emit the wrong public JSON format version", []p8mutation.Patch{{
			Path:   sourceRenderer,
			Before: "\twriter.write(`{\"formatVersion\":1,\"mode\":`)\n",
			After:  "\twriter.write(`{\"formatVersion\":2,\"mode\":`)\n",
		}}, guarantees),
		mutation("MIGRATION_EXPLAIN_OPEN_JSON", "append an undeclared open-ended JSON field", []p8mutation.Patch{{
			Path:   sourceRenderer,
			Before: "\twriter.write(`,\"guarantees\":{\"appliesChanges\":false,\"usesReviewedTypedPlan\":true,\"zeroDowntime\":false,\"durationEstimated\":false}}`)\n",
			After:  "\twriter.write(`,\"guarantees\":{\"appliesChanges\":false,\"usesReviewedTypedPlan\":true,\"zeroDowntime\":false,\"durationEstimated\":false},\"future\":true}`)\n",
		}}, guarantees),
		mutation("MIGRATION_EXPLAIN_DIVERGE_COMPATIBILITY_PROJECTION", "rename a frozen migration-plan wire field only in the compatibility projection", []p8mutation.Patch{{
			Path:   sourceJSONCompatibility,
			Before: "`json:\"beforePhysicalFingerprint\"`",
			After:  "`json:\"beforePhysicalFingerprintMutated\"`",
		}}, gate("TestMigrationExplainReportIsImmutableAndRenderersShareOneReport")),
		mutation("MIGRATION_EXPLAIN_HIDE_FORMAT_BUMP_FROM_CLI_INVENTORY", "change the migration-plan compatibility source format discriminator", []p8mutation.Patch{{
			Path:   sourceJSONCompatibility,
			Before: "return JSONCompatibilityDocument{FormatVersion: reportFormatVersion}",
			After:  "return JSONCompatibilityDocument{FormatVersion: reportFormatVersion + 1}",
		}}, cliCompatibility),
		mutation("MIGRATION_EXPLAIN_CHANGE_OPTIONAL_WIRE_FIELD", "make the optional migration-plan display field required in compatibility authority", []p8mutation.Patch{{
			Path:   sourceJSONCompatibility,
			Before: "`json:\"display,omitempty\"`",
			After:  "`json:\"display\"`",
		}}, cliCompatibility),
		mutation("MIGRATION_EXPLAIN_OMIT_CLI_COMPATIBILITY_SOURCE", "omit migration-plan from the CLI compatibility source inventory", []p8mutation.Patch{{
			Path:   sourceCLICompatibilityGate,
			Before: "\t\t{Name: \"migration-plan\", FormatVersion: migrationexplain.JSONCompatibilitySource().FormatVersion, Value: migrationexplain.JSONCompatibilitySource()},\n",
			After:  "",
		}}, cliCompatibility),
	}
}

func quotedGoString(value string) string {
	result := `"`
	for _, character := range value {
		switch character {
		case '\\', '"':
			result += `\` + string(character)
		default:
			result += string(character)
		}
	}
	return result + `"`
}
