package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compatibility"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	"github.com/eleven-am/golem/go/internal/migration"
	migrationexplain "github.com/eleven-am/golem/go/internal/migration/explain"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestP8EveryMachineJSONDocumentCarriesExactFormatVersion(t *testing.T) {
	tests := []struct {
		name  string
		want  uint16
		value any
	}{
		{name: "build-diagnostics", want: buildDiagnosticsOutputFormatVersion, value: buildDiagnosticsOutput{FormatVersion: buildDiagnosticsOutputFormatVersion}},
		{name: "doctor", want: diagnosticsFormatVersion, value: newDoctorOutput("sqlite")},
		{name: "generate-check", want: generationOutputFormatVersion, value: generateOutput{FormatVersion: generationOutputFormatVersion, Changed: []string{}, Stale: []string{}}},
		{name: "inspect", want: 2, value: inspectOutput{FormatVersion: 2}},
		{name: "migration-apply", want: migrationOutputFormatVersion, value: migrationApplyOutput{FormatVersion: migrationOutputFormatVersion, Applied: []migration.MigrationID{}}},
		{name: "migration-new", want: migrationOutputFormatVersion, value: migrationNewOutput{FormatVersion: migrationOutputFormatVersion, Providers: []compilerir.Provider{}, Changed: []string{}}},
		{name: "migration-plan", want: 1, value: migrationexplain.JSONCompatibilitySource()},
		{name: "version", want: diagnosticsFormatVersion, value: currentVersion()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := encodeJSON(&output, test.value); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				FormatVersion uint16 `json:"formatVersion"`
			}
			decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
			if err := decoder.Decode(&envelope); err != nil || envelope.FormatVersion != test.want {
				t.Fatalf("machine JSON format version=%d want=%d err=%v", envelope.FormatVersion, test.want, err)
			}
		})
	}
}

const (
	p8CLIJSONCorpusSHA256   = "a1228e10ba31fba966029245a85964bc0e49db85e55ac28774ea39a7ee5221df"
	p8PersistedCorpusSHA256 = "b63e73e98aad59a15aa34931143e550961015c110e39703102ca0668fa5fd62c"
)

func TestP8CLIJSONAndPersistedFormatCompatibilityGate(t *testing.T) {
	cli := currentCLICompatibilityInventory(t)
	cliBytes, err := compatibility.EncodeCLIInventory(cli)
	if err != nil {
		t.Fatal(err)
	}
	if actual := compatibility.Digest(cliBytes); actual != p8CLIJSONCorpusSHA256 {
		t.Fatalf("CLI JSON compatibility inventory changed: got %s\n%s", actual, cliBytes)
	}
	parsedCLI, err := compatibility.ParseCLIInventory(cliBytes)
	if err != nil || compatibility.CompareCLI(parsedCLI, cli) != compatibility.LayerUnchanged {
		t.Fatalf("CLI JSON compatibility inventory is not canonical: %v", err)
	}

	persisted := currentPersistedCompatibilityInventory()
	persistedBytes, err := compatibility.EncodePersistedInventory(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if actual := compatibility.Digest(persistedBytes); actual != p8PersistedCorpusSHA256 {
		t.Fatalf("persisted format compatibility inventory changed: got %s", actual)
	}
	parsedPersisted, err := compatibility.ParsePersistedInventory(persistedBytes)
	if err != nil || compatibility.ComparePersisted(parsedPersisted, persisted) != compatibility.LayerUnchanged {
		t.Fatalf("persisted format compatibility inventory is not canonical: %v", err)
	}
}

func currentCLICompatibilityInventory(t *testing.T) compatibility.CLIInventory {
	t.Helper()
	value, err := compatibility.BuildCLIInventory([]compatibility.CLISource{
		{Name: "build-diagnostics", FormatVersion: buildDiagnosticsOutputFormatVersion, Value: buildDiagnosticsOutput{}},
		{Name: "doctor", FormatVersion: diagnosticsFormatVersion, Value: doctorOutput{}},
		{Name: "generate-check", FormatVersion: generationOutputFormatVersion, Value: generateOutput{}},
		{Name: "inspect", FormatVersion: 2, Value: inspectOutput{}},
		{Name: "migration-apply", FormatVersion: migrationOutputFormatVersion, Value: migrationApplyOutput{}},
		{Name: "migration-new", FormatVersion: migrationOutputFormatVersion, Value: migrationNewOutput{}},
		{Name: "migration-plan", FormatVersion: migrationexplain.JSONCompatibilitySource().FormatVersion, Value: migrationexplain.JSONCompatibilitySource()},
		{Name: "version", FormatVersion: diagnosticsFormatVersion, Value: versionOutput{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(value.Documents))
	for index := range value.Documents {
		names[index] = value.Documents[index].Name
	}
	wantNames := []string{"build-diagnostics", "doctor", "generate-check", "inspect", "migration-apply", "migration-new", "migration-plan", "version"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("machine JSON compatibility document names=%v; want exact %v", names, wantNames)
	}
	return value
}

func currentPersistedCompatibilityInventory() compatibility.PersistedInventory {
	numeric := func(value uint64) string { return strconv.FormatUint(value, 10) }
	return compatibility.PersistedInventory{FormatVersion: compatibility.PersistedInventoryFormatVersion, Formats: []compatibility.PersistedFormat{
		{Name: "canonical-ir", Current: numeric(uint64(compilerir.CanonicalFormatVersion)), Historical: []string{numeric(uint64(compilerir.CanonicalFormatVersion))}},
		{Name: "contract-ir", Current: numeric(uint64(compilerir.ContractFormatVersion)), Historical: []string{"4", "5", numeric(uint64(compilerir.ContractFormatVersion))}},
		{Name: "event-codec", Current: eventcodec.CodecIdentity, Historical: []string{eventcodec.CodecIdentity}},
		{Name: "event-schema", Current: numeric(uint64(compilerir.EventSchemaFormatVersion)), Historical: []string{numeric(uint64(compilerir.EventSchemaFormatVersion))}},
		{Name: "fact-codec", Current: mutationfact.CodecIdentityV2, Historical: []string{mutationfact.CodecIdentityV1, mutationfact.CodecIdentityV2}},
		{Name: "generated-manifest", Current: numeric(uint64(manifest.FormatVersion)), Historical: []string{"1", numeric(uint64(manifest.FormatVersion))}},
		{Name: "graphql-abi", Current: numeric(uint64(graphqlcontract.ABIVersion)), Historical: []string{"4", numeric(uint64(graphqlcontract.ABIVersion))}},
		{Name: "migration-canonical", Current: numeric(uint64(migration.ManifestCanonicalVersion)), Historical: []string{numeric(uint64(migration.ManifestCanonicalVersion))}},
		{Name: "migration-ledger", Current: numeric(uint64(physical.MigrationLedgerFormatVersionV1)), Historical: []string{numeric(uint64(physical.MigrationLedgerFormatVersionV1))}},
		{Name: "migration-manifest", Current: numeric(uint64(migration.ManifestFormatVersion)), Historical: []string{numeric(uint64(migration.ManifestFormatVersion))}},
		{Name: "model-ir", Current: numeric(uint64(compilerir.ModelFormatVersion)), Historical: []string{"1", numeric(uint64(compilerir.ModelFormatVersion))}},
		{Name: "physical-canonical", Current: numeric(uint64(physical.CanonicalFormatVersion)), Historical: []string{"1", "2", numeric(uint64(physical.CanonicalFormatVersion))}},
		{Name: "physical-schema", Current: numeric(uint64(physical.SchemaFormatVersion)), Historical: []string{"1", "2", numeric(uint64(physical.SchemaFormatVersion))}},
		{Name: "schema-bundle", Current: numeric(uint64(golem.SchemaBundleFormatVersion)), Historical: []string{numeric(uint64(golem.SchemaBundleFormatVersion))}},
	}}
}
