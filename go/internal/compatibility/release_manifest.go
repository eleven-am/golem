package compatibility

import "strings"

// TrustedManifestSHA256 is compiled separately from compatibility/manifest.json.
// Release tooling and tests must use this trust root rather than a digest read
// from, or recomputed and accepted alongside, the artifact itself.
const TrustedManifestSHA256 = "59bd82177890ff594f053ab0cc06f4d1a0b15567d85e673ae6ca563602062c1c"

func DevelopmentManifest() Manifest {
	return Manifest{
		FormatVersion: FormatVersion,
		Module:        Module,
		Release: Release{
			Development: true,
			Version:     "devel",
			Tag:         "",
			Commit:      strings.Repeat("0", 40),
		},
		MinimumGoVersion: "1.25.0",
		Providers: []Provider{
			{Provider: "postgresql", MinimumVersion: "15.0.0", VerificationProfiles: []string{"c", "linguistic"}},
			{Provider: "sqlite", MinimumVersion: "3.38.0", VerificationProfiles: []string{"file", "named-shared-memory"}},
		},
		DeploymentProfiles: []string{"adapted-multi-process", "database-backed-single-process", "embedded-single-process"},
		Digests: Digests{
			PublicGoAPI:    PublicGoAPICorpusSHA256,
			GeneratedGoABI: GeneratedGoABICorpusSHA256,
			GraphQLABI:     GraphQLABICorpusSHA256,
			CLIJSON:        "3a9d2a922b5784438145fd116a00b2aee46eb9ed73e97784ba60c577172ed137",
			Observation:    "4a439f570a9561d587a22064ee3f7aa0040103624e303578149b5dec6d2e3cd5",
		},
		Versions: Versions{
			Generator: "p1-v1", GeneratedTemplateABI: "p8-go-abi-v5",
			SchemaBundle: 2, GeneratedManifest: 2, GraphQL: 4,
			ModelIR: 1, ContractIR: 5, CanonicalIR: 1,
			PhysicalSchema: 1, PhysicalCanonical: 1,
			MigrationManifest: 1, MigrationCanonical: 1, MigrationLedger: 1,
			EventSchema: 1,
			FactCodecs:  []string{"golem.fact.v1", "golem.fact.v2"},
			EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
		},
		HistoricalDecode: HistoricalDecode{
			SchemaBundles: []uint16{2}, GeneratedManifests: []uint16{1, 2},
			ModelIR: []uint16{1}, ContractIR: []uint16{4, 5}, CanonicalIR: []uint16{1},
			PhysicalSchema: []uint16{1}, PhysicalCanonical: []uint16{1},
			MigrationManifest: []uint16{1}, MigrationCanonical: []uint16{1}, MigrationLedger: []uint16{1},
			FactCodecs: []string{"golem.fact.v1", "golem.fact.v2"}, EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
		},
		RequiredActions: []string{},
		KnownBoundaries: []string{"cdc.requires-adapter", "mysql.unsupported"},
	}
}
