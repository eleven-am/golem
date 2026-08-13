package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	historicalModelV1FormatVersion uint16 = 1
	maxCanonicalModelBytes                = 16 << 20
)

// Provenance is the exact pre-v2 source boundary at commit 1f773a9 from
// which the retained ModelIR-v1 JSON shape and canonical normalization were
// copied. The adapted decoder's own digest remains pinned separately.
const (
	historicalModelV1UpstreamCommit          = "1f773a9"
	historicalModelV1TypesUpstreamSHA256     = "20a0d1bd5f3e1fbbd991d8abbca226a33d72afae71bd51703b2c4a88a2cab5b9"
	historicalModelV1TypesUpstreamLines      = 848
	historicalModelV1CanonicalUpstreamSHA256 = "fdcd49abd6935e67cda3e0f7a4d81e1a06622aac9ac3af4a1c5a68dfb56abbcf"
	historicalModelV1CanonicalUpstreamLines  = 770
)

// These DTOs are the complete released ModelIR-v1 JSON vocabulary. They are
// deliberately independent of the mutable current ModelIR structs. In
// particular, v1ModelDecl has no optimisticConcurrency member, including no
// nullable placeholder for it.
type v1Model struct {
	FormatVersion uint16                `json:"formatVersion"`
	Schema        v1SchemaIdentity      `json:"schema"`
	Providers     []Provider            `json:"providers"`
	Enums         []v1Enum              `json:"enums"`
	Models        []v1ModelDecl         `json:"models"`
	Relations     []v1Relation          `json:"relations"`
	Extensions    []v1ProviderExtension `json:"extensions"`
}

type v1SchemaIdentity struct {
	ID           SchemaID      `json:"id"`
	StableName   string        `json:"stableName"`
	PackagePath  string        `json:"packagePath"`
	RootFunction string        `json:"rootFunction"`
	Actor        v1GoNamedType `json:"actor"`
}

type v1GoNamedType struct {
	PackagePath string `json:"packagePath"`
	Name        string `json:"name"`
}

type v1Enum struct {
	ID          EnumID        `json:"id"`
	Go          v1GoNamedType `json:"go"`
	LogicalName string        `json:"logicalName"`
	Values      []v1EnumValue `json:"values"`
}

type v1EnumValue struct {
	ID        EnumValueID `json:"id"`
	GoName    string      `json:"goName"`
	WireValue string      `json:"wireValue"`
}

type v1ModelDecl struct {
	ID                ModelID           `json:"id"`
	CanonicalIdentity string            `json:"canonicalIdentity"`
	Go                v1GoNamedType     `json:"go"`
	LogicalName       string            `json:"logicalName"`
	Table             v1TableBinding    `json:"table"`
	Fields            []v1Field         `json:"fields"`
	PrimaryKey        *v1Key            `json:"primaryKey,omitempty"`
	Uniques           []v1Key           `json:"uniques"`
	Indexes           []v1Index         `json:"indexes"`
	Checks            []v1Check         `json:"checks"`
	EqualityIndexes   []v1EqualityIndex `json:"equalityIndexes"`
}

type v1TableBinding struct {
	PhysicalName SQLIdentifier `json:"physicalName"`
}

type v1Field struct {
	ID                FieldID          `json:"id"`
	CanonicalIdentity string           `json:"canonicalIdentity"`
	GoName            string           `json:"goName"`
	LogicalName       string           `json:"logicalName"`
	DeclarationOrder  uint32           `json:"declarationOrder"`
	Kind              FieldKind        `json:"kind"`
	Scalar            *v1ScalarField   `json:"scalar,omitempty"`
	Relation          *v1RelationField `json:"relation,omitempty"`
}

type v1ScalarField struct {
	Column           SQLIdentifier      `json:"column"`
	Type             v1LogicalType      `json:"type"`
	Nullable         bool               `json:"nullable"`
	Default          *v1Default         `json:"default,omitempty"`
	Generation       *v1GeneratedColumn `json:"generation,omitempty"`
	Updated          bool               `json:"updated"`
	DatabaseReadOnly bool               `json:"databaseReadOnly"`
}

type v1LogicalType struct {
	Kind         LogicalTypeKind `json:"kind"`
	EnumID       *EnumID         `json:"enumId,omitempty"`
	Element      *v1LogicalType  `json:"element,omitempty"`
	Precision    *uint16         `json:"precision,omitempty"`
	Scale        *uint16         `json:"scale,omitempty"`
	MaxLength    *uint32         `json:"maxLength,omitempty"`
	JSONSchemaID *string         `json:"jsonSchemaId,omitempty"`
	Capability   *CapabilityID   `json:"capability,omitempty"`
}

type v1TypedLiteral struct {
	Kind      LiteralKind `json:"kind"`
	Canonical string      `json:"canonical"`
}

type v1ProviderSymbolRef struct {
	Provider Provider `json:"provider"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Version  uint16   `json:"version"`
}

type v1Default struct {
	Kind     DefaultKind          `json:"kind"`
	Producer DefaultProducer      `json:"producer"`
	Literal  *v1TypedLiteral      `json:"literal,omitempty"`
	Provider *v1ProviderSymbolRef `json:"provider,omitempty"`
}

type v1Key struct {
	ID           KeyID         `json:"id"`
	Kind         KeyKind       `json:"kind"`
	LogicalName  string        `json:"logicalName"`
	PhysicalName SQLIdentifier `json:"physicalName"`
	Fields       []FieldID     `json:"fields"`
}

type v1SchemaSymbolRef struct {
	Identity      string           `json:"identity"`
	Kind          SchemaSymbolKind `json:"kind"`
	Name          string           `json:"name"`
	Version       uint16           `json:"version"`
	Provider      ProviderScope    `json:"provider"`
	Volatility    SchemaVolatility `json:"volatility"`
	Deterministic bool             `json:"deterministic"`
}

type v1SchemaExpr struct {
	Kind              SchemaExprKind     `json:"kind"`
	CanonicalIdentity string             `json:"canonicalIdentity"`
	ResultType        v1LogicalType      `json:"resultType"`
	Nullable          bool               `json:"nullable"`
	Provider          ProviderScope      `json:"provider"`
	Volatility        SchemaVolatility   `json:"volatility"`
	Deterministic     bool               `json:"deterministic"`
	Symbol            *v1SchemaSymbolRef `json:"symbol,omitempty"`
	Field             *FieldID           `json:"field,omitempty"`
	Literal           *v1TypedLiteral    `json:"literal,omitempty"`
	Operands          []v1SchemaExpr     `json:"operands"`
	ReferencedFields  []FieldID          `json:"referencedFields"`
}

type v1SchemaPredicate struct {
	Kind               SchemaPredicateKind `json:"kind"`
	CanonicalIdentity  string              `json:"canonicalIdentity"`
	ResultType         v1LogicalType       `json:"resultType"`
	Nullable           bool                `json:"nullable"`
	Provider           ProviderScope       `json:"provider"`
	Volatility         SchemaVolatility    `json:"volatility"`
	Deterministic      bool                `json:"deterministic"`
	Symbol             *v1SchemaSymbolRef  `json:"symbol,omitempty"`
	Constant           *bool               `json:"constant,omitempty"`
	ExpressionOperands []v1SchemaExpr      `json:"expressionOperands"`
	Children           []v1SchemaPredicate `json:"children"`
	ReferencedFields   []FieldID           `json:"referencedFields"`
}

type v1Check struct {
	ID           CheckID           `json:"id"`
	PhysicalName SQLIdentifier     `json:"physicalName"`
	Predicate    v1SchemaPredicate `json:"predicate"`
	Provider     ProviderScope     `json:"provider"`
}

type v1GeneratedColumn struct {
	Expr     v1SchemaExpr     `json:"expr"`
	Storage  GeneratedStorage `json:"storage"`
	Provider ProviderScope    `json:"provider"`
}

type v1IndexKey struct {
	Column    *FieldID             `json:"column,omitempty"`
	Expr      *v1SchemaExpr        `json:"expr,omitempty"`
	Direction SortDirection        `json:"direction"`
	Nulls     NullsOrder           `json:"nulls"`
	Collation *string              `json:"collation,omitempty"`
	OpClass   *v1ProviderSymbolRef `json:"opClass,omitempty"`
}

type v1Index struct {
	ID           IndexID            `json:"id"`
	ModelID      ModelID            `json:"modelId"`
	PhysicalName SQLIdentifier      `json:"physicalName"`
	Unique       bool               `json:"unique"`
	Method       IndexMethod        `json:"method"`
	Keys         []v1IndexKey       `json:"keys"`
	Include      []FieldID          `json:"include"`
	Predicate    *v1SchemaPredicate `json:"predicate,omitempty"`
	Provider     ProviderScope      `json:"provider"`
}

type v1EqualityIndex struct {
	FieldID FieldID            `json:"fieldId"`
	Kind    EqualityAccessKind `json:"kind"`
	KeyID   *KeyID             `json:"keyId,omitempty"`
	IndexID *IndexID           `json:"indexId,omitempty"`
}

type v1RelationField struct {
	RelationID RelationID           `json:"relationId"`
	Role       RelationEndpointRole `json:"role"`
	Kind       RelationKind         `json:"kind"`
}

type v1ForeignKey struct {
	ID           ForeignKeyID      `json:"id"`
	PhysicalName SQLIdentifier     `json:"physicalName"`
	OnUpdate     ReferentialAction `json:"onUpdate"`
	OnDelete     ReferentialAction `json:"onDelete"`
	Match        MatchKind         `json:"match"`
	Deferrable   Deferrability     `json:"deferrable"`
}

type v1ThroughRelation struct {
	ModelID ModelID `json:"modelId"`
}

type v1Relation struct {
	ID           RelationID          `json:"id"`
	Name         string              `json:"name"`
	SourceModel  ModelID             `json:"sourceModel"`
	TargetModel  ModelID             `json:"targetModel"`
	SourceField  FieldID             `json:"sourceField"`
	InverseField *FieldID            `json:"inverseField,omitempty"`
	Cardinality  RelationCardinality `json:"cardinality"`
	LocalFields  []FieldID           `json:"localFields"`
	RemoteFields []FieldID           `json:"remoteFields"`
	ForeignKey   *v1ForeignKey       `json:"foreignKey,omitempty"`
	Through      *v1ThroughRelation  `json:"through,omitempty"`
}

type v1ProviderExtension struct {
	ID       ExtensionID `json:"id"`
	Provider Provider    `json:"provider"`
	Version  uint16      `json:"version"`
	Owner    ObjectID    `json:"owner"`
	Kind     string      `json:"kind"`
	Payload  string      `json:"payload"`
}

// CanonicalDecodeModelV1 is the only decoder for released ModelIR-v1 bytes.
// It validates and canonicalizes with the retained v1 vocabulary before
// projecting into current memory. Current-only fields therefore remain absent
// by construction and cannot be smuggled by relabelling v2 JSON.
func CanonicalDecodeModelV1(payload []byte) (ModelIR, error) {
	canonical, err := decodeModelV1(payload)
	if err != nil {
		return ModelIR{}, err
	}
	var current ModelIR
	if err := json.Unmarshal(canonical, &current); err != nil {
		return ModelIR{}, fmt.Errorf("model IR historical v1 decode: project retained DTO: %w", err)
	}
	current.FormatVersion = ModelFormatVersion
	for index := range current.Models {
		current.Models[index].OptimisticConcurrency = nil
	}
	return current, nil
}

// ModelFingerprintV1 verifies exact released v1 canonical bytes before
// applying the unchanged v1 fingerprint domain.
func ModelFingerprintV1(payload []byte) (Fingerprint, error) {
	canonical, err := decodeModelV1(payload)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:model-fingerprint:v1", canonical), nil
}

func decodeModelV1(payload []byte) ([]byte, error) {
	if err := validateModelJSONEnvelope(payload, historicalModelV1FormatVersion); err != nil {
		return nil, err
	}
	var historical v1Model
	if err := decodeExactModelJSON(payload, &historical); err != nil {
		return nil, fmt.Errorf("model IR historical v1 decode: %w", err)
	}
	if err := validateModelV1(historical); err != nil {
		return nil, fmt.Errorf("model IR historical v1 decode: invalid model: %w", err)
	}
	canonical := historical
	normalizeModelV1(&canonical)
	reencoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("model IR historical v1 decode: re-encode: %w", err)
	}
	if !bytes.Equal(reencoded, payload) {
		return nil, fmt.Errorf("model IR historical v1 decode: document is not in canonical normalized form")
	}
	return reencoded, nil
}

func validateModelJSONEnvelope(payload []byte, expected uint16) error {
	if len(payload) == 0 {
		return fmt.Errorf("model IR decode: empty document")
	}
	if len(payload) > maxCanonicalModelBytes {
		return fmt.Errorf("model IR decode: document exceeds %d bytes", maxCanonicalModelBytes)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return fmt.Errorf("model IR decode: %w", err)
	}
	var envelope struct {
		FormatVersion uint16 `json:"formatVersion"`
	}
	if err := decodeExactModelJSON(payload, &envelope); err != nil {
		// The envelope intentionally permits later fields; decode only the
		// version with the ordinary decoder after duplicate-key preflight.
		var loose struct {
			FormatVersion uint16 `json:"formatVersion"`
		}
		if decodeErr := json.Unmarshal(payload, &loose); decodeErr != nil {
			return fmt.Errorf("model IR decode: format version: %w", decodeErr)
		}
		envelope = loose
	}
	if envelope.FormatVersion != expected {
		return fmt.Errorf("model IR version %d is unsupported; expected %d", envelope.FormatVersion, expected)
	}
	return nil
}

func decodeExactModelJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func validateModelV1(model v1Model) error {
	if model.FormatVersion != historicalModelV1FormatVersion {
		return fmt.Errorf("format version is %d", model.FormatVersion)
	}
	providers := map[Provider]bool{}
	for _, provider := range model.Providers {
		if provider != SQLite && provider != PostgreSQL {
			return fmt.Errorf("unknown provider %q", provider)
		}
		if providers[provider] {
			return fmt.Errorf("provider %q is duplicated", provider)
		}
		providers[provider] = true
	}
	enums := map[EnumID]bool{}
	enumValues := map[EnumValueID]bool{}
	for _, enum := range model.Enums {
		if enum.ID == "" || enums[enum.ID] {
			return fmt.Errorf("enum identity %q is empty or duplicated", enum.ID)
		}
		enums[enum.ID] = true
		for _, value := range enum.Values {
			if value.ID == "" || enumValues[value.ID] {
				return fmt.Errorf("enum value identity %q is empty or duplicated", value.ID)
			}
			enumValues[value.ID] = true
		}
	}
	models := map[ModelID]bool{}
	fields := map[FieldID]ModelID{}
	for _, modelDecl := range model.Models {
		if modelDecl.ID == "" || models[modelDecl.ID] {
			return fmt.Errorf("model identity %q is empty or duplicated", modelDecl.ID)
		}
		models[modelDecl.ID] = true
		localFields := map[FieldID]bool{}
		for _, field := range modelDecl.Fields {
			if field.ID == "" || localFields[field.ID] {
				return fmt.Errorf("model %s field identity %q is empty or duplicated", modelDecl.ID, field.ID)
			}
			if owner, duplicate := fields[field.ID]; duplicate {
				return fmt.Errorf("field %s belongs to both %s and %s", field.ID, owner, modelDecl.ID)
			}
			fields[field.ID] = modelDecl.ID
			localFields[field.ID] = true
			if err := validateV1Field(field, enums); err != nil {
				return fmt.Errorf("model %s field %s: %w", modelDecl.ID, field.ID, err)
			}
		}
		if err := validateV1KeysAndIndexes(modelDecl, localFields, enums); err != nil {
			return fmt.Errorf("model %s: %w", modelDecl.ID, err)
		}
	}
	relations := map[RelationID]bool{}
	for _, relation := range model.Relations {
		if relation.ID == "" || relations[relation.ID] || !models[relation.SourceModel] || !models[relation.TargetModel] {
			return fmt.Errorf("relation %q has an empty identity or unknown model", relation.ID)
		}
		relations[relation.ID] = true
		if fields[relation.SourceField] != relation.SourceModel || relation.InverseField != nil && fields[*relation.InverseField] != relation.TargetModel {
			return fmt.Errorf("relation %s has an unknown or wrong-owner endpoint field", relation.ID)
		}
		if relation.Cardinality != RelationOne && relation.Cardinality != RelationMany {
			return fmt.Errorf("relation %s has unknown cardinality %q", relation.ID, relation.Cardinality)
		}
		if len(relation.LocalFields) == 0 || len(relation.LocalFields) != len(relation.RemoteFields) {
			return fmt.Errorf("relation %s has invalid correlation arity", relation.ID)
		}
		for index := range relation.LocalFields {
			if fields[relation.LocalFields[index]] != relation.SourceModel || fields[relation.RemoteFields[index]] != relation.TargetModel {
				return fmt.Errorf("relation %s has an unknown or wrong-owner correlation field", relation.ID)
			}
		}
		if relation.ForeignKey != nil {
			if !v1Actions[relation.ForeignKey.OnUpdate] || !v1Actions[relation.ForeignKey.OnDelete] || relation.ForeignKey.Match != MatchSimple || !v1Deferrabilities[relation.ForeignKey.Deferrable] {
				return fmt.Errorf("relation %s has an unknown foreign-key value", relation.ID)
			}
		}
	}
	extensions := map[ExtensionID]bool{}
	for _, extension := range model.Extensions {
		if extension.ID == "" || extensions[extension.ID] || extension.Version == 0 || extension.Owner == "" || (extension.Provider != SQLite && extension.Provider != PostgreSQL) {
			return fmt.Errorf("extension %q has an invalid identity/provider/version", extension.ID)
		}
		extensions[extension.ID] = true
	}
	return nil
}

func validateV1Field(field v1Field, enums map[EnumID]bool) error {
	switch field.Kind {
	case FieldScalar, FieldEnum, FieldScalarList:
		if field.Scalar == nil || field.Relation != nil {
			return fmt.Errorf("scalar-like field requires exactly one scalar payload")
		}
		if err := validateV1LogicalType(field.Scalar.Type, enums, 0); err != nil {
			return err
		}
		if field.Kind == FieldEnum && field.Scalar.Type.Kind != TypeEnum || field.Kind == FieldScalarList && field.Scalar.Type.Kind != TypeScalarList || field.Kind == FieldScalar && (field.Scalar.Type.Kind == TypeEnum || field.Scalar.Type.Kind == TypeScalarList) {
			return fmt.Errorf("field kind %q disagrees with logical type %q", field.Kind, field.Scalar.Type.Kind)
		}
		if field.Scalar.Default != nil {
			value := field.Scalar.Default
			if !v1DefaultKinds[value.Kind] || !v1DefaultProducers[value.Producer] {
				return fmt.Errorf("unknown default kind/producer")
			}
			if value.Literal != nil && !v1LiteralKinds[value.Literal.Kind] {
				return fmt.Errorf("unknown literal kind %q", value.Literal.Kind)
			}
		}
		if field.Scalar.Generation != nil {
			if !v1GeneratedStorage[field.Scalar.Generation.Storage] || !v1ProviderScopes[field.Scalar.Generation.Provider] {
				return fmt.Errorf("unknown generated-column value")
			}
			if err := validateV1Expr(field.Scalar.Generation.Expr, enums, 0); err != nil {
				return err
			}
		}
	case FieldRelation:
		if field.Relation == nil || field.Scalar != nil || !v1RelationRoles[field.Relation.Role] || !v1RelationKinds[field.Relation.Kind] {
			return fmt.Errorf("relation field has an invalid payload")
		}
	default:
		return fmt.Errorf("unknown field kind %q", field.Kind)
	}
	return nil
}

func validateV1LogicalType(value v1LogicalType, enums map[EnumID]bool, depth int) error {
	if depth > 64 || !v1LogicalKinds[value.Kind] {
		return fmt.Errorf("unknown or excessively nested logical type %q", value.Kind)
	}
	if value.Kind == TypeScalarList {
		if value.Element == nil {
			return fmt.Errorf("scalar list requires an element")
		}
		return validateV1LogicalType(*value.Element, enums, depth+1)
	}
	if value.Element != nil {
		return fmt.Errorf("non-list logical type %q carries an element", value.Kind)
	}
	if value.Kind == TypeEnum {
		if value.EnumID == nil || !enums[*value.EnumID] {
			return fmt.Errorf("enum type references an unknown enum")
		}
	} else if value.EnumID != nil {
		return fmt.Errorf("non-enum logical type carries enum identity")
	}
	return nil
}

func validateV1Expr(expr v1SchemaExpr, enums map[EnumID]bool, depth int) error {
	if depth > 64 || !v1ExprKinds[expr.Kind] || !v1ProviderScopes[expr.Provider] || !v1Volatilities[expr.Volatility] {
		return fmt.Errorf("unknown or excessively nested schema expression")
	}
	if err := validateV1LogicalType(expr.ResultType, enums, depth+1); err != nil {
		return err
	}
	if expr.Symbol != nil && (!v1SymbolKinds[expr.Symbol.Kind] || !v1ProviderScopes[expr.Symbol.Provider] || !v1Volatilities[expr.Symbol.Volatility] || expr.Symbol.Version == 0) {
		return fmt.Errorf("schema expression carries an unknown symbol")
	}
	for _, operand := range expr.Operands {
		if err := validateV1Expr(operand, enums, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateV1Predicate(predicate v1SchemaPredicate, enums map[EnumID]bool, depth int) error {
	if depth > 64 || !v1PredicateKinds[predicate.Kind] || !v1ProviderScopes[predicate.Provider] || !v1Volatilities[predicate.Volatility] {
		return fmt.Errorf("unknown or excessively nested schema predicate")
	}
	if err := validateV1LogicalType(predicate.ResultType, enums, depth+1); err != nil {
		return err
	}
	if predicate.Symbol != nil && (!v1SymbolKinds[predicate.Symbol.Kind] || !v1ProviderScopes[predicate.Symbol.Provider] || !v1Volatilities[predicate.Symbol.Volatility] || predicate.Symbol.Version == 0) {
		return fmt.Errorf("schema predicate carries an unknown symbol")
	}
	for _, operand := range predicate.ExpressionOperands {
		if err := validateV1Expr(operand, enums, depth+1); err != nil {
			return err
		}
	}
	for _, child := range predicate.Children {
		if err := validateV1Predicate(child, enums, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateV1KeysAndIndexes(model v1ModelDecl, fields map[FieldID]bool, enums map[EnumID]bool) error {
	validateKey := func(key v1Key) error {
		if key.ID == "" || !v1KeyKinds[key.Kind] || len(key.Fields) == 0 {
			return fmt.Errorf("key %q has invalid identity/kind/arity", key.ID)
		}
		for _, field := range key.Fields {
			if !fields[field] {
				return fmt.Errorf("key %s references unknown field %s", key.ID, field)
			}
		}
		return nil
	}
	if model.PrimaryKey != nil {
		if err := validateKey(*model.PrimaryKey); err != nil || model.PrimaryKey.Kind != KeyPrimary {
			return fmt.Errorf("invalid primary key: %v", err)
		}
	}
	for _, key := range model.Uniques {
		if err := validateKey(key); err != nil || key.Kind != KeyUnique {
			return fmt.Errorf("invalid unique key: %v", err)
		}
	}
	for _, index := range model.Indexes {
		if index.ID == "" || index.ModelID != model.ID || !v1IndexMethods[index.Method] || !v1ProviderScopes[index.Provider] || len(index.Keys) == 0 {
			return fmt.Errorf("index %q has invalid identity/model/method/provider/arity", index.ID)
		}
		for _, key := range index.Keys {
			if (key.Column == nil) == (key.Expr == nil) || !v1SortDirections[key.Direction] || !v1NullOrders[key.Nulls] {
				return fmt.Errorf("index %s has invalid key", index.ID)
			}
			if key.Column != nil && !fields[*key.Column] {
				return fmt.Errorf("index %s references unknown field %s", index.ID, *key.Column)
			}
			if key.Expr != nil {
				if err := validateV1Expr(*key.Expr, enums, 0); err != nil {
					return fmt.Errorf("index %s expression: %w", index.ID, err)
				}
			}
			if key.OpClass != nil && (key.OpClass.Provider != SQLite && key.OpClass.Provider != PostgreSQL || key.OpClass.Version == 0) {
				return fmt.Errorf("index %s has invalid operator class", index.ID)
			}
		}
		for _, field := range index.Include {
			if !fields[field] {
				return fmt.Errorf("index %s includes unknown field %s", index.ID, field)
			}
		}
		if index.Predicate != nil {
			if err := validateV1Predicate(*index.Predicate, enums, 0); err != nil {
				return fmt.Errorf("index %s predicate: %w", index.ID, err)
			}
		}
	}
	for _, check := range model.Checks {
		if check.ID == "" || !v1ProviderScopes[check.Provider] {
			return fmt.Errorf("check %q has invalid identity/provider", check.ID)
		}
		if err := validateV1Predicate(check.Predicate, enums, 0); err != nil {
			return err
		}
	}
	for _, equality := range model.EqualityIndexes {
		if !fields[equality.FieldID] || !v1EqualityKinds[equality.Kind] || (equality.KeyID == nil) == (equality.IndexID == nil) {
			return fmt.Errorf("field %s has invalid equality index", equality.FieldID)
		}
	}
	return nil
}

func normalizeModelV1(model *v1Model) {
	sort.Slice(model.Providers, func(i, j int) bool { return providerRankV1(model.Providers[i]) < providerRankV1(model.Providers[j]) })
	sort.Slice(model.Enums, func(i, j int) bool { return model.Enums[i].ID < model.Enums[j].ID })
	for index := range model.Enums {
		if model.Enums[index].Values == nil {
			model.Enums[index].Values = []v1EnumValue{}
		}
	}
	sort.Slice(model.Models, func(i, j int) bool { return model.Models[i].ID < model.Models[j].ID })
	for index := range model.Models {
		normalizeModelDeclV1(&model.Models[index])
	}
	sort.Slice(model.Relations, func(i, j int) bool { return model.Relations[i].ID < model.Relations[j].ID })
	for index := range model.Relations {
		if model.Relations[index].LocalFields == nil {
			model.Relations[index].LocalFields = []FieldID{}
		}
		if model.Relations[index].RemoteFields == nil {
			model.Relations[index].RemoteFields = []FieldID{}
		}
	}
	sort.Slice(model.Extensions, func(i, j int) bool {
		left, right := model.Extensions[i], model.Extensions[j]
		if left.Provider != right.Provider {
			return providerRankV1(left.Provider) < providerRankV1(right.Provider)
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
		model.Enums = []v1Enum{}
	}
	if model.Models == nil {
		model.Models = []v1ModelDecl{}
	}
	if model.Relations == nil {
		model.Relations = []v1Relation{}
	}
	if model.Extensions == nil {
		model.Extensions = []v1ProviderExtension{}
	}
}

func normalizeModelDeclV1(model *v1ModelDecl) {
	sort.Slice(model.Fields, func(i, j int) bool { return model.Fields[i].ID < model.Fields[j].ID })
	sort.Slice(model.Uniques, func(i, j int) bool { return model.Uniques[i].ID < model.Uniques[j].ID })
	sort.Slice(model.Indexes, func(i, j int) bool { return model.Indexes[i].ID < model.Indexes[j].ID })
	sort.Slice(model.Checks, func(i, j int) bool { return model.Checks[i].ID < model.Checks[j].ID })
	sort.Slice(model.EqualityIndexes, func(i, j int) bool {
		left, right := model.EqualityIndexes[i], model.EqualityIndexes[j]
		if left.FieldID != right.FieldID {
			return left.FieldID < right.FieldID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return v1EqualityIdentity(left) < v1EqualityIdentity(right)
	})
	if model.Fields == nil {
		model.Fields = []v1Field{}
	}
	if model.Uniques == nil {
		model.Uniques = []v1Key{}
	}
	if model.Indexes == nil {
		model.Indexes = []v1Index{}
	}
	if model.Checks == nil {
		model.Checks = []v1Check{}
	}
	if model.EqualityIndexes == nil {
		model.EqualityIndexes = []v1EqualityIndex{}
	}
	if model.PrimaryKey != nil && model.PrimaryKey.Fields == nil {
		model.PrimaryKey.Fields = []FieldID{}
	}
	for index := range model.Uniques {
		if model.Uniques[index].Fields == nil {
			model.Uniques[index].Fields = []FieldID{}
		}
	}
	for index := range model.Indexes {
		entry := &model.Indexes[index]
		if entry.Keys == nil {
			entry.Keys = []v1IndexKey{}
		}
		if entry.Include == nil {
			entry.Include = []FieldID{}
		}
		for keyIndex := range entry.Keys {
			if entry.Keys[keyIndex].Expr != nil {
				normalizeExprV1(entry.Keys[keyIndex].Expr)
			}
		}
		if entry.Predicate != nil {
			normalizePredicateV1(entry.Predicate)
		}
	}
	for index := range model.Fields {
		if model.Fields[index].Scalar != nil && model.Fields[index].Scalar.Generation != nil {
			normalizeExprV1(&model.Fields[index].Scalar.Generation.Expr)
		}
	}
	for index := range model.Checks {
		normalizePredicateV1(&model.Checks[index].Predicate)
	}
}

func normalizeExprV1(expr *v1SchemaExpr) {
	for index := range expr.Operands {
		normalizeExprV1(&expr.Operands[index])
	}
	sort.Slice(expr.ReferencedFields, func(i, j int) bool { return expr.ReferencedFields[i] < expr.ReferencedFields[j] })
	if expr.Operands == nil {
		expr.Operands = []v1SchemaExpr{}
	}
	if expr.ReferencedFields == nil {
		expr.ReferencedFields = []FieldID{}
	}
}

func normalizePredicateV1(predicate *v1SchemaPredicate) {
	for index := range predicate.ExpressionOperands {
		normalizeExprV1(&predicate.ExpressionOperands[index])
	}
	for index := range predicate.Children {
		normalizePredicateV1(&predicate.Children[index])
	}
	sort.Slice(predicate.ReferencedFields, func(i, j int) bool { return predicate.ReferencedFields[i] < predicate.ReferencedFields[j] })
	if predicate.ExpressionOperands == nil {
		predicate.ExpressionOperands = []v1SchemaExpr{}
	}
	if predicate.Children == nil {
		predicate.Children = []v1SchemaPredicate{}
	}
	if predicate.ReferencedFields == nil {
		predicate.ReferencedFields = []FieldID{}
	}
}

func v1EqualityIdentity(value v1EqualityIndex) string {
	if value.KeyID != nil {
		return "key:" + string(*value.KeyID)
	}
	if value.IndexID != nil {
		return "index:" + string(*value.IndexID)
	}
	return ""
}

func providerRankV1(provider Provider) int {
	switch provider {
	case SQLite:
		return 0
	case PostgreSQL:
		return 1
	default:
		return 100
	}
}

var (
	v1LogicalKinds     = map[LogicalTypeKind]bool{TypeBool: true, TypeInt16: true, TypeInt32: true, TypeInt64: true, TypeFloat32: true, TypeFloat64: true, TypeDecimal: true, TypeString: true, TypeBytes: true, TypeUUID: true, TypeDate: true, TypeTime: true, TypeDateTime: true, TypeJSON: true, TypeEnum: true, TypeScalarList: true}
	v1DefaultKinds     = map[DefaultKind]bool{DefaultLiteral: true, DefaultIdentity: true, DefaultUUID: true, DefaultNow: true, DefaultProvider: true}
	v1DefaultProducers = map[DefaultProducer]bool{ProducerDatabase: true, ProducerApplication: true, ProducerProvider: true}
	v1LiteralKinds     = map[LiteralKind]bool{LiteralBool: true, LiteralInteger: true, LiteralFloat: true, LiteralDecimal: true, LiteralString: true, LiteralBytes: true, LiteralUUID: true, LiteralDate: true, LiteralTime: true, LiteralDateTime: true, LiteralJSON: true, LiteralEnum: true, LiteralList: true}
	v1KeyKinds         = map[KeyKind]bool{KeyPrimary: true, KeyUnique: true}
	v1ProviderScopes   = map[ProviderScope]bool{ProviderScopePortable: true, ProviderScopeSQLite: true, ProviderScopePostgreSQL: true}
	v1ExprKinds        = map[SchemaExprKind]bool{SchemaExprField: true, SchemaExprLiteral: true, SchemaExprOperator: true, SchemaExprFunction: true, SchemaExprCast: true}
	v1PredicateKinds   = map[SchemaPredicateKind]bool{SchemaPredicateConstant: true, SchemaPredicateOperator: true, SchemaPredicateAnd: true, SchemaPredicateOr: true, SchemaPredicateNot: true}
	v1SymbolKinds      = map[SchemaSymbolKind]bool{SchemaSymbolOperator: true, SchemaSymbolFunction: true, SchemaSymbolCast: true}
	v1Volatilities     = map[SchemaVolatility]bool{SchemaVolatilityImmutable: true, SchemaVolatilityStable: true, SchemaVolatilityVolatile: true}
	v1GeneratedStorage = map[GeneratedStorage]bool{GeneratedStored: true, GeneratedVirtual: true}
	v1IndexMethods     = map[IndexMethod]bool{IndexBTree: true}
	v1SortDirections   = map[SortDirection]bool{SortAsc: true, SortDesc: true}
	v1NullOrders       = map[NullsOrder]bool{NullsDefault: true, NullsFirst: true, NullsLast: true}
	v1EqualityKinds    = map[EqualityAccessKind]bool{EqualityViaKey: true, EqualityViaIndex: true}
	v1RelationRoles    = map[RelationEndpointRole]bool{RelationSource: true, RelationInverse: true}
	v1RelationKinds    = map[RelationKind]bool{RelationBelongsTo: true, RelationHasOne: true, RelationHasMany: true}
	v1Actions          = map[ReferentialAction]bool{ActionNoAction: true, ActionRestrict: true, ActionCascade: true, ActionSetNull: true, ActionSetDefault: true}
	v1Deferrabilities  = map[Deferrability]bool{NotDeferrable: true, InitiallyImmediate: true, InitiallyDeferred: true}
)
