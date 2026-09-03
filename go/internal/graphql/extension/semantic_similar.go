package extension

import (
	"crypto/sha256"
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

// IsSemanticSimilarOperation verifies the complete synthetic contract against
// the logical model's provider-neutral semantic-index authority, mirroring
// IsSemanticSearchOperation for the similarity root.
func IsSemanticSimilarOperation(compilation ir.CompilationIR, operation ir.CustomOperationContractIR) bool {
	return isSemanticOperation(compilation, operation, true)
}

// semanticSimilarOperation takes the model's unique selector rather than an
// opaque record key: the key encoding is internal to managed semantic storage,
// and the selector is already public ABI that the authorized read resolves.
func semanticSimilarOperation(contract ir.ModelContractIR, index semanticcontract.Index, identity ir.ExtensionID, exportedPlural, exportedIndex, packagePath string) ir.CustomOperationContractIR {
	return ir.CustomOperationContractIR{
		ExtensionID: identity,
		Operation:   ir.CustomOperationQuery,
		Name:        "similar" + exportedPlural + "By" + exportedIndex,
		Arguments: []ir.GraphQLArgumentContractIR{
			{Name: "source", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeSelector, Name: contract.GraphQLName, Nullable: false}},
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

func semanticSimilarIdentity(model ir.ModelID, index string) ir.ExtensionID {
	digest := sha256.Sum256([]byte("golem:graphql-semantic-similar:v1\x00" + string(model) + "\x00" + index))
	return ir.ExtensionID(fmt.Sprintf("%x", digest[:16]))
}
