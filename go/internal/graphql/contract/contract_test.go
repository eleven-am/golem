package contract

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestLowerCamelFrozenInitialisms(t *testing.T) {
	want := map[string]string{"ID": "id", "URLValue": "urlValue", "CreatedAt": "createdAt", "X": "x", "already": "already"}
	for input, expected := range want {
		if actual := LowerCamel(input); actual != expected {
			t.Errorf("LowerCamel(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestNormalizeMaterializesAndOverridesGraphQLContract(t *testing.T) {
	modelID := ir.ModelID("10000000000000000000000000000000")
	compilation := ir.CompilationIR{Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
		ModelID: modelID, GraphQLName: "Post", Exposed: true,
		Fields: []ir.FieldContractIR{{FieldID: "20000000000000000000000000000000", GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}}},
	}}}}
	operations := []ir.Operation{ir.OperationFindOne, ir.OperationFindMany}
	plural := "articles"
	roots := ir.GraphQLRootNamesIR{FindOne: "article"}
	defaultPage, maxPage := uint32(25), uint32(250)
	diagnostics := Normalize(&compilation, []ModelPatch{{ModelID: modelID, Operations: &operations, Plural: &plural, Roots: &roots, DefaultPage: &defaultPage, MaximumPage: &maxPage}})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	model := compilation.Contract.Models[0]
	if compilation.Contract.GraphQLABIVersion != ABIVersion || model.GraphQLPlural != "articles" || model.Roots.FindOne != "article" || model.Roots.FindMany != "articles" || model.Limits.DefaultPageSize != 25 || model.Limits.MaxPageSize != 250 {
		t.Fatalf("normalized model = %#v", model)
	}
	if !reflect.DeepEqual(model.Operations, operations) {
		t.Fatalf("operations = %#v", model.Operations)
	}
}

func TestNormalizeRejectsNamesOperationsLimitsAndRootCollisions(t *testing.T) {
	compilation := ir.CompilationIR{Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{
		{ModelID: "a", GraphQLName: "Post", GraphQLPlural: "posts", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}, Fields: []ir.FieldContractIR{{FieldID: "a1", GraphQLName: "same"}, {FieldID: "a2", GraphQLName: "same"}}, Limits: ir.LimitContractIR{DefaultPageSize: 10, MaxPageSize: 5}},
		{ModelID: "b", GraphQLName: "User", GraphQLPlural: "users", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne, ir.OperationFindOne, "invented"}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}},
	}}}
	diagnostics := Normalize(&compilation, nil)
	want := map[string]bool{"P5_GRAPHQL_FIELD_COLLISION": false, "P5_GRAPHQL_PAGE_LIMIT": false, "P5_GRAPHQL_ROOT_COLLISION": false, "P5_GRAPHQL_OPERATION_DUPLICATE": false, "P5_GRAPHQL_OPERATION_UNKNOWN": false}
	for _, diagnostic := range diagnostics {
		if _, ok := want[diagnostic.Code]; ok {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing %s in %#v", code, diagnostics)
		}
	}
}

func TestGraphQLOnlyChangeDoesNotChangeModelFingerprint(t *testing.T) {
	model := ir.ModelIR{FormatVersion: ir.ModelFormatVersion}
	left := ir.ContractIR{FormatVersion: ir.ContractFormatVersion, GraphQLABIVersion: ABIVersion, Models: []ir.ModelContractIR{{ModelID: "m", GraphQLName: "Post", GraphQLPlural: "posts"}}}
	right := left
	right.Models = append([]ir.ModelContractIR(nil), left.Models...)
	right.Models[0].GraphQLName = "Article"
	leftModel, _ := ir.ModelFingerprint(model)
	rightModel, _ := ir.ModelFingerprint(model)
	leftContract, _ := ir.ContractFingerprint(left)
	rightContract, _ := ir.ContractFingerprint(right)
	if leftModel != rightModel {
		t.Fatal("GraphQL-only change altered ModelFingerprint")
	}
	if leftContract == rightContract {
		t.Fatal("GraphQL-only change did not alter ContractFingerprint")
	}
}
