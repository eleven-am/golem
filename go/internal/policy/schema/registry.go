// Package schema owns the one-time, fail-closed conversion from the public
// representation-opaque SchemaBundle into immutable, ID-keyed runtime facts.
package schema

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

// ErrorCode is a stable bootstrap failure category. Detail text is diagnostic
// only; callers should branch on Code.
type ErrorCode string

const (
	CodeBundle      ErrorCode = "P2_SCHEMA_BUNDLE"
	CodeDocument    ErrorCode = "P2_SCHEMA_DOCUMENT"
	CodeFingerprint ErrorCode = "P2_SCHEMA_FINGERPRINT"
	CodeIdentity    ErrorCode = "P2_SCHEMA_IDENTITY"
	CodeModel       ErrorCode = "P2_SCHEMA_MODEL"
	CodeField       ErrorCode = "P2_SCHEMA_FIELD"
	CodeRelation    ErrorCode = "P2_SCHEMA_RELATION"
	CodeContract    ErrorCode = "P2_SCHEMA_CONTRACT"
	CodeProvider    ErrorCode = "P2_SCHEMA_PROVIDER"
	CodePhysical    ErrorCode = "P2_SCHEMA_PHYSICAL"
)

// Error reports a deterministic schema bootstrap failure.
type Error struct {
	Code   ErrorCode
	Path   string
	Detail string
}

func (err *Error) Error() string {
	if err.Path == "" {
		return fmt.Sprintf("%s: %s", err.Code, err.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", err.Code, err.Path, err.Detail)
}

func fail(code ErrorCode, path, format string, args ...any) error {
	return &Error{Code: code, Path: path, Detail: fmt.Sprintf(format, args...)}
}

// Registry contains only privately owned values and maps. It is immutable
// after New returns; every collection-valued accessor returns a copy.
type Registry struct {
	generationDigest    golem.SchemaDigest
	modelFingerprint    golem.SchemaDigest
	contractFingerprint golem.SchemaDigest
	providers           []golem.Provider
	models              map[golem.ModelID]Model
	fields              map[golem.ModelID]map[golem.FieldID]Field
	relations           map[relationKey]RelationEndpoint
	enumValues          map[compilerir.EnumID]map[string]compilerir.EnumValueID
	enumLabels          map[compilerir.EnumID]map[compilerir.EnumValueID]string
	physicalModels      map[golem.Provider]map[golem.ModelID]PhysicalModel
	physicalFields      map[golem.Provider]map[golem.ModelID]map[golem.FieldID]PhysicalField
	physicalNamespaces  map[golem.Provider]physical.PhysicalName
	capabilities        map[golem.Provider]map[compilerir.CapabilityID]physical.CapabilityFact
}

type relationKey struct {
	model    golem.ModelID
	field    golem.FieldID
	relation golem.RelationID
}

func (registry *Registry) GenerationDigest() golem.SchemaDigest { return registry.generationDigest }
func (registry *Registry) ModelFingerprint() golem.SchemaDigest { return registry.modelFingerprint }
func (registry *Registry) ContractFingerprint() golem.SchemaDigest {
	return registry.contractFingerprint
}

func (registry *Registry) Providers() []golem.Provider {
	return append([]golem.Provider(nil), registry.providers...)
}

// HasModel and HasField expose only identity membership for consumers that do
// not need schema facts. Both are nil-safe so an absent bootstrap registry
// fails closed at subsystem boundaries.
func (registry *Registry) HasModel(id golem.ModelID) bool {
	if registry == nil {
		return false
	}
	_, ok := registry.models[id]
	return ok
}

func (registry *Registry) HasField(model golem.ModelID, field golem.FieldID) bool {
	if registry == nil {
		return false
	}
	fields, ok := registry.fields[model]
	if !ok {
		return false
	}
	_, ok = fields[field]
	return ok
}

// Model returns a model only when the fixed-width ID is present in this exact
// fingerprinted registry.
func (registry *Registry) Model(id golem.ModelID) (Model, bool) {
	value, ok := registry.models[id]
	return value, ok
}

// Field rejects both unknown fields and known fields supplied with the wrong
// owning model.
func (registry *Registry) Field(model golem.ModelID, field golem.FieldID) (Field, bool) {
	values, ok := registry.fields[model]
	if !ok {
		return Field{}, false
	}
	value, ok := values[field]
	return value, ok
}

// RelationEndpoint requires all three untrusted identities to agree with one
// normalized endpoint.
func (registry *Registry) RelationEndpoint(model golem.ModelID, field golem.FieldID, relation golem.RelationID) (RelationEndpoint, bool) {
	value, ok := registry.relations[relationKey{model: model, field: field, relation: relation}]
	if !ok {
		return RelationEndpoint{}, false
	}
	return value.clone(), true
}

func (registry *Registry) EnumValue(enum compilerir.EnumID, authoredLabel string) (compilerir.EnumValueID, bool) {
	values, ok := registry.enumValues[enum]
	if !ok {
		return "", false
	}
	value, ok := values[authoredLabel]
	return value, ok
}

func (registry *Registry) EnumLabel(enum compilerir.EnumID, value compilerir.EnumValueID) (string, bool) {
	if registry == nil {
		return "", false
	}
	values, ok := registry.enumLabels[enum]
	if !ok {
		return "", false
	}
	label, ok := values[value]
	return label, ok
}

func (registry *Registry) PhysicalModel(provider golem.Provider, model golem.ModelID) (PhysicalModel, bool) {
	models, ok := registry.physicalModels[provider]
	if !ok {
		return PhysicalModel{}, false
	}
	value, ok := models[model]
	return value, ok
}

func (registry *Registry) PhysicalNamespace(provider golem.Provider) (physical.PhysicalName, bool) {
	if registry == nil {
		return "", false
	}
	value, ok := registry.physicalNamespaces[provider]
	return value, ok
}

func (registry *Registry) PhysicalField(provider golem.Provider, model golem.ModelID, field golem.FieldID) (PhysicalField, bool) {
	models, ok := registry.physicalFields[provider]
	if !ok {
		return PhysicalField{}, false
	}
	fields, ok := models[model]
	if !ok {
		return PhysicalField{}, false
	}
	value, ok := fields[field]
	if !ok {
		return PhysicalField{}, false
	}
	return value.clone(), true
}

func (registry *Registry) Capability(provider golem.Provider, id compilerir.CapabilityID) (physical.CapabilityFact, bool) {
	values, ok := registry.capabilities[provider]
	if !ok {
		return physical.CapabilityFact{}, false
	}
	value, ok := values[id]
	return value, ok
}

// Model is the minimum provider-neutral model fact used by the binder.
type Model struct {
	id golem.ModelID
}

func (model Model) ID() golem.ModelID { return model.id }

// Field is a provider-neutral logical field fact.
type Field struct {
	model        golem.ModelID
	id           golem.FieldID
	kind         compilerir.FieldKind
	logicalType  compilerir.LogicalTypeIR
	nullable     bool
	relation     golem.RelationID
	relationRole compilerir.RelationEndpointRole
}

func (field Field) ModelID() golem.ModelID                { return field.model }
func (field Field) ID() golem.FieldID                     { return field.id }
func (field Field) Kind() compilerir.FieldKind            { return field.kind }
func (field Field) LogicalType() compilerir.LogicalTypeIR { return cloneLogicalType(field.logicalType) }
func (field Field) Nullable() bool                        { return field.nullable }
func (field Field) RelationID() (golem.RelationID, bool) {
	return field.relation, field.kind == compilerir.FieldRelation
}
func (field Field) RelationRole() (compilerir.RelationEndpointRole, bool) {
	return field.relationRole, field.kind == compilerir.FieldRelation
}

// Correlation is one ordered parent-to-child scalar field pair.
type Correlation struct {
	parent golem.FieldID
	child  golem.FieldID
}

func (pair Correlation) ParentFieldID() golem.FieldID { return pair.parent }
func (pair Correlation) ChildFieldID() golem.FieldID  { return pair.child }

// RelationEndpoint is one source or inverse traversal lens.
type RelationEndpoint struct {
	model       golem.ModelID
	field       golem.FieldID
	relation    golem.RelationID
	target      golem.ModelID
	role        compilerir.RelationEndpointRole
	kind        compilerir.RelationKind
	cardinality compilerir.RelationCardinality
	correlation []Correlation
}

func (endpoint RelationEndpoint) ModelID() golem.ModelID                { return endpoint.model }
func (endpoint RelationEndpoint) FieldID() golem.FieldID                { return endpoint.field }
func (endpoint RelationEndpoint) RelationID() golem.RelationID          { return endpoint.relation }
func (endpoint RelationEndpoint) TargetModelID() golem.ModelID          { return endpoint.target }
func (endpoint RelationEndpoint) Role() compilerir.RelationEndpointRole { return endpoint.role }
func (endpoint RelationEndpoint) Kind() compilerir.RelationKind         { return endpoint.kind }
func (endpoint RelationEndpoint) Cardinality() compilerir.RelationCardinality {
	return endpoint.cardinality
}
func (endpoint RelationEndpoint) Correlation() []Correlation {
	return append([]Correlation(nil), endpoint.correlation...)
}
func (endpoint RelationEndpoint) clone() RelationEndpoint {
	endpoint.correlation = append([]Correlation(nil), endpoint.correlation...)
	return endpoint
}

type PhysicalModel struct {
	provider golem.Provider
	model    golem.ModelID
	name     physical.PhysicalName
}

func (model PhysicalModel) Provider() golem.Provider    { return model.provider }
func (model PhysicalModel) ModelID() golem.ModelID      { return model.model }
func (model PhysicalModel) Name() physical.PhysicalName { return model.name }

type PhysicalField struct {
	provider             golem.Provider
	model                golem.ModelID
	field                golem.FieldID
	table                physical.PhysicalName
	column               physical.PhysicalName
	storage              physical.StorageType
	nullable             bool
	requiredCapabilities []physical.CapabilityRequirement
}

func (field PhysicalField) Provider() golem.Provider          { return field.provider }
func (field PhysicalField) ModelID() golem.ModelID            { return field.model }
func (field PhysicalField) FieldID() golem.FieldID            { return field.field }
func (field PhysicalField) TableName() physical.PhysicalName  { return field.table }
func (field PhysicalField) ColumnName() physical.PhysicalName { return field.column }
func (field PhysicalField) Storage() physical.StorageType     { return cloneStorage(field.storage) }
func (field PhysicalField) Nullable() bool                    { return field.nullable }
func (field PhysicalField) RequiredCapabilities() []physical.CapabilityRequirement {
	return append([]physical.CapabilityRequirement(nil), field.requiredCapabilities...)
}
func (field PhysicalField) clone() PhysicalField {
	field.storage = cloneStorage(field.storage)
	field.requiredCapabilities = append([]physical.CapabilityRequirement(nil), field.requiredCapabilities...)
	return field
}

func cloneStorage(value physical.StorageType) physical.StorageType {
	if value.Symbol != nil {
		symbol := *value.Symbol
		value.Symbol = &symbol
	}
	return value
}

func cloneLogicalType(value compilerir.LogicalTypeIR) compilerir.LogicalTypeIR {
	if value.EnumID != nil {
		copy := *value.EnumID
		value.EnumID = &copy
	}
	if value.Element != nil {
		copy := cloneLogicalType(*value.Element)
		value.Element = &copy
	}
	if value.Precision != nil {
		copy := *value.Precision
		value.Precision = &copy
	}
	if value.Scale != nil {
		copy := *value.Scale
		value.Scale = &copy
	}
	if value.MaxLength != nil {
		copy := *value.MaxLength
		value.MaxLength = &copy
	}
	if value.JSONSchemaID != nil {
		copy := *value.JSONSchemaID
		value.JSONSchemaID = &copy
	}
	if value.Capability != nil {
		copy := *value.Capability
		value.Capability = &copy
	}
	return value
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("%q is not a canonical 128-bit lowercase hexadecimal ID", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func modelID(value compilerir.ModelID) (golem.ModelID, error) {
	parsed, err := fixedID(string(value))
	return golem.ModelID(parsed), err
}

func fieldID(value compilerir.FieldID) (golem.FieldID, error) {
	parsed, err := fixedID(string(value))
	return golem.FieldID(parsed), err
}

func relationID(value compilerir.RelationID) (golem.RelationID, error) {
	parsed, err := fixedID(string(value))
	return golem.RelationID(parsed), err
}
