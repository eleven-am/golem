package codegen

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"strings"
	"testing"
)

func TestPageBindingUsesItsTypedPageBridge(t *testing.T) {
	compilation := ir.CompilationIR{Contract: ir.ContractIR{CustomOperations: []ir.CustomOperationContractIR{{
		Name: "feed", Operation: ir.CustomOperationQuery, ExtensionID: "page-extension",
		Pagination: &ir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "v1"},
		Resolver:   ir.AttachedMethodIR{PackagePath: "example.test/app", Name: "Feed", Kind: "customquery"},
	}}}}
	bindings, err := renderCustomBindings(&compilation, func(path, preferred string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || !strings.Contains(bindings[0], "BindGeneratedPageQueryContract(bundle,") || !strings.Contains(bindings[0], "ResolverName: \"Feed\"") || !strings.HasSuffix(bindings[0], ", Feed)") {
		t.Fatalf("bindings=%#v", bindings)
	}
}
