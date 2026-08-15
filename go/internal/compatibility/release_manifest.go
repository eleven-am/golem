package compatibility

import (
	"strings"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

// TrustedManifestSHA256 is compiled separately from compatibility/manifest.json.
// Release tooling and tests must use this trust root rather than a digest read
// from, or recomputed and accepted alongside, the artifact itself.
const TrustedManifestSHA256 = "e4afe81c49a3c47fe44bdb63745b3720007c11502ba7f0334adae0384be58ad9"

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
			CLIJSON:        "a1228e10ba31fba966029245a85964bc0e49db85e55ac28774ea39a7ee5221df",
			Observation:    "914a495c1b7832491e7aa32a17e31efb82bf249d338de376c1a7608013132803",
		},
		Versions: Versions{
			Generator: codegenmanifest.GeneratorVersion, GeneratedTemplateABI: codegenmanifest.TemplateABIVersion,
			SchemaBundle: 2, GeneratedManifest: codegenmanifest.FormatVersion, GraphQL: graphqlcontract.ABIVersion,
			ModelIR: compilerir.ModelFormatVersion, ContractIR: compilerir.ContractFormatVersion, CanonicalIR: compilerir.CanonicalFormatVersion,
			PhysicalSchema: uint16(physical.SchemaFormatVersion), PhysicalCanonical: uint16(physical.CanonicalFormatVersion),
			MigrationManifest: 1, MigrationCanonical: 1, MigrationLedger: 1,
			EventSchema: 1,
			FactCodecs:  []string{"golem.fact.v1", "golem.fact.v2"},
			EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
		},
		HistoricalDecode: HistoricalDecode{
			SchemaBundles: []uint16{2}, GeneratedManifests: []uint16{1, 2},
			GraphQL: []uint16{4, 5}, ModelIR: []uint16{1, 2}, ContractIR: []uint16{4, 5, 6}, CanonicalIR: []uint16{1},
			PhysicalSchema: []uint16{1, 2, 3}, PhysicalCanonical: []uint16{1, 2, 3},
			MigrationManifest: []uint16{1}, MigrationCanonical: []uint16{1}, MigrationLedger: []uint16{1},
			FactCodecs: []string{"golem.fact.v1", "golem.fact.v2"}, EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
		},
		RequiredActions: []string{"migrate.database", "regenerate.generated"},
		KnownBoundaries: []string{"cdc.requires-adapter", "mysql.unsupported"},
	}
}
