package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

const historicalContractV5FormatVersion uint16 = 5
const maxHistoricalContractV5Bytes = 16 << 20

// Provenance is the pre-v6 source boundary from which the retained v5 JSON
// shape and canonical normalization were copied. The adapted decoder's own
// source digest is pinned in historical_contract_v5_provenance_test.go.
const (
	historicalContractV5TypesUpstreamSHA256     = "20a0d1bd5f3e1fbbd991d8abbca226a33d72afae71bd51703b2c4a88a2cab5b9"
	historicalContractV5TypesUpstreamLines      = 848
	historicalContractV5CanonicalUpstreamSHA256 = "fdcd49abd6935e67cda3e0f7a4d81e1a06622aac9ac3af4a1c5a68dfb56abbcf"
	historicalContractV5CanonicalUpstreamLines  = 770
)

// These DTOs are the released ContractIR v5 JSON shape. They deliberately do
// not embed or alias current contract structs. In particular, v5ModelContract
// has no optimisticConcurrency member, so a v6 document relabelled as v5 is an
// unknown-field failure rather than a lossy decode.
type v5Contract struct {
	FormatVersion     uint16              `json:"formatVersion"`
	GraphQLABIVersion uint16              `json:"graphqlAbiVersion"`
	Models            []v5ModelContract   `json:"models"`
	Enums             []v5EnumContract    `json:"enums"`
	Methods           []v5AttachedMethod  `json:"methods"`
	CustomOperations  []v5CustomOperation `json:"customOperations"`
}

type v5ModelContract struct {
	ModelID               ModelID           `json:"modelId"`
	GraphQLName           string            `json:"graphqlName"`
	GraphQLPlural         string            `json:"graphqlPlural"`
	Roots                 v5GraphQLRoots    `json:"roots"`
	Fields                []v5FieldContract `json:"fields"`
	HookOwnedCreateFields []FieldID         `json:"hookOwnedCreateFields"`
	Selectors             []v5Selector      `json:"selectors"`
	Operations            []Operation       `json:"operations"`
	Subscriptions         bool              `json:"subscriptions"`
	Event                 *v5EventContract  `json:"event,omitempty"`
	Aggregation           *v5Aggregation    `json:"aggregation,omitempty"`
	ScopedReads           bool              `json:"scopedReads"`
	Limits                v5Limits          `json:"limits"`
	Computed              []v5ComputedField `json:"computed"`
	Exposed               bool              `json:"exposed"`
}

type v5GraphQLRoots struct {
	FindOne, FindMany, Create, Update, Upsert, Delete, UpdateMany, DeleteMany string
	Aggregate, GroupBy, RelationGroupBy, Events                               string
}

func (roots v5GraphQLRoots) MarshalJSON() ([]byte, error) {
	type wire struct {
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
	return json.Marshal(wire(roots))
}

func (roots *v5GraphQLRoots) UnmarshalJSON(payload []byte) error {
	type wire struct {
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
	var decoded wire
	if err := decodeExactContractV5JSON(payload, &decoded); err != nil {
		return err
	}
	*roots = v5GraphQLRoots(decoded)
	return nil
}

type v5FieldContract struct {
	FieldID     FieldID     `json:"fieldId"`
	GraphQLName string      `json:"graphqlName"`
	Modes       []FieldMode `json:"modes"`
}

type v5Selector struct {
	KeyID  KeyID     `json:"keyId"`
	Kind   KeyKind   `json:"kind"`
	Name   string    `json:"name"`
	Fields []FieldID `json:"fields"`
}

type v5EventContract struct {
	PayloadTypeName    string       `json:"payloadTypeName"`
	IdentityTypeName   string       `json:"identityTypeName,omitempty"`
	MetadataFields     []string     `json:"metadataFields"`
	DeleteSnapshotFull bool         `json:"deleteSnapshotFull"`
	Schema             v5EventShape `json:"schema"`
	SchemaFingerprint  Fingerprint  `json:"schemaFingerprint"`
}

type v5EventShape struct {
	FormatVersion  uint16         `json:"formatVersion"`
	ModelID        ModelID        `json:"modelId"`
	PrimaryKeyID   KeyID          `json:"primaryKeyId"`
	IdentityFields []v5EventField `json:"identityFields"`
	SnapshotFields []v5EventField `json:"snapshotFields"`
	Enums          []v5EventEnum  `json:"enums"`
}

type v5EventField struct {
	FieldID  FieldID       `json:"fieldId"`
	Type     v5LogicalType `json:"type"`
	Nullable bool          `json:"nullable"`
}

type v5LogicalType struct {
	Kind         LogicalTypeKind `json:"kind"`
	EnumID       *EnumID         `json:"enumId,omitempty"`
	Element      *v5LogicalType  `json:"element,omitempty"`
	Precision    *uint16         `json:"precision,omitempty"`
	Scale        *uint16         `json:"scale,omitempty"`
	MaxLength    *uint32         `json:"maxLength,omitempty"`
	JSONSchemaID *string         `json:"jsonSchemaId,omitempty"`
	Capability   *CapabilityID   `json:"capability,omitempty"`
}

type v5EventEnum struct {
	EnumID  EnumID        `json:"enumId"`
	Members []EnumValueID `json:"members"`
}

type v5EnumContract struct {
	EnumID      EnumID        `json:"enumId"`
	GraphQLName string        `json:"graphqlName"`
	Values      []v5EnumValue `json:"values"`
}

type v5EnumValue struct {
	ValueID     EnumValueID `json:"valueId"`
	GraphQLName string      `json:"graphqlName"`
}

type v5Aggregation struct {
	Enabled                       bool                  `json:"enabled"`
	Dimensions                    []FieldID             `json:"dimensions"`
	DimensionsExplicit            bool                  `json:"dimensionsExplicit"`
	Measures                      []FieldID             `json:"measures"`
	MeasuresExplicit              bool                  `json:"measuresExplicit"`
	RelationDimensions            []v5RelationDimension `json:"relationDimensions"`
	GraphQLMaxGroups              uint32                `json:"graphqlMaxGroups"`
	RelationMaxIntermediateGroups uint32                `json:"relationMaxIntermediateGroups"`
}

type v5RelationDimension struct {
	Name          string       `json:"name"`
	Path          []RelationID `json:"path"`
	TerminalField FieldID      `json:"terminalField"`
}

type v5Limits struct {
	MaxTake         uint32 `json:"maxTake,omitempty"`
	DefaultPageSize uint32 `json:"defaultPageSize,omitempty"`
	MaxPageSize     uint32 `json:"maxPageSize,omitempty"`
}

type v5GraphQLType struct {
	Kind     GraphQLTypeKind `json:"kind"`
	Name     string          `json:"name,omitempty"`
	Nullable bool            `json:"nullable"`
	Element  *v5GraphQLType  `json:"element,omitempty"`
}

type v5GraphQLArgument struct {
	Name string        `json:"name"`
	Type v5GraphQLType `json:"type"`
}

type v5ComputedField struct {
	ExtensionID ExtensionID         `json:"extensionId"`
	Name        string              `json:"name"`
	Result      v5GraphQLType       `json:"result"`
	Arguments   []v5GraphQLArgument `json:"arguments"`
	Requires    []FieldID           `json:"requires"`
	Resolver    v5AttachedMethod    `json:"resolver"`
	Batch       *v5ComputedBatch    `json:"batch,omitempty"`
}

type v5ComputedBatch struct {
	KeyField     FieldID           `json:"keyField"`
	Loader       v5AttachedMethod  `json:"loader"`
	CacheKey     *v5AttachedMethod `json:"cacheKey,omitempty"`
	MaxBatchSize uint32            `json:"maxBatchSize"`
}

type v5CustomOperation struct {
	ExtensionID ExtensionID               `json:"extensionId"`
	Operation   CustomOperationKind       `json:"operation"`
	Name        string                    `json:"name"`
	Arguments   []v5GraphQLArgument       `json:"arguments"`
	Result      v5GraphQLType             `json:"result"`
	Resolver    v5AttachedMethod          `json:"resolver"`
	Capability  CustomOperationCapability `json:"capability"`
}

type v5AttachedMethod struct {
	ModelID     *ModelID       `json:"modelId,omitempty"`
	PackagePath string         `json:"packagePath,omitempty"`
	Receiver    v5GoNamedType  `json:"receiver"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Actor       *v5GoNamedType `json:"actor,omitempty"`
}

type v5GoNamedType struct {
	PackagePath string `json:"packagePath"`
	Name        string `json:"name"`
}

// CanonicalDecodeContractV5 is the only decoder for released ContractIR v5.
// It validates against retained v5 DTOs and closed v5 enumerations, requires
// byte-for-byte v5 canonical form, and then projects the validated document
// into current memory with every v6-only fact absent. It never calls current
// framing, exact-JSON, normalization, or projection-validation helpers.
func CanonicalDecodeContractV5(payload []byte) (ContractIR, error) {
	canonical, err := decodeContractV5(payload)
	if err != nil {
		return ContractIR{}, err
	}
	var current ContractIR
	if err := json.Unmarshal(canonical, &current); err != nil {
		return ContractIR{}, fmt.Errorf("contract IR historical v5 decode: project retained DTO: %w", err)
	}
	current.FormatVersion = ContractFormatVersion
	for index := range current.Models {
		current.Models[index].OptimisticConcurrency = nil
	}
	return current, nil
}

// ContractFingerprintV5 verifies exact released v5 canonical bytes before
// applying the unchanged v1 contract-fingerprint domain.
func ContractFingerprintV5(payload []byte) (Fingerprint, error) {
	canonical, err := decodeContractV5(payload)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:contract-fingerprint:v1", canonical), nil
}

func decodeContractV5(payload []byte) ([]byte, error) {
	if err := validateContractV5JSONEnvelope(payload); err != nil {
		return nil, err
	}
	var historical v5Contract
	if err := decodeExactContractV5JSON(payload, &historical); err != nil {
		return nil, fmt.Errorf("contract IR historical v5 decode: %w", err)
	}
	if err := validateContractV5(historical); err != nil {
		return nil, fmt.Errorf("contract IR historical v5 decode: invalid contract: %w", err)
	}
	canonical := historical
	normalizeContractV5(&canonical)
	reencoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("contract IR historical v5 decode: re-encode: %w", err)
	}
	if !bytes.Equal(reencoded, payload) {
		return nil, fmt.Errorf("contract IR historical v5 decode: document is not in canonical normalized form")
	}
	return reencoded, nil
}

func validateContractV5JSONEnvelope(payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("contract IR historical v5 decode: empty document")
	}
	if len(payload) > maxHistoricalContractV5Bytes {
		return fmt.Errorf("contract IR historical v5 decode: document exceeds %d bytes", maxHistoricalContractV5Bytes)
	}
	if err := rejectDuplicateContractV5JSONKeys(payload); err != nil {
		return fmt.Errorf("contract IR historical v5 decode: %w", err)
	}
	var envelope struct {
		FormatVersion uint16 `json:"formatVersion"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("contract IR historical v5 decode: format version: %w", err)
	}
	if err := requireContractV5JSONEOF(decoder); err != nil {
		return fmt.Errorf("contract IR historical v5 decode: format version: %w", err)
	}
	if envelope.FormatVersion != historicalContractV5FormatVersion {
		return fmt.Errorf("contract IR version %d is unsupported; expected %d", envelope.FormatVersion, historicalContractV5FormatVersion)
	}
	return nil
}

func decodeExactContractV5JSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireContractV5JSONEOF(decoder)
}

func requireContractV5JSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func rejectDuplicateContractV5JSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 1024 {
			return fmt.Errorf("JSON nesting exceeds 1024")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return fmt.Errorf("object is not closed")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return fmt.Errorf("array is not closed")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	return requireContractV5JSONEOF(decoder)
}

func validateContractV5(contract v5Contract) error {
	if contract.FormatVersion != historicalContractV5FormatVersion {
		return fmt.Errorf("format version is %d", contract.FormatVersion)
	}
	if contract.GraphQLABIVersion != 0 && contract.GraphQLABIVersion != 4 {
		return fmt.Errorf("GraphQL ABI version %d is not a released v5 value", contract.GraphQLABIVersion)
	}
	modelIDs := map[ModelID]struct{}{}
	for _, model := range contract.Models {
		if model.ModelID == "" {
			return fmt.Errorf("model identity is empty")
		}
		if _, duplicate := modelIDs[model.ModelID]; duplicate {
			return fmt.Errorf("model %s is duplicated", model.ModelID)
		}
		modelIDs[model.ModelID] = struct{}{}
		fields := map[FieldID]struct{}{}
		for _, field := range model.Fields {
			if field.FieldID == "" {
				return fmt.Errorf("model %s contains an empty field identity", model.ModelID)
			}
			if _, duplicate := fields[field.FieldID]; duplicate {
				return fmt.Errorf("model %s field %s is duplicated", model.ModelID, field.FieldID)
			}
			fields[field.FieldID] = struct{}{}
			modes := map[FieldMode]struct{}{}
			for _, mode := range field.Modes {
				if !v5FieldModes[mode] {
					return fmt.Errorf("model %s field %s has unknown mode %q", model.ModelID, field.FieldID, mode)
				}
				if _, duplicate := modes[mode]; duplicate {
					return fmt.Errorf("model %s field %s repeats mode %q", model.ModelID, field.FieldID, mode)
				}
				modes[mode] = struct{}{}
			}
		}
		selectors := map[KeyID]struct{}{}
		for _, selector := range model.Selectors {
			if !v5KeyKinds[selector.Kind] {
				return fmt.Errorf("model %s selector %s has unknown kind %q", model.ModelID, selector.KeyID, selector.Kind)
			}
			if selector.KeyID == "" {
				return fmt.Errorf("model %s contains an empty selector identity", model.ModelID)
			}
			if _, duplicate := selectors[selector.KeyID]; duplicate {
				return fmt.Errorf("model %s selector %s is duplicated", model.ModelID, selector.KeyID)
			}
			selectors[selector.KeyID] = struct{}{}
		}
		operations := map[Operation]struct{}{}
		for _, operation := range model.Operations {
			if !v5Operations[operation] {
				return fmt.Errorf("model %s has unknown operation %q", model.ModelID, operation)
			}
			if _, duplicate := operations[operation]; duplicate {
				return fmt.Errorf("model %s repeats operation %q", model.ModelID, operation)
			}
			operations[operation] = struct{}{}
		}
		if model.Event != nil {
			if model.Event.Schema.FormatVersion != EventSchemaFormatVersion || model.Event.Schema.ModelID != model.ModelID {
				return fmt.Errorf("model %s has invalid event schema identity/version", model.ModelID)
			}
			for _, field := range append(append([]v5EventField(nil), model.Event.Schema.IdentityFields...), model.Event.Schema.SnapshotFields...) {
				if err := validateV5LogicalType(field.Type, 0); err != nil {
					return fmt.Errorf("model %s event field %s: %w", model.ModelID, field.FieldID, err)
				}
			}
		}
		for _, computed := range model.Computed {
			if err := validateV5GraphQLType(computed.Result, 0); err != nil {
				return fmt.Errorf("model %s computed %s result: %w", model.ModelID, computed.Name, err)
			}
			for _, argument := range computed.Arguments {
				if err := validateV5GraphQLType(argument.Type, 0); err != nil {
					return fmt.Errorf("model %s computed %s argument %s: %w", model.ModelID, computed.Name, argument.Name, err)
				}
			}
		}
	}
	enumIDs := map[EnumID]struct{}{}
	for _, enum := range contract.Enums {
		if enum.EnumID == "" {
			return fmt.Errorf("enum identity is empty")
		}
		if _, duplicate := enumIDs[enum.EnumID]; duplicate {
			return fmt.Errorf("enum %s is duplicated", enum.EnumID)
		}
		enumIDs[enum.EnumID] = struct{}{}
		members := map[EnumValueID]struct{}{}
		for _, member := range enum.Values {
			if member.ValueID == "" {
				return fmt.Errorf("enum %s contains an empty member identity", enum.EnumID)
			}
			if _, duplicate := members[member.ValueID]; duplicate {
				return fmt.Errorf("enum %s member %s is duplicated", enum.EnumID, member.ValueID)
			}
			members[member.ValueID] = struct{}{}
		}
	}
	for _, operation := range contract.CustomOperations {
		if operation.Operation != CustomOperationQuery && operation.Operation != CustomOperationMutation {
			return fmt.Errorf("custom operation %s has unknown kind %q", operation.Name, operation.Operation)
		}
		if operation.Capability != CustomOperationCallerOnly {
			return fmt.Errorf("custom operation %s has unknown capability %q", operation.Name, operation.Capability)
		}
		if err := validateV5GraphQLType(operation.Result, 0); err != nil {
			return fmt.Errorf("custom operation %s result: %w", operation.Name, err)
		}
		for _, argument := range operation.Arguments {
			if err := validateV5GraphQLType(argument.Type, 0); err != nil {
				return fmt.Errorf("custom operation %s argument %s: %w", operation.Name, argument.Name, err)
			}
		}
	}
	return nil
}

var v5FieldModes = map[FieldMode]bool{ModeVisible: true, ModeHidden: true, ModeReadOnly: true, ModeWriteOnly: true, ModeImmutable: true}
var v5KeyKinds = map[KeyKind]bool{KeyPrimary: true, KeyUnique: true}
var v5Operations = map[Operation]bool{
	OperationFindOne: true, OperationFindMany: true, OperationCreate: true, OperationUpdate: true,
	OperationUpsert: true, OperationDelete: true, OperationUpdateMany: true, OperationDeleteMany: true,
	OperationAggregate: true, OperationGroupBy: true, OperationRelationGroupBy: true,
}
var v5GraphQLKinds = map[GraphQLTypeKind]bool{
	GraphQLTypeScalar: true, GraphQLTypeEnum: true, GraphQLTypeModel: true, GraphQLTypeList: true,
	GraphQLTypePredicate: true, GraphQLTypeSelector: true, GraphQLTypeCreateInput: true,
	GraphQLTypeUpdateInput: true, GraphQLTypeUpdateManyInput: true,
}
var v5LogicalKinds = map[LogicalTypeKind]bool{
	TypeBool: true, TypeInt16: true, TypeInt32: true, TypeInt64: true, TypeFloat32: true, TypeFloat64: true,
	TypeDecimal: true, TypeString: true, TypeBytes: true, TypeUUID: true, TypeDate: true, TypeTime: true,
	TypeDateTime: true, TypeJSON: true, TypeEnum: true, TypeScalarList: true,
}

func validateV5GraphQLType(value v5GraphQLType, depth int) error {
	if depth > 64 {
		return fmt.Errorf("type nesting exceeds 64")
	}
	if !v5GraphQLKinds[value.Kind] {
		return fmt.Errorf("unknown GraphQL type kind %q", value.Kind)
	}
	if value.Kind == GraphQLTypeList {
		if value.Element == nil || value.Name != "" {
			return fmt.Errorf("list type requires exactly one unnamed element")
		}
		return validateV5GraphQLType(*value.Element, depth+1)
	}
	if value.Element != nil || value.Name == "" {
		return fmt.Errorf("non-list type %q requires a name and no element", value.Kind)
	}
	return nil
}

func validateV5LogicalType(value v5LogicalType, depth int) error {
	if depth > 64 {
		return fmt.Errorf("logical type nesting exceeds 64")
	}
	if !v5LogicalKinds[value.Kind] {
		return fmt.Errorf("unknown logical type kind %q", value.Kind)
	}
	if value.Kind == TypeScalarList {
		if value.Element == nil {
			return fmt.Errorf("scalar list requires an element")
		}
		return validateV5LogicalType(*value.Element, depth+1)
	}
	if value.Element != nil {
		return fmt.Errorf("non-list logical type %q cannot have an element", value.Kind)
	}
	if value.Kind == TypeEnum && value.EnumID == nil || value.Kind != TypeEnum && value.EnumID != nil {
		return fmt.Errorf("logical enum identity does not match kind %q", value.Kind)
	}
	return nil
}

func normalizeContractV5(contract *v5Contract) {
	sort.Slice(contract.Models, func(i, j int) bool { return contract.Models[i].ModelID < contract.Models[j].ModelID })
	for index := range contract.Models {
		model := &contract.Models[index]
		sort.Slice(model.Fields, func(i, j int) bool { return model.Fields[i].FieldID < model.Fields[j].FieldID })
		for fieldIndex := range model.Fields {
			sort.Slice(model.Fields[fieldIndex].Modes, func(i, j int) bool { return model.Fields[fieldIndex].Modes[i] < model.Fields[fieldIndex].Modes[j] })
			if model.Fields[fieldIndex].Modes == nil {
				model.Fields[fieldIndex].Modes = []FieldMode{}
			}
		}
		sort.Slice(model.HookOwnedCreateFields, func(i, j int) bool { return model.HookOwnedCreateFields[i] < model.HookOwnedCreateFields[j] })
		if len(model.HookOwnedCreateFields) > 1 {
			write := 1
			for read := 1; read < len(model.HookOwnedCreateFields); read++ {
				if model.HookOwnedCreateFields[read] != model.HookOwnedCreateFields[write-1] {
					model.HookOwnedCreateFields[write] = model.HookOwnedCreateFields[read]
					write++
				}
			}
			model.HookOwnedCreateFields = model.HookOwnedCreateFields[:write]
		}
		sort.Slice(model.Selectors, func(i, j int) bool {
			if model.Selectors[i].Name != model.Selectors[j].Name {
				return model.Selectors[i].Name < model.Selectors[j].Name
			}
			return model.Selectors[i].KeyID < model.Selectors[j].KeyID
		})
		for selectorIndex := range model.Selectors {
			if model.Selectors[selectorIndex].Fields == nil {
				model.Selectors[selectorIndex].Fields = []FieldID{}
			}
		}
		sort.Slice(model.Operations, func(i, j int) bool { return model.Operations[i] < model.Operations[j] })
		if model.Aggregation != nil {
			sort.Slice(model.Aggregation.Dimensions, func(i, j int) bool { return model.Aggregation.Dimensions[i] < model.Aggregation.Dimensions[j] })
			sort.Slice(model.Aggregation.Measures, func(i, j int) bool { return model.Aggregation.Measures[i] < model.Aggregation.Measures[j] })
			sort.Slice(model.Aggregation.RelationDimensions, func(i, j int) bool {
				return model.Aggregation.RelationDimensions[i].Name < model.Aggregation.RelationDimensions[j].Name
			})
			if model.Aggregation.Dimensions == nil {
				model.Aggregation.Dimensions = []FieldID{}
			}
			if model.Aggregation.Measures == nil {
				model.Aggregation.Measures = []FieldID{}
			}
			if model.Aggregation.RelationDimensions == nil {
				model.Aggregation.RelationDimensions = []v5RelationDimension{}
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
				model.Event.Schema.IdentityFields = []v5EventField{}
			}
			if model.Event.Schema.SnapshotFields == nil {
				model.Event.Schema.SnapshotFields = []v5EventField{}
			}
			sort.Slice(model.Event.Schema.Enums, func(i, j int) bool { return model.Event.Schema.Enums[i].EnumID < model.Event.Schema.Enums[j].EnumID })
			for enumIndex := range model.Event.Schema.Enums {
				sort.Slice(model.Event.Schema.Enums[enumIndex].Members, func(i, j int) bool {
					return model.Event.Schema.Enums[enumIndex].Members[i] < model.Event.Schema.Enums[enumIndex].Members[j]
				})
				if model.Event.Schema.Enums[enumIndex].Members == nil {
					model.Event.Schema.Enums[enumIndex].Members = []EnumValueID{}
				}
			}
			if model.Event.Schema.Enums == nil {
				model.Event.Schema.Enums = []v5EventEnum{}
			}
		}
		sort.Slice(model.Computed, func(i, j int) bool {
			if model.Computed[i].Name != model.Computed[j].Name {
				return model.Computed[i].Name < model.Computed[j].Name
			}
			return model.Computed[i].ExtensionID < model.Computed[j].ExtensionID
		})
		for computedIndex := range model.Computed {
			computed := &model.Computed[computedIndex]
			sort.Slice(computed.Arguments, func(i, j int) bool { return computed.Arguments[i].Name < computed.Arguments[j].Name })
			sort.Slice(computed.Requires, func(i, j int) bool { return computed.Requires[i] < computed.Requires[j] })
			if computed.Arguments == nil {
				computed.Arguments = []v5GraphQLArgument{}
			}
			if computed.Requires == nil {
				computed.Requires = []FieldID{}
			}
		}
		if model.Fields == nil {
			model.Fields = []v5FieldContract{}
		}
		if model.HookOwnedCreateFields == nil {
			model.HookOwnedCreateFields = []FieldID{}
		}
		if model.Selectors == nil {
			model.Selectors = []v5Selector{}
		}
		if model.Operations == nil {
			model.Operations = []Operation{}
		}
		if model.Computed == nil {
			model.Computed = []v5ComputedField{}
		}
	}
	sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].EnumID < contract.Enums[j].EnumID })
	for enumIndex := range contract.Enums {
		sort.Slice(contract.Enums[enumIndex].Values, func(i, j int) bool {
			return contract.Enums[enumIndex].Values[i].ValueID < contract.Enums[enumIndex].Values[j].ValueID
		})
		if contract.Enums[enumIndex].Values == nil {
			contract.Enums[enumIndex].Values = []v5EnumValue{}
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
			operation.Arguments = []v5GraphQLArgument{}
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
		contract.Models = []v5ModelContract{}
	}
	if contract.Enums == nil {
		contract.Enums = []v5EnumContract{}
	}
	if contract.Methods == nil {
		contract.Methods = []v5AttachedMethod{}
	}
	if contract.CustomOperations == nil {
		contract.CustomOperations = []v5CustomOperation{}
	}
}
