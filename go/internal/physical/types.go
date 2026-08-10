// Package physical defines the normalized, provider-specific schema boundary
// between Golem's logical compiler IR and provider lowering, migration,
// rendering, and introspection. Values are semantic; this package contains no
// DDL or provider catalog text.
package physical

import "github.com/eleven-am/golem/go/internal/compiler/ir"

const (
	SchemaFormatVersion    uint32 = 1
	CanonicalFormatVersion uint32 = 1
)

type PhysicalName string

type Version struct {
	Major uint32
	Minor uint32
	Patch uint32
}

type DriverIdentity struct {
	Module  string
	Adapter string
}

type CapabilityVerification string

const (
	VerificationVersionFloor CapabilityVerification = "version_floor"
	VerificationRuntimeProbe CapabilityVerification = "runtime_probe"
	VerificationCompileProbe CapabilityVerification = "compile_probe"
	VerificationExtension    CapabilityVerification = "extension"
)

// CapabilityFact is a manifest fact with an explicit proof mechanism, never an
// optimistic boolean inferred from a provider name.
type CapabilityFact struct {
	ID           ir.CapabilityID
	Version      uint32
	Verification CapabilityVerification
}

type ProviderManifest struct {
	Provider       ir.Provider
	Driver         DriverIdentity
	MinimumVersion Version
	Capabilities   []CapabilityFact
}

type Namespace struct {
	Name PhysicalName
}

// ObjectRef names the stable logical owner of a physical requirement or
// diagnostic. ObjectID contains a KeyID, ForeignKeyID, CheckID, IndexID,
// ExtensionID, or system ObjectID when the narrower fields do not apply.
type ObjectRef struct {
	Kind     ir.ObjectKind
	ModelID  ir.ModelID
	FieldID  ir.FieldID
	ObjectID ir.ObjectID
}

type CapabilityRequirement struct {
	Capability ir.CapabilityID
	Owner      ObjectRef
}

type StorageKind string

const (
	StorageSQLiteInteger StorageKind = "sqlite.integer"
	StorageSQLiteReal    StorageKind = "sqlite.real"
	StorageSQLiteText    StorageKind = "sqlite.text"
	StorageSQLiteBlob    StorageKind = "sqlite.blob"

	StoragePostgreSQLBoolean     StorageKind = "postgresql.boolean"
	StoragePostgreSQLSmallInt    StorageKind = "postgresql.smallint"
	StoragePostgreSQLInteger     StorageKind = "postgresql.integer"
	StoragePostgreSQLBigInt      StorageKind = "postgresql.bigint"
	StoragePostgreSQLReal        StorageKind = "postgresql.real"
	StoragePostgreSQLDouble      StorageKind = "postgresql.double_precision"
	StoragePostgreSQLNumeric     StorageKind = "postgresql.numeric"
	StoragePostgreSQLText        StorageKind = "postgresql.text"
	StoragePostgreSQLBytea       StorageKind = "postgresql.bytea"
	StoragePostgreSQLUUID        StorageKind = "postgresql.uuid"
	StoragePostgreSQLDate        StorageKind = "postgresql.date"
	StoragePostgreSQLTime        StorageKind = "postgresql.time"
	StoragePostgreSQLTimestampTZ StorageKind = "postgresql.timestamptz"
	StoragePostgreSQLJSONB       StorageKind = "postgresql.jsonb"
	StorageProviderExtension     StorageKind = "provider.extension"
)

type SemanticSymbol struct {
	Identity string
	Kind     ir.SchemaSymbolKind
	Version  uint16
	Provider ir.ProviderScope
}

type StorageType struct {
	Kind      StorageKind
	Precision uint16
	Scale     uint16
	Length    uint32
	Symbol    *SemanticSymbol
}

type ExpressionKind string

const (
	ExpressionColumn   ExpressionKind = "column"
	ExpressionLiteral  ExpressionKind = "literal"
	ExpressionOperator ExpressionKind = "operator"
	ExpressionFunction ExpressionKind = "function"
	ExpressionCast     ExpressionKind = "cast"
)

// Expression is a closed semantic tree. Symbol identity is registered by a
// provider; no node carries rendered SQL.
type Expression struct {
	Kind     ExpressionKind
	Type     StorageType
	Nullable bool
	Symbol   *SemanticSymbol
	Column   *ir.FieldID
	Literal  *ir.TypedLiteralIR
	Operands []Expression
}

type DefaultKind string

const (
	DefaultNone     DefaultKind = "none"
	DefaultLiteral  DefaultKind = "literal"
	DefaultProvider DefaultKind = "provider"
)

type PhysicalDefault struct {
	Kind       DefaultKind
	Literal    *ir.TypedLiteralIR
	Expression *Expression
}

type GeneratedKind string

const (
	GeneratedStored  GeneratedKind = "stored"
	GeneratedVirtual GeneratedKind = "virtual"
)

type GeneratedExpression struct {
	Kind       GeneratedKind
	Expression Expression
}

type PhysicalColumn struct {
	ID                   ir.FieldID
	Name                 PhysicalName
	Ordinal              uint32
	Storage              StorageType
	Nullable             bool
	Default              PhysicalDefault
	Generated            *GeneratedExpression
	Collation            *SemanticSymbol
	RequiredCapabilities []CapabilityRequirement
}

type PhysicalKey struct {
	ID                   ir.KeyID
	Name                 PhysicalName
	Columns              []ir.FieldID
	RequiredCapabilities []CapabilityRequirement
}

type PhysicalForeignKey struct {
	ID                   ir.ForeignKeyID
	Name                 PhysicalName
	Columns              []ir.FieldID
	ReferencedTable      ir.ModelID
	ReferencedColumns    []ir.FieldID
	OnUpdate             ir.ReferentialAction
	OnDelete             ir.ReferentialAction
	Deferrable           ir.Deferrability
	RequiredCapabilities []CapabilityRequirement
}

type PhysicalCheck struct {
	ID                   ir.CheckID
	Name                 PhysicalName
	Expression           Expression
	RequiredCapabilities []CapabilityRequirement
}

type IndexMethod string

const (
	IndexBTree IndexMethod = "btree"
	IndexHash  IndexMethod = "hash"
	IndexGIN   IndexMethod = "gin"
	IndexGiST  IndexMethod = "gist"
	IndexBRIN  IndexMethod = "brin"
)

type IndexKey struct {
	Column     *ir.FieldID
	Expression *Expression
	Direction  ir.SortDirection
	Nulls      ir.NullsOrder
	Collation  *SemanticSymbol
	OpClass    *SemanticSymbol
}

type IndexCreationMode string

const (
	IndexTransactional  IndexCreationMode = "transactional"
	IndexAutocommitOnly IndexCreationMode = "autocommit_only"
)

type PhysicalIndex struct {
	ID                   ir.IndexID
	Name                 PhysicalName
	Unique               bool
	Method               IndexMethod
	Keys                 []IndexKey
	Include              []ir.FieldID
	Predicate            *Expression
	CreationMode         IndexCreationMode
	RequiredCapabilities []CapabilityRequirement
}

type PhysicalTable struct {
	ID                   ir.ModelID
	Name                 PhysicalName
	Columns              []PhysicalColumn
	PrimaryKey           *PhysicalKey
	Uniques              []PhysicalKey
	ForeignKeys          []PhysicalForeignKey
	Checks               []PhysicalCheck
	Indexes              []PhysicalIndex
	RequiredCapabilities []CapabilityRequirement
}

type SemanticValueKind string

const (
	ValueBool    SemanticValueKind = "bool"
	ValueInteger SemanticValueKind = "integer"
	ValueString  SemanticValueKind = "string"
	ValueID      SemanticValueKind = "id"
	ValueList    SemanticValueKind = "list"
)

type SemanticValue struct {
	Kind    SemanticValueKind
	Bool    bool
	Integer int64
	String  string
	List    []SemanticValue
}

type Attribute struct {
	Name  string
	Value SemanticValue
}

// Extension is a registered semantic provider extension. Kind and Version
// select provider-owned lowering/rendering; Attributes contain normalized data,
// never SQL text.
type Extension struct {
	ID                   ir.ExtensionID
	Provider             ir.Provider
	Kind                 string
	Version              uint16
	Owner                ObjectRef
	Attributes           []Attribute
	RequiredCapabilities []CapabilityRequirement
}

type SystemObjectKind string

const (
	SystemMigrationLedger SystemObjectKind = "migration_ledger"
	SystemMigrationLock   SystemObjectKind = "migration_lock"
	SystemOutbox          SystemObjectKind = "outbox"
	SystemOutboxDelivery  SystemObjectKind = "outbox_delivery"
	SystemUpsertGuard     SystemObjectKind = "upsert_guard"

	// These IDs identify provider-neutral semantic system objects. Providers may
	// render different namespaces and storage, but must not mint provider-specific
	// identities.
	MigrationLedgerObjectIDV1      ir.ObjectID = "cb3020cd708be72c384f431749769c98"
	MigrationLedgerFormatVersionV1 uint16      = 1
	MigrationLockObjectIDV1        ir.ObjectID = "e28aeab0927ce864e8d65a1e1dc62fc9"
	OutboxObjectIDV1               ir.ObjectID = "14b2d0b9de583fe675fa72de1d1c78c8"
	OutboxDeliveryObjectIDV1       ir.ObjectID = "51aa5c96fd5d24e27e182ed85f7bcbf2"
	UpsertGuardObjectIDV1          ir.ObjectID = "076704f0bfb30b5fed47137811a6dd18"
)

// OutboxSystemObjectV1 is the exact provider-neutral registry entry for the
// transactional mutation-fact outbox. Its physical shape remains provider
// owned and is selected solely by this kind/version pair.
func OutboxSystemObjectV1() SystemObject {
	return SystemObject{ID: OutboxObjectIDV1, Kind: SystemOutbox, Version: 1, Name: "_golem_outbox"}
}

// IsOutboxSystemObjectV1 accepts the canonical registry entry after deep-copy
// normalization, which may represent empty registries as non-nil slices.
func IsOutboxSystemObjectV1(object SystemObject) bool {
	return object.ID == OutboxObjectIDV1 && object.Kind == SystemOutbox && object.Version == 1 && object.Name == "_golem_outbox" && len(object.Attributes) == 0 && len(object.RequiredCapabilities) == 0
}

// OutboxDeliverySystemObjectV1 is the mutable operational state for one
// immutable outbox causation. It is deliberately separate from Outbox V1 so
// publication never rewrites commit-derived facts.
func OutboxDeliverySystemObjectV1() SystemObject {
	return SystemObject{ID: OutboxDeliveryObjectIDV1, Kind: SystemOutboxDelivery, Version: 1, Name: "_golem_outbox_delivery"}
}

// IsOutboxDeliverySystemObjectV1 accepts only the closed v1 registry entry.
func IsOutboxDeliverySystemObjectV1(object SystemObject) bool {
	return object.ID == OutboxDeliveryObjectIDV1 && object.Kind == SystemOutboxDelivery && object.Version == 1 && object.Name == "_golem_outbox_delivery" && len(object.Attributes) == 0 && len(object.RequiredCapabilities) == 0
}

// UpsertGuardSystemObjectV1 is the provider-neutral selector serialization
// primitive. SQLite renders a guard-token relation; PostgreSQL implements the
// same semantic object with transaction-scoped advisory locks and no relation.
func UpsertGuardSystemObjectV1() SystemObject {
	return SystemObject{ID: UpsertGuardObjectIDV1, Kind: SystemUpsertGuard, Version: 1, Name: "_golem_upsert_guard"}
}

// IsUpsertGuardSystemObjectV1 accepts only the closed v1 registry entry.
func IsUpsertGuardSystemObjectV1(object SystemObject) bool {
	return object.ID == UpsertGuardObjectIDV1 && object.Kind == SystemUpsertGuard && object.Version == 1 && object.Name == "_golem_upsert_guard" && len(object.Attributes) == 0 && len(object.RequiredCapabilities) == 0
}

// SystemObject is selected from a versioned closed registry. Its semantic kind
// and version define its provider-specific columns and constraints without DDL.
type SystemObject struct {
	ID                   ir.ObjectID
	Kind                 SystemObjectKind
	Version              uint16
	Name                 PhysicalName
	Attributes           []Attribute
	RequiredCapabilities []CapabilityRequirement
}

type SystemSchema struct {
	Version   uint32
	Namespace Namespace
	Objects   []SystemObject
}

type UnmanagedObject struct {
	Kind string
	Name PhysicalName
}

type PhysicalSchema struct {
	Version          uint32
	CanonicalVersion uint32
	Provider         ProviderManifest
	Namespace        Namespace
	Tables           []PhysicalTable
	Extensions       []Extension
	System           SystemSchema
	Unmanaged        []UnmanagedObject
}
