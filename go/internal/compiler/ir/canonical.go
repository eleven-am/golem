package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type Fingerprint string

func CanonicalRaw(input RawDeclIR) ([]byte, error) {
	if input.FormatVersion != RawDeclFormatVersion {
		return nil, fmt.Errorf("raw declaration IR version %d is unsupported; expected %d", input.FormatVersion, RawDeclFormatVersion)
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeRaw(&copy)
	return json.Marshal(copy)
}

func CanonicalModel(input ModelIR) ([]byte, error) {
	if input.FormatVersion != ModelFormatVersion {
		return nil, fmt.Errorf("model IR version %d is unsupported; expected %d", input.FormatVersion, ModelFormatVersion)
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeModel(&copy)
	return json.Marshal(copy)
}

func CanonicalContract(input ContractIR) ([]byte, error) {
	if input.FormatVersion != ContractFormatVersion {
		return nil, fmt.Errorf("contract IR version %d is unsupported; expected %d", input.FormatVersion, ContractFormatVersion)
	}
	// Preserve bootstrap source compatibility even though the deprecated field
	// is deliberately excluded from serialized ContractIR.
	for index := range input.Models {
		if len(input.Models[index].Fields) == 0 && len(input.Models[index].FieldModes) != 0 {
			input.Models[index].Fields = append([]FieldContractIR(nil), input.Models[index].FieldModes...)
		}
		input.Models[index].FieldModes = nil
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeContract(&copy)
	return json.Marshal(copy)
}

func CanonicalRawGoType(input RawGoTypeRef) ([]byte, error) {
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeRawGoType(&copy)
	return json.Marshal(copy)
}

func ModelFingerprint(input ModelIR) (Fingerprint, error) {
	encoded, err := CanonicalModel(input)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:model-fingerprint:v1", encoded), nil
}

// CanonicalEmptyModel is the reviewed logical starting point for an initial
// migration. Its fingerprint uses the ordinary ModelFingerprint domain; it is
// not an arbitrary sentinel digest.
func CanonicalEmptyModel() ModelIR {
	return ModelIR{FormatVersion: ModelFormatVersion, Providers: []Provider{}, Enums: []EnumIR{}, Models: []ModelDeclIR{}, Relations: []RelationIR{}, Extensions: []ProviderExtensionIR{}}
}

func EmptyModelFingerprint() Fingerprint {
	value, err := ModelFingerprint(CanonicalEmptyModel())
	if err != nil {
		panic(err)
	}
	return value
}

func ContractFingerprint(input ContractIR) (Fingerprint, error) {
	encoded, err := CanonicalContract(input)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:contract-fingerprint:v1", encoded), nil
}

// BuildEventSchemaShape closes the exact value schema needed by fact codecs.
// snapshotFields is semantic order supplied by the compiler; callers normally
// pass every locally stored scalar/enum field in model declaration order.
func BuildEventSchemaShape(model ModelDeclIR, enums []EnumIR, snapshotFields []FieldID) (EventSchemaShapeIR, error) {
	if model.ID == "" {
		return EventSchemaShapeIR{}, fmt.Errorf("event schema requires a model identity")
	}
	if model.PrimaryKey == nil || model.PrimaryKey.ID == "" || len(model.PrimaryKey.Fields) == 0 {
		return EventSchemaShapeIR{}, fmt.Errorf("event schema for model %s requires a primary key", model.ID)
	}
	fields := make(map[FieldID]FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	shape := EventSchemaShapeIR{FormatVersion: EventSchemaFormatVersion, ModelID: model.ID, PrimaryKeyID: model.PrimaryKey.ID}
	appendField := func(destination *[]EventFieldSchemaIR, fieldID FieldID) error {
		field, exists := fields[fieldID]
		if !exists || field.Scalar == nil || field.Kind == FieldRelation {
			return fmt.Errorf("event schema field %s is not a stored scalar of model %s", fieldID, model.ID)
		}
		if err := validateEventLogicalType(field.Scalar.Type); err != nil {
			return fmt.Errorf("event schema field %s: %w", fieldID, err)
		}
		*destination = append(*destination, EventFieldSchemaIR{FieldID: field.ID, Type: field.Scalar.Type, Nullable: field.Scalar.Nullable})
		return nil
	}
	seenIdentity := make(map[FieldID]bool, len(model.PrimaryKey.Fields))
	for _, fieldID := range model.PrimaryKey.Fields {
		if seenIdentity[fieldID] {
			return EventSchemaShapeIR{}, fmt.Errorf("event schema primary key repeats field %s", fieldID)
		}
		seenIdentity[fieldID] = true
		if err := appendField(&shape.IdentityFields, fieldID); err != nil {
			return EventSchemaShapeIR{}, err
		}
	}
	seenSnapshot := make(map[FieldID]bool, len(snapshotFields))
	for _, fieldID := range snapshotFields {
		if seenSnapshot[fieldID] {
			return EventSchemaShapeIR{}, fmt.Errorf("event schema snapshot repeats field %s", fieldID)
		}
		seenSnapshot[fieldID] = true
		if err := appendField(&shape.SnapshotFields, fieldID); err != nil {
			return EventSchemaShapeIR{}, err
		}
	}
	referenced := map[EnumID]bool{}
	var collectEnums func(LogicalTypeIR)
	collectEnums = func(value LogicalTypeIR) {
		if value.Kind == TypeEnum && value.EnumID != nil {
			referenced[*value.EnumID] = true
		}
		if value.Element != nil {
			collectEnums(*value.Element)
		}
	}
	for _, field := range shape.IdentityFields {
		collectEnums(field.Type)
	}
	for _, field := range shape.SnapshotFields {
		collectEnums(field.Type)
	}
	enumByID := make(map[EnumID]EnumIR, len(enums))
	for _, enum := range enums {
		enumByID[enum.ID] = enum
	}
	for enumID := range referenced {
		enum, exists := enumByID[enumID]
		if !exists {
			return EventSchemaShapeIR{}, fmt.Errorf("event schema references unknown enum %s", enumID)
		}
		entry := EventEnumSchemaIR{EnumID: enumID, Members: make([]EnumValueID, len(enum.Values))}
		for index, member := range enum.Values {
			entry.Members[index] = member.ID
		}
		sort.Slice(entry.Members, func(i, j int) bool { return entry.Members[i] < entry.Members[j] })
		shape.Enums = append(shape.Enums, entry)
	}
	sort.Slice(shape.Enums, func(i, j int) bool { return shape.Enums[i].EnumID < shape.Enums[j].EnumID })
	if shape.IdentityFields == nil {
		shape.IdentityFields = []EventFieldSchemaIR{}
	}
	if shape.SnapshotFields == nil {
		shape.SnapshotFields = []EventFieldSchemaIR{}
	}
	if shape.Enums == nil {
		shape.Enums = []EventEnumSchemaIR{}
	}
	return shape, nil
}

func validateEventLogicalType(value LogicalTypeIR) error {
	switch value.Kind {
	case TypeBool, TypeInt16, TypeInt32, TypeInt64, TypeFloat32, TypeFloat64,
		TypeDecimal, TypeString, TypeBytes, TypeUUID, TypeDate, TypeTime,
		TypeDateTime, TypeJSON:
		return nil
	case TypeEnum:
		if value.EnumID == nil || *value.EnumID == "" {
			return fmt.Errorf("enum logical type requires a stable enum identity")
		}
		return nil
	case TypeScalarList:
		if value.Element == nil {
			return fmt.Errorf("scalar-list logical type requires an element type")
		}
		return validateEventLogicalType(*value.Element)
	default:
		return fmt.Errorf("logical type %q has no lossless event codec", value.Kind)
	}
}

// EventSchemaFingerprint is the sole event-schema digest definition shared by
// compiler metadata and durable fact codecs. Ordered identity/snapshot fields
// remain ordered; enum inventories canonicalize by stable identity.
func EventSchemaFingerprint(input EventSchemaShapeIR) (Fingerprint, error) {
	if input.FormatVersion != EventSchemaFormatVersion {
		return "", fmt.Errorf("event schema version %d is unsupported; expected %d", input.FormatVersion, EventSchemaFormatVersion)
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return "", err
	}
	sort.Slice(copy.Enums, func(i, j int) bool { return copy.Enums[i].EnumID < copy.Enums[j].EnumID })
	for index := range copy.Enums {
		sort.Slice(copy.Enums[index].Members, func(i, j int) bool { return copy.Enums[index].Members[i] < copy.Enums[index].Members[j] })
		if copy.Enums[index].Members == nil {
			copy.Enums[index].Members = []EnumValueID{}
		}
	}
	if copy.IdentityFields == nil {
		copy.IdentityFields = []EventFieldSchemaIR{}
	}
	if copy.SnapshotFields == nil {
		copy.SnapshotFields = []EventFieldSchemaIR{}
	}
	if copy.Enums == nil {
		copy.Enums = []EventEnumSchemaIR{}
	}
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:event-schema-fingerprint:v1", encoded), nil
}

func fingerprint(domain string, encoded []byte) Fingerprint {
	hash := sha256.New()
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	hash.Write(encoded)
	return Fingerprint(hex.EncodeToString(hash.Sum(nil)))
}

func cloneJSON[T any](input T) (T, error) {
	var output T
	encoded, err := json.Marshal(input)
	if err != nil {
		return output, err
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return output, err
	}
	return output, nil
}

func normalizeRaw(raw *RawDeclIR) {
	raw.Root.Span = NormalizeSourceSpan(raw.Root.Span)
	raw.Root.SchemaNameSpan = NormalizeSourceSpan(raw.Root.SchemaNameSpan)
	for i := range raw.Root.Models {
		raw.Root.Models[i].Span = NormalizeSourceSpan(raw.Root.Models[i].Span)
	}
	sort.Slice(raw.Root.Models, func(i, j int) bool {
		left, right := raw.Root.Models[i], raw.Root.Models[j]
		if left.PackagePath != right.PackagePath {
			return left.PackagePath < right.PackagePath
		}
		return left.GoName < right.GoName
	})
	for i := range raw.Root.Providers {
		raw.Root.Providers[i].Span = NormalizeSourceSpan(raw.Root.Providers[i].Span)
	}
	sort.Slice(raw.Root.Providers, func(i, j int) bool {
		return providerRank(raw.Root.Providers[i].Provider) < providerRank(raw.Root.Providers[j].Provider)
	})
	for i := range raw.Root.EmbeddingSpaces {
		raw.Root.EmbeddingSpaces[i].Span = NormalizeSourceSpan(raw.Root.EmbeddingSpaces[i].Span)
	}
	sort.Slice(raw.Root.EmbeddingSpaces, func(i, j int) bool {
		return raw.Root.EmbeddingSpaces[i].Name < raw.Root.EmbeddingSpaces[j].Name
	})
	if raw.Root.Actor != nil {
		raw.Root.Actor.Span = NormalizeSourceSpan(raw.Root.Actor.Span)
	}
	for i := range raw.Models {
		model := &raw.Models[i]
		model.Span = NormalizeSourceSpan(model.Span)
		sortRawAttributes(model.Marker)
		for fieldIndex := range model.Fields {
			field := &model.Fields[fieldIndex]
			field.Span = NormalizeSourceSpan(field.Span)
			normalizeRawGoType(&field.GoType)
			sortRawAttributes(field.GolemAttrs)
		}
		for directiveIndex := range model.Directives {
			directive := &model.Directives[directiveIndex]
			directive.Span = NormalizeSourceSpan(directive.Span)
			sortRawAttributes(directive.Attributes)
		}
		sort.Slice(model.Directives, func(i, j int) bool {
			if model.Directives[i].Kind != model.Directives[j].Kind {
				return model.Directives[i].Kind < model.Directives[j].Kind
			}
			return model.Directives[i].Name < model.Directives[j].Name
		})
	}
	sort.Slice(raw.Models, func(i, j int) bool {
		if raw.Models[i].PackagePath != raw.Models[j].PackagePath {
			return raw.Models[i].PackagePath < raw.Models[j].PackagePath
		}
		return raw.Models[i].GoName < raw.Models[j].GoName
	})
	for i := range raw.Enums {
		raw.Enums[i].Span = NormalizeSourceSpan(raw.Enums[i].Span)
		raw.Enums[i].Method.Span = NormalizeSourceSpan(raw.Enums[i].Method.Span)
		for j := range raw.Enums[i].Values {
			raw.Enums[i].Values[j].Span = NormalizeSourceSpan(raw.Enums[i].Values[j].Span)
		}
		sort.SliceStable(raw.Enums[i].Values, func(a, b int) bool {
			return raw.Enums[i].Values[a].Ordinal < raw.Enums[i].Values[b].Ordinal
		})
	}
	sort.Slice(raw.Enums, func(i, j int) bool {
		if raw.Enums[i].PackagePath != raw.Enums[j].PackagePath {
			return raw.Enums[i].PackagePath < raw.Enums[j].PackagePath
		}
		return raw.Enums[i].GoName < raw.Enums[j].GoName
	})
	for i := range raw.Methods {
		raw.Methods[i].Span = NormalizeSourceSpan(raw.Methods[i].Span)
	}
	sort.Slice(raw.Methods, func(i, j int) bool {
		left, right := raw.Methods[i], raw.Methods[j]
		if left.ReceiverPackage != right.ReceiverPackage {
			return left.ReceiverPackage < right.ReceiverPackage
		}
		if left.ReceiverGoName != right.ReceiverGoName {
			return left.ReceiverGoName < right.ReceiverGoName
		}
		return left.Name < right.Name
	})
	forceRawSlices(raw)
}

func normalizeRawGoType(rawType *RawGoTypeRef) {
	rawType.Span = NormalizeSourceSpan(rawType.Span)
	for index := range rawType.Args {
		normalizeRawGoType(&rawType.Args[index])
	}
	if rawType.Args == nil {
		rawType.Args = []RawGoTypeRef{}
	}
}

func sortRawAttributes(attributes []RawAttribute) {
	for i := range attributes {
		attributes[i].Span = NormalizeSourceSpan(attributes[i].Span)
	}
	sort.Slice(attributes, func(i, j int) bool { return attributes[i].Name < attributes[j].Name })
}

func forceRawSlices(raw *RawDeclIR) {
	if raw.Root.Providers == nil {
		raw.Root.Providers = []RawProviderRef{}
	}
	if raw.Root.Models == nil {
		raw.Root.Models = []RawModelRef{}
	}
	if raw.Root.EmbeddingSpaces == nil {
		raw.Root.EmbeddingSpaces = []RawEmbeddingSpace{}
	}
	if raw.Models == nil {
		raw.Models = []RawModelDecl{}
	}
	if raw.Enums == nil {
		raw.Enums = []RawEnumDecl{}
	}
	if raw.Methods == nil {
		raw.Methods = []RawMethodDecl{}
	}
	for i := range raw.Models {
		if raw.Models[i].Marker == nil {
			raw.Models[i].Marker = []RawAttribute{}
		}
		if raw.Models[i].Fields == nil {
			raw.Models[i].Fields = []RawFieldDecl{}
		}
		if raw.Models[i].Directives == nil {
			raw.Models[i].Directives = []RawDirectiveDecl{}
		}
		for fieldIndex := range raw.Models[i].Fields {
			if raw.Models[i].Fields[fieldIndex].GolemAttrs == nil {
				raw.Models[i].Fields[fieldIndex].GolemAttrs = []RawAttribute{}
			}
		}
		for directiveIndex := range raw.Models[i].Directives {
			directive := &raw.Models[i].Directives[directiveIndex]
			if directive.Components == nil {
				directive.Components = []string{}
			}
			if directive.Attributes == nil {
				directive.Attributes = []RawAttribute{}
			}
		}
	}
	for i := range raw.Enums {
		if raw.Enums[i].Values == nil {
			raw.Enums[i].Values = []RawEnumValue{}
		}
	}
}

func normalizeModel(model *ModelIR) {
	sort.Slice(model.Providers, func(i, j int) bool { return providerRank(model.Providers[i]) < providerRank(model.Providers[j]) })
	sort.Slice(model.Enums, func(i, j int) bool { return model.Enums[i].ID < model.Enums[j].ID })
	for i := range model.Enums {
		if model.Enums[i].Values == nil {
			model.Enums[i].Values = []EnumValueIR{}
		}
	}
	sort.Slice(model.Models, func(i, j int) bool { return model.Models[i].ID < model.Models[j].ID })
	for i := range model.Models {
		entry := &model.Models[i]
		sort.Slice(entry.Fields, func(a, b int) bool { return entry.Fields[a].ID < entry.Fields[b].ID })
		sort.Slice(entry.Uniques, func(a, b int) bool { return entry.Uniques[a].ID < entry.Uniques[b].ID })
		sort.Slice(entry.Indexes, func(a, b int) bool { return entry.Indexes[a].ID < entry.Indexes[b].ID })
		sort.Slice(entry.Checks, func(a, b int) bool { return entry.Checks[a].ID < entry.Checks[b].ID })
		sort.Slice(entry.EqualityIndexes, func(a, b int) bool {
			left, right := entry.EqualityIndexes[a], entry.EqualityIndexes[b]
			if left.FieldID != right.FieldID {
				return left.FieldID < right.FieldID
			}
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			return equalityIndexIdentity(left) < equalityIndexIdentity(right)
		})
		if entry.Fields == nil {
			entry.Fields = []FieldIR{}
		}
		if entry.Uniques == nil {
			entry.Uniques = []KeyIR{}
		}
		if entry.Indexes == nil {
			entry.Indexes = []IndexIR{}
		}
		if entry.Checks == nil {
			entry.Checks = []CheckIR{}
		}
		if entry.EqualityIndexes == nil {
			entry.EqualityIndexes = []EqualityIndexIR{}
		}
		if entry.PrimaryKey != nil && entry.PrimaryKey.Fields == nil {
			entry.PrimaryKey.Fields = []FieldID{}
		}
		for index := range entry.Uniques {
			if entry.Uniques[index].Fields == nil {
				entry.Uniques[index].Fields = []FieldID{}
			}
		}
		for index := range entry.Indexes {
			if entry.Indexes[index].Keys == nil {
				entry.Indexes[index].Keys = []IndexKeyIR{}
			}
			if entry.Indexes[index].Include == nil {
				entry.Indexes[index].Include = []FieldID{}
			}
			for keyIndex := range entry.Indexes[index].Keys {
				if entry.Indexes[index].Keys[keyIndex].Expr != nil {
					normalizeSchemaExpr(entry.Indexes[index].Keys[keyIndex].Expr)
				}
			}
			if entry.Indexes[index].Predicate != nil {
				normalizeSchemaPredicate(entry.Indexes[index].Predicate)
			}
		}
		for fieldIndex := range entry.Fields {
			if entry.Fields[fieldIndex].Scalar != nil && entry.Fields[fieldIndex].Scalar.Generation != nil {
				normalizeSchemaExpr(&entry.Fields[fieldIndex].Scalar.Generation.Expr)
			}
		}
		for checkIndex := range entry.Checks {
			normalizeSchemaPredicate(&entry.Checks[checkIndex].Predicate)
		}
	}
	sort.Slice(model.Relations, func(i, j int) bool { return model.Relations[i].ID < model.Relations[j].ID })
	for i := range model.Relations {
		if model.Relations[i].LocalFields == nil {
			model.Relations[i].LocalFields = []FieldID{}
		}
		if model.Relations[i].RemoteFields == nil {
			model.Relations[i].RemoteFields = []FieldID{}
		}
	}
	sort.Slice(model.Extensions, func(i, j int) bool {
		left, right := model.Extensions[i], model.Extensions[j]
		if left.Provider != right.Provider {
			return providerRank(left.Provider) < providerRank(right.Provider)
		}
		if left.Owner != right.Owner {
			return left.Owner < right.Owner
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ID < right.ID
	})
	if model.Providers == nil {
		model.Providers = []Provider{}
	}
	if model.Enums == nil {
		model.Enums = []EnumIR{}
	}
	if model.Models == nil {
		model.Models = []ModelDeclIR{}
	}
	if model.Relations == nil {
		model.Relations = []RelationIR{}
	}
	if model.Extensions == nil {
		model.Extensions = []ProviderExtensionIR{}
	}
}

func normalizeContract(contract *ContractIR) {
	sort.Slice(contract.Models, func(i, j int) bool { return contract.Models[i].ModelID < contract.Models[j].ModelID })
	for i := range contract.Models {
		model := &contract.Models[i]
		if len(model.Fields) == 0 && len(model.FieldModes) != 0 {
			model.Fields = append([]FieldContractIR(nil), model.FieldModes...)
		}
		model.FieldModes = nil
		sort.Slice(model.Fields, func(a, b int) bool { return model.Fields[a].FieldID < model.Fields[b].FieldID })
		for fieldIndex := range model.Fields {
			sort.Slice(model.Fields[fieldIndex].Modes, func(a, b int) bool {
				return model.Fields[fieldIndex].Modes[a] < model.Fields[fieldIndex].Modes[b]
			})
		}
		sort.Slice(model.HookOwnedCreateFields, func(a, b int) bool {
			return model.HookOwnedCreateFields[a] < model.HookOwnedCreateFields[b]
		})
		if len(model.HookOwnedCreateFields) > 1 {
			write := 1
			for read := 1; read < len(model.HookOwnedCreateFields); read++ {
				if model.HookOwnedCreateFields[read] == model.HookOwnedCreateFields[write-1] {
					continue
				}
				model.HookOwnedCreateFields[write] = model.HookOwnedCreateFields[read]
				write++
			}
			model.HookOwnedCreateFields = model.HookOwnedCreateFields[:write]
		}
		sort.Slice(model.Selectors, func(a, b int) bool {
			if model.Selectors[a].Name != model.Selectors[b].Name {
				return model.Selectors[a].Name < model.Selectors[b].Name
			}
			return model.Selectors[a].KeyID < model.Selectors[b].KeyID
		})
		for selectorIndex := range model.Selectors {
			if model.Selectors[selectorIndex].Fields == nil {
				model.Selectors[selectorIndex].Fields = []FieldID{}
			}
		}
		sort.Slice(model.Operations, func(a, b int) bool { return model.Operations[a] < model.Operations[b] })
		if model.Aggregation != nil {
			sort.Slice(model.Aggregation.Dimensions, func(a, b int) bool { return model.Aggregation.Dimensions[a] < model.Aggregation.Dimensions[b] })
			sort.Slice(model.Aggregation.Measures, func(a, b int) bool { return model.Aggregation.Measures[a] < model.Aggregation.Measures[b] })
			sort.Slice(model.Aggregation.RelationDimensions, func(a, b int) bool {
				return model.Aggregation.RelationDimensions[a].Name < model.Aggregation.RelationDimensions[b].Name
			})
			if model.Aggregation.Dimensions == nil {
				model.Aggregation.Dimensions = []FieldID{}
			}
			if model.Aggregation.Measures == nil {
				model.Aggregation.Measures = []FieldID{}
			}
			if model.Aggregation.RelationDimensions == nil {
				model.Aggregation.RelationDimensions = []RelationDimensionContractIR{}
			}
			for relationIndex := range model.Aggregation.RelationDimensions {
				if model.Aggregation.RelationDimensions[relationIndex].Path == nil {
					model.Aggregation.RelationDimensions[relationIndex].Path = []RelationID{}
				}
			}
		}
		if model.Event != nil {
			if model.Event.MetadataFields == nil {
				model.Event.MetadataFields = []string{}
			}
			if model.Event.Schema.IdentityFields == nil {
				model.Event.Schema.IdentityFields = []EventFieldSchemaIR{}
			}
			if model.Event.Schema.SnapshotFields == nil {
				model.Event.Schema.SnapshotFields = []EventFieldSchemaIR{}
			}
			sort.Slice(model.Event.Schema.Enums, func(a, b int) bool { return model.Event.Schema.Enums[a].EnumID < model.Event.Schema.Enums[b].EnumID })
			for enumIndex := range model.Event.Schema.Enums {
				sort.Slice(model.Event.Schema.Enums[enumIndex].Members, func(a, b int) bool {
					return model.Event.Schema.Enums[enumIndex].Members[a] < model.Event.Schema.Enums[enumIndex].Members[b]
				})
				if model.Event.Schema.Enums[enumIndex].Members == nil {
					model.Event.Schema.Enums[enumIndex].Members = []EnumValueID{}
				}
			}
			if model.Event.Schema.Enums == nil {
				model.Event.Schema.Enums = []EventEnumSchemaIR{}
			}
		}
		sort.Slice(model.Computed, func(a, b int) bool {
			if model.Computed[a].Name != model.Computed[b].Name {
				return model.Computed[a].Name < model.Computed[b].Name
			}
			return model.Computed[a].ExtensionID < model.Computed[b].ExtensionID
		})
		for computedIndex := range model.Computed {
			computed := &model.Computed[computedIndex]
			sort.Slice(computed.Arguments, func(a, b int) bool { return computed.Arguments[a].Name < computed.Arguments[b].Name })
			sort.Slice(computed.Requires, func(a, b int) bool { return computed.Requires[a] < computed.Requires[b] })
			if computed.Arguments == nil {
				computed.Arguments = []GraphQLArgumentContractIR{}
			}
			if computed.Requires == nil {
				computed.Requires = []FieldID{}
			}
			normalizeGraphQLType(&computed.Result)
			for argumentIndex := range computed.Arguments {
				normalizeGraphQLType(&computed.Arguments[argumentIndex].Type)
			}
		}
		if model.Fields == nil {
			model.Fields = []FieldContractIR{}
		}
		if model.HookOwnedCreateFields == nil {
			model.HookOwnedCreateFields = []FieldID{}
		}
		if model.Selectors == nil {
			model.Selectors = []SelectorContractIR{}
		}
		if model.Operations == nil {
			model.Operations = []Operation{}
		}
		if model.Computed == nil {
			model.Computed = []ComputedFieldContractIR{}
		}
		for fieldIndex := range model.Fields {
			if model.Fields[fieldIndex].Modes == nil {
				model.Fields[fieldIndex].Modes = []FieldMode{}
			}
		}
	}
	sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].EnumID < contract.Enums[j].EnumID })
	for enumIndex := range contract.Enums {
		sort.Slice(contract.Enums[enumIndex].Values, func(i, j int) bool {
			return contract.Enums[enumIndex].Values[i].ValueID < contract.Enums[enumIndex].Values[j].ValueID
		})
		if contract.Enums[enumIndex].Values == nil {
			contract.Enums[enumIndex].Values = []EnumValueContractIR{}
		}
	}
	sort.Slice(contract.CustomOperations, func(i, j int) bool {
		if contract.CustomOperations[i].Operation != contract.CustomOperations[j].Operation {
			return contract.CustomOperations[i].Operation < contract.CustomOperations[j].Operation
		}
		if contract.CustomOperations[i].Name != contract.CustomOperations[j].Name {
			return contract.CustomOperations[i].Name < contract.CustomOperations[j].Name
		}
		return contract.CustomOperations[i].ExtensionID < contract.CustomOperations[j].ExtensionID
	})
	for operationIndex := range contract.CustomOperations {
		operation := &contract.CustomOperations[operationIndex]
		sort.Slice(operation.Arguments, func(i, j int) bool { return operation.Arguments[i].Name < operation.Arguments[j].Name })
		if operation.Arguments == nil {
			operation.Arguments = []GraphQLArgumentContractIR{}
		}
		normalizeGraphQLType(&operation.Result)
		for argumentIndex := range operation.Arguments {
			normalizeGraphQLType(&operation.Arguments[argumentIndex].Type)
		}
	}
	sort.Slice(contract.Methods, func(i, j int) bool {
		left, right := contract.Methods[i], contract.Methods[j]
		if left.Receiver.PackagePath != right.Receiver.PackagePath {
			return left.Receiver.PackagePath < right.Receiver.PackagePath
		}
		if left.Receiver.Name != right.Receiver.Name {
			return left.Receiver.Name < right.Receiver.Name
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
	if contract.Models == nil {
		contract.Models = []ModelContractIR{}
	}
	if contract.Enums == nil {
		contract.Enums = []EnumContractIR{}
	}
	if contract.Methods == nil {
		contract.Methods = []AttachedMethodIR{}
	}
	if contract.CustomOperations == nil {
		contract.CustomOperations = []CustomOperationContractIR{}
	}
}

func normalizeGraphQLType(value *GraphQLTypeIR) {
	if value != nil && value.Element != nil {
		normalizeGraphQLType(value.Element)
	}
}

func normalizeSchemaExpr(expression *SchemaExprIR) {
	for index := range expression.Operands {
		normalizeSchemaExpr(&expression.Operands[index])
	}
	if expression.Operands == nil {
		expression.Operands = []SchemaExprIR{}
	}
	sort.Slice(expression.ReferencedFields, func(i, j int) bool { return expression.ReferencedFields[i] < expression.ReferencedFields[j] })
	if expression.ReferencedFields == nil {
		expression.ReferencedFields = []FieldID{}
	}
}

func normalizeSchemaPredicate(predicate *SchemaPredicateIR) {
	for index := range predicate.ExpressionOperands {
		normalizeSchemaExpr(&predicate.ExpressionOperands[index])
	}
	for index := range predicate.Children {
		normalizeSchemaPredicate(&predicate.Children[index])
	}
	if predicate.ExpressionOperands == nil {
		predicate.ExpressionOperands = []SchemaExprIR{}
	}
	if predicate.Children == nil {
		predicate.Children = []SchemaPredicateIR{}
	}
	sort.Slice(predicate.ReferencedFields, func(i, j int) bool { return predicate.ReferencedFields[i] < predicate.ReferencedFields[j] })
	if predicate.ReferencedFields == nil {
		predicate.ReferencedFields = []FieldID{}
	}
}

func equalityIndexIdentity(value EqualityIndexIR) string {
	if value.KeyID != nil {
		return string(*value.KeyID)
	}
	if value.IndexID != nil {
		return string(*value.IndexID)
	}
	return ""
}

func providerRank(provider Provider) int {
	switch provider {
	case SQLite:
		return 0
	case PostgreSQL:
		return 1
	default:
		return 100
	}
}
