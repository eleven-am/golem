package runtime_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p5extensions"
)

func assertListItemsLimit(t *testing.T, failure p10CustomError, code string, root string, max string) {
	t.Helper()
	if failure.Extensions["code"] != code || failure.Extensions["reason"] != "LIMIT_EXCEEDED" || failure.Extensions["limit"] != "list_items" || fmt.Sprint(failure.Extensions["max"]) != max {
		t.Fatalf("list limit error = %#v", failure)
	}
	if !reflect.DeepEqual(failure.Path, []any{root}) {
		t.Fatalf("path = %#v, want [%s]", failure.Path, root)
	}
}

func TestCustomListLongerThanDeclaredMaximumFails(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		for id := 1; id <= 3; id++ {
			fixture.seedOwned(t, 700+id, fixture.alpha, "alpha", "pending")
		}
		response := fixture.run(t, "alpha", `query { cappedInvites(owner: "alpha") { id } }`)
		if response.Data != nil {
			t.Fatalf("a list above its maximum must not be returned, even shortened: %#v", response.Data)
		}
		failure := response.onlyError(t)
		assertListItemsLimit(t, failure, "LIMIT_EXCEEDED", "cappedInvites", "2")
		if failure.Message != "custom query cappedInvites returned more than 2 items" {
			t.Fatalf("message = %q", failure.Message)
		}
		if reports := fixture.reports.take(); len(reports) != 1 {
			t.Fatalf("an application defect must be reported exactly once, got %v", reports)
		}
		if fixture.readCount() != 1 {
			t.Fatalf("an over-maximum list must be refused before hydration: want only the resolver's own read, got %d reads", fixture.readCount())
		}
	})
}

func TestCustomListAtItsDeclaredMaximumIsReturnedWhole(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 711, fixture.alpha, "alpha", "pending")
		fixture.seedOwned(t, 712, fixture.alpha, "alpha", "pending")
		response := fixture.run(t, "alpha", `query { cappedInvites(owner: "alpha") { id } }`)
		if len(response.Errors) != 0 || len(response.items(t, "cappedInvites")) != 2 {
			t.Fatalf("response = %#v", response)
		}
		if reports := fixture.reports.take(); len(reports) != 0 {
			t.Fatalf("a list within its maximum reported %v", reports)
		}
	})
}

func TestMaximumOfOneRootDoesNotLimitAnother(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		for id := 1; id <= 3; id++ {
			fixture.seedOwned(t, 720+id, fixture.alpha, "alpha", "pending")
		}
		response := fixture.run(t, "alpha", `query { searchInvites(owner: "alpha") { id } }`)
		if len(response.Errors) != 0 || len(response.items(t, "searchInvites")) != 3 {
			t.Fatalf("response = %#v", response)
		}
	})
}

func TestNestedCustomListInnerLevelIsBoundedToo(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		for id := 1; id <= 3; id++ {
			fixture.seedOwned(t, 730+id, fixture.alpha, "alpha", "pending")
		}
		response := fixture.run(t, "alpha", `query { groupedInvites(owner: "alpha") { id } }`)
		if len(response.Errors) != 0 {
			t.Fatalf("groups of at most two within declared maxima (10, 2) failed: %#v", response.Errors)
		}
		groups, _ := response.Data["groupedInvites"].([]any)
		if len(groups) != 2 {
			t.Fatalf("groups = %#v", response.Data["groupedInvites"])
		}
	})
}

func TestCommittedCustomMutationResultOverMaximumIsNeverShortened(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		first, second, third := p10OperationID(t, 741), p10OperationID(t, 742), p10OperationID(t, 743)
		response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitCappedInvites(first: "%s", second: "%s", third: "%s", teamID: "%s") { id } }`, first, second, third, fixture.alpha))
		if response.Data != nil {
			t.Fatalf("a shortened or partial list was returned: %#v", response.Data)
		}
		failure := response.onlyError(t)
		assertListItemsLimit(t, failure, "COMMITTED_RESULT_UNAVAILABLE", "submitCappedInvites", "2")
		if failure.Extensions["committed"] != true {
			t.Fatalf("a committed mutation must say so: %#v", failure)
		}
		if failure.Message != "custom mutation submitCappedInvites committed, but its result is unavailable" {
			t.Fatalf("message = %q", failure.Message)
		}
		for _, id := range []golem.UUID{first, second, third} {
			if exists, _, _ := fixture.inviteExists(t, id); !exists {
				t.Fatalf("invite %s was not committed", id)
			}
		}
		if reports := fixture.reports.take(); len(reports) != 1 {
			t.Fatalf("the defect must be reported exactly once, got %v", reports)
		}
	})
}

func TestComputedListLongerThanDeclaredMaximumFails(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			var mu sync.Mutex
			var reported []error
			server, database, _ := newP5ExtensionReportingFixture(t, profile, func(err error) {
				mu.Lock()
				defer mu.Unlock()
				reported = append(reported, err)
			})
			seedP5ExtensionUsers(t, database, profile.provider)
			for index, counter := range []int{3, 4, 0} {
				id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
				if _, err := database.ExecContext(context.Background(), database.Rebind(`UPDATE `+p5ExtensionUsersTable(profile.provider)+` SET "counter" = ? WHERE "id" = ?`), counter, id); err != nil {
					t.Fatal(err)
				}
			}
			principal := p5extensions.Principal{ID: "alpha", Valid: true}
			query := `query Steps($id: UUID!) { user(where: {ID: $id}) { id counter counterSteps } }`

			atMaximum := executeP5ExtensionGraphQL(t, server, principal, query, map[string]any{"id": "00000000-0000-0000-0000-000000000001"})
			user := p5ExtensionObject(t, atMaximum.Data["user"])
			if len(atMaximum.Errors) != 0 || fmt.Sprint(user["counterSteps"]) != "[1 2 3]" {
				t.Fatalf("a computed list of exactly its maximum must be returned whole: %#v %#v", atMaximum.Data, atMaximum.Errors)
			}
			empty := executeP5ExtensionGraphQL(t, server, principal, query, map[string]any{"id": "00000000-0000-0000-0000-000000000003"})
			if len(empty.Errors) != 0 || fmt.Sprint(p5ExtensionObject(t, empty.Data["user"])["counterSteps"]) != "[]" {
				t.Fatalf("an empty computed list: %#v %#v", empty.Data, empty.Errors)
			}
			mu.Lock()
			clean := len(reported)
			mu.Unlock()
			if clean != 0 {
				t.Fatalf("lists within the maximum reported %v", reported)
			}

			over := executeP5ExtensionGraphQL(t, server, principal, query, map[string]any{"id": "00000000-0000-0000-0000-000000000002"})
			if len(over.Errors) != 1 {
				t.Fatalf("errors = %#v", over.Errors)
			}
			failure := over.Errors[0]
			if failure.Extensions["code"] != "LIMIT_EXCEEDED" || failure.Extensions["reason"] != "LIMIT_EXCEEDED" || failure.Extensions["limit"] != "list_items" || fmt.Sprint(failure.Extensions["max"]) != "3" {
				t.Fatalf("extensions = %#v", failure.Extensions)
			}
			if failure.Message != "computed field User.counterSteps returned more than 3 items" || fmt.Sprint(failure.Path) != "[user counterSteps]" {
				t.Fatalf("message=%q path=%v", failure.Message, failure.Path)
			}
			overUser := p5ExtensionObject(t, over.Data["user"])
			if value, present := overUser["counterSteps"]; !present || value != nil || overUser["id"] != "00000000-0000-0000-0000-000000000002" {
				t.Fatalf("the list must be null, never shortened, and its siblings intact: %#v", overUser)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(reported) != 1 {
				t.Fatalf("an application defect must be reported exactly once, got %v", reported)
			}
		})
	}
}
