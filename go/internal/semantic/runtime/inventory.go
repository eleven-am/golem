// Package runtime validates and owns the provider-neutral runtime inventory
// for generated semantic indexes.
package runtime

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

type Index struct {
	Descriptor       semanticstorage.Descriptor
	Provider         embedding.Provider
	Specification    embedding.Specification
	SpaceFingerprint [32]byte
}

type Inventory struct{ indexes []Index }

func NewInventory(schema physical.PhysicalSchema, registry embedding.Registry) (Inventory, error) {
	result := Inventory{indexes: make([]Index, 0, len(schema.Extensions))}
	seen := make(map[string]bool, len(schema.Extensions))
	for _, extension := range schema.Extensions {
		descriptor, err := semanticstorage.Decode(extension)
		if err != nil {
			return Inventory{}, fmt.Errorf("P9_SEMANTIC_SCHEMA: %w", err)
		}
		key := string(descriptor.ModelID) + "\x00" + descriptor.Name
		if seen[key] {
			return Inventory{}, fmt.Errorf("P9_SEMANTIC_SCHEMA: duplicate semantic index")
		}
		seen[key] = true
		provider, ok := registry.Lookup(descriptor.Space)
		if !ok {
			return Inventory{}, fmt.Errorf("P9_SEMANTIC_CONFIG: embedding space %q is not configured", descriptor.Space)
		}
		specification, ok := registry.Specification(descriptor.Space)
		if !ok || specification.Dimensions() != int(descriptor.Dimensions) {
			return Inventory{}, fmt.Errorf("P9_SEMANTIC_CONFIG: embedding space %q does not match the generated dimensions", descriptor.Space)
		}
		result.indexes = append(result.indexes, Index{
			Descriptor: descriptor, Provider: provider, Specification: specification,
			SpaceFingerprint: sha256.Sum256([]byte(specification.FingerprintInput())),
		})
	}
	sort.Slice(result.indexes, func(i, j int) bool {
		if result.indexes[i].Descriptor.ModelID != result.indexes[j].Descriptor.ModelID {
			return result.indexes[i].Descriptor.ModelID < result.indexes[j].Descriptor.ModelID
		}
		return result.indexes[i].Descriptor.Name < result.indexes[j].Descriptor.Name
	})
	return result, nil
}

func (inventory Inventory) Indexes() []Index {
	return append([]Index(nil), inventory.indexes...)
}
