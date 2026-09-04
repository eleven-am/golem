package schema

import (
	"context"
	"strings"
	"testing"
)

func TestExtractRejectsSystemOnRelationsAndKeepsSystemOnScalars(t *testing.T) {
	result := Extract(context.Background(), Config{Dir: "testdata/systemconflict", Pattern: "."})
	relation := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "P1_GOLEM_TAG_SYSTEM_RELATION" {
			relation = true
			if !strings.Contains(diagnostic.Message, "system") {
				t.Fatalf("relation diagnostic does not name the mode: %q", diagnostic.Message)
			}
		}
		if diagnostic.Code == "P1_GOLEM_TAG_UNKNOWN" && strings.Contains(diagnostic.Message, "system") {
			t.Fatalf("system is not a recognised scalar attribute: %#v", diagnostic)
		}
	}
	if !relation {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}
