package schematest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type CompositeRelationFixture struct {
	Bundle                                              golem.SchemaBundle
	Registry                                            *schema.Registry
	SQLite, PostgreSQL                                  physical.PhysicalSchema
	Tenant                                              golem.ModelID
	Item                                                golem.ModelID
	TenantRegion, TenantID, TenantItems                 golem.FieldID
	ItemRegion, ItemID, OwnerRegion, OwnerID, ItemOwner golem.FieldID
	Ownership                                           golem.RelationID
	TenantKey, ItemKey                                  golem.KeyID
}

func NewCompositeRelation(t testing.TB) CompositeRelationFixture {
	return newCompositeRelation(t, "public")
}

func NewCompositeRelationPostgreSQLNamespace(t testing.TB, namespace physical.PhysicalName) CompositeRelationFixture {
	return newCompositeRelation(t, namespace)
}

func newCompositeRelation(t testing.TB, postgresNamespace physical.PhysicalName) CompositeRelationFixture {
	t.Helper()
	tenant, item := compilerir.ModelID(id(161)), compilerir.ModelID(id(162))
	tenantRegion, tenantID, tenantItems := compilerir.FieldID(id(171)), compilerir.FieldID(id(172)), compilerir.FieldID(id(173))
	itemRegion, itemID, ownerRegion, ownerID, itemOwner := compilerir.FieldID(id(181)), compilerir.FieldID(id(182)), compilerir.FieldID(id(183)), compilerir.FieldID(id(184)), compilerir.FieldID(id(185))
	ownership := compilerir.RelationID(id(191))
	tenantKey, itemKey := compilerir.KeyID(id(201)), compilerir.KeyID(id(202))
	model := compilerir.ModelIR{FormatVersion: compilerir.ModelFormatVersion, Providers: []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL}, Models: []compilerir.ModelDeclIR{
		{ID: tenant, LogicalName: "Tenant", Table: compilerir.TableBindingIR{PhysicalName: "tenants"}, Fields: []compilerir.FieldIR{
			scalar(tenantRegion, "Region", "region", compilerir.TypeUUID, false), scalar(tenantID, "ID", "id", compilerir.TypeUUID, false),
			relation(tenantItems, "Items", ownership, compilerir.RelationInverse, compilerir.RelationHasMany),
		}, PrimaryKey: &compilerir.KeyIR{ID: tenantKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_tenants", Fields: []compilerir.FieldID{tenantRegion, tenantID}}},
		{ID: item, LogicalName: "Item", Table: compilerir.TableBindingIR{PhysicalName: "items"}, Fields: []compilerir.FieldIR{
			scalar(itemRegion, "Region", "region", compilerir.TypeUUID, false), scalar(itemID, "ID", "id", compilerir.TypeUUID, false),
			scalar(ownerRegion, "OwnerRegion", "owner_region", compilerir.TypeUUID, false), scalar(ownerID, "OwnerID", "owner_id", compilerir.TypeUUID, false),
			relation(itemOwner, "Owner", ownership, compilerir.RelationSource, compilerir.RelationBelongsTo),
		}, PrimaryKey: &compilerir.KeyIR{ID: itemKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_items", Fields: []compilerir.FieldID{itemRegion, itemID}}},
	}, Relations: []compilerir.RelationIR{{
		ID: ownership, SourceModel: item, TargetModel: tenant, SourceField: itemOwner, InverseField: &tenantItems, Cardinality: compilerir.RelationMany,
		LocalFields: []compilerir.FieldID{ownerRegion, ownerID}, RemoteFields: []compilerir.FieldID{tenantRegion, tenantID},
	}}}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: tenant, Fields: fieldContracts(tenantRegion, tenantID, tenantItems)},
		{ModelID: item, Fields: fieldContracts(itemRegion, itemID, ownerRegion, ownerID, itemOwner)},
	}}
	modelDocument := document(t, uint32(compilerir.ModelFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalModel(model)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ModelFingerprint(model)
		return payload, fingerprint, err
	})
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})
	sqliteSchema := compositeRelationPhysical(compilerir.SQLite, tenant, item, tenantRegion, tenantID, itemRegion, itemID, ownerRegion, ownerID, tenantKey, itemKey)
	postgresSchema := compositeRelationPhysical(compilerir.PostgreSQL, tenant, item, tenantRegion, tenantID, itemRegion, itemID, ownerRegion, ownerID, tenantKey, itemKey)
	postgresSchema.Namespace.Name = postgresNamespace
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{3}, "schematest", "p4-composite-relation", modelDocument, contractDocument,
		providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("bootstrap composite relation schema: %v", err)
	}
	return CompositeRelationFixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		Tenant: golem.ModelID(mustFixed(t, string(tenant))), Item: golem.ModelID(mustFixed(t, string(item))),
		TenantRegion: golem.FieldID(mustFixed(t, string(tenantRegion))), TenantID: golem.FieldID(mustFixed(t, string(tenantID))), TenantItems: golem.FieldID(mustFixed(t, string(tenantItems))),
		ItemRegion: golem.FieldID(mustFixed(t, string(itemRegion))), ItemID: golem.FieldID(mustFixed(t, string(itemID))),
		OwnerRegion: golem.FieldID(mustFixed(t, string(ownerRegion))), OwnerID: golem.FieldID(mustFixed(t, string(ownerID))), ItemOwner: golem.FieldID(mustFixed(t, string(itemOwner))),
		Ownership: golem.RelationID(mustFixed(t, string(ownership))), TenantKey: golem.KeyID(mustFixed(t, string(tenantKey))), ItemKey: golem.KeyID(mustFixed(t, string(itemKey))),
	}
}

func compositeRelationPhysical(provider compilerir.Provider, tenant, item compilerir.ModelID, tenantRegion, tenantID, itemRegion, itemID, ownerRegion, ownerID compilerir.FieldID, tenantKey, itemKey compilerir.KeyID) physical.PhysicalSchema {
	manifest, namespace, uuid := postgresprovider.New().Manifest(), physical.PhysicalName("public"), physical.StoragePostgreSQLUUID
	if provider == compilerir.SQLite {
		manifest, namespace, uuid = sqliteprovider.New().Manifest(), "main", physical.StorageSQLiteText
	}
	column := func(field compilerir.FieldID, name string, ordinal uint32) physical.PhysicalColumn {
		return physical.PhysicalColumn{ID: field, Name: physical.PhysicalName(name), Ordinal: ordinal, Storage: physical.StorageType{Kind: uuid}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	}
	return physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: manifest, Namespace: physical.Namespace{Name: namespace}, Tables: []physical.PhysicalTable{
		{ID: tenant, Name: "tenants", Columns: []physical.PhysicalColumn{column(tenantRegion, "region", 0), column(tenantID, "id", 1)}, PrimaryKey: &physical.PhysicalKey{ID: tenantKey, Name: "pk_tenants", Columns: []compilerir.FieldID{tenantRegion, tenantID}}},
		{ID: item, Name: "items", Columns: []physical.PhysicalColumn{column(itemRegion, "region", 0), column(itemID, "id", 1), column(ownerRegion, "owner_region", 2), column(ownerID, "owner_id", 3)}, PrimaryKey: &physical.PhysicalKey{ID: itemKey, Name: "pk_items", Columns: []compilerir.FieldID{itemRegion, itemID}}},
	}}
}
