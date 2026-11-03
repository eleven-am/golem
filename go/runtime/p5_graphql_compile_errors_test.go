package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	"github.com/eleven-am/golem/go/internal/testenv"
)

const compileErrorMarker = "private-driver-token"

var compileErrorLimits = publicgraphql.Limits{MaxInputDepth: 4, MaxInputNodes: 6, MaxListItems: 3, MaxDepth: 3, MaxSelectedFields: 4, MaxAliases: 2}

type compileErrorCase struct {
	name       string
	query      string
	variables  map[string]any
	message    string
	path       []any
	extensions map[string]any
}

func compileErrorCases() []compileErrorCase {
	listVariable := `query($w: UserWhereInput) { users(where: $w) { id } }`
	return []compileErrorCase{
		{name: "take-over-maximum", query: `{ users(take: 1000) { id } }`, message: "Argument take on User must be between -500 and 500, excluding 0", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take", "min": -500, "max": 500}},
		{name: "take-zero", query: `{ users(take: 0) { id } }`, message: "Argument take on User must not be 0", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take"}},
		{name: "take-negative-without-order-by", query: `{ users(take: -1) { id } }`, message: "Argument take on User is negative and requires orderBy", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "take"}},
		{name: "child-take-zero", query: `{ users(take: 1) { posts(take: 0) { id } } }`, message: "Argument take on Post must not be 0", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take"}},
		{name: "child-take-negative-without-order-by", query: `{ users(take: 1) { posts(take: -1) { id } } }`, message: "Argument take on Post is negative and requires orderBy", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "take"}},
		{name: "skip-negative", query: `{ users(skip: -1) { id } }`, message: "Argument skip on User must be at least 0", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "skip"}},
		{name: "skip-variable-omitted", query: `query($k: Int) { users(skip: $k) { id } }`, message: "Argument skip on User cannot be null", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "skip"}},
		{name: "where-null", query: `{ users(where: null) { id } }`, message: "Argument where on User cannot be null", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where"}},
		{name: "cursor-null", query: `{ users(cursor: null) { id } }`, message: "Argument cursor on User cannot be null", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "cursor"}},
		{name: "uuid-literal-with-marker", query: `{ users(where: {id: {equals: "` + compileErrorMarker + `"}}) { id } }`, message: "Input where.id.equals on User is not a valid UUID", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "where.id.equals"}},
		{name: "uuid-variable-with-marker", query: `query($u: UUID!) { users(where: {id: {equals: $u}}) { id } }`, variables: map[string]any{"u": compileErrorMarker},
			message: "Input where.id.equals on User is not a valid UUID", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "where.id.equals"}},
		{name: "uuid-not-canonical", query: `{ users(where: {id: {equals: "00000000-0000-0000-0000-00000000000A"}}) { id } }`, message: "Input where.id.equals on User is not a canonical UUID", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "where.id.equals"}},
		{name: "string-with-nul", query: listVariable, variables: map[string]any{"w": map[string]any{"name": map[string]any{"equals": "a\x00b"}}},
			message: "Input where.name.equals on User must not contain NUL characters", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "where.name.equals"}},
		{name: "null-field-condition-in-list", query: listVariable, variables: map[string]any{"w": map[string]any{"NOT": []any{map[string]any{"name": map[string]any{"equals": "a"}}, map[string]any{"name": nil}}}},
			message: "Input where.NOT[1].name on User cannot be null", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.NOT[1].name"}},
		{name: "order-by-duplicate", query: `{ users(orderBy: [{id: asc}, {id: desc}]) { id } }`, message: "Argument orderBy on User names a field more than once", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "orderBy"}},
		{name: "order-by-empty", query: `{ users(orderBy: []) { id } }`, message: "Argument orderBy on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "orderBy"}},
		{name: "order-by-two-members", query: `{ users(orderBy: [{id: asc, name: asc}]) { id } }`, message: "Input orderBy[0] on User must set exactly one member", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "orderBy[0]"}},
		{name: "where-empty", query: `{ users(where: {}) { id } }`, message: "Argument where on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where"}},
		{name: "and-empty", query: `{ users(where: {AND: []}) { id } }`, message: "Input where.AND on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.AND"}},
		{name: "field-condition-empty", query: `{ users(where: {name: {}}) { id } }`, message: "Input where.name on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.name"}},
		{name: "all-with-sibling", query: `{ users(where: {all: true, name: {equals: "x"}}) { id } }`, message: "Input where.all on User must be the only member and Boolean", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.all"}},
		{name: "distinct-duplicate", query: `{ users(distinct: [id, id]) { id } }`, message: "Argument distinct on User names a field more than once", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "distinct"}},
		{name: "distinct-empty", query: `{ users(distinct: []) { id } }`, message: "Argument distinct on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "distinct"}},
		{name: "relation-filter-empty", query: `{ users(where: {friendshipsFrom: {some: {}}}) { id } }`, message: "Input where.friendshipsFrom.some on User must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.friendshipsFrom.some"}},
		{name: "relation-filter-null", query: `{ users(where: {posts: {some: null}}) { id } }`, message: "Input where.posts.some on User cannot be null", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where.posts.some"}},
		{name: "relation-count-where-empty", query: `{ users { _count { posts(where: {}) } } }`, message: "Argument where on Post must not be empty", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "argument": "where"}},
		{name: "input-list-items", query: `{ users(where: {OR: [{all: true}, {all: true}, {all: true}, {all: true}]}) { id } }`, message: "Input exceeds MaxListItems (3)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxListItems", "max": 3}},
		{name: "input-list-items-in-operand", query: listVariable, variables: map[string]any{"w": map[string]any{"name": map[string]any{"in": []any{"a", "b", "c", "d"}}}},
			message: "Input exceeds MaxListItems (3)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxListItems", "max": 3}},
		{name: "input-depth", query: `{ users(where: {OR: [{OR: [{OR: [{OR: [{all: true}]}]}]}]}) { id } }`, message: "Input exceeds MaxInputDepth (4)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxInputDepth", "max": 4}},
		{name: "input-nodes", query: `{ users(where: {AND: [{name: {equals: "a"}}, {name: {equals: "b"}}, {name: {equals: "c"}}], OR: [{name: {equals: "a"}}, {name: {equals: "b"}}, {name: {equals: "c"}}]}) { id } }`,
			message: "Input exceeds MaxInputNodes (6)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxInputNodes", "max": 6}},
		{name: "selection-depth", query: `{ users(take: 1) { posts(take: 1) { comments(take: 1) { replies(take: 1) { id } } } } }`, message: "Selection exceeds MaxDepth (3)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxDepth", "max": 3}},
		{name: "selection-fields", query: `{ users { id name posts { id } comments { id } } }`, message: "Selection exceeds MaxSelectedFields (4)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxSelectedFields", "max": 4}},
		{name: "selection-aliases", query: `{ users { x: id y: name z: id } }`, message: "Selection exceeds MaxAliases (2)", path: []any{"users"},
			extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "LIMIT_EXCEEDED", "limit": "MaxAliases", "max": 2}},
		{name: "every-root-skipped", query: `query($skip: Boolean!) { users @skip(if: $skip) { id } }`, variables: map[string]any{"skip": true}, message: "Selection must not be empty",
			extensions: map[string]any{"code": "BAD_USER_INPUT"}},
	}
}

func TestGraphQLReadCompileErrorsAreTypedAndPrecise(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertCompileErrorCases(t, newSocialMutationFixture(t, golem.ModelID{}, nil))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			assertCompileErrorCases(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func assertCompileErrorCases(t *testing.T, fixture socialMutationFixture) {
	t.Helper()
	var begins atomic.Int64
	harness := newP5SocialGraphQLHarnessWith(t, fixture, compileErrorLimits, func() { begins.Add(1) }, nil)
	for _, test := range compileErrorCases() {
		t.Run(test.name, func(t *testing.T) {
			response := harness.server.Execute(context.Background(), graphMutationPrincipal{}, publicgraphql.Request{Query: test.query, Variables: test.variables})
			if response.Data != nil || len(response.Errors) != 1 {
				t.Fatalf("response=%#v want exactly one error and no data", response)
			}
			failure := response.Errors[0]
			if failure.Message != test.message {
				t.Fatalf("message=%q want %q", failure.Message, test.message)
			}
			if !reflect.DeepEqual(failure.Path, test.path) {
				t.Fatalf("path=%#v want %#v", failure.Path, test.path)
			}
			if !reflect.DeepEqual(failure.Extensions, test.extensions) {
				t.Fatalf("extensions=%#v want %#v", failure.Extensions, test.extensions)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), compileErrorMarker) {
				t.Fatalf("response echoes caller text: %s", encoded)
			}
		})
	}
	if count := begins.Load(); count != 0 {
		t.Fatalf("compile refusals began %d caller executions; no SQL may run", count)
	}
}
