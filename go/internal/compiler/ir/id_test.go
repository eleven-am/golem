package ir

import (
	"crypto/sha256"
	"testing"
)

func TestStableIDsAreDomainSeparatedAndNonPositional(t *testing.T) {
	registry := NewIDRegistry()
	model, diagnostic := registry.Register(ObjectModel, ModelIdentity("social", "Post"), SourceSpan{})
	if diagnostic != nil {
		t.Fatal(diagnostic.Message)
	}
	field, diagnostic := registry.Register(ObjectField, OwnedIdentity(model.ID, "ID"), SourceSpan{})
	if diagnostic != nil {
		t.Fatal(diagnostic.Message)
	}
	relation, diagnostic := registry.Register(ObjectRelation, OwnedIdentity(model.ID, "ID"), SourceSpan{})
	if diagnostic != nil {
		t.Fatal(diagnostic.Message)
	}
	if len(model.ID) != 32 || len(field.ID) != 32 || len(relation.ID) != 32 {
		t.Fatalf("stable IDs must contain 128 bits: %q %q %q", model.ID, field.ID, relation.ID)
	}
	if field.ID == relation.ID {
		t.Fatal("field and relation domains must not collide for equal identity input")
	}
}

func TestIDRegistryRejectsDuplicateCanonicalIdentity(t *testing.T) {
	registry := NewIDRegistry()
	_, diagnostic := registry.Register(ObjectModel, "social\x00Post", SourceSpan{})
	if diagnostic != nil {
		t.Fatal(diagnostic.Message)
	}
	_, diagnostic = registry.Register(ObjectModel, "social\x00Post", SourceSpan{})
	if diagnostic == nil || diagnostic.Code != "P1_ID_DUPLICATE" {
		t.Fatalf("expected duplicate diagnostic, got %#v", diagnostic)
	}
}

func TestIDRegistryChecksFullAndTruncatedCollisions(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		registry := newIDRegistry(func(_ []byte) [sha256.Size]byte { return [sha256.Size]byte{1} })
		_, _ = registry.Register(ObjectModel, "one", SourceSpan{})
		_, diagnostic := registry.Register(ObjectModel, "two", SourceSpan{})
		if diagnostic == nil || diagnostic.Code != "P1_ID_FULL_COLLISION" {
			t.Fatalf("expected full collision, got %#v", diagnostic)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		calls := byte(0)
		registry := newIDRegistry(func(_ []byte) [sha256.Size]byte {
			calls++
			var result [sha256.Size]byte
			result[0] = 1
			result[31] = calls
			return result
		})
		_, _ = registry.Register(ObjectField, "one", SourceSpan{})
		_, diagnostic := registry.Register(ObjectField, "two", SourceSpan{})
		if diagnostic == nil || diagnostic.Code != "P1_ID_TRUNCATED_COLLISION" {
			t.Fatalf("expected truncated collision, got %#v", diagnostic)
		}
	})
}
