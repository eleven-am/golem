package sql

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type schemaResolver struct{ registry *schema.Registry }

func SchemaResolver(registry *schema.Registry) Resolver { return schemaResolver{registry: registry} }

func (resolver schemaResolver) Providers() ir.ProviderSet {
	if resolver.registry == nil {
		return 0
	}
	providers := make([]ir.Provider, 0, 2)
	for _, provider := range resolver.registry.Providers() {
		if converted, ok := internalProvider(provider); ok {
			providers = append(providers, converted)
		}
	}
	set, _ := ir.NewProviderSet(providers...)
	return set
}

func (resolver schemaResolver) SchemaFingerprint() [32]byte {
	if resolver.registry == nil {
		return [32]byte{}
	}
	return [32]byte(resolver.registry.ModelFingerprint())
}

func (resolver schemaResolver) Model(provider ir.Provider, model ir.ModelID) (Model, bool) {
	external, ok := externalProvider(provider)
	if !ok || resolver.registry == nil {
		return Model{}, false
	}
	physicalModel, ok := resolver.registry.PhysicalModel(external, golem.ModelID(model))
	if !ok {
		return Model{}, false
	}
	namespace, ok := resolver.registry.PhysicalNamespace(external)
	if !ok {
		return Model{}, false
	}
	return Model{ID: model, Namespace: namespace, Table: physicalModel.Name()}, true
}

func (resolver schemaResolver) Field(provider ir.Provider, model ir.ModelID, field ir.FieldID) (Field, bool) {
	external, ok := externalProvider(provider)
	if !ok || resolver.registry == nil {
		return Field{}, false
	}
	logical, ok := resolver.registry.Field(golem.ModelID(model), golem.FieldID(field))
	if !ok || logical.Kind() != compilerir.FieldScalar {
		return Field{}, false
	}
	physicalField, ok := resolver.registry.PhysicalField(external, golem.ModelID(model), golem.FieldID(field))
	if !ok {
		return Field{}, false
	}
	typ, err := policyType(logical.LogicalType(), logical.Nullable())
	if err != nil {
		return Field{}, false
	}
	return Field{Model: model, ID: field, Column: physicalField.ColumnName(), Type: typ, Nullable: logical.Nullable()}, true
}

func (resolver schemaResolver) Relation(model ir.ModelID, field ir.FieldID, relation ir.RelationID) (Relation, bool) {
	if resolver.registry == nil {
		return Relation{}, false
	}
	endpoint, ok := resolver.registry.RelationEndpoint(golem.ModelID(model), golem.FieldID(field), golem.RelationID(relation))
	if !ok {
		return Relation{}, false
	}
	pairs := make([]Correlation, len(endpoint.Correlation()))
	for index, pair := range endpoint.Correlation() {
		pairs[index] = Correlation{Parent: ir.FieldID(pair.ParentFieldID()), Child: ir.FieldID(pair.ChildFieldID())}
	}
	var cardinality ir.RelationCardinality
	switch endpoint.Cardinality() {
	case compilerir.RelationOne:
		cardinality = ir.RelationToOne
	case compilerir.RelationMany:
		cardinality = ir.RelationToMany
	default:
		return Relation{}, false
	}
	return Relation{Model: model, Field: field, ID: relation, Target: ir.ModelID(endpoint.TargetModelID()), Cardinality: cardinality, Pairs: pairs}, true
}

func (resolver schemaResolver) Capability(provider ir.Provider, capability ir.Capability) bool {
	external, ok := externalProvider(provider)
	if !ok || resolver.registry == nil {
		return false
	}
	_, ok = resolver.registry.Capability(external, compilerir.CapabilityID(capabilityName(capability)))
	return ok
}

func (resolver schemaResolver) EnumWire(enum ir.EnumID, value ir.EnumValueID) (string, bool) {
	if resolver.registry == nil {
		return "", false
	}
	return resolver.registry.EnumLabel(compilerir.EnumID(hex.EncodeToString(enum[:])), compilerir.EnumValueID(hex.EncodeToString(value[:])))
}

func externalProvider(provider ir.Provider) (golem.Provider, bool) {
	switch provider {
	case ir.ProviderSQLite:
		return golem.SQLite, true
	case ir.ProviderPostgreSQL:
		return golem.PostgreSQL, true
	default:
		return "", false
	}
}

func internalProvider(provider golem.Provider) (ir.Provider, bool) {
	switch provider {
	case golem.SQLite:
		return ir.ProviderSQLite, true
	case golem.PostgreSQL:
		return ir.ProviderPostgreSQL, true
	default:
		return 0, false
	}
}

func capabilityName(capability ir.Capability) string {
	switch capability {
	case ir.CapabilityBinaryText:
		return "policy.binary-text.v1"
	case ir.CapabilityASCIIInsensitiveText:
		return "policy.ascii-insensitive-text.v1"
	case ir.CapabilityExactJSON:
		return "policy.exact-json.v1"
	case ir.CapabilityScalarListJSON:
		return "scalar-list.json-array.v1"
	case ir.CapabilityRelationCorrelation:
		return "policy.relation-correlation.v1"
	default:
		return ""
	}
}

func policyType(logical compilerir.LogicalTypeIR, nullable bool) (ir.TypeRef, error) {
	kind := mapLogicalKind(logical.Kind)
	var enum ir.EnumID
	if logical.EnumID != nil {
		decoded, err := fixedID(string(*logical.EnumID))
		if err != nil {
			return ir.TypeRef{}, err
		}
		enum = ir.EnumID(decoded)
	}
	var element *ir.TypeRef
	if logical.Element != nil {
		converted, err := policyType(*logical.Element, false)
		if err != nil {
			return ir.TypeRef{}, err
		}
		element = &converted
	}
	var precision, scale uint16
	if logical.Precision != nil {
		precision = *logical.Precision
	}
	if logical.Scale != nil {
		scale = *logical.Scale
	}
	capability := ir.Capability(0)
	if logical.Kind == compilerir.TypeScalarList {
		capability = ir.CapabilityScalarListJSON
	}
	return ir.NewTypeRef(kind, nullable, precision, scale, enum, element, capability)
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("invalid fixed identity %q", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func mapLogicalKind(kind compilerir.LogicalTypeKind) ir.ValueKind {
	switch kind {
	case compilerir.TypeBool:
		return ir.ValueBool
	case compilerir.TypeInt16:
		return ir.ValueInt16
	case compilerir.TypeInt32:
		return ir.ValueInt32
	case compilerir.TypeInt64:
		return ir.ValueInt64
	case compilerir.TypeFloat32:
		return ir.ValueFloat32
	case compilerir.TypeFloat64:
		return ir.ValueFloat64
	case compilerir.TypeDecimal:
		return ir.ValueDecimal
	case compilerir.TypeString:
		return ir.ValueString
	case compilerir.TypeBytes:
		return ir.ValueBytes
	case compilerir.TypeUUID:
		return ir.ValueUUID
	case compilerir.TypeDate:
		return ir.ValueDate
	case compilerir.TypeTime:
		return ir.ValueTime
	case compilerir.TypeDateTime:
		return ir.ValueDateTime
	case compilerir.TypeEnum:
		return ir.ValueEnum
	case compilerir.TypeJSON:
		return ir.ValueJSON
	case compilerir.TypeScalarList:
		return ir.ValueScalarList
	default:
		return 0
	}
}
