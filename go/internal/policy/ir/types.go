// Package ir defines the closed, provider-neutral policy representation.
//
// Values in this package are immutable after construction. All constructors
// copy caller-owned data and all collection accessors return copies. Schema
// binding and operator semantics live in sibling packages; this package owns
// stable identities, variants, structural invariants, and canonical identity.
package ir

import "fmt"

const CanonicalFormatVersion uint16 = 1

type (
	ModelID             [16]byte
	FieldID             [16]byte
	RelationID          [16]byte
	EnumID              [16]byte
	EnumValueID         [16]byte
	OperatorID          uint16
	Capability          uint16
	ProviderSet         uint8
	Action              uint8
	Effect              uint8
	ConditionKind       uint8
	LogicalOperator     uint8
	ComparisonMode      uint8
	Provider            uint8
	ValueKind           uint8
	OperandKind         uint8
	JSONKind            uint8
	JSONNullKind        uint8
	RelationCardinality uint8
)

const (
	ActionRead   Action = 1
	ActionCreate Action = 2
	ActionUpdate Action = 3
	ActionDelete Action = 4
)

const (
	EffectGrant Effect = 1
	EffectDeny  Effect = 2
)

const (
	ConditionConstant ConditionKind = 1
	ConditionLogical  ConditionKind = 2
	ConditionScalar   ConditionKind = 3
	ConditionList     ConditionKind = 4
	ConditionJSON     ConditionKind = 5
	ConditionRelation ConditionKind = 6
)

const (
	LogicalAnd LogicalOperator = 1
	LogicalOr  LogicalOperator = 2
	LogicalNot LogicalOperator = 3
)

const (
	ComparisonSensitive        ComparisonMode = 1
	ComparisonASCIIInsensitive ComparisonMode = 2
)

const (
	ProviderSQLite     Provider = 1
	ProviderPostgreSQL Provider = 2
)

const (
	ValueBool       ValueKind = 1
	ValueInt16      ValueKind = 2
	ValueInt32      ValueKind = 3
	ValueInt64      ValueKind = 4
	ValueFloat32    ValueKind = 5
	ValueFloat64    ValueKind = 6
	ValueDecimal    ValueKind = 7
	ValueString     ValueKind = 8
	ValueBytes      ValueKind = 9
	ValueUUID       ValueKind = 10
	ValueDate       ValueKind = 11
	ValueTime       ValueKind = 12
	ValueDateTime   ValueKind = 13
	ValueEnum       ValueKind = 14
	ValueJSON       ValueKind = 15
	ValueScalarList ValueKind = 16
)

const (
	OperandNone     OperandKind = 1
	OperandOne      OperandKind = 2
	OperandMany     OperandKind = 3
	OperandFlag     OperandKind = 4
	OperandJSONNull OperandKind = 5
)

const (
	JSONNull   JSONKind = 1
	JSONBool   JSONKind = 2
	JSONNumber JSONKind = 3
	JSONString JSONKind = 4
	JSONArray  JSONKind = 5
	JSONObject JSONKind = 6
)

const (
	JSONDbNull       JSONNullKind = 1
	JSONDocumentNull JSONNullKind = 2
	JSONAnyNull      JSONNullKind = 3
)

const (
	RelationToOne  RelationCardinality = 1
	RelationToMany RelationCardinality = 2
)

// Operator identities are persisted ABI. Explicit assignments prevent a new
// entry from renumbering existing policy fingerprints.
const (
	OperatorEqual              OperatorID = 1
	OperatorNotEqual           OperatorID = 2
	OperatorIn                 OperatorID = 3
	OperatorNotIn              OperatorID = 4
	OperatorLessThan           OperatorID = 5
	OperatorLessThanOrEqual    OperatorID = 6
	OperatorGreaterThan        OperatorID = 7
	OperatorGreaterThanOrEqual OperatorID = 8
	OperatorContains           OperatorID = 9
	OperatorStartsWith         OperatorID = 10
	OperatorEndsWith           OperatorID = 11
	OperatorIsNull             OperatorID = 12
	OperatorIsNotNull          OperatorID = 13

	OperatorListEqual     OperatorID = 101
	OperatorListHas       OperatorID = 102
	OperatorListHasEvery  OperatorID = 103
	OperatorListHasSome   OperatorID = 104
	OperatorListIsEmpty   OperatorID = 105
	OperatorListIsNull    OperatorID = 106
	OperatorListIsNotNull OperatorID = 107

	OperatorJSONIsNull             OperatorID = 201
	OperatorJSONIsNotNull          OperatorID = 202
	OperatorJSONEqual              OperatorID = 203
	OperatorJSONNotEqual           OperatorID = 204
	OperatorJSONLessThan           OperatorID = 205
	OperatorJSONLessThanOrEqual    OperatorID = 206
	OperatorJSONGreaterThan        OperatorID = 207
	OperatorJSONGreaterThanOrEqual OperatorID = 208
	OperatorJSONStringContains     OperatorID = 209
	OperatorJSONStringStartsWith   OperatorID = 210
	OperatorJSONStringEndsWith     OperatorID = 211
	OperatorJSONArrayContains      OperatorID = 212
	OperatorJSONArrayStartsWith    OperatorID = 213
	OperatorJSONArrayEndsWith      OperatorID = 214

	OperatorRelationIs        OperatorID = 301
	OperatorRelationIsNot     OperatorID = 302
	OperatorRelationIsNull    OperatorID = 303
	OperatorRelationIsNotNull OperatorID = 304
	OperatorRelationSome      OperatorID = 305
	OperatorRelationEvery     OperatorID = 306
	OperatorRelationNone      OperatorID = 307
)

// Runtime capabilities use one closed numeric identity table. Schema bootstrap
// is responsible for mapping P1 capability strings to these identities.
const (
	CapabilityBinaryText           Capability = 1
	CapabilityASCIIInsensitiveText Capability = 2
	CapabilityExactJSON            Capability = 3
	CapabilityScalarListJSON       Capability = 4
	CapabilityRelationCorrelation  Capability = 5
)

const (
	providerSQLiteBit     ProviderSet = 1 << 0
	providerPostgreSQLBit ProviderSet = 1 << 1
	allProviderBits                   = providerSQLiteBit | providerPostgreSQLBit
)

func NewProviderSet(providers ...Provider) (ProviderSet, error) {
	var set ProviderSet
	for _, provider := range providers {
		switch provider {
		case ProviderSQLite:
			set |= providerSQLiteBit
		case ProviderPostgreSQL:
			set |= providerPostgreSQLBit
		default:
			return 0, fmt.Errorf("policy IR: unknown provider %d", provider)
		}
	}
	if set == 0 {
		return 0, fmt.Errorf("policy IR: provider set must not be empty")
	}
	return set, nil
}

func PortableProviders() ProviderSet { return allProviderBits }

func (set ProviderSet) Valid() bool { return set != 0 && set&^allProviderBits == 0 }

func (set ProviderSet) Contains(provider Provider) bool {
	switch provider {
	case ProviderSQLite:
		return set&providerSQLiteBit != 0
	case ProviderPostgreSQL:
		return set&providerPostgreSQLBit != 0
	default:
		return false
	}
}

func (set ProviderSet) IsSubsetOf(other ProviderSet) bool {
	return set.Valid() && other.Valid() && set&^other == 0
}

func (set ProviderSet) Providers() []Provider {
	providers := make([]Provider, 0, 2)
	if set.Contains(ProviderSQLite) {
		providers = append(providers, ProviderSQLite)
	}
	if set.Contains(ProviderPostgreSQL) {
		providers = append(providers, ProviderPostgreSQL)
	}
	return providers
}

func validAction(action Action) bool { return action >= ActionRead && action <= ActionDelete }
func validEffect(effect Effect) bool { return effect == EffectGrant || effect == EffectDeny }
func validMode(mode ComparisonMode) bool {
	return mode == ComparisonSensitive || mode == ComparisonASCIIInsensitive
}
func validValueKind(kind ValueKind) bool { return kind >= ValueBool && kind <= ValueScalarList }
func validCapability(capability Capability) bool {
	return capability >= CapabilityBinaryText && capability <= CapabilityRelationCorrelation
}
func validOperator(operator OperatorID) bool {
	return operator >= OperatorEqual && operator <= OperatorIsNotNull ||
		operator >= OperatorListEqual && operator <= OperatorListIsNotNull ||
		operator >= OperatorJSONIsNull && operator <= OperatorJSONArrayEndsWith ||
		operator >= OperatorRelationIs && operator <= OperatorRelationNone
}
