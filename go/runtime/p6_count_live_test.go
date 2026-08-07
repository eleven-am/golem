package runtime_test

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func TestP6CountAndAggregateCountAuthorizedScopeOracle(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			actor := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			caller, err := harness.app.ForPrincipal(context.Background(), actor)
			if err != nil {
				t.Fatal(err)
			}

			ordinary, err := caller.Posts.Count(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			count := p5social.Posts.CountAll()
			harness.trace.reset()
			result, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(
				p5social.Posts.AggregateSelect(count),
			))
			if err != nil {
				t.Fatal(err)
			}
			analytical, ok := golem.AggregateValue(result, count).Get()
			if !ok || analytical != ordinary || analytical != 4 {
				t.Fatalf("ordinary=%d aggregate=%d present=%t", ordinary, analytical, ok)
			}
			if statements := harness.trace.snapshot(); len(statements) != 1 {
				t.Fatalf("aggregate statement count=%d", len(statements))
			}
		})
	}
}

func TestP6CountFieldClassifiesNullDistributionButCountAllDoesNot(t *testing.T) {
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

			all := p5social.Comments.CountAll()
			parentIDs := p5social.Comments.ParentID.Count()
			result, err := caller.Comments.Aggregate(context.Background(), p5social.Comments.Aggregate(
				p5social.Comments.AggregateSelect(all, parentIDs),
			))
			if err != nil {
				t.Fatal(err)
			}
			allValue, allPresent := golem.AggregateValue(result, all).Get()
			parentValue, parentPresent := golem.AggregateValue(result, parentIDs).Get()
			if !allPresent || !parentPresent || allValue != 3 || parentValue != 1 {
				t.Fatalf("count-all=%d/%t parent-count=%d/%t", allValue, allPresent, parentValue, parentPresent)
			}

			// User.Name is conditional. Counting rows is legal without proving that
			// field, while counting Name must classify it and refuse before SQL.
			userCount := p5social.Users.CountAll()
			if _, err := caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
				p5social.Users.AggregateSelect(userCount),
			)); err != nil {
				t.Fatalf("count-all improperly required conditional field access: %v", err)
			}
			harness.trace.reset()
			if _, err := caller.Users.Aggregate(context.Background(), p5social.Users.Aggregate(
				p5social.Users.AggregateSelect(p5social.Users.Name.Count()),
			)); err == nil {
				t.Fatal("conditional field-count succeeded without a selecting proof")
			}
			if statements := harness.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("refused field-count touched the database: %#v", statements)
			}
		})
	}
}

func TestP6CountMissingInvisibleAndSystemStances(t *testing.T) {
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

			count := p5social.Tags.CountAll()
			callerResult, err := caller.Tags.Aggregate(context.Background(), p5social.Tags.Aggregate(
				p5social.Tags.AggregateSelect(count),
			))
			if err != nil {
				t.Fatal(err)
			}
			systemResult, err := harness.app.System().Tags.Aggregate(context.Background(), p5social.Tags.Aggregate(
				p5social.Tags.AggregateSelect(count),
			))
			if err != nil {
				t.Fatal(err)
			}
			callerCount, callerPresent := golem.AggregateValue(callerResult, count).Get()
			systemCount, systemPresent := golem.AggregateValue(systemResult, count).Get()
			if !callerPresent || !systemPresent || callerCount != 1 || systemCount != 2 {
				t.Fatalf("caller=%d/%t system=%d/%t", callerCount, callerPresent, systemCount, systemPresent)
			}

			postCount := p5social.Posts.CountAll()
			missing, err := caller.Posts.Aggregate(context.Background(), p5social.Posts.Aggregate(
				p5social.Posts.AggregateWhere(p5social.Posts.Title.Eq("does-not-exist")),
				p5social.Posts.AggregateSelect(postCount),
			))
			if err != nil {
				t.Fatal(err)
			}
			missingCount, present := golem.AggregateValue(missing, postCount).Get()
			if !present || missingCount != 0 {
				t.Fatalf("missing count=%d/%t", missingCount, present)
			}

			harness.trace.reset()
			if _, err := harness.app.ForPrincipal(context.Background(), p5social.Principal{}); err == nil {
				t.Fatal("invalid principal resolved a caller")
			}
			if statements := harness.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("invalid principal touched database: %#v", statements)
			}
		})
	}
}
