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

type ProjectedColumn struct {
	ID       ir.FieldID
	Name     physical.PhysicalName
	Storage  physical.StorageType
	Nullable bool
}

type OwnerProjection struct {
	Identity []ProjectedColumn
	Fields   []ProjectedColumn
	Updates  []ProjectedColumn
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
	for _, field := range owner.PrimaryKey.Columns {
		column, exists := columns[field]
		if !exists || column.Nullable {
			return physical.Extension{}, fmt.Errorf("full-text storage: primary identity column %s is invalid", field)
		}
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

func ProjectOwner(extension physical.Extension, owner physical.PhysicalTable) (OwnerProjection, error) {
	descriptor, err := Decode(extension)
	if err != nil {
		return OwnerProjection{}, err
	}
	if owner.ID != descriptor.ModelID || owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return OwnerProjection{}, fmt.Errorf("full-text storage: owner projection has no primary identity")
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	project := func(field ir.FieldID) (ProjectedColumn, error) {
		column, exists := columns[field]
		if !exists {
			return ProjectedColumn{}, fmt.Errorf("full-text storage: projected column %s is absent", field)
		}
		return ProjectedColumn{ID: column.ID, Name: column.Name, Storage: column.Storage, Nullable: column.Nullable}, nil
	}
	result := OwnerProjection{Identity: make([]ProjectedColumn, len(owner.PrimaryKey.Columns)), Fields: make([]ProjectedColumn, len(descriptor.Index.Fields))}
	for position, field := range owner.PrimaryKey.Columns {
		column, projectErr := project(field)
		if projectErr != nil || column.Nullable {
			return OwnerProjection{}, fmt.Errorf("full-text storage: projected identity column %s is invalid", field)
		}
		result.Identity[position] = column
	}
	for position, field := range descriptor.Index.Fields {
		column, projectErr := project(ir.FieldID(field.ID))
		if projectErr != nil {
			return OwnerProjection{}, projectErr
		}
		result.Fields[position] = column
	}
	updates, updateErr := UpdateColumns(extension, owner)
	if updateErr != nil {
		return OwnerProjection{}, updateErr
	}
	result.Updates = make([]ProjectedColumn, len(updates))
	for position, column := range updates {
		result.Updates[position] = ProjectedColumn{ID: column.ID, Name: column.Name, Storage: column.Storage, Nullable: column.Nullable}
	}
	return result, nil
}

func UpdateColumns(extension physical.Extension, owner physical.PhysicalTable) ([]physical.PhysicalColumn, error) {
	descriptor, err := Decode(extension)
	if err != nil {
		return nil, err
	}
	if owner.ID != descriptor.ModelID || owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return nil, fmt.Errorf("full-text storage: update owner has no primary identity")
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	result := make([]physical.PhysicalColumn, 0, len(owner.PrimaryKey.Columns)+len(descriptor.Index.Fields))
	seen := make(map[ir.FieldID]bool, cap(result))
	add := func(field ir.FieldID) (physical.PhysicalColumn, error) {
		column, exists := columns[field]
		if !exists {
			return physical.PhysicalColumn{}, fmt.Errorf("full-text storage: update column %s is absent", field)
		}
		if !seen[field] {
			seen[field] = true
			result = append(result, column)
		}
		return column, nil
	}
	for _, field := range owner.PrimaryKey.Columns {
		if _, addErr := add(field); addErr != nil {
			return nil, addErr
		}
	}
	visiting := make(map[ir.FieldID]bool)
	var addGeneratedDependencies func(ir.FieldID) error
	addGeneratedDependencies = func(field ir.FieldID) error {
		column, exists := columns[field]
		if !exists {
			return fmt.Errorf("full-text storage: generated dependency %s is absent", field)
		}
		if column.Generated == nil {
			return nil
		}
		if visiting[field] {
			return fmt.Errorf("full-text storage: generated dependency cycle at %s", field)
		}
		visiting[field] = true
		var walk func(physical.Expression) error
		walk = func(expression physical.Expression) error {
			if expression.Column != nil {
				dependency, addErr := add(*expression.Column)
				if addErr != nil {
					return addErr
				}
				if dependency.Generated != nil {
					if dependencyErr := addGeneratedDependencies(dependency.ID); dependencyErr != nil {
						return dependencyErr
					}
				}
			}
			for _, operand := range expression.Operands {
				if walkErr := walk(operand); walkErr != nil {
					return walkErr
				}
			}
			return nil
		}
		walkErr := walk(column.Generated.Expression)
		delete(visiting, field)
		return walkErr
	}
	for _, field := range descriptor.Index.Fields {
		fieldID := ir.FieldID(field.ID)
		if _, addErr := add(fieldID); addErr != nil {
			return nil, addErr
		}
		if dependencyErr := addGeneratedDependencies(fieldID); dependencyErr != nil {
			return nil, dependencyErr
		}
	}
	return result, nil
}
