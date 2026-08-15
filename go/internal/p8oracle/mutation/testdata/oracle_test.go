package oracle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

const (
	ownerIDText    = "91000000-0000-0000-0000-000000000001"
	otherIDText    = "91000000-0000-0000-0000-000000000002"
	postModelID    = "b11b75f11668a6f7019d05b1740d5118"
	commentModelID = "364a3861c2d382ec0f02e83974f3b3c4"
)

type principalKey struct{}

type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       *provider.Database
	app      *social.App[social.Principal]
	caller   *social.Caller[social.Principal]
	graph    *social.GraphQLServer
	handler  http.Handler
	trace    *hookTrace
	reports  *errorReports
	observed *observationTrace
	samples  map[string][]observedOperation
	owner    golem.UUID
	other    golem.UUID
}

type hookTrace struct {
	mu     sync.Mutex
	phases []string
}

func (trace *hookTrace) ObservePostHook(_ context.Context, phase social.PostHookPhase, _ golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases = append(trace.phases, string(phase))
	return nil
}

func (trace *hookTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases = nil
}

func (trace *hookTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.phases...)
}

type errorReports struct {
	mu     sync.Mutex
	values []string
}

type observedOperation struct {
	Kind       observe.Kind
	Operation  observe.Operation
	Outcome    observe.Outcome
	Reason     observe.Reason
	Model      golem.ModelID
	Statements int
	Aggregate  int64
}

type observationTrace struct {
	mu     sync.Mutex
	values []observedOperation
}

func (trace *observationTrace) ObserveGolem(_ context.Context, value observe.Observation) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = append(trace.values, observedOperation{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Reason: value.Reason(),
		Model: value.ModelID(), Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
	})
}

func (trace *observationTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.values = nil
}

func (trace *observationTrace) snapshot() []observedOperation {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]observedOperation(nil), trace.values...)
}

func (reports *errorReports) add(err error) {
	reports.mu.Lock()
	defer reports.mu.Unlock()
	reports.values = append(reports.values, err.Error())
}

func (reports *errorReports) snapshot() []string {
	reports.mu.Lock()
	defer reports.mu.Unlock()
	return append([]string(nil), reports.values...)
}

type postTruth struct {
	ID        string
	AuthorID  string
	Title     string
	Body      string
	Published int
}

type factCounts map[string]int64

type nestedPair struct{ program, graph string }

type graphResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
	Raw string `json:"-"`
}

func TestP8ExternalOracleScenario(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "cross-entry":
		f.crossEntry()
	case "nested-batch-upsert":
		f.nestedBatchUpsert()
	case "custom-transaction":
		f.customTransaction()
	case "denial-provider-failure":
		f.denialAndProviderFailure()
	default:
		t.Fatalf("unknown mutation oracle scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(ctx, sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	default:
		t.Fatalf("unknown provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	reports := &errorReports{}
	observed := &observationTrace{}
	application, err := social.Open(ctx, social.Config[social.Principal]{
		Database:       database,
		EventTransport: transport,
		Observer:       observed,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			if principal.Development {
				return social.Actor{UserID: principal.DevUserID, Authenticated: true}, nil
			}
			return social.Actor{}, nil
		},
		SnapshotPrincipal:   func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:       func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal:      func(social.Principal) string { return "p8-mutation-oracle" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			reports.add(failure.Cause())
		},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	owner := mustUUID(t, ownerIDText)
	other := mustUUID(t, otherIDText)
	system := application.System()
	for _, user := range []struct {
		id, handle, email string
	}{{ownerIDText, "p8-owner", "owner@example.invalid"}, {otherIDText, "p8-other", "other@example.invalid"}} {
		if _, err := system.Users.Create(ctx, social.Users.Create(
			social.Users.ID.Create(mustUUID(t, user.id)), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.email),
		)); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	principal := social.Principal{Development: true, DevUserID: owner}
	caller, err := application.ForPrincipal(ctx, principal)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	trace := &hookTrace{}
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(ctx context.Context) (social.Principal, bool) {
			value, ok := ctx.Value(principalKey{}).(social.Principal)
			return value, ok
		},
		ReportInternalError: func(_ context.Context, err error) { reports.add(err) },
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestContext := context.WithValue(request.Context(), principalKey{}, principal)
		requestContext = social.WithPostHookObserver(requestContext, trace)
		graph.Handler().ServeHTTP(writer, request.WithContext(requestContext))
	})
	return &fixture{t: t, ctx: social.WithPostHookObserver(ctx, trace), db: database, app: application, caller: caller, graph: graph, handler: handler, trace: trace, reports: reports, observed: observed, samples: map[string][]observedOperation{}, owner: owner, other: other}
}

func (f *fixture) close() {
	if f.graph != nil {
		_ = f.graph.Shutdown(context.Background())
	}
	if f.db != nil {
		_ = f.db.Close()
	}
}

func (f *fixture) crossEntry() {
	ids := []string{
		"91100000-0000-0000-0000-000000000001",
		"91100000-0000-0000-0000-000000000002",
		"91100000-0000-0000-0000-000000000003",
	}
	beforeFacts := f.facts()
	for index, entry := range []string{"caller", "caller-tx", "graphql"} {
		id := mustUUID(f.t, ids[index])
		f.trace.reset()
		switch entry {
		case "caller":
			_, err := f.caller.Posts.Create(f.ctx, postInput(f.t, id, "equivalent", "equivalent body"))
			mustNoError(f.t, err)
		case "caller-tx":
			mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
				_, err := tx.Posts.Create(f.ctx, postInput(f.t, id, "equivalent", "equivalent body"))
				return err
			}))
		case "graphql":
			response := f.graphql(`mutation Create($data: PostCreateInput!) { createPost(data: $data) { id title } }`, map[string]any{"data": graphPostInput(ids[index], "equivalent", "equivalent body", nil)})
			if len(response.Errors) != 0 {
				f.t.Fatalf("GraphQL create=%s trusted=%v observed=%+v", response.Raw, f.reports.snapshot(), f.observed.snapshot())
			}
		}
		got := f.post(ids[index])
		want := postTruth{ID: ids[index], AuthorID: ownerIDText, Title: "equivalent", Body: "equivalent body", Published: 0}
		if got != want {
			f.t.Fatalf("%s committed row=%+v want=%+v", entry, got, want)
		}
		wantHooks := []string{"before_create", "after_create", "after_commit_create"}
		if phases := f.trace.snapshot(); !reflect.DeepEqual(phases, wantHooks) {
			f.t.Fatalf("%s hook trace=%v want=%v", entry, phases, wantHooks)
		}
	}
	afterFacts := f.facts()
	assertFactDelta(f.t, beforeFacts, afterFacts, factCounts{postModelID + "/created": 3})

	// Reading, mutating, and rereading through one Caller exercises the public
	// invalidation boundary. Direct SQL supplies the independent expected value.
	target := mustUUID(f.t, ids[0])
	before, err := f.caller.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(target), social.Posts.Select(social.Posts.Title))
	mustNoError(f.t, err)
	if value, _ := golem.Value(before, social.Posts.Title).Get(); value != "equivalent" {
		f.t.Fatalf("pre-invalidation title=%q", value)
	}
	_, err = f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(target), social.Posts.Update(social.Posts.Title.Set("fresh title")))
	mustNoError(f.t, err)
	after, err := f.caller.Posts.FindUnique(f.ctx, social.Posts.ByID.Value(target), social.Posts.Select(social.Posts.Title))
	mustNoError(f.t, err)
	value, _ := golem.Value(after, social.Posts.Title).Get()
	if value != "fresh title" || f.post(ids[0]).Title != "fresh title" {
		f.t.Fatalf("post-mutation public/direct truth=%q/%q", value, f.post(ids[0]).Title)
	}
	assertFactDelta(f.t, afterFacts, f.facts(), factCounts{postModelID + "/updated": 1})
	f.ordinaryMutationMatrix()
}

func (f *fixture) ordinaryMutationMatrix() {
	surfaces := []string{"caller", "caller-tx", "graphql"}
	primary := map[string]string{"caller": uid(301), "caller-tx": uid(302), "graphql": uid(303)}
	upserts := map[string]string{"caller": uid(311), "caller-tx": uid(312), "graphql": uid(313)}
	batch := map[string][2]string{
		"caller":    {uid(321), uid(322)},
		"caller-tx": {uid(323), uid(324)},
		"graphql":   {uid(325), uid(326)},
	}

	before := f.facts()
	for _, surface := range surfaces {
		f.ordinaryCreate(surface, primary[surface], "ordinary-create")
		if got := f.post(primary[surface]); got.Title != "ordinary-create" || got.AuthorID != ownerIDText {
			f.t.Fatalf("%s create truth=%+v", surface, got)
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/created": 3})

	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryUpdate(surface, primary[surface], "ordinary-update")
		if got := f.post(primary[surface]); got.Title != "ordinary-update" {
			f.t.Fatalf("%s update changed fields=%+v", surface, got)
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/updated": 3})

	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryUpsert(surface, upserts[surface], "upsert-created", "unused")
		if got := f.post(upserts[surface]); got.Title != "upsert-created" {
			f.t.Fatalf("%s upsert create truth=%+v", surface, got)
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/created": 3})

	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryUpsert(surface, upserts[surface], "unused", "upsert-updated")
		if got := f.post(upserts[surface]); got.Title != "upsert-updated" {
			f.t.Fatalf("%s upsert update truth=%+v", surface, got)
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/updated": 3})

	for _, surface := range surfaces {
		for _, id := range batch[surface] {
			f.ordinaryCreate(surface, id, "batch-before")
		}
	}
	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryUpdateMany(surface, batch[surface], "batch-after")
		for _, id := range batch[surface] {
			if got := f.post(id); got.Title != "batch-after" {
				f.t.Fatalf("%s updateMany truth=%+v", surface, got)
			}
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/updated": 6})

	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryDeleteMany(surface, batch[surface])
		for _, id := range batch[surface] {
			if f.postExists(id) {
				f.t.Fatalf("%s deleteMany retained %s", surface, id)
			}
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/deleted": 6})

	before = f.facts()
	for _, surface := range surfaces {
		f.ordinaryDelete(surface, primary[surface])
		if f.postExists(primary[surface]) {
			f.t.Fatalf("%s delete retained row", surface)
		}
	}
	assertFactDelta(f.t, before, f.facts(), factCounts{postModelID + "/deleted": 3})
	f.assertOrdinaryObservationSamples()
}

func (f *fixture) ordinaryCreate(surface, id, title string) {
	f.t.Helper()
	f.observed.reset()
	input := postInput(f.t, mustUUID(f.t, id), title, title+" body")
	switch surface {
	case "caller":
		_, err := f.caller.Posts.Create(f.ctx, input)
		mustNoError(f.t, err)
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			_, err := tx.Posts.Create(f.ctx, input)
			return err
		}))
	case "graphql":
		f.graphql(`mutation Create($data: PostCreateInput!) { createPost(data: $data) { id } }`, map[string]any{"data": graphPostInput(id, title, title+" body", nil)}).requireSuccess(f.t)
	default:
		f.t.Fatalf("unknown mutation surface %q", surface)
	}
	f.captureSample(surface+"/create", f.observed.snapshot())
}

func (f *fixture) ordinaryUpdate(surface, id, title string) {
	f.t.Helper()
	f.observed.reset()
	switch surface {
	case "caller":
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)), social.Posts.Update(social.Posts.Title.Set(title)))
		mustNoError(f.t, err)
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			_, err := tx.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)), social.Posts.Update(social.Posts.Title.Set(title)))
			return err
		}))
	case "graphql":
		f.graphql(`mutation Update($id: UUID!, $title: String!) { updatePost(where: {ID: $id}, data: {title: {set: $title}}) { id } }`, map[string]any{"id": id, "title": title}).requireSuccess(f.t)
	}
	f.captureSample(surface+"/update", f.observed.snapshot())
}

func (f *fixture) ordinaryUpsert(surface, id, createTitle, updateTitle string) {
	f.t.Helper()
	f.observed.reset()
	create := postInput(f.t, mustUUID(f.t, id), createTitle, createTitle+" body")
	update := social.Posts.Update(social.Posts.Title.Set(updateTitle))
	switch surface {
	case "caller":
		_, err := f.caller.Posts.Upsert(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)), create, update)
		mustNoError(f.t, err)
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			_, err := tx.Posts.Upsert(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)), create, update)
			return err
		}))
	case "graphql":
		f.graphql(`mutation Upsert($where: PostWhereUniqueInput!, $create: PostCreateInput!, $update: PostUpdateInput!) { upsertPost(where: $where, create: $create, update: $update) { id } }`, map[string]any{
			"where": map[string]any{"ID": id}, "create": graphPostInput(id, createTitle, createTitle+" body", nil), "update": map[string]any{"title": map[string]any{"set": updateTitle}},
		}).requireSuccess(f.t)
	}
	branch := "create"
	if createTitle == "unused" {
		branch = "update"
	}
	f.captureSample(surface+"/upsert-"+branch, f.observed.snapshot())
}

func (f *fixture) ordinaryUpdateMany(surface string, ids [2]string, title string) {
	f.t.Helper()
	f.observed.reset()
	a, b := mustUUID(f.t, ids[0]), mustUUID(f.t, ids[1])
	switch surface {
	case "caller":
		count, err := f.caller.Posts.UpdateMany(f.ctx, social.Posts.ID.In(a, b), social.Posts.UpdateMany(social.Posts.Title.Set(title)))
		if err != nil || count != 2 {
			f.t.Fatalf("Caller updateMany=%d/%v", count, err)
		}
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			count, err := tx.Posts.UpdateMany(f.ctx, social.Posts.ID.In(a, b), social.Posts.UpdateMany(social.Posts.Title.Set(title)))
			if err == nil && count != 2 {
				return errors.New("CallerTx updateMany count differs")
			}
			return err
		}))
	case "graphql":
		response := f.graphql(`mutation UpdateMany($ids: [UUID!], $title: String!) { updateManyPosts(where: {id: {in: $ids}}, data: {title: {set: $title}}) { count } }`, map[string]any{"ids": []any{ids[0], ids[1]}, "title": title})
		response.requireSuccess(f.t)
		if graphBatchCount(response.Data["updateManyPosts"]) != 2 {
			f.t.Fatalf("GraphQL updateMany=%v", response.Data)
		}
	}
	f.captureSample(surface+"/update-many", f.observed.snapshot())
}

func (f *fixture) ordinaryDeleteMany(surface string, ids [2]string) {
	f.t.Helper()
	f.observed.reset()
	a, b := mustUUID(f.t, ids[0]), mustUUID(f.t, ids[1])
	switch surface {
	case "caller":
		count, err := f.caller.Posts.DeleteMany(f.ctx, social.Posts.ID.In(a, b))
		if err != nil || count != 2 {
			f.t.Fatalf("Caller deleteMany=%d/%v", count, err)
		}
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			count, err := tx.Posts.DeleteMany(f.ctx, social.Posts.ID.In(a, b))
			if err == nil && count != 2 {
				return errors.New("CallerTx deleteMany count differs")
			}
			return err
		}))
	case "graphql":
		response := f.graphql(`mutation DeleteMany($ids: [UUID!]) { deleteManyPosts(where: {id: {in: $ids}}) { count } }`, map[string]any{"ids": []any{ids[0], ids[1]}})
		response.requireSuccess(f.t)
		if graphBatchCount(response.Data["deleteManyPosts"]) != 2 {
			f.t.Fatalf("GraphQL deleteMany=%v", response.Data)
		}
	}
	f.captureSample(surface+"/delete-many", f.observed.snapshot())
}

func (f *fixture) ordinaryDelete(surface, id string) {
	f.t.Helper()
	f.observed.reset()
	switch surface {
	case "caller":
		_, err := f.caller.Posts.Delete(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)))
		mustNoError(f.t, err)
	case "caller-tx":
		mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
			_, err := tx.Posts.Delete(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, id)))
			return err
		}))
	case "graphql":
		f.graphql(`mutation Delete($id: UUID!) { deletePost(where: {ID: $id}) { id } }`, map[string]any{"id": id}).requireSuccess(f.t)
	}
	f.captureSample(surface+"/delete", f.observed.snapshot())
}

func (f *fixture) captureSample(key string, values []observedOperation) {
	if _, exists := f.samples[key]; !exists {
		f.samples[key] = values
	}
}

func (f *fixture) assertOrdinaryObservationSamples() {
	f.t.Helper()
	type expectation struct {
		surface, name string
		operation     observe.Operation
		statements    int
		aggregate     int64
	}
	expectations := []expectation{
		{"caller", "create", observe.OperationMutationCreate, 4, 0},
		{"caller", "update", observe.OperationMutationUpdate, 5, 0},
		{"caller", "delete", observe.OperationMutationDelete, 4, 0},
		{"caller", "update-many", observe.OperationMutationUpdateMany, 6, 2},
		{"caller", "delete-many", observe.OperationMutationDeleteMany, 5, 2},
		{"caller", "upsert-create", observe.OperationMutationUpsert, 7, 0},
		{"caller", "upsert-update", observe.OperationMutationUpsert, 8, 0},
		{"caller-tx", "create", observe.OperationMutationCreate, 2, 0},
		{"caller-tx", "update", observe.OperationMutationUpdate, 3, 0},
		{"caller-tx", "delete", observe.OperationMutationDelete, 2, 0},
		{"caller-tx", "update-many", observe.OperationMutationUpdateMany, 4, 2},
		{"caller-tx", "delete-many", observe.OperationMutationDeleteMany, 3, 2},
		{"caller-tx", "upsert-create", observe.OperationMutationUpsert, 5, 0},
		{"caller-tx", "upsert-update", observe.OperationMutationUpsert, 6, 0},
		{"graphql", "create", observe.OperationMutationCreate, 5, 0},
		{"graphql", "update", observe.OperationMutationUpdate, 6, 0},
		{"graphql", "delete", observe.OperationMutationDelete, 5, 0},
		{"graphql", "update-many", observe.OperationMutationUpdateMany, 6, 2},
		{"graphql", "delete-many", observe.OperationMutationDeleteMany, 5, 2},
		{"graphql", "upsert-create", observe.OperationMutationUpsert, 8, 0},
		{"graphql", "upsert-update", observe.OperationMutationUpsert, 9, 0},
	}
	if f.db.Provider() == golem.PostgreSQL {
		for index := range expectations {
			if strings.HasPrefix(expectations[index].name, "upsert-") {
				expectations[index].statements--
			}
		}
	}
	for _, want := range expectations {
		key := want.surface + "/" + want.name
		values := f.samples[key]
		assertObserved(f.t, key, values, observe.KindMutation, want.operation, observe.OutcomeSuccess, observe.ReasonNone, postModelID, want.statements, want.aggregate)
		switch want.surface {
		case "caller-tx":
			assertObserved(f.t, key+" ancestor", values, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, "", want.statements+2, 0)
		case "graphql":
			assertObserved(f.t, key+" ancestor", values, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeSuccess, observe.ReasonNone, "", want.statements, 0)
		}
	}
}

func assertObserved(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, outcome observe.Outcome, reason observe.Reason, model string, statements int, aggregate int64) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		valueModel := ""
		if value.Model != (golem.ModelID{}) {
			valueModel = fmt.Sprintf("%x", value.Model[:])
		}
		if value.Kind == kind && value.Operation == operation && valueModel == model {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("%s observations=%+v want exactly one %s/%s", label, values, kind, operation)
	}
	got := matches[0]
	gotModel := ""
	if got.Model != (golem.ModelID{}) {
		gotModel = fmt.Sprintf("%x", got.Model[:])
	}
	if got.Outcome != outcome || got.Reason != reason || gotModel != model || got.Statements != statements || got.Aggregate != aggregate {
		t.Fatalf("%s observation=%+v model=%s want outcome=%s reason=%s model=%s statements=%d aggregate=%d", label, got, gotModel, outcome, reason, model, statements, aggregate)
	}
}

func (f *fixture) nestedBatchUpsert() {
	programPost := "91200000-0000-0000-0000-000000000001"
	programComment := "91200000-0000-0000-0000-000000000011"
	graphPost := "91200000-0000-0000-0000-000000000002"
	graphComment := "91200000-0000-0000-0000-000000000012"
	baseline := f.facts()
	values := append(postValues(f.t, mustUUID(f.t, programPost), "nested", "nested body"),
		social.Posts.Comments.Create(social.Comments.Create(
			social.Comments.ID.Create(mustUUID(f.t, programComment)),
			social.Comments.AuthorID.Create(f.owner), social.Comments.Body.Create("child"),
		)))
	_, err := f.caller.Posts.Create(f.ctx, social.Posts.Create(values...))
	mustNoError(f.t, err)
	response := f.graphql(`mutation Nested($data: PostCreateInput!) { createPost(data: $data) { id } }`, map[string]any{"data": graphPostInput(graphPost, "nested", "nested body", []map[string]any{{"id": graphComment, "body": "child", "author": map[string]any{"connect": map[string]any{"ID": ownerIDText}}}})})
	response.requireSuccess(f.t)
	for _, pair := range [][2]string{{programPost, programComment}, {graphPost, graphComment}} {
		if got := f.post(pair[0]); got.AuthorID != ownerIDText || got.Title != "nested" {
			f.t.Fatalf("nested post truth=%+v", got)
		}
		if postID, authorID, body := f.comment(pair[1]); postID != pair[0] || authorID != ownerIDText || body != "child" {
			f.t.Fatalf("nested comment truth=%q/%q/%q", postID, authorID, body)
		}
	}
	assertFactDelta(f.t, baseline, f.facts(), factCounts{postModelID + "/created": 2, commentModelID + "/created": 2})

	beforeBatch := f.facts()
	count, err := f.caller.Posts.UpdateMany(f.ctx, social.Posts.ID.Eq(mustUUID(f.t, programPost)), social.Posts.UpdateMany(social.Posts.Published.Set(true)))
	if err != nil || count != 1 {
		f.t.Fatalf("programmatic batch count=%d error=%v", count, err)
	}
	batch := f.graphql(`mutation Batch($where: PostWhereInput!, $data: PostUpdateManyInput!) { updateManyPosts(where: $where, data: $data) { count } }`, map[string]any{
		"where": map[string]any{"id": map[string]any{"equals": graphPost}}, "data": map[string]any{"published": map[string]any{"set": true}},
	})
	batch.requireSuccess(f.t)
	if graphBatchCount(batch.Data["updateManyPosts"]) != 1 {
		f.t.Fatalf("GraphQL batch result=%v", batch.Data)
	}
	if f.post(programPost).Published != 1 || f.post(graphPost).Published != 1 {
		f.t.Fatal("batch persisted truth differs")
	}
	assertFactDelta(f.t, beforeBatch, f.facts(), factCounts{postModelID + "/updated": 2})

	programUpsert := "91200000-0000-0000-0000-000000000021"
	graphUpsert := "91200000-0000-0000-0000-000000000022"
	beforeUpsert := f.facts()
	_, err = f.caller.Posts.Upsert(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, programUpsert)), postInput(f.t, mustUUID(f.t, programUpsert), "upsert create", "body"), social.Posts.Update(social.Posts.Title.Set("unused")))
	mustNoError(f.t, err)
	created := f.graphql(`mutation Upsert($where: PostWhereUniqueInput!, $create: PostCreateInput!, $update: PostUpdateInput!) { upsertPost(where: $where, create: $create, update: $update) { id title } }`, map[string]any{
		"where": map[string]any{"ID": graphUpsert}, "create": graphPostInput(graphUpsert, "upsert create", "body", nil), "update": map[string]any{"title": map[string]any{"set": "unused"}},
	})
	created.requireSuccess(f.t)
	_, err = f.caller.Posts.Upsert(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, programUpsert)), postInput(f.t, mustUUID(f.t, programUpsert), "unused", "body"), social.Posts.Update(social.Posts.Title.Set("upsert update")))
	mustNoError(f.t, err)
	updated := f.graphql(`mutation Upsert($where: PostWhereUniqueInput!, $create: PostCreateInput!, $update: PostUpdateInput!) { upsertPost(where: $where, create: $create, update: $update) { id title } }`, map[string]any{
		"where": map[string]any{"ID": graphUpsert}, "create": graphPostInput(graphUpsert, "unused", "body", nil), "update": map[string]any{"title": map[string]any{"set": "upsert update"}},
	})
	updated.requireSuccess(f.t)
	if f.post(programUpsert).Title != "upsert update" || f.post(graphUpsert).Title != "upsert update" {
		f.t.Fatal("upsert branches did not converge on hand-enumerated truth")
	}
	assertFactDelta(f.t, beforeUpsert, f.facts(), factCounts{postModelID + "/created": 2, postModelID + "/updated": 2})

	// The same independently expected target is contested through distinct
	// Caller operations. The oracle does not predict the winning creator; it
	// requires database coordination to yield eight successes, one physical
	// row, and the fixed update-branch value after every contender completes.
	concurrentID := "91200000-0000-0000-0000-000000000031"
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, runErr := f.caller.Posts.Upsert(context.Background(), social.Posts.ByID.Value(mustUUID(f.t, concurrentID)),
				postInput(f.t, mustUUID(f.t, concurrentID), "race create", "race body"),
				social.Posts.Update(social.Posts.Title.Set("race update")))
			errorsFound <- runErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	for runErr := range errorsFound {
		mustNoError(f.t, runErr)
	}
	if got := f.post(concurrentID); got.Title != "race update" || got.AuthorID != ownerIDText {
		f.t.Fatalf("coordinated upsert truth=%+v", got)
	}

	f.exhaustiveNestedVocabulary()
}

func (f *fixture) exhaustiveNestedVocabulary() {
	// Each case owns disjoint rows. Seed writes happen before the fact snapshot;
	// every assertion below therefore describes only the operation under test.
	// create
	posts := nestedPair{uid(101), uid(102)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments := nestedPair{uid(111), uid(112)}
	f.runNestedPair("create", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Create(commentInput(f.t, comments.program, "create"))))
		return err
	}, map[string]any{"create": []any{graphNestedComment(comments.graph, "create")}}, factCounts{commentModelID + "/created": 2})
	f.assertComment(comments.program, posts.program, "create")
	f.assertComment(comments.graph, posts.graph, "create")

	// createMany
	posts = nestedPair{uid(121), uid(122)}
	f.seedOwnedPosts(posts.program, posts.graph)
	p1, p2, g1, g2 := uid(123), uid(124), uid(125), uid(126)
	f.runNestedPair("create-many", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.CreateMany(commentInput(f.t, p1, "many-a"), commentInput(f.t, p2, "many-b"))))
		return err
	}, map[string]any{"createMany": []any{graphNestedComment(g1, "many-a"), graphNestedComment(g2, "many-b")}}, factCounts{commentModelID + "/created": 4})
	f.assertComment(p1, posts.program, "many-a")
	f.assertComment(p2, posts.program, "many-b")
	f.assertComment(g1, posts.graph, "many-a")
	f.assertComment(g2, posts.graph, "many-b")

	// connect moves an independently existing owned child from a donor.
	posts = nestedPair{uid(131), uid(132)}
	donors := nestedPair{uid(133), uid(134)}
	f.seedOwnedPosts(posts.program, posts.graph, donors.program, donors.graph)
	comments = nestedPair{uid(135), uid(136)}
	f.seedComment(comments.program, donors.program, "connect")
	f.seedComment(comments.graph, donors.graph, "connect")
	f.runNestedPair("connect", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Connect(social.Comments.ByID.Value(mustUUID(f.t, comments.program)))))
		return err
	}, map[string]any{"connect": []any{map[string]any{"ID": comments.graph}}}, factCounts{commentModelID + "/updated": 2})
	f.assertComment(comments.program, posts.program, "connect")
	f.assertComment(comments.graph, posts.graph, "connect")

	// disconnect is explicitly refused because Comment.Post is required.
	posts = nestedPair{uid(141), uid(142)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(143), uid(144)}
	f.seedComment(comments.program, posts.program, "disconnect")
	f.seedComment(comments.graph, posts.graph, "disconnect")
	before := f.facts()
	_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
		social.Posts.Comments.Disconnect(social.Comments.ByID.Value(mustUUID(f.t, comments.program)))))
	assertPublicCode(f.t, err, golem.CodeBadUserInput)
	failed := f.graphUpdate(posts.graph, map[string]any{"disconnect": []any{map[string]any{"ID": comments.graph}}})
	failed.requireCode(f.t, "BAD_USER_INPUT")
	assertFactDelta(f.t, before, f.facts(), factCounts{})
	f.assertComment(comments.program, posts.program, "disconnect")
	f.assertComment(comments.graph, posts.graph, "disconnect")

	// set to the exact existing membership is a successful, lossless no-op.
	posts = nestedPair{uid(151), uid(152)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(153), uid(154)}
	f.seedComment(comments.program, posts.program, "set")
	f.seedComment(comments.graph, posts.graph, "set")
	f.runNestedPair("set", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Set(social.Comments.ByID.Value(mustUUID(f.t, comments.program)))))
		return err
	}, map[string]any{"set": []any{map[string]any{"ID": comments.graph}}}, factCounts{})
	f.assertComment(comments.program, posts.program, "set")
	f.assertComment(comments.graph, posts.graph, "set")

	// A non-empty set moves a required child from a donor and therefore executes
	// the explicit set-relation mutation node rather than only proving a no-op.
	posts = nestedPair{uid(155), uid(156)}
	donors = nestedPair{uid(157), uid(158)}
	f.seedOwnedPosts(posts.program, posts.graph, donors.program, donors.graph)
	comments = nestedPair{uid(159), uid(160)}
	f.seedComment(comments.program, donors.program, "set-move")
	f.seedComment(comments.graph, donors.graph, "set-move")
	f.runNestedPair("set-move", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Set(social.Comments.ByID.Value(mustUUID(f.t, comments.program)))))
		return err
	}, map[string]any{"set": []any{map[string]any{"ID": comments.graph}}}, factCounts{commentModelID + "/updated": 2})
	f.assertComment(comments.program, posts.program, "set-move")
	f.assertComment(comments.graph, posts.graph, "set-move")

	// delete
	posts = nestedPair{uid(161), uid(162)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(163), uid(164)}
	f.seedComment(comments.program, posts.program, "delete")
	f.seedComment(comments.graph, posts.graph, "delete")
	f.runNestedPair("delete", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Delete(social.Comments.ByID.Value(mustUUID(f.t, comments.program)))))
		return err
	}, map[string]any{"delete": []any{map[string]any{"ID": comments.graph}}}, factCounts{commentModelID + "/deleted": 2})
	f.assertCommentAbsent(comments.program)
	f.assertCommentAbsent(comments.graph)

	// deleteMany
	posts = nestedPair{uid(171), uid(172)}
	f.seedOwnedPosts(posts.program, posts.graph)
	ids := []string{uid(173), uid(174), uid(175), uid(176)}
	f.seedComment(ids[0], posts.program, "delete-many")
	f.seedComment(ids[1], posts.program, "delete-many")
	f.seedComment(ids[2], posts.graph, "delete-many")
	f.seedComment(ids[3], posts.graph, "delete-many")
	f.runNestedPair("delete-many", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.DeleteMany(social.Comments.Body.Eq("delete-many"))))
		return err
	}, map[string]any{"deleteMany": []any{map[string]any{"body": map[string]any{"equals": "delete-many"}}}}, factCounts{commentModelID + "/deleted": 4})
	for _, id := range ids {
		f.assertCommentAbsent(id)
	}

	// connectOrCreate takes the create branch with hand-enumerated input.
	posts = nestedPair{uid(181), uid(182)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(183), uid(184)}
	f.runNestedPair("connect-or-create-create", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.ConnectOrCreate(social.Comments.ByID.Value(mustUUID(f.t, comments.program)), commentInput(f.t, comments.program, "connect-or-create"))))
		return err
	}, map[string]any{"connectOrCreate": []any{map[string]any{"where": map[string]any{"ID": comments.graph}, "create": graphNestedComment(comments.graph, "connect-or-create")}}}, factCounts{commentModelID + "/created": 2})
	f.assertComment(comments.program, posts.program, "connect-or-create")
	f.assertComment(comments.graph, posts.graph, "connect-or-create")
	// The same operation's connect branch moves exact existing children.
	posts = nestedPair{uid(185), uid(186)}
	donors = nestedPair{uid(187), uid(188)}
	f.seedOwnedPosts(posts.program, posts.graph, donors.program, donors.graph)
	comments = nestedPair{uid(189), uid(190)}
	f.seedComment(comments.program, donors.program, "connect-or-create-existing")
	f.seedComment(comments.graph, donors.graph, "connect-or-create-existing")
	f.runNestedPair("connect-or-create-connect", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.ConnectOrCreate(social.Comments.ByID.Value(mustUUID(f.t, comments.program)), commentInput(f.t, comments.program, "unused"))))
		return err
	}, map[string]any{"connectOrCreate": []any{map[string]any{"where": map[string]any{"ID": comments.graph}, "create": graphNestedComment(comments.graph, "unused")}}}, factCounts{commentModelID + "/updated": 2})
	f.assertComment(comments.program, posts.program, "connect-or-create-existing")
	f.assertComment(comments.graph, posts.graph, "connect-or-create-existing")

	// update
	posts = nestedPair{uid(191), uid(192)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(193), uid(194)}
	f.seedComment(comments.program, posts.program, "before-update")
	f.seedComment(comments.graph, posts.graph, "before-update")
	f.runNestedPair("update", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Update(social.Comments.ByID.Value(mustUUID(f.t, comments.program)), social.Comments.Update(social.Comments.Body.Set("updated")))))
		return err
	}, map[string]any{"update": []any{map[string]any{"where": map[string]any{"ID": comments.graph}, "data": map[string]any{"body": map[string]any{"set": "updated"}}}}}, factCounts{commentModelID + "/updated": 2})
	f.assertComment(comments.program, posts.program, "updated")
	f.assertComment(comments.graph, posts.graph, "updated")
	txPost, txComment := uid(195), uid(196)
	f.seedOwnedPosts(txPost)
	f.seedComment(txComment, txPost, "before-tx-update")
	before = f.facts()
	mustNoError(f.t, f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, updateErr := tx.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, txPost)), social.Posts.Update(
			social.Posts.Comments.Update(social.Comments.ByID.Value(mustUUID(f.t, txComment)), social.Comments.Update(social.Comments.Body.Set("after-tx-update")))))
		return updateErr
	}))
	f.assertComment(txComment, txPost, "after-tx-update")
	assertFactDelta(f.t, before, f.facts(), factCounts{commentModelID + "/updated": 1, postModelID + "/updated": 1})

	// updateMany
	posts = nestedPair{uid(201), uid(202)}
	f.seedOwnedPosts(posts.program, posts.graph)
	ids = []string{uid(203), uid(204), uid(205), uid(206)}
	for _, id := range ids[:2] {
		f.seedComment(id, posts.program, "before-many")
	}
	for _, id := range ids[2:] {
		f.seedComment(id, posts.graph, "before-many")
	}
	f.runNestedPair("update-many", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.UpdateMany(social.Comments.Body.Eq("before-many"), social.Comments.UpdateMany(social.Comments.Body.Set("after-many")))))
		return err
	}, map[string]any{"updateMany": []any{map[string]any{"where": map[string]any{"body": map[string]any{"equals": "before-many"}}, "data": map[string]any{"body": map[string]any{"set": "after-many"}}}}}, factCounts{commentModelID + "/updated": 4})
	for index, id := range ids {
		post := posts.program
		if index >= 2 {
			post = posts.graph
		}
		f.assertComment(id, post, "after-many")
	}

	// upsert takes the update branch for an exact existing child.
	posts = nestedPair{uid(211), uid(212)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(213), uid(214)}
	f.seedComment(comments.program, posts.program, "before-upsert")
	f.seedComment(comments.graph, posts.graph, "before-upsert")
	f.runNestedPair("upsert-update", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Upsert(social.Comments.ByID.Value(mustUUID(f.t, comments.program)), commentInput(f.t, comments.program, "unused"), social.Comments.Update(social.Comments.Body.Set("after-upsert")))))
		return err
	}, map[string]any{"upsert": []any{map[string]any{"where": map[string]any{"ID": comments.graph}, "create": graphNestedComment(comments.graph, "unused"), "update": map[string]any{"body": map[string]any{"set": "after-upsert"}}}}}, factCounts{commentModelID + "/updated": 2})
	f.assertComment(comments.program, posts.program, "after-upsert")
	f.assertComment(comments.graph, posts.graph, "after-upsert")
	// And its create branch creates a child exactly once.
	posts = nestedPair{uid(215), uid(216)}
	f.seedOwnedPosts(posts.program, posts.graph)
	comments = nestedPair{uid(217), uid(218)}
	f.runNestedPair("upsert-create", posts, func() error {
		_, err := f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, posts.program)), social.Posts.Update(
			social.Posts.Comments.Upsert(social.Comments.ByID.Value(mustUUID(f.t, comments.program)), commentInput(f.t, comments.program, "upsert-created"), social.Comments.Update(social.Comments.Body.Set("unused")))))
		return err
	}, map[string]any{"upsert": []any{map[string]any{"where": map[string]any{"ID": comments.graph}, "create": graphNestedComment(comments.graph, "upsert-created"), "update": map[string]any{"body": map[string]any{"set": "unused"}}}}}, factCounts{commentModelID + "/created": 2})
	f.assertComment(comments.program, posts.program, "upsert-created")
	f.assertComment(comments.graph, posts.graph, "upsert-created")
	f.assertNestedObservationSamples()
}

func (f *fixture) assertNestedObservationSamples() {
	f.t.Helper()
	type expectation struct {
		name             string
		callerStatements int
		graphStatements  int
		childOperation   observe.Operation
		childStatements  int
		childCount       int
	}
	expectations := []expectation{
		{"create", 9, 11, observe.OperationMutationCreate, 2, 1},
		{"create-many", 12, 15, observe.OperationMutationCreate, 2, 2},
		{"connect", 10, 11, observe.OperationMutationConnect, 3, 1},
		{"set", 8, 9, "", 0, 0},
		{"set-move", 11, 12, observe.OperationMutationSetRelation, 3, 1},
		{"delete", 9, 10, observe.OperationMutationDelete, 2, 1},
		{"delete-many", 9, 10, observe.OperationMutationDeleteMany, 2, 1},
		{"connect-or-create-create", 12, 14, observe.OperationMutationCreate, 2, 1},
		{"connect-or-create-connect", 13, 14, observe.OperationMutationConnect, 3, 1},
		{"update", 11, 12, observe.OperationMutationUpdate, 3, 1},
		{"update-many", 10, 11, observe.OperationMutationUpdateMany, 3, 1},
		{"upsert-update", 14, 15, observe.OperationMutationUpdate, 3, 1},
		{"upsert-create", 12, 14, observe.OperationMutationCreate, 2, 1},
	}
	if f.db.Provider() == golem.PostgreSQL {
		for index := range expectations {
			if strings.HasPrefix(expectations[index].name, "connect-or-create-") || strings.HasPrefix(expectations[index].name, "upsert-") {
				expectations[index].callerStatements--
				expectations[index].graphStatements--
			}
		}
	}
	for _, want := range expectations {
		for _, surface := range []string{"caller", "graphql"} {
			key := "nested/" + surface + "/" + want.name
			values := f.samples[key]
			statements := want.callerStatements
			if surface == "graphql" {
				statements = want.graphStatements
			}
			assertObserved(f.t, key+" root", values, observe.KindMutation, observe.OperationMutationUpdate, observe.OutcomeSuccess, observe.ReasonNone, postModelID, statements, 0)
			if want.childCount != 0 {
				assertObservedCount(f.t, key+" child", values, observe.KindMutation, want.childOperation, commentModelID, want.childStatements, want.childCount)
			}
			if surface == "graphql" {
				assertObserved(f.t, key+" ancestor", values, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeSuccess, observe.ReasonNone, "", statements, 0)
			}
		}
	}
}

func assertObservedCount(t *testing.T, label string, values []observedOperation, kind observe.Kind, operation observe.Operation, model string, statements, count int) {
	t.Helper()
	var matches []observedOperation
	for _, value := range values {
		if value.Kind == kind && value.Operation == operation && fmt.Sprintf("%x", value.Model[:]) == model {
			matches = append(matches, value)
		}
	}
	if len(matches) != count {
		t.Fatalf("%s observations=%+v want count=%d", label, values, count)
	}
	for _, got := range matches {
		if got.Outcome != observe.OutcomeSuccess || got.Reason != observe.ReasonNone || got.Statements != statements {
			t.Fatalf("%s child observation=%+v want success statements=%d", label, got, statements)
		}
	}
}

func (f *fixture) runNestedPair(name string, posts nestedPair, program func() error, graphData map[string]any, want factCounts) {
	f.t.Helper()
	before := f.facts()
	f.observed.reset()
	mustNoError(f.t, program())
	f.captureSample("nested/caller/"+name, f.observed.snapshot())
	f.observed.reset()
	f.graphUpdate(posts.graph, graphData).requireSuccess(f.t)
	f.captureSample("nested/graphql/"+name, f.observed.snapshot())
	want[postModelID+"/updated"] = 2
	assertFactDelta(f.t, before, f.facts(), want)
}

func (f *fixture) graphUpdate(postID string, comments map[string]any) graphResponse {
	f.t.Helper()
	return f.graphql(`mutation NestedUpdate($id: UUID!, $data: PostUpdateInput!) {
  updatePost(where: {ID: $id}, data: $data) { id }
}`, map[string]any{"id": postID, "data": map[string]any{"comments": comments}})
}

func (f *fixture) seedOwnedPosts(ids ...string) {
	f.t.Helper()
	for _, id := range ids {
		_, err := f.app.System().Posts.Create(f.ctx, systemPostInput(f.t, mustUUID(f.t, id), f.owner, "nested vocabulary"))
		mustNoError(f.t, err)
	}
}

func (f *fixture) seedComment(id, postID, body string) {
	f.t.Helper()
	_, err := f.app.System().Comments.Create(f.ctx, social.Comments.Create(
		social.Comments.ID.Create(mustUUID(f.t, id)), social.Comments.PostID.Create(mustUUID(f.t, postID)),
		social.Comments.AuthorID.Create(f.owner), social.Comments.Body.Create(body),
	))
	mustNoError(f.t, err)
}

func commentInput(t *testing.T, id, body string) social.CommentCreateInput {
	t.Helper()
	return social.Comments.Create(
		social.Comments.ID.Create(mustUUID(t, id)), social.Comments.AuthorID.Create(mustUUID(t, ownerIDText)), social.Comments.Body.Create(body),
	)
}

func graphNestedComment(id, body string) map[string]any {
	return map[string]any{
		"id": id, "body": body,
		"author": map[string]any{"connect": map[string]any{"ID": ownerIDText}},
	}
}

func (f *fixture) assertComment(id, postID, body string) {
	f.t.Helper()
	gotPost, gotAuthor, gotBody := f.comment(id)
	if gotPost != postID || gotAuthor != ownerIDText || gotBody != body {
		f.t.Fatalf("comment %s truth=%q/%q/%q want=%q/%q/%q", id, gotPost, gotAuthor, gotBody, postID, ownerIDText, body)
	}
}

func (f *fixture) assertCommentAbsent(id string) {
	f.t.Helper()
	var count int
	query := f.db.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM comments WHERE id=?`)
	if err := f.db.UnsafeSQLX().QueryRowxContext(f.ctx, query, id).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("comment %s still exists", id)
	}
}

func uid(sequence int) string {
	return fmt.Sprintf("92000000-0000-0000-0000-%012d", sequence)
}

func (f *fixture) customTransaction() {
	programSuccess := "91300000-0000-0000-0000-000000000001"
	graphSuccess := "91300000-0000-0000-0000-000000000002"
	programFail := "91300000-0000-0000-0000-000000000003"
	graphFail := "91300000-0000-0000-0000-000000000004"
	for _, id := range []string{programSuccess, graphSuccess, programFail, graphFail} {
		_, err := f.app.System().Posts.Create(f.ctx, systemPostInput(f.t, mustUUID(f.t, id), f.owner, "custom baseline"))
		mustNoError(f.t, err)
	}
	baseline := f.facts()
	f.observed.reset()
	count, err := social.PublishPost(f.ctx, f.caller, social.PublishPostArgs{PostID: mustUUID(f.t, programSuccess)})
	if err != nil || count != 1 {
		f.t.Fatalf("direct custom success count=%d error=%v", count, err)
	}
	f.captureSample("custom/direct-success", f.observed.snapshot())
	f.observed.reset()
	response := f.graphql(`mutation Publish($id: UUID!) { publishPost(postID: $id, fail: false) }`, map[string]any{"id": graphSuccess})
	response.requireSuccess(f.t)
	if response.Data["publishPost"] != "1" {
		f.t.Fatalf("GraphQL custom success=%v", response.Data)
	}
	f.captureSample("custom/graphql-success", f.observed.snapshot())
	if f.post(programSuccess).Published != 1 || f.post(graphSuccess).Published != 1 {
		f.t.Fatal("custom success did not commit exact rows")
	}
	assertFactDelta(f.t, baseline, f.facts(), factCounts{postModelID + "/updated": 2})

	beforeFailure := f.facts()
	f.observed.reset()
	count, err = social.PublishPost(f.ctx, f.caller, social.PublishPostArgs{PostID: mustUUID(f.t, programFail), Fail: true})
	if err == nil || err.Error() != "requested publish rollback" || count != 1 {
		f.t.Fatalf("direct rollback result count=%d error=%v", count, err)
	}
	f.captureSample("custom/direct-failure", f.observed.snapshot())
	f.observed.reset()
	failed := f.graphql(`mutation Publish($id: UUID!) { publishPost(postID: $id, fail: true) }`, map[string]any{"id": graphFail})
	failed.requireCode(f.t, "INTERNAL_SERVER_ERROR")
	if strings.Contains(failed.Raw, "requested publish rollback") {
		f.t.Fatalf("trusted custom error leaked: %s", failed.Raw)
	}
	f.captureSample("custom/graphql-failure", f.observed.snapshot())
	if f.post(programFail).Published != 0 || f.post(graphFail).Published != 0 {
		f.t.Fatal("custom transaction failure committed")
	}
	assertFactDelta(f.t, beforeFailure, f.facts(), factCounts{})
	reports := f.reports.snapshot()
	if len(reports) != 1 || reports[0] != "requested publish rollback" {
		f.t.Fatalf("trusted custom reports=%v", reports)
	}
	f.assertCustomObservationSamples()
}

func (f *fixture) assertCustomObservationSamples() {
	f.t.Helper()
	directSuccess := f.samples["custom/direct-success"]
	assertObserved(f.t, "custom direct success child", directSuccess, observe.KindMutation, observe.OperationMutationUpdateMany, observe.OutcomeSuccess, observe.ReasonNone, postModelID, 4, 1)
	assertObserved(f.t, "custom direct success transaction", directSuccess, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, "", 6, 0)

	graphSuccess := f.samples["custom/graphql-success"]
	assertObserved(f.t, "custom GraphQL success child", graphSuccess, observe.KindMutation, observe.OperationMutationUpdateMany, observe.OutcomeSuccess, observe.ReasonNone, postModelID, 4, 1)
	assertObserved(f.t, "custom GraphQL success transaction", graphSuccess, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeSuccess, observe.ReasonNone, "", 6, 0)
	assertObserved(f.t, "custom GraphQL success root", graphSuccess, observe.KindGraphQL, observe.OperationGraphQLCustomMutation, observe.OutcomeSuccess, observe.ReasonNone, "", 6, 0)
	assertObserved(f.t, "custom GraphQL success ancestor", graphSuccess, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeSuccess, observe.ReasonNone, "", 6, 0)

	directFailure := f.samples["custom/direct-failure"]
	assertObserved(f.t, "custom direct rollback child", directFailure, observe.KindMutation, observe.OperationMutationUpdateMany, observe.OutcomeSuccess, observe.ReasonNone, postModelID, 4, 1)
	assertObserved(f.t, "custom direct rollback transaction", directFailure, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeFailure, observe.ReasonProvider, "", 4, 0)

	graphFailure := f.samples["custom/graphql-failure"]
	assertObserved(f.t, "custom GraphQL rollback child", graphFailure, observe.KindMutation, observe.OperationMutationUpdateMany, observe.OutcomeSuccess, observe.ReasonNone, postModelID, 4, 1)
	assertObserved(f.t, "custom GraphQL rollback transaction", graphFailure, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeFailure, observe.ReasonProvider, "", 4, 0)
	assertObserved(f.t, "custom GraphQL rollback root", graphFailure, observe.KindGraphQL, observe.OperationGraphQLCustomMutation, observe.OutcomeFailure, observe.ReasonProvider, "", 4, 0)
	assertObserved(f.t, "custom GraphQL rollback ancestor", graphFailure, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeFailure, observe.ReasonProvider, "", 4, 0)
}

func (f *fixture) denialAndProviderFailure() {
	otherPost := "91400000-0000-0000-0000-000000000001"
	_, err := f.app.System().Posts.Create(f.ctx, systemPostInput(f.t, mustUUID(f.t, otherPost), f.other, "other owned"))
	mustNoError(f.t, err)
	baseline := f.facts()
	f.observed.reset()
	_, err = f.caller.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, otherPost)), social.Posts.Update(social.Posts.Title.Set("forbidden")))
	f.captureSample("denial/caller", f.observed.snapshot())
	assertPublicCode(f.t, err, golem.CodeNotFound)
	f.observed.reset()
	err = f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, updateErr := tx.Posts.Update(f.ctx, social.Posts.ByID.Value(mustUUID(f.t, otherPost)), social.Posts.Update(social.Posts.Title.Set("forbidden tx")))
		return updateErr
	})
	f.captureSample("denial/caller-tx", f.observed.snapshot())
	assertPublicCode(f.t, err, golem.CodeNotFound)
	f.observed.reset()
	denied := f.graphql(`mutation Denied($id: UUID!) { updatePost(where: {ID: $id}, data: {title: {set: "forbidden graph"}}) { id } }`, map[string]any{"id": otherPost})
	f.captureSample("denial/graphql", f.observed.snapshot())
	denied.requireCode(f.t, "NOT_FOUND")
	if f.post(otherPost).Title != "other owned" {
		f.t.Fatal("denied mutation changed row")
	}
	assertFactDelta(f.t, baseline, f.facts(), factCounts{})

	// A duplicate nested comment forces a real provider constraint failure
	// after the parent mutation graph has begun. Every entry point must roll the
	// whole graph back, suppress post-commit hooks, and emit no durable facts.
	duplicateComment := "91400000-0000-0000-0000-000000000011"
	seedPost := "91400000-0000-0000-0000-000000000012"
	_, err = f.app.System().Posts.Create(f.ctx, systemPostInput(f.t, mustUUID(f.t, seedPost), f.owner, "provider seed"))
	mustNoError(f.t, err)
	_, err = f.app.System().Comments.Create(f.ctx, social.Comments.Create(
		social.Comments.ID.Create(mustUUID(f.t, duplicateComment)), social.Comments.PostID.Create(mustUUID(f.t, seedPost)),
		social.Comments.AuthorID.Create(f.owner), social.Comments.Body.Create("existing"),
	))
	mustNoError(f.t, err)
	beforeFailure := f.facts()
	programPost := "91400000-0000-0000-0000-000000000021"
	f.trace.reset()
	f.observed.reset()
	_, err = f.caller.Posts.Create(f.ctx, nestedPostInput(f.t, mustUUID(f.t, programPost), mustUUID(f.t, duplicateComment)))
	f.captureSample("provider/caller", f.observed.snapshot())
	assertPublicCode(f.t, err, golem.CodeConflict)
	if f.postExists(programPost) || contains(f.trace.snapshot(), "after_commit_create") {
		f.t.Fatalf("programmatic provider failure row/hooks=%t/%v", f.postExists(programPost), f.trace.snapshot())
	}
	txPost := "91400000-0000-0000-0000-000000000022"
	f.trace.reset()
	f.observed.reset()
	err = f.caller.Transaction(f.ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, createErr := tx.Posts.Create(f.ctx, nestedPostInput(f.t, mustUUID(f.t, txPost), mustUUID(f.t, duplicateComment)))
		return createErr
	})
	f.captureSample("provider/caller-tx", f.observed.snapshot())
	assertPublicCode(f.t, err, golem.CodeConflict)
	if f.postExists(txPost) || contains(f.trace.snapshot(), "after_commit_create") {
		f.t.Fatalf("CallerTx provider failure error/row/hooks=%v/%t/%v", err, f.postExists(txPost), f.trace.snapshot())
	}
	graphPost := "91400000-0000-0000-0000-000000000023"
	f.trace.reset()
	f.observed.reset()
	failed := f.graphql(`mutation ProviderFailure($data: PostCreateInput!) { createPost(data: $data) { id } }`, map[string]any{"data": graphPostInput(graphPost, "provider failure", "body", []map[string]any{{"id": duplicateComment, "body": "duplicate", "author": map[string]any{"connect": map[string]any{"ID": ownerIDText}}}})})
	f.captureSample("provider/graphql", f.observed.snapshot())
	failed.requireCode(f.t, "CONFLICT")
	if f.postExists(graphPost) || contains(f.trace.snapshot(), "after_commit_create") {
		f.t.Fatalf("GraphQL provider failure=%s row=%t hooks=%v", failed.Raw, f.postExists(graphPost), f.trace.snapshot())
	}
	if strings.Contains(strings.ToLower(failed.Raw), "unique") || strings.Contains(strings.ToLower(failed.Raw), "constraint") {
		f.t.Fatalf("provider detail leaked: %s", failed.Raw)
	}
	assertFactDelta(f.t, beforeFailure, f.facts(), factCounts{})
	if postID, authorID, body := f.comment(duplicateComment); postID != seedPost || authorID != ownerIDText || body != "existing" {
		f.t.Fatal("provider failure changed existing child")
	}
	f.assertDenialProviderObservationSamples()
}

func (f *fixture) assertDenialProviderObservationSamples() {
	f.t.Helper()
	denied := []struct {
		key     string
		withTx  bool
		withGQL bool
	}{
		{key: "denial/caller"},
		{key: "denial/caller-tx", withTx: true},
		{key: "denial/graphql", withGQL: true},
	}
	for _, sample := range denied {
		values := f.samples[sample.key]
		assertObserved(f.t, sample.key+" root", values, observe.KindMutation, observe.OperationMutationUpdate, observe.OutcomeRefused, observe.ReasonNotFound, postModelID, 1, 0)
		if sample.withTx {
			assertObserved(f.t, sample.key+" transaction", values, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeRefused, observe.ReasonNotFound, "", 1, 0)
		}
		if sample.withGQL {
			assertObserved(f.t, sample.key+" GraphQL", values, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeRefused, observe.ReasonNotFound, "", 1, 0)
		}
	}

	providerFailures := []struct {
		key                 string
		rootStatements      int
		withTx, withGraphQL bool
	}{
		{key: "provider/caller", rootStatements: 3},
		{key: "provider/caller-tx", rootStatements: 3, withTx: true},
		{key: "provider/graphql", rootStatements: 4, withGraphQL: true},
	}
	for _, sample := range providerFailures {
		values := f.samples[sample.key]
		assertObserved(f.t, sample.key+" child", values, observe.KindMutation, observe.OperationMutationCreate, observe.OutcomeFailure, observe.ReasonProvider, commentModelID, 1, 0)
		assertObserved(f.t, sample.key+" root", values, observe.KindMutation, observe.OperationMutationCreate, observe.OutcomeRefused, observe.ReasonConflict, postModelID, sample.rootStatements, 0)
		if sample.withTx {
			assertObserved(f.t, sample.key+" transaction", values, observe.KindTransaction, observe.OperationCallerTransaction, observe.OutcomeRefused, observe.ReasonConflict, "", sample.rootStatements, 0)
		}
		if sample.withGraphQL {
			assertObserved(f.t, sample.key+" GraphQL", values, observe.KindGraphQL, observe.OperationGraphQLMutation, observe.OutcomeRefused, observe.ReasonConflict, "", sample.rootStatements, 0)
		}
	}
}

func postValues(t *testing.T, id golem.UUID, title, body string) []golem.CreateValue[social.Post] {
	// Supply the other user's ID deliberately. The public hook owns attribution,
	// and the direct-SQL oracle requires the authenticated owner to be persisted.
	return postValuesForAuthor(t, id, mustUUID(t, otherIDText), title, body)
}

func postValuesForAuthor(t *testing.T, id, author golem.UUID, title, body string) []golem.CreateValue[social.Post] {
	return []golem.CreateValue[social.Post]{
		social.Posts.ID.Create(id), social.Posts.AuthorID.Create(author), social.Posts.Title.Create(title), social.Posts.Body.Create(body),
		social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "13:14:15")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)), social.Posts.Topics.Create(golem.List[string]{"p8"}),
	}
}

func postInput(t *testing.T, id golem.UUID, title, body string) social.PostCreateInput {
	return social.Posts.Create(postValues(t, id, title, body)...)
}

func nestedPostInput(t *testing.T, postID, commentID golem.UUID) social.PostCreateInput {
	return social.Posts.Create(append(postValues(t, postID, "provider failure", "body"),
		social.Posts.Comments.Create(social.Comments.Create(
			social.Comments.ID.Create(commentID), social.Comments.AuthorID.Create(mustUUID(t, ownerIDText)), social.Comments.Body.Create("duplicate"),
		)))...)
}

func systemPostInput(t *testing.T, id, author golem.UUID, title string) social.PostCreateInput {
	return social.Posts.Create(postValuesForAuthor(t, id, author, title, title+" body")...)
}

func graphPostInput(id, title, body string, comments []map[string]any) map[string]any {
	result := map[string]any{
		"id": id, "title": title, "body": body, "published": false,
		"liveDate": "2026-08-09", "liveTime": "13:14:15", "metadata": map[string]any{"language": "en", "pinned": false},
		"topics": []any{"p8"},
	}
	if comments != nil {
		result["comments"] = map[string]any{"create": comments}
	}
	return result
}

func (f *fixture) graphql(query string, variables map[string]any) graphResponse {
	f.t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	mustNoError(f.t, err)
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		f.t.Fatalf("decode GraphQL response: %v body=%s", err, recorder.Body.String())
	}
	response.Raw = recorder.Body.String()
	if recorder.Code != http.StatusOK {
		f.t.Fatalf("GraphQL status=%d body=%s", recorder.Code, response.Raw)
	}
	return response
}

func (response graphResponse) requireSuccess(t *testing.T) {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors: %s", response.Raw)
	}
}

func (response graphResponse) requireCode(t *testing.T, want string) {
	t.Helper()
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != want {
		t.Fatalf("GraphQL failure=%s want code=%s", response.Raw, want)
	}
}

func graphBatchCount(value any) int {
	object, _ := value.(map[string]any)
	count, _ := object["count"].(float64)
	return int(count)
}

func (f *fixture) post(id string) postTruth {
	f.t.Helper()
	var result postTruth
	query := f.db.UnsafeSQLX().Rebind(`SELECT CAST(id AS TEXT),CAST(author_id AS TEXT),title,body,CASE WHEN published THEN 1 ELSE 0 END FROM posts WHERE id=?`)
	if err := f.db.UnsafeSQLX().QueryRowxContext(f.ctx, query, id).Scan(&result.ID, &result.AuthorID, &result.Title, &result.Body, &result.Published); err != nil {
		f.t.Fatalf("direct post truth %s: %v", id, err)
	}
	return result
}

func (f *fixture) postExists(id string) bool {
	f.t.Helper()
	var count int
	query := f.db.UnsafeSQLX().Rebind(`SELECT COUNT(*) FROM posts WHERE id=?`)
	if err := f.db.UnsafeSQLX().QueryRowxContext(f.ctx, query, id).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count == 1
}

func (f *fixture) comment(id string) (string, string, string) {
	f.t.Helper()
	var postID, authorID, body string
	query := f.db.UnsafeSQLX().Rebind(`SELECT CAST(post_id AS TEXT),CAST(author_id AS TEXT),body FROM comments WHERE id=?`)
	if err := f.db.UnsafeSQLX().QueryRowxContext(f.ctx, query, id).Scan(&postID, &authorID, &body); err != nil {
		f.t.Fatal(err)
	}
	return postID, authorID, body
}

func (f *fixture) facts() factCounts {
	f.t.Helper()
	table := `"_golem_outbox"`
	if f.db.Provider() == golem.PostgreSQL {
		table = `"_golem"."_golem_outbox"`
	}
	rows, err := f.db.UnsafeSQLX().QueryxContext(f.ctx, `SELECT model_id,action,COUNT(*) FROM `+table+` GROUP BY model_id,action`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	result := factCounts{}
	for rows.Next() {
		var model, action string
		var count int64
		if err := rows.Scan(&model, &action, &count); err != nil {
			f.t.Fatal(err)
		}
		result[model+"/"+action] = count
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return result
}

func assertFactDelta(t *testing.T, before, after, want factCounts) {
	t.Helper()
	keys := map[string]struct{}{}
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	for key := range want {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	got := factCounts{}
	for _, key := range ordered {
		if delta := after[key] - before[key]; delta != 0 {
			got[key] = delta
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable fact delta=%v want=%v", got, want)
	}
}

func assertPublicCode(t *testing.T, err error, want golem.ErrorCode) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("public failure=%v want=%s", err, want)
	}
	wantMessage := map[golem.ErrorCode]string{
		golem.CodeNotFound: "NOT_FOUND: record not found",
		golem.CodeConflict: "CONFLICT: mutation conflicted",
	}[want]
	if wantMessage != "" && err.Error() != wantMessage {
		t.Fatalf("public failure text=%q want=%q", err.Error(), wantMessage)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	mustNoError(t, err)
	return result
}

func mustDate(t *testing.T, value string) golem.Date {
	t.Helper()
	result, err := golem.ParseDate(value)
	mustNoError(t, err)
	return result
}

func mustTime(t *testing.T, value string) golem.Time {
	t.Helper()
	result, err := golem.ParseTime(value)
	mustNoError(t, err)
	return result
}

func mustJSON(t *testing.T, value string) golem.JSON[any] {
	t.Helper()
	result, err := golem.NewJSONDocument[any]([]byte(value))
	mustNoError(t, err)
	return result
}
