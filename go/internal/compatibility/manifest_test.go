package compatibility

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompatibilityManifestStrictCanonicalTrustBoundary(t *testing.T) {
	value := compatibilityFixture()
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse(encoded, Digest(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Module != Module || decoded.Release != value.Release {
		t.Fatalf("decoded manifest = %#v", decoded)
	}

	tests := []struct {
		name   string
		bytes  []byte
		digest string
		reason Reason
	}{
		{name: "untrusted bytes", bytes: encoded, digest: strings.Repeat("f", 64), reason: ReasonUntrustedDigest},
		{name: "trailing value", bytes: append(append([]byte(nil), encoded...), []byte("{}\n")...), digest: "", reason: ReasonInvalidEncoding},
		{name: "noncanonical whitespace", bytes: bytes.Replace(encoded, []byte("  \"module\""), []byte("    \"module\""), 1), digest: "", reason: ReasonNoncanonical},
		{name: "duplicate field", bytes: bytes.Replace(encoded, []byte("  \"module\":"), []byte("  \"module\": \"github.com/eleven-am/golem/go\",\n  \"module\":"), 1), digest: "", reason: ReasonNoncanonical},
		{name: "unknown field", bytes: bytes.Replace(encoded, []byte("  \"module\":"), []byte("  \"unknown\": 1,\n  \"module\":"), 1), digest: "", reason: ReasonInvalidEncoding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trusted := test.digest
			if trusted == "" {
				trusted = Digest(test.bytes)
			}
			_, err := Parse(test.bytes, trusted)
			if reason, ok := CodeOf(err); !ok || reason != test.reason {
				t.Fatalf("error = %#v reason=%q known=%t", err, reason, ok)
			}
		})
	}
}

func TestCompatibilityManifestRejectsInvalidClosedInventories(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "missing module", mutate: func(value *Manifest) { value.Module = "" }},
		{name: "foreign module", mutate: func(value *Manifest) { value.Module = "example.com/foreign" }},
		{name: "development provenance", mutate: func(value *Manifest) { value.Release.Commit = strings.Repeat("1", 40) }},
		{name: "provider order", mutate: func(value *Manifest) { value.Providers[0], value.Providers[1] = value.Providers[1], value.Providers[0] }},
		{name: "missing C verification profile", mutate: func(value *Manifest) { value.Providers[0].VerificationProfiles = []string{"linguistic"} }},
		{name: "duplicate verification profile", mutate: func(value *Manifest) { value.Providers[0].VerificationProfiles = []string{"c", "c"} }},
		{name: "deployment profile confusion", mutate: func(value *Manifest) { value.DeploymentProfiles[0] = "linguistic" }},
		{name: "missing digest", mutate: func(value *Manifest) { value.Digests.GraphQLABI = "" }},
		{name: "zero format identity", mutate: func(value *Manifest) { value.Versions.ContractIR = 0 }},
		{name: "current codec not historically decoded", mutate: func(value *Manifest) { value.HistoricalDecode.FactCodecs = []string{"golem.fact.v1"} }},
		{name: "unsorted actions", mutate: func(value *Manifest) { value.RequiredActions = []string{"operator.restart", "migration.apply"} }},
		{name: "boundary prose", mutate: func(value *Manifest) { value.KnownBoundaries = []string{"contains spaces"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := compatibilityFixture()
			test.mutate(&value)
			if _, err := Encode(value); err == nil {
				t.Fatal("invalid manifest encoded")
			} else if reason, ok := CodeOf(err); !ok || reason != ReasonInvalidManifest {
				t.Fatalf("error = %#v reason=%q known=%t", err, reason, ok)
			}
		})
	}
}

func compatibilityFixture() Manifest {
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
			PublicGoAPI: strings.Repeat("1", 64), GeneratedGoABI: strings.Repeat("2", 64),
			GraphQLABI: strings.Repeat("3", 64), CLIJSON: strings.Repeat("4", 64), Observation: strings.Repeat("5", 64),
		},
		Versions: Versions{
			Generator: "p1-v1", GeneratedTemplateABI: "p8-go-abi-v5",
			SchemaBundle: 2, GeneratedManifest: 2, GraphQL: 4,
			ModelIR: 1, ContractIR: 5, CanonicalIR: 1, PhysicalSchema: 1, PhysicalCanonical: 1,
			MigrationManifest: 1, MigrationCanonical: 1, MigrationLedger: 1, EventSchema: 1,
			FactCodecs: []string{"golem.fact.v1", "golem.fact.v2"}, EventCodecs: []string{"golem.event.v1"},
			PrincipalSnapshotCodecs: []string{},
		},
		HistoricalDecode: HistoricalDecode{
			SchemaBundles: []uint16{1, 2}, GeneratedManifests: []uint16{1, 2}, ModelIR: []uint16{1}, ContractIR: []uint16{4, 5}, CanonicalIR: []uint16{1},
			PhysicalSchema: []uint16{1}, PhysicalCanonical: []uint16{1}, MigrationManifest: []uint16{1}, MigrationCanonical: []uint16{1}, MigrationLedger: []uint16{1},
			FactCodecs:  []string{"golem.fact.v1", "golem.fact.v2"},
			EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
		},
		RequiredActions: []string{},
		KnownBoundaries: []string{"cdc.requires-adapter", "mysql.unsupported"},
	}
}
