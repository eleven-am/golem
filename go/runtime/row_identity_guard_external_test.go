package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func pinnedGuardSQL(t *testing.T, profile p5ExtensionProviderProfile, label string) string {
	t.Helper()
	provider := "sqlite"
	if profile.provider == golem.PostgreSQL {
		provider = "postgresql"
	}
	content, err := os.ReadFile(filepath.Join("testdata", "row_identity_guard", provider+"."+label+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func assertPinnedGuardSQL(t *testing.T, harness *p5SocialGeneratedHarness, label string) {
	t.Helper()
	statements := harness.trace.snapshot()
	harness.trace.reset()
	if len(statements) != 1 {
		t.Fatalf("%s issued %d statements, want 1: %q", label, len(statements), statements)
	}
	if want := pinnedGuardSQL(t, harness.profile, label); statements[0] != want {
		t.Fatalf("%s SQL changed\ngot:  %s\nwant: %s", label, statements[0], want)
	}
}

func TestGroupedAndAggregateReadsUnchangedByHiddenIdentity(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			harness := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			count, titleCount, titleMin := p5social.Posts.CountAll(), p5social.Posts.Title.Count(), p5social.Posts.Title.Min()
			title, authorName := p5social.Posts.Title.Dimension(), p5social.Posts.AuthorName

			harness.trace.reset()
			aggregate, err := caller.Posts.Aggregate(ctx, p5social.Posts.Aggregate(p5social.Posts.AggregateSelect(count, titleCount, titleMin)))
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := golem.AggregateValue(aggregate, count).Get(); !ok || value != 4 {
				t.Fatalf("aggregate count=%d present=%t", value, ok)
			}
			if value, ok := golem.AggregateValue(aggregate, titleCount).Get(); !ok || value != 4 {
				t.Fatalf("aggregate title count=%d present=%t", value, ok)
			}
			if value, ok := golem.AggregateValue(aggregate, titleMin).Get(); !ok || value != "a-open" {
				t.Fatalf("aggregate title min=%q present=%t", value, ok)
			}
			assertPinnedGuardSQL(t, harness, "aggregate")

			groups, err := caller.Posts.GroupBy(ctx, p5social.Posts.GroupBy(
				p5social.Posts.GroupDimensions(title), p5social.Posts.GroupMeasures(count),
				p5social.Posts.GroupOrderBy(titleMin.Asc()), p5social.Posts.GroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			wantGroups := []struct {
				title string
				count int64
			}{{"a-open", 2}, {"b-hidden", 1}, {"c-open", 1}}
			if len(groups) != len(wantGroups) {
				t.Fatalf("groups=%d want %d", len(groups), len(wantGroups))
			}
			for index, want := range wantGroups {
				gotTitle, titleOK := golem.GroupValue(groups[index], title).Get()
				gotCount, countOK := golem.GroupValue(groups[index], count).Get()
				if !titleOK || !countOK || gotTitle != want.title || int64(gotCount) != want.count {
					t.Fatalf("group %d title=%q/%t count=%d/%t want %+v", index, gotTitle, titleOK, gotCount, countOK, want)
				}
			}
			assertPinnedGuardSQL(t, harness, "groupBy")

			relationGroups, err := caller.Posts.RelationGroupBy(ctx, p5social.Posts.RelationGroupBy(
				p5social.Posts.RelationGroupDimensions(authorName), p5social.Posts.RelationGroupMeasures(count),
				p5social.Posts.RelationGroupWhere(p5social.Posts.AuthorID.Eq(principal.UserID)),
				p5social.Posts.RelationGroupOrderBy(authorName.Asc()), p5social.Posts.RelationGroupTake(100),
			))
			if err != nil {
				t.Fatal(err)
			}
			if len(relationGroups) != 1 {
				t.Fatalf("relation groups=%d", len(relationGroups))
			}
			if name, ok := golem.RelationGroupValue(relationGroups[0], authorName).Get(); !ok || name != "owner" {
				t.Fatalf("relation group author=%q present=%t", name, ok)
			}
			if value, ok := golem.RelationGroupValue(relationGroups[0], count).Get(); !ok || value != 3 {
				t.Fatalf("relation group count=%d present=%t", value, ok)
			}
			assertPinnedGuardSQL(t, harness, "relationGroupBy")
		})
	}
}

func TestReadsSelectingTheKeyAreByteIdentical(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			harness := newP5SocialGeneratedHarness(t, profile)
			caller, err := harness.app.ForPrincipal(ctx, p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
			if err != nil {
				t.Fatal(err)
			}
			harness.trace.reset()
			posts, err := caller.Posts.FindMany(ctx, p5social.Posts.OrderBy(p5social.Posts.Title.Asc(), p5social.Posts.ID.Asc()), p5social.Posts.Take(10),
				p5social.Posts.Select(p5social.Posts.ID, p5social.Posts.Title))
			assertPinnedGuardSQL(t, harness, "keyRead")
			if err != nil {
				t.Fatal(err)
			}
			wantPosts := []struct {
				id    string
				title string
			}{{p5SocialID(10), "a-open"}, {p5SocialID(13), "a-open"}, {p5SocialID(11), "b-hidden"}, {p5SocialID(12), "c-open"}}
			if len(posts) != len(wantPosts) {
				t.Fatalf("posts=%d want %d", len(posts), len(wantPosts))
			}
			for index, want := range wantPosts {
				id, idOK := golem.Value(posts[index], p5social.Posts.ID).Get()
				postTitle, titleOK := golem.Value(posts[index], p5social.Posts.Title).Get()
				if !idOK || !titleOK || id.String() != want.id || postTitle != want.title {
					t.Fatalf("post %d id=%v/%t title=%q/%t want %+v", index, id, idOK, postTitle, titleOK, want)
				}
			}

			users, err := caller.Users.FindMany(ctx, p5social.Users.OrderBy(p5social.Users.ID.Asc()), p5social.Users.Take(10),
				p5social.Users.Select(p5social.Users.ID, p5social.Users.Posts.Args(p5social.Posts.OrderBy(p5social.Posts.ID.Asc()), p5social.Posts.Take(5), p5social.Posts.Select(p5social.Posts.ID, p5social.Posts.Title))))
			assertPinnedGuardSQL(t, harness, "nestedKeyRead")
			if err != nil {
				t.Fatal(err)
			}
			if len(users) != 3 {
				t.Fatalf("users=%d", len(users))
			}
			for index, wantChildren := range []int{3, -1, -1} {
				children, present := golem.Many(users[index], p5social.Users.Posts.ToMany).Get()
				if wantChildren < 0 && present || wantChildren >= 0 && (!present || len(children) != wantChildren) {
					t.Fatalf("user %d children=%d present=%t want %d (-1 is masked)", index, len(children), present, wantChildren)
				}
			}
		})
	}
}
