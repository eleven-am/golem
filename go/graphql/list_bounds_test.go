package graphql

import (
	"context"
	"strings"
	"testing"
)

type boundsExecutor struct{ bounds map[string]int }

func (boundsExecutor) Execute(context.Context, int, Operation) Response {
	return Response{Data: map[string]any{}}
}

func (executor boundsExecutor) ListBounds() ListBounds { return NewListBounds(executor.bounds) }

const listBoundsSDL = `type Query {
  me: [User!]!
  tags: [String!]!
  users(take: Int = 2): [User!]!
  groups(by: [String!]!, take: Int): [Group!]!
}
type User { id: Int! collections(take: Int = 3): [Collection!]! related: [[Collection!]!]! }
type Collection { id: Int! }
type Group { key: String! }`

var listBoundsTable = map[string]int{"Query.me": 1, "Query.groups": 10_000, "User.related": 6}

func listBoundsServer(t *testing.T, limits Limits, bounds map[string]int) (*Server[int], error) {
	t.Helper()
	return NewServer(listBoundsSDL, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits:               limits,
		ReportInternalError:  func(context.Context, error) {},
	}, boundsExecutor{bounds: bounds})
}

func complexityOf(t *testing.T, limits Limits, query string) int {
	t.Helper()
	limits.MaxComplexity = 1
	server, err := listBoundsServer(t, limits, listBoundsTable)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 1, Request{Query: query})
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "QUERY_LIMIT_EXCEEDED" {
		t.Fatalf("query %q was not refused by the complexity limit: %#v", query, response)
	}
	cost, ok := response.Errors[0].Extensions["cost"].(int)
	if !ok {
		t.Fatalf("query %q refusal carries no cost: %#v", query, response.Errors[0].Extensions)
	}
	return cost
}

func TestComplexityUsesDeclaredListBounds(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		query string
		cost  int
	}{
		{"custom list with an explicit child take", `{ me { collections(take: 1) { id } } }`, 1 + 1*(1+1*1)},
		{"custom list with the child SDL default", `{ me { collections { id } } }`, 1 + 1*(1+3*1)},
		{"nested custom list at the product of its bounds", `{ users(take: 1) { related { id } } }`, 1 + 1*(1+6*1)},
		{"groupBy without take at its group maximum", `{ groups(by: ["a"]) { key } }`, 1 + 10_000*1},
		{"explicit take wins over a bound", `{ groups(by: ["a"], take: 5) { key } }`, 1 + 5*1},
		{"negative take counts its magnitude", `{ groups(by: ["a"], take: -4) { key } }`, 1 + 4*1},
		{"SDL default wins when no take is given", `{ users { id collections(take: 1) { id } } }`, 1 + 2*(1+1+1*1)},
		{"scalar list is a leaf", `{ tags me { id } }`, 1 + (1 + 1*1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if cost := complexityOf(t, Limits{}, testCase.query); cost != testCase.cost {
				t.Fatalf("cost = %d, want %d", cost, testCase.cost)
			}
		})
	}
}

func TestComplexityIgnoresMaxPageSize(t *testing.T) {
	queries := []string{
		`{ me { collections { id } } }`,
		`{ users(take: 1) { related { id } } }`,
		`{ groups(by: ["a"]) { key } }`,
	}
	for _, query := range queries {
		small := complexityOf(t, Limits{MaxPageSize: 500}, query)
		large := complexityOf(t, Limits{MaxPageSize: 1000}, query)
		if small != large {
			t.Fatalf("%s costs %d at MaxPageSize 500 and %d at 1000", query, small, large)
		}
	}
}

func TestNewServerRejectsExecutorMissingABound(t *testing.T) {
	for _, missing := range []string{"Query.me", "Query.groups", "User.related"} {
		bounds := map[string]int{}
		for key, bound := range listBoundsTable {
			if key != missing {
				bounds[key] = bound
			}
		}
		_, err := listBoundsServer(t, Limits{}, bounds)
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "declares no list bound") {
			t.Fatalf("missing %s: error = %v", missing, err)
		}
	}
}

func TestNewServerRejectsBoundForUnknownField(t *testing.T) {
	for name, extra := range map[string]string{
		"a field that does not exist":    "Query.nothing",
		"a scalar list":                  "Query.tags",
		"a list with a take default":     "Query.users",
		"a relation list with a default": "User.collections",
		"a type that does not exist":     "Ghost.items",
		"an introspection list":          "__Schema.types",
		"a field that is not a list":     "Group.key",
	} {
		t.Run(name, func(t *testing.T) {
			bounds := map[string]int{extra: 5}
			for key, bound := range listBoundsTable {
				bounds[key] = bound
			}
			_, err := listBoundsServer(t, Limits{}, bounds)
			if err == nil || !strings.Contains(err.Error(), extra) || !strings.Contains(err.Error(), "not a list without a take default") {
				t.Fatalf("extra %s: error = %v", extra, err)
			}
		})
	}
}

func TestNewServerRejectsBoundBelowOne(t *testing.T) {
	for _, bound := range []int{0, -3} {
		bounds := map[string]int{}
		for key, value := range listBoundsTable {
			bounds[key] = value
		}
		bounds["Query.me"] = bound
		_, err := listBoundsServer(t, Limits{}, bounds)
		if err == nil || !strings.Contains(err.Error(), "Query.me must be at least 1") {
			t.Fatalf("bound %d: error = %v", bound, err)
		}
	}
}

func TestNewListBoundsCopiesItsInput(t *testing.T) {
	entries := map[string]int{"Query.me": 1}
	bounds := NewListBounds(entries)
	entries["Query.me"] = 99
	if got, ok := bounds.of("Query", "me"); !ok || got != 1 {
		t.Fatalf("bound after the caller mutated its map = %d/%v, want 1", got, ok)
	}
}
