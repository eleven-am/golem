package compatibility

import "testing"

func TestGraphQLSemanticDiffSeparatesAdditiveAndBreakingChanges(t *testing.T) {
	base := mustGraphQLInventory(t, `
scalar DateTime
enum Visibility { PUBLIC PRIVATE }
type Query { post(id: ID!): Post }
type Post { id: ID! title: String! }
input PostInput { title: String! }
`)
	unchanged := mustGraphQLInventory(t, `
scalar DateTime
enum Visibility { PUBLIC PRIVATE }
type Query { post(id: ID!): Post }
type Post { id: ID! title: String! }
input PostInput { title: String! }
`)
	additive := mustGraphQLInventory(t, `
scalar DateTime
enum Visibility { FOLLOWERS PUBLIC PRIVATE }
type Query { post(id: ID!, locale: String): Post posts: [Post!]! }
type Post { id: ID! title: String! excerpt: String }
input PostInput { title: String! body: String }
`)
	requiredInput := mustGraphQLInventory(t, `
scalar DateTime
enum Visibility { PUBLIC PRIVATE }
type Query { post(id: ID!): Post }
type Post { id: ID! title: String! }
input PostInput { title: String! body: String! }
`)
	changedOutput := mustGraphQLInventory(t, `
scalar DateTime
enum Visibility { PUBLIC PRIVATE }
type Query { post(id: ID!): Post }
type Post { id: ID! title: String }
input PostInput { title: String! }
`)
	if got := CompareGraphQL(base, unchanged); got != LayerUnchanged {
		t.Fatalf("unchanged classification = %q", got)
	}
	if got := CompareGraphQL(base, additive); got != LayerAdditive {
		t.Fatalf("additive classification = %q", got)
	}
	if got := CompareGraphQL(base, requiredInput); got != LayerBreaking {
		t.Fatalf("required input classification = %q", got)
	}
	if got := CompareGraphQL(base, changedOutput); got != LayerBreaking {
		t.Fatalf("changed output classification = %q", got)
	}
}

func mustGraphQLInventory(t *testing.T, source string) GraphQLInventory {
	t.Helper()
	value, err := BuildGraphQLInventory([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGraphQLInventoryMergesExtensionsAndCanonicalizesObjectValues(t *testing.T) {
	left := mustGraphQLInventory(t, `
directive @policy(config: PolicyInput = {role: "reader", enabled: true}) on FIELD_DEFINITION
input PolicyInput { enabled: Boolean role: String }
type Query { post: Post }
type Post { id: ID! }
extend type Post { title: String @policy(config: {enabled: true, role: "reader"}) }
extend input PolicyInput { scope: String }
`)
	right := mustGraphQLInventory(t, `
directive @policy(config: PolicyInput = {enabled: true, role: "reader"}) on FIELD_DEFINITION
input PolicyInput { enabled: Boolean role: String }
extend input PolicyInput { scope: String }
type Query { post: Post }
type Post { id: ID! }
extend type Post { title: String @policy(config: {role: "reader", enabled: true}) }
`)
	if CompareGraphQL(left, right) != LayerUnchanged {
		t.Fatal("extension composition or object-value order changed the inventory")
	}
	for _, definition := range left.Definitions {
		if definition.Name == "Post" && len(definition.Fields) != 2 {
			t.Fatalf("Post extension fields = %#v", definition.Fields)
		}
		if definition.Name == "PolicyInput" && len(definition.Fields) != 3 {
			t.Fatalf("PolicyInput extension fields = %#v", definition.Fields)
		}
	}
}

func TestGraphQLDirectiveExpansionsAreAdditive(t *testing.T) {
	base := mustGraphQLInventory(t, `
directive @trace(sample: Boolean) on FIELD_DEFINITION
type Query { ok: Boolean }
`)
	additive := mustGraphQLInventory(t, `
directive @trace(sample: Boolean, label: String) repeatable on FIELD_DEFINITION | OBJECT
type Query { ok: Boolean }
`)
	breaking := mustGraphQLInventory(t, `
directive @trace(sample: Boolean!) on FIELD_DEFINITION
type Query { ok: Boolean }
`)
	if got := CompareGraphQL(base, additive); got != LayerAdditive {
		t.Fatalf("directive expansion classification = %q", got)
	}
	if got := CompareGraphQL(base, breaking); got != LayerBreaking {
		t.Fatalf("directive required argument classification = %q", got)
	}
}
