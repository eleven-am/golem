package extension

import (
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

// AddSemanticSearchOperations exposes every provider-neutral semantic index as
// one caller-only GraphQL query. It runs after authored GraphQL extensions so
// the complete root namespace can be rejected atomically on collision.
func AddSemanticSearchOperations(compilation *ir.CompilationIR) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P9_SEMANTIC_GRAPHQL_COMPILATION_REQUIRED", "semantic GraphQL search requires a compilation", ir.SourceSpan{})}
	}
	contracts := make(map[ir.ModelID]ir.ModelContractIR, len(compilation.Contract.Models))
	for _, contract := range compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	indexes, err := semanticcontract.IndexesByModel(compilation.Model)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P9_SEMANTIC_GRAPHQL_INDEX", "semantic GraphQL search: "+err.Error(), ir.SourceSpan{})}
	}
	if len(indexes) != 0 && compilation.Model.Schema.PackagePath == "" {
		return []ir.Diagnostic{ir.NewError("P9_SEMANTIC_GRAPHQL_PACKAGE_REQUIRED", "semantic GraphQL search requires the generated application package", ir.SourceSpan{})}
	}
	var diagnostics []ir.Diagnostic
	operations := append([]ir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)
	identities := make(map[ir.ExtensionID]bool, len(operations))
	for _, operation := range operations {
		identities[operation.ExtensionID] = true
	}
	for _, model := range compilation.Model.Models {
		contract, present := contracts[model.ID]
		for _, index := range indexes[model.ID] {
			if !present {
				diagnostics = append(diagnostics, ir.NewError("P9_SEMANTIC_GRAPHQL_MODEL", fmt.Sprintf("semantic index %q belongs to an unknown GraphQL model", index.Name), ir.SourceSpan{}))
				continue
			}
			if !contract.Exposed {
				continue
			}
			exported, ok := semanticcontract.ExportedIndexName(index.Name)
			if !ok {
				diagnostics = append(diagnostics, ir.NewError("P9_SEMANTIC_GRAPHQL_NAME", fmt.Sprintf("semantic index %q cannot form a GraphQL search name", index.Name), ir.SourceSpan{}))
				continue
			}
			exportedPlural, ok := semanticcontract.ExportedIndexName(contract.GraphQLPlural)
			if !ok {
				diagnostics = append(diagnostics, ir.NewError("P9_SEMANTIC_GRAPHQL_NAME", fmt.Sprintf("GraphQL plural %q cannot form a semantic search root", contract.GraphQLPlural), ir.SourceSpan{}))
				continue
			}
			identity := semanticSearchIdentity(model.ID, index.Name)
			if identities[identity] {
				diagnostics = append(diagnostics, ir.NewError("P9_SEMANTIC_GRAPHQL_IDENTITY", fmt.Sprintf("semantic index %q has a duplicate GraphQL identity", index.Name), ir.SourceSpan{}))
				continue
			}
			identities[identity] = true
			operations = append(operations, semanticSearchOperation(contract, index, identity, exportedPlural, exported, compilation.Model.Schema.PackagePath))
			similarIdentity := semanticSimilarIdentity(model.ID, index.Name)
			if identities[similarIdentity] {
				diagnostics = append(diagnostics, ir.NewError("P9_SEMANTIC_GRAPHQL_IDENTITY", fmt.Sprintf("semantic index %q has a duplicate GraphQL identity", index.Name), ir.SourceSpan{}))
				continue
			}
			identities[similarIdentity] = true
			operations = append(operations, semanticSimilarOperation(contract, index, similarIdentity, exportedPlural, exported, compilation.Model.Schema.PackagePath))
		}
	}
	if len(diagnostics) == 0 {
		diagnostics = append(diagnostics, validateCollisions(compilation.Contract.Models, operations)...)
	}
	ir.SortDiagnostics(diagnostics)
	if hasErrors(diagnostics) {
		return diagnostics
	}
	compilation.Contract.CustomOperations = operations
	return diagnostics
}

// IsSemanticSearchOperation verifies the complete synthetic contract against
// the logical model's provider-neutral semantic-index authority.
func IsSemanticSearchOperation(compilation ir.CompilationIR, operation ir.CustomOperationContractIR) bool {
	if compilation.Model.Schema.PackagePath == "" || operation.Resolver.PackagePath != compilation.Model.Schema.PackagePath {
		return false
	}
	var contract ir.ModelContractIR
	for _, candidate := range compilation.Contract.Models {
		if operation.Result.Kind == ir.GraphQLTypeList && operation.Result.Element != nil && candidate.GraphQLName == operation.Result.Element.Name {
			contract = candidate
			break
		}
	}
	if contract.ModelID == "" || !contract.Exposed {
		return false
	}
	indexes, err := semanticcontract.IndexesByModel(compilation.Model)
	if err != nil {
		return false
	}
	var expectedIndex semanticcontract.Index
	found := false
	for _, index := range indexes[contract.ModelID] {
		if index.Name == operation.Resolver.Name {
			expectedIndex, found = index, true
			break
		}
	}
	if !found {
		return false
	}
	exported, ok := semanticcontract.ExportedIndexName(expectedIndex.Name)
	if !ok {
		return false
	}
	exportedPlural, ok := semanticcontract.ExportedIndexName(contract.GraphQLPlural)
	if !ok {
		return false
	}
	expected := semanticSearchOperation(contract, expectedIndex, semanticSearchIdentity(contract.ModelID, expectedIndex.Name), exportedPlural, exported, compilation.Model.Schema.PackagePath)
	return reflect.DeepEqual(operation, expected)
}

func semanticSearchOperation(contract ir.ModelContractIR, index semanticcontract.Index, identity ir.ExtensionID, exportedPlural, exportedIndex, packagePath string) ir.CustomOperationContractIR {
	return ir.CustomOperationContractIR{
		ExtensionID: identity,
		Operation:   ir.CustomOperationQuery,
		Name:        "search" + exportedPlural + "By" + exportedIndex,
		Arguments: []ir.GraphQLArgumentContractIR{
			{Name: "query", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: false}},
			{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "where", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypePredicate, Name: contract.GraphQLName, Nullable: true}},
		},
		Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Nullable: false, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: contract.GraphQLName, Nullable: false}},
		Resolver: ir.AttachedMethodIR{
			PackagePath: packagePath,
			Name:        index.Name,
			Kind:        "customquery",
		},
		Capability: ir.CustomOperationCallerOnly,
	}
}

func semanticSearchIdentity(model ir.ModelID, index string) ir.ExtensionID {
	digest := sha256.Sum256([]byte("golem:graphql-semantic-search:v1\x00" + string(model) + "\x00" + index))
	return ir.ExtensionID(fmt.Sprintf("%x", digest[:16]))
}
