package storage

import (
	"encoding/json"
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	attributeDefinition = "definition"
	attributeProjection = "projection"
	attributeStorage    = "storage"
)

type projectedColumn struct {
	ID       ir.FieldID            `json:"id"`
	Name     physical.PhysicalName `json:"name"`
	Storage  physical.StorageType  `json:"storage"`
	Nullable bool                  `json:"nullable"`
}

type ownerProjection struct {
	Identity []projectedColumn `json:"identity"`
	Fields   []projectedColumn `json:"fields"`
}

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
	fields := make([]projectedColumn, len(index.Fields))
	for position, field := range index.Fields {
		column, exists := columns[ir.FieldID(field.ID)]
		if !exists || column.Storage.Kind != physical.StorageSQLiteText && column.Storage.Kind != physical.StoragePostgreSQLText && column.Storage.Kind != physical.StoragePostgreSQLVarchar {
			return physical.Extension{}, fmt.Errorf("full-text storage: field %s is not stored text", field.ID)
		}
		fields[position] = projectColumn(column)
	}
	if owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return physical.Extension{}, fmt.Errorf("full-text storage: owner has no primary identity")
	}
	identity := make([]projectedColumn, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column, exists := columns[field]
		if !exists || column.Nullable {
			return physical.Extension{}, fmt.Errorf("full-text storage: primary identity column %s is invalid", field)
		}
		identity[position] = projectColumn(column)
	}
	projection, err := json.Marshal(ownerProjection{Identity: identity, Fields: fields})
	if err != nil {
		return physical.Extension{}, fmt.Errorf("full-text storage: encode owner projection: %w", err)
	}
	storage := "_golem_fulltext_" + string(extension.ID)
	return physical.Extension{
		ID: extension.ID, Provider: extension.Provider, Kind: extension.Kind, Version: extension.Version,
		Owner: physical.ObjectRef{Kind: ir.ObjectModel, ModelID: ir.ModelID(extension.Owner)},
		Attributes: []physical.Attribute{
			{Name: attributeDefinition, Value: physical.SemanticValue{Kind: physical.ValueString, String: extension.Payload}},
			{Name: attributeProjection, Value: physical.SemanticValue{Kind: physical.ValueString, String: string(projection)}},
			{Name: attributeStorage, Value: physical.SemanticValue{Kind: physical.ValueString, String: storage}},
		},
	}, nil
}

func projectColumn(column physical.PhysicalColumn) projectedColumn {
	return projectedColumn{ID: column.ID, Name: column.Name, Storage: column.Storage, Nullable: column.Nullable}
}

func Decode(extension physical.Extension) (Descriptor, error) {
	if extension.Kind != fulltextcontract.IndexKind || extension.Version != fulltextcontract.Version || extension.Owner.Kind != ir.ObjectModel || extension.Owner.ModelID == "" || len(extension.Attributes) != 3 {
		return Descriptor{}, fmt.Errorf("full-text storage: invalid physical extension")
	}
	attributes := make(map[string]physical.SemanticValue, 3)
	for _, attribute := range extension.Attributes {
		if _, duplicate := attributes[attribute.Name]; duplicate {
			return Descriptor{}, fmt.Errorf("full-text storage: duplicate attribute %q", attribute.Name)
		}
		attributes[attribute.Name] = attribute.Value
	}
	definition, projection, storage := attributes[attributeDefinition], attributes[attributeProjection], attributes[attributeStorage]
	if definition.Kind != physical.ValueString || projection.Kind != physical.ValueString || storage.Kind != physical.ValueString || storage.String != "_golem_fulltext_"+string(extension.ID) {
		return Descriptor{}, fmt.Errorf("full-text storage: invalid attributes")
	}
	index, err := fulltextcontract.Decode(definition.String)
	if err != nil {
		return Descriptor{}, err
	}
	var owner ownerProjection
	if err := json.Unmarshal([]byte(projection.String), &owner); err != nil || len(owner.Identity) == 0 || len(owner.Fields) != len(index.Fields) {
		return Descriptor{}, fmt.Errorf("full-text storage: invalid owner projection")
	}
	canonical, err := json.Marshal(owner)
	if err != nil || string(canonical) != projection.String {
		return Descriptor{}, fmt.Errorf("full-text storage: owner projection is not canonical")
	}
	identityIDs := make(map[ir.FieldID]bool, len(owner.Identity))
	for position, field := range owner.Fields {
		if string(field.ID) != index.Fields[position].ID || field.Name == "" || field.Storage.Kind == "" {
			return Descriptor{}, fmt.Errorf("full-text storage: invalid field projection")
		}
	}
	for _, column := range owner.Identity {
		if column.ID == "" || identityIDs[column.ID] || column.Name == "" || column.Storage.Kind == "" || column.Nullable {
			return Descriptor{}, fmt.Errorf("full-text storage: invalid identity projection")
		}
		identityIDs[column.ID] = true
	}
	return Descriptor{ID: extension.ID, ModelID: extension.Owner.ModelID, Index: index, Storage: physical.PhysicalName(storage.String)}, nil
}
