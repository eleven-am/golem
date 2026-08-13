package extension

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestAddSemanticSearchOperationsCreatesOneProviderNeutralCallerQuery(t *testing.T) {
	compilation := extensionFixture()
	compilation.Model.Schema.PackagePath = "example/social"
	compilation.Contract.Models[0].GraphQLPlural = "accounts"
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related_posts", Space: "content", Dimensions: 3, Fields: []string{"user-name"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	for index, provider := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		compilation.Model.Extensions = append(compilation.Model.Extensions, ir.ProviderExtensionIR{
			ID: ir.ExtensionID(strings.Repeat(string(rune('1'+index)), 32)), Provider: provider, Version: 1,
			Owner: ir.ObjectID("user"), Kind: semanticcontract.IndexKind, Payload: payload,
		})
	}
	if diagnostics := AddSemanticSearchOperations(&compilation); hasErrors(diagnostics) {
		t.Fatalf("semantic search diagnostics = %#v", diagnostics)
	}
	if len(compilation.Contract.CustomOperations) != 1 {
		t.Fatalf("semantic operations = %#v", compilation.Contract.CustomOperations)
	}
	operation := compilation.Contract.CustomOperations[0]
	if operation.Name != "searchAccountsByRelatedPosts" || operation.Operation != ir.CustomOperationQuery || operation.Capability != ir.CustomOperationCallerOnly {
		t.Fatalf("semantic operation = %#v", operation)
	}
	if operation.ExtensionID == "" || operation.Resolver.PackagePath != "example/social" || operation.Resolver.Name != "related_posts" || operation.Resolver.Kind != "customquery" {
		t.Fatalf("semantic resolver identity = %#v", operation)
	}
	if len(operation.Arguments) != 3 || operation.Arguments[0].Name != "query" || operation.Arguments[0].Type.Nullable || operation.Arguments[1].Name != "take" || !operation.Arguments[1].Type.Nullable || operation.Arguments[2].Type.Kind != ir.GraphQLTypePredicate {
		t.Fatalf("semantic arguments = %#v", operation.Arguments)
	}
	if operation.Result.Kind != ir.GraphQLTypeList || operation.Result.Nullable || operation.Result.Element == nil || operation.Result.Element.Kind != ir.GraphQLTypeModel || operation.Result.Element.Nullable {
		t.Fatalf("semantic result = %#v", operation.Result)
	}
	if !IsSemanticSearchOperation(compilation, operation) {
		t.Fatal("generated semantic operation does not match its index authority")
	}
	tampered := operation
	tampered.Name = "searchUsersBySomethingElse"
	if IsSemanticSearchOperation(compilation, tampered) {
		t.Fatal("tampered semantic operation matched its index authority")
	}
}

func TestAddSemanticSearchOperationsRejectsAuthoredRootCollision(t *testing.T) {
	compilation := extensionFixture()
	compilation.Model.Schema.PackagePath = "example/social"
	payload, _ := semanticcontract.Encode(semanticcontract.Index{Name: "content", Space: "content", Dimensions: 3, Fields: []string{"user-name"}, Metric: "cosine"})
	compilation.Model.Extensions = []ir.ProviderExtensionIR{{ID: "semantic-index", Provider: ir.SQLite, Version: 1, Owner: "user", Kind: semanticcontract.IndexKind, Payload: payload}}
	compilation.Contract.CustomOperations = []ir.CustomOperationContractIR{{
		ExtensionID: "authored", Operation: ir.CustomOperationQuery, Name: "searchUsersByContent",
		Resolver: ir.AttachedMethodIR{PackagePath: "example/social", Name: "Search"}, Capability: ir.CustomOperationCallerOnly,
	}}
	diagnostics := AddSemanticSearchOperations(&compilation)
	if !diagnosticCode(diagnostics, "P5_CUSTOM_ROOT_COLLISION") || len(compilation.Contract.CustomOperations) != 1 {
		t.Fatalf("collision diagnostics/atomicity = %#v / %#v", diagnostics, compilation.Contract.CustomOperations)
	}
}

func TestAddSemanticSearchOperationsSkipsHiddenModel(t *testing.T) {
	compilation := extensionFixture()
	compilation.Model.Schema.PackagePath = "example/social"
	compilation.Contract.Models[0].Exposed = false
	payload, _ := semanticcontract.Encode(semanticcontract.Index{Name: "content", Space: "content", Dimensions: 3, Fields: []string{"user-name"}, Metric: "cosine"})
	compilation.Model.Extensions = []ir.ProviderExtensionIR{{ID: "semantic-index", Provider: ir.SQLite, Version: 1, Owner: "user", Kind: semanticcontract.IndexKind, Payload: payload}}
	if diagnostics := AddSemanticSearchOperations(&compilation); len(diagnostics) != 0 {
		t.Fatalf("hidden semantic diagnostics = %#v", diagnostics)
	}
	if len(compilation.Contract.CustomOperations) != 0 {
		t.Fatalf("hidden model gained GraphQL operations = %#v", compilation.Contract.CustomOperations)
	}
}
