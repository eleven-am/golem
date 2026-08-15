package compatibility

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcodegen "github.com/eleven-am/golem/go/internal/graphql/codegen"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestOptimisticConcurrencyCompatibilitySourceAuthority(t *testing.T) {
	if compilerir.ModelFormatVersion != 2 || compilerir.ContractFormatVersion != 6 {
		t.Fatalf("compiler formats=%d/%d; want 2/6", compilerir.ModelFormatVersion, compilerir.ContractFormatVersion)
	}
	if graphqlcontract.ABIVersion != 5 || graphqlcodegen.GraphQLABIVersion != "p8-graphql-abi-v5" {
		t.Fatalf("GraphQL ABI=%d/%q; want 5/p8-graphql-abi-v5", graphqlcontract.ABIVersion, graphqlcodegen.GraphQLABIVersion)
	}
	if codegenmanifest.TemplateABIVersion != "p8-go-abi-v7" {
		t.Fatalf("generated template ABI=%q; want p8-go-abi-v7", codegenmanifest.TemplateABIVersion)
	}
	if uint32(physical.LatestFrozenPhysicalFormatVersion) != physical.SchemaFormatVersion || physical.SchemaFormatVersion != 3 || physical.CanonicalFormatVersion != 3 {
		t.Fatalf("physical current/frozen=%d/%d/%d; want 3/3/3", physical.SchemaFormatVersion, physical.CanonicalFormatVersion, physical.LatestFrozenPhysicalFormatVersion)
	}

	manifest := DevelopmentManifest()
	if manifest.FormatVersion != 2 || FormatVersion != 2 {
		t.Fatalf("compatibility manifest format=%d source=%d; want current v2", manifest.FormatVersion, FormatVersion)
	}
	wantVersions := Versions{
		Generator: "p1-v1", GeneratedTemplateABI: "p8-go-abi-v7",
		SchemaBundle: 2, GeneratedManifest: 2, GraphQL: 5,
		ModelIR: 2, ContractIR: 6, CanonicalIR: 1,
		PhysicalSchema: 3, PhysicalCanonical: 3,
		MigrationManifest: 1, MigrationCanonical: 1, MigrationLedger: 1,
		EventSchema: 1,
		FactCodecs:  []string{"golem.fact.v1", "golem.fact.v2"}, EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
	}
	if !reflect.DeepEqual(manifest.Versions, wantVersions) {
		t.Fatalf("development manifest target versions are stale: %#v", manifest.Versions)
	}
	wantHistory := HistoricalDecode{
		SchemaBundles: []uint16{2}, GeneratedManifests: []uint16{1, 2}, GraphQL: []uint16{4, 5},
		ModelIR: []uint16{1, 2}, ContractIR: []uint16{4, 5, 6}, CanonicalIR: []uint16{1},
		PhysicalSchema: []uint16{1, 2, 3}, PhysicalCanonical: []uint16{1, 2, 3},
		MigrationManifest: []uint16{1}, MigrationCanonical: []uint16{1}, MigrationLedger: []uint16{1},
		FactCodecs: []string{"golem.fact.v1", "golem.fact.v2"}, EventCodecs: []string{"golem.event.v1"}, PrincipalSnapshotCodecs: []string{},
	}
	if !reflect.DeepEqual(manifest.HistoricalDecode, wantHistory) {
		t.Fatalf("development manifest retained inventories are stale: %#v", manifest.HistoricalDecode)
	}
	if !reflect.DeepEqual(manifest.RequiredActions, []string{"migrate.database", "regenerate.generated"}) {
		t.Fatalf("compatibility actions=%v; want exact migration/regeneration actions", manifest.RequiredActions)
	}
	if manifest.MigrationGuide != nil {
		t.Fatalf("compatibility migration guide authority=%#v; want none", manifest.MigrationGuide)
	}
	if manifest.Digests.Observation != "914a495c1b7832491e7aa32a17e31efb82bf249d338de376c1a7608013132803" {
		t.Fatalf("observation inventory digest=%s", manifest.Digests.Observation)
	}
	if manifest.Digests.CLIJSON != "a1228e10ba31fba966029245a85964bc0e49db85e55ac28774ea39a7ee5221df" {
		t.Fatalf("CLI JSON inventory digest=%s", manifest.Digests.CLIJSON)
	}
	wantPublicGoAPI := []string{"./embedding", "./events", "./events/nats", "./golem", "./golemtest", "./graphql", "./observe", "./provider", "./provider/postgresql", "./provider/sqlite", "./queryplan", "./runtime"}
	authority := publicGoAPIPatterns()
	if !reflect.DeepEqual(authority, wantPublicGoAPI) {
		t.Fatalf("public Go package inventory=%v", authority)
	}
	exported := PublicGoAPIPatterns()
	if !reflect.DeepEqual(exported, authority) {
		t.Fatalf("exported public Go package inventory=%v; authority=%v", exported, authority)
	}
	exported[0] = "./mutated"
	if fresh := PublicGoAPIPatterns(); !reflect.DeepEqual(fresh, authority) {
		t.Fatalf("exported public Go package inventory aliases caller mutation: got=%v; authority=%v", fresh, authority)
	}

	checked, err := os.ReadFile(filepath.Join(compatibilityModuleRoot(t), "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Manifest
	if err := json.Unmarshal(checked, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, manifest) {
		t.Fatal("checked compatibility manifest differs from source authority")
	}
	canonical, err := Encode(manifest)
	if err != nil || !bytes.Equal(canonical, checked) {
		t.Fatalf("checked compatibility manifest is not canonical source-authority bytes: %v", err)
	}
}
