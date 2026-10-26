package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type writeReachOutcome struct {
	code    golem.ErrorCode
	reason  golem.Reason
	changed bool
}

type writeReachCase struct {
	name     string
	policies func(reachHandles) reachPolicies
	write    func(context.Context, *Caller[testPrincipal, testActor], schematest.Fixture, reachHandles, reachPost) error
	expect   func(reachPost) writeReachOutcome
	nested   bool
}

func writeReachUUID(text string) golem.UUID {
	value, err := golem.ParseUUID(text)
	if err != nil {
		panic(err)
	}
	return value
}

func writeReachDecimal() golem.Decimal {
	value, err := golem.ParseDecimal("0")
	if err != nil {
		panic(err)
	}
	return value
}

func writeReachTarget(fixture schematest.Fixture, post reachPost) golem.MutationTarget[testPost] {
	return golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, writeReachUUID(post.id)))
}

func writeReachUserTarget(fixture schematest.Fixture, id string) golem.MutationTarget[testUser] {
	return golem.GeneratedUniqueSelectorValue[testUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, writeReachUUID(id)))
}

func writeReachPostPolicies(read, write func(reachHandles, *golem.Rules[testPost])) func(reachHandles) reachPolicies {
	return func(handles reachHandles) reachPolicies {
		return reachPolicies{
			user: func(rules *golem.Rules[testUser]) {
				rules.CanRead(golem.All[testUser]())
				rules.CanUpdate(golem.All[testUser]())
			},
			post: func(rules *golem.Rules[testPost]) {
				if read != nil {
					read(handles, rules)
				}
				write(handles, rules)
			},
		}
	}
}

func readAllowedPrefix(handles reachHandles, rules *golem.Rules[testPost]) {
	rules.CanRead(handles.title.StartsWith("allowed-"))
}

func deleteWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles, post reachPost) error {
	_, err := CallerDelete(ctx, caller, handles.posts, writeReachTarget(fixture, post))
	return err
}

func updateWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles, post reachPost) error {
	_, err := CallerUpdate(ctx, caller, handles.posts, writeReachTarget(fixture, post), golem.GeneratedUpdateInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, post.title+"-mask")))
	return err
}

func upsertWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles, post reachPost) error {
	values := []golem.CreateValue[testPost]{
		golem.GeneratedCreateFieldValue(fixture.Post, handles.postID, writeReachUUID(post.id)),
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[testPost, int64](fixture.PostBigInt), int64(0)),
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[testPost, golem.Decimal](fixture.PostDecimal), writeReachDecimal()),
		golem.GeneratedCreateFieldValue(fixture.Post, handles.title, "allowed-created"),
	}
	if post.author != nil {
		values = append(values, golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID), writeReachUUID(*post.author)))
	}
	create := golem.GeneratedCreateInput[testPost](fixture.Post, values...)
	_, err := CallerUpsert(ctx, caller, handles.posts, writeReachTarget(fixture, post), create, golem.GeneratedUpdateInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, post.title+"-mask")))
	return err
}

func nestedDeleteWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles, post reachPost) error {
	input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedDelete[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, writeReachTarget(fixture, post)))
	_, err := CallerUpdate(ctx, caller, handles.users, writeReachUserTarget(fixture, *post.author), input)
	return err
}

func deleteManyWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], _ schematest.Fixture, handles reachHandles, post reachPost) error {
	_, err := CallerDeleteMany(ctx, caller, handles.posts, handles.postID.Eq(writeReachUUID(post.id)))
	return err
}

func nestedUpdateWrite(ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles, post reachPost) error {
	update := golem.GeneratedUpdateInput[testPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, handles.title, post.title+"-mask"))
	input := golem.GeneratedUpdateInput[testUser](fixture.User, golem.GeneratedNestedUpdate[testUser, testPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, writeReachTarget(fixture, post), update))
	_, err := CallerUpdate(ctx, caller, handles.users, writeReachUserTarget(fixture, *post.author), input)
	return err
}

func allowedWhen(allowed bool) writeReachOutcome {
	if allowed {
		return writeReachOutcome{changed: true}
	}
	return writeReachOutcome{code: golem.CodeNotFound}
}

func readableAndMatching(suffix string) func(reachPost) writeReachOutcome {
	return func(post reachPost) writeReachOutcome {
		return allowedWhen(readablePostTitle(post.title) && len(post.title) >= len(suffix) && post.title[len(post.title)-len(suffix):] == suffix)
	}
}

func writeReachCases() []writeReachCase {
	disjointDelete := writeReachPostPolicies(readAllowedPrefix, func(handles reachHandles, rules *golem.Rules[testPost]) {
		rules.CanDelete(handles.title.EndsWith("mask"))
	})
	disjointUpdate := writeReachPostPolicies(readAllowedPrefix, func(handles reachHandles, rules *golem.Rules[testPost]) {
		rules.CanUpdate(handles.title.EndsWith("mask"))
	})
	unconditionalDelete := writeReachPostPolicies(readAllowedPrefix, func(_ reachHandles, rules *golem.Rules[testPost]) {
		rules.CanDelete(golem.All[testPost]())
	})
	unconditionalUpdate := writeReachPostPolicies(readAllowedPrefix, func(_ reachHandles, rules *golem.Rules[testPost]) {
		rules.CanUpdate(golem.All[testPost]())
	})
	readable := func(post reachPost) writeReachOutcome { return allowedWhen(readablePostTitle(post.title)) }
	return []writeReachCase{
		{name: "DisjointDelete", policies: disjointDelete, write: deleteWrite, expect: readableAndMatching("mask")},
		{name: "UnconditionalDelete", policies: unconditionalDelete, write: deleteWrite, expect: readable},
		{name: "DisjointUpdate", policies: disjointUpdate, write: updateWrite, expect: readableAndMatching("mask")},
		{name: "UnconditionalUpdate", policies: unconditionalUpdate, write: updateWrite, expect: readable},
		{name: "UnconditionalUpsert", policies: unconditionalUpdate, write: upsertWrite, expect: func(post reachPost) writeReachOutcome {
			if readablePostTitle(post.title) {
				return writeReachOutcome{changed: true}
			}
			return writeReachOutcome{code: golem.CodeForbidden, reason: golem.ReasonNotGranted}
		}},
		{name: "NestedDisjointDelete", policies: disjointDelete, write: nestedDeleteWrite, expect: readableAndMatching("mask"), nested: true},
		{name: "NestedUnconditionalUpdate", policies: unconditionalUpdate, write: nestedUpdateWrite, expect: readable, nested: true},
		{name: "MissingDeleteGrant", policies: writeReachPostPolicies(readAllowedPrefix, func(reachHandles, *golem.Rules[testPost]) {}), write: deleteWrite, expect: func(reachPost) writeReachOutcome {
			return writeReachOutcome{code: golem.CodeForbidden, reason: golem.ReasonNotGranted}
		}},
		{name: "DisjointDeleteMany", policies: disjointDelete, write: deleteManyWrite, expect: func(post reachPost) writeReachOutcome {
			outcome := readableAndMatching("mask")(post)
			return writeReachOutcome{changed: outcome.changed}
		}},
		{name: "MissingDeleteManyGrant", policies: writeReachPostPolicies(readAllowedPrefix, func(reachHandles, *golem.Rules[testPost]) {}), write: deleteManyWrite, expect: func(reachPost) writeReachOutcome {
			return writeReachOutcome{code: golem.CodeForbidden, reason: golem.ReasonNotGranted}
		}},
		{name: "MissingUpdateGrant", policies: writeReachPostPolicies(readAllowedPrefix, func(reachHandles, *golem.Rules[testPost]) {}), write: updateWrite, expect: func(reachPost) writeReachOutcome {
			return writeReachOutcome{code: golem.CodeForbidden, reason: golem.ReasonNotGranted}
		}},
		{name: "UnprovenDelete", policies: writeReachPostPolicies(nil, func(_ reachHandles, rules *golem.Rules[testPost]) {
			rules.CanDelete(golem.All[testPost]())
		}), write: deleteWrite, expect: func(reachPost) writeReachOutcome {
			return writeReachOutcome{code: golem.CodeForbidden, reason: golem.ReasonAuthorizationUnproven}
		}},
	}
}

func postTitles(t *testing.T, ctx context.Context, target reachDatabase, fixture schematest.Fixture, handles reachHandles) map[string]string {
	t.Helper()
	app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, reachPolicies{
		user: func(rules *golem.Rules[testUser]) { rules.CanRead(golem.All[testUser]()) },
		post: func(rules *golem.Rules[testPost]) { rules.CanRead(golem.All[testPost]()) },
	})
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(ctx, caller, handles.posts, golem.Select[testPost](handles.postID, handles.title))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		id, _ := golem.Value(row, handles.postID).Get()
		title, _ := golem.Value(row, handles.title).Get()
		result[id.String()] = title
	}
	return result
}

func assertWriteOutcome(t *testing.T, err error, want writeReachOutcome, before, after map[string]string, post reachPost) {
	t.Helper()
	changed := before[post.id] != after[post.id] || len(before) != len(after)
	if changed != want.changed {
		t.Errorf("%s changed=%v want=%v (err=%v)", post.title, changed, want.changed, err)
	}
	for id, title := range before {
		if id != post.id && after[id] != title {
			t.Errorf("%s write changed another row %s", post.title, id)
		}
	}
	if want.code == "" {
		if err != nil {
			t.Errorf("%s refused: %v (%v)", post.title, err, errors.Unwrap(err))
		}
		return
	}
	var failure *golem.Error
	if !errors.As(err, &failure) {
		t.Errorf("%s err=%v want code %s", post.title, err, want.code)
		return
	}
	if failure.Code != want.code || want.reason != "" && failure.Reason != want.reason {
		t.Errorf("%s code=%s reason=%s want %s/%s (%v)", post.title, failure.Code, failure.Reason, want.code, want.reason, err)
	}
	if failure.Code == golem.CodeBadUserInput {
		t.Errorf("%s authorization outcome surfaced as BAD_USER_INPUT", post.title)
	}
}

func TestExistingRowWritesPlanOverReadReachAcrossProviders(t *testing.T) {
	fixture := schematest.NewIndexedFullExactScopedNullableAuthor(t)
	handles := newReachHandles(fixture)
	for index, open := range reachDatabases(t, "write_reach", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			for _, candidate := range writeReachCases() {
				candidate := candidate
				t.Run(candidate.name, func(t *testing.T) {
					for _, post := range reachPosts {
						post := post
						if candidate.nested && post.author == nil {
							continue
						}
						t.Run(post.title+"_"+post.id[len(post.id)-2:], func(t *testing.T) {
							ctx := context.Background()
							target := open(t)
							before := postTitles(t, ctx, target, fixture, handles)
							app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, candidate.policies(handles))
							caller, err := app.ForPrincipal(ctx, testPrincipal{})
							if err != nil {
								t.Fatal(err)
							}
							err = candidate.write(ctx, caller, fixture, handles, post)
							after := postTitles(t, ctx, target, fixture, handles)
							assertWriteOutcome(t, err, candidate.expect(post), before, after, post)
						})
					}
				})
			}
		})
	}
}

func TestWriteActionRelationProbeDoesNotDependOnUnreadableTarget(t *testing.T) {
	fixture := schematest.NewIndexedFullExactScopedNullableAuthor(t)
	handles := newReachHandles(fixture)
	unreadableAuthor := reachPosts[2]
	for index, open := range reachDatabases(t, "write_probe", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			outcomes := make(map[string]golem.ErrorCode)
			for _, name := range []string{"bob", "nobody"} {
				name := name
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					target := open(t)
					before := postTitles(t, ctx, target, fixture, handles)
					app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, reachPolicies{
						user: func(rules *golem.Rules[testUser]) { rules.CanRead(handles.userName.StartsWith("a")) },
						post: func(rules *golem.Rules[testPost]) {
							rules.CanRead(golem.All[testPost]())
							rules.CanDelete(handles.author.Is(handles.userName.Eq(name)))
						},
					})
					caller, err := app.ForPrincipal(ctx, testPrincipal{})
					if err != nil {
						t.Fatal(err)
					}
					err = deleteWrite(ctx, caller, fixture, handles, unreadableAuthor)
					after := postTitles(t, ctx, target, fixture, handles)
					assertWriteOutcome(t, err, writeReachOutcome{code: golem.CodeNotFound}, before, after, unreadableAuthor)
					var failure *golem.Error
					if errors.As(err, &failure) {
						outcomes[name] = failure.Code
					}
				})
			}
			if outcomes["bob"] != outcomes["nobody"] {
				t.Fatalf("delete outcome depends on the unreadable author: %v", outcomes)
			}
		})
	}
}

func TestWriteActionRelationRuleStillAppliesToReadableTarget(t *testing.T) {
	fixture := schematest.NewIndexedFullExactScopedNullableAuthor(t)
	handles := newReachHandles(fixture)
	readableAuthor := reachPosts[0]
	for index, open := range reachDatabases(t, "write_probe_readable", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			for name, want := range map[string]writeReachOutcome{"alice": {changed: true}, "nobody": {code: golem.CodeNotFound}} {
				name, want := name, want
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					target := open(t)
					before := postTitles(t, ctx, target, fixture, handles)
					app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, reachPolicies{
						user: func(rules *golem.Rules[testUser]) { rules.CanRead(handles.userName.StartsWith("a")) },
						post: func(rules *golem.Rules[testPost]) {
							rules.CanRead(golem.All[testPost]())
							rules.CanDelete(handles.author.Is(handles.userName.Eq(name)))
						},
					})
					caller, err := app.ForPrincipal(ctx, testPrincipal{})
					if err != nil {
						t.Fatal(err)
					}
					err = deleteWrite(ctx, caller, fixture, handles, readableAuthor)
					after := postTitles(t, ctx, target, fixture, handles)
					assertWriteOutcome(t, err, want, before, after, readableAuthor)
				})
			}
		})
	}
}
