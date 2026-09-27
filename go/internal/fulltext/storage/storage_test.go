package storage

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestLowerPinsIndexedAndIdentityColumnProjection(t *testing.T) {
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension := ir.ProviderExtensionIR{ID: "index", Provider: ir.PostgreSQL, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}
	owner := physical.PhysicalTable{
		ID: "document",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLUUID}},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
	}
	before, err := Lower(extension, owner)
	if err != nil {
		t.Fatal(err)
	}
	indexedRename := owner
	indexedRename.Columns = append([]physical.PhysicalColumn(nil), owner.Columns...)
	indexedRename.Columns[1].Name = "content"
	afterIndexedRename, err := Lower(extension, indexedRename)
	if err != nil {
		t.Fatal(err)
	}
	identityStorage := owner
	identityStorage.Columns = append([]physical.PhysicalColumn(nil), owner.Columns...)
	identityStorage.Columns[0].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLText}
	afterIdentityStorage, err := Lower(extension, identityStorage)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, afterIndexedRename) || reflect.DeepEqual(before, afterIdentityStorage) {
		t.Fatal("owner column projection did not change the physical extension")
	}
	for _, candidate := range []physical.Extension{before, afterIndexedRename, afterIdentityStorage} {
		if _, err := Decode(candidate); err != nil {
			t.Fatalf("decode projected extension: %v", err)
		}
	}
}
