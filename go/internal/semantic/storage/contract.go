// Package storage maps the portable semantic-index compiler contract into the
// closed physical extension consumed by SQLite and PostgreSQL providers.
package storage

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

const (
	attributeDimensions = "dimensions"
	attributeFields     = "fields"
	attributeMetric     = "metric"
	attributeName       = "name"
	attributeSpace      = "space"
	attributeStorage    = "storage"
)

type Descriptor struct {
	ID         ir.ExtensionID
	ModelID    ir.ModelID
	Name       string
	Space      string
	Dimensions uint16
	Fields     []ir.FieldID
	Metric     string
	Storage    physical.PhysicalName
}

func Lower(extension ir.ProviderExtensionIR) (physical.Extension, error) {
	if extension.Kind != semanticcontract.IndexKind || extension.Version != semanticcontract.Version {
		return physical.Extension{}, fmt.Errorf("semantic storage: unsupported extension kind=%q version=%d", extension.Kind, extension.Version)
	}
	index, err := semanticcontract.DecodeIndex(extension.Payload)
	if err != nil {
		return physical.Extension{}, err
	}
	storage := "_golem_semantic_" + string(extension.ID)
	return physical.Extension{
		ID:       extension.ID,
		Provider: extension.Provider,
		Kind:     extension.Kind,
		Version:  extension.Version,
		Owner:    physical.ObjectRef{Kind: ir.ObjectModel, ModelID: ir.ModelID(extension.Owner)},
		Attributes: []physical.Attribute{
			{Name: attributeDimensions, Value: physical.SemanticValue{Kind: physical.ValueInteger, Integer: int64(index.Dimensions)}},
			{Name: attributeFields, Value: physical.SemanticValue{Kind: physical.ValueString, String: strings.Join(index.Fields, ",")}},
			{Name: attributeMetric, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Metric}},
			{Name: attributeName, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Name}},
			{Name: attributeSpace, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Space}},
			{Name: attributeStorage, Value: physical.SemanticValue{Kind: physical.ValueString, String: storage}},
		},
	}, nil
}

func Decode(extension physical.Extension) (Descriptor, error) {
	if extension.Kind != semanticcontract.IndexKind || extension.Version != semanticcontract.Version || extension.Owner.Kind != ir.ObjectModel || extension.Owner.ModelID == "" {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid physical extension header")
	}
	if len(extension.Attributes) != 6 {
		return Descriptor{}, fmt.Errorf("semantic storage: physical extension requires six attributes")
	}
	attributes := make(map[string]physical.SemanticValue, len(extension.Attributes))
	for _, attribute := range extension.Attributes {
		if _, duplicate := attributes[attribute.Name]; duplicate {
			return Descriptor{}, fmt.Errorf("semantic storage: duplicate attribute %q", attribute.Name)
		}
		attributes[attribute.Name] = attribute.Value
	}
	stringValue := func(name string) (string, error) {
		value, ok := attributes[name]
		if !ok || value.Kind != physical.ValueString || value.String == "" {
			return "", fmt.Errorf("semantic storage: invalid %s attribute", name)
		}
		return value.String, nil
	}
	name, err := stringValue(attributeName)
	if err != nil {
		return Descriptor{}, err
	}
	space, err := stringValue(attributeSpace)
	if err != nil {
		return Descriptor{}, err
	}
	metric, err := stringValue(attributeMetric)
	if err != nil || metric != "cosine" {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid metric attribute")
	}
	storage, err := stringValue(attributeStorage)
	if err != nil || storage != "_golem_semantic_"+string(extension.ID) {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid storage attribute")
	}
	dimensions := attributes[attributeDimensions]
	if dimensions.Kind != physical.ValueInteger || dimensions.Integer < 1 || dimensions.Integer > 2000 {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid dimensions attribute")
	}
	fieldValues := attributes[attributeFields]
	if fieldValues.Kind != physical.ValueString || fieldValues.String == "" {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid fields attribute")
	}
	encodedFields := strings.Split(fieldValues.String, ",")
	fields := make([]ir.FieldID, len(encodedFields))
	for index, field := range encodedFields {
		if field == "" || strings.ContainsAny(field, "\x00,") {
			return Descriptor{}, fmt.Errorf("semantic storage: invalid field attribute")
		}
		fields[index] = ir.FieldID(field)
	}
	return Descriptor{ID: extension.ID, ModelID: extension.Owner.ModelID, Name: name, Space: space, Dimensions: uint16(dimensions.Integer), Fields: fields, Metric: metric, Storage: physical.PhysicalName(storage)}, nil
}
