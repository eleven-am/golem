package p8mutation

import "time"

// historicalV1SchemaAgreementMutations is intentionally isolated from the
// global catalog until the frozen physical-v1 bootstrap seam is reviewed.
func historicalV1SchemaAgreementMutations() []Mutation {
	agreementGate := Gate{Directory: "go", Package: "./internal/policy/schema", Test: "TestHistoricalPhysicalV1PostgreSQLBoundedStringAgreementIsExact"}
	frozenGate := Gate{Directory: "go", Package: "./internal/policy/schema", Test: "TestHistoricalRegistryLoadsExactReleasedSocialV1V4PhysicalV1BundleOnly"}
	mutation := func(label, summary, before, after string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: "go/internal/policy/schema/bootstrap.go", Before: before, After: after}}, Gate: agreementGate, Timeout: 2 * time.Minute}
	}
	return []Mutation{
		mutation("HISTORICAL_V1_SCHEMA_AGREEMENT_ACCEPTS_V2_V3_TEXT", "apply the frozen physical-v1 PostgreSQL text representation to later formats",
			"schema.Version != 1 || schema.CanonicalVersion != 1", "schema.Version == 0 || schema.CanonicalVersion == 0"),
		mutation("HISTORICAL_V1_SCHEMA_AGREEMENT_OMITS_CHECK_PROOF", "accept bounded PostgreSQL text without the exact frozen max-length check",
			"return found == 1", "return found >= 0"),
		mutation("HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_FIELD", "ignore the frozen check expression field identity",
			" || !equalHistoricalV1Field(left.Column, right.Column)", ""),
		mutation("HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_LENGTH", "ignore the frozen check expression length literal",
			" || !equalHistoricalV1Literal(left.Literal, right.Literal)", ""),
		mutation("HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_OPERATOR", "ignore the frozen check expression operator and function symbols",
			" || !equalHistoricalV1Symbol(left.Symbol, right.Symbol)", ""),
		{
			Label: "HISTORICAL_V1_SCHEMA_AGREEMENT_USES_ACTIVE_SOCIAL", Summary: "route historical acceptance back to the mutable active social fixture",
			Patches: []Patch{{Path: "go/internal/policy/schema/historical_contract_v5_acceptance_test.go",
				Before: `filepath.Join(filepath.Dir(source), "..", "..", "compatibility", "testdata", "p7", "generated", "zz_golem_registry.gen.go")`,
				After:  `filepath.Join(filepath.Dir(source), "..", "..", "..", "runtime", "testdata", "p5social", "zz_golem_registry.gen.go")`}},
			Gate: frozenGate, Timeout: 2 * time.Minute,
		},
	}
}
