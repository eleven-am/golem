package storage

import (
	"reflect"
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
	if got := []ir.FieldID{before.Updates[0].ID, before.Updates[1].ID, before.Updates[2].ID}; !reflect.DeepEqual(got, []ir.FieldID{"id", "body", "source"}) {
		t.Fatalf("update dependencies=%v", got)
	}
}
