package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p5socialactive"
	"github.com/jmoiron/sqlx"
)

const (
	p5ActivePostgreSQLNamespace       physical.PhysicalName = "golem_p5_social_active"
	p5ActivePostgreSQLSystemNamespace physical.PhysicalName = "golem_p5_social_active_system"
)

var p5ActivePostgreSQLLock sync.Mutex

type p5ActivePrincipalKey struct{}

type p5ActiveResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

type p5ActiveHarness struct {
	profile  p5ExtensionProviderProfile
	database *sqlx.DB
	app      *p5socialactive.App[p5socialactive.Principal]
	server   *p5socialactive.GraphQLServer
}

func newP5ActiveHarness(t *testing.T, profile p5ExtensionProviderProfile, limits golemruntime.MutationLimits) *p5ActiveHarness {
	t.Helper()
	ctx := context.Background()
	var (
		database *sqlx.DB
		apply    func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	)
	if profile.provider == golem.SQLite {
		var err error
		database, _, err = sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "p5-active-social.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		apply = sqliteprovider.New().ApplyInitial
	} else {
		p5ActivePostgreSQLLock.Lock()
		t.Cleanup(p5ActivePostgreSQLLock.Unlock)
		var err error
		database, _, err = postgresprovider.New().Open(ctx, profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		p5ActiveCleanupPostgreSQL(t, database)
		apply = postgresprovider.New().ApplyInitial
		t.Cleanup(func() { p5ActiveCleanupPostgreSQL(t, database) })
	}
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)

	var encoded []byte
	for _, document := range p5socialactive.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
			break
		}
	}
	if len(encoded) == 0 {
		t.Fatalf("generated fixture has no %s physical schema", profile.provider)
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if profile.provider == golem.PostgreSQL && (schema.Namespace.Name != p5ActivePostgreSQLNamespace || schema.System.Namespace.Name != p5ActivePostgreSQLSystemNamespace) {
		t.Fatalf("active fixture namespaces=%q/%q", schema.Namespace.Name, schema.System.Namespace.Name)
	}
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}

	app, err := p5socialactive.Open(ctx, p5socialactive.Config[p5socialactive.Principal]{
		DB: database, Provider: profile.provider, MutationLimits: limits,
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			t.Errorf("unexpected after-commit hook failure: %v", failure)
		},
		ResolvePrincipal: func(_ context.Context, principal p5socialactive.Principal) (p5socialactive.Actor, error) {
			return principal, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.GraphQL(p5socialactive.GraphQLConfig[p5socialactive.Principal]{
		PrincipalFromContext: func(ctx context.Context) (p5socialactive.Principal, bool) {
			principal, ok := ctx.Value(p5ActivePrincipalKey{}).(p5socialactive.Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Logf("active generated GraphQL internal: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &p5ActiveHarness{profile: profile, database: database, app: app, server: server}
}

func p5ActiveCleanupPostgreSQL(t *testing.T, database *sqlx.DB) {
	t.Helper()
	for _, namespace := range []physical.PhysicalName{p5ActivePostgreSQLNamespace, p5ActivePostgreSQLSystemNamespace} {
		if _, err := database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
			t.Errorf("drop active fixture namespace %s: %v", namespace, err)
		}
	}
}

func (h *p5ActiveHarness) table(name string) string {
	if h.profile.provider == golem.PostgreSQL {
		return `"` + string(p5ActivePostgreSQLNamespace) + `"."` + name + `"`
	}
	return `"` + name + `"`
}

func (h *p5ActiveHarness) outbox() string {
	if h.profile.provider == golem.PostgreSQL {
		return `"` + string(p5ActivePostgreSQLSystemNamespace) + `"."_golem_outbox"`
	}
	return `"_golem_outbox"`
}

func (h *p5ActiveHarness) execute(t *testing.T, principal p5socialactive.Principal, query string) p5ActiveResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), p5ActivePrincipalKey{}, principal)
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(recorder, request)
	var response p5ActiveResponse
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode GraphQL status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return response
}

func (h *p5ActiveHarness) mutate(t *testing.T, query string) map[string]any {
	t.Helper()
	response := h.execute(t, p5socialactive.Principal{}, query)
	if len(response.Errors) != 0 || response.Data == nil {
		t.Fatalf("active generated GraphQL mutation failed: %#v\n%s", response, query)
	}
	return response.Data
}

func (h *p5ActiveHarness) factCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+h.outbox()); err != nil {
		t.Fatal(err)
	}
	return count
}

func p5ActiveID(value int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", value) }

func p5ActiveUUID(t *testing.T, value int) golem.UUID {
	t.Helper()
	parsed, err := golem.ParseUUID(p5ActiveID(value))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func p5ActiveObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL object=%#v", value)
	}
	return result
}

func TestP5ActiveGeneratedMutationSDLHasLegalNestedCardinality(t *testing.T) {
	h := newP5ActiveHarness(t, p5ExtensionProviderProfile{name: "sqlite", provider: golem.SQLite}, golemruntime.MutationLimits{})
	sdl := h.server.SDL()
	toOneStart := strings.Index(sdl, "input PostAuthorUpdateRelationInput {")
	toManyStart := strings.Index(sdl, "input UserPostsUpdateRelationInput {")
	if toOneStart < 0 || toManyStart < 0 {
		t.Fatalf("generated SDL is missing nested update inputs")
	}
	toOne := sdl[toOneStart : strings.Index(sdl[toOneStart:], "}\n")+toOneStart]
	toMany := sdl[toManyStart : strings.Index(sdl[toManyStart:], "}\n")+toManyStart]
	for _, forbidden := range []string{"disconnect:", "set:", "delete:", "deleteMany:", "updateMany:"} {
		if strings.Contains(toOne, forbidden) {
			t.Fatalf("required to-one input illegally exposes %s:\n%s", forbidden, toOne)
		}
	}
	for _, operation := range []string{"create:", "createMany:", "connect:", "connectOrCreate:", "disconnect:", "set:", "update:", "updateMany:", "upsert:", "delete:", "deleteMany:"} {
		if !strings.Contains(toMany, operation) {
			t.Fatalf("to-many input is missing %s:\n%s", operation, toMany)
		}
	}
}

func TestP5ActiveGeneratedMutationScalarCallerParityAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5ActiveHarness(t, profile, golemruntime.MutationLimits{})
			owner := p5ActiveID(1)
			h.mutate(t, `mutation { createUser(data: {id: "`+owner+`", name: "owner"}) { id } }`)
			omitted := p5ActiveObject(t, h.mutate(t, `mutation { createPost(data: {
  id: "`+p5ActiveID(90)+`", title: "omitted-defaults", author: {connect: {ID: "`+owner+`"}}
}) { id counter optional } }`)["createPost"])
			if omitted["counter"] != json.Number("7") || omitted["optional"] != json.Number("11") {
				t.Fatalf("omitted create did not preserve database defaults: %#v", omitted)
			}
			explicit := p5ActiveObject(t, h.mutate(t, `mutation { createPost(data: {
  id: "`+p5ActiveID(91)+`", title: "explicit-zero-null", counter: 0, optional: null,
  author: {connect: {ID: "`+owner+`"}}
}) { id counter optional } }`)["createPost"])
			if explicit["counter"] != json.Number("0") || explicit["optional"] != nil {
				t.Fatalf("explicit zero/null create was conflated with omission: %#v", explicit)
			}
			h.mutate(t, `mutation {
  omitted: deletePost(where: {ID: "`+p5ActiveID(90)+`"}) { id }
  explicit: deletePost(where: {ID: "`+p5ActiveID(91)+`"}) { id }
}`)
			if _, err := h.database.ExecContext(context.Background(), `DELETE FROM `+h.outbox()); err != nil {
				t.Fatal(err)
			}

			p5socialactive.ResetHooks()
			assertP5ActiveGraphQLScalarSequence(t, h, owner, 100)
			graphqlFacts := h.factCount(t)
			graphqlHooks := p5socialactive.SnapshotHooks()
			// P7 event capture is intentionally disabled on this P5 fixture, so
			// P4 must take the same no-fact path for generated GraphQL and the
			// generated Go caller. Hook and persisted-state parity remain active.
			if graphqlFacts != 0 {
				t.Fatalf("subscription-disabled GraphQL durable facts=%d want=0 hooks=%+v", graphqlFacts, graphqlHooks)
			}

			if _, err := h.database.ExecContext(context.Background(), `DELETE FROM `+h.outbox()); err != nil {
				t.Fatal(err)
			}
			p5socialactive.ResetHooks()
			assertP5ActiveGeneratedCallerScalarSequence(t, h, 200)
			goFacts := h.factCount(t)
			goHooks := p5socialactive.SnapshotHooks()
			if goFacts != graphqlFacts || !reflect.DeepEqual(goHooks, graphqlHooks) {
				t.Fatalf("generated GraphQL/Go caller parity facts=%d/%d hooks=%+v/%+v", graphqlFacts, goFacts, graphqlHooks, goHooks)
			}
		})
	}
}

func assertP5ActiveGraphQLScalarSequence(t *testing.T, h *p5ActiveHarness, owner string, base int) {
	t.Helper()
	id := func(offset int) string { return p5ActiveID(base + offset) }
	created := p5ActiveObject(t, h.mutate(t, `mutation { createPost(data: {
    id: "`+id(1)+`", title: "graphql-create", counter: 10, optional: 5,
    author: {connect: {ID: "`+owner+`"}}
  }) { id title counter optional } }`)["createPost"])
	if created["counter"] != json.Number("10") || created["optional"] != json.Number("5") {
		t.Fatalf("GraphQL create scalar result=%#v", created)
	}
	updated := p5ActiveObject(t, h.mutate(t, `mutation { updatePost(where: {ID: "`+id(1)+`"}, data: {
    title: {set: "graphql-set"}, counter: {increment: 2}, optional: {setNull: true}
  }) { title counter optional } }`)["updatePost"])
	if updated["title"] != "graphql-set" || updated["counter"] != json.Number("12") || updated["optional"] != nil {
		t.Fatalf("GraphQL set/increment/null result=%#v", updated)
	}
	h.mutate(t, `mutation { updatePost(where: {ID: "`+id(1)+`"}, data: {counter: {decrement: 3}}) { id } }`)
	h.mutate(t, `mutation { upsertPost(where: {ID: "`+id(1)+`"},
    create: {id: "`+id(1)+`", title: "unused", author: {connect: {ID: "`+owner+`"}}},
    update: {title: {set: "graphql-upserted"}}
  ) { id title } }`)
	h.mutate(t, `mutation { upsertPost(where: {ID: "`+id(2)+`"},
    create: {id: "`+id(2)+`", title: "graphql-upsert-create", author: {connect: {ID: "`+owner+`"}}},
    update: {title: {set: "unused"}}
  ) { id title counter optional } }`)
	h.mutate(t, `mutation { deletePost(where: {ID: "`+id(2)+`"}) { id } }`)
	for _, offset := range []int{3, 4} {
		h.mutate(t, `mutation { createPost(data: {id: "`+id(offset)+`", title: "graphql-batch", author: {connect: {ID: "`+owner+`"}}}) { id counter optional } }`)
	}
	batch := h.mutate(t, `mutation {
  updateManyPosts(where: {title: {equals: "graphql-batch"}}, data: {title: {set: "graphql-batched"}}) { count }
  deleteManyPosts(where: {title: {equals: "graphql-batched"}}) { count }
}`)
	if p5ActiveObject(t, batch["updateManyPosts"])["count"] != json.Number("2") || p5ActiveObject(t, batch["deleteManyPosts"])["count"] != json.Number("2") {
		t.Fatalf("GraphQL batch counts=%#v", batch)
	}
	var title string
	var counter int
	var optional *int
	query := h.database.Rebind(`SELECT "title","counter","optional" FROM ` + h.table("posts") + ` WHERE "id"=?`)
	if err := h.database.QueryRowxContext(context.Background(), query, id(1)).Scan(&title, &counter, &optional); err != nil || title != "graphql-upserted" || counter != 9 || optional != nil {
		t.Fatalf("GraphQL persisted scalar state title=%q counter=%d optional=%v err=%v", title, counter, optional, err)
	}
}

func assertP5ActiveGeneratedCallerScalarSequence(t *testing.T, h *p5ActiveHarness, base int) {
	t.Helper()
	ctx := context.Background()
	caller, err := h.app.ForPrincipal(ctx, p5socialactive.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	owner := p5ActiveUUID(t, 1)
	id := func(offset int) golem.UUID { return p5ActiveUUID(t, base+offset) }
	create := func(offset int, title string) p5socialactive.PostCreateInput {
		return p5socialactive.Posts.Create(
			p5socialactive.Posts.ID.Create(id(offset)), p5socialactive.Posts.Title.Create(title),
			p5socialactive.Posts.Counter.Create(10), p5socialactive.Posts.Optional.Create(5),
			p5socialactive.Posts.Author.Connect(p5socialactive.Users.ByID.Value(owner)),
		)
	}
	if _, err := caller.Posts.Create(ctx, create(1, "go-create")); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Posts.Update(ctx, p5socialactive.Posts.ByID.Value(id(1)), p5socialactive.Posts.Update(
		p5socialactive.Posts.Title.Set("go-set"), p5socialactive.Posts.Counter.Increment(2), p5socialactive.Posts.Optional.Null(),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Posts.Update(ctx, p5socialactive.Posts.ByID.Value(id(1)), p5socialactive.Posts.Update(p5socialactive.Posts.Counter.Decrement(3))); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Posts.Upsert(ctx, p5socialactive.Posts.ByID.Value(id(1)), create(1, "unused"), p5socialactive.Posts.Update(p5socialactive.Posts.Title.Set("go-upserted"))); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Posts.Upsert(ctx, p5socialactive.Posts.ByID.Value(id(2)), create(2, "go-upsert-create"), p5socialactive.Posts.Update(p5socialactive.Posts.Title.Set("unused"))); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Posts.Delete(ctx, p5socialactive.Posts.ByID.Value(id(2))); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{3, 4} {
		if _, err := caller.Posts.Create(ctx, create(offset, "go-batch")); err != nil {
			t.Fatal(err)
		}
	}
	count, err := caller.Posts.UpdateMany(ctx, p5socialactive.Posts.Title.Eq("go-batch"), p5socialactive.Posts.UpdateMany(p5socialactive.Posts.Title.Set("go-batched")))
	if err != nil || count != 2 {
		t.Fatalf("Go caller updateMany count=%d err=%v", count, err)
	}
	count, err = caller.Posts.DeleteMany(ctx, p5socialactive.Posts.Title.Eq("go-batched"))
	if err != nil || count != 2 {
		t.Fatalf("Go caller deleteMany count=%d err=%v", count, err)
	}
}

func TestP5ActiveGeneratedNestedDenialAndLimitRollbackAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			for _, scenario := range []struct {
				name      string
				limits    golemruntime.MutationLimits
				principal p5socialactive.Principal
				code      string
			}{
				{name: "independent-child-denial", principal: p5socialactive.Principal{DenyPostCreate: true}, code: string(golem.CodeForbidden)},
				{name: "touched-graph-overflow", limits: golemruntime.MutationLimits{MaxTouchedRows: 1}, code: string(golem.CodeBadUserInput)},
			} {
				scenario := scenario
				t.Run(scenario.name, func(t *testing.T) {
					h := newP5ActiveHarness(t, profile, scenario.limits)
					owner, nested := p5ActiveID(1), p5ActiveID(2)
					h.mutate(t, `mutation { createUser(data: {id: "`+owner+`", name: "before"}) { id } }`)
					if _, err := h.database.ExecContext(context.Background(), `DELETE FROM `+h.outbox()); err != nil {
						t.Fatal(err)
					}
					p5socialactive.ResetHooks()
					response := h.execute(t, scenario.principal, `mutation { updateUser(where: {ID: "`+owner+`"}, data: {
  name: {set: "must-roll-back"}, posts: {create: [{id: "`+nested+`", title: "must-roll-back"}]}
}) { id name } }`)
					if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != scenario.code {
						t.Fatalf("rollback response=%#v want code=%s", response, scenario.code)
					}
					var name string
					if err := h.database.GetContext(context.Background(), &name, h.database.Rebind(`SELECT "name" FROM `+h.table("users")+` WHERE "id"=?`), owner); err != nil || name != "before" {
						t.Fatalf("parent rollback name=%q err=%v", name, err)
					}
					var posts int
					if err := h.database.GetContext(context.Background(), &posts, h.database.Rebind(`SELECT COUNT(*) FROM `+h.table("posts")+` WHERE "id"=?`), nested); err != nil || posts != 0 {
						t.Fatalf("child rollback count=%d err=%v", posts, err)
					}
					wantHooks := p5socialactive.HookSnapshot{BeforeCreate: 1}
					if hooks, facts := p5socialactive.SnapshotHooks(), h.factCount(t); hooks != wantHooks || facts != 0 {
						t.Fatalf("rollback hook/fact trace=%+v/%d want=%+v/0", hooks, facts, wantHooks)
					}
				})
			}
		})
	}
}

func TestP5ActiveGeneratedCompleteElevenOperationSocialGraphAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			h := newP5ActiveHarness(t, profile, golemruntime.MutationLimits{})
			friend, tagID := p5ActiveID(2), p5ActiveID(30)
			h.mutate(t, `mutation {
  friend: createUser(data: {id: "`+friend+`", name: "friend"}) { id }
  tag: createTag(data: {id: "`+tagID+`", name: "deep-tag"}) { id }
}`)
			p5socialactive.ResetHooks()
			owner, post := p5ActiveID(1), p5ActiveID(10)
			comment, reply := p5ActiveID(20), p5ActiveID(21)
			h.mutate(t, `mutation { createUser(data: {
  id: "`+owner+`", name: "owner"
  posts: {create: [{
    id: "`+post+`", title: "post"
    comments: {create: [{
      id: "`+comment+`", body: "comment", author: {connect: {ID: "`+owner+`"}}
      replies: {create: [{
        id: "`+reply+`", body: "reply"
        post: {connect: {ID: "`+post+`"}}
        author: {connect: {ID: "`+owner+`"}}
      }]}
    }]}
    postTags: {create: [{tag: {connect: {Name: "deep-tag"}}}]}
  }]}
  friendshipsFrom: {create: [{friend: {connect: {ID: "`+friend+`"}}}]}
}) { id name } }`)
			assertP5ActiveCounts(t, h, map[string]int{
				"users": 2, "posts": 1, "comments": 2, "friendships": 1, "tags": 1, "post_tags": 1,
			})
			if hooks := p5socialactive.SnapshotHooks(); hooks != (p5socialactive.HookSnapshot{BeforeCreate: 1, AfterCreate: 1, AfterCommitCreate: 1}) {
				t.Fatalf("deep generated GraphQL hook path=%+v", hooks)
			}

			vocabularyOwner := p5ActiveID(4)
			h.mutate(t, `mutation { createUser(data: {id: "`+vocabularyOwner+`", name: "vocabulary-owner"}) { id } }`)
			for _, seed := range []struct {
				id, title string
			}{{p5ActiveID(53), "connect-before"}, {p5ActiveID(54), "connect-or-create-before"}} {
				h.mutate(t, `mutation { createPost(data: {
  id: "`+seed.id+`", title: "`+seed.title+`", author: {connect: {ID: "`+friend+`"}}
}) { id } }`)
			}

			// Separate requests prove each public nested operation independently
			// enters the same P4 transaction boundary as the root update.
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `create: [{id: "`+p5ActiveID(50)+`", title: "create"}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `createMany: [
  {id: "`+p5ActiveID(51)+`", title: "many-a"},
  {id: "`+p5ActiveID(52)+`", title: "many-b"}
]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `connect: [{ID: "`+p5ActiveID(53)+`"}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `connectOrCreate: [{
  where: {ID: "`+p5ActiveID(54)+`"}, create: {id: "`+p5ActiveID(54)+`", title: "unused"}
}]`))
			h.mutate(t, `mutation { updateComment(where: {ID: "`+comment+`"}, data: {
  replies: {disconnect: [{ID: "`+reply+`"}]}
}) { id body } }`)
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `set: [
  {ID: "`+p5ActiveID(50)+`"}, {ID: "`+p5ActiveID(51)+`"}, {ID: "`+p5ActiveID(52)+`"},
  {ID: "`+p5ActiveID(53)+`"}, {ID: "`+p5ActiveID(54)+`"}
]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `update: [{
  where: {ID: "`+p5ActiveID(50)+`"}, data: {title: {set: "updated"}}
}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `updateMany: [{
  where: {id: {in: ["`+p5ActiveID(51)+`", "`+p5ActiveID(52)+`"]}},
  data: {title: {set: "bulk-updated"}}
}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `upsert: [{
  where: {ID: "`+p5ActiveID(50)+`"},
  create: {id: "`+p5ActiveID(50)+`", title: "unused"},
  update: {title: {set: "upsert-updated"}}
}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `upsert: [{
  where: {ID: "`+p5ActiveID(55)+`"},
  create: {id: "`+p5ActiveID(55)+`", title: "upsert-created"},
  update: {title: {set: "unused"}}
}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `delete: [{ID: "`+p5ActiveID(55)+`"}]`))
			h.mutate(t, p5ActiveUserPostsUpdate(vocabularyOwner, `deleteMany: [{
  id: {in: ["`+p5ActiveID(51)+`", "`+p5ActiveID(52)+`"]}
}]`))

			var title, author string
			query := h.database.Rebind(`SELECT "title","author_id" FROM ` + h.table("posts") + ` WHERE "id"=?`)
			if err := h.database.QueryRowxContext(context.Background(), query, p5ActiveID(50)).Scan(&title, &author); err != nil || title != "upsert-updated" || author != vocabularyOwner {
				t.Fatalf("nested survivor title=%q author=%q err=%v", title, author, err)
			}
			var removed int
			query = h.database.Rebind(`SELECT COUNT(*) FROM ` + h.table("posts") + ` WHERE "id" IN (?, ?, ?)`)
			if err := h.database.GetContext(context.Background(), &removed, query, p5ActiveID(51), p5ActiveID(52), p5ActiveID(55)); err != nil || removed != 0 {
				t.Fatalf("nested deletes remaining=%d err=%v", removed, err)
			}
			var parentID *string
			query = h.database.Rebind(`SELECT "parent_id" FROM ` + h.table("comments") + ` WHERE "id"=?`)
			if err := h.database.GetContext(context.Background(), &parentID, query, reply); err != nil || parentID != nil {
				t.Fatalf("nested disconnect parent=%v err=%v", parentID, err)
			}
		})
	}
}

func p5ActiveUserPostsUpdate(owner, body string) string {
	return `mutation { updateUser(where: {ID: "` + owner + `"}, data: {posts: {` + body + `}}) { id name } }`
}

func assertP5ActiveCounts(t *testing.T, h *p5ActiveHarness, want map[string]int) {
	t.Helper()
	for table, expected := range want {
		var count int
		if err := h.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+h.table(table)); err != nil || count != expected {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, expected, err)
		}
	}
}
