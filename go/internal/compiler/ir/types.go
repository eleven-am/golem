// Package ir defines the versioned, provider-neutral compiler interchange
// representations. It intentionally contains data only: source loading,
// provider lowering, and code generation live in other packages.
package ir

import (
	"slices"
	"sort"
)

const (
	RawDeclFormatVersion uint16 = 2
	ModelFormatVersion   uint16 = 2
	// OptimisticConcurrencyModelFormatVersionRequired freezes the first
	// ModelIR format that owns the optimistic-concurrency field identity.
	// Released v1 documents remain owned by CanonicalDecodeModelV1.
	OptimisticConcurrencyModelFormatVersionRequired uint16 = 2
	ContractFormatVersion                           uint16 = 6
	// OptimisticConcurrencyContractFormatVersionRequired freezes the first
	// ContractIR format that carries the optimistic-concurrency projection.
	// Released v5 documents remain owned by CanonicalDecodeContractV5.
	OptimisticConcurrencyContractFormatVersionRequired uint16 = 6
	// CanonicalFormatVersion versions the deterministic ModelIR and ContractIR
	// encodings used for fingerprints and generated runtime schema bundles.
	CanonicalFormatVersion uint16 = 1
)

type (
	SchemaID      string
	ModelID       string
	FieldID       string
	RelationID    string
	EnumID        string
	EnumValueID   string
	KeyID         string
	IndexID       string
	CheckID       string
	ForeignKeyID  string
	ExtensionID   string
	ObjectID      string
	CapabilityID  string
	SQLIdentifier string
)

type Provider string

const (
	SQLite     Provider = "sqlite"
	PostgreSQL Provider = "postgresql"
)

// SourceSpan is module-relative source evidence. Absolute paths are forbidden.
type SourceSpan struct {
	ModulePath   string `json:"modulePath,omitempty"`
	RelativeFile string `json:"relativeFile,omitempty"`
	StartLine    uint32 `json:"startLine,omitempty"`
	StartColumn  uint32 `json:"startColumn,omitempty"`
	EndLine      uint32 `json:"endLine,omitempty"`
	EndColumn    uint32 `json:"endColumn,omitempty"`
}

type RawDeclIR struct {
	FormatVersion uint16          `json:"formatVersion"`
	Root          RawSchemaDecl   `json:"root"`
	Models        []RawModelDecl  `json:"models"`
	Enums         []RawEnumDecl   `json:"enums"`
	Methods       []RawMethodDecl `json:"methods"`
}

type RawSchemaDecl struct {
	PackagePath     string              `json:"packagePath"`
	FunctionName    string              `json:"functionName"`
	ParameterName   string              `json:"parameterName"`
	SchemaName      string              `json:"schemaName"`
	SchemaNameSpan  SourceSpan          `json:"schemaNameSpan"`
	Actor           *RawNamedTypeRef    `json:"actor,omitempty"`
	Providers       []RawProviderRef    `json:"providers"`
	EmbeddingSpaces []RawEmbeddingSpace `json:"embeddingSpaces"`
	Models          []RawModelRef       `json:"models"`
	Span            SourceSpan          `json:"span"`
}

type RawEmbeddingSpace struct {
	Name       string     `json:"name"`
	Dimensions uint16     `json:"dimensions"`
	Span       SourceSpan `json:"span"`
}

type RawNamedTypeRef struct {
	PackagePath string     `json:"packagePath"`
	GoName      string     `json:"goName"`
	Span        SourceSpan `json:"span"`
}

// RawProviderRef retains authored evidence for one typed provider constant.
// Normalized ModelIR later sorts providers by the fixed registry order.
type RawProviderRef struct {
	Provider Provider   `json:"provider"`
	Ordinal  uint32     `json:"ordinal"`
	Span     SourceSpan `json:"span"`
}

// RawModelRef is one Model[T](schema) registration. FieldName and marker
// attributes belonged to the rejected root-struct proposal and are deliberately
// absent from the accepted function-root ABI.
type RawModelRef struct {
	PackagePath string     `json:"packagePath"`
	GoName      string     `json:"goName"`
	Ordinal     uint32     `json:"ordinal"`
	Span        SourceSpan `json:"span"`
}

type RawModelDecl struct {
	PackagePath string             `json:"packagePath"`
	GoName      string             `json:"goName"`
	Marker      []RawAttribute     `json:"marker"`
	Fields      []RawFieldDecl     `json:"fields"`
	Directives  []RawDirectiveDecl `json:"directives"`
	Span        SourceSpan         `json:"span"`
}

type RawFieldDecl struct {
	GoName     string         `json:"goName"`
	TypeSyntax string         `json:"typeSyntax"`
	GoType     RawGoTypeRef   `json:"goType"`
	DBTag      *string        `json:"dbTag,omitempty"`
	GolemAttrs []RawAttribute `json:"golemAttributes"`
	IsBlank    bool           `json:"isBlank"`
	Span       SourceSpan     `json:"span"`
}

type RawGoTypeKind string

const (
	RawGoTypeBuiltin       RawGoTypeKind = "builtin"
	RawGoTypeNamed         RawGoTypeKind = "named"
	RawGoTypePointer       RawGoTypeKind = "pointer"
	RawGoTypeSlice         RawGoTypeKind = "slice"
	RawGoTypeInstantiation RawGoTypeKind = "instantiation"
)

// RawGoTypeRef is canonical, alias-independent Go type evidence. Named and
// instantiated types use canonical package paths. Pointer and slice nodes have
// exactly one ordered argument; instantiations retain authored type-argument
// order. TypeSyntax remains alongside it only for diagnostics.
type RawGoTypeRef struct {
	Kind        RawGoTypeKind  `json:"kind"`
	PackagePath string         `json:"packagePath,omitempty"`
	GoName      string         `json:"goName,omitempty"`
	Args        []RawGoTypeRef `json:"args"`
	Span        SourceSpan     `json:"span"`
}

type RawAttribute struct {
	Name     string     `json:"name"`
	RawValue *string    `json:"rawValue,omitempty"`
	Span     SourceSpan `json:"span"`
}

type RawDirectiveDecl struct {
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Components []string       `json:"components"`
	Attributes []RawAttribute `json:"attributes"`
	Span       SourceSpan     `json:"span"`
}

type RawEnumDecl struct {
	PackagePath string         `json:"packagePath"`
	GoName      string         `json:"goName"`
	Underlying  string         `json:"underlying"`
	Values      []RawEnumValue `json:"values"`
	Method      RawMethodRef   `json:"method"`
	Span        SourceSpan     `json:"span"`
}

type RawEnumValue struct {
	GoName      string     `json:"goName"`
	WireValue   string     `json:"wireValue"`
	GraphQLName *string    `json:"graphqlName,omitempty"`
	StableID    *string    `json:"stableId,omitempty"`
	Ordinal     uint32     `json:"ordinal"`
	Span        SourceSpan `json:"span"`
}

type RawMethodRef struct {
	ReceiverPackage string     `json:"receiverPackage"`
	ReceiverGoName  string     `json:"receiverGoName"`
	Name            string     `json:"name"`
	Span            SourceSpan `json:"span"`
}

type RawMethodDecl struct {
	ReceiverPackage string     `json:"receiverPackage"`
	ReceiverGoName  string     `json:"receiverGoName"`
	Name            string     `json:"name"`
	Signature       string     `json:"signature"`
	BodySyntax      string     `json:"bodySyntax"`
	Span            SourceSpan `json:"span"`
}

type ModelIR struct {
	FormatVersion uint16                `json:"formatVersion"`
	Schema        SchemaIdentityIR      `json:"schema"`
	Providers     []Provider            `json:"providers"`
	Enums         []EnumIR              `json:"enums"`
	Models        []ModelDeclIR         `json:"models"`
	Relations     []RelationIR          `json:"relations"`
	Extensions    []ProviderExtensionIR `json:"extensions"`
}

type SchemaIdentityIR struct {
	ID           SchemaID      `json:"id"`
	StableName   string        `json:"stableName"`
	PackagePath  string        `json:"packagePath"`
	RootFunction string        `json:"rootFunction"`
	Actor        GoNamedTypeIR `json:"actor"`
}

type GoNamedTypeIR struct {
	PackagePath string `json:"packagePath"`
	Name        string `json:"name"`
}

type EnumIR struct {
	ID          EnumID        `json:"id"`
	Go          GoNamedTypeIR `json:"go"`
	LogicalName string        `json:"logicalName"`
	Values      []EnumValueIR `json:"values"`
}

type EnumValueIR struct {
	ID        EnumValueID `json:"id"`
	GoName    string      `json:"goName"`
	WireValue string      `json:"wireValue"`
}

type ModelDeclIR struct {
	ID                ModelID           `json:"id"`
	CanonicalIdentity string            `json:"canonicalIdentity"`
	Go                GoNamedTypeIR     `json:"go"`
	LogicalName       string            `json:"logicalName"`
	Table             TableBindingIR    `json:"table"`
	Fields            []FieldIR         `json:"fields"`
	PrimaryKey        *KeyIR            `json:"primaryKey,omitempty"`
	Uniques           []KeyIR           `json:"uniques"`
	Indexes           []IndexIR         `json:"indexes"`
	Checks            []CheckIR         `json:"checks"`
	EqualityIndexes   []EqualityIndexIR `json:"equalityIndexes"`
	// OptimisticConcurrency is the sole provider-neutral owner of the stable
	// field identity selected by the explicit model declaration. Downstream
	// contracts project and validate this identity; they do not infer it.
	OptimisticConcurrency *FieldID `json:"optimisticConcurrency,omitempty"`
}

type TableBindingIR struct {
	PhysicalName SQLIdentifier `json:"physicalName"`
}

type FieldKind string

const (
	FieldScalar     FieldKind = "scalar"
	FieldEnum       FieldKind = "enum"
	FieldScalarList FieldKind = "scalarList"
	FieldRelation   FieldKind = "relation"
)

type FieldIR struct {
	ID                FieldID          `json:"id"`
	CanonicalIdentity string           `json:"canonicalIdentity"`
	GoName            string           `json:"goName"`
	LogicalName       string           `json:"logicalName"`
	DeclarationOrder  uint32           `json:"declarationOrder"`
	Kind              FieldKind        `json:"kind"`
	Scalar            *ScalarFieldIR   `json:"scalar,omitempty"`
	Relation          *RelationFieldIR `json:"relation,omitempty"`
}

type ScalarFieldIR struct {
	Column           SQLIdentifier      `json:"column"`
	Type             LogicalTypeIR      `json:"type"`
	Nullable         bool               `json:"nullable"`
	Default          *DefaultIR         `json:"default,omitempty"`
	Generation       *GeneratedColumnIR `json:"generation,omitempty"`
	Updated          bool               `json:"updated"`
	DatabaseReadOnly bool               `json:"databaseReadOnly"`
}

type LogicalTypeKind string

const (
	TypeBool       LogicalTypeKind = "bool"
	TypeInt16      LogicalTypeKind = "int16"
	TypeInt32      LogicalTypeKind = "int32"
	TypeInt64      LogicalTypeKind = "int64"
	TypeFloat32    LogicalTypeKind = "float32"
	TypeFloat64    LogicalTypeKind = "float64"
	TypeDecimal    LogicalTypeKind = "decimal"
	TypeString     LogicalTypeKind = "string"
	TypeBytes      LogicalTypeKind = "bytes"
	TypeUUID       LogicalTypeKind = "uuid"
	TypeDate       LogicalTypeKind = "date"
	TypeTime       LogicalTypeKind = "time"
	TypeDateTime   LogicalTypeKind = "dateTime"
	TypeJSON       LogicalTypeKind = "json"
	TypeEnum       LogicalTypeKind = "enum"
	TypeScalarList LogicalTypeKind = "scalarList"
)

var scalarGraphQLNameByKind = map[LogicalTypeKind]string{
	TypeBool: "Boolean", TypeInt16: "Int", TypeInt32: "Int", TypeInt64: "BigInt",
	TypeFloat32: "Float", TypeFloat64: "Float", TypeDecimal: "Decimal", TypeString: "String",
	TypeBytes: "Bytes", TypeUUID: "UUID", TypeDate: "Date", TypeTime: "Time",
	TypeDateTime: "DateTime", TypeJSON: "JSON",
}

func ScalarGraphQLName(kind LogicalTypeKind) (string, bool) {
	name, ok := scalarGraphQLNameByKind[kind]
	return name, ok
}

func ScalarGraphQLNames() []string {
	names := make([]string, 0, len(scalarGraphQLNameByKind))
	for _, name := range scalarGraphQLNameByKind {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func IsScalarGraphQLName(name string) bool {
	return slices.Contains(ScalarGraphQLNames(), name)
}

type LogicalTypeIR struct {
	Kind         LogicalTypeKind `json:"kind"`
	EnumID       *EnumID         `json:"enumId,omitempty"`
	Element      *LogicalTypeIR  `json:"element,omitempty"`
	Precision    *uint16         `json:"precision,omitempty"`
	Scale        *uint16         `json:"scale,omitempty"`
	MaxLength    *uint32         `json:"maxLength,omitempty"`
	JSONSchemaID *string         `json:"jsonSchemaId,omitempty"`
	Capability   *CapabilityID   `json:"capability,omitempty"`
}

type DefaultKind string
type DefaultProducer string

const (
	DefaultLiteral  DefaultKind = "literal"
	DefaultIdentity DefaultKind = "identity"
	DefaultUUID     DefaultKind = "uuid"
	DefaultNow      DefaultKind = "now"
	DefaultProvider DefaultKind = "provider"

	ProducerDatabase    DefaultProducer = "database"
	ProducerApplication DefaultProducer = "application"
	ProducerProvider    DefaultProducer = "provider"
)

type LiteralKind string

const (
	LiteralBool     LiteralKind = "bool"
	LiteralInteger  LiteralKind = "integer"
	LiteralFloat    LiteralKind = "float"
	LiteralDecimal  LiteralKind = "decimal"
	LiteralString   LiteralKind = "string"
	LiteralBytes    LiteralKind = "bytes"
	LiteralUUID     LiteralKind = "uuid"
	LiteralDate     LiteralKind = "date"
	LiteralTime     LiteralKind = "time"
	LiteralDateTime LiteralKind = "dateTime"
	LiteralJSON     LiteralKind = "json"
	LiteralEnum     LiteralKind = "enum"
	LiteralList     LiteralKind = "list"
)

// TypedLiteralIR stores a canonical, type-directed textual form. Bytes use
// unpadded base64 and JSON/list values use canonical JSON.
type TypedLiteralIR struct {
	Kind      LiteralKind `json:"kind"`
	Canonical string      `json:"canonical"`
}

type ProviderSymbolRef struct {
	Provider Provider `json:"provider"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Version  uint16   `json:"version"`
}

type DefaultIR struct {
	Kind     DefaultKind        `json:"kind"`
	Producer DefaultProducer    `json:"producer"`
	Literal  *TypedLiteralIR    `json:"literal,omitempty"`
	Provider *ProviderSymbolRef `json:"provider,omitempty"`
}

type KeyKind string

const (
	KeyPrimary KeyKind = "primary"
	KeyUnique  KeyKind = "unique"
)

type KeyIR struct {
	ID           KeyID         `json:"id"`
	Kind         KeyKind       `json:"kind"`
	LogicalName  string        `json:"logicalName"`
	PhysicalName SQLIdentifier `json:"physicalName"`
	Fields       []FieldID     `json:"fields"`
}

type ProviderScope string

const (
	ProviderScopePortable   ProviderScope = "portable"
	ProviderScopeSQLite     ProviderScope = "sqlite"
	ProviderScopePostgreSQL ProviderScope = "postgresql"
)

type SchemaExprKind string
type SchemaPredicateKind string
type SchemaSymbolKind string
type SchemaVolatility string

const (
	SchemaExprField    SchemaExprKind = "field"
	SchemaExprLiteral  SchemaExprKind = "literal"
	SchemaExprOperator SchemaExprKind = "operator"
	SchemaExprFunction SchemaExprKind = "function"
	SchemaExprCast     SchemaExprKind = "cast"

	SchemaPredicateConstant SchemaPredicateKind = "constant"
	SchemaPredicateOperator SchemaPredicateKind = "operator"
	SchemaPredicateAnd      SchemaPredicateKind = "and"
	SchemaPredicateOr       SchemaPredicateKind = "or"
	SchemaPredicateNot      SchemaPredicateKind = "not"

	SchemaSymbolOperator SchemaSymbolKind = "operator"
	SchemaSymbolFunction SchemaSymbolKind = "function"
	SchemaSymbolCast     SchemaSymbolKind = "cast"

	SchemaVolatilityImmutable SchemaVolatility = "immutable"
	SchemaVolatilityStable    SchemaVolatility = "stable"
	SchemaVolatilityVolatile  SchemaVolatility = "volatile"
)

// SchemaSymbolRef identifies a registered semantic operator, function, or
// cast. Identity is the registry's durable canonical identity; Name is only its
// diagnostic spelling. I-B validates arity, types, provider availability, and
// the consistency of volatility and determinism declarations.
type SchemaSymbolRef struct {
	Identity      string           `json:"identity"`
	Kind          SchemaSymbolKind `json:"kind"`
	Name          string           `json:"name"`
	Version       uint16           `json:"version"`
	Provider      ProviderScope    `json:"provider"`
	Volatility    SchemaVolatility `json:"volatility"`
	Deterministic bool             `json:"deterministic"`
}

// SchemaExprIR is the closed provider-neutral value-expression shape. Operand
// order is semantic. ReferencedFields is a canonical set used by planners and
// generated dependency metadata; I-B proves that it agrees with the tree.
type SchemaExprIR struct {
	Kind              SchemaExprKind   `json:"kind"`
	CanonicalIdentity string           `json:"canonicalIdentity"`
	ResultType        LogicalTypeIR    `json:"resultType"`
	Nullable          bool             `json:"nullable"`
	Provider          ProviderScope    `json:"provider"`
	Volatility        SchemaVolatility `json:"volatility"`
	Deterministic     bool             `json:"deterministic"`
	Symbol            *SchemaSymbolRef `json:"symbol,omitempty"`
	Field             *FieldID         `json:"field,omitempty"`
	Literal           *TypedLiteralIR  `json:"literal,omitempty"`
	Operands          []SchemaExprIR   `json:"operands"`
	ReferencedFields  []FieldID        `json:"referencedFields"`
}

// SchemaPredicateIR is separate from the P2 authorization predicate language.
// ExpressionOperands are ordered comparison/function operands; Children are
// ordered predicate operands for logical composition.
type SchemaPredicateIR struct {
	Kind               SchemaPredicateKind `json:"kind"`
	CanonicalIdentity  string              `json:"canonicalIdentity"`
	ResultType         LogicalTypeIR       `json:"resultType"`
	Nullable           bool                `json:"nullable"`
	Provider           ProviderScope       `json:"provider"`
	Volatility         SchemaVolatility    `json:"volatility"`
	Deterministic      bool                `json:"deterministic"`
	Symbol             *SchemaSymbolRef    `json:"symbol,omitempty"`
	Constant           *bool               `json:"constant,omitempty"`
	ExpressionOperands []SchemaExprIR      `json:"expressionOperands"`
	Children           []SchemaPredicateIR `json:"children"`
	ReferencedFields   []FieldID           `json:"referencedFields"`
}

type CheckIR struct {
	ID           CheckID           `json:"id"`
	PhysicalName SQLIdentifier     `json:"physicalName"`
	Predicate    SchemaPredicateIR `json:"predicate"`
	Provider     ProviderScope     `json:"provider"`
}

type GeneratedStorage string

const (
	GeneratedStored  GeneratedStorage = "stored"
	GeneratedVirtual GeneratedStorage = "virtual"
)

type GeneratedColumnIR struct {
	Expr     SchemaExprIR     `json:"expr"`
	Storage  GeneratedStorage `json:"storage"`
	Provider ProviderScope    `json:"provider"`
}

type SortDirection string
type NullsOrder string
type IndexMethod string

const (
	SortAsc      SortDirection = "asc"
	SortDesc     SortDirection = "desc"
	NullsDefault NullsOrder    = "default"
	NullsFirst   NullsOrder    = "first"
	NullsLast    NullsOrder    = "last"
	IndexBTree   IndexMethod   = "btree"
)

type IndexKeyIR struct {
	Column    *FieldID           `json:"column,omitempty"`
	Expr      *SchemaExprIR      `json:"expr,omitempty"`
	Direction SortDirection      `json:"direction"`
	Nulls     NullsOrder         `json:"nulls"`
	Collation *string            `json:"collation,omitempty"`
	OpClass   *ProviderSymbolRef `json:"opClass,omitempty"`
}

type IndexIR struct {
	ID           IndexID            `json:"id"`
	ModelID      ModelID            `json:"modelId"`
	PhysicalName SQLIdentifier      `json:"physicalName"`
	Unique       bool               `json:"unique"`
	Method       IndexMethod        `json:"method"`
	Keys         []IndexKeyIR       `json:"keys"`
	Include      []FieldID          `json:"include"`
	Predicate    *SchemaPredicateIR `json:"predicate,omitempty"`
	Provider     ProviderScope      `json:"provider"`
}

type EqualityAccessKind string

const (
	EqualityViaKey   EqualityAccessKind = "key"
	EqualityViaIndex EqualityAccessKind = "index"
)

// EqualityIndexIR records the normalized access path that makes equality on a
// field indexed. Only a leading plain column appears here; expression and
// include fields never do. Exactly one of KeyID and IndexID is populated.
type EqualityIndexIR struct {
	FieldID FieldID            `json:"fieldId"`
	Kind    EqualityAccessKind `json:"kind"`
	KeyID   *KeyID             `json:"keyId,omitempty"`
	IndexID *IndexID           `json:"indexId,omitempty"`
}

type RelationEndpointRole string
type RelationKind string
type RelationCardinality string

const (
	RelationSource    RelationEndpointRole = "source"
	RelationInverse   RelationEndpointRole = "inverse"
	RelationBelongsTo RelationKind         = "belongsTo"
	RelationHasOne    RelationKind         = "hasOne"
	RelationHasMany   RelationKind         = "hasMany"
	RelationOne       RelationCardinality  = "one"
	RelationMany      RelationCardinality  = "many"
)

type RelationFieldIR struct {
	RelationID RelationID           `json:"relationId"`
	Role       RelationEndpointRole `json:"role"`
	Kind       RelationKind         `json:"kind"`
}

type ReferentialAction string
type MatchKind string
type Deferrability string

const (
	ActionNoAction     ReferentialAction = "noAction"
	ActionRestrict     ReferentialAction = "restrict"
	ActionCascade      ReferentialAction = "cascade"
	ActionSetNull      ReferentialAction = "setNull"
	ActionSetDefault   ReferentialAction = "setDefault"
	MatchSimple        MatchKind         = "simple"
	NotDeferrable      Deferrability     = "notDeferrable"
	InitiallyImmediate Deferrability     = "initiallyImmediate"
	InitiallyDeferred  Deferrability     = "initiallyDeferred"
)

type ForeignKeyIR struct {
	ID           ForeignKeyID      `json:"id"`
	PhysicalName SQLIdentifier     `json:"physicalName"`
	OnUpdate     ReferentialAction `json:"onUpdate"`
	OnDelete     ReferentialAction `json:"onDelete"`
	Match        MatchKind         `json:"match"`
	Deferrable   Deferrability     `json:"deferrable"`
}

type ThroughRelationIR struct {
	ModelID ModelID `json:"modelId"`
}

type RelationIR struct {
	ID           RelationID          `json:"id"`
	Name         string              `json:"name"`
	SourceModel  ModelID             `json:"sourceModel"`
	TargetModel  ModelID             `json:"targetModel"`
	SourceField  FieldID             `json:"sourceField"`
	InverseField *FieldID            `json:"inverseField,omitempty"`
	Cardinality  RelationCardinality `json:"cardinality"`
	LocalFields  []FieldID           `json:"localFields"`
	RemoteFields []FieldID           `json:"remoteFields"`
	ForeignKey   *ForeignKeyIR       `json:"foreignKey,omitempty"`
	Through      *ThroughRelationIR  `json:"through,omitempty"`
}

type ProviderExtensionIR struct {
	ID       ExtensionID `json:"id"`
	Provider Provider    `json:"provider"`
	Version  uint16      `json:"version"`
	Owner    ObjectID    `json:"owner"`
	Kind     string      `json:"kind"`
	Payload  string      `json:"payload"`
}

type CompilationIR struct {
	Model    ModelIR    `json:"model"`
	Contract ContractIR `json:"contract"`
}

type ContractIR struct {
	FormatVersion     uint16                      `json:"formatVersion"`
	GraphQLABIVersion uint16                      `json:"graphqlAbiVersion"`
	Models            []ModelContractIR           `json:"models"`
	Enums             []EnumContractIR            `json:"enums"`
	Methods           []AttachedMethodIR          `json:"methods"`
	CustomOperations  []CustomOperationContractIR `json:"customOperations"`
}

type ModelContractIR struct {
	ModelID       ModelID            `json:"modelId"`
	GraphQLName   string             `json:"graphqlName"`
	GraphQLPlural string             `json:"graphqlPlural"`
	Roots         GraphQLRootNamesIR `json:"roots"`
	Fields        []FieldContractIR  `json:"fields"`
	// OptimisticConcurrency is a validated projection of the ModelIR owner. It
	// is never inferred from contract names or field exposure.
	//
	// ContractIR v6 first serialized this projection. The retained v5 decoder
	// has a separate DTO without this member and rejects relabelled v6 bytes.
	OptimisticConcurrency *FieldID `json:"optimisticConcurrency,omitempty"`
	// HookOwnedCreateFields are stable scalar identities omitted only from
	// GraphQL create-shaped inputs and populated by BeforeCreate.
	HookOwnedCreateFields []FieldID            `json:"hookOwnedCreateFields"`
	Selectors             []SelectorContractIR `json:"selectors"`
	// FieldModes is a bootstrap source-compatibility field. It is not serialized;
	// normalized producers and consumers use Fields.
	FieldModes    []FieldContractIR         `json:"-"`
	Operations    []Operation               `json:"operations"`
	Subscriptions bool                      `json:"subscriptions"`
	Event         *EventContractIR          `json:"event,omitempty"`
	Aggregation   *AggregationContractIR    `json:"aggregation,omitempty"`
	ScopedReads   bool                      `json:"scopedReads"`
	Limits        LimitContractIR           `json:"limits"`
	Computed      []ComputedFieldContractIR `json:"computed"`
	Exposed       bool                      `json:"exposed"`
}

const EventSchemaFormatVersion uint16 = 1

// EventContractIR contains the generated/transport-facing names plus the
// closed logical schema used to decode durable facts. Names deliberately live
// outside EventSchemaShapeIR so transport-only renames cannot alter its digest.
type EventContractIR struct {
	PayloadTypeName    string             `json:"payloadTypeName"`
	IdentityTypeName   string             `json:"identityTypeName,omitempty"`
	MetadataFields     []string           `json:"metadataFields"`
	DeleteSnapshotFull bool               `json:"deleteSnapshotFull"`
	Schema             EventSchemaShapeIR `json:"schema"`
	SchemaFingerprint  Fingerprint        `json:"schemaFingerprint"`
}

// EventSchemaShapeIR is the provider-neutral, GraphQL-independent schema for
// one model's event identity and private pre-delete snapshot. IdentityFields
// preserve primary-key component order. SnapshotFields preserve declared model
// field order. Enum inventories are canonicalized by stable identity.
type EventSchemaShapeIR struct {
	FormatVersion  uint16               `json:"formatVersion"`
	ModelID        ModelID              `json:"modelId"`
	PrimaryKeyID   KeyID                `json:"primaryKeyId"`
	IdentityFields []EventFieldSchemaIR `json:"identityFields"`
	SnapshotFields []EventFieldSchemaIR `json:"snapshotFields"`
	Enums          []EventEnumSchemaIR  `json:"enums"`
}

type EventFieldSchemaIR struct {
	FieldID  FieldID       `json:"fieldId"`
	Type     LogicalTypeIR `json:"type"`
	Nullable bool          `json:"nullable"`
}

type EventEnumSchemaIR struct {
	EnumID  EnumID        `json:"enumId"`
	Members []EnumValueID `json:"members"`
}

type EnumContractIR struct {
	EnumID      EnumID                `json:"enumId"`
	GraphQLName string                `json:"graphqlName"`
	Values      []EnumValueContractIR `json:"values"`
}

type EnumValueContractIR struct {
	ValueID     EnumValueID `json:"valueId"`
	GraphQLName string      `json:"graphqlName"`
}

type FieldMode string

const (
	ModeVisible   FieldMode = "visible"
	ModeHidden    FieldMode = "hidden"
	ModeReadOnly  FieldMode = "readOnly"
	ModeWriteOnly FieldMode = "writeOnly"
	ModeImmutable FieldMode = "immutable"
)

func HasMode(modes []FieldMode, wanted FieldMode) bool {
	for _, mode := range modes {
		if mode == wanted {
			return true
		}
	}
	return false
}

func ModesReadable(modes []FieldMode) bool {
	return !HasMode(modes, ModeHidden) && !HasMode(modes, ModeWriteOnly)
}

type FieldContractIR struct {
	FieldID     FieldID     `json:"fieldId"`
	GraphQLName string      `json:"graphqlName"`
	Modes       []FieldMode `json:"modes"`
}

// SelectorContractIR is transport-facing identity metadata. KeyID is its
// stable identity, Name is its stable generated API name, and Fields preserve
// the primary/unique component order.
type SelectorContractIR struct {
	KeyID  KeyID     `json:"keyId"`
	Kind   KeyKind   `json:"kind"`
	Name   string    `json:"name"`
	Fields []FieldID `json:"fields"`
}

type Operation string

const (
	OperationFindOne         Operation = "findOne"
	OperationFindMany        Operation = "findMany"
	OperationCreate          Operation = "create"
	OperationUpdate          Operation = "update"
	OperationUpsert          Operation = "upsert"
	OperationDelete          Operation = "delete"
	OperationUpdateMany      Operation = "updateMany"
	OperationDeleteMany      Operation = "deleteMany"
	OperationAggregate       Operation = "aggregate"
	OperationGroupBy         Operation = "groupBy"
	OperationRelationGroupBy Operation = "relationGroupBy"
)

type GraphQLRootNamesIR struct {
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

type AggregationContractIR struct {
	Enabled                       bool                          `json:"enabled"`
	Dimensions                    []FieldID                     `json:"dimensions"`
	DimensionsExplicit            bool                          `json:"dimensionsExplicit"`
	Measures                      []FieldID                     `json:"measures"`
	MeasuresExplicit              bool                          `json:"measuresExplicit"`
	RelationDimensions            []RelationDimensionContractIR `json:"relationDimensions"`
	GraphQLMaxGroups              uint32                        `json:"graphqlMaxGroups"`
	RelationMaxIntermediateGroups uint32                        `json:"relationMaxIntermediateGroups"`
}

// RelationDimensionContractIR names one compiler-validated forward-to-one
// traversal. Path order is semantic and therefore never canonical-sorted.
type RelationDimensionContractIR struct {
	Name          string       `json:"name"`
	Path          []RelationID `json:"path"`
	TerminalField FieldID      `json:"terminalField"`
}

type LimitContractIR struct {
	MaxTake         uint32 `json:"maxTake,omitempty"`
	DefaultPageSize uint32 `json:"defaultPageSize,omitempty"`
	MaxPageSize     uint32 `json:"maxPageSize,omitempty"`
}

type GraphQLTypeKind string

const (
	GraphQLTypeScalar          GraphQLTypeKind = "scalar"
	GraphQLTypeEnum            GraphQLTypeKind = "enum"
	GraphQLTypeModel           GraphQLTypeKind = "model"
	GraphQLTypeList            GraphQLTypeKind = "list"
	GraphQLTypePredicate       GraphQLTypeKind = "predicate"
	GraphQLTypeSelector        GraphQLTypeKind = "selector"
	GraphQLTypeCreateInput     GraphQLTypeKind = "createInput"
	GraphQLTypeUpdateInput     GraphQLTypeKind = "updateInput"
	GraphQLTypeUpdateManyInput GraphQLTypeKind = "updateManyInput"
)

// GraphQLTypeIR is a closed transport type tree. Name is populated only for a
// scalar, enum, model, or model-derived input leaf; list nodes contain exactly
// one Element.
type GraphQLTypeIR struct {
	Kind     GraphQLTypeKind `json:"kind"`
	Name     string          `json:"name,omitempty"`
	Nullable bool            `json:"nullable"`
	Element  *GraphQLTypeIR  `json:"element,omitempty"`
}

type GraphQLArgumentContractIR struct {
	Name string        `json:"name"`
	Type GraphQLTypeIR `json:"type"`
}

type ComputedFieldContractIR struct {
	ExtensionID ExtensionID                 `json:"extensionId"`
	Name        string                      `json:"name"`
	Result      GraphQLTypeIR               `json:"result"`
	Arguments   []GraphQLArgumentContractIR `json:"arguments"`
	Requires    []FieldID                   `json:"requires"`
	Resolver    AttachedMethodIR            `json:"resolver"`
	Batch       *ComputedBatchContractIR    `json:"batch,omitempty"`
}

type ComputedBatchContractIR struct {
	KeyField     FieldID           `json:"keyField"`
	Loader       AttachedMethodIR  `json:"loader"`
	CacheKey     *AttachedMethodIR `json:"cacheKey,omitempty"`
	MaxBatchSize uint32            `json:"maxBatchSize"`
}

type CustomOperationKind string

const (
	CustomOperationQuery    CustomOperationKind = "query"
	CustomOperationMutation CustomOperationKind = "mutation"
)

type CustomOperationCapability string

const CustomOperationCallerOnly CustomOperationCapability = "callerOnly"

type CustomOperationContractIR struct {
	ExtensionID ExtensionID                 `json:"extensionId"`
	Operation   CustomOperationKind         `json:"operation"`
	Name        string                      `json:"name"`
	Arguments   []GraphQLArgumentContractIR `json:"arguments"`
	Result      GraphQLTypeIR               `json:"result"`
	Resolver    AttachedMethodIR            `json:"resolver"`
	Capability  CustomOperationCapability   `json:"capability"`
}

type AttachedMethodIR struct {
	ModelID     *ModelID       `json:"modelId,omitempty"`
	PackagePath string         `json:"packagePath,omitempty"`
	Receiver    GoNamedTypeIR  `json:"receiver"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Actor       *GoNamedTypeIR `json:"actor,omitempty"`
}
