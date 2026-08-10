package golem

import (
	"context"
	"strconv"
	"testing"
)

type graphQLExtensionUser struct{}
type graphQLExtensionArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

var graphQLExtensionName = GeneratedTextField[graphQLExtensionUser, string](FieldID{1})

func (graphQLExtensionUser) DefineGraphQL(graphql *GraphQLModel[graphQLExtensionUser]) {
	ComputedField(graphql, "greeting", GraphQLString().NonNull(), graphQLExtensionUser{}.Greeting, Requires(graphQLExtensionName))
	BatchedComputedField(graphql, "batchGreeting", GraphQLString(), graphQLExtensionName, loadGraphQLExtensionGreetings, 64, Requires(graphQLExtensionName))
	BatchedComputedFieldWithCacheKey(graphql, "canonicalGreeting", GraphQLString(), graphQLExtensionName, loadGraphQLExtensionGreetings, graphQLExtensionCacheKey, 64, Requires(graphQLExtensionName))
}

func (graphQLExtensionUser) Greeting(context.Context, Row[graphQLExtensionUser], graphQLExtensionArgs) (string, error) {
	return "", nil
}

func loadGraphQLExtensionGreetings(context.Context, []string, graphQLExtensionArgs) (map[string]string, error) {
	return nil, nil
}

func graphQLExtensionCacheKey(value string) (string, error) {
	return strconv.Quote(value), nil
}

func defineGraphQLExtensionSchema(schema *GraphQLSchema) {
	Query(schema, "searchUsers", searchGraphQLExtensionUsers)
	Mutation(schema, "renameUser", renameGraphQLExtensionUser)
}

func searchGraphQLExtensionUsers(context.Context, *struct{}, graphQLExtensionArgs) ([]Row[graphQLExtensionUser], error) {
	return nil, nil
}

func renameGraphQLExtensionUser(context.Context, *struct{}, graphQLExtensionArgs) (Row[graphQLExtensionUser], error) {
	return Row[graphQLExtensionUser]{}, nil
}

func TestGraphQLStaticExtensionDeclarationsAreRuntimeInert(t *testing.T) {
	graphQLExtensionUser{}.DefineGraphQL(nil)
	defineGraphQLExtensionSchema(nil)
	if err := MaskedDependency("User.greeting", "name"); err == nil {
		t.Fatal("MaskedDependency returned nil")
	}
}
