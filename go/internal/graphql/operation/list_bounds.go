package operation

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

// ListBound identifies whether take is enforced by an operation owner. Custom
// resolver arguments named take do not narrow their declared result maximum.
type ListBound struct {
	Maximum     int
	TakeBounded bool
}

func (c *Compiler) ListBounds() (map[string]ListBound, error) {
	bounds := map[string]ListBound{}
	if c.analytics != nil {
		for root, maximum := range c.analytics.GroupRootMaxima() {
			bounds["Query."+root] = ListBound{Maximum: maximum, TakeBounded: true}
		}
	}
	for _, operation := range c.compilation.Contract.CustomOperations {
		if _, searchTake := graphqlextension.SearchTakeDefault(c.compilation, operation); searchTake {
			continue
		}
		if capacity, _ := operation.Result.CompositeListCapacity(); operation.Result.Kind == compilerir.GraphQLTypeList {
			bounds[rootTypeName(operation.Operation)+"."+operation.Name] = ListBound{Maximum: capacity}
		}
	}
	reachable, err := graphqlschema.ReachableTypes(c.compilation)
	if err != nil {
		return nil, err
	}
	for _, definition := range c.compilation.Contract.GraphQLTypes {
		if definition.Kind != compilerir.GraphQLTypeObject || !reachable[definition.Name] {
			continue
		}
		for _, field := range definition.Fields {
			if capacity, _ := field.Type.CompositeListCapacity(); field.Type.Kind == compilerir.GraphQLTypeList {
				bounds[definition.Name+"."+field.Name] = ListBound{Maximum: capacity}
			}
		}
	}
	for _, contract := range c.compilation.Contract.Models {
		if !reachable[contract.GraphQLName] {
			continue
		}
		for _, field := range contract.Computed {
			if capacity, _ := field.Result.CompositeListCapacity(); field.Result.Kind == compilerir.GraphQLTypeList {
				bounds[contract.GraphQLName+"."+field.Name] = ListBound{Maximum: capacity}
			}
		}
	}
	return bounds, nil
}

func rootTypeName(kind compilerir.CustomOperationKind) string {
	if kind == compilerir.CustomOperationMutation {
		return "Mutation"
	}
	return "Query"
}
