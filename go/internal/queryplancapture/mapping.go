package queryplancapture

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ResolveCandidate maps one provider-reported relation token through the exact
// statement alias map first, then through the exact physical registry. It does
// not parse renderer or provider naming conventions.
func ResolveCandidate(registry *schema.Registry, provider golem.Provider, aliases AliasMap, candidate string) (AliasIdentity, MatchStatus) {
	if identity, status := aliases.Resolve(candidate); status != MatchUnknown {
		return identity, status
	}
	model, ok := registry.PhysicalModelIDByName(provider, physical.PhysicalName(candidate))
	if !ok {
		return AliasIdentity{}, MatchUnknown
	}
	identity, ok := PhysicalIdentity(model)
	if !ok {
		return AliasIdentity{}, MatchUnknown
	}
	return identity, MatchExact
}

// ResolvePhysicalAccess maps one named provider access object and requires it
// to belong to the already-resolved model. Provider names are discarded.
func ResolvePhysicalAccess(registry *schema.Registry, provider golem.Provider, model golem.ModelID, candidate string, bitmap bool) (AccessKind, IndexID, bool) {
	if registry == nil || model == (golem.ModelID{}) || candidate == "" {
		return 0, IndexID{}, false
	}
	object, ok := registry.PhysicalAccessObjectByName(provider, physical.PhysicalName(candidate))
	if !ok || object.ModelID() != model {
		return 0, IndexID{}, false
	}
	return accessObjectIdentity(object, bitmap)
}

// ResolvePhysicalAccessAny is restricted to provider nodes (such as a
// PostgreSQL Bitmap Index Scan) that report a reviewed access object but omit
// a relation/alias. The access object itself remains the model authority.
func ResolvePhysicalAccessAny(registry *schema.Registry, provider golem.Provider, candidate string, bitmap bool) (AliasIdentity, AccessKind, IndexID, bool) {
	if registry == nil || candidate == "" {
		return AliasIdentity{}, 0, IndexID{}, false
	}
	object, ok := registry.PhysicalAccessObjectByName(provider, physical.PhysicalName(candidate))
	if !ok {
		return AliasIdentity{}, 0, IndexID{}, false
	}
	identity, ok := PhysicalIdentity(object.ModelID())
	if !ok {
		return AliasIdentity{}, 0, IndexID{}, false
	}
	access, indexID, ok := accessObjectIdentity(object, bitmap)
	return identity, access, indexID, ok
}

// ResolvePhysicalKeyByFields is the name-free key mapping seam used when a
// provider reports rowid/autoindex structure instead of an authored key name.
func ResolvePhysicalKeyByFields(registry *schema.Registry, provider golem.Provider, model golem.ModelID, kind schema.PhysicalAccessKind, fields []golem.FieldID) (AccessKind, IndexID, bool) {
	object, ok := registry.PhysicalKeyAccessByFields(provider, model, kind, fields)
	if !ok {
		return 0, IndexID{}, false
	}
	return accessObjectIdentity(object, false)
}

// ResolveOnlyPrimaryKey maps SQLite rowid only when the reviewed registry has
// exactly one primary-key fact for the model. No autoindex token is parsed.
func ResolveOnlyPrimaryKey(registry *schema.Registry, provider golem.Provider, model golem.ModelID) (AccessKind, IndexID, bool) {
	if registry == nil || model == (golem.ModelID{}) {
		return 0, IndexID{}, false
	}
	var match schema.PhysicalAccessObject
	found := false
	for _, object := range registry.PhysicalKeyAccessObjects(provider, model) {
		if object.Kind() != schema.PhysicalAccessPrimaryKey {
			continue
		}
		if found {
			return 0, IndexID{}, false
		}
		match, found = object, true
	}
	if !found {
		return 0, IndexID{}, false
	}
	return accessObjectIdentity(match, false)
}

// ResolvePhysicalFieldSequence maps provider column tokens through the exact
// registry and preserves order. Unknown, duplicate, or ambiguous columns fail.
func ResolvePhysicalFieldSequence(registry *schema.Registry, provider golem.Provider, model golem.ModelID, columns []string) ([]golem.FieldID, bool) {
	if registry == nil || model == (golem.ModelID{}) || len(columns) == 0 {
		return nil, false
	}
	modelFact, ok := registry.Model(model)
	if !ok {
		return nil, false
	}
	result := make([]golem.FieldID, len(columns))
	seen := make(map[golem.FieldID]struct{}, len(columns))
	for index, column := range columns {
		var match golem.FieldID
		found := false
		for _, fieldID := range modelFact.Fields() {
			field, present := registry.PhysicalField(provider, model, fieldID)
			if !present || string(field.ColumnName()) != column {
				continue
			}
			if found {
				return nil, false
			}
			match, found = fieldID, true
		}
		if !found {
			return nil, false
		}
		if _, duplicate := seen[match]; duplicate {
			return nil, false
		}
		seen[match] = struct{}{}
		result[index] = match
	}
	return result, true
}

func accessObjectIdentity(object schema.PhysicalAccessObject, bitmap bool) (AccessKind, IndexID, bool) {
	var access AccessKind
	switch object.Kind() {
	case schema.PhysicalAccessPrimaryKey:
		access = AccessPrimaryKey
	case schema.PhysicalAccessUniqueIndex:
		access = AccessUniqueIndex
	case schema.PhysicalAccessIndex:
		access = AccessIndex
	default:
		return 0, IndexID{}, false
	}
	var identity IndexID
	if key, ok := object.KeyID(); ok {
		identity = IndexID(key)
	} else if index, ok := object.IndexID(); ok {
		identity = IndexID(index)
	} else {
		return 0, IndexID{}, false
	}
	if bitmap {
		access = AccessBitmapIndex
	}
	return access, identity, identity != (IndexID{})
}
