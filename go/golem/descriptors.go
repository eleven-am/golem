package golem

// KeyID is the fixed-width stable identity of a primary or unique key.
type KeyID [16]byte

type IdentityKind string

const (
	PrimaryIdentity IdentityKind = "primary"
	UniqueIdentity  IdentityKind = "unique"
)

// IdentityMetadata is immutable logical selector metadata. It carries no
// values and no physical identifiers; P3/P4 own operation payloads.
type IdentityMetadata struct {
	model  ModelID
	key    KeyID
	kind   IdentityKind
	fields []FieldID
}

func GeneratedIdentityMetadata(model ModelID, key KeyID, kind IdentityKind, fields ...FieldID) IdentityMetadata {
	return IdentityMetadata{model: model, key: key, kind: kind, fields: append([]FieldID(nil), fields...)}
}

func (metadata IdentityMetadata) ModelID() ModelID   { return metadata.model }
func (metadata IdentityMetadata) KeyID() KeyID       { return metadata.key }
func (metadata IdentityMetadata) Kind() IdentityKind { return metadata.kind }
func (metadata IdentityMetadata) Fields() []FieldID {
	return append([]FieldID(nil), metadata.fields...)
}
func (metadata IdentityMetadata) clone() IdentityMetadata {
	return GeneratedIdentityMetadata(metadata.model, metadata.key, metadata.kind, metadata.fields...)
}

// IdentitySelector is a typed generated handle for a compound identity.
// Single-field identities reuse their ScalarField handle. It intentionally has
// no value-bearing constructor in P1.
type IdentitySelector[M any] struct {
	metadata IdentityMetadata
	_        func() M
}

func GeneratedIdentitySelector[M any](model ModelID, key KeyID, kind IdentityKind, fields ...FieldID) IdentitySelector[M] {
	return IdentitySelector[M]{metadata: GeneratedIdentityMetadata(model, key, kind, fields...)}
}

func (selector IdentitySelector[M]) Metadata() IdentityMetadata { return selector.metadata.clone() }

type RelationMetadata struct {
	model       ModelID
	target      ModelID
	field       FieldID
	id          RelationID
	role        RelationRole
	cardinality RelationCardinality
}

type RelationRole string
type RelationCardinality string

const (
	RelationSource  RelationRole        = "source"
	RelationInverse RelationRole        = "inverse"
	RelationToOne   RelationCardinality = "toOne"
	RelationToMany  RelationCardinality = "toMany"
)

func GeneratedRelationMetadata(model, target ModelID, field FieldID, id RelationID, role RelationRole, cardinality RelationCardinality) RelationMetadata {
	return RelationMetadata{model: model, target: target, field: field, id: id, role: role, cardinality: cardinality}
}

func (metadata RelationMetadata) ModelID() ModelID                 { return metadata.model }
func (metadata RelationMetadata) TargetModelID() ModelID           { return metadata.target }
func (metadata RelationMetadata) FieldID() FieldID                 { return metadata.field }
func (metadata RelationMetadata) RelationID() RelationID           { return metadata.id }
func (metadata RelationMetadata) Role() RelationRole               { return metadata.role }
func (metadata RelationMetadata) Cardinality() RelationCardinality { return metadata.cardinality }

// GeneratedModelShape is the logical, provider-independent registry payload
// emitted beside a model. Slices are copied at every public boundary.
type GeneratedModelShape struct {
	scanFields  []FieldID
	writeFields []FieldID
	identities  []IdentityMetadata
	relations   []RelationMetadata
}

func GeneratedDescriptorShape(scanFields, writeFields []FieldID, identities []IdentityMetadata, relations []RelationMetadata) GeneratedModelShape {
	shape := GeneratedModelShape{scanFields: append([]FieldID(nil), scanFields...), writeFields: append([]FieldID(nil), writeFields...), relations: append([]RelationMetadata(nil), relations...)}
	shape.identities = cloneIdentities(identities)
	return shape
}

type ModelMetadata struct {
	id          ModelID
	scanFields  []FieldID
	writeFields []FieldID
	identities  []IdentityMetadata
	relations   []RelationMetadata
}

func (metadata ModelMetadata) ModelID() ModelID { return metadata.id }

// ScanFields is the persisted scalar field order used to build later scanners.
// P1 emits this metadata only: executable row codecs/scanners belong to the P3
// runtime and must not be faked with reflection or untyped destination slices.
func (metadata ModelMetadata) ScanFields() []FieldID {
	return append([]FieldID(nil), metadata.scanFields...)
}

// WriteFields preserves declaration order for persisted fields that are not
// generated or read-only. Runtime defaults do not remove a field from this
// structural order; operation-specific input/payload rules belong to P4.
func (metadata ModelMetadata) WriteFields() []FieldID {
	return append([]FieldID(nil), metadata.writeFields...)
}
func (metadata ModelMetadata) Identities() []IdentityMetadata {
	return cloneIdentities(metadata.identities)
}
func (metadata ModelMetadata) Relations() []RelationMetadata {
	return append([]RelationMetadata(nil), metadata.relations...)
}
func (metadata ModelMetadata) clone() ModelMetadata {
	return ModelMetadata{id: metadata.id, scanFields: metadata.ScanFields(), writeFields: metadata.WriteFields(), identities: metadata.Identities(), relations: metadata.Relations()}
}

type PackageDescriptors struct {
	generation SchemaDigest
	models     []ModelMetadata
}
type ApplicationDescriptors struct {
	generation SchemaDigest
	packages   []PackageDescriptors
}

func GeneratedPackageDescriptors(models ...ModelMetadata) PackageDescriptors {
	result := PackageDescriptors{models: make([]ModelMetadata, len(models))}
	for index, model := range models {
		result.models[index] = model.clone()
	}
	return result
}

func GeneratedStampedPackageDescriptors(generation SchemaDigest, models ...ModelMetadata) PackageDescriptors {
	result := GeneratedPackageDescriptors(models...)
	result.generation = generation
	return result
}

func (descriptors PackageDescriptors) GenerationDigest() SchemaDigest { return descriptors.generation }

func GeneratedApplicationDescriptors(expected SchemaDigest, packages ...PackageDescriptors) (ApplicationDescriptors, error) {
	digests := make([]SchemaDigest, len(packages))
	for index, pkg := range packages {
		digests[index] = pkg.generation
	}
	if err := validateGenerationDigests("descriptors", expected, digests); err != nil {
		return ApplicationDescriptors{}, err
	}
	result := ApplicationDescriptors{generation: expected, packages: make([]PackageDescriptors, len(packages))}
	for index, pkg := range packages {
		result.packages[index] = GeneratedStampedPackageDescriptors(pkg.generation, pkg.Models()...)
	}
	return result, nil
}

func (descriptors ApplicationDescriptors) GenerationDigest() SchemaDigest {
	return descriptors.generation
}

func (descriptors PackageDescriptors) Models() []ModelMetadata {
	result := make([]ModelMetadata, len(descriptors.models))
	for index, model := range descriptors.models {
		result[index] = model.clone()
	}
	return result
}

func (descriptors ApplicationDescriptors) Models() []ModelMetadata {
	var result []ModelMetadata
	for _, pkg := range descriptors.packages {
		result = append(result, pkg.Models()...)
	}
	return result
}

func (descriptors ApplicationDescriptors) Lookup(id ModelID) (ModelMetadata, bool) {
	for _, model := range descriptors.Models() {
		if model.id == id {
			return model, true
		}
	}
	return ModelMetadata{}, false
}

func cloneIdentities(values []IdentityMetadata) []IdentityMetadata {
	result := make([]IdentityMetadata, len(values))
	for index, value := range values {
		result[index] = value.clone()
	}
	return result
}
