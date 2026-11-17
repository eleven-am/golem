package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Frozen from github.com/eleven-am/golem/go@v0.8.8. These independent DTOs and
// normalization preserve the released format-7 bytes, including ABI-6 transport
// trees, optimistic concurrency and hook-owned fields. Current-only vocabulary
// cannot enter by relabelling a newer document. See the provenance test.
const (
	historicalContractV7Release         = "v0.8.8"
	historicalContractV7TypesSHA256     = "3859f99d641200441c80de33bee5c1b4cce11c81e36ff9bca14d4fe5620acb40"
	historicalContractV7CanonicalSHA256 = "307913227863ea86564a3942192b279c1a1a2c0c7383b5167c8044f37d95e454"
	historicalContractV7FramingSHA256   = "cd3b37827c81e1843d7c18d51de941fe97170e0781a9f18511263a2aa9d594c5"
)

type contractV7ContractIR struct {
	FormatVersion     uint16                                `json:"formatVersion"`
	GraphQLABIVersion uint16                                `json:"graphqlAbiVersion"`
	Models            []contractV7ModelContractIR           `json:"models"`
	Enums             []contractV7EnumContractIR            `json:"enums"`
	Methods           []contractV7AttachedMethodIR          `json:"methods"`
	CustomOperations  []contractV7CustomOperationContractIR `json:"customOperations"`
	TxOperations      []contractV7TxOperationContractIR     `json:"txOperations,omitempty"`
}

type contractV7ModelContractIR struct {
	ModelID       ModelID                      `json:"modelId"`
	GraphQLName   string                       `json:"graphqlName"`
	GraphQLPlural string                       `json:"graphqlPlural"`
	Roots         contractV7GraphQLRootNamesIR `json:"roots"`
	Fields        []contractV7FieldContractIR  `json:"fields"`
	// OptimisticConcurrency is a validated projection of the ModelIR owner. It
	// is never inferred from contract names or field exposure.
	//
	// contractV7ContractIR v6 first serialized this projection. The retained v5 decoder
	// has a separate DTO without this member and rejects relabelled v6 bytes.
	OptimisticConcurrency *FieldID `json:"optimisticConcurrency,omitempty"`
	// HookOwnedCreateFields are stable scalar identities omitted only from
	// GraphQL create-shaped inputs and populated by BeforeCreate.
	HookOwnedCreateFields []FieldID                      `json:"hookOwnedCreateFields"`
	Selectors             []contractV7SelectorContractIR `json:"selectors"`
	// FieldModes is a bootstrap source-compatibility field. It is not serialized;
	// normalized producers and consumers use Fields.
	FieldModes          []contractV7FieldContractIR         `json:"-"`
	Operations          []Operation                         `json:"operations"`
	Subscriptions       bool                                `json:"subscriptions"`
	Event               *contractV7EventContractIR          `json:"event,omitempty"`
	Aggregation         *contractV7AggregationContractIR    `json:"aggregation,omitempty"`
	ScopedReads         bool                                `json:"scopedReads"`
	Limits              contractV7LimitContractIR           `json:"limits"`
	Computed            []contractV7ComputedFieldContractIR `json:"computed"`
	Exposed             bool                                `json:"exposed"`
	IndexSearchDisabled bool                                `json:"indexSearchDisabled,omitempty"`
}

type contractV7GraphQLRootNamesIR struct {
	FindOne         string `json:"findOne"`
	FindMany        string `json:"findMany"`
	Create          string `json:"create"`
	Update          string `json:"update"`
	Upsert          string `json:"upsert"`
	Delete          string `json:"delete"`
	UpdateMany      string `json:"updateMany"`
	DeleteMany      string `json:"deleteMany"`
	Aggregate       string `json:"aggregate"`
	GroupBy         string `json:"groupBy"`
	RelationGroupBy string `json:"relationGroupBy"`
	Events          string `json:"events"`
}

type contractV7FieldContractIR struct {
	FieldID     FieldID     `json:"fieldId"`
	GraphQLName string      `json:"graphqlName"`
	Modes       []FieldMode `json:"modes"`
}

type contractV7SelectorContractIR struct {
	KeyID  KeyID     `json:"keyId"`
	Kind   KeyKind   `json:"kind"`
	Name   string    `json:"name"`
	Fields []FieldID `json:"fields"`
}

type contractV7EventContractIR struct {
	PayloadTypeName    string                       `json:"payloadTypeName"`
	IdentityTypeName   string                       `json:"identityTypeName,omitempty"`
	MetadataFields     []string                     `json:"metadataFields"`
	DeleteSnapshotFull bool                         `json:"deleteSnapshotFull"`
	Schema             contractV7EventSchemaShapeIR `json:"schema"`
	SchemaFingerprint  Fingerprint                  `json:"schemaFingerprint"`
}

type contractV7EventSchemaShapeIR struct {
	FormatVersion  uint16                         `json:"formatVersion"`
	ModelID        ModelID                        `json:"modelId"`
	PrimaryKeyID   KeyID                          `json:"primaryKeyId"`
	IdentityFields []contractV7EventFieldSchemaIR `json:"identityFields"`
	SnapshotFields []contractV7EventFieldSchemaIR `json:"snapshotFields"`
	Enums          []contractV7EventEnumSchemaIR  `json:"enums"`
}

type contractV7EventFieldSchemaIR struct {
	FieldID  FieldID                 `json:"fieldId"`
	Type     contractV7LogicalTypeIR `json:"type"`
	Nullable bool                    `json:"nullable"`
}

type contractV7LogicalTypeIR struct {
	Kind         LogicalTypeKind          `json:"kind"`
	EnumID       *EnumID                  `json:"enumId,omitempty"`
	Element      *contractV7LogicalTypeIR `json:"element,omitempty"`
	Precision    *uint16                  `json:"precision,omitempty"`
	Scale        *uint16                  `json:"scale,omitempty"`
	MaxLength    *uint32                  `json:"maxLength,omitempty"`
	JSONSchemaID *string                  `json:"jsonSchemaId,omitempty"`
	Capability   *CapabilityID            `json:"capability,omitempty"`
}

type contractV7EventEnumSchemaIR struct {
	EnumID  EnumID        `json:"enumId"`
	Members []EnumValueID `json:"members"`
}

type contractV7AggregationContractIR struct {
	Enabled                       bool                                    `json:"enabled"`
	Dimensions                    []FieldID                               `json:"dimensions"`
	DimensionsExplicit            bool                                    `json:"dimensionsExplicit"`
	Measures                      []FieldID                               `json:"measures"`
	MeasuresExplicit              bool                                    `json:"measuresExplicit"`
	RelationDimensions            []contractV7RelationDimensionContractIR `json:"relationDimensions"`
	GraphQLMaxGroups              uint32                                  `json:"graphqlMaxGroups"`
	RelationMaxIntermediateGroups uint32                                  `json:"relationMaxIntermediateGroups"`
}

type contractV7RelationDimensionContractIR struct {
	Name          string       `json:"name"`
	Path          []RelationID `json:"path"`
	TerminalField FieldID      `json:"terminalField"`
}

type contractV7LimitContractIR struct {
	MaxTake         uint32 `json:"maxTake,omitempty"`
	DefaultPageSize uint32 `json:"defaultPageSize,omitempty"`
	MaxPageSize     uint32 `json:"maxPageSize,omitempty"`
}

type contractV7ComputedFieldContractIR struct {
	ExtensionID ExtensionID                           `json:"extensionId"`
	Name        string                                `json:"name"`
	Result      contractV7GraphQLTypeIR               `json:"result"`
	Arguments   []contractV7GraphQLArgumentContractIR `json:"arguments"`
	Requires    []FieldID                             `json:"requires"`
	Resolver    contractV7AttachedMethodIR            `json:"resolver"`
	Batch       *contractV7ComputedBatchContractIR    `json:"batch,omitempty"`
}

type contractV7GraphQLTypeIR struct {
	Kind     GraphQLTypeKind          `json:"kind"`
	Name     string                   `json:"name,omitempty"`
	Nullable bool                     `json:"nullable"`
	Element  *contractV7GraphQLTypeIR `json:"element,omitempty"`
	MaxItems uint32                   `json:"maxItems,omitempty"`
}

type contractV7GraphQLArgumentContractIR struct {
	Name string                  `json:"name"`
	Type contractV7GraphQLTypeIR `json:"type"`
}

type contractV7AttachedMethodIR struct {
	ModelID     *ModelID                 `json:"modelId,omitempty"`
	PackagePath string                   `json:"packagePath,omitempty"`
	Receiver    contractV7GoNamedTypeIR  `json:"receiver"`
	Name        string                   `json:"name"`
	Kind        string                   `json:"kind"`
	Actor       *contractV7GoNamedTypeIR `json:"actor,omitempty"`
}

type contractV7GoNamedTypeIR struct {
	PackagePath string `json:"packagePath"`
	Name        string `json:"name"`
}

type contractV7ComputedBatchContractIR struct {
	KeyField     FieldID                     `json:"keyField"`
	Loader       contractV7AttachedMethodIR  `json:"loader"`
	CacheKey     *contractV7AttachedMethodIR `json:"cacheKey,omitempty"`
	MaxBatchSize uint32                      `json:"maxBatchSize"`
}

type contractV7EnumContractIR struct {
	EnumID      EnumID                          `json:"enumId"`
	GraphQLName string                          `json:"graphqlName"`
	Values      []contractV7EnumValueContractIR `json:"values"`
}

type contractV7EnumValueContractIR struct {
	ValueID     EnumValueID `json:"valueId"`
	GraphQLName string      `json:"graphqlName"`
}

type contractV7CustomOperationContractIR struct {
	ExtensionID ExtensionID                           `json:"extensionId"`
	Operation   CustomOperationKind                   `json:"operation"`
	Name        string                                `json:"name"`
	Arguments   []contractV7GraphQLArgumentContractIR `json:"arguments"`
	Result      contractV7GraphQLTypeIR               `json:"result"`
	Resolver    contractV7AttachedMethodIR            `json:"resolver"`
	Capability  CustomOperationCapability             `json:"capability"`
}

type contractV7TxOperationContractIR struct {
	ExtensionID ExtensionID                `json:"extensionId"`
	Name        string                     `json:"name"`
	Operation   contractV7AttachedMethodIR `json:"operation"`
}

// DecodeHistoricalContractV7 verifies released canonical bytes before projecting
// the closed retained DTO into current memory. FormatVersion remains 7 and the
// original GraphQL ABI is preserved; this is not an active format-8 contract.
func DecodeHistoricalContractV7(payload []byte) (ContractIR, error) {
	canonical, err := decodeHistoricalContractV7(payload)
	if err != nil {
		return ContractIR{}, err
	}
	var projected ContractIR
	if err := decodeExactContractJSON(canonical, &projected); err != nil {
		return ContractIR{}, fmt.Errorf("historical contract v7 projection: %w", err)
	}
	return projected, nil
}

// FingerprintHistoricalContractV7 checks exact released canonical encoding and
// uses the original fingerprint domain, never current CanonicalContract.
func FingerprintHistoricalContractV7(payload []byte) (Fingerprint, error) {
	canonical, err := decodeHistoricalContractV7(payload)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:contract-fingerprint:v1", canonical), nil
}

func decodeHistoricalContractV7(payload []byte) ([]byte, error) {
	if err := validateContractJSONEnvelope(payload, 7); err != nil {
		return nil, err
	}
	var retained contractV7ContractIR
	if err := decodeExactContractJSON(payload, &retained); err != nil {
		return nil, fmt.Errorf("historical contract v7 decode: %w", err)
	}
	if err := validateContractV7Vocabulary(reflect.ValueOf(retained), 0); err != nil {
		return nil, err
	}
	normalizeContractV7(&retained)
	canonical, err := json.Marshal(retained)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, payload) {
		return nil, fmt.Errorf("historical contract v7 is not in released canonical normalized form")
	}
	return canonical, nil
}

// Struct vocabulary is frozen above; enum vocabularies are frozen here rather
// than inherited from additive constants in current IR. Primitive stable IDs
// and open method-kind strings retain their released representation.
var contractV7Enums = map[reflect.Type][]string{
	reflect.TypeOf(FieldMode("")):                 {"visible", "hidden", "readOnly", "writeOnly", "immutable", "system", "internal"},
	reflect.TypeOf(KeyKind("")):                   {"primary", "unique"},
	reflect.TypeOf(Operation("")):                 {"findOne", "findMany", "create", "update", "upsert", "delete", "updateMany", "deleteMany", "aggregate", "groupBy", "relationGroupBy"},
	reflect.TypeOf(GraphQLTypeKind("")):           {"scalar", "enum", "model", "list", "predicate", "selector", "createInput", "updateInput", "updateManyInput"},
	reflect.TypeOf(CustomOperationKind("")):       {"query", "mutation"},
	reflect.TypeOf(CustomOperationCapability("")): {"callerOnly"},
	reflect.TypeOf(LogicalTypeKind("")):           {"bool", "int16", "int32", "int64", "float32", "float64", "decimal", "string", "bytes", "uuid", "date", "time", "dateTime", "json", "enum", "scalarList"},
}

func validateContractV7Vocabulary(value reflect.Value, depth int) error {
	if depth > 1024 {
		return fmt.Errorf("historical contract v7 nesting exceeds 1024")
	}
	if vocabulary, ok := contractV7Enums[value.Type()]; ok {
		for _, allowed := range vocabulary {
			if value.String() == allowed {
				return nil
			}
		}
		return fmt.Errorf("historical contract v7 unsupported %s value", value.Type().Name())
	}
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			return validateContractV7Vocabulary(value.Elem(), depth+1)
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if err := validateContractV7Vocabulary(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := validateContractV7Vocabulary(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeContractV7(contract *contractV7ContractIR) {
	sort.Slice(contract.Models, func(i, j int) bool { return contract.Models[i].ModelID < contract.Models[j].ModelID })
	for i := range contract.Models {
		model := &contract.Models[i]
		if len(model.Fields) == 0 && len(model.FieldModes) != 0 {
			model.Fields = append([]contractV7FieldContractIR(nil), model.FieldModes...)
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
				model.Aggregation.RelationDimensions = []contractV7RelationDimensionContractIR{}
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
				model.Event.Schema.IdentityFields = []contractV7EventFieldSchemaIR{}
			}
			if model.Event.Schema.SnapshotFields == nil {
				model.Event.Schema.SnapshotFields = []contractV7EventFieldSchemaIR{}
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
				model.Event.Schema.Enums = []contractV7EventEnumSchemaIR{}
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
				computed.Arguments = []contractV7GraphQLArgumentContractIR{}
			}
			if computed.Requires == nil {
				computed.Requires = []FieldID{}
			}
			normalizeGraphQLTypeV7(&computed.Result)
			for argumentIndex := range computed.Arguments {
				normalizeGraphQLTypeV7(&computed.Arguments[argumentIndex].Type)
			}
		}
		if model.Fields == nil {
			model.Fields = []contractV7FieldContractIR{}
		}
		if model.HookOwnedCreateFields == nil {
			model.HookOwnedCreateFields = []FieldID{}
		}
		if model.Selectors == nil {
			model.Selectors = []contractV7SelectorContractIR{}
		}
		if model.Operations == nil {
			model.Operations = []Operation{}
		}
		if model.Computed == nil {
			model.Computed = []contractV7ComputedFieldContractIR{}
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
			contract.Enums[enumIndex].Values = []contractV7EnumValueContractIR{}
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
			operation.Arguments = []contractV7GraphQLArgumentContractIR{}
		}
		normalizeGraphQLTypeV7(&operation.Result)
		for argumentIndex := range operation.Arguments {
			normalizeGraphQLTypeV7(&operation.Arguments[argumentIndex].Type)
		}
	}
	sort.Slice(contract.TxOperations, func(i, j int) bool {
		if contract.TxOperations[i].Name != contract.TxOperations[j].Name {
			return contract.TxOperations[i].Name < contract.TxOperations[j].Name
		}
		return contract.TxOperations[i].ExtensionID < contract.TxOperations[j].ExtensionID
	})
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
		contract.Models = []contractV7ModelContractIR{}
	}
	if contract.Enums == nil {
		contract.Enums = []contractV7EnumContractIR{}
	}
	if contract.Methods == nil {
		contract.Methods = []contractV7AttachedMethodIR{}
	}
	if contract.CustomOperations == nil {
		contract.CustomOperations = []contractV7CustomOperationContractIR{}
	}
}

func normalizeGraphQLTypeV7(value *contractV7GraphQLTypeIR) {
	if value != nil && value.Element != nil {
		normalizeGraphQLTypeV7(value.Element)
	}
}
