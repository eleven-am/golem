package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func TestP6GraphQLAndGoAnalyticsPlanPolicySQLAndResultOracle(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			count, titleCount, titleMin := p5social.Posts.CountAll(), p5social.Posts.Title.Count(), p5social.Posts.Title.Min()
			title, authorName := p5social.Posts.Title.Dimension(), p5social.Posts.AuthorName

			harness.trace.reset()
			aggregate, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(p5social.Posts.AggregateSelect(count, titleCount, titleMin)))
			if err != nil {
				t.Fatal(err)
			}
			groups, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(
				p5social.Posts.GroupDimensions(title), p5social.Posts.GroupMeasures(count),
				p5social.Posts.GroupHaving(golem.AndGroup(count.GTE(1), titleCount.GTE(1), titleMin.GTE("a-open"))),
				p5social.Posts.GroupOrderBy(titleMin.Asc()), p5social.Posts.GroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			relationGroups, err := caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(authorName), p5social.Posts.RelationGroupMeasures(count),
				p5social.Posts.RelationGroupWhere(p5social.Posts.AuthorID.Eq(principal.UserID)),
				p5social.Posts.RelationGroupOrderBy(authorName.Asc()), p5social.Posts.RelationGroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			goSQL := harness.trace.snapshot()
			if len(goSQL) != 3 {
				t.Fatalf("Go analytics statements=%d", len(goSQL))
			}
			if value, ok := golem.AggregateValue(aggregate, count).Get(); !ok || value != 4 {
				t.Fatalf("Go aggregate count=%d/%v", value, ok)
			}
			if len(groups) != 3 || len(relationGroups) != 1 {
				t.Fatalf("Go group sizes=%d/%d", len(groups), len(relationGroups))
			}

			harness.trace.reset()
			response := harness.execute(t, principal, `query {
  aggregatePosts { count countFields { title } min { title } }
  groupByPosts(by: [title], having: {count: {gte: "1"}, countFields: {title: {gte: "1"}}, min: {title: {gte: "a-open"}}}, orderBy: [{min: {title: asc}}], take: 100) { key { title } count }
  relationGroupByPosts(by: [authorName], where: {authorID: {equals: "00000000-0000-0000-0000-000000000001"}}, orderBy: [{key: {authorName: asc}}], take: 100) { key { authorName } count }
}`, nil)
			if len(response.Errors) != 0 {
				t.Fatalf("GraphQL analytics errors=%#v", response.Errors)
			}
			graphqlSQL := harness.trace.snapshot()
			if !reflect.DeepEqual(graphqlSQL, goSQL) {
				t.Fatalf("GraphQL and Go SQL differ\nGraphQL=%#v\nGo=%#v", graphqlSQL, goSQL)
			}
			aggregateJSON := p5SocialMap(t, response.Data["aggregatePosts"])
			if aggregateJSON["count"] != "4" || p5SocialMap(t, aggregateJSON["countFields"])["title"] != "4" || p5SocialMap(t, aggregateJSON["min"])["title"] != "a-open" {
				t.Fatalf("GraphQL aggregate=%#v", aggregateJSON)
			}
			groupJSON := p5SocialSlice(t, response.Data["groupByPosts"])
			if len(groupJSON) != 3 || p5SocialMap(t, p5SocialMap(t, groupJSON[0])["key"])["title"] != "a-open" || p5SocialMap(t, groupJSON[0])["count"] != "2" {
				t.Fatalf("GraphQL groups=%#v", groupJSON)
			}
			relationJSON := p5SocialSlice(t, response.Data["relationGroupByPosts"])
			if len(relationJSON) != 1 || p5SocialMap(t, p5SocialMap(t, relationJSON[0])["key"])["authorName"] != "owner" || p5SocialMap(t, relationJSON[0])["count"] != "3" {
				t.Fatalf("GraphQL relation groups=%#v", relationJSON)
			}
		})
	}
}

func TestP6GraphQLErrorSanitizationAndPrincipalIsolation(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	owner := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
	other := p5social.Principal{Valid: true, UserID: golem.UUID{15: 2}}
	query := `query Isolated($user: UUID!) { relationGroupByPosts(by: [authorName], where: {authorID: {equals: $user}}, take: 100) { key { authorName } count } }`
	type outcome struct {
		principal string
		response  p5SocialGeneratedResponse
		err       error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for name, principal := range map[string]p5social.Principal{"owner": owner, "other": other} {
		name, principal := name, principal
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := harness.executeRaw(context.WithValue(context.Background(), p5SocialPrincipalKey{}, principal), query, map[string]any{"user": principal.UserID.String()})
			results <- outcome{principal: name, response: response, err: err}
		}()
	}
	wait.Wait()
	close(results)
	seen := map[string]string{}
	for result := range results {
		if result.err != nil || len(result.response.Errors) != 0 {
			t.Fatalf("%s response error=%v/%#v", result.principal, result.err, result.response.Errors)
		}
		rows := p5SocialSlice(t, result.response.Data["relationGroupByPosts"])
		if len(rows) != 1 {
			t.Fatalf("%s rows=%#v", result.principal, rows)
		}
		seen[result.principal] = fmt.Sprint(p5SocialMap(t, p5SocialMap(t, rows[0])["key"])["authorName"], ":", p5SocialMap(t, rows[0])["count"])
	}
	if seen["owner"] != "owner:3" || seen["other"] != "other:1" {
		t.Fatalf("principal-isolated results=%#v", seen)
	}

	harness.trace.reset()
	invalid := harness.execute(t, owner, `query { groupByPosts(by: [title]) { key { authorID } count } }`, nil)
	if len(invalid.Errors) != 1 || invalid.Errors[0].Extensions["code"] != "BAD_USER_INPUT" || strings.Contains(strings.ToLower(invalid.Errors[0].Message), "p6_") || len(harness.trace.snapshot()) != 0 {
		t.Fatalf("semantic error was not sanitized/no-SQL: %#v statements=%#v", invalid.Errors, harness.trace.snapshot())
	}
	harness.trace.reset()
	overflow := harness.execute(t, owner, `query { groupByPosts(by: [title], having: {count: {gt: "9223372036854775808"}}) { key { title } count } }`, nil)
	if len(overflow.Errors) != 1 || overflow.Errors[0].Extensions["code"] != "BAD_USER_INPUT" || len(harness.trace.snapshot()) != 0 {
		t.Fatalf("out-of-int64 count having was not refused before SQL: %#v", overflow.Errors)
	}

	if err := harness.database.Close(); err != nil {
		t.Fatal(err)
	}
	failure := harness.execute(t, owner, `query { aggregatePosts { count } }`, nil)
	if len(failure.Errors) != 1 || failure.Errors[0].Extensions["code"] != "BAD_USER_INPUT" || failure.Errors[0].Message != "analytics query failed" || strings.Contains(strings.ToLower(failure.Errors[0].Message), "driver") || strings.Contains(strings.ToLower(failure.Errors[0].Message), "closed") {
		t.Fatalf("provider failure leaked details: %#v", failure.Errors)
	}
}

func TestP6GraphQLMissingTakeProbesPlusOneAndExplicitTakeNeverClamps(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarnessWithAnalyticsLimits(t, profile, golemruntime.AnalyticsLimits{MaxProgrammaticGroups: 2})
			for index := 0; index < 101; index++ {
				query := `INSERT INTO ` + harness.table("posts") + ` ("id","author_id","title") VALUES (?,?,?)`
				if _, err := harness.database.ExecContext(context.Background(), harness.database.Rebind(query), p5SocialID(1_000+index), p5SocialID(1), fmt.Sprintf("scale-%03d", index)); err != nil {
					t.Fatal(err)
				}
			}
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			title := p5social.Posts.Title.Dimension()
			harness.trace.reset()
			if _, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(p5social.Posts.GroupDimensions(title), p5social.Posts.GroupTake(3))); err == nil {
				t.Fatal("programmatic maxGroups was not enforced")
			}
			if len(harness.trace.snapshot()) != 0 {
				t.Fatal("programmatic maxGroups refusal touched SQL")
			}

			harness.trace.reset()
			explicit := harness.execute(t, principal, `query { groupByPosts(by: [title], orderBy: [{key: {title: asc}}], take: 3) { key { title } count } }`, nil)
			if len(explicit.Errors) != 0 || len(p5SocialSlice(t, explicit.Data["groupByPosts"])) != 3 {
				t.Fatalf("GraphQL take was coupled to programmatic max: %#v", explicit)
			}
			if len(harness.trace.snapshot()) != 1 {
				t.Fatalf("explicit GraphQL take statements=%d", len(harness.trace.snapshot()))
			}

			harness.trace.reset()
			omitted := harness.execute(t, principal, `query { groupByPosts(by: [title], orderBy: [{key: {title: asc}}]) { key { title } count } }`, nil)
			if len(omitted.Errors) != 1 || omitted.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
				t.Fatalf("omitted take overflow=%#v", omitted)
			}
			if omitted.Data != nil && omitted.Data["groupByPosts"] != nil {
				t.Fatalf("omitted take returned a partial prefix: %#v", omitted.Data)
			}
			if len(harness.trace.snapshot()) != 1 {
				t.Fatalf("missing-take probe statements=%d", len(harness.trace.snapshot()))
			}

			harness.trace.reset()
			bounded := harness.execute(t, principal, `query { groupByPosts(by: [title], orderBy: [{key: {title: asc}}], take: 100) { key { title } count } }`, nil)
			if len(bounded.Errors) != 0 || len(p5SocialSlice(t, bounded.Data["groupByPosts"])) != 100 {
				t.Fatalf("explicit take was clamped or refused: errors=%#v rows=%#v", bounded.Errors, bounded.Data["groupByPosts"])
			}
		})
	}
}

func TestP6ProgrammaticGroupLimitIsIndependentOfGraphQL(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarnessWithAnalyticsLimits(t, profile, golemruntime.AnalyticsLimits{MaxProgrammaticGroups: 2})
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			title := p5social.Posts.Title.Dimension()
			harness.trace.reset()
			_, err = caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(
				p5social.Posts.GroupDimensions(title),
				p5social.Posts.GroupTake(3),
			))
			if err == nil || len(harness.trace.snapshot()) != 0 {
				t.Fatalf("programmatic limit did not refuse before SQL: error=%v statements=%#v", err, harness.trace.snapshot())
			}
			harness.trace.reset()
			response := harness.execute(t, principal, `query { groupByPosts(by: [title], orderBy: [{key: {title: asc}}], take: 3) { key { title } count } }`, nil)
			if len(response.Errors) != 0 || len(p5SocialSlice(t, response.Data["groupByPosts"])) != 3 {
				t.Fatalf("GraphQL limit was coupled to programmatic limit: %#v", response)
			}
			if len(harness.trace.snapshot()) != 1 {
				t.Fatalf("GraphQL independent-limit statements=%d", len(harness.trace.snapshot()))
			}
		})
	}
}

func TestP6LocalAggregateAndGroupUseAuthorizedSingleStatementsAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			userID, err := golem.ParseUUID(p5SocialID(1))
			if err != nil {
				t.Fatalf("%v: %v", err, errors.Unwrap(err))
			}
			caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: userID})
			if err != nil {
				t.Fatal(err)
			}
			count := p5social.Posts.CountAll()
			titles := p5social.Posts.Title.Count()
			ordinaryCount, err := caller.Posts.Count(context.Background())
			if err != nil || ordinaryCount != 4 {
				t.Fatalf("ordinary count=%d error=%v", ordinaryCount, err)
			}
			harness.trace.reset()
			result, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(p5social.Posts.AggregateSelect(count, titles)))
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := golem.AggregateValue(result, count).Get(); !ok || value != 4 {
				t.Fatalf("count=%v/%v", value, ok)
			}
			if value, ok := golem.AggregateValue(result, titles).Get(); !ok || value != 4 {
				t.Fatalf("title count=%v/%v", value, ok)
			}
			if got := len(harness.trace.snapshot()); got != 1 {
				t.Fatalf("aggregate statements=%d", got)
			}

			title := p5social.Posts.Title.Dimension()
			harness.trace.reset()
			rows, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(p5social.Posts.GroupDimensions(title), p5social.Posts.GroupMeasures(count), p5social.Posts.GroupOrderBy(title.Asc())))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 {
				t.Fatalf("groups=%d", len(rows))
			}
			if value, ok := golem.GroupValue(rows[0], title).Get(); !ok || value != "a-open" {
				t.Fatalf("first title=%q/%v", value, ok)
			}
			if value, ok := golem.GroupValue(rows[0], count).Get(); !ok || value != 2 {
				t.Fatalf("first count=%d/%v", value, ok)
			}
			if got := len(harness.trace.snapshot()); got != 1 {
				t.Fatalf("group statements=%d", got)
			}

			authorName := p5social.Posts.AuthorName
			harness.trace.reset()
			_, err = caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(authorName),
				p5social.Posts.RelationGroupMeasures(count),
			))
			if err == nil {
				t.Fatal("conditional relation dimension succeeded without a proved contribution predicate")
			}
			if got := len(harness.trace.snapshot()); got != 0 {
				t.Fatalf("undischarged relation group statements=%d", got)
			}

			harness.trace.reset()
			relationRows, err := caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(authorName),
				p5social.Posts.RelationGroupMeasures(count),
				p5social.Posts.RelationGroupWhere(p5social.Posts.AuthorID.Eq(userID)),
				p5social.Posts.RelationGroupOrderBy(authorName.Asc()),
			))
			if err != nil {
				t.Fatalf("%v: %v", err, errors.Unwrap(err))
			}
			if len(relationRows) != 1 {
				t.Fatalf("authorized relation groups=%d", len(relationRows))
			}
			if value, ok := golem.RelationGroupValue(relationRows[0], authorName).Get(); !ok || value != "owner" {
				t.Fatalf("author name=%q/%v", value, ok)
			}
			if value, ok := golem.RelationGroupValue(relationRows[0], count).Get(); !ok || value != 3 {
				t.Fatalf("author count=%d/%v", value, ok)
			}
			if got := len(harness.trace.snapshot()); got != 1 {
				t.Fatalf("relation group statements=%d", got)
			}
		})
	}
}

func TestP6PostTagExplicitJoinModelAnalytics(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
			if err != nil {
				t.Fatal(err)
			}
			count := p5social.PostTags.CountAll()
			tagName := p5social.PostTags.TagName.Dimension()
			harness.trace.reset()
			groups, err := caller.PostTags.GroupBy(context.Background(), p5social.PostTags.GroupBy(
				p5social.PostTags.GroupDimensions(tagName),
				p5social.PostTags.GroupMeasures(count),
				p5social.PostTags.GroupOrderBy(tagName.Asc()),
				p5social.PostTags.GroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			statements := harness.trace.snapshot()
			if len(statements) != 1 {
				t.Fatalf("explicit join-model analytics statements=%d", len(statements))
			}
			if strings.Contains(strings.ToUpper(statements[0]), " JOIN ") {
				t.Fatalf("ordinary PostTag analytics unexpectedly traversed a relation: %s", statements[0])
			}
			if len(groups) != 2 {
				t.Fatalf("PostTag groups=%d", len(groups))
			}
			firstKey, firstOK := golem.GroupValue(groups[0], tagName).Get()
			firstCount, firstCountOK := golem.GroupValue(groups[0], count).Get()
			secondKey, secondOK := golem.GroupValue(groups[1], tagName).Get()
			secondCount, secondCountOK := golem.GroupValue(groups[1], count).Get()
			if !firstOK || !firstCountOK || !secondOK || !secondCountOK || firstKey != "tag-a" || secondKey != "tag-b" || firstCount != 1 || secondCount != 1 {
				t.Fatalf("PostTag groups=%#v", groups)
			}
		})
	}
}

func TestP6InvalidProgrammaticRequestCorpusTouchesDatabaseZeroTimes(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	userID, err := golem.ParseUUID(p5SocialID(1))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"zero aggregate", func() error {
			_, err := caller.Posts.Aggregate(context.Background(), golem.AggregateRequest[p5social.Post]{})
			return err
		}},
		{"duplicate aggregate measure", func() error {
			count := p5social.Posts.CountAll()
			_, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(p5social.Posts.AggregateSelect(count, count)))
			return err
		}},
		{"duplicate group dimension", func() error {
			title := p5social.Posts.Title.Dimension()
			_, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(p5social.Posts.GroupDimensions(title, title)))
			return err
		}},
		{"zero take", func() error {
			title := p5social.Posts.Title.Dimension()
			_, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(p5social.Posts.GroupDimensions(title), p5social.Posts.GroupTake(0)))
			return err
		}},
		{"negative skip", func() error {
			title := p5social.Posts.Title.Dimension()
			_, err := caller.Posts.GroupBy(context.Background(), p5social.Posts.GroupBy(p5social.Posts.GroupDimensions(title), p5social.Posts.GroupSkip(-1)))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness.trace.reset()
			if err := test.run(); err == nil {
				t.Fatal("invalid request succeeded")
			}
			if statements := harness.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("invalid request touched database: %#v", statements)
			}
		})
	}
}

func TestP6AnalyticsCallerSystemAndTransactionParity(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	userID, err := golem.ParseUUID(p5SocialID(1))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	title := p5social.Posts.Title.Dimension()
	count := p5social.Posts.CountAll()
	request := p5social.Posts.GroupBy(
		p5social.Posts.GroupDimensions(title),
		p5social.Posts.GroupMeasures(count),
		p5social.Posts.GroupOrderBy(title.Asc()),
	)
	assert := func(rows []golem.GroupRow[p5social.Post], err error) {
		t.Helper()
		if err != nil || len(rows) != 3 {
			t.Fatalf("group rows=%d err=%v", len(rows), err)
		}
		if value, ok := golem.GroupValue(rows[0], title).Get(); !ok || value != "a-open" {
			t.Fatalf("first group=%q/%v", value, ok)
		}
	}
	rows, err := caller.Posts.GroupBy(context.Background(), request)
	assert(rows, err)
	rows, err = harness.app.System().Posts.GroupBy(context.Background(), request)
	assert(rows, err)
	if err := caller.Transaction(context.Background(), func(tx *p5social.CallerTx[p5social.Principal]) error {
		rows, err := tx.Posts.GroupBy(context.Background(), request)
		assert(rows, err)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := harness.app.System().Transaction(context.Background(), func(tx *p5social.SystemTx[p5social.Principal]) error {
		rows, err := tx.Posts.GroupBy(context.Background(), request)
		assert(rows, err)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestP6ConditionalMeasureDimensionHavingAndOrderDischarge(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	userID, err := golem.ParseUUID(p5SocialID(1))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	nameCount := p5social.Users.Name.Count()
	nameDimension := p5social.Users.Name.Dimension()
	nameMinimum := p5social.Users.Name.Min()
	idDimension := p5social.Users.ID.Dimension()

	invalid := []struct {
		name string
		run  func() error
	}{
		{"selected measure", func() error {
			_, err := caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(p5social.Users.AggregateSelect(nameCount)))
			return err
		}},
		{"dimension", func() error {
			_, err := caller.Users.GroupBy(context.Background(), p5social.Users.GroupBy(p5social.Users.GroupDimensions(nameDimension)))
			return err
		}},
		{"private having", func() error {
			_, err := caller.Users.GroupBy(context.Background(), p5social.Users.GroupBy(
				p5social.Users.GroupDimensions(idDimension),
				p5social.Users.GroupHaving(nameMinimum.GT("a")),
			))
			return err
		}},
		{"private order", func() error {
			_, err := caller.Users.GroupBy(context.Background(), p5social.Users.GroupBy(
				p5social.Users.GroupDimensions(idDimension),
				p5social.Users.GroupOrderBy(nameMinimum.Asc()),
			))
			return err
		}},
	}
	for _, test := range invalid {
		t.Run(test.name+" refuses before SQL", func(t *testing.T) {
			harness.trace.reset()
			if err := test.run(); err == nil {
				t.Fatal("undischarged conditional field succeeded")
			}
			if statements := harness.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("undischarged field touched database: %#v", statements)
			}
		})
	}

	where := p5social.Users.ID.Eq(userID)
	harness.trace.reset()
	aggregate, err := caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
		p5social.Users.AggregateWhere(where),
		p5social.Users.AggregateSelect(nameCount),
	))
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := golem.AggregateValue(aggregate, nameCount).Get(); !ok || value != 1 {
		t.Fatalf("discharged name count=%d/%v", value, ok)
	}
	rows, err := caller.Users.GroupBy(context.Background(), p5social.Users.GroupBy(
		p5social.Users.GroupDimensions(idDimension, nameDimension),
		p5social.Users.GroupWhere(where),
		p5social.Users.GroupHaving(nameMinimum.GT("a")),
		p5social.Users.GroupOrderBy(nameMinimum.Asc()),
	))
	if err != nil || len(rows) != 1 {
		t.Fatalf("discharged group rows=%d error=%v", len(rows), err)
	}
	if statements := harness.trace.snapshot(); len(statements) != 2 {
		t.Fatalf("discharged analytics statements=%d", len(statements))
	}
}

func TestP6UndischargedFieldRefusesByLogicalNameBeforeSQL(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
	if err != nil {
		t.Fatal(err)
	}
	harness.trace.reset()
	_, err = caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
		p5social.Users.AggregateSelect(p5social.Users.Name.Count()),
	))
	if err == nil || !strings.Contains(err.Error(), `"name"`) {
		t.Fatalf("undischarged field error does not identify the public logical name: %v", err)
	}
	if statements := harness.trace.snapshot(); len(statements) != 0 {
		t.Fatalf("undischarged logical field touched database: %#v", statements)
	}
}

func TestP6RelationRequestRejectsRelatedMeasuresAndForeignPaths(t *testing.T) {
	harness := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	caller, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := golem.RuntimeFreezeRelationGroupRequest(p5social.Posts.RelationGroupBy(
		p5social.Posts.RelationGroupDimensions(p5social.Posts.AuthorName),
	))
	if err != nil {
		t.Fatal(err)
	}
	term := valid.Dimensions()[0]
	forged := golem.GeneratedRelationDimension[p5social.Post, string](term.Model, term.RelationName, []golem.RelationID{{9}}, term.Field, true)
	harness.trace.reset()
	_, err = caller.Posts.RelationGroupBy(context.Background(), p5social.Posts.RelationGroupBy(
		p5social.Posts.RelationGroupDimensions(forged),
	))
	if err == nil {
		t.Fatal("foreign relation path succeeded")
	}
	if statements := harness.trace.snapshot(); len(statements) != 0 {
		t.Fatalf("foreign relation path touched database: %#v", statements)
	}
}

func TestP6ClassificationPositionSpyCoversWhereCountMeasureDimensionHavingOrderAndGraphQLSelection(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}

			// The caller predicate is itself a value-influencing position. Name is
			// conditional on the actor's ID, and filtering by Name alone cannot
			// discharge that condition.
			harness.trace.reset()
			_, err = caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
				p5social.Users.AggregateWhere(p5social.Users.Name.Eq("owner")),
				p5social.Users.AggregateSelect(p5social.Users.CountAll()),
			))
			if err == nil || len(harness.trace.snapshot()) != 0 {
				t.Fatalf("conditional where was not classified before SQL: error=%v statements=%#v", err, harness.trace.snapshot())
			}

			where := p5social.Users.ID.Eq(principal.UserID)
			nameCount := p5social.Users.Name.Count()
			nameMin := p5social.Users.Name.Min()
			nameDimension := p5social.Users.Name.Dimension()
			harness.trace.reset()
			aggregate, err := caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
				p5social.Users.AggregateWhere(where),
				p5social.Users.AggregateSelect(nameCount, nameMin),
			))
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := golem.AggregateValue(aggregate, nameCount).Get(); !ok || value != 1 {
				t.Fatalf("classified field-count=%d/%t", value, ok)
			}
			rows, err := caller.Users.GroupBy(context.Background(), p5social.Users.GroupBy(
				p5social.Users.GroupDimensions(nameDimension),
				p5social.Users.GroupWhere(where),
				p5social.Users.GroupHaving(nameMin.Eq("owner")),
				p5social.Users.GroupOrderBy(nameMin.Asc()),
			))
			if err != nil || len(rows) != 1 {
				t.Fatalf("classified dimension/having/order rows=%d error=%v", len(rows), err)
			}
			if len(harness.trace.snapshot()) != 2 {
				t.Fatalf("classified Go analytical positions statements=%d", len(harness.trace.snapshot()))
			}

			// Active gqlgen selection compilation must contribute the selected
			// measure to the same runtime plan. SQL execution proves the selected
			// min field survived lowering; Go/GraphQL byte parity is covered by the
			// dedicated cross-entry-point oracle.
			harness.trace.reset()
			response := harness.execute(t, principal, `query { aggregatePosts { min { title } } }`, nil)
			if len(response.Errors) != 0 || p5SocialMap(t, p5SocialMap(t, response.Data["aggregatePosts"])["min"])["title"] != "a-open" {
				t.Fatalf("GraphQL selection classification response=%#v", response)
			}
			if len(harness.trace.snapshot()) != 1 {
				t.Fatalf("GraphQL selected measure statements=%d", len(harness.trace.snapshot()))
			}
		})
	}
}
