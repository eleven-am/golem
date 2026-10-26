package compile

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestTxOperationsAreRecordedOutsideTheGraphQLContract(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/tx_operations", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	contract := result.Compilation.Contract
	var names []string
	for _, operation := range contract.TxOperations {
		names = append(names, operation.Name)
		if operation.ExtensionID == "" || operation.Operation.Kind != "txoperation" || operation.Operation.PackagePath != result.Compilation.Model.Schema.PackagePath {
			t.Fatalf("transaction operation = %#v", operation)
		}
	}
	if want := []string{"countUsers", "recordUsers"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("transaction operations = %v, want %v", names, want)
	}
	if contract.TxOperations[1].Operation.Name != "RecordUsers" {
		t.Fatalf("recordUsers names %#v", contract.TxOperations[1].Operation)
	}
	if len(contract.CustomOperations) != 1 || contract.CustomOperations[0].Name != "renameUsers" {
		t.Fatalf("custom operations = %#v", contract.CustomOperations)
	}
	document, err := graphqlschema.Build(*result.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.SDL, "renameUsers") || strings.Contains(document.SDL, "recordUsers") || strings.Contains(document.SDL, "countUsers") {
		t.Fatalf("SDL exposes a transaction operation or lost the custom mutation:\n%s", document.SDL)
	}
	canonical, err := ir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"txOperations":[`) {
		t.Fatalf("canonical contract omits transaction operations: %s", canonical)
	}
	withoutOperations := contract
	withoutOperations.TxOperations = nil
	bare, err := ir.CanonicalContract(withoutOperations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "txOperations") {
		t.Fatalf("a contract without transaction operations changed its canonical bytes: %s", bare)
	}
}

func TestTxOperationDeclarationsReportGoldenDiagnostics(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/tx_operations_invalid", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil {
		t.Fatal("invalid transaction operations were accepted")
	}
	type finding struct {
		code string
		line int
	}
	var findings []finding
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Primary.RelativeFile != "internal/compiler/compile/testdata/tx_operations_invalid/schema.go" {
			t.Fatalf("diagnostic span = %#v", diagnostic)
		}
		findings = append(findings, finding{code: diagnostic.Code, line: int(diagnostic.Primary.StartLine)})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].line < findings[j].line })
	want := []finding{
		{code: "P11_OPERATION_SIGNATURE", line: 32},
		{code: "P11_OPERATION_NAME", line: 33},
		{code: "P11_OPERATION_DUPLICATE", line: 35},
		{code: "P11_OPERATION_SIGNATURE", line: 36},
		{code: "P11_OPERATION_FUNCTION", line: 37},
		{code: "P11_OPERATION_DUPLICATE", line: 38},
	}
	if !reflect.DeepEqual(findings, want) {
		t.Fatalf("diagnostics = %+v, want %+v (all %#v)", findings, want, result.Diagnostics)
	}
}
