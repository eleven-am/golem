package storage

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	attributeDefinition = "definition"
	attributeStorage    = "storage"
)

type Descriptor struct {
	ID      ir.ExtensionID
	ModelID ir.ModelID
	Index   fulltextcontract.Index
	Storage physical.PhysicalName
}

func Lower(extension ir.ProviderExtensionIR, owner physical.PhysicalTable) (physical.Extension, error) {
	if extension.Kind != fulltextcontract.IndexKind || extension.Version != fulltextcontract.Version || owner.ID != ir.ModelID(extension.Owner) {
		return physical.Extension{}, fmt.Errorf("full-text storage: invalid extension")
	}
	index, err := fulltextcontract.Decode(extension.Payload)
	if err != nil {
		return physical.Extension{}, err
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	for _, field := range index.Fields {
		column, exists := columns[ir.FieldID(field.ID)]
		if !exists || column.Storage.Kind != physical.StorageSQLiteText && column.Storage.Kind != physical.StoragePostgreSQLText && column.Storage.Kind != physical.StoragePostgreSQLVarchar {
			return physical.Extension{}, fmt.Errorf("full-text storage: field %s is not stored text", field.ID)
		}
	}
	if owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return physical.Extension{}, fmt.Errorf("full-text storage: owner has no primary identity")
	}
	storage := "_golem_fulltext_" + string(extension.ID)
	return physical.Extension{
		ID: extension.ID, Provider: extension.Provider, Kind: extension.Kind, Version: extension.Version,
		Owner: physical.ObjectRef{Kind: ir.ObjectModel, ModelID: ir.ModelID(extension.Owner)},
		Attributes: []physical.Attribute{
			{Name: attributeDefinition, Value: physical.SemanticValue{Kind: physical.ValueString, String: extension.Payload}},
			{Name: attributeStorage, Value: physical.SemanticValue{Kind: physical.ValueString, String: storage}},
		},
	}, nil
}

func Decode(extension physical.Extension) (Descriptor, error) {
	if extension.Kind != fulltextcontract.IndexKind || extension.Version != fulltextcontract.Version || extension.Owner.Kind != ir.ObjectModel || extension.Owner.ModelID == "" || len(extension.Attributes) != 2 {
		return Descriptor{}, fmt.Errorf("full-text storage: invalid physical extension")
	}
	attributes := make(map[string]physical.SemanticValue, 2)
	for _, attribute := range extension.Attributes {
		if _, duplicate := attributes[attribute.Name]; duplicate {
			return Descriptor{}, fmt.Errorf("full-text storage: duplicate attribute %q", attribute.Name)
		}
		attributes[attribute.Name] = attribute.Value
	}
	definition, storage := attributes[attributeDefinition], attributes[attributeStorage]
	if definition.Kind != physical.ValueString || storage.Kind != physical.ValueString || storage.String != "_golem_fulltext_"+string(extension.ID) {
		return Descriptor{}, fmt.Errorf("full-text storage: invalid attributes")
	}
	index, err := fulltextcontract.Decode(definition.String)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{ID: extension.ID, ModelID: extension.Owner.ModelID, Index: index, Storage: physical.PhysicalName(storage.String)}, nil
}
