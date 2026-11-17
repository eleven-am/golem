package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestGraphQLReadMasksPositionsOccurrencesAndSQLAcrossProviders(t *testing.T) {
	for _, profile := range p5ReadProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			harness := newP5ReadProviderHarness(t, profile)
			harness.seed(t)
			t.Run("conditional-masks-stay-local", func(t *testing.T) {
				assertP5ConditionalMasksLocal(t, harness)
			})
			t.Run("positions-aliases-fragments-and-directives", func(t *testing.T) {
				assertP5ReadPositionsAndOccurrences(t, harness)
			})
			t.Run("refused-inputs-issue-zero-sql", func(t *testing.T) {
				assertP5ReadRefusalsIssueZeroSQL(t, harness)
			})
			t.Run("hidden-and-missing-unique-match", func(t *testing.T) {
				assertP5ReadHiddenMissing(t, harness)
			})
		})
	}
}

func assertP5ConditionalMasksLocal(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	h.captured = nil
	response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query {
  users(orderBy: [{id: asc}], take: 3) {
    id name
    posts(orderBy: [{id: asc}], take: 4) { id title author { id name } }
    _count { posts }
  }
}`})
	if len(response.Errors) != 0 {
		t.Fatalf("conditional-mask GraphQL errors=%#v", response.Errors)
	}
	want := []any{
		map[string]any{"id": mutationResultUUIDText(1), "name": "alpha-open", "posts": []any{
			map[string]any{"id": mutationResultUUIDText(11), "title": "one-open", "author": map[string]any{"id": mutationResultUUIDText(1), "name": "alpha-open"}},
			map[string]any{"id": mutationResultUUIDText(12), "title": "two-closed", "author": nil},
		}, "_count": map[string]any{"posts": int32(2)}},
		map[string]any{"id": mutationResultUUIDText(2), "name": nil, "posts": nil, "_count": map[string]any{"posts": nil}},
		map[string]any{"id": mutationResultUUIDText(3), "name": "gamma-open", "posts": []any{
			map[string]any{"id": mutationResultUUIDText(14), "title": "four-closed", "author": nil},
		}, "_count": map[string]any{"posts": int32(1)}},
	}
	got := response.Data.(map[string]any)["users"]
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("conditional masks escaped occurrence\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
	if len(h.trace.snapshot()) == 0 {
		t.Fatal("accepted conditional-mask query issued no provider SQL")
	}
}

func assertP5ReadPositionsAndOccurrences(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	h.captured = nil
	response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query Occurrences($showName: Boolean!) {
  user(where: { id: "00000000-0000-0000-0000-000000000001" }) {
    id name
    ...UserNameAgain @include(if: $showName)
    openPosts: posts(where: { title: { endsWith: "-open" } }, orderBy: [{title: asc}], take: 1) { ...PostBits }
    closedPosts: posts(where: { title: { contains: "closed" } }, orderBy: [{title: desc}], take: -1) { id title }
    _count {
      opens: posts(where: { title: { endsWith: "-open" } })
      closed: posts(where: { title: { contains: "closed" } })
    }
  }
}
fragment UserNameAgain on User { name }
fragment PostBits on Post { id title }
`, Variables: map[string]any{"showName": true}})
	if len(response.Errors) != 0 {
		t.Fatalf("occurrence GraphQL errors=%#v", response.Errors)
	}
	user := response.Data.(map[string]any)["user"].(map[string]any)
	if len(user["openPosts"].([]any)) != 1 || user["openPosts"].([]any)[0].(map[string]any)["title"] != "one-open" || len(user["closedPosts"].([]any)) != 1 || user["closedPosts"].([]any)[0].(map[string]any)["title"] != "two-closed" {
		t.Fatalf("relation aliases collapsed=%#v", user)
	}
	counts := user["_count"].(map[string]any)
	if counts["opens"] != int32(1) || counts["closed"] != int32(1) {
		t.Fatalf("count aliases collapsed=%#v", counts)
	}
	if len(h.captured) != 1 {
		t.Fatalf("occurrence root requests=%d want=1", len(h.captured))
	}
	seenRelations, seenCounts := 0, 0
	occurrences := map[golem.RuntimeOccurrenceID]bool{}
	for _, selection := range h.captured[0].Selection() {
		if !selection.IsRelation() && !selection.IsRelationCount() {
			continue
		}
		if occurrences[selection.OccurrenceID()] || selection.OccurrenceID() == 0 {
			t.Fatalf("relation/count occurrence identity collapsed: %#v", selection)
		}
		occurrences[selection.OccurrenceID()] = true
		if selection.IsRelation() {
			seenRelations++
		} else {
			seenCounts++
		}
	}
	if seenRelations != 2 || seenCounts != 2 {
		t.Fatalf("relation/count occurrence inventory relations=%d counts=%d", seenRelations, seenCounts)
	}

	h.trace.reset()
	h.captured = nil
	positionResponse := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query Positions {
  window: users(
    where: { AND: [{ name: { endsWith: "-open" } }, { posts: { some: { title: { startsWith: "one" } } } }] }
    orderBy: [{name: asc}, {id: desc}]
    cursor: { id: "00000000-0000-0000-0000-000000000001" }
    distinct: [name]
    skip: 0
    take: 2
  ) { id }
  reverse: users(orderBy: [{id: asc}], cursor: { id: "00000000-0000-0000-0000-000000000003" }, take: -1) { id }
  defaulted: users(orderBy: [{id: asc}]) { id }
}`})
	if len(positionResponse.Errors) != 0 {
		t.Fatalf("position GraphQL errors=%#v", positionResponse.Errors)
	}
	if len(h.captured) != 3 {
		t.Fatalf("position requests=%d want=3", len(h.captured))
	}
	window := h.captured[0]
	_, where := window.Where()
	_, cursor := window.Cursor()
	take, hasTake := window.Take()
	skip, hasSkip := window.Skip()
	if !where || len(window.OrderBy()) != 2 || !cursor || len(window.Distinct()) != 1 || !hasTake || take != 2 || !hasSkip || skip != 0 {
		t.Fatalf("root query positions not preserved: %#v", window)
	}
	reverseTake, reversePresent := h.captured[1].Take()
	defaultTake, defaultPresent := h.captured[2].Take()
	if !reversePresent || reverseTake != -1 || !defaultPresent || defaultTake != 50 {
		t.Fatalf("signed/default paging reverse=%d/%t default=%d/%t", reverseTake, reversePresent, defaultTake, defaultPresent)
	}
	statements := h.trace.snapshot()
	if len(statements) == 0 || !p5TraceContainsTable(statements, "users") || !p5TraceContainsTable(statements, "posts") {
		t.Fatalf("accepted query positions did not reach provider SQL: %#v", statements)
	}
}

func assertP5ReadRefusalsIssueZeroSQL(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	for _, test := range []struct {
		name, query, code string
		begins            int64
	}{
		{name: "ambiguous-where", query: `{ users(where: { all: true, name: { equals: "forged" } }) { id } }`, code: "BAD_USER_INPUT"},
		{name: "unknown-input", query: `{ users(where: { forged: { equals: "x" } }) { id } }`, code: "GRAPHQL_VALIDATION_FAILED"},
		{name: "conditional-order", query: `{ users(orderBy: [{name: asc}]) { id } }`, code: "FORBIDDEN", begins: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h.trace.reset()
			h.captured = nil
			before := h.begins.Load()
			response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: test.query})
			if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != test.code {
				t.Fatalf("refusal response=%#v want code=%s", response, test.code)
			}
			if delta := h.begins.Load() - before; delta != test.begins {
				t.Fatalf("refusal caller executions=%d want=%d", delta, test.begins)
			}
			if statements := h.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("refused query issued provider SQL: %#v", statements)
			}
		})
	}
}

func assertP5ReadHiddenMissing(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	query := `query($id: UUID!) { post(where: {id: $id}) { id title } }`
	invisible := h.disclosureServer.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": mutationResultUUIDText(12)}})
	missing := h.disclosureServer.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": mutationResultUUIDText(99)}})
	if !reflect.DeepEqual(invisible, missing) || len(invisible.Errors) != 0 || !reflect.DeepEqual(invisible.Data, map[string]any{"post": nil}) {
		t.Fatalf("hidden/missing disclosure differs invisible=%#v missing=%#v", invisible, missing)
	}
	if len(h.trace.snapshot()) == 0 {
		t.Fatal("hidden/missing unique checks issued no provider SQL")
	}
}

func p5TraceContainsTable(statements []string, table string) bool {
	needle := `"` + table + `"`
	for _, statement := range statements {
		if strings.Contains(statement, needle) {
			return true
		}
	}
	return false
}
