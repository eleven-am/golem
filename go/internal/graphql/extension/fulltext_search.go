package extension

import (
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
)

// AddFullTextSearchOperations exposes every full-text index as one caller-only
// GraphQL query after authored operations have reserved their root names.
func AddFullTextSearchOperations(compilation *ir.CompilationIR) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P9_FULLTEXT_GRAPHQL_COMPILATION_REQUIRED", "full-text GraphQL search requires a compilation", ir.SourceSpan{})}
	}
	contracts := make(map[ir.ModelID]ir.ModelContractIR, len(compilation.Contract.Models))
	for _, contract := range compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	indexes, err := fulltextcontract.IndexesByModel(compilation.Model)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P9_FULLTEXT_GRAPHQL_INDEX", "full-text GraphQL search: "+err.Error(), ir.SourceSpan{})}
	}
	if len(indexes) != 0 && compilation.Model.Schema.PackagePath == "" {
		return []ir.Diagnostic{ir.NewError("P9_FULLTEXT_GRAPHQL_PACKAGE_REQUIRED", "full-text GraphQL search requires the generated application package", ir.SourceSpan{})}
	}
	operations := append([]ir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)
	identities := make(map[ir.ExtensionID]bool, len(operations))
	for _, operation := range operations {
		identities[operation.ExtensionID] = true
	}
	var diagnostics []ir.Diagnostic
	for _, model := range compilation.Model.Models {
		contract, present := contracts[model.ID]
		for _, index := range indexes[model.ID] {
			if !present {
				diagnostics = append(diagnostics, ir.NewError("P9_FULLTEXT_GRAPHQL_MODEL", fmt.Sprintf("full-text index %q belongs to an unknown GraphQL model", index.Name), ir.SourceSpan{}))
				continue
			}
			if !contract.Exposed {
				continue
			}
			exported, ok := fulltextcontract.ExportedIndexName(index.Name)
			if !ok {
				diagnostics = append(diagnostics, ir.NewError("P9_FULLTEXT_GRAPHQL_NAME", fmt.Sprintf("full-text index %q cannot form a GraphQL search name", index.Name), ir.SourceSpan{}))
				continue
			}
			exportedPlural, ok := fulltextcontract.ExportedIndexName(contract.GraphQLPlural)
			if !ok {
				diagnostics = append(diagnostics, ir.NewError("P9_FULLTEXT_GRAPHQL_NAME", fmt.Sprintf("GraphQL plural %q cannot form a full-text search root", contract.GraphQLPlural), ir.SourceSpan{}))
				continue
			}
			identity := fullTextSearchIdentity(model.ID, index.Name)
			if identities[identity] {
				diagnostics = append(diagnostics, ir.NewError("P9_FULLTEXT_GRAPHQL_IDENTITY", fmt.Sprintf("full-text index %q has a duplicate GraphQL identity", index.Name), ir.SourceSpan{}))
				continue
			}
			identities[identity] = true
			operations = append(operations, fullTextSearchOperation(contract, index, identity, exportedPlural, exported, compilation.Model.Schema.PackagePath))
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

// IsFullTextSearchOperation verifies a generated root against its index.
func IsFullTextSearchOperation(compilation ir.CompilationIR, operation ir.CustomOperationContractIR) bool {
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
	indexes, err := fulltextcontract.IndexesByModel(compilation.Model)
	if err != nil {
		return false
	}
	for _, index := range indexes[contract.ModelID] {
		if index.Name != operation.Resolver.Name {
			continue
		}
		exported, ok := fulltextcontract.ExportedIndexName(index.Name)
		if !ok {
			return false
		}
		exportedPlural, ok := fulltextcontract.ExportedIndexName(contract.GraphQLPlural)
		if !ok {
			return false
		}
		expected := fullTextSearchOperation(contract, index, fullTextSearchIdentity(contract.ModelID, index.Name), exportedPlural, exported, compilation.Model.Schema.PackagePath)
		return reflect.DeepEqual(operation, expected)
	}
	return false
}

func fullTextSearchOperation(contract ir.ModelContractIR, index fulltextcontract.Index, identity ir.ExtensionID, exportedPlural, exportedIndex, packagePath string) ir.CustomOperationContractIR {
	return ir.CustomOperationContractIR{
		ExtensionID: identity, Operation: ir.CustomOperationQuery, Name: "textSearch" + exportedPlural + "By" + exportedIndex,
		Arguments: []ir.GraphQLArgumentContractIR{
			{Name: "query", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: false}},
			{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "where", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypePredicate, Name: contract.GraphQLName, Nullable: true}},
		},
		Result:   ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Nullable: false, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: contract.GraphQLName, Nullable: false}},
		Resolver: ir.AttachedMethodIR{PackagePath: packagePath, Name: index.Name, Kind: "customquery"}, Capability: ir.CustomOperationCallerOnly,
	}
}

func fullTextSearchIdentity(model ir.ModelID, index string) ir.ExtensionID {
	digest := sha256.Sum256([]byte("golem:graphql-fulltext-search:v1\x00" + string(model) + "\x00" + index))
	return ir.ExtensionID(fmt.Sprintf("%x", digest[:16]))
}
