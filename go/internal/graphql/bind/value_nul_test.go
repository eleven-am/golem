package bind

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestGraphQLStringInputRejectsNUL(t *testing.T) {
	state := &inputState{}
	if _, err := state.value(compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, "nul\x00byte"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("GraphQL String with NUL error=%v; want a NUL value-domain refusal", err)
	}
	if _, err := state.value(compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, "nul byte"); err != nil {
		t.Fatal(err)
	}
}
