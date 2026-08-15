package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type p5ReadProviderProfile struct {
	name     string
	provider golem.Provider
	dsn      string
	env      string
}

func p5ReadProviderProfiles() []p5ReadProviderProfile {
	return []p5ReadProviderProfile{
		{name: "sqlite", provider: golem.SQLite},
		{name: "postgresql-c", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN")), env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "postgresql-linguistic", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN")), env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
}

type p5ReadProviderHarness struct {
	app              *App[oraclePrincipal, oracleActor]
	server           *publicgraphql.Server[oraclePrincipal]
	disclosureServer *publicgraphql.Server[oraclePrincipal]
	fixture          schematest.Fixture
	trace            *p5SQLTrace
	begins           atomic.Int64
	captured         []golem.FrozenReadRequest
}

type p5ReadProviderExecution struct {
	caller  *Caller[oraclePrincipal, oracleActor]
	harness *p5ReadProviderHarness
}

func (execution p5ReadProviderExecution) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	execution.harness.captured = append(execution.harness.captured, request)
	return execution.caller.ExecuteFrozenRead(ctx, request)
}

func TestGraphQLReadMasksPositionsOccurrencesAndSQLAcrossProviders(t *testing.T) {
	for _, profile := range p5ReadProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			harness := newP5ReadProviderHarness(t, profile)
			harness.seed(t)
			t.Run("conditional-masks-stay-local", func(t *testing.T) {
				assertP5ConditionalMasksLocal(t, harness)
			})
			t.Run("positions-aliases-fragments-and-directives", func(t *testing.T) {
				assertP5ReadPositionsAndOccurrences(t, harness)
			})
			t.Run("refused-inputs-issue-zero-sql", func(t *testing.T) {
				assertP5ReadRefusalsIssueZeroSQL(t, harness)
			})
			t.Run("hidden-and-missing-unique-match", func(t *testing.T) {
				assertP5ReadHiddenMissing(t, harness)
			})
		})
	}
}

func newP5ReadProviderHarness(t *testing.T, profile p5ReadProviderProfile) *p5ReadProviderHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.NewIndexed(t)
	trace := &p5SQLTrace{}
	var database *sqlx.DB
	var bundle golem.SchemaBundle
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "p5-read-provider.db")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		if err := bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		dsn := plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
		base := p5DriverConnector{driver: registeredDriver, dsn: dsn}
		database = sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "sqlite")
		database.SetMaxOpenConns(4)
		database.SetMaxIdleConns(4)
		if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		bundle = fixture.Bundle
	} else {
		configuration, err := pgx.ParseConfig(profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		if configuration.RuntimeParams == nil {
			configuration.RuntimeParams = map[string]string{}
		}
		configuration.RuntimeParams["timezone"] = "UTC"
		configuration.RuntimeParams["datestyle"] = "ISO, YMD"
		configuration.RuntimeParams["intervalstyle"] = "iso_8601"
		configuration.RuntimeParams["standard_conforming_strings"] = "on"
		base := stdlib.GetConnector(*configuration)
		database = sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "pgx")
		suffix := fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
		applicationNamespace := physical.PhysicalName("golem_p5_read_" + suffix)
		systemNamespace := physical.PhysicalName("golem_p5_read_system_" + suffix)
		physicalSchema := fixture.PostgreSQL
		physicalSchema.Namespace.Name = applicationNamespace
		physicalSchema.System.Namespace.Name = systemNamespace
		t.Cleanup(func() {
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		})
		if err := postgresprovider.New().ApplyInitial(ctx, database, physicalSchema); err != nil {
			t.Fatal(err)
		}
		bundle = postgresRuntimeBundle(t, fixture, physicalSchema)
	}
	t.Cleanup(func() { _ = database.Close() })
	bundle, compilation := p5GraphQLBundle(t, bundle)

	userIdentity := golem.GeneratedIdentityMetadata(fixture.User, fixture.UserKey, golem.PrimaryIdentity, fixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(fixture.Post, fixture.PostKey, golem.PrimaryIdentity, fixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(fixture.User, fixture.Post, fixture.UserPosts, fixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(fixture.Post, fixture.User, fixture.PostAuthor, fixture.Authorship, golem.RelationSource, golem.RelationToOne)
	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	userPosts := golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postID := golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	postAuthor := golem.GeneratedToOne[oraclePost, oracleUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		if actor.UserPrefix == "" {
			rules.CanRead(golem.All[oracleUser]())
		} else {
			rules.CanRead(userName.StartsWith(actor.UserPrefix))
		}
		rules.CanReadFields(golem.All[oracleUser](), userID)
		rules.CannotReadFields(golem.All[oracleUser](), userName, userPosts)
		rules.CanReadFields(userName.EndsWith("-open"), userName, userPosts)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
			rules.CanReadFields(golem.All[oraclePost](), postID, postTitle)
		} else {
			reach := postTitle.StartsWith(actor.PostPrefix)
			rules.CanRead(reach)
			rules.CanReadFields(reach, postID, postTitle)
		}
		rules.CannotReadFields(golem.All[oraclePost](), postAuthor)
		rules.CanReadFields(postTitle.EndsWith("-open"), postAuthor)
		return rules.Freeze(fixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, profile.provider), Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	harness := &p5ReadProviderHarness{app: app, fixture: fixture, trace: trace}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{
		Bundle: bundle,
		BeginCaller: func(operationContext context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
			harness.begins.Add(1)
			caller, callerErr := app.ForPrincipal(operationContext, principal)
			if callerErr != nil {
				return nil, callerErr
			}
			return p5ReadProviderExecution{caller: caller, harness: harness}, nil
		},
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected provider GraphQL executor error: %v", report)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{
		PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected provider GraphQL server error: %v", report)
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	harness.server = server
	disclosureUserBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		rules.CanRead(golem.All[oracleUser]())
		return rules.Freeze(fixture.User)
	})
	disclosurePostBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
		} else {
			rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		}
		return rules.Freeze(fixture.Post)
	})
	disclosureBindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{disclosureUserBinding, disclosurePostBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	disclosureApp, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, profile.provider), Bundle: bundle, Bindings: disclosureBindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	disclosureExecutor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{
		Bundle: bundle,
		BeginCaller: func(operationContext context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
			return disclosureApp.ForPrincipal(operationContext, principal)
		},
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected disclosure GraphQL executor error: %v", report)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.disclosureServer, err = publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{
		PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected disclosure GraphQL server error: %v", report)
		},
	}, disclosureExecutor)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func (h *p5ReadProviderHarness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	users := nestedAcceptanceTable(h.app, h.fixture.User)
	posts := nestedAcceptanceTable(h.app, h.fixture.Post)
	for _, row := range [][2]string{{mutationResultUUIDText(1), "alpha-open"}, {mutationResultUUIDText(2), "beta-closed"}, {mutationResultUUIDText(3), "gamma-open"}} {
		query := h.app.database.Rebind(`INSERT INTO ` + users + `("id","name") VALUES (?,?)`)
		if _, err := h.app.database.ExecContext(ctx, query, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{mutationResultUUIDText(11), mutationResultUUIDText(1), "one-open"},
		{mutationResultUUIDText(12), mutationResultUUIDText(1), "two-closed"},
		{mutationResultUUIDText(13), mutationResultUUIDText(2), "three-open"},
		{mutationResultUUIDText(14), mutationResultUUIDText(3), "four-closed"},
	} {
		query := h.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title") VALUES (?,?,?)`)
		if _, err := h.app.database.ExecContext(ctx, query, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	h.trace.reset()
	h.captured = nil
	h.begins.Store(0)
}

func assertP5ConditionalMasksLocal(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	h.captured = nil
	response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query {
  users(orderBy: [{id: asc}], take: 3) {
    id name
    posts(orderBy: [{id: asc}], take: 4) { id title author { id name } }
    _count { posts }
  }
}`})
	if len(response.Errors) != 0 {
		t.Fatalf("conditional-mask GraphQL errors=%#v", response.Errors)
	}
	want := []any{
		map[string]any{"id": mutationResultUUIDText(1), "name": "alpha-open", "posts": []any{
			map[string]any{"id": mutationResultUUIDText(11), "title": "one-open", "author": map[string]any{"id": mutationResultUUIDText(1), "name": "alpha-open"}},
			map[string]any{"id": mutationResultUUIDText(12), "title": "two-closed", "author": nil},
		}, "_count": map[string]any{"posts": int32(2)}},
		map[string]any{"id": mutationResultUUIDText(2), "name": nil, "posts": nil, "_count": map[string]any{"posts": nil}},
		map[string]any{"id": mutationResultUUIDText(3), "name": "gamma-open", "posts": []any{
			map[string]any{"id": mutationResultUUIDText(14), "title": "four-closed", "author": nil},
		}, "_count": map[string]any{"posts": int32(1)}},
	}
	got := response.Data.(map[string]any)["users"]
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("conditional masks escaped occurrence\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
	if len(h.trace.snapshot()) == 0 {
		t.Fatal("accepted conditional-mask query issued no provider SQL")
	}
}

func assertP5ReadPositionsAndOccurrences(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	h.captured = nil
	response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query Occurrences($showName: Boolean!) {
  user(where: { id: "00000000-0000-0000-0000-000000000001" }) {
    id name
    ...UserNameAgain @include(if: $showName)
    openPosts: posts(where: { title: { endsWith: "-open" } }, orderBy: [{title: asc}], take: 1) { ...PostBits }
    closedPosts: posts(where: { title: { contains: "closed" } }, orderBy: [{title: desc}], take: -1) { id title }
    _count {
      opens: posts(where: { title: { endsWith: "-open" } })
      closed: posts(where: { title: { contains: "closed" } })
    }
  }
}
fragment UserNameAgain on User { name }
fragment PostBits on Post { id title }
`, Variables: map[string]any{"showName": true}})
	if len(response.Errors) != 0 {
		t.Fatalf("occurrence GraphQL errors=%#v", response.Errors)
	}
	user := response.Data.(map[string]any)["user"].(map[string]any)
	if len(user["openPosts"].([]any)) != 1 || user["openPosts"].([]any)[0].(map[string]any)["title"] != "one-open" || len(user["closedPosts"].([]any)) != 1 || user["closedPosts"].([]any)[0].(map[string]any)["title"] != "two-closed" {
		t.Fatalf("relation aliases collapsed=%#v", user)
	}
	counts := user["_count"].(map[string]any)
	if counts["opens"] != int32(1) || counts["closed"] != int32(1) {
		t.Fatalf("count aliases collapsed=%#v", counts)
	}
	if len(h.captured) != 1 {
		t.Fatalf("occurrence root requests=%d want=1", len(h.captured))
	}
	seenRelations, seenCounts := 0, 0
	occurrences := map[golem.RuntimeOccurrenceID]bool{}
	for _, selection := range h.captured[0].Selection() {
		if !selection.IsRelation() && !selection.IsRelationCount() {
			continue
		}
		if occurrences[selection.OccurrenceID()] || selection.OccurrenceID() == 0 {
			t.Fatalf("relation/count occurrence identity collapsed: %#v", selection)
		}
		occurrences[selection.OccurrenceID()] = true
		if selection.IsRelation() {
			seenRelations++
		} else {
			seenCounts++
		}
	}
	if seenRelations != 2 || seenCounts != 2 {
		t.Fatalf("relation/count occurrence inventory relations=%d counts=%d", seenRelations, seenCounts)
	}

	h.trace.reset()
	h.captured = nil
	positionResponse := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: `query Positions {
  window: users(
    where: { AND: [{ name: { endsWith: "-open" } }, { posts: { some: { title: { startsWith: "one" } } } }] }
    orderBy: [{name: asc}, {id: desc}]
    cursor: { id: "00000000-0000-0000-0000-000000000001" }
    distinct: [name]
    skip: 0
    take: 2
  ) { id }
  reverse: users(orderBy: [{id: asc}], cursor: { id: "00000000-0000-0000-0000-000000000003" }, take: -1) { id }
  defaulted: users(orderBy: [{id: asc}]) { id }
}`})
	if len(positionResponse.Errors) != 0 {
		t.Fatalf("position GraphQL errors=%#v", positionResponse.Errors)
	}
	if len(h.captured) != 3 {
		t.Fatalf("position requests=%d want=3", len(h.captured))
	}
	window := h.captured[0]
	_, where := window.Where()
	_, cursor := window.Cursor()
	take, hasTake := window.Take()
	skip, hasSkip := window.Skip()
	if !where || len(window.OrderBy()) != 2 || !cursor || len(window.Distinct()) != 1 || !hasTake || take != 2 || !hasSkip || skip != 0 {
		t.Fatalf("root query positions not preserved: %#v", window)
	}
	reverseTake, reversePresent := h.captured[1].Take()
	defaultTake, defaultPresent := h.captured[2].Take()
	if !reversePresent || reverseTake != -1 || !defaultPresent || defaultTake != 50 {
		t.Fatalf("signed/default paging reverse=%d/%t default=%d/%t", reverseTake, reversePresent, defaultTake, defaultPresent)
	}
	statements := h.trace.snapshot()
	if len(statements) == 0 || !p5TraceContainsTable(statements, "users") || !p5TraceContainsTable(statements, "posts") {
		t.Fatalf("accepted query positions did not reach provider SQL: %#v", statements)
	}
}

func assertP5ReadRefusalsIssueZeroSQL(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	for _, test := range []struct {
		name, query, code string
		begins            int64
	}{
		{name: "ambiguous-where", query: `{ users(where: { all: true, name: { equals: "forged" } }) { id } }`, code: "BAD_USER_INPUT"},
		{name: "unknown-input", query: `{ users(where: { forged: { equals: "x" } }) { id } }`, code: "GRAPHQL_VALIDATION_FAILED"},
		{name: "conditional-order", query: `{ users(orderBy: [{name: asc}]) { id } }`, code: "FORBIDDEN", begins: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h.trace.reset()
			h.captured = nil
			before := h.begins.Load()
			response := h.server.Execute(context.Background(), oraclePrincipal{}, publicgraphql.Request{Query: test.query})
			if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != test.code {
				t.Fatalf("refusal response=%#v want code=%s", response, test.code)
			}
			if delta := h.begins.Load() - before; delta != test.begins {
				t.Fatalf("refusal caller executions=%d want=%d", delta, test.begins)
			}
			if statements := h.trace.snapshot(); len(statements) != 0 {
				t.Fatalf("refused query issued provider SQL: %#v", statements)
			}
		})
	}
}

func assertP5ReadHiddenMissing(t *testing.T, h *p5ReadProviderHarness) {
	t.Helper()
	h.trace.reset()
	query := `query($id: UUID!) { post(where: {id: $id}) { id title } }`
	invisible := h.disclosureServer.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": mutationResultUUIDText(12)}})
	missing := h.disclosureServer.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": mutationResultUUIDText(99)}})
	if !reflect.DeepEqual(invisible, missing) || len(invisible.Errors) != 0 || !reflect.DeepEqual(invisible.Data, map[string]any{"post": nil}) {
		t.Fatalf("hidden/missing disclosure differs invisible=%#v missing=%#v", invisible, missing)
	}
	if len(h.trace.snapshot()) == 0 {
		t.Fatal("hidden/missing unique checks issued no provider SQL")
	}
}

func p5TraceContainsTable(statements []string, table string) bool {
	needle := `"` + table + `"`
	for _, statement := range statements {
		if strings.Contains(statement, needle) {
			return true
		}
	}
	return false
}
