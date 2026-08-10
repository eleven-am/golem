package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

type provider struct{ specification embedding.Specification }

func (value provider) Specification() embedding.Specification { return value.specification }
func (provider) Embed(context.Context, []embedding.Input) ([]embedding.Vector, error) {
	return nil, nil
}

func semanticSchema(t *testing.T, dimensions uint16) physical.PhysicalSchema {
	t.Helper()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: dimensions, Fields: []string{"title"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "semantic-post-related", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: 1, Owner: "post", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return physical.PhysicalSchema{Extensions: []physical.Extension{extension}}
}

func TestInventoryRequiresExactConfiguredDimensions(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 16)
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider{specification: specification}})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(semanticSchema(t, 3), registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Indexes()) != 1 || inventory.Indexes()[0].SpaceFingerprint == ([32]byte{}) {
		t.Fatalf("inventory=%#v", inventory.Indexes())
	}
	if _, err := NewInventory(semanticSchema(t, 4), registry); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("dimension mismatch=%v", err)
	}
	if _, err := NewInventory(semanticSchema(t, 3), embedding.Registry{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing provider=%v", err)
	}
}
