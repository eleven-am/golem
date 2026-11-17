package pipeline

import (
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

// Set GOLEM_GENERATED_CLIENT_ROOT to this checkout's absolute client directory
// (with locked dependencies installed) to exercise its TypeScript page client
// against the same fresh generated application on both providers.
func TestFreshGeneratedContinuationPagesHTTP(t *testing.T) {
	runGeneratedDTOConsumerWithProviders(t, "continuationpages", nil, continuationPagesConsumerTest, compilerir.SQLite, compilerir.PostgreSQL)
}

const continuationPagesConsumerTest = `package acceptance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "example.test/home"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providerpostgres "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type pagePrincipalKey struct{}

type pageFixtureHandler struct {
	http.Handler
	protocol map[string]any
}

func checkContinuationClient(t *testing.T, handler http.Handler) {
	t.Helper()
	clientRoot := os.Getenv("GOLEM_GENERATED_CLIENT_ROOT")
	if clientRoot == "" {
		t.Log("real TypeScript client requires explicit client root")
		return
	}
	if !filepath.IsAbs(clientRoot) {
		t.Fatal("real client requires an absolute client root")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), pagePrincipalKey{}, app.Principal(r.Header.Get("x-golem-profile")))
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	manifest, err := filepath.Abs("zz_golem_client.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(clientRoot, "node_modules/vitest/vitest.mjs"), "run", "--config", "vitest.config.ts")
	command.Dir = filepath.Join(clientRoot, "core")
	command.Env = append(os.Environ(), "GOLEM_GENERATED_HTTP_URL="+server.URL, "GOLEM_GENERATED_HTTP_MANIFEST="+manifest, "GOLEM_GENERATED_CLIENT_FIXTURE=continuation", "NODE_OPTIONS=--max-old-space-size=256", "UV_THREADPOOL_SIZE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real continuation client failed: %v\n%s", err, output)
	}
	t.Log("real TypeScript continuation client passed")
}

func pageHTTP(t *testing.T, handler http.Handler, profile, field, mode, after string, seed int32, scalarOnly bool) map[string]any {
	t.Helper()
	selection := "ordinal tie entry { id title rank }"
	if scalarOnly {
		selection = "ordinal tie"
	}
	if field == "rankedRows" {
		selection = "id title rank"
	}
	query := "query Page($seed:Int!,$mode:String!,$after:String){" + field + "(seed:$seed,mode:$mode,take:25,after:$after){items{" + selection + "} next}}"
	variables := map[string]any{"seed": seed, "mode": mode}
	if after != "" {
		variables["after"] = after
	}
	return pageHTTPQuery(t, handler, profile, query, variables)
}

func pageHTTPQuery(t *testing.T, handler http.Handler, profile, query string, variables map[string]any) map[string]any {
	t.Helper()
	fixture, ok := handler.(pageFixtureHandler)
	if !ok {
		t.Fatal("page requests require the actual generated application's admitted identity")
	}
	return pageHTTPEnvelope(t, handler, profile, map[string]any{"query": query, "variables": variables, "extensions": map[string]any{"golem": fixture.protocol}})
}

func pageHTTPEnvelope(t *testing.T, handler http.Handler, profile string, envelope map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	// An ambient actor is deliberately counterfeit. Admission must replace it
	// before Scope and the resolver, and every HTTP request gets a new Caller.
	ctx := golem.RuntimeContextWithActor(context.Background(), app.Actor{Profile: "forged", Revision: -1})
	req = req.WithContext(context.WithValue(ctx, pagePrincipalKey{}, app.Principal(profile)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func pageData(t *testing.T, response map[string]any, field string) ([]any, string) {
	t.Helper()
	if response["errors"] != nil {
		t.Fatalf("page failed: %#v", response)
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing page data: %#v", response)
	}
	page, ok := data[field].(map[string]any)
	if !ok {
		t.Fatalf("missing page: %#v", response)
	}
	if len(page) != 2 {
		t.Fatalf("page disclosed unexpected metadata: %#v", page)
	}
	items, ok := page["items"].([]any)
	if !ok {
		t.Fatalf("items not a list: %#v", page)
	}
	next, _ := page["next"].(string)
	return items, next
}

func refusedPage(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	failures, ok := response["errors"].([]any)
	if !ok || len(failures) == 0 {
		t.Fatalf("invalid page succeeded: %#v", response)
	}
	if data, ok := response["data"].(map[string]any); ok {
		for _, value := range data {
			if page, ok := value.(map[string]any); ok && page["next"] != nil && page["next"] != "" {
				t.Fatalf("failed page issued token: %#v", response)
			}
		}
	}
	return failures[0].(map[string]any)["extensions"].(map[string]any)
}

func assertOrdinals(t *testing.T, items []any, want []int, scalarOnly bool) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("ordinals count=%d want=%v", len(items), want)
	}
	for index, raw := range items {
		item := raw.(map[string]any)
		ordinal := want[index]
		if item["ordinal"] != float64(ordinal) || item["tie"] != float64((ordinal-1)/5) {
			t.Fatalf("immutable tie/order: item=%#v want=%d", item, ordinal)
		}
		if !scalarOnly {
			entry := item["entry"].(map[string]any)
			if entry["id"] != app.CandidateID(uint32(ordinal)).String() {
				t.Fatalf("wrong candidate row: %#v", item)
			}
		}
	}
}
func ordinals(first, last int, include func(int) bool) []int {
	var result []int
	for n := first; n <= last; n++ {
		if include == nil || include(n) {
			result = append(result, n)
		}
	}
	return result
}

func TestContinuationPages(t *testing.T) {
	ctx := context.Background()
	var database *provider.Database
	var err error
	switch os.Getenv("GOLEM_OBJECT_PROVIDER") {
	case "sqlite":
		database, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	case "postgresql":
		database, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB"), Pool: providerpostgres.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
	default:
		t.Fatal("explicit reviewed provider required")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var revision atomic.Int32
	var admissions, scopes, internalReports atomic.Int64
	open := func(database *provider.Database, age time.Duration) (*app.App[app.Principal], http.Handler) {
		t.Helper()
		application, err := app.Open(ctx, app.Config[app.Principal]{Database: database,
			ResolvePrincipal: func(_ context.Context, principal app.Principal) (app.Actor, error) {
				admissions.Add(1)
				return app.Actor{Profile: string(principal), Revision: revision.Load()}, nil
			},
			Pages: &golemruntime.PageConfig[app.Actor]{ActiveKey: "shared-key", Keys: []golem.ContinuationKey{{ID: "shared-key", Material: [32]byte{1, 2, 3}}}, MaxAge: age,
				Scope: func(ctx context.Context, actor app.Actor) (string, error) {
					scopes.Add(1)
					bound := golem.ActorFrom[app.Actor](ctx)
					if bound != actor || bound.Profile == "forged" {
						return "", fmt.Errorf("scope actor was not pinned")
					}
					return fmt.Sprintf("account-1/%s/%d", actor.Profile, actor.Revision), nil
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		server, err := application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext: func(ctx context.Context) (app.Principal, bool) {
			principal, ok := ctx.Value(pagePrincipalKey{}).(app.Principal)
			return principal, ok
		}, ReportInternalError: func(context.Context, error) { internalReports.Add(1) }})
		if err != nil {
			t.Fatal(err)
		}
		if !application.DataIncarnation().Valid() {
			t.Fatal("generated application did not expose its admitted durable identity")
		}
		return application, pageFixtureHandler{Handler: server.Handler(), protocol: map[string]any{
			"protocol": 1, "contract": server.ContractFingerprint().String(), "manifestVersion": 3,
			"capabilities": []string{"data-incarnation-v1"}, "dataIncarnation": application.DataIncarnation().String(),
		}}
	}
	application, handler := open(database, 10*time.Minute)
	system := application.System()
	for n := 1; n <= 60; n++ {
		_, err := system.Entries.Create(ctx, app.Entries.Create(app.Entries.ID.Create(app.CandidateID(uint32(n))), app.Entries.Ordinal.Create(int32(n)), app.Entries.Rank.Create(int32((n-1)/5)), app.Entries.Even.Create(n%2 == 0), app.Entries.Visible.Create(true), app.Entries.Title.Create(fmt.Sprintf("entry-%02d", n))))
		if err != nil {
			t.Fatal(err)
		}
	}
	witnesses, err := system.Entries.FindMany(ctx, golem.OrderBy(app.Entries.ID.Asc()), golem.Take[app.Entry](60))
	if err != nil || len(witnesses) != 60 {
		t.Fatalf("real candidate identity snapshot: rows=%d err=%v", len(witnesses), err)
	}
	for index, row := range witnesses {
		id, present := golem.Value(row, app.Entries.ID).Get()
		if !present || id != app.CandidateID(uint32(index+1)) {
			t.Fatal("candidate witness does not match immutable UUID order")
		}
	}
	copy(app.CandidateWitnesses[:], witnesses)
	checkContinuationClient(t, handler)
	// Admission is also a write boundary. A missing, malformed or stale stamp
	// must not resolve an actor or create a row; the correctly bound control
	// proves this exact generated mutation can otherwise commit.
	mutationID := app.CandidateID(70)
	mutation := "mutation($data:EntryCreateInput!){createEntry(data:$data){id}}"
	mutationVariables := map[string]any{"data": map[string]any{
		"id": mutationID.String(), "ordinal": 70, "rank": 9, "even": false, "visible": true, "title": "outside-snapshot-mutation",
	}}
	static := handler.(pageFixtureHandler).protocol
	stale := "00000000000000000000000000000001"
	if stale == application.DataIncarnation().String() {
		stale = "00000000000000000000000000000002"
	}
	for _, stamp := range []any{nil, "0000000000000000000000000000000A", stale} {
		block := make(map[string]any, len(static))
		for key, value := range static {
			if key != "dataIncarnation" {
				block[key] = value
			}
		}
		if stamp != nil {
			block["dataIncarnation"] = stamp
		}
		before := admissions.Load()
		refusal := pageHTTPEnvelope(t, handler, "full", map[string]any{"query": mutation, "variables": mutationVariables, "extensions": map[string]any{"golem": block}})
		if refusal["errors"] == nil || refusal["data"] != nil || admissions.Load() != before {
			t.Fatalf("invalid incarnation reached mutation admission: %#v", refusal)
		}
	}
	mutationRows, err := system.Entries.FindMany(ctx, golem.Where(app.Entries.ID.Eq(mutationID)), golem.Take[app.Entry](1))
	if err != nil || len(mutationRows) != 0 {
		t.Fatalf("refused mutation wrote its row: rows=%d error=%v", len(mutationRows), err)
	}
	created := pageHTTPQuery(t, handler, "full", mutation, mutationVariables)
	if created["errors"] != nil || created["data"] == nil {
		t.Fatalf("correctly bound mutation could not commit: %#v", created)
	}
	mutationRows, err = system.Entries.FindMany(ctx, golem.Where(app.Entries.ID.Eq(mutationID)), golem.Take[app.Entry](1))
	if err != nil || len(mutationRows) != 1 {
		t.Fatalf("correctly bound mutation did not persist: rows=%d error=%v", len(mutationRows), err)
	}
	// Full immutable population: ties have UUID tie-breaks, not SQL row order.
	after := ""
	seen := map[int]bool{}
	for index, wantCount := range []int{25, 25, 10} {
		before := admissions.Load()
		items, next := pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "normal", after, 7, false), "rankedFeed")
		if admissions.Load() != before+1 {
			t.Fatal("HTTP request reused principal admission")
		}
		first := index*25 + 1
		last := min(first+wantCount-1, 60)
		assertOrdinals(t, items, ordinals(first, last, nil), false)
		for _, raw := range items {
			n := int(raw.(map[string]any)["ordinal"].(float64))
			if seen[n] {
				t.Fatalf("duplicated candidate %d", n)
			}
			seen[n] = true
		}
		if (index < 2) != (next != "") {
			t.Fatalf("terminal state at page %d: %q", index, next)
		}
		after = next
	}
	if len(seen) != 60 {
		t.Fatalf("candidate coverage=%d", len(seen))
	}
	if app.ReadHookCalls.Load() == 0 {
		t.Fatal("continuation fixture skipped its typed before-read hook")
	}
	for _, profile := range []string{"even", "tail", "none"} {
		after = ""
		for index := 0; index < 3; index++ {
			scalarOnly := profile != "even"
			items, next := pageData(t, pageHTTP(t, handler, profile, "rankedFeed", "normal", after, 7, scalarOnly), "rankedFeed")
			first, last := index*25+1, min((index+1)*25, 60)
			want := ordinals(first, last, func(n int) bool { return profile == "even" && n%2 == 0 || profile == "tail" && n > 25 })
			assertOrdinals(t, items, want, scalarOnly)
			if (index < 2) != (next != "") {
				t.Fatalf("withheld candidates lost progress profile=%s page=%d", profile, index)
			}
			after = next
		}
	}
	if app.LargestCandidateWindow.Load() != 25 {
		t.Fatalf("candidate budget not applied: %d", app.LargestCandidateWindow.Load())
	}
	// Refill may inspect more candidates than take, but never more than the
	// declared budget. The next checkpoint follows consumed candidates rather
	// than the number of visible items or the whole preloaded window.
	for _, profile := range []string{"full", "even", "none"} {
		after = ""
		last, total := 0, 0
		for page := 0; ; page++ {
			if page >= 3 {
				t.Fatal("bounded refill did not terminate")
			}
			items, next := pageData(t, pageHTTP(t, handler, profile, "rankedFeed", "refill", after, 7, true), "rankedFeed")
			if len(items) > 25 || profile == "even" && page == 0 && len(items) != 25 || profile == "none" && len(items) != 0 {
				t.Fatalf("bounded refill profile=%s page=%d items=%d", profile, page, len(items))
			}
			for _, raw := range items {
				n := int(raw.(map[string]any)["ordinal"].(float64))
				step := 1
				if profile == "even" {
					step = 2
				}
				if n != last+step {
					t.Fatalf("refill skipped or duplicated candidate: %d after %d", n, last)
				}
				last, total = n, total+1
			}
			if next == "" {
				break
			}
			after = next
		}
		want := 60
		if profile == "even" {
			want = 30
		}
		if profile == "none" {
			want = 0
		}
		if total != want {
			t.Fatalf("refill profile=%s coverage=%d want=%d", profile, total, want)
		}
	}
	if app.LargestCandidateWindow.Load() != 50 {
		t.Fatalf("refill did not exercise its candidate budget: %d", app.LargestCandidateWindow.Load())
	}
	rows, _ := pageData(t, pageHTTP(t, handler, "even", "rankedRows", "normal", "", 7, false), "rankedRows")
	if len(rows) != 12 {
		t.Fatalf("direct-model authorization: %#v", rows)
	}
	for index, raw := range rows {
		if raw.(map[string]any)["id"] != app.CandidateID(uint32((index+1)*2)).String() {
			t.Fatalf("direct-model order: %#v", rows)
		}
	}
	// Private identity is not a public selector/projection capability. These
	// model results use only genuine sealed System-read identity witnesses;
	// ordinary GraphQL hydration must reauthorize and replace their old cells.
	if _, err := system.Entries.Update(ctx, app.Entries.ByID.Value(app.CandidateID(1)), app.Entries.Update(app.Entries.Title.Set("fresh-private-id-title"))); err != nil {
		t.Fatal(err)
	}
	privateQuery := "{identityRows(seed:7,mode:\"normal\",take:25){items{title} next}}"
	privateItems, privateNext := pageData(t, pageHTTPQuery(t, handler, "privateID", privateQuery, nil), "identityRows")
	if len(privateItems) != 25 || privateNext == "" {
		t.Fatalf("title-only model page requires hidden ID: items=%d next=%q", len(privateItems), privateNext)
	}
	for index, raw := range privateItems {
		item := raw.(map[string]any)
		expected := fmt.Sprintf("entry-%02d", index+1)
		if index == 0 {
			expected = "fresh-private-id-title"
		}
		if len(item) != 1 || item["title"] != expected {
			t.Fatalf("hidden ID leaked or witness cells bypassed fresh hydration: %#v", item)
		}
	}
	oldTitle, present := golem.Value(app.CandidateWitnesses[0], app.Entries.Title).Get()
	if !present || oldTitle != "entry-01" {
		t.Fatal("candidate witness was not a genuinely stale row")
	}
	explicitID := pageHTTPQuery(t, handler, "privateID", "{identityRows(seed:7,mode:\"normal\",take:25){items{id title} next}}", nil)
	identityFailure := refusedPage(t, explicitID)
	if identityFailure["code"] != "FORBIDDEN" || identityFailure["reason"] != "FIELD_NOT_READABLE" {
		t.Fatalf("explicit private ID projection did not refuse with field authority: %#v", explicitID)
	}
	deniedWitnesses, deniedNext := pageData(t, pageHTTPQuery(t, handler, "none", privateQuery, nil), "identityRows")
	if len(deniedWitnesses) != 0 || deniedNext == "" {
		t.Fatal("sealed candidate rows bypassed fresh caller row authorization or lost consumption")
	}

	// A retained token is bound to arguments, operation, actor/profile and scope
	// revision. These failures must happen before entering either page resolver.
	_, token := pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "normal", "", 7, false), "rankedFeed")
	bad := func(profile, field, mode, token string, seed int32) {
		t.Helper()
		calls := app.ResolverCalls.Load()
		ext := refusedPage(t, pageHTTP(t, handler, profile, field, mode, token, seed, false))
		if ext["code"] != "BAD_USER_INPUT" || app.ResolverCalls.Load() != calls {
			t.Fatalf("bad continuation reached resolver: %#v", ext)
		}
	}
	bad("full", "rankedFeed", "normal", token+"!", 7)
	bad("full", "rankedFeed", "normal", token, 8)
	bad("even", "rankedFeed", "normal", token, 7)
	bad("full", "rankedRows", "normal", token, 7)
	revision.Add(1)
	bad("full", "rankedFeed", "normal", token, 7)
	revision.Add(-1)
	if scopes.Load() == 0 {
		t.Fatal("scope callback was not exercised")
	}
	for _, query := range []string{
		"{rankedFeed(seed:7,mode:\"normal\",take:51){items{ordinal} next}}",
		"{rankedFeed(seed:7,mode:\"normal\"){totalResults}}",
	} {
		calls := app.ResolverCalls.Load()
		response := pageHTTPQuery(t, handler, "none", query, nil)
		if response["errors"] == nil || app.ResolverCalls.Load() != calls {
			t.Fatalf("invalid limit or denied-total query reached resolver: %#v", response)
		}
	}
	// Tokens are replayable for retries; the immutable candidate interval is
	// identical even though its newly issued opaque token need not be.
	for attempt := 0; attempt < 2; attempt++ {
		items, _ := pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "normal", token, 7, false), "rankedFeed")
		assertOrdinals(t, items, ordinals(26, 50, nil), false)
	}

	// Reauthorization changes only visibility, never the frozen population or
	// ordering. Rank changes and insertions cannot shift ordinal checkpoints.
	if _, err := system.Entries.Update(ctx, app.Entries.ByID.Value(app.CandidateID(26)), app.Entries.Update(app.Entries.Visible.Set(false))); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Entries.Delete(ctx, app.Entries.ByID.Value(app.CandidateID(27))); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Entries.Update(ctx, app.Entries.ByID.Value(app.CandidateID(60)), app.Entries.Update(app.Entries.Rank.Set(-100), app.Entries.Title.Set("changed-after-first-page"))); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Entries.Create(ctx, app.Entries.Create(app.Entries.ID.Create(app.CandidateID(61)), app.Entries.Ordinal.Create(61), app.Entries.Rank.Create(-101), app.Entries.Even.Create(false), app.Entries.Visible.Create(true), app.Entries.Title.Create("outside-snapshot"))); err != nil {
		t.Fatal(err)
	}
	items, next := pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "normal", token, 7, false), "rankedFeed")
	assertOrdinals(t, items, ordinals(28, 50, nil), false)
	items, next = pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "normal", next, 7, false), "rankedFeed")
	assertOrdinals(t, items, ordinals(51, 60, nil), false)
	if next != "" || items[9].(map[string]any)["entry"].(map[string]any)["title"] != "changed-after-first-page" {
		t.Fatal("current hydration or fixed terminal population changed")
	}

	// Result failures cannot expose a continuation. The fixture deliberately
	// violates the checkpoint contract only in the explicit error modes.
	_, stuck := pageData(t, pageHTTP(t, handler, "full", "rankedFeed", "stuck", "", 7, false), "rankedFeed")
	refusedPage(t, pageHTTP(t, handler, "full", "rankedFeed", "stuck", stuck, 7, false))
	refusedPage(t, pageHTTP(t, handler, "full", "rankedFeed", "oversized", "", 7, false))
	if internalReports.Load() == 0 {
		t.Fatal("invalid resolver result did not reach internal reporting")
	}

	// Restarting an application over the same database preserves its identity
	// and admits the same traversal. A separately migrated database with the
	// identical bundle and shared keyring must reject that continuation.
	reopened, resumed := open(database, 10*time.Minute)
	if reopened.DataIncarnation() != application.DataIncarnation() {
		t.Fatal("application restart changed the durable identity")
	}
	resumedItems, _ := pageData(t, pageHTTP(t, resumed, "full", "rankedFeed", "normal", token, 7, false), "rankedFeed")
	assertOrdinals(t, resumedItems, ordinals(28, 50, nil), false)
	var alternate *provider.Database
	switch os.Getenv("GOLEM_OBJECT_PROVIDER") {
	case "sqlite":
		alternate, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_ALTERNATE_DB")})
	case "postgresql":
		alternate, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_ALTERNATE_DB"), Pool: providerpostgres.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
	}
	if err != nil || alternate == nil {
		t.Fatalf("open independently migrated comparison database: %v", err)
	}
	t.Cleanup(func() { _ = alternate.Close() })
	replacement, restarted := open(alternate, 10*time.Minute)
	if replacement.DataIncarnation() == application.DataIncarnation() {
		t.Fatal("independent databases reused a durable identity")
	}
	calls := app.ResolverCalls.Load()
	refusedPage(t, pageHTTP(t, restarted, "full", "rankedFeed", "normal", token, 7, false))
	if app.ResolverCalls.Load() != calls {
		t.Fatal("old-incarnation token reached replacement resolver")
	}
	_, shortLived := open(database, 2*time.Second)
	_, expiring := pageData(t, pageHTTP(t, shortLived, "full", "rankedFeed", "normal", "", 7, false), "rankedFeed")
	time.Sleep(2100 * time.Millisecond)
	calls = app.ResolverCalls.Load()
	expiry := refusedPage(t, pageHTTP(t, shortLived, "full", "rankedFeed", "normal", expiring, 7, false))
	if expiry["code"] != "BAD_USER_INPUT" || expiry["reason"] != "CONTINUATION_EXPIRED" || app.ResolverCalls.Load() != calls {
		t.Fatalf("expired traversal did not request restart before resolver: %#v", expiry)
	}
}
`
