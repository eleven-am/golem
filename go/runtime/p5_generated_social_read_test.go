package runtime_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	providerapi "github.com/eleven-am/golem/go/provider"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

const (
	p5SocialPostgreSQLNamespace       physical.PhysicalName = "golem_p5_generated_social"
	p5SocialPostgreSQLSystemNamespace physical.PhysicalName = "golem_p5_generated_social_system"
)

var p5SocialPostgreSQLLock sync.Mutex

type p5SocialPrincipalKey struct{}

type p5SocialGeneratedHarness struct {
	profile     p5ExtensionProviderProfile
	database    *sqlx.DB
	handle      *providerapi.Database
	trace       *p5ExtensionSQLTrace
	server      *p5social.GraphQLServer
	app         *p5social.App[p5social.Principal]
	resolutions *atomic.Int64
	audits      *p5SocialAuditSink
}

type p5SocialAuditSink struct {
	mu      sync.Mutex
	records []golem.ScopedAuditRecord
}

func (sink *p5SocialAuditSink) report(_ context.Context, record golem.ScopedAuditRecord) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.records = append(sink.records, record)
}

func (sink *p5SocialAuditSink) snapshot() []golem.ScopedAuditRecord {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]golem.ScopedAuditRecord(nil), sink.records...)
}

func (sink *p5SocialAuditSink) reset() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.records = nil
}

type p5SocialGeneratedResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Path       []any          `json:"path"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func newP5SocialGeneratedHarness(t *testing.T, profile p5ExtensionProviderProfile) *p5SocialGeneratedHarness {
	return newP5SocialGeneratedHarnessWithAnalyticsLimits(t, profile, golemruntime.AnalyticsLimits{})
}

func newP5SocialGeneratedHarnessWithAnalyticsLimits(t *testing.T, profile p5ExtensionProviderProfile, analyticsLimits golemruntime.AnalyticsLimits) *p5SocialGeneratedHarness {
	t.Helper()
	ctx := context.Background()
	trace := &p5ExtensionSQLTrace{}
	var database *sqlx.DB
	var apply func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "generated-social.sqlite")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		_ = bootstrap.Close()
		base := p5ExtensionDriverConnector{driver: registeredDriver, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"}
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: base, trace: trace}), "sqlite")
		apply = sqliteprovider.New().ApplyInitial
	} else {
		p5SocialPostgreSQLLock.Lock()
		t.Cleanup(p5SocialPostgreSQLLock.Unlock)
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
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: stdlib.GetConnector(*configuration), trace: trace}), "pgx")
		p6AcquirePostgreSQLTestLock(t, profile.dsn, 0x5036534f4349414c)
		p5CleanupSocialPostgreSQL(t, database)
		apply = postgresprovider.New().ApplyInitial
		t.Cleanup(func() {
			cleanup, _, err := postgresprovider.New().Open(context.Background(), profile.dsn)
			if err != nil {
				t.Errorf("open social cleanup: %v", err)
				return
			}
			defer cleanup.Close()
			p5CleanupSocialPostgreSQL(t, cleanup)
		})
	}
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	t.Cleanup(func() { _ = database.Close() })
	var encoded []byte
	for _, document := range p5social.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if profile.provider == golem.PostgreSQL && (schema.Namespace.Name != p5SocialPostgreSQLNamespace || schema.System.Namespace.Name != p5SocialPostgreSQLSystemNamespace) {
		t.Fatalf("social provider namespaces=%q/%q", schema.Namespace.Name, schema.System.Namespace.Name)
	}
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	databaseHandle := p8AdoptTracedProviderHandle(database, profile)
	resolutions := &atomic.Int64{}
	audits := &p5SocialAuditSink{}
	app, err := p5social.Open(ctx, p5social.Config[p5social.Principal]{
		Database:          databaseHandle,
		AnalyticsLimits:   analyticsLimits,
		AuditPrincipal:    func(principal p5social.Principal) string { return principal.UserID.String() },
		ReportScopedQuery: audits.report,
		ResolvePrincipal: func(_ context.Context, principal p5social.Principal) (p5social.Actor, error) {
			resolutions.Add(1)
			if !principal.Valid {
				return p5social.Actor{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "graphql", p5social.GolemGeneratedUserDescriptor.Metadata().ModelID(), golem.FieldID{}, "invalid principal", nil)
			}
			return p5social.Actor{UserID: principal.UserID, AllowedTag: "tag-a"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.GraphQL(p5social.GraphQLConfig[p5social.Principal]{
		Limits: p5social.GraphQLLimits{MaxDepth: 32, MaxSelectedFields: 2048, MaxAliases: 1024, MaxComplexity: 100000},
		PrincipalFromContext: func(ctx context.Context) (p5social.Principal, bool) {
			principal, ok := ctx.Value(p5SocialPrincipalKey{}).(p5social.Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Logf("generated social GraphQL internal: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &p5SocialGeneratedHarness{profile: profile, database: database, handle: databaseHandle, trace: trace, server: server, app: app, resolutions: resolutions, audits: audits}
	harness.seed(t)
	trace.reset()
	p5social.ResetPolicyProbe()
	return harness
}

func p6AcquirePostgreSQLTestLock(t *testing.T, dsn string, key int64) {
	t.Helper()
	lockDB, _, err := postgresprovider.New().Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL test lock database: %v", err)
	}
	connection, err := lockDB.Connx(context.Background())
	if err != nil {
		_ = lockDB.Close()
		t.Fatalf("reserve PostgreSQL test lock connection: %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, key); err != nil {
		_ = connection.Close()
		_ = lockDB.Close()
		t.Fatalf("acquire PostgreSQL test lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		_ = connection.Close()
		_ = lockDB.Close()
	})
}

func p5CleanupSocialPostgreSQL(t *testing.T, database *sqlx.DB) {
	t.Helper()
	for _, namespace := range []physical.PhysicalName{p5SocialPostgreSQLNamespace, p5SocialPostgreSQLSystemNamespace} {
		if _, err := database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
			t.Errorf("drop social schema %s: %v", namespace, err)
		}
	}
}

func (h *p5SocialGeneratedHarness) table(name string) string {
	if h.profile.provider == golem.PostgreSQL {
		return `"` + string(p5SocialPostgreSQLNamespace) + `"."` + name + `"`
	}
	return `"` + name + `"`
}

func p5SocialID(value int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", value) }

func (h *p5SocialGeneratedHarness) seed(t *testing.T) {
	t.Helper()
	exec := func(query string, arguments ...any) {
		if _, err := h.database.ExecContext(context.Background(), h.database.Rebind(query), arguments...); err != nil {
			t.Fatalf("seed %s: %v", query, err)
		}
	}
	for _, row := range []struct {
		id   int
		name string
	}{{1, "owner"}, {2, "other"}, {3, "third"}} {
		exec(`INSERT INTO `+h.table("users")+` ("id","name") VALUES (?,?)`, p5SocialID(row.id), row.name)
	}
	for _, row := range []struct {
		id, author int
		title      string
	}{{10, 1, "a-open"}, {11, 1, "b-hidden"}, {12, 2, "c-open"}, {13, 1, "a-open"}} {
		exec(`INSERT INTO `+h.table("posts")+` ("id","author_id","title") VALUES (?,?,?)`, p5SocialID(row.id), p5SocialID(row.author), row.title)
	}
	for _, row := range []struct {
		id, post, author, parent int
		body                     string
	}{{20, 10, 1, 0, "root-open"}, {21, 10, 2, 20, "reply-hidden"}, {22, 11, 1, 0, "closed-hidden"}} {
		var parent any
		if row.parent != 0 {
			parent = p5SocialID(row.parent)
		}
		exec(`INSERT INTO `+h.table("comments")+` ("id","post_id","author_id","parent_id","body") VALUES (?,?,?,?,?)`, p5SocialID(row.id), p5SocialID(row.post), p5SocialID(row.author), parent, row.body)
	}
	exec(`INSERT INTO `+h.table("friendships")+` ("user_id","friend_id") VALUES (?,?)`, p5SocialID(1), p5SocialID(2))
	for _, row := range []struct {
		id   int
		name string
	}{{30, "tag-a"}, {31, "tag-b"}} {
		exec(`INSERT INTO `+h.table("tags")+` ("id","name") VALUES (?,?)`, p5SocialID(row.id), row.name)
	}
	exec(`INSERT INTO `+h.table("post_tags")+` ("post_id","tag_name") VALUES (?,?)`, p5SocialID(10), "tag-a")
	exec(`INSERT INTO `+h.table("post_tags")+` ("post_id","tag_name") VALUES (?,?)`, p5SocialID(11), "tag-b")
}

func (h *p5SocialGeneratedHarness) execute(t *testing.T, principal p5social.Principal, query string, variables map[string]any) p5SocialGeneratedResponse {
	t.Helper()
	response, err := h.executeRaw(context.WithValue(context.Background(), p5SocialPrincipalKey{}, principal), query, variables)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func (h *p5SocialGeneratedHarness) executeRaw(ctx context.Context, query string, variables map[string]any) (p5SocialGeneratedResponse, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return p5SocialGeneratedResponse{}, err
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(recorder, request)
	var response p5SocialGeneratedResponse
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return response, fmt.Errorf("decode status %d: %w", recorder.Code, err)
	}
	return response, nil
}

func p5SocialMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("object=%#v", value)
	}
	return result
}
func p5SocialSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("list=%#v", value)
	}
	return result
}

func TestP5ActiveGeneratedSocialGraphMasksOccurrencesAndCompleteSixModelsAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			response := h.execute(t, p5social.Principal{UserID: golem.UUID{15: 1}, Valid: true}, `query Social($owner: UUID!, $other: UUID!, $withHidden: Boolean!) {
  owner: user(where: {ID: $owner}) {
    id name
    firstPosts: posts(orderBy: [{title: asc}, {id: asc}]) { ...PostView }
    secondPosts: posts(orderBy: [{id: desc}], take: 1) { id title }
    firstCount: _count { posts comments }
    secondCount: _count { posts(where: {title: {endsWith: "-open"}}) }
  }
  other: user(where: {ID: $other}) { id name posts { id } _count { posts } }
  open: post(where: {ID: "00000000-0000-0000-0000-000000000010"}) {
    id title author { id name }
    firstComments: comments(orderBy: [{id: asc}]) { id body replies { id body } _count { replies } }
    secondComments: comments(orderBy: [{id: desc}], take: 1) { id body }
    _count { comments postTags }
  }
  hidden: post(where: {ID: "00000000-0000-0000-0000-000000000011"}) {
    id title author { id } comments { id } postTags { tag { id name } } _count { comments postTags }
  }
  recursive: comment(where: {ID: "00000000-0000-0000-0000-000000000020"}) {
    id body replies { id body replyTo { id body } } firstCount: _count { replies } secondCount: _count { replies(where: {body: {contains: "reply"}}) }
  }
  friendship(where: {UserID_FriendID: {userID: $owner, friendID: $other}}) { userID friendID user { id name } friend { id name } }
  tag(where: {Name: "tag-a"}) { id name postTags { post { id title } tag { id name } } _count { postTags } }
  optional: tag(where: {Name: "tag-b"}) @include(if: $withHidden) { id name }
}
fragment PostView on Post { id title author { id name } comments { id body } _count { comments } }`, map[string]any{
				"owner": p5SocialID(1), "other": p5SocialID(2), "withHidden": true,
			})
			if len(response.Errors) != 0 {
				t.Fatalf("complete social errors=%#v", response.Errors)
			}
			owner := p5SocialMap(t, response.Data["owner"])
			if owner["name"] != "owner" || len(p5SocialSlice(t, owner["firstPosts"])) != 3 || len(p5SocialSlice(t, owner["secondPosts"])) != 1 {
				t.Fatalf("owner=%#v", owner)
			}
			if p5SocialMap(t, owner["firstCount"])["posts"] != json.Number("3") || p5SocialMap(t, owner["secondCount"])["posts"] != json.Number("2") {
				t.Fatalf("owner counts=%#v/%#v", owner["firstCount"], owner["secondCount"])
			}
			other := p5SocialMap(t, response.Data["other"])
			if other["name"] != nil || other["posts"] != nil || p5SocialMap(t, other["_count"])["posts"] != nil {
				t.Fatalf("conditional user masks=%#v", other)
			}
			open := p5SocialMap(t, response.Data["open"])
			if open["author"] == nil || len(p5SocialSlice(t, open["firstComments"])) != 2 || p5SocialMap(t, open["_count"])["comments"] != json.Number("2") {
				t.Fatalf("open post=%#v", open)
			}
			hidden := p5SocialMap(t, response.Data["hidden"])
			if hidden["author"] == nil || hidden["comments"] != nil || hidden["postTags"] != nil || p5SocialMap(t, hidden["_count"])["comments"] != nil {
				t.Fatalf("hidden post masks=%#v", hidden)
			}
			recursive := p5SocialMap(t, response.Data["recursive"])
			if len(p5SocialSlice(t, recursive["replies"])) != 1 || p5SocialMap(t, recursive["firstCount"])["replies"] != json.Number("1") {
				t.Fatalf("recursive=%#v", recursive)
			}
			if response.Data["friendship"] == nil || response.Data["tag"] == nil || response.Data["optional"] != nil {
				t.Fatalf("six-model/hidden unique response=%#v", response.Data)
			}
			probe := p5social.PolicyProbe()
			sort.Strings(probe)
			if !reflect.DeepEqual(probe, []string{"Comment", "Friendship", "Post", "PostTag", "Tag", "User"}) {
				t.Fatalf("policy trace=%v", probe)
			}
			if h.resolutions.Load() != 1 {
				t.Fatalf("principal resolutions=%d", h.resolutions.Load())
			}
		})
	}
}

func TestP5ActiveGeneratedSocialPositionsPagingSelectorsAndZeroSQLAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{UserID: golem.UUID{15: 1}, Valid: true}
			response := h.execute(t, principal, `query Positions($cursor: UUID!) {
  defaultPage: posts(orderBy: [{id: asc}]) { id title }
  reversePage: posts(orderBy: [{id: asc}], take: -2) { id }
  classified: posts(
    where: {AND: [{title: {contains: "open"}}, {author: {is: {id: {in: ["00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"]}}}}]}
    orderBy: [{title: asc}, {id: asc}]
    skip: 0
    take: 3
  ) { id title author { id } _count { comments(where: {body: {contains: "root"}}) } }
  cursorPage: posts(orderBy: [{id: asc}], cursor: {ID: $cursor}, take: 3) { id }
  distinctPage: posts(orderBy: [{title: asc}, {id: asc}], distinct: [title], take: 10) { id title }
  tagByUnique: tag(where: {Name: "tag-a"}) { id name }
  postTagByCompound: postTag(where: {PostID_TagName: {postID: "00000000-0000-0000-0000-000000000010", tagName: "tag-a"}}) { postID tagName }
}`, map[string]any{"cursor": p5SocialID(10)})
			if len(response.Errors) != 0 {
				t.Fatalf("positions errors=%#v", response.Errors)
			}
			if len(p5SocialSlice(t, response.Data["defaultPage"])) != 4 || len(p5SocialSlice(t, response.Data["reversePage"])) != 2 || response.Data["tagByUnique"] == nil || response.Data["postTagByCompound"] == nil {
				t.Fatalf("positions/paging=%#v", response.Data)
			}

			invalidPositions := map[string]string{
				"where":    `query { posts(where: {title: {equals: 1}}) { id } }`,
				"order":    `query { posts(orderBy: [{author: asc}]) { id } }`,
				"cursor":   `query { posts(cursor: {}) { id } }`,
				"distinct": `query { posts(distinct: [author]) { id } }`,
				"relation": `query { posts(where: {author: {is: {unknown: {equals: "x"}}}}) { id } }`,
				"count":    `query { posts { id _count { comments(where: {body: {equals: 1}}) } } }`,
			}
			for name, query := range invalidPositions {
				h.trace.reset()
				before := h.resolutions.Load()
				invalid, err := h.executeRaw(context.WithValue(context.Background(), p5SocialPrincipalKey{}, principal), query, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(invalid.Errors) == 0 {
					t.Fatalf("invalid %s position accepted=%#v", name, invalid.Data)
				}
				if statements := h.trace.snapshot(); len(statements) != 0 {
					t.Fatalf("invalid %s position issued SQL=%v", name, statements)
				}
				if h.resolutions.Load() != before {
					t.Fatalf("invalid %s position resolved principal", name)
				}
			}
		})
	}
}

func TestP5ActiveGeneratedSocialGraphQLMatchesGeneratedGoCallerAndInvisibleUnique(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{UserID: golem.UUID{15: 1}, Valid: true}
			graphqlResponse := h.execute(t, principal, `query Parity { posts(orderBy: [{title: asc}, {id: asc}], take: 10) { id title } other: user(where: {ID: "00000000-0000-0000-0000-000000000002"}) { id name } hidden: tag(where: {Name: "tag-b"}) { id name } missing: tag(where: {Name: "absent"}) { id name } }`, nil)
			if len(graphqlResponse.Errors) != 0 {
				t.Fatalf("GraphQL parity errors=%#v", graphqlResponse.Errors)
			}
			graphqlPolicy := p5social.PolicyProbe()
			sort.Strings(graphqlPolicy)
			p5social.ResetPolicyProbe()
			caller, err := h.app.ForPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := caller.Posts.FindMany(context.Background(), p5social.Posts.OrderBy(p5social.Posts.Title.Asc(), p5social.Posts.ID.Asc()), p5social.Posts.Take(10), p5social.Posts.Select(p5social.Posts.ID, p5social.Posts.Title))
			if err != nil {
				t.Fatal(err)
			}
			goPolicy := p5social.PolicyProbe()
			sort.Strings(goPolicy)
			if !reflect.DeepEqual(graphqlPolicy, goPolicy) {
				t.Fatalf("GraphQL/Go policy trace differs graphql=%v go=%v", graphqlPolicy, goPolicy)
			}
			goValues := make([]string, len(rows))
			for index, row := range rows {
				id, _ := golem.Value(row, p5social.Posts.ID).Get()
				title, _ := golem.Value(row, p5social.Posts.Title).Get()
				goValues[index] = title + ":" + id.String()
			}
			graphqlValues := make([]string, 0, len(rows))
			for _, value := range p5SocialSlice(t, graphqlResponse.Data["posts"]) {
				row := p5SocialMap(t, value)
				graphqlValues = append(graphqlValues, row["title"].(string)+":"+row["id"].(string))
			}
			if !reflect.DeepEqual(graphqlValues, goValues) {
				t.Fatalf("GraphQL/Go order differs graphql=%v go=%v", graphqlValues, goValues)
			}
			other, err := caller.Users.FindUnique(context.Background(), p5social.Users.ByID.Value(golem.UUID{15: 2}), p5social.Users.Select(p5social.Users.ID, p5social.Users.Name))
			if err != nil {
				t.Fatal(err)
			}
			if p5SocialMap(t, graphqlResponse.Data["other"])["name"] != nil || golem.Value(other, p5social.Users.Name).State() != golem.ReadNull {
				t.Fatalf("GraphQL/Go scalar mask differs graphql=%#v go=%v", graphqlResponse.Data["other"], golem.Value(other, p5social.Users.Name).State())
			}
			for _, name := range []string{"tag-b", "absent"} {
				_, findErr := caller.Tags.FindUnique(context.Background(), p5social.Tags.ByName.Value(name), p5social.Tags.Select(p5social.Tags.ID, p5social.Tags.Name))
				var public *golem.Error
				if !errors.As(findErr, &public) || public.Code != golem.CodeNotFound {
					t.Fatalf("Go caller unique %q error=%v public=%#v", name, findErr, public)
				}
			}
			if graphqlResponse.Data["hidden"] != nil || graphqlResponse.Data["missing"] != nil {
				t.Fatalf("hidden/missing mismatch=%#v sql=%v policy=%v", graphqlResponse.Data, h.trace.snapshot(), graphqlPolicy)
			}
		})
	}
}
