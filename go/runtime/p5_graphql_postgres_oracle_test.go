package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/jmoiron/sqlx"
)

type p5PostgresGraphQLHarness struct {
	database  *sqlx.DB
	server    *publicgraphql.Server[oraclePrincipal]
	namespace physical.PhysicalName
}

func newP5PostgresGraphQLHarness(t *testing.T, dsn, profile string, indexed bool) p5PostgresGraphQLHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	if indexed {
		fixture = schematest.NewIndexed(t)
	}
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p5_graphql_%s_%d", profile, time.Now().UnixNano()))
	physicalSchema := fixture.PostgreSQL
	physicalSchema.Namespace.Name = namespace
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	})
	if err := provider.ApplyInitial(ctx, database, physicalSchema); err != nil {
		t.Fatal(err)
	}
	bundle, compilation := p5GraphQLBundle(t, postgresRuntimeBundle(t, fixture, physicalSchema))

	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		if actor.UserPrefix == "" {
			rules.CanRead(golem.All[oracleUser]())
		} else {
			rules.CanRead(userName.StartsWith(actor.UserPrefix))
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
		} else {
			rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		}
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	application, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{
		Bundle: bundle,
		BeginCaller: func(ctx context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
			return application.ForPrincipal(ctx, principal)
		},
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("unexpected internal GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{
		PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("unexpected GraphQL server error: %v", err) },
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return p5PostgresGraphQLHarness{database: database, server: server, namespace: namespace}
}

func (h p5PostgresGraphQLHarness) seed(t *testing.T) {
	t.Helper()
	users := [][2]string{
		{"00000000-0000-0000-0000-000000000001", "Z-open"},
		{"00000000-0000-0000-0000-000000000002", "a-closed"},
		{"00000000-0000-0000-0000-000000000003", "é-open"},
		{"00000000-0000-0000-0000-000000000004", "É-closed"},
	}
	posts := [][3]string{
		{"00000000-0000-0000-0000-000000000011", users[0][0], "allowed-z-open"},
		{"00000000-0000-0000-0000-000000000012", users[0][0], "denied"},
		{"00000000-0000-0000-0000-000000000013", users[1][0], "allowed-a-closed"},
		{"00000000-0000-0000-0000-000000000014", users[2][0], "allowed-e-open"},
		{"00000000-0000-0000-0000-000000000015", users[3][0], "allowed-accent-closed"},
	}
	usersTable := `"` + string(h.namespace) + `"."users"`
	postsTable := `"` + string(h.namespace) + `"."posts"`
	for _, row := range users {
		if _, err := h.database.ExecContext(context.Background(), `INSERT INTO `+usersTable+`("id","name") VALUES ($1,$2)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range posts {
		if _, err := h.database.ExecContext(context.Background(), `INSERT INTO `+postsTable+`("id","author_id","title") VALUES ($1,$2,$3)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndependentSocialGraphQLOraclePostgreSQLProfiles(t *testing.T) {
	profiles := []struct{ name, environment string }{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.environment))
			if dsn == "" {
				t.Skip(profile.environment + " is not configured")
			}
			for _, indexed := range []bool{false, true} {
				strategy := "batched"
				if indexed {
					strategy = "correlated"
				}
				t.Run(strategy, func(t *testing.T) {
					harness := newP5PostgresGraphQLHarness(t, dsn, profile.name+"_"+strategy, indexed)
					harness.seed(t)
					principal := oraclePrincipal{PostPrefix: "allowed-"}
					response := harness.server.Execute(context.Background(), principal, publicgraphql.Request{Query: `query {
  users(orderBy: [{name: asc}], take: 10) {
    id name
    posts(orderBy: [{id: asc}], take: 1) { id title }
  }
}`})
					if len(response.Errors) != 0 {
						t.Fatalf("GraphQL errors: %#v", response.Errors)
					}
					want := []any{
						map[string]any{"id": "00000000-0000-0000-0000-000000000001", "name": "Z-open", "posts": []any{map[string]any{"id": "00000000-0000-0000-0000-000000000011", "title": "allowed-z-open"}}},
						map[string]any{"id": "00000000-0000-0000-0000-000000000002", "name": "a-closed", "posts": []any{map[string]any{"id": "00000000-0000-0000-0000-000000000013", "title": "allowed-a-closed"}}},
						map[string]any{"id": "00000000-0000-0000-0000-000000000003", "name": "é-open", "posts": []any{map[string]any{"id": "00000000-0000-0000-0000-000000000014", "title": "allowed-e-open"}}},
						map[string]any{"id": "00000000-0000-0000-0000-000000000004", "name": "É-closed", "posts": []any{map[string]any{"id": "00000000-0000-0000-0000-000000000015", "title": "allowed-accent-closed"}}},
					}
					data := response.Data.(map[string]any)
					if !reflect.DeepEqual(data["users"], want) {
						gotJSON, _ := json.Marshal(data["users"])
						wantJSON, _ := json.Marshal(want)
						t.Fatalf("portable GraphQL oracle mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
					}

					hidden := harness.server.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: `{ post(where: {id: "00000000-0000-0000-0000-000000000012"}) { id } }`})
					missing := harness.server.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: `{ post(where: {id: "00000000-0000-0000-0000-000000000099"}) { id } }`})
					if len(hidden.Errors) != 0 || len(missing.Errors) != 0 || !reflect.DeepEqual(hidden.Data, missing.Data) || hidden.Data.(map[string]any)["post"] != nil {
						t.Fatalf("hidden/missing disclosure differs: hidden=%#v missing=%#v", hidden, missing)
					}
				})
			}
		})
	}
}
