package runtime_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func scopedPostTitles() (golem.ScopedQuery[p5social.Post], golem.ScopedResult[string]) {
	posts := p5social.Posts.Scope()
	title := p5social.Posts.Title.At(posts)
	return golem.From(posts).Where(title.EndsWith("-open")).Select(title).OrderBy(title.Asc()), title
}

func TestP6ScopedSystemAndTransactionParity(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := h.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			query, title := scopedPostTitles()
			rows, err := caller.Posts.Scoped(context.Background(), query)
			if err != nil || len(rows) != 3 {
				t.Fatalf("caller rows=%d err=%v", len(rows), err)
			}
			if value, ok := golem.ScopedValue(rows[0], title).Get(); !ok || value != "a-open" {
				t.Fatalf("first=%q/%v", value, ok)
			}
			systemRows, err := h.app.System().Posts.Scoped(context.Background(), query)
			if err != nil || len(systemRows) != len(rows) {
				t.Fatalf("system rows=%d err=%v", len(systemRows), err)
			}
			if err := caller.Transaction(context.Background(), func(tx *p5social.CallerTx[p5social.Principal]) error {
				got, txErr := tx.Posts.Scoped(context.Background(), query)
				if txErr != nil || len(got) != len(rows) {
					t.Fatalf("caller tx rows=%d err=%v", len(got), txErr)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.app.System().Transaction(context.Background(), func(tx *p5social.SystemTx[p5social.Principal]) error {
				got, txErr := tx.Posts.Scoped(context.Background(), query)
				if txErr != nil || len(got) != len(rows) {
					t.Fatalf("system tx rows=%d err=%v", len(got), txErr)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if records := h.audits.snapshot(); len(records) != 4 {
				t.Fatalf("audit records=%d", len(records))
			}
		})
	}
}

func TestP6ScopedLeftJoinMissingAndInvisibleTargetAreIndistinguishable(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			caller, err := h.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
			if err != nil {
				t.Fatal(err)
			}
			posts := p5social.Posts.Scope()
			postTags := golem.LeftJoin(posts, p5social.Posts.PostTags)
			tags := golem.LeftJoin(postTags, p5social.PostTags.Tag)
			title := p5social.Posts.Title.At(posts)
			tagName := p5social.Tags.Name.At(tags)
			rows, err := caller.Posts.Scoped(context.Background(), golem.From(posts).Join(tags).Select(title, tagName).OrderBy(title.Asc()))
			if err != nil {
				t.Fatal(err)
			}
			states := map[string][]golem.ReadState{}
			for _, row := range rows {
				name, ok := golem.ScopedValue(row, title).Get()
				if !ok {
					t.Fatal("post title unexpectedly hidden")
				}
				states[name] = append(states[name], golem.ScopedValue(row, tagName).State())
			}
			if len(states["a-open"]) != 2 || states["a-open"][0] != golem.ReadPresent || states["a-open"][1] != golem.ReadNull {
				t.Fatalf("visible/missing states=%v", states["a-open"])
			}
			if len(states["b-hidden"]) != 1 || states["b-hidden"][0] != golem.ReadNull {
				t.Fatalf("policy-invisible state=%v", states["b-hidden"])
			}
			if len(states["c-open"]) != 1 || states["c-open"][0] != golem.ReadNull {
				t.Fatalf("missing state=%v", states["c-open"])
			}
			sql := h.trace.snapshot()
			if len(sql) != 1 || !strings.Contains(sql[0], " LEFT JOIN ") {
				t.Fatalf("left join statements=%#v", sql)
			}
		})
	}
}

func TestP6ScopedToManyJoinCountsAuthorizedPairsWithoutImplicitDeduplication(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			caller, err := h.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
			if err != nil {
				t.Fatal(err)
			}
			posts := p5social.Posts.Scope()
			comments := golem.InnerJoin(posts, p5social.Posts.Comments)
			pairs := posts.Count()
			h.trace.reset()
			rows, err := caller.Posts.Scoped(context.Background(), golem.From(posts).Join(comments).Select(pairs))
			if err != nil || len(rows) != 1 {
				t.Fatalf("pair rows=%d err=%v", len(rows), err)
			}
			if value, ok := golem.ScopedValue(rows[0], pairs).Get(); !ok || value != 3 {
				t.Fatalf("joined pair count=%d/%v", value, ok)
			}
			if statements := h.trace.snapshot(); len(statements) != 1 || strings.Contains(strings.ToUpper(statements[0]), "DISTINCT") {
				t.Fatalf("pair SQL=%#v", statements)
			}
		})
	}
}

func TestP6ScopedRuntimeForgeryAndMixedRootCorpusTouchesDatabaseZeroTimes(t *testing.T) {
	h := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	caller, err := h.app.ForPrincipal(context.Background(), p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}})
	if err != nil {
		t.Fatal(err)
	}
	first, second := p5social.Posts.Scope(), p5social.Posts.Scope()
	foreignTitle := p5social.Posts.Title.At(second)
	h.trace.reset()
	if rows, err := caller.Posts.Scoped(context.Background(), golem.From(first).Select(foreignTitle)); err == nil || rows != nil {
		t.Fatalf("mixed scope rows=%d err=%v", len(rows), err)
	}
	if statements := h.trace.snapshot(); len(statements) != 0 {
		t.Fatalf("forged query touched SQL: %#v", statements)
	}
}

func TestP6ScopedAuditStartupRequirements(t *testing.T) {
	h := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	_, err := p5social.Open(context.Background(), p5social.Config[p5social.Principal]{
		Database:         h.handle,
		ResolvePrincipal: func(context.Context, p5social.Principal) (p5social.Actor, error) { return p5social.Actor{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "AuditPrincipal") {
		t.Fatalf("missing audit callbacks error=%v", err)
	}
}

func TestP6ScopedAuditSuccessFailureCancellationAndTx(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := h.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			query, _ := scopedPostTitles()
			h.audits.reset()
			if _, err := caller.Posts.Scoped(context.Background(), query); err != nil {
				t.Fatal(err)
			}
			posts := p5social.Posts.Scope()
			title := p5social.Posts.Title.At(posts)
			if _, err := caller.Posts.Scoped(context.Background(), golem.From(posts).Select(title).Take(0)); err == nil {
				t.Fatal("zero take was accepted")
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := caller.Posts.Scoped(cancelled, query); err == nil {
				t.Fatal("cancelled scoped query succeeded")
			}
			if err := caller.Transaction(context.Background(), func(tx *p5social.CallerTx[p5social.Principal]) error {
				_, inner := tx.Posts.Scoped(context.Background(), query)
				return inner
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.database.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := caller.Posts.Scoped(context.Background(), query); err == nil {
				t.Fatal("closed database scoped query succeeded")
			}
			records := h.audits.snapshot()
			if len(records) != 5 {
				t.Fatalf("audit outcomes=%d", len(records))
			}
			want := []golem.ScopedOutcome{golem.ScopedOutcomeSucceeded, golem.ScopedOutcomeRefused, golem.ScopedOutcomeCancelled, golem.ScopedOutcomeSucceeded, golem.ScopedOutcomeFailed}
			for i := range want {
				if records[i].Outcome() != want[i] {
					t.Fatalf("outcome[%d]=%v want %v", i, records[i].Outcome(), want[i])
				}
			}
			for _, record := range records {
				if record.PrincipalAuditID() != principal.UserID.String() || record.ExecutionID() == 0 {
					t.Fatalf("audit identity=%q/%d", record.PrincipalAuditID(), record.ExecutionID())
				}
			}
		})
	}
}

func TestP6ConcurrentAuditPrincipalIsolation(t *testing.T) {
	h := newP5SocialGeneratedHarness(t, p5ExtensionProviderProfiles()[0])
	h.audits.reset()
	query, _ := scopedPostTitles()
	principals := []p5social.Principal{{Valid: true, UserID: golem.UUID{15: 1}}, {Valid: true, UserID: golem.UUID{15: 2}}}
	var wait sync.WaitGroup
	for _, principal := range principals {
		principal := principal
		wait.Add(1)
		go func() {
			defer wait.Done()
			caller, err := h.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := caller.Posts.Scoped(context.Background(), query); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	records := h.audits.snapshot()
	if len(records) != 2 {
		t.Fatalf("records=%d", len(records))
	}
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.PrincipalAuditID()] = true
	}
	for _, principal := range principals {
		if !seen[principal.UserID.String()] {
			t.Fatalf("missing principal %s", principal.UserID)
		}
	}
}

func TestP6ScopedLimitAndCancellationCorpus(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			cases := []struct {
				name       string
				limits     golemruntime.AnalyticsLimits
				statements int
				query      func() golem.ScopedQuery[p5social.Post]
			}{
				{"shape", golemruntime.AnalyticsLimits{MaxScopedSelections: 1}, 0, func() golem.ScopedQuery[p5social.Post] {
					posts := p5social.Posts.Scope()
					return golem.From(posts).Select(p5social.Posts.ID.At(posts), p5social.Posts.Title.At(posts))
				}},
				{"contribution", golemruntime.AnalyticsLimits{MaxContributionRows: 2}, 1, func() golem.ScopedQuery[p5social.Post] { query, _ := scopedPostTitles(); return query }},
				{"contribution-hidden-by-skip", golemruntime.AnalyticsLimits{MaxContributionRows: 2}, 1, func() golem.ScopedQuery[p5social.Post] {
					posts := p5social.Posts.Scope()
					title := p5social.Posts.Title.At(posts)
					return golem.From(posts).Select(title).OrderBy(title.Asc()).Skip(100)
				}},
				{"contribution-hidden-by-ungrouped-having", golemruntime.AnalyticsLimits{MaxContributionRows: 2}, 1, func() golem.ScopedQuery[p5social.Post] {
					posts := p5social.Posts.Scope()
					count := posts.Count()
					return golem.From(posts).Having(count.GT(int64(100))).Select(count)
				}},
				{"intermediate", golemruntime.AnalyticsLimits{MaxContributionRows: 100, MaxIntermediateGroups: 2}, 1, func() golem.ScopedQuery[p5social.Post] {
					posts := p5social.Posts.Scope()
					title := p5social.Posts.Title.At(posts)
					count := posts.Count()
					return golem.From(posts).GroupBy(title).Select(title, count).OrderBy(title.Asc())
				}},
				{"result", golemruntime.AnalyticsLimits{MaxContributionRows: 100, MaxIntermediateGroups: 100, MaxProgrammaticGroups: 2}, 1, func() golem.ScopedQuery[p5social.Post] {
					posts := p5social.Posts.Scope()
					title := p5social.Posts.Title.At(posts)
					return golem.From(posts).Select(title).OrderBy(title.Asc())
				}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					h := newP5SocialGeneratedHarnessWithAnalyticsLimits(t, profile, test.limits)
					caller, err := h.app.ForPrincipal(context.Background(), principal)
					if err != nil {
						t.Fatal(err)
					}
					h.trace.reset()
					rows, err := caller.Posts.Scoped(context.Background(), test.query())
					if err == nil || rows != nil {
						t.Fatalf("limit returned rows=%d err=%v", len(rows), err)
					}
					if got := len(h.trace.snapshot()); got != test.statements {
						t.Fatalf("limit statements=%d err=%v", got, err)
					}
				})
			}
		})
	}
}
