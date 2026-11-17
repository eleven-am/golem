package compile

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestValidateCompleteRejectsDataIncarnationProjectionDrift(t *testing.T) {
	compilation := ir.CompilationIR{Contract: ir.ContractIR{DataIncarnation: &ir.DataIncarnationContractIR{Kind: "provider", Version: 1}}}
	if diagnostics := validateComplete(compilation); !containsCode(diagnostics, "P12_DATA_INCARNATION_CONTRACT") {
		t.Fatalf("missing data incarnation agreement diagnostic: %#v", diagnostics)
	}
	compilation.Contract.DataIncarnation = nil
	if diagnostics := validateComplete(compilation); containsCode(diagnostics, "P12_DATA_INCARNATION_CONTRACT") {
		t.Fatalf("ordinary compilation incorrectly requires data incarnation: %#v", diagnostics)
	}
}
