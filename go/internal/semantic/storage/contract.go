// Package storage maps the portable semantic-index compiler contract into the
// closed physical extension consumed by SQLite and PostgreSQL providers.
package storage

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

const (
	attributeDimensions = "dimensions"
	attributeFields     = "fields"
	attributeIdentity   = "identity"
	attributeMetric     = "metric"
	attributeName       = "name"
	attributeSpace      = "space"
	attributeStorage    = "storage"
)

// IdentityColumn is one owner primary-key column mirrored into the semantic
// shadow state table. It carries the owner's physical name, provider storage
// type, and nullability so ranking can join shadow rows back to the authorized
// candidate set through real identity columns.
type IdentityColumn struct {
	Name    physical.PhysicalName
	Storage physical.StorageType
	NotNull bool
}

type Descriptor struct {
	ID         ir.ExtensionID
	ModelID    ir.ModelID
	Name       string
	Space      string
	Dimensions uint16
	Fields     []ir.FieldID
	Metric     string
	Storage    physical.PhysicalName
	Identity   []IdentityColumn
}

var identityStorageKinds = map[physical.StorageKind]bool{
	physical.StorageSQLiteInteger: true, physical.StorageSQLiteReal: true,
	physical.StorageSQLiteText: true, physical.StorageSQLiteBlob: true,
	physical.StoragePostgreSQLBoolean: true, physical.StoragePostgreSQLSmallInt: true,
	physical.StoragePostgreSQLInteger: true, physical.StoragePostgreSQLBigInt: true,
	physical.StoragePostgreSQLReal: true, physical.StoragePostgreSQLDouble: true,
	physical.StoragePostgreSQLNumeric: true, physical.StoragePostgreSQLVarchar: true,
	physical.StoragePostgreSQLText: true, physical.StoragePostgreSQLBytea: true,
	physical.StoragePostgreSQLUUID: true, physical.StoragePostgreSQLDate: true,
	physical.StoragePostgreSQLTime: true, physical.StoragePostgreSQLTimestampTZ: true,
	physical.StoragePostgreSQLJSONB: true,
}

var reservedIdentityColumns = map[string]bool{
	"record_key": true, "source_hash": true, "space_fingerprint": true,
	"status": true, "attempt_count": true, "error_code": true, "updated_at": true,
}

func Lower(extension ir.ProviderExtensionIR, owner physical.PhysicalTable) (physical.Extension, error) {
	if extension.Kind != semanticcontract.IndexKind || extension.Version != semanticcontract.Version {
		return physical.Extension{}, fmt.Errorf("semantic storage: unsupported extension kind=%q version=%d", extension.Kind, extension.Version)
	}
	index, err := semanticcontract.DecodeIndex(extension.Payload)
	if err != nil {
		return physical.Extension{}, err
	}
	identity, err := encodeIdentity(ir.ModelID(extension.Owner), owner)
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
			{Name: attributeIdentity, Value: physical.SemanticValue{Kind: physical.ValueString, String: identity}},
			{Name: attributeMetric, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Metric}},
			{Name: attributeName, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Name}},
			{Name: attributeSpace, Value: physical.SemanticValue{Kind: physical.ValueString, String: index.Space}},
			{Name: attributeStorage, Value: physical.SemanticValue{Kind: physical.ValueString, String: storage}},
		},
	}, nil
}

func encodeIdentity(model ir.ModelID, owner physical.PhysicalTable) (string, error) {
	if owner.ID != model || owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return "", fmt.Errorf("semantic storage: owner table %s has no physical primary identity", model)
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	entries := make([]string, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column, exists := columns[field]
		if !exists {
			return "", fmt.Errorf("semantic storage: primary identity column %s is absent from owner table %s", field, model)
		}
		if !identityStorageKinds[column.Storage.Kind] || column.Storage.Symbol != nil {
			return "", fmt.Errorf("semantic storage: primary identity column %s has unsupported storage %q", field, column.Storage.Kind)
		}
		if strings.ContainsAny(string(column.Name), ",:") || column.Name == "" {
			return "", fmt.Errorf("semantic storage: primary identity column %s has an unencodable name", field)
		}
		if reservedIdentityColumns[strings.ToLower(string(column.Name))] {
			return "", fmt.Errorf("semantic storage: primary identity column %s uses reserved shadow name %q", field, column.Name)
		}
		notNull := "0"
		if !column.Nullable {
			notNull = "1"
		}
		entries[position] = strings.Join([]string{
			string(column.Name), string(column.Storage.Kind),
			strconv.FormatUint(uint64(column.Storage.Precision), 10),
			strconv.FormatUint(uint64(column.Storage.Scale), 10),
			strconv.FormatUint(uint64(column.Storage.Length), 10),
			notNull,
		}, ":")
	}
	return strings.Join(entries, ","), nil
}

func decodeIdentity(encoded string) ([]IdentityColumn, error) {
	invalid := fmt.Errorf("semantic storage: invalid identity attribute")
	entries := strings.Split(encoded, ",")
	result := make([]IdentityColumn, len(entries))
	seen := make(map[string]bool, len(entries))
	for position, entry := range entries {
		parts := strings.Split(entry, ":")
		if len(parts) != 6 || parts[0] == "" || seen[parts[0]] {
			return nil, invalid
		}
		seen[parts[0]] = true
		kind := physical.StorageKind(parts[1])
		if !identityStorageKinds[kind] {
			return nil, invalid
		}
		precision, precisionErr := strconv.ParseUint(parts[2], 10, 16)
		scale, scaleErr := strconv.ParseUint(parts[3], 10, 16)
		length, lengthErr := strconv.ParseUint(parts[4], 10, 32)
		if precisionErr != nil || scaleErr != nil || lengthErr != nil || parts[5] != "0" && parts[5] != "1" {
			return nil, invalid
		}
		result[position] = IdentityColumn{
			Name:    physical.PhysicalName(parts[0]),
			Storage: physical.StorageType{Kind: kind, Precision: uint16(precision), Scale: uint16(scale), Length: uint32(length)},
			NotNull: parts[5] == "1",
		}
	}
	return result, nil
}

func Decode(extension physical.Extension) (Descriptor, error) {
	if extension.Kind != semanticcontract.IndexKind || extension.Version != semanticcontract.Version || extension.Owner.Kind != ir.ObjectModel || extension.Owner.ModelID == "" {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid physical extension header")
	}
	// Historical snapshots predate the identity projection and are replayed
	// verbatim. They decode with an empty Identity; only a current compile can
	// carry the seventh attribute.
	if len(extension.Attributes) != 6 && len(extension.Attributes) != 7 {
		return Descriptor{}, fmt.Errorf("semantic storage: physical extension requires six or seven attributes")
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
	var identity []IdentityColumn
	if encoded, present := attributes[attributeIdentity]; present {
		if encoded.Kind != physical.ValueString || encoded.String == "" {
			return Descriptor{}, fmt.Errorf("semantic storage: invalid identity attribute")
		}
		identity, err = decodeIdentity(encoded.String)
		if err != nil {
			return Descriptor{}, err
		}
	} else if len(extension.Attributes) != 6 {
		return Descriptor{}, fmt.Errorf("semantic storage: invalid identity attribute")
	}
	return Descriptor{ID: extension.ID, ModelID: extension.Owner.ModelID, Name: name, Space: space, Dimensions: uint16(dimensions.Integer), Fields: fields, Metric: metric, Storage: physical.PhysicalName(storage), Identity: identity}, nil
}
