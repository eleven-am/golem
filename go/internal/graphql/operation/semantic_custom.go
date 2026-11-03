package operation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

type semanticCustomRoot struct {
	where *golem.FrozenPredicate
}

func (c *Compiler) bindSemanticSearchTake(operation compilerir.CustomOperationContractIR, arguments map[string]any, explicit bool) (bool, error) {
	if c == nil || !(graphqlextension.IsSemanticSearchOperation(c.compilation, operation) || graphqlextension.IsSemanticSimilarOperation(c.compilation, operation) || graphqlextension.IsFullTextSearchOperation(c.compilation, operation)) {
		return false, nil
	}
	contract, ok := c.modelContractByGraphQLName(customResultModelName(operation.Result))
	if !ok {
		return false, fmt.Errorf("semantic search result model is absent")
	}
	maximum := semanticruntime.MaximumResults
	maximum = readir.NarrowCap(maximum, int(contract.Limits.MaxPageSize))
	maximum = readir.NarrowCap(maximum, int(contract.Limits.MaxTake))
	maximum = readir.NarrowCap(maximum, c.limits.Bind.MaxPageSize)
	value := int(contract.Limits.DefaultPageSize)
	if value < 1 {
		value = 50
	}
	if value > maximum {
		value = maximum
	}
	if supplied, present := arguments["take"]; explicit && present && supplied != nil {
		integer, ok := supplied.(int32)
		if !ok {
			return false, fmt.Errorf("semantic search take has value %T", supplied)
		}
		value = int(integer)
	}
	if value < 1 || value > maximum {
		return false, fmt.Errorf("semantic search take must be between 1 and %d", maximum)
	}
	// Generated semantic resolvers receive an exact non-nil value. The runtime
	// operation compiler, not generated source, owns the effective limit.
	arguments["take"] = int32(value)
	return true, nil
}

func (c *Compiler) newSemanticCustomRoot(operation compilerir.CustomOperationContractIR, modelID compilerir.ModelID, arguments map[string]any) (*semanticCustomRoot, error) {
	if !graphqlextension.IsSemanticSearchOperation(c.compilation, operation) && !graphqlextension.IsSemanticSimilarOperation(c.compilation, operation) && !graphqlextension.IsFullTextSearchOperation(c.compilation, operation) {
		return nil, fmt.Errorf("semantic search contract is not authoritative")
	}
	publicModel, err := publicModelID(modelID)
	if err != nil {
		return nil, err
	}
	result := &semanticCustomRoot{}
	if raw, present := arguments["where"]; present && raw != nil {
		where, ok := raw.(golem.FrozenPredicate)
		if !ok || where.View().RootModelID() != publicModel {
			return nil, fmt.Errorf("semantic search predicate is not authoritative")
		}
		result.where = &where
	}
	return result, nil
}

func (c *Compiler) modelContractByGraphQLName(name string) (compilerir.ModelContractIR, bool) {
	for _, model := range c.compilation.Contract.Models {
		if model.GraphQLName == name {
			return model, true
		}
	}
	return compilerir.ModelContractIR{}, false
}

func customResultModelName(result compilerir.GraphQLTypeIR) string {
	if result.Kind == compilerir.GraphQLTypeModel {
		return result.Name
	}
	if result.Kind == compilerir.GraphQLTypeList && result.Element != nil {
		return customResultModelName(*result.Element)
	}
	return ""
}
