package runtime_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func TestRowVersionIsAStringOnReadsAndMutationsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		read := fixture.graphql(t, "alpha", `{ teams { id owner _version } }`)
		if len(read.Errors) != 0 {
			t.Fatalf("read errors=%#v", read.Errors)
		}
		teams, _ := read.Data["teams"].([]any)
		if len(teams) != 1 {
			t.Fatalf("teams=%v", read.Data)
		}
		first := teams[0].(map[string]any)["_version"]
		text, isString := first.(string)
		if !isString {
			t.Fatalf("_version=%T %v, want a decimal string", first, first)
		}
		before, err := strconv.ParseInt(text, 10, 64)
		if err != nil || before <= 0 {
			t.Fatalf("_version=%q err=%v", text, err)
		}

		if _, err := fixture.caller(t, "alpha").Teams.Update(context.Background(), p10operations.Teams.ByID.Value(fixture.alpha), p10operations.Teams.Update(p10operations.Teams.Owner.Set("alpha"))); err != nil {
			t.Fatal(err)
		}
		reread := fixture.graphql(t, "alpha", fmt.Sprintf(`{ team(where: {ID: "%s"}) { _version } }`, fixture.alpha))
		if len(reread.Errors) != 0 {
			t.Fatalf("reread errors=%#v", reread.Errors)
		}
		team, _ := reread.Data["team"].(map[string]any)
		after, err := strconv.ParseInt(team["_version"].(string), 10, 64)
		if err != nil || after <= before {
			t.Fatalf("_version after an update = %d, before = %d, err=%v", after, before, err)
		}

		mutation := fixture.graphql(t, "alpha", fmt.Sprintf(`mutation { updateTeam(where: {ID: "%s"}, data: {owner: {set: "alpha"}}) { _version } }`, fixture.alpha))
		if len(mutation.Errors) != 0 {
			t.Fatalf("mutation errors=%#v", mutation.Errors)
		}
		updated, _ := mutation.Data["updateTeam"].(map[string]any)
		returned, err := strconv.ParseInt(updated["_version"].(string), 10, 64)
		if err != nil || returned <= after {
			t.Fatalf("mutation response _version = %d, previous = %d, err=%v", returned, after, err)
		}
	})
}

func TestRowVersionCannotBeFilteredOrWrittenThroughGraphQLAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		controls := []string{
			`{ teams(where: {owner: {equals: "alpha"}}, orderBy: [{owner: asc}]) { id } }`,
			fmt.Sprintf(`mutation { updateTeam(where: {ID: "%s"}, data: {owner: {set: "alpha"}}) { id } }`, fixture.alpha),
			fmt.Sprintf(`mutation { createTeam(data: {id: "%s", owner: "alpha"}) { id } }`, p10OperationID(t, 7701)),
		}
		for _, control := range controls {
			if response := fixture.graphql(t, "alpha", control); len(response.Errors) != 0 {
				t.Fatalf("control %q failed: %#v", control, response.Errors)
			}
		}
		for name, query := range map[string]string{
			"filter": `{ teams(where: {_version: {equals: "1"}}) { id } }`,
			"order":  `{ teams(orderBy: [{_version: asc}]) { id } }`,
			"update": fmt.Sprintf(`mutation { updateTeam(where: {ID: "%s"}, data: {_version: {set: 9}}) { id } }`, fixture.alpha),
			"create": fmt.Sprintf(`mutation { createTeam(data: {id: "%s", owner: "alpha", _version: 9}) { id } }`, p10OperationID(t, 7700)),
			"upsert": fmt.Sprintf(`mutation { upsertTeam(where: {ID: "%s"}, create: {id: "%s", owner: "alpha"}, update: {_version: {set: 9}}) { id } }`, fixture.alpha, fixture.alpha),
			"unique": `{ team(where: {_version: "1"}) { id } }`,
		} {
			response := fixture.graphql(t, "alpha", query)
			if len(response.Errors) == 0 {
				t.Errorf("%s: a client-supplied row version was accepted and executed", name)
			} else if code := response.Errors[0].Extensions["code"]; code != "GRAPHQL_VALIDATION_FAILED" {
				t.Errorf("%s: a client-supplied row version passed schema validation and was refused only at execution (code %v)", name, code)
			}
		}
	})
}

func TestHooksReadTheRowVersionLikeAnyOtherFieldAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		id := p10OperationID(t, 7800)
		var seen int64
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
			rows, err := golem.HookFindManyRows(ctx, executor, p10operations.GolemGeneratedTeamDescriptor, golem.Where(p10operations.Teams.ID.Eq(id)), p10operations.Teams.Select(p10operations.Teams.GolemVersion))
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("hook read %d rows", len(rows))
			}
			seen, _ = golem.Value(rows[0], p10operations.Teams.GolemVersion).Get()
			return nil
		})
		if _, err := fixture.caller(t, "alpha").Teams.Create(context.Background(), p10TeamInput(id)); err != nil {
			t.Fatal(err)
		}
		row, err := fixture.app.System().Teams.FindUnique(context.Background(), p10operations.Teams.ByID.Value(id), p10operations.Teams.Select(p10operations.Teams.GolemVersion))
		if err != nil {
			t.Fatal(err)
		}
		stored, _ := golem.Value(row, p10operations.Teams.GolemVersion).Get()
		if seen <= 0 || seen != stored {
			t.Fatalf("hook saw version %d, committed row holds %d", seen, stored)
		}
	})
}
