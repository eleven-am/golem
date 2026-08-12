// Package schema owns the one-time, fail-closed conversion from the public
// representation-opaque SchemaBundle into immutable, ID-keyed runtime facts.
package schema

import (
	"encoding/hex"
	"fmt"
	"sort"

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
	generationDigest         golem.SchemaDigest
	modelFingerprint         golem.SchemaDigest
	contractFingerprint      golem.SchemaDigest
	providers                []golem.Provider
	models                   map[golem.ModelID]Model
	fields                   map[golem.ModelID]map[golem.FieldID]Field
	relations                map[relationKey]RelationEndpoint
	enumValues               map[compilerir.EnumID]map[string]compilerir.EnumValueID
	enumLabels               map[compilerir.EnumID]map[compilerir.EnumValueID]string
	physicalModels           map[golem.Provider]map[golem.ModelID]PhysicalModel
	physicalFields           map[golem.Provider]map[golem.ModelID]map[golem.FieldID]PhysicalField
	physicalModelNames       map[golem.Provider]map[physical.PhysicalName]golem.ModelID
	physicalAccessObjects    map[golem.Provider]map[physical.PhysicalName]PhysicalAccessObject
	physicalKeyAccessObjects map[golem.Provider]map[golem.ModelID][]PhysicalAccessObject
	physicalNamespaces       map[golem.Provider]physical.PhysicalName
	physicalSystemNamespaces map[golem.Provider]physical.PhysicalName
	capabilities             map[golem.Provider]map[compilerir.CapabilityID]physical.CapabilityFact
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

// HasScopedReads reports whether the fingerprinted contract enables at least
// one audited scoped root. It exposes no mutable model inventory.
func (registry *Registry) HasScopedReads() bool {
	if registry == nil {
		return false
	}
	for _, model := range registry.models {
		if model.scopedReads {
			return true
		}
	}
	return false
}

// EventModels returns the subscription-enabled model inventory in stable ID
// order. It exists for the event history resolver, which must index generated
// historical bundles without exposing the registry's mutable maps.
func (registry *Registry) EventModels() []Model {
	if registry == nil {
		return nil
	}
	ids := make([]golem.ModelID, 0)
	for id, model := range registry.models {
		if model.subscriptions {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return string(ids[i][:]) < string(ids[j][:]) })
	result := make([]Model, len(ids))
	for index, id := range ids {
		result[index], _ = registry.Model(id)
	}
	return result
}

// Model returns a model only when the fixed-width ID is present in this exact
// fingerprinted registry.
func (registry *Registry) Model(id golem.ModelID) (Model, bool) {
	value, ok := registry.models[id]
	if value.optimisticConcurrency != nil {
		field := *value.optimisticConcurrency
		value.optimisticConcurrency = &field
	}
	value.fields = append([]golem.FieldID(nil), value.fields...)
	value.primaryKey = append([]golem.FieldID(nil), value.primaryKey...)
	value.eventSnapshot = append([]golem.FieldID(nil), value.eventSnapshot...)
	value.identities = make([]Identity, len(registry.models[id].identities))
	for index, identity := range registry.models[id].identities {
		value.identities[index] = identity.clone()
	}
	if analytics, present := value.Analytics(); present {
		value.analytics = &analytics
	}
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
	value.modes = append([]compilerir.FieldMode(nil), value.modes...)
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

// ForwardToOneRelation resolves only the compiler-owned source traversal for a
// relation identity. Configured analytical paths store stable relation IDs,
// never author-controlled SQL or field names.
func (registry *Registry) ForwardToOneRelation(model golem.ModelID, relation golem.RelationID) (RelationEndpoint, bool) {
	if registry == nil {
		return RelationEndpoint{}, false
	}
	for _, endpoint := range registry.relations {
		if endpoint.model == model && endpoint.relation == relation && endpoint.role == compilerir.RelationSource && endpoint.cardinality == compilerir.RelationOne {
			return endpoint.clone(), true
		}
	}
	return RelationEndpoint{}, false
}

// IdentityChangeRequiresReferentialEnumeration reports whether changing any of
// the supplied fields can alter a relation correlation owned by another row.
// Scalar mutation planning uses this to permit unreferenced mutable identities
// while refusing provider cascades whose complete affected set is absent from
// the mutation graph.
func (registry *Registry) IdentityChangeRequiresReferentialEnumeration(model golem.ModelID, fields []golem.FieldID) bool {
	if registry == nil || model == (golem.ModelID{}) || len(fields) == 0 {
		return false
	}
	changed := make(map[golem.FieldID]struct{}, len(fields))
	for _, field := range fields {
		changed[field] = struct{}{}
	}
	for _, endpoint := range registry.relations {
		if endpoint.model != model {
			continue
		}
		for _, pair := range endpoint.correlation {
			if _, present := changed[pair.parent]; present {
				return true
			}
		}
	}
	return false
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

func (registry *Registry) PhysicalSystemNamespace(provider golem.Provider) (physical.PhysicalName, bool) {
	if registry == nil {
		return "", false
	}
	value, ok := registry.physicalSystemNamespaces[provider]
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

// PhysicalModelIDByName maps one provider diagnostic table name to the stable
// model identity in this exact fingerprinted registry. The input name is used
// only as a private lookup key and is never retained in the returned fact.
func (registry *Registry) PhysicalModelIDByName(provider golem.Provider, name physical.PhysicalName) (golem.ModelID, bool) {
	if registry == nil {
		return golem.ModelID{}, false
	}
	values, ok := registry.physicalModelNames[provider]
	if !ok {
		return golem.ModelID{}, false
	}
	value, ok := values[name]
	return value, ok
}

// PhysicalAccessObjectByName maps one provider diagnostic access-object name
// to a closed stable identity. Unknown names fail closed; no naming convention
// is parsed or inferred.
func (registry *Registry) PhysicalAccessObjectByName(provider golem.Provider, name physical.PhysicalName) (PhysicalAccessObject, bool) {
	if registry == nil {
		return PhysicalAccessObject{}, false
	}
	values, ok := registry.physicalAccessObjects[provider]
	if !ok {
		return PhysicalAccessObject{}, false
	}
	value, ok := values[name]
	return value.clone(), ok
}

// PhysicalKeyAccessObjects returns the reviewed primary/unique key inventory
// for one model and provider snapshot. It exists for provider plans (notably
// SQLite rowid and autoindex plans) that do not report an authored key name.
func (registry *Registry) PhysicalKeyAccessObjects(provider golem.Provider, model golem.ModelID) []PhysicalAccessObject {
	if registry == nil {
		return nil
	}
	models, ok := registry.physicalKeyAccessObjects[provider]
	if !ok {
		return nil
	}
	values := models[model]
	result := make([]PhysicalAccessObject, len(values))
	for index, value := range values {
		result[index] = value.clone()
	}
	return result
}

// PhysicalKeyAccessByFields resolves exactly one reviewed primary/unique key
// by its stable ordered field sequence. Reordered, unknown, or ambiguous input
// fails closed instead of guessing from a provider-generated name.
func (registry *Registry) PhysicalKeyAccessByFields(provider golem.Provider, model golem.ModelID, kind PhysicalAccessKind, fields []golem.FieldID) (PhysicalAccessObject, bool) {
	if registry == nil || model == (golem.ModelID{}) || kind != PhysicalAccessPrimaryKey && kind != PhysicalAccessUniqueIndex || !validPhysicalKeyFields(fields) {
		return PhysicalAccessObject{}, false
	}
	models, ok := registry.physicalKeyAccessObjects[provider]
	if !ok {
		return PhysicalAccessObject{}, false
	}
	var match PhysicalAccessObject
	found := false
	for _, value := range models[model] {
		if value.kind != kind || !equalPhysicalKeyFields(value.fields, fields) {
			continue
		}
		if found {
			return PhysicalAccessObject{}, false
		}
		match, found = value, true
	}
	return match.clone(), found
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
	id                    golem.ModelID
	fields                []golem.FieldID
	primaryKey            []golem.FieldID
	identities            []Identity
	equality              map[golem.FieldID]struct{}
	maxTake               uint32
	subscriptions         bool
	eventSchema           compilerir.Fingerprint
	eventSnapshot         []golem.FieldID
	analytics             *compilerir.AggregationContractIR
	scopedReads           bool
	hookOwnedCreate       []golem.FieldID
	optimisticConcurrency *golem.FieldID
}

func (model Model) ID() golem.ModelID       { return model.id }
func (model Model) Fields() []golem.FieldID { return append([]golem.FieldID(nil), model.fields...) }
func (model Model) PrimaryKey() []golem.FieldID {
	return append([]golem.FieldID(nil), model.primaryKey...)
}
func (model Model) Identities() []Identity {
	result := make([]Identity, len(model.identities))
	for index, identity := range model.identities {
		result[index] = identity.clone()
	}
	return result
}
func (model Model) Identity(key golem.KeyID) (Identity, bool) {
	for _, identity := range model.identities {
		if identity.key == key {
			return identity.clone(), true
		}
	}
	return Identity{}, false
}

func (model Model) MaxTake() (uint32, bool) { return model.maxTake, model.maxTake != 0 }

// SubscriptionsEnabled is the normalized contract decision controlling
// durable mutation-fact capture for this model.
func (model Model) SubscriptionsEnabled() bool { return model.subscriptions }

// EventSchema returns the compiler-validated logical schema fingerprint and
// complete private pre-delete scalar inventory for a subscription-enabled
// model. The field order is declared model-field order and the returned slice
// is privately owned by the caller.
func (model Model) EventSchema() (compilerir.Fingerprint, []golem.FieldID, bool) {
	if !model.subscriptions || model.eventSchema == "" {
		return "", nil, false
	}
	return model.eventSchema, append([]golem.FieldID(nil), model.eventSnapshot...), true
}
func (model Model) Analytics() (compilerir.AggregationContractIR, bool) {
	if model.analytics == nil {
		return compilerir.AggregationContractIR{}, false
	}
	value := *model.analytics
	value.Dimensions = append([]compilerir.FieldID(nil), value.Dimensions...)
	value.Measures = append([]compilerir.FieldID(nil), value.Measures...)
	value.RelationDimensions = append([]compilerir.RelationDimensionContractIR(nil), value.RelationDimensions...)
	for index := range value.RelationDimensions {
		value.RelationDimensions[index].Path = append([]compilerir.RelationID(nil), value.RelationDimensions[index].Path...)
	}
	return value, true
}
func (model Model) ScopedReadsEnabled() bool { return model.scopedReads }

// GraphQLHookOwnedCreateFields returns the compiler-validated scalar fields
// omitted from GraphQL create shapes and supplied by a model BeforeCreate
// hook. The inventory is contract metadata, not database schema metadata.
func (model Model) GraphQLHookOwnedCreateFields() []golem.FieldID {
	return append([]golem.FieldID(nil), model.hookOwnedCreate...)
}

// OptimisticConcurrency returns the sole compiler-owned version field. The
// value is copied from the immutable three-way bootstrap agreement.
func (model Model) OptimisticConcurrency() (golem.FieldID, bool) {
	if model.optimisticConcurrency == nil {
		return golem.FieldID{}, false
	}
	return *model.optimisticConcurrency, true
}

// EqualityIndexed reports the compiler-proven provider-neutral fact that an
// equality lookup on this field is served by a leading key/index column.
func (model Model) EqualityIndexed(field golem.FieldID) bool {
	if _, ok := model.equality[field]; ok {
		return true
	}
	if len(model.primaryKey) != 0 && model.primaryKey[0] == field {
		return true
	}
	for _, identity := range model.identities {
		if len(identity.fields) != 0 && identity.fields[0] == field {
			return true
		}
	}
	return false
}

type Identity struct {
	key    golem.KeyID
	kind   compilerir.KeyKind
	fields []golem.FieldID
}

func (identity Identity) KeyID() golem.KeyID       { return identity.key }
func (identity Identity) Kind() compilerir.KeyKind { return identity.kind }
func (identity Identity) Fields() []golem.FieldID {
	return append([]golem.FieldID(nil), identity.fields...)
}
func (identity Identity) clone() Identity { identity.fields = identity.Fields(); return identity }

// Field is a provider-neutral logical field fact.
type Field struct {
	model            golem.ModelID
	id               golem.FieldID
	kind             compilerir.FieldKind
	logicalType      compilerir.LogicalTypeIR
	nullable         bool
	defaultValue     *compilerir.DefaultIR
	generation       *compilerir.GeneratedColumnIR
	updated          bool
	databaseReadOnly bool
	graphqlName      string
	modes            []compilerir.FieldMode
	relation         golem.RelationID
	relationRole     compilerir.RelationEndpointRole
}

func (field Field) ModelID() golem.ModelID                { return field.model }
func (field Field) ID() golem.FieldID                     { return field.id }
func (field Field) Kind() compilerir.FieldKind            { return field.kind }
func (field Field) LogicalType() compilerir.LogicalTypeIR { return cloneLogicalType(field.logicalType) }
func (field Field) Nullable() bool                        { return field.nullable }
func (field Field) Default() (compilerir.DefaultIR, bool) {
	if field.defaultValue == nil {
		return compilerir.DefaultIR{}, false
	}
	return cloneDefault(*field.defaultValue), true
}
func (field Field) Generation() (compilerir.GeneratedColumnIR, bool) {
	if field.generation == nil {
		return compilerir.GeneratedColumnIR{}, false
	}
	return cloneGeneratedColumn(*field.generation), true
}
func (field Field) Updated() bool          { return field.updated }
func (field Field) DatabaseReadOnly() bool { return field.databaseReadOnly }
func (field Field) GraphQLName() string    { return field.graphqlName }
func (field Field) Modes() []compilerir.FieldMode {
	return append([]compilerir.FieldMode(nil), field.modes...)
}

// Visible reports whether the field belongs to the default public projection.
// Empty modes are visible for source compatibility; hidden and write-only
// explicitly remove a field from read output.
func (field Field) Visible() bool {
	for _, mode := range field.modes {
		if mode == compilerir.ModeHidden || mode == compilerir.ModeWriteOnly {
			return false
		}
	}
	return true
}
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

// PhysicalAccessKind is the closed registry classification of a provider
// access object. Bitmap selection is a provider-plan strategy over an index;
// it is deliberately not stored as a second physical identity here.
type PhysicalAccessKind uint8

const (
	PhysicalAccessUnknown PhysicalAccessKind = iota
	PhysicalAccessPrimaryKey
	PhysicalAccessUniqueIndex
	PhysicalAccessIndex
)

// PhysicalIndexID is the fixed-width stable identity of a physical index. It
// contains no provider name and grants no query capability.
type PhysicalIndexID [16]byte

// PhysicalAccessObject contains only sanitized identity facts. The mutually
// exclusive key and index fields preserve whether the physical owner was a
// primary/unique key or an explicit index.
type PhysicalAccessObject struct {
	kind    PhysicalAccessKind
	model   golem.ModelID
	keyID   golem.KeyID
	indexID PhysicalIndexID
	fields  []golem.FieldID
}

func (value PhysicalAccessObject) Kind() PhysicalAccessKind { return value.kind }
func (value PhysicalAccessObject) ModelID() golem.ModelID   { return value.model }
func (value PhysicalAccessObject) KeyID() (golem.KeyID, bool) {
	return value.keyID, value.keyID != (golem.KeyID{}) && value.indexID == (PhysicalIndexID{})
}
func (value PhysicalAccessObject) IndexID() (PhysicalIndexID, bool) {
	return value.indexID, value.indexID != (PhysicalIndexID{}) && value.keyID == (golem.KeyID{})
}
func (value PhysicalAccessObject) FieldIDs() []golem.FieldID {
	return append([]golem.FieldID(nil), value.fields...)
}
func (value PhysicalAccessObject) clone() PhysicalAccessObject {
	value.fields = value.FieldIDs()
	return value
}

func validPhysicalKeyFields(fields []golem.FieldID) bool {
	if len(fields) == 0 {
		return false
	}
	seen := make(map[golem.FieldID]struct{}, len(fields))
	for _, field := range fields {
		if field == (golem.FieldID{}) {
			return false
		}
		if _, duplicate := seen[field]; duplicate {
			return false
		}
		seen[field] = struct{}{}
	}
	return true
}

func equalPhysicalKeyFields(left, right []golem.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

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

func cloneDefault(value compilerir.DefaultIR) compilerir.DefaultIR {
	if value.Literal != nil {
		literal := *value.Literal
		value.Literal = &literal
	}
	if value.Provider != nil {
		provider := *value.Provider
		value.Provider = &provider
	}
	return value
}

func cloneGeneratedColumn(value compilerir.GeneratedColumnIR) compilerir.GeneratedColumnIR {
	value.Expr = cloneSchemaExpr(value.Expr)
	return value
}

func cloneSchemaExpr(value compilerir.SchemaExprIR) compilerir.SchemaExprIR {
	value.ResultType = cloneLogicalType(value.ResultType)
	if value.Symbol != nil {
		symbol := *value.Symbol
		value.Symbol = &symbol
	}
	if value.Field != nil {
		field := *value.Field
		value.Field = &field
	}
	if value.Literal != nil {
		literal := *value.Literal
		value.Literal = &literal
	}
	value.Operands = make([]compilerir.SchemaExprIR, len(value.Operands))
	for index, operand := range value.Operands {
		value.Operands[index] = cloneSchemaExpr(operand)
	}
	value.ReferencedFields = append([]compilerir.FieldID(nil), value.ReferencedFields...)
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

func keyID(value compilerir.KeyID) (golem.KeyID, error) {
	parsed, err := fixedID(string(value))
	return golem.KeyID(parsed), err
}

func physicalIndexID(value compilerir.IndexID) (PhysicalIndexID, error) {
	parsed, err := fixedID(string(value))
	return PhysicalIndexID(parsed), err
}
