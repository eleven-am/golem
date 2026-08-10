package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type ObjectKind string

const (
	ObjectSchema     ObjectKind = "schema"
	ObjectModel      ObjectKind = "model"
	ObjectField      ObjectKind = "field"
	ObjectRelation   ObjectKind = "relation"
	ObjectEnum       ObjectKind = "enum"
	ObjectEnumValue  ObjectKind = "enum-value"
	ObjectKey        ObjectKind = "key"
	ObjectIndex      ObjectKind = "index"
	ObjectCheck      ObjectKind = "check"
	ObjectForeignKey ObjectKind = "foreign-key"
	ObjectExtension  ObjectKind = "extension"
)

type StableIdentity struct {
	Kind      ObjectKind
	Canonical string
	Full      [sha256.Size]byte
	ID        string
}

type identityHasher func([]byte) [sha256.Size]byte

type IDRegistry struct {
	hash        identityHasher
	byCanonical map[string]StableIdentity
	byFull      map[[sha256.Size]byte]StableIdentity
	byShort     map[string]StableIdentity
}

func NewIDRegistry() *IDRegistry {
	return newIDRegistry(sha256.Sum256)
}

func newIDRegistry(hash identityHasher) *IDRegistry {
	return &IDRegistry{
		hash:        hash,
		byCanonical: make(map[string]StableIdentity),
		byFull:      make(map[[sha256.Size]byte]StableIdentity),
		byShort:     make(map[string]StableIdentity),
	}
}

// Register derives and collision-checks one stable identity. Duplicate
// canonical identities are errors because two declarations may not own one ID.
func (registry *IDRegistry) Register(kind ObjectKind, canonical string, span SourceSpan) (StableIdentity, *Diagnostic) {
	key := string(kind) + "\x00" + canonical
	if existing, ok := registry.byCanonical[key]; ok {
		diagnostic := NewError(
			"P1_ID_DUPLICATE",
			fmt.Sprintf("duplicate %s stable identity %q (%s)", kind, canonical, existing.ID),
			span,
		)
		return StableIdentity{}, &diagnostic
	}

	domain := []byte("golem:" + string(kind) + ":v1\x00" + canonical)
	full := registry.hash(domain)
	short := hex.EncodeToString(full[:16])
	identity := StableIdentity{Kind: kind, Canonical: canonical, Full: full, ID: short}

	if existing, ok := registry.byFull[full]; ok && (existing.Kind != kind || existing.Canonical != canonical) {
		diagnostic := NewError(
			"P1_ID_FULL_COLLISION",
			fmt.Sprintf("%s identity %q collides with %s identity %q", kind, canonical, existing.Kind, existing.Canonical),
			span,
		)
		return StableIdentity{}, &diagnostic
	}
	if existing, ok := registry.byShort[short]; ok && existing.Full != full {
		diagnostic := NewError(
			"P1_ID_TRUNCATED_COLLISION",
			fmt.Sprintf("%s identity %q has truncated ID %s already used by %s identity %q", kind, canonical, short, existing.Kind, existing.Canonical),
			span,
		)
		return StableIdentity{}, &diagnostic
	}

	registry.byCanonical[key] = identity
	registry.byFull[full] = identity
	registry.byShort[short] = identity
	return identity, nil
}

func ModelIdentity(schemaStableName, logicalModelName string) string {
	return schemaStableName + "\x00" + logicalModelName
}

func OwnedIdentity(ownerID, logicalName string) string {
	return ownerID + "\x00" + logicalName
}

func SchemaIDFrom(identity StableIdentity) SchemaID         { return SchemaID(identity.ID) }
func ModelIDFrom(identity StableIdentity) ModelID           { return ModelID(identity.ID) }
func FieldIDFrom(identity StableIdentity) FieldID           { return FieldID(identity.ID) }
func RelationIDFrom(identity StableIdentity) RelationID     { return RelationID(identity.ID) }
func EnumIDFrom(identity StableIdentity) EnumID             { return EnumID(identity.ID) }
func EnumValueIDFrom(identity StableIdentity) EnumValueID   { return EnumValueID(identity.ID) }
func KeyIDFrom(identity StableIdentity) KeyID               { return KeyID(identity.ID) }
func IndexIDFrom(identity StableIdentity) IndexID           { return IndexID(identity.ID) }
func CheckIDFrom(identity StableIdentity) CheckID           { return CheckID(identity.ID) }
func ForeignKeyIDFrom(identity StableIdentity) ForeignKeyID { return ForeignKeyID(identity.ID) }
func ExtensionIDFrom(identity StableIdentity) ExtensionID   { return ExtensionID(identity.ID) }
