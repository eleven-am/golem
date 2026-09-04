package sqlite

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

func legacySemanticExtension(t *testing.T, identity string) physical.Extension {
	t.Helper()
	attributes := []physical.Attribute{
		{Name: "name", Value: physical.SemanticValue{Kind: physical.ValueString, String: "content"}},
		{Name: "space", Value: physical.SemanticValue{Kind: physical.ValueString, String: "content"}},
		{Name: "metric", Value: physical.SemanticValue{Kind: physical.ValueString, String: "cosine"}},
		{Name: "storage", Value: physical.SemanticValue{Kind: physical.ValueString, String: "_golem_semantic_legacy"}},
		{Name: "dimensions", Value: physical.SemanticValue{Kind: physical.ValueInteger, Integer: 3}},
		{Name: "fields", Value: physical.SemanticValue{Kind: physical.ValueString, String: "example.Article.Title"}},
	}
	if identity != "" {
		attributes = append(attributes, physical.Attribute{Name: "identity", Value: physical.SemanticValue{Kind: physical.ValueString, String: identity}})
	}
	return physical.Extension{
		ID:         ir.ExtensionID("legacy"),
		Kind:       semanticcontract.IndexKind,
		Version:    semanticcontract.Version,
		Owner:      physical.ObjectRef{Kind: ir.ObjectModel, ModelID: "example.Article"},
		Attributes: attributes,
	}
}

func TestReviewedReplayRendersASemanticExtensionWithoutAnIdentityProjection(t *testing.T) {
	extension := legacySemanticExtension(t, "")
	if _, err := renderSemanticExtension(extension, false); err == nil {
		t.Fatal("a current compile must still refuse an absent identity projection")
	}
	statements, err := renderSemanticExtension(extension, true)
	if err != nil {
		t.Fatalf("reviewed replay refused a historical extension: %v", err)
	}
	if !strings.Contains(statements[0], "CREATE TABLE") {
		t.Fatalf("first statement is not the state table: %q", statements[0])
	}
}

func TestSemanticStatementCountMatchesTheIntrospectionRegistry(t *testing.T) {
	for name, expected := range map[string]struct {
		identity   string
		statements int
		indexes    int
	}{
		"legacy":  {"", 2, 0},
		"current": {"id:sqlite.text:0:0:0:1", 4, 2},
	} {
		t.Run(name, func(t *testing.T) {
			extension := legacySemanticExtension(t, expected.identity)
			descriptor, err := semanticstorage.Decode(extension)
			if err != nil {
				t.Fatal(err)
			}
			statements, err := renderSemanticExtension(extension, expected.identity == "")
			if err != nil {
				t.Fatal(err)
			}
			if len(statements) != expected.statements {
				t.Fatalf("rendered %d statements, want %d", len(statements), expected.statements)
			}
			if names := semanticStateIndexNames(descriptor); len(names) != expected.indexes {
				t.Fatalf("registry claims %d state indexes, want %d", len(names), expected.indexes)
			}
			if len(statements) != len(semanticStateIndexNames(descriptor))+2 {
				t.Fatal("introspection would reject statements the renderer produced")
			}
		})
	}
}
