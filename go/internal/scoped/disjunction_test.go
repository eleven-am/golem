package scoped

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestScopedRootWhereKeepsDisjunctivePredicateUnderPolicy(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Where(golem.OrScoped(title.Eq("visible"), title.Eq("hidden"))).Select(title))
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{policyir.ModelID(fixture.Post): alternativeStringReadPolicy(t, fixture.Post, fixture.PostTitle, "visible", "also-visible")}
	planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			proof, err := scopedProof(provider, fixture)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := Render(planned, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			sql := statement.SQL()
			whereAt := strings.Index(sql, " WHERE ")
			if whereAt < 0 {
				t.Fatalf("scoped statement has no WHERE: %s", sql)
			}
			assertScopedDisjunctionsNested(t, sql[whereAt+len(" WHERE "):], 1)
		})
	}
}

func TestScopedDisjunctiveRootPolicyStaysUnderJoinedPredicate(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	author := golem.InnerJoin(posts, golem.GeneratedToOne[scopedSQLPost, scopedSQLUser](fixture.PostAuthor, fixture.Authorship, fixture.User))
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	name := golem.GeneratedScopedTextField(author, golem.GeneratedTextField[scopedSQLUser, string](fixture.UserName))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Join(author).Where(name.Eq("writer")).Select(title))
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{
		policyir.ModelID(fixture.Post): alternativeStringReadPolicy(t, fixture.Post, fixture.PostTitle, "visible", "also-visible"),
		policyir.ModelID(fixture.User): readPolicy(t, fixture.User, true),
	}
	planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			proof, err := scopedProof(provider, fixture)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := Render(planned, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			sql := statement.SQL()
			whereAt := strings.Index(sql, " WHERE ")
			if whereAt < 0 {
				t.Fatalf("scoped statement has no WHERE: %s", sql)
			}
			assertScopedDisjunctionsNested(t, sql[whereAt+len(" WHERE "):], 1)
		})
	}
}

func TestScopedUndischargedFieldIsAnAuthorizationRefusal(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	policies := policyMap{policyir.ModelID(fixture.Post): conditionalFieldByStringReadPolicy(t, fixture.Post, fixture.PostTitle, fixture.PostTitle, "visible")}
	for name, query := range map[string]golem.ScopedQuery[scopedSQLPost]{
		"where":    golem.From(posts).Where(title.Contains("x")).Select(posts.Count()),
		"group-by": golem.From(posts).GroupBy(title).Select(title),
	} {
		t.Run(name, func(t *testing.T) {
			frozen, err := golem.RuntimeFreezeScopedQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
			var failure *AuthorizationError
			if !errors.As(err, &failure) || failure.Field != fixture.PostTitle {
				t.Fatalf("undischarged conditional field error=%v (%T)", err, err)
			}
		})
	}
}

func TestScopedCountMeasureHavingBindsAnInteger(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	titles := title.Count()
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).GroupBy(title).Having(titles.GT(int64(1))).Select(title, titles))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, _ := scopedProof(provider, fixture)
		statement, err := Render(planned, fixture.Registry, provider, proof)
		if err != nil {
			t.Fatalf("%v: count measure having: %v", provider, err)
		}
		args := statement.Args()
		if len(args) == 0 || args[len(args)-1] != int64(1) {
			t.Fatalf("%v: count measure operand args=%#v", provider, args)
		}
		if strings.Contains(statement.SQL(), `COUNT("golem_s0"."title") COLLATE`) {
			t.Fatalf("%v: integer count measure was compared under a text collation: %s", provider, statement.SQL())
		}
	}
}

func assertScopedDisjunctionsNested(t *testing.T, fragment string, minimum int) {
	t.Helper()
	depth, found := 0, false
	for index := 0; index < len(fragment); index++ {
		switch fragment[index] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if strings.HasPrefix(fragment[index:], " OR ") {
			found = true
			if depth < minimum {
				t.Fatalf("disjunction escaped its WHERE operand at depth %d (want >= %d): %s", depth, minimum, fragment)
			}
		}
	}
	if !found {
		t.Fatalf("fragment has no disjunction to check: %s", fragment)
	}
}
