package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/eleven-am/golem/go/runtime/testdata/p5extensions"
)

type p10CustomError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions"`
}

type p10CustomResponse struct {
	Data   map[string]any   `json:"data"`
	Errors []p10CustomError `json:"errors"`
}

func (fixture *p10OperationFixture) run(t *testing.T, principal, query string) p10CustomResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), p10OperationPrincipalKey{}, p10operations.Principal{ID: principal, Valid: true})
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, request)
	var response p10CustomResponse
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode GraphQL status=%d: %v", recorder.Code, err)
	}
	return response
}

func (fixture *p10OperationFixture) readCount() int {
	count := 0
	for _, record := range fixture.observer.snapshot(observe.Kind("")) {
		if record.kind == observe.KindRead {
			count++
		}
	}
	return count
}

func (fixture *p10OperationFixture) seedOwned(t *testing.T, id int, team golem.UUID, owner, status string) golem.UUID {
	t.Helper()
	key := p10OperationID(t, id)
	if _, err := fixture.app.System().Invites.Create(context.Background(), p10operations.Invites.Create(
		p10operations.Invites.ID.Create(key), p10operations.Invites.TeamID.Create(team),
		p10operations.Invites.Owner.Create(owner), p10operations.Invites.Email.Create(fmt.Sprintf("seed-%d@example.test", id)),
		p10operations.Invites.Status.Create(status),
	)); err != nil {
		t.Fatal(err)
	}
	return key
}

func (fixture *p10OperationFixture) systemInvites(owners ...string) p10operations.InviteSource {
	return func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
		options := []golem.ReadOption[p10operations.Invite]{
			p10operations.Invites.OrderBy(p10operations.Invites.ID.Asc()),
			p10operations.Invites.Select(p10operations.Invites.ID, p10operations.Invites.Status, p10operations.Invites.Email),
		}
		if len(owners) != 0 {
			var predicate golem.Predicate[p10operations.Invite]
			for index, owner := range owners {
				match := p10operations.Invites.Owner.Eq(owner)
				if index == 0 {
					predicate = match
				} else {
					predicate = predicate.Or(match)
				}
			}
			options = append(options, p10operations.Invites.Where(predicate))
		}
		return fixture.app.System().Invites.FindMany(ctx, options...)
	}
}

func (response p10CustomResponse) items(t *testing.T, root string) []map[string]any {
	t.Helper()
	raw, ok := response.Data[root].([]any)
	if !ok {
		t.Fatalf("%s is not a list: %#v (errors=%#v)", root, response.Data[root], response.Errors)
	}
	result := make([]map[string]any, len(raw))
	for index, item := range raw {
		result[index], _ = item.(map[string]any)
	}
	return result
}

func (response p10CustomResponse) onlyError(t *testing.T) p10CustomError {
	t.Helper()
	if len(response.Errors) != 1 {
		t.Fatalf("want exactly one error, got %#v (data=%#v)", response.Errors, response.Data)
	}
	return response.Errors[0]
}

func TestCustomQueryNestedRelationResolvesThroughCallerPolicy(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 11, fixture.alpha, "alpha", "pending")
		fixture.seedOwned(t, 12, fixture.alpha, "alpha", "pending")
		selection := `{ id email status team { owner invites { id } } }`
		response := fixture.run(t, "alpha", `{ custom: searchInvites(owner: "alpha") `+selection+` ordinary: invites(where: {owner: {equals: "alpha"}}, orderBy: [{id: asc}]) `+selection+` }`)
		if len(response.Errors) != 0 {
			t.Fatalf("custom nested selection failed: %#v", response.Errors)
		}
		if len(response.items(t, "custom")) != 2 || !reflect.DeepEqual(response.Data["custom"], response.Data["ordinary"]) {
			t.Fatalf("custom result differs from the generated root\ncustom=%#v\nordinary=%#v", response.Data["custom"], response.Data["ordinary"])
		}
		counts := `{ id owner invites { id } _count { invites } }`
		response = fixture.run(t, "alpha", `{ custom: searchTeams(owner: "alpha") `+counts+` ordinary: teams(where: {owner: {equals: "alpha"}}, orderBy: [{id: asc}]) `+counts+` }`)
		if len(response.Errors) != 0 {
			t.Fatalf("custom relation count failed: %#v", response.Errors)
		}
		teams := response.items(t, "custom")
		if len(teams) != 1 || !reflect.DeepEqual(response.Data["custom"], response.Data["ordinary"]) {
			t.Fatalf("custom team result differs from the generated root\ncustom=%#v\nordinary=%#v", response.Data["custom"], response.Data["ordinary"])
		}
		if count, _ := teams[0]["_count"].(map[string]any); fmt.Sprint(count["invites"]) != "2" {
			t.Fatalf("relation count = %#v", teams[0]["_count"])
		}
	})
}

func TestCustomQueryNestedRelationRespectsNestedPolicy(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 21, fixture.alpha, "alpha", "pending")
		fixture.seedOwned(t, 22, fixture.beta, "alpha", "pending")
		selection := `{ id team { owner } }`
		response := fixture.run(t, "alpha", `{ custom: searchInvites(owner: "alpha") `+selection+` ordinary: invites(where: {owner: {equals: "alpha"}}, orderBy: [{id: asc}]) `+selection+` }`)
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		custom := response.items(t, "custom")
		if len(custom) != 2 || custom[0]["team"] == nil || custom[1]["team"] != nil {
			t.Fatalf("another owner's team was disclosed or dropped: %#v", custom)
		}
		if !reflect.DeepEqual(response.Data["custom"], response.Data["ordinary"]) {
			t.Fatalf("custom differs from generated\ncustom=%#v\nordinary=%#v", response.Data["custom"], response.Data["ordinary"])
		}
	})
}

func TestCustomQueryScalarOnlySelectionIsStillCallerRead(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 31, fixture.alpha, "alpha", "pending")
		fixture.seedOwned(t, 32, fixture.beta, "beta", "pending")
		p10operations.SetInviteSource(fixture.systemInvites())
		fixture.observer.reset()
		response := fixture.run(t, "alpha", `{ searchInvites(owner: "ignored") { id status } }`)
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		items := response.items(t, "searchInvites")
		if len(items) != 1 || items[0]["id"] != p10OperationID(t, 31).String() {
			t.Fatalf("the caller saw a row it cannot read, or lost one it can: %#v", items)
		}
		if reads := fixture.readCount(); reads != 2 {
			t.Fatalf("reads = %d, want the resolver's read plus exactly one hydration read", reads)
		}
	})
}

func TestCustomResultFieldsAreCallerMasked(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 41, fixture.alpha, "alpha", "hidden-email")
		fixture.seedOwned(t, 42, fixture.alpha, "alpha", "pending")
		p10operations.SetInviteSource(fixture.systemInvites("alpha"))
		response := fixture.run(t, "alpha", `{ searchInvites(owner: "ignored") { id status email } }`)
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		items := response.items(t, "searchInvites")
		if len(items) != 2 || items[0]["email"] != nil || items[1]["email"] != "seed-42@example.test" {
			t.Fatalf("a field masked for the caller was disclosed from the resolver's System read: %#v", items)
		}
	})
}

func TestCustomQueryHydrationPreservesOrderAndDuplicates(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		a := fixture.seedOwned(t, 51, fixture.alpha, "alpha", "pending")
		b := fixture.seedOwned(t, 52, fixture.alpha, "alpha", "pending")
		p10operations.SetInviteSource(func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
			rows, err := fixture.systemInvites("alpha")(ctx, "")
			if err != nil || len(rows) != 2 {
				return nil, errors.Join(err, errors.New("seed rows are absent"))
			}
			return []golem.Row[p10operations.Invite]{rows[1], rows[0], rows[1]}, nil
		})
		response := fixture.run(t, "alpha", `{ searchInvites(owner: "x") { id } groupedInvites(owner: "x") { id } }`)
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		var ids []any
		for _, item := range response.items(t, "searchInvites") {
			ids = append(ids, item["id"])
		}
		if !reflect.DeepEqual(ids, []any{b.String(), a.String(), b.String()}) {
			t.Fatalf("resolver order or duplicate lost: %v", ids)
		}
		groups, _ := response.Data["groupedInvites"].([]any)
		if len(groups) != 2 || len(groups[0].([]any)) != 2 || len(groups[1].([]any)) != 1 || groups[1].([]any)[0].(map[string]any)["id"] != b.String() {
			t.Fatalf("nested list shape lost: %#v", groups)
		}
	})
}

func TestCustomQueryHydrationOmitsRowsDeletedBeforeHydration(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedOwned(t, 61, fixture.alpha, "alpha", "pending")
		doomed := fixture.seedOwned(t, 62, fixture.alpha, "alpha", "pending")
		deleting := func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
			rows, err := fixture.systemInvites("alpha")(ctx, "")
			if err != nil {
				return nil, err
			}
			if _, err := fixture.app.System().Invites.Delete(ctx, p10operations.Invites.ByID.Value(doomed)); err != nil {
				return nil, err
			}
			return rows, nil
		}
		p10operations.SetInviteSource(deleting)
		response := fixture.run(t, "alpha", `{ searchInvites(owner: "x") { id } }`)
		if items := response.items(t, "searchInvites"); len(response.Errors) != 0 || len(items) != 1 || items[0]["id"] != p10OperationID(t, 61).String() {
			t.Fatalf("a vanished row was not omitted: %#v errors=%#v", items, response.Errors)
		}

		fixture.seedOwned(t, 62, fixture.alpha, "alpha", "pending")
		single := func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
			rows, err := fixture.systemInvites("alpha")(ctx, "")
			if err != nil {
				return nil, err
			}
			if _, err := fixture.app.System().Invites.Delete(ctx, p10operations.Invites.ByID.Value(p10OperationID(t, 61))); err != nil {
				return nil, err
			}
			return rows[:1], nil
		}
		p10operations.SetInviteSource(single)
		response = fixture.run(t, "alpha", `{ firstInvite(owner: "x") { id } }`)
		if len(response.Errors) != 0 || response.Data["firstInvite"] != nil {
			t.Fatalf("a nullable single result must be null without an error: data=%#v errors=%#v", response.Data, response.Errors)
		}

		fixture.seedOwned(t, 61, fixture.alpha, "alpha", "pending")
		required := func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
			rows, err := fixture.systemInvites("alpha")(ctx, "")
			if err != nil {
				return nil, err
			}
			if _, err := fixture.app.System().Invites.Delete(ctx, p10operations.Invites.ByID.Value(p10OperationID(t, 61))); err != nil {
				return nil, err
			}
			return rows[:1], nil
		}
		p10operations.SetInviteSource(required)
		response = fixture.run(t, "alpha", `{ requiredInvite(owner: "x") { id } }`)
		failure := response.onlyError(t)
		if failure.Extensions["code"] != "NOT_FOUND" || !reflect.DeepEqual(failure.Path, []any{"requiredInvite"}) {
			t.Fatalf("a non-null single result must be NOT_FOUND at its path: %#v", failure)
		}
	})
}

func TestCustomQueryHydrationWithoutPrimaryKeyProjection(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		id := fixture.seedOwned(t, 71, fixture.alpha, "alpha", "pending")
		p10operations.SetInviteSource(func(ctx context.Context, _ string) ([]golem.Row[p10operations.Invite], error) {
			return fixture.app.System().Invites.FindMany(ctx, p10operations.Invites.Select(p10operations.Invites.Status))
		})
		response := fixture.run(t, "alpha", `{ searchInvites(owner: "x") { id status team { owner } } }`)
		items := response.items(t, "searchInvites")
		if len(response.Errors) != 0 || len(items) != 1 || items[0]["id"] != id.String() || items[0]["status"] != "pending" {
			t.Fatalf("a resolver that did not project the key failed: %#v errors=%#v", items, response.Errors)
		}
	})
}

func TestCustomMutationReturnsCreatedRowWithNestedSelection(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		id := p10OperationID(t, 81)
		response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitInvite(id: "%s", teamID: "%s", owner: "alpha", email: "n@example.test") { id status team { owner invites { id } } } }`, id, fixture.alpha))
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		created, _ := response.Data["submitInvite"].(map[string]any)
		team, _ := created["team"].(map[string]any)
		if created["id"] != id.String() || created["status"] != "submitted" || team["owner"] != "alpha" {
			t.Fatalf("created row with nested selection = %#v", created)
		}
	})
}

func TestCustomMutationCommittedResultIsNeverHidden(t *testing.T) {
	assertCommitted := func(t *testing.T, failure p10CustomError, root string, reason string, unreadable string) {
		t.Helper()
		if failure.Extensions["code"] != "COMMITTED_RESULT_UNAVAILABLE" || failure.Extensions["committed"] != true || !reflect.DeepEqual(failure.Path, []any{root}) {
			t.Fatalf("committed error = %#v", failure)
		}
		if got, _ := failure.Extensions["reason"].(string); got != reason {
			t.Fatalf("reason = %q, want %q (%#v)", got, reason, failure)
		}
		if unreadable != "" && fmt.Sprint(failure.Extensions["unreadable"]) != unreadable {
			t.Fatalf("unreadable = %v, want %s (%#v)", failure.Extensions["unreadable"], unreadable, failure)
		}
		if unreadable == "" {
			if _, present := failure.Extensions["unreadable"]; present {
				t.Fatalf("unexpected unreadable count: %#v", failure)
			}
		}
		for key := range failure.Extensions {
			switch key {
			case "code", "reason", "committed", "unreadable", "limit", "max":
			default:
				t.Fatalf("extension %q is not part of the committed-result contract: %#v", key, failure)
			}
		}
	}

	t.Run("non-null single is unreadable", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			hidden, later := p10OperationID(t, 91), p10OperationID(t, 92)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { first: submitInvite(id: "%s", teamID: "%s", owner: "stranger", email: "h@example.test") { id } second: inviteMember(id: "%s", teamID: "%s", owner: "alpha", email: "l@example.test", fail: false) }`, hidden, fixture.alpha, later, fixture.alpha))
			if response.Data != nil {
				t.Fatalf("a non-null root failure must null data: %#v", response.Data)
			}
			assertCommitted(t, response.onlyError(t), "first", "RESULT_NOT_READABLE", "1")
			if exists, _, _ := fixture.inviteExists(t, hidden); !exists {
				t.Fatal("the committed row does not exist")
			}
			if exists, _, _ := fixture.inviteExists(t, later); exists {
				t.Fatal("a later serial root ran after a non-null root failed")
			}
			if reports := fixture.reports.take(); len(reports) != 0 {
				t.Fatalf("an unreadable result is not an internal fault: %v", reports)
			}
		})
	})

	t.Run("nullable single is unreadable", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			hidden, later := p10OperationID(t, 93), p10OperationID(t, 94)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { first: submitInviteMaybe(id: "%s", teamID: "%s", owner: "stranger", email: "h@example.test") { id } second: inviteMember(id: "%s", teamID: "%s", owner: "alpha", email: "l@example.test", fail: false) }`, hidden, fixture.alpha, later, fixture.alpha))
			assertCommitted(t, response.onlyError(t), "first", "RESULT_NOT_READABLE", "1")
			if value, present := response.Data["first"]; !present || value != nil || response.Data["second"] != later.String() {
				t.Fatalf("data = %#v", response.Data)
			}
			if exists, _, _ := fixture.inviteExists(t, hidden); !exists {
				t.Fatal("the committed row does not exist")
			}
			if exists, _, _ := fixture.inviteExists(t, later); !exists {
				t.Fatal("a later serial root did not run after a nullable root failed")
			}
		})
	})

	t.Run("list with one unreadable row is never shortened", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			first, second, third := p10OperationID(t, 95), p10OperationID(t, 96), p10OperationID(t, 97)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitInvites(first: "%s", second: "%s", third: "%s", teamID: "%s", foreign: "stranger") { id } }`, first, second, third, fixture.alpha))
			if response.Data != nil {
				t.Fatalf("a shortened or partial list was returned: %#v", response.Data)
			}
			assertCommitted(t, response.onlyError(t), "submitInvites", "RESULT_NOT_READABLE", "1")
			for _, id := range []golem.UUID{first, second, third} {
				if exists, _, _ := fixture.inviteExists(t, id); !exists {
					t.Fatalf("invite %s was not committed", id)
				}
			}
		})
	})

	t.Run("a result that cannot be encoded after commit", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 98)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitBlank(id: "%s", teamID: "%s", owner: "alpha", email: "b@example.test") { id } }`, id, fixture.alpha))
			if response.Data != nil {
				t.Fatalf("data = %#v", response.Data)
			}
			assertCommitted(t, response.onlyError(t), "submitBlank", "", "")
			if exists, _, _ := fixture.inviteExists(t, id); !exists {
				t.Fatal("the committed row does not exist")
			}
			if reports := fixture.reports.take(); len(reports) != 1 {
				t.Fatalf("the cause must be reported exactly once, got %v", reports)
			}
		})
	})

	teamExists := func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
		t.Helper()
		_, err := fixture.app.System().Teams.FindUnique(context.Background(), p10operations.Teams.ByID.Value(id), p10operations.Teams.Select(p10operations.Teams.ID))
		return err == nil
	}

	t.Run("a computed field failure that null-propagates to a custom root", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 400)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitTeam(id: "%s", owner: "alpha") { id pinnedInvite { id } } }`, id))
			if response.Data != nil {
				t.Fatalf("data = %#v", response.Data)
			}
			assertCommitted(t, response.onlyError(t), "submitTeam", "", "")
			if !teamExists(t, fixture, id) {
				t.Fatal("the committed team does not exist")
			}
		})
	})

	t.Run("a computed field failure that null-propagates to a generated root", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 401)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { createTeam(data: {id: "%s", owner: "alpha"}) { id pinnedInvite { id } } }`, id))
			if response.Data != nil {
				t.Fatalf("data = %#v", response.Data)
			}
			assertCommitted(t, response.onlyError(t), "createTeam", "", "")
			if !teamExists(t, fixture, id) {
				t.Fatal("the committed team does not exist")
			}
		})
	})

	t.Run("a nullable computed field failure keeps the committed row visible", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 402)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitTeam(id: "%s", owner: "alpha") { id newestInvite { id } } }`, id))
			created, _ := response.Data["submitTeam"].(map[string]any)
			if created["id"] != id.String() || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] == "COMMITTED_RESULT_UNAVAILABLE" {
				t.Fatalf("data=%#v errors=%#v", response.Data, response.Errors)
			}
		})
	})

	t.Run("a resolver error before commit is unchanged", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 99)
			response := fixture.run(t, "alpha", fmt.Sprintf(`mutation { submitInvite(id: "%s", teamID: "%s", owner: "alpha", email: "not-an-email") { id } }`, id, fixture.alpha))
			failure := response.onlyError(t)
			if failure.Extensions["code"] != "INTERNAL_SERVER_ERROR" {
				t.Fatalf("a resolver failure changed presentation: %#v", failure)
			}
			if _, present := failure.Extensions["committed"]; present {
				t.Fatalf("an uncommitted failure claims to have committed: %#v", failure)
			}
			if exists, _, _ := fixture.inviteExists(t, id); exists {
				t.Fatal("the failed resolver wrote a row")
			}
		})
	})
}

func TestUnreadableCommittedMutationRowCarriesIdentity(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		for label, run := range map[string]func(id golem.UUID) (golem.Row[p10operations.Invite], error){
			"scalar": func(id golem.UUID) (golem.Row[p10operations.Invite], error) {
				return p10operations.Mutate(ctx, caller, p10operations.SubmitInvite, p10operations.SubmitArgs{ID: id, TeamID: fixture.alpha, Owner: "stranger", Email: "i@example.test"})
			},
			"nested": func(id golem.UUID) (golem.Row[p10operations.Invite], error) {
				return p10operations.Mutate(ctx, caller, p10operations.SubmitNested, p10operations.SubmitArgs{ID: id, TeamID: p10OperationID(t, 300), Owner: "stranger", Email: "i@example.test"})
			},
		} {
			id := p10OperationID(t, 200)
			if label == "nested" {
				id = p10OperationID(t, 201)
			}
			row, err := run(id)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if _, present := golem.Value(row, p10operations.Invites.ID).Get(); present {
				t.Fatalf("%s: the unreadable row disclosed a cell", label)
			}
			committed := golem.RuntimeTypedRowIdentity(row)
			if !committed.Present() {
				t.Fatalf("%s: the committed-but-unreadable row has no identity", label)
			}
			stored, err := fixture.app.System().Invites.FindUnique(ctx, p10operations.Invites.ByID.Value(id), p10operations.Invites.Select(p10operations.Invites.Status))
			if err != nil {
				t.Fatal(err)
			}
			if golem.RuntimeTypedRowIdentity(stored).Token() != committed.Token() {
				t.Fatalf("%s: identity differs from the persisted row's identity", label)
			}
		}
	})
}

func TestComputedModelResultIsCallerReadAndResolvesNested(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		second := p10OperationID(t, 301)
		if _, err := fixture.app.System().Teams.Create(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(second), p10operations.Teams.Owner.Create("alpha"))); err != nil {
			t.Fatal(err)
		}
		fixture.seedOwned(t, 311, fixture.alpha, "beta", "pending")
		fixture.seedOwned(t, 312, fixture.alpha, "alpha", "pending")
		p10operations.SetInviteSource(fixture.systemInvites())
		fixture.observer.reset()
		response := fixture.run(t, "alpha", `{ searchTeams(owner: "alpha") { id openInvites { id team { owner } } newestInvite { id } } }`)
		if len(response.Errors) != 0 {
			t.Fatalf("errors: %#v", response.Errors)
		}
		teams := response.items(t, "searchTeams")
		if len(teams) != 2 {
			t.Fatalf("teams = %#v", teams)
		}
		for _, team := range teams {
			open, _ := team["openInvites"].([]any)
			if len(open) != 1 || open[0].(map[string]any)["id"] != p10OperationID(t, 312).String() || open[0].(map[string]any)["team"].(map[string]any)["owner"] != "alpha" {
				t.Fatalf("unreadable rows must be omitted and nested selections resolved: %#v", team)
			}
			if value, present := team["newestInvite"]; !present || value != nil {
				t.Fatalf("an unreadable nullable computed result must be null: %#v", team)
			}
		}
		if reads := fixture.readCount(); reads != 8 {
			t.Fatalf("reads = %d, want the root read, four resolver System reads, one hydration read per computed slot and one relation batch", reads)
		}

		response = fixture.run(t, "alpha", `{ searchTeams(owner: "alpha") { id pinnedInvite { id } } }`)
		failure := response.Errors[0]
		if failure.Extensions["code"] != "NOT_FOUND" || len(failure.Path) != 3 || failure.Path[2] != "pinnedInvite" {
			t.Fatalf("a non-null computed result must be NOT_FOUND at its occurrence: %#v", response.Errors)
		}
	})
}

func TestCustomQueryHydrationBatchesComputedOncePerSlot(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			server, database, _ := newP5ExtensionProviderFixture(t, profile)
			seedP5ExtensionUsers(t, database, profile.provider)
			p5extensions.ResetProbe()
			response := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `{ searchUsers(where: {all: true}) { id batchGreeting(prefix: "slot") } }`, nil)
			if len(response.Errors) != 0 {
				t.Fatalf("errors: %#v", response.Errors)
			}
			if rows := p5ExtensionRows(t, response.Data["searchUsers"]); len(rows) != 3 {
				t.Fatalf("rows = %d, want 3", len(rows))
			}
			if probe := p5extensions.SnapshotProbe(); fmt.Sprint(probe.BatchSizes) != "[2 1]" {
				t.Fatalf("a batched computed field ran once per row instead of once per slot: batch sizes %v", probe.BatchSizes)
			}
		})
	}
}
