package operation

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

func (c *Compiler) ListBounds() map[string]int {
	bounds := map[string]int{}
	if c.analytics != nil {
		for root, maximum := range c.analytics.GroupRootMaxima() {
			bounds["Query."+root] = maximum
		}
	}
	for _, operation := range c.compilation.Contract.CustomOperations {
		if _, searchTake := graphqlextension.SearchTakeDefault(c.compilation, operation); searchTake {
			continue
		}
		if capacity, composite := operation.Result.CompositeListCapacity(); composite {
			bounds[rootTypeName(operation.Operation)+"."+operation.Name] = capacity
		}
	}
	for _, contract := range c.compilation.Contract.Models {
		if !contract.Exposed {
			continue
		}
		for _, field := range contract.Computed {
			if capacity, composite := field.Result.CompositeListCapacity(); composite {
				bounds[contract.GraphQLName+"."+field.Name] = capacity
			}
		}
	}
	return bounds
}

func rootTypeName(kind compilerir.CustomOperationKind) string {
	if kind == compilerir.CustomOperationMutation {
		return "Mutation"
	}
	return "Query"
}
