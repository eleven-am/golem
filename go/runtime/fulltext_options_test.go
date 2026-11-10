package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func textSearchIDs(t *testing.T, results []golem.FullTextResult[mutationResultPost], fixture mutationResultFixture) string {
	t.Helper()
	ids := make([]string, len(results))
	for position, result := range results {
		id, _ := golem.Value(result.Row(), fixture.postID).Get()
		ids[position] = fmt.Sprint(id[15])
	}
	return strings.Join(ids, ",")
}

func createOptionPosts(t *testing.T, fixture mutationResultFixture, posts []struct {
	id    byte
	title string
}) {
	t.Helper()
	for _, post := range posts {
		if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: 1}, post.title)); err != nil {
			t.Fatal(err)
		}
	}
}

var fullTextOptionsIndexes = []fulltextcontract.Index{{Name: "content", Folding: fulltextcontract.FoldingNone}}

func TestTextSearchOrderByUsesPortableCollationOnEveryProvider(t *testing.T) {
	runFullTextNormalizationProfiles(t, fullTextOptionsIndexes, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		createOptionPosts(t, fixture, []struct {
			id    byte
			title string
		}{{81, "apple fruit"}, {82, "Banana fruit"}, {83, "cherry fruit"}})
		ascending, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.title.Asc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, ascending, fixture); got != "82,81,83" {
			t.Fatalf("%s ascending ids=%s want 82,81,83", profile.name, got)
		}
		descending, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.title.Desc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, descending, fixture); got != "83,81,82" {
			t.Fatalf("%s descending ids=%s want 83,81,82", profile.name, got)
		}
	})
}

func TestTextSearchOrderByAppliesBeforeTake(t *testing.T) {
	runFullTextNormalizationProfiles(t, fullTextOptionsIndexes, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		createOptionPosts(t, fixture, []struct {
			id    byte
			title string
		}{{91, "fruit a"}, {92, "fruit b"}, {93, "fruit c"}, {94, "fruit d"}, {95, "fruit e"}})
		byScore, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "fruit", 2, golem.TextSearchOptions[mutationResultPost]{})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, byScore, fixture); got != "91,92" {
			t.Fatalf("%s unordered ids=%s want 91,92", profile.name, got)
		}
		ordered, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "fruit", 2, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.title.Desc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, ordered, fixture); got != "95,94" {
			t.Fatalf("%s ordered ids=%s want 95,94", profile.name, got)
		}
	})
}

func TestTextSearchAllTermsRequiresEveryTerm(t *testing.T) {
	runFullTextNormalizationProfiles(t, fullTextOptionsIndexes, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		createOptionPosts(t, fixture, []struct {
			id    byte
			title string
		}{{71, "alpha beta"}, {72, "alpha"}, {73, "beta gamma"}, {74, "alpha alpha"}})
		anyTerms, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "alpha beta", 10, golem.TextSearchOptions[mutationResultPost]{})
		if err != nil {
			t.Fatal(err)
		}
		if len(anyTerms) != 4 {
			t.Fatalf("%s any-term rows=%d want 4", profile.name, len(anyTerms))
		}
		allTerms, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "alpha beta", 10, golem.TextSearchOptions[mutationResultPost]{AllTerms: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, allTerms, fixture); got != "71" {
			t.Fatalf("%s all-term ids=%s want 71", profile.name, got)
		}
		prefixed, err := SystemTextSearchWith(context.Background(), fixture.app.System(), fixture.postDescriptor, "content", "alpha bet*", 10, golem.TextSearchOptions[mutationResultPost]{AllTerms: true, OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.title.Asc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, prefixed, fixture); got != "71" {
			t.Fatalf("%s all-term prefix ids=%s want 71", profile.name, got)
		}
	})
}

func TestTextSearchOrdersNullsFirstAscendingAndLastDescending(t *testing.T) {
	runFullTextProfiles(t, fullTextOptionsIndexes, schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		system := fixture.app.System()
		for _, post := range []struct {
			id     byte
			author *golem.UUID
		}{{61, &golem.UUID{15: 2}}, {62, nil}, {63, &golem.UUID{15: 1}}} {
			input := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: post.id}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "fruit"),
			)
			if post.author != nil {
				input = golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: post.id}),
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, *post.author),
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "fruit"),
				)
			}
			if _, err := SystemCreate(context.Background(), system, fixture.postDescriptor, input); err != nil {
				t.Fatal(err)
			}
		}
		ascending, err := SystemTextSearchWith(context.Background(), system, fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.authorID.Asc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, ascending, fixture); got != "62,63,61" {
			t.Fatalf("%s ascending ids=%s want 62,63,61", profile.name, got)
		}
		descending, err := SystemTextSearchWith(context.Background(), system, fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.authorID.Desc()}})
		if err != nil {
			t.Fatal(err)
		}
		if got := textSearchIDs(t, descending, fixture); got != "61,63,62" {
			t.Fatalf("%s descending ids=%s want 61,63,62", profile.name, got)
		}
	})
}

func fullTextCallerWithConditionalAuthor(t *testing.T, fixture mutationResultFixture, allowed golem.UUID) *Caller[mutationResultPrincipal, mutationResultActor] {
	t.Helper()
	caller := mustMutationResultCaller(t, fixture)
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CannotReadFields(golem.All[mutationResultPost](), fixture.authorID)
		rules.CanReadFields(fixture.postID.Eq(allowed), fixture.authorID)
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(
		fixture.schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	caller.policies, err = policyruntime.Build(policyruntime.BuildRequest[mutationResultActor]{
		Bindings: bindings, Actor: mutationResultActor{}, Registry: fixture.app.registry,
		Provider: fixture.app.provider, Capabilities: fixture.app.capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func TestTextSearchRefusesConditionalOrderFieldLikeFindMany(t *testing.T) {
	runFullTextNormalizationProfiles(t, fullTextOptionsIndexes, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		createOptionPosts(t, fixture, []struct {
			id    byte
			title string
		}{{51, "fruit one"}, {52, "fruit two"}})
		ctx := context.Background()
		allowed := golem.UUID{15: 51}
		caller := fullTextCallerWithConditionalAuthor(t, fixture, allowed)
		order := golem.OrderBy(fixture.authorID.Asc())
		_, findErr := CallerFindMany(ctx, caller, fixture.postDescriptor, order, golem.Select[mutationResultPost](fixture.postID))
		var expected *golem.Error
		if !errors.As(findErr, &expected) {
			t.Fatalf("%s findMany error=%v", profile.name, findErr)
		}
		_, searchErr := CallerTextSearchWith(ctx, caller, fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.authorID.Asc()}, Select: golem.Select[mutationResultPost](fixture.postID)})
		var got *golem.Error
		if !errors.As(searchErr, &got) {
			t.Fatalf("%s text search error=%v", profile.name, searchErr)
		}
		if got.Code != expected.Code || got.Field != expected.Field {
			t.Fatalf("%s text search error code=%s field=%x, findMany code=%s field=%x", profile.name, got.Code, got.Field, expected.Code, expected.Field)
		}
		discharged, err := CallerTextSearchWith(ctx, caller, fixture.postDescriptor, "content", "fruit", 10, golem.TextSearchOptions[mutationResultPost]{
			Where:   []golem.Predicate[mutationResultPost]{fixture.postID.Eq(allowed)},
			OrderBy: []golem.OrderTerm[mutationResultPost]{fixture.authorID.Asc()},
			Select:  golem.Select[mutationResultPost](fixture.postID),
		})
		if err != nil {
			t.Fatalf("%s discharged order: %v", profile.name, err)
		}
		if got := textSearchIDs(t, discharged, fixture); got != "51" {
			t.Fatalf("%s discharged ids=%s want 51", profile.name, got)
		}
	})
}
