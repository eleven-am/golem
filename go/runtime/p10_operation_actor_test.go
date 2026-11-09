package runtime_test

import (
	"context"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func TestCustomOperationResolverSeesTheCallersActorAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		var mu sync.Mutex
		var seen []p10operations.Actor
		p10operations.Reset(func(ctx context.Context, operation string, _ *p10operations.Caller[p10operations.Principal]) error {
			if operation == "reviewInvite" {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, golem.ActorFrom[p10operations.Actor](ctx))
			}
			return nil
		})
		invite := p10OperationID(t, 1300)
		if _, err := p10operations.Mutate(context.Background(), fixture.caller(t, "alpha"), p10operations.ReviewInvite, p10operations.AcceptArgs{ID: invite}); err != nil {
			t.Fatal(err)
		}
		response := fixture.graphql(t, "beta", `mutation { reviewInvite(id: "`+invite.String()+`", alsoEmail: false) }`)
		if len(response.Errors) != 0 {
			t.Fatalf("GraphQL errors: %#v", response.Errors)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 || seen[0].ID != "alpha" || seen[1].ID != "beta" {
			t.Fatalf("resolver actors = %#v, want alpha then beta", seen)
		}
	})
}

func TestCustomQueryResolverSeesTheCallersActorAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		var mu sync.Mutex
		var seen []p10operations.Actor
		p10operations.Reset(func(ctx context.Context, operation string, _ *p10operations.Caller[p10operations.Principal]) error {
			if operation == "searchInvites" {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, golem.ActorFrom[p10operations.Actor](ctx))
			}
			return nil
		})
		for _, id := range []string{"alpha", "beta"} {
			response := fixture.graphql(t, id, `query { searchInvites(owner: "`+id+`") { id } }`)
			if len(response.Errors) != 0 {
				t.Fatalf("GraphQL errors: %#v", response.Errors)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 || seen[0].ID != "alpha" || seen[1].ID != "beta" {
			t.Fatalf("resolver actors = %#v, want alpha then beta", seen)
		}
	})
}

func TestGeneratedQueryGivesAGoSideCustomQueryTheCallersActorAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		var mu sync.Mutex
		var seen []p10operations.Actor
		p10operations.Reset(func(ctx context.Context, operation string, _ *p10operations.Caller[p10operations.Principal]) error {
			if operation == "searchInvites" {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, golem.ActorFrom[p10operations.Actor](ctx))
			}
			return nil
		})
		for _, id := range []string{"alpha", "beta"} {
			if _, err := p10operations.Query(context.Background(), fixture.caller(t, id), p10operations.SearchInvites, p10operations.SearchArgs{Owner: id}); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 || seen[0].ID != "alpha" || seen[1].ID != "beta" {
			t.Fatalf("resolver actors = %#v, want alpha then beta", seen)
		}
	})
}
