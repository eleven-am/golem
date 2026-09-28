package storage

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestProjectOwnerDetectsIndexedAndIdentityColumnChanges(t *testing.T) {
	sourceID := ir.FieldID("source")
	generated := &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Column: &sourceID}}
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := Lower(ir.ProviderExtensionIR{ID: "index", Provider: ir.PostgreSQL, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, physical.PhysicalTable{
		ID: "document",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLUUID}},
			{ID: "source", Name: "source", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Generated: generated},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
		Uniques:    []physical.PhysicalKey{{Columns: []ir.FieldID{"source"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := physical.PhysicalTable{
		ID: "document",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLUUID}},
			{ID: "source", Name: "source", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Generated: generated},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
		Uniques:    []physical.PhysicalKey{{Columns: []ir.FieldID{"source"}}},
	}
	before, err := ProjectOwner(extension, owner)
	if err != nil {
		t.Fatal(err)
	}
	indexedRename := owner
	indexedRename.Columns = append([]physical.PhysicalColumn(nil), owner.Columns...)
	indexedRename.Columns[2].Name = "content"
	afterIndexedRename, err := ProjectOwner(extension, indexedRename)
	if err != nil {
		t.Fatal(err)
	}
	identityStorage := owner
	identityStorage.Columns = append([]physical.PhysicalColumn(nil), owner.Columns...)
	identityStorage.Columns[0].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLText}
	afterIdentityStorage, err := ProjectOwner(extension, identityStorage)
	if err != nil {
		t.Fatal(err)
	}
	dependencyRename := owner
	dependencyRename.Columns = append([]physical.PhysicalColumn(nil), owner.Columns...)
	dependencyRename.Columns[1].Name = "raw_content"
	afterDependencyRename, err := ProjectOwner(extension, dependencyRename)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, afterIndexedRename) || reflect.DeepEqual(before, afterIdentityStorage) || reflect.DeepEqual(before, afterDependencyRename) {
		t.Fatal("owner projection did not expose a full-text storage dependency")
	}
	if got := []ir.FieldID{before.Updates[0].ID, before.Updates[1].ID, before.Updates[2].ID}; !reflect.DeepEqual(got, []ir.FieldID{"id", "source", "body"}) {
		t.Fatalf("update dependencies=%v", got)
	}
}

func TestLowerRejectsProviderReservedIdentityNames(t *testing.T) {
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		provider ir.Provider
		identity physical.PhysicalName
		storage  physical.StorageKind
		want     string
	}{
		{name: "PostgreSQL document", provider: ir.PostgreSQL, identity: "document", storage: physical.StoragePostgreSQLText, want: "reserved name document"},
		{name: "SQLite docid", provider: ir.SQLite, identity: "DoCiD", storage: physical.StorageSQLiteText, want: "reserved name docid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, lowerErr := Lower(ir.ProviderExtensionIR{ID: "index", Provider: test.provider, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, physical.PhysicalTable{
				ID: "document",
				Columns: []physical.PhysicalColumn{
					{ID: "id", Name: test.identity, Storage: physical.StorageType{Kind: test.storage}},
					{ID: "body", Name: "body", Storage: physical.StorageType{Kind: test.storage}, Nullable: true},
				},
				PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
			})
			if lowerErr == nil || !strings.Contains(lowerErr.Error(), test.want) {
				t.Fatalf("error=%v, want %q", lowerErr, test.want)
			}
		})
	}
	_, err = Lower(ir.ProviderExtensionIR{ID: "index", Provider: ir.SQLite, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, physical.PhysicalTable{
		ID: "document",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true},
			{ID: "slug", Name: "DoCiD", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
		Uniques:    []physical.PhysicalKey{{Columns: []ir.FieldID{"slug"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved name docid") {
		t.Fatalf("unique collision error=%v", err)
	}
	for _, test := range []struct {
		name, column, want string
		unique             bool
	}{
		{name: "owner rowid alias", column: "RoWiD", want: "reserved rowid alias"},
		{name: "shadow owner rowid", column: SQLiteOwnerRowIDColumn, want: "reserved name " + SQLiteOwnerRowIDColumn, unique: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := physical.PhysicalTable{
				ID: "document",
				Columns: []physical.PhysicalColumn{
					{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
					{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true},
					{ID: "reserved", Name: physical.PhysicalName(test.column), Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true},
				},
				PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
			}
			if test.unique {
				owner.Uniques = []physical.PhysicalKey{{Columns: []ir.FieldID{"reserved"}}}
			}
			_, lowerErr := Lower(ir.ProviderExtensionIR{ID: "index", Provider: ir.SQLite, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, owner)
			if lowerErr == nil || !strings.Contains(lowerErr.Error(), test.want) {
				t.Fatalf("error=%v, want %q", lowerErr, test.want)
			}
		})
	}
}

func TestProjectOwnerTracksGeneratedUniqueDependencies(t *testing.T) {
	sourceID := ir.FieldID("source")
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	owner := physical.PhysicalTable{
		ID: "document",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: sourceID, Name: "source", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "slug", Name: "slug", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Generated: &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Column: &sourceID}}},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
		Uniques:    []physical.PhysicalKey{{Columns: []ir.FieldID{"slug"}}},
	}
	extension, err := Lower(ir.ProviderExtensionIR{ID: "index", Provider: ir.SQLite, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := ProjectOwner(extension, owner)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]ir.FieldID, len(projection.Updates))
	for position, column := range projection.Updates {
		got[position] = column.ID
	}
	if want := []ir.FieldID{"id", "slug", "source", "body"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updates=%v, want %v", got, want)
	}
}
