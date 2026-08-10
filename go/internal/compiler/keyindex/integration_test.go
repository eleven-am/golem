package keyindex

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/resolve"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
)

func TestFrontendBaseResolverFragmentIntegration(t *testing.T) {
	extracted := schema.Extract(context.Background(), schema.Config{Dir: "../schema/testdata/social", Pattern: "."})
	if len(extracted.Diagnostics) != 0 {
		t.Fatalf("source extraction failed: %#v", extracted.Diagnostics)
	}
	resolved := resolve.Base(extracted.Raw)
	if len(resolved.Diagnostics) != 0 {
		t.Fatalf("base resolution failed: %#v", resolved.Diagnostics)
	}
	linked := Link(extracted.Raw, resolved.Compilation, nil, resolved.IDs)
	if len(linked.Diagnostics) != 0 {
		t.Fatalf("key/index linking failed: %#v", linked.Diagnostics)
	}
	if len(linked.Fragments) != 2 {
		t.Fatalf("got %d model fragments, want 2", len(linked.Fragments))
	}
	primaryCount, indexCount := 0, 0
	for _, fragment := range linked.Fragments {
		if fragment.PrimaryKey != nil {
			primaryCount++
		}
		indexCount += len(fragment.Indexes)
	}
	if primaryCount != 2 || indexCount != 1 {
		t.Fatalf("unexpected linked objects: %#v", linked.Fragments)
	}
	if diagnostics := ApplyFragments(&resolved.Compilation, linked.Fragments); len(diagnostics) != 0 {
		t.Fatalf("fragment application failed: %#v", diagnostics)
	}
	for _, model := range resolved.Compilation.Model.Models {
		if model.PrimaryKey == nil {
			t.Fatalf("model %s has no applied primary key", model.LogicalName)
		}
	}
}
