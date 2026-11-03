package extension

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func generatedSearchCompilation(t *testing.T, limits ir.LimitContractIR) ir.CompilationIR {
	t.Helper()
	compilation := extensionFixture()
	compilation.Model.Schema.PackagePath = "example/social"
	compilation.Contract.Models[0].GraphQLPlural = "accounts"
	compilation.Contract.Models[0].Limits = limits
	semanticPayload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related_posts", Space: "content", Dimensions: 3, Fields: []string{"user-name"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	fullTextPayload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "mail_content", Folding: fulltextcontract.FoldingDiacritics, Prefix: []uint8{}, Fields: []fulltextcontract.Field{{ID: "user-name", Weight: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for index, provider := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		compilation.Model.Extensions = append(compilation.Model.Extensions,
			ir.ProviderExtensionIR{ID: ir.ExtensionID(strings.Repeat(string(rune('1'+index)), 32)), Provider: provider, Version: 1, Owner: "user", Kind: semanticcontract.IndexKind, Payload: semanticPayload},
			ir.ProviderExtensionIR{ID: ir.ExtensionID(strings.Repeat(string(rune('5'+index)), 32)), Provider: provider, Version: 1, Owner: "user", Kind: fulltextcontract.IndexKind, Payload: fullTextPayload},
		)
	}
	if diagnostics := AddSemanticSearchOperations(&compilation); hasErrors(diagnostics) {
		t.Fatalf("semantic diagnostics = %#v", diagnostics)
	}
	if diagnostics := AddFullTextSearchOperations(&compilation); hasErrors(diagnostics) {
		t.Fatalf("full-text diagnostics = %#v", diagnostics)
	}
	if len(compilation.Contract.CustomOperations) != 3 {
		t.Fatalf("generated operations = %#v", compilation.Contract.CustomOperations)
	}
	return compilation
}

func TestGeneratedSearchRootsDeclareTheirPageMaximum(t *testing.T) {
	cases := []struct {
		name   string
		limits ir.LimitContractIR
		want   uint32
	}{
		{"ceiling of the search engines", ir.LimitContractIR{DefaultPageSize: 50, MaxPageSize: 5000}, 1000},
		{"model page maximum", ir.LimitContractIR{DefaultPageSize: 50, MaxPageSize: 500}, 500},
		{"model take maximum", ir.LimitContractIR{DefaultPageSize: 10, MaxPageSize: 500, MaxTake: 30}, 30},
		{"unset take maximum", ir.LimitContractIR{DefaultPageSize: 50, MaxPageSize: 40}, 40},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			compilation := generatedSearchCompilation(t, test.limits)
			for _, operation := range compilation.Contract.CustomOperations {
				if operation.Result.Kind != ir.GraphQLTypeList || operation.Result.MaxItems != test.want {
					t.Fatalf("%s result maximum = %d, want %d", operation.Name, operation.Result.MaxItems, test.want)
				}
				if len(operation.Result.ResultListBoundViolations()) != 0 {
					t.Fatalf("%s violates the declared-maximum rule", operation.Name)
				}
			}
		})
	}
}

func TestGeneratedSearchRootsAreRecognisedOnlyWithTheirDerivedMaximum(t *testing.T) {
	compilation := generatedSearchCompilation(t, ir.LimitContractIR{DefaultPageSize: 50, MaxPageSize: 500})
	for _, operation := range compilation.Contract.CustomOperations {
		tampered := operation
		tampered.Result.MaxItems++
		recognised := IsSemanticSearchOperation(compilation, tampered) || IsSemanticSimilarOperation(compilation, tampered) || IsFullTextSearchOperation(compilation, tampered)
		if recognised {
			t.Fatalf("%s with a forged maximum was recognised as a generated root", operation.Name)
		}
		genuine := IsSemanticSearchOperation(compilation, operation) || IsSemanticSimilarOperation(compilation, operation) || IsFullTextSearchOperation(compilation, operation)
		if !genuine {
			t.Fatalf("%s was not recognised as a generated root", operation.Name)
		}
	}
}

func TestNormalizeRejectsUndeclaredAndMisplacedListMaximaInDeclarations(t *testing.T) {
	stringList := ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}
	boundedArgument := stringList
	boundedArgument.MaxItems = 4
	cases := []struct {
		name     string
		computed []ComputedDeclaration
		custom   []CustomOperationDeclaration
		code     string
		mention  string
	}{
		{
			name: "custom list without a maximum", code: "P5_GRAPHQL_LIST_MAX_REQUIRED", mention: "custom query tags result list level 1",
			custom: []CustomOperationDeclaration{{Operation: customCallerOnly(validCustom("tags", "tags", stringList))}},
		},
		{
			name: "computed list without a maximum", code: "P5_GRAPHQL_LIST_MAX_REQUIRED", mention: "computed field User.words result list level 1",
			computed: []ComputedDeclaration{{ModelID: "user", Field: func() ir.ComputedFieldContractIR {
				field := validComputed("words", "words", []ir.FieldID{"user-name"})
				field.Result = stringList
				return field
			}()}},
		},
		{
			name: "maximum on a custom argument", code: "P5_GRAPHQL_LIST_MAX_SCALAR", mention: "custom query tags argument ids",
			custom: []CustomOperationDeclaration{{Operation: func() ir.CustomOperationContractIR {
				operation := customCallerOnly(validCustom("tags", "tags", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}))
				operation.Arguments = []ir.GraphQLArgumentContractIR{{Name: "ids", Type: boundedArgument}}
				return operation
			}()}},
		},
		{
			name: "maximum on a scalar custom result", code: "P5_GRAPHQL_LIST_MAX_SCALAR", mention: "custom query tags result level 1",
			custom: []CustomOperationDeclaration{{Operation: customCallerOnly(validCustom("tags", "tags", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", MaxItems: 2}))}},
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			compilation := extensionFixture()
			found := false
			for _, diagnostic := range Normalize(&compilation, test.computed, test.custom) {
				if diagnostic.Code == test.code && strings.Contains(diagnostic.Message, test.mention) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s mentioning %q", test.code, test.mention)
			}
			if len(compilation.Contract.CustomOperations) != 0 || len(compilation.Contract.Models[0].Computed) != 0 {
				t.Fatal("a rejected declaration changed the contract")
			}
		})
	}
}

func customCallerOnly(operation ir.CustomOperationContractIR) ir.CustomOperationContractIR {
	operation.Capability = ir.CustomOperationCallerOnly
	return operation
}
