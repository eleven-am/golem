package runtime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestGraphQLEmptyRelationPredicatesUseReadableExistenceAcrossProviders(t *testing.T) {
	for _, profile := range p5ReadProviderProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			h := newP5ReadProviderHarness(t, profile)
			h.seed(t)
			query := h.app.database.Rebind(`INSERT INTO ` + nestedAcceptanceTable(h.app, h.fixture.User) + `("id","name") VALUES (?,?)`)
			if _, err := h.app.database.ExecContext(context.Background(), query, mutationResultUUIDText(4), "delta-open"); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, quantifier, prefix string
				want                     []int
			}{
				{"some", "some", "", []int{1, 3}},
				{"none", "none", "", []int{4}},
				{"every", "every", "", []int{1, 3, 4}},
				{"some-hidden-targets", "some", "one-", []int{1}},
				{"none-hidden-targets", "none", "one-", []int{3, 4}},
				{"every-hidden-targets", "every", "one-", []int{1, 3, 4}},
			} {
				t.Run(test.name, func(t *testing.T) {
					// The source predicate proves that its relation field is
					// readable. The target policy independently hides other posts.
					query := fmt.Sprintf(`{ users(where: {AND: [{name: {endsWith: "-open"}}, {posts: {%s: {}}}]}, orderBy: [{id: asc}]) { id } }`, test.quantifier)
					response := h.server.Execute(context.Background(), oraclePrincipal{PostPrefix: test.prefix}, publicgraphql.Request{Query: query})
					if len(response.Errors) != 0 {
						t.Fatalf("empty predicate refused: %#v", response.Errors)
					}
					want := make([]any, 0, len(test.want))
					for _, id := range test.want {
						want = append(want, map[string]any{"id": mutationResultUUIDText(byte(id))})
					}
					if got := response.Data.(map[string]any)["users"]; !reflect.DeepEqual(got, want) {
						t.Fatalf("rows=%#v want=%#v", got, want)
					}
				})
			}
			for _, where := range []string{`{}`, `{AND: [{}]}`, `{posts: {}}`, `{posts: {some: {AND: [{}]}}}`} {
				h.trace.reset()
				response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `{ users(where: ` + where + `) { id } }`})
				if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "BAD_USER_INPUT" || len(h.trace.snapshot()) != 0 {
					t.Fatalf("empty root/logical/envelope guard changed: %s => %#v", where, response)
				}
			}
		})
	}
}

func TestGraphQLOrderByObjectGivesUsableSchemaExample(t *testing.T) {
	h := newP5ReadProviderHarness(t, p5ReadProviderProfile{name: "sqlite", provider: golem.SQLite})
	h.seed(t)
	h.trace.reset()
	response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `{ users(orderBy: {id: asc}) { id } }`})
	if len(response.Errors) != 1 || response.Data != nil || h.begins.Load() != 0 || len(h.trace.snapshot()) != 0 {
		t.Fatalf("invalid order reached execution: %#v", response)
	}
	failure := response.Errors[0]
	want := map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "orderBy", "expectedType": "[UserOrderByInput!]", "example": "[{id: asc}]"}
	if !reflect.DeepEqual(failure.Extensions, want) || !strings.Contains(failure.Message, "orderBy") || !strings.Contains(failure.Message, "[UserOrderByInput!]") || !strings.Contains(failure.Message, "[{id: asc}]") {
		t.Fatalf("order diagnostic=%#v", failure)
	}
	// The suggested syntax must actually compile and execute.
	accepted := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `{ users(orderBy: ` + failure.Extensions["example"].(string) + `) { id } }`})
	if len(accepted.Errors) != 0 || len(accepted.Data.(map[string]any)["users"].([]any)) != 3 {
		t.Fatalf("suggested order syntax failed: %#v", accepted)
	}
	t.Run("omitted-skip-variable-is-an-omitted-argument", func(t *testing.T) {
		h.captured = nil
		h.trace.reset()
		h.begins.Store(0)
		response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{
			Query: `query($k: Int) { users(skip: $k, orderBy: [{id: asc}]) { id } }`,
		})
		if len(response.Errors) != 0 || !reflect.DeepEqual(response.Data, accepted.Data) {
			t.Fatalf("omitted skip did not preserve unpaged results: %#v", response)
		}
		if h.begins.Load() != 1 || len(h.captured) != 1 || len(h.trace.snapshot()) == 0 {
			t.Fatalf("omitted skip did not execute one real read: begins=%d captured=%d", h.begins.Load(), len(h.captured))
		}
		if _, present := h.captured[0].Skip(); present {
			t.Fatal("undefined variable was materialized into a skip argument")
		}
	})
}
