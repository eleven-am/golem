package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type p5MutationHookCounters struct {
	before      atomic.Int64
	after       atomic.Int64
	afterCommit atomic.Int64
	omitted     atomic.Int64
	explicitNil atomic.Int64
}

type p5MutationHarness struct {
	mutationVocabularyFixture
	server  *publicgraphql.Server[mutationResultPrincipal]
	decimal golem.EqualField[mutationResultPost, golem.Decimal]
	sdl     string
	compile compilerir.CompilationIR
	capture *p5MutationExecutionCapture
}

type p5MutationExecutionCapture struct {
	sync.Mutex
	execution *CallerMutationExecution[mutationResultPrincipal, mutationResultActor]
	err       error
}

func (capture *p5MutationExecutionCapture) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	return capture.execution.ExecuteFrozenRead(ctx, request)
}

func (capture *p5MutationExecutionCapture) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
	result, err := capture.execution.ExecuteFrozenMutation(ctx, request)
	capture.Lock()
	capture.err = err
	capture.Unlock()
	return result, err
}

func (capture *p5MutationExecutionCapture) lastError() error {
	capture.Lock()
	defer capture.Unlock()
	return capture.err
}

func (capture *p5MutationExecutionCapture) invalidationEpoch() uint64 {
	capture.Lock()
	defer capture.Unlock()
	if capture.execution == nil || capture.execution.caller == nil || capture.execution.caller.executor == nil {
		return 0
	}
	return capture.execution.caller.executor.invalidationEpoch()
}

type p5MutationVetoKey struct{}

func TestGraphQLCreateUpdateNullIncrementDecrementAndBatchOracle(t *testing.T) {
	counters := &p5MutationHookCounters{}
	harness := newP5MutationHarness(t, counters, false)
	ctx := context.Background()

	graphqlCreate := func(id byte, title string, optional string) map[string]any {
		t.Helper()
		optionalField := ""
		if optional == "null" {
			optionalField = ", optionalInt: null"
		}
		response := harness.execute(t, ctx, fmt.Sprintf(`mutation {
	  createPost(data: {
	    id: %q, title: %q, author: { connect: { id: %q } },
	    bigInt: "10", decimal: "1.25"%s
	  }) { id title bigInt decimal optionalInt }
}`, mutationResultUUIDText(id), title, mutationResultUUIDText(1), optionalField))
		return response["createPost"].(map[string]any)
	}

	omitted := graphqlCreate(201, "graphql-omitted", "")
	explicitNull := graphqlCreate(202, "graphql-null", "null")
	if omitted["optionalInt"] != "7" || explicitNull["optionalInt"] != nil {
		t.Fatalf("GraphQL create presence was lost: omitted=%#v explicit-null=%#v", omitted, explicitNull)
	}
	seven := int64(7)
	assertP5PersistedPost(t, harness, 201, "graphql-omitted", 10, &seven)
	assertP5PersistedPost(t, harness, 202, "graphql-null", 10, nil)
	if counters.omitted.Load() != 1 || counters.explicitNil.Load() != 1 {
		t.Fatalf("before-hook presence trace omitted=%d explicit-null=%d", counters.omitted.Load(), counters.explicitNil.Load())
	}

	updated := harness.execute(t, ctx, fmt.Sprintf(`mutation {
  updatePost(where: { id: %q }, data: {
    title: { set: "graphql-set" }, bigInt: { increment: "2" }, optionalInt: { setNull: true }
  }) { title bigInt optionalInt }
}`, mutationResultUUIDText(201)))["updatePost"].(map[string]any)
	if updated["title"] != "graphql-set" || updated["bigInt"] != "12" || updated["optionalInt"] != nil {
		t.Fatalf("GraphQL set/increment/null result=%#v", updated)
	}
	decremented := harness.execute(t, ctx, fmt.Sprintf(`mutation {
  updatePost(where: { id: %q }, data: { bigInt: { decrement: "3" } }) { title bigInt optionalInt }
}`, mutationResultUUIDText(201)))["updatePost"].(map[string]any)
	if decremented["title"] != "graphql-set" || decremented["bigInt"] != "9" || decremented["optionalInt"] != nil {
		t.Fatalf("GraphQL decrement result=%#v", decremented)
	}

	caller := mustMutationResultCaller(t, harness.mutationResultFixture)
	_, err := CallerCreate(ctx, caller, harness.postDescriptor, harness.createExactPost(t, 211, "go-omitted", false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = CallerCreate(ctx, caller, harness.postDescriptor, harness.createExactPost(t, 212, "go-null", true))
	if err != nil {
		t.Fatal(err)
	}
	assertP5PersistedPost(t, harness, 211, "go-omitted", 10, &seven)
	assertP5PersistedPost(t, harness, 212, "go-null", 10, nil)
	goUpdated, err := CallerUpdate(ctx, caller, harness.postDescriptor, harness.target(211), golem.GeneratedUpdateInput[mutationResultPost](harness.schema.Post,
		golem.GeneratedSetFieldValue(harness.schema.Post, harness.title, "go-set"),
		golem.GeneratedIncrementFieldValue(harness.schema.Post, harness.bigInt, int64(2)),
		golem.GeneratedNullFieldValue(harness.schema.Post, harness.optionalInt),
	), golem.Select[mutationResultPost](harness.title, harness.bigInt, harness.optionalInt))
	if err != nil {
		t.Fatal(err)
	}
	goDecremented, err := CallerUpdate(ctx, caller, harness.postDescriptor, harness.target(211), golem.GeneratedUpdateInput[mutationResultPost](harness.schema.Post,
		golem.GeneratedDecrementFieldValue(harness.schema.Post, harness.bigInt, int64(3)),
	), golem.Select[mutationResultPost](harness.title, harness.bigInt, harness.optionalInt))
	if err != nil {
		t.Fatal(err)
	}
	assertP5MutationRow(t, goUpdated, harness, "go-set", 12, true)
	assertP5MutationRow(t, goDecremented, harness, "go-set", 9, true)
	assertP5PersistedPost(t, harness, 201, "graphql-set", 9, nil)
	assertP5PersistedPost(t, harness, 211, "go-set", 9, nil)

	graphqlCreate(221, "graphql-batch", "")
	graphqlCreate(222, "graphql-batch", "null")
	graphqlBatch := harness.execute(t, ctx, `mutation {
  updateManyPosts(where: { title: { equals: "graphql-batch" } }, data: { title: { set: "graphql-batched" } }) { count }
  deleteManyPosts(where: { title: { equals: "graphql-batched" } }) { count }
}`)
	if graphqlBatch["updateManyPosts"].(map[string]any)["count"] != int32(2) || graphqlBatch["deleteManyPosts"].(map[string]any)["count"] != int32(2) {
		t.Fatalf("GraphQL bounded batch counts=%#v", graphqlBatch)
	}
	for _, id := range []byte{231, 232} {
		if _, err := CallerCreate(ctx, caller, harness.postDescriptor, harness.createExactPost(t, id, "go-batch", false)); err != nil {
			t.Fatal(err)
		}
	}
	goUpdateCount, err := CallerUpdateMany(ctx, caller, harness.postDescriptor, harness.title.Eq("go-batch"), golem.GeneratedUpdateManyInput[mutationResultPost](harness.schema.Post,
		golem.GeneratedSetFieldValue(harness.schema.Post, harness.title, "go-batched"),
	))
	if err != nil {
		t.Fatal(err)
	}
	goDeleteCount, err := CallerDeleteMany(ctx, caller, harness.postDescriptor, harness.title.Eq("go-batched"))
	if err != nil {
		t.Fatal(err)
	}
	if goUpdateCount != 2 || goDeleteCount != 2 {
		t.Fatalf("Go caller bounded batch counts update=%d delete=%d", goUpdateCount, goDeleteCount)
	}
	for _, title := range []string{"graphql-batch", "graphql-batched", "go-batch", "go-batched"} {
		assertMutationResultTitleCount(t, harness.mutationResultFixture, title, 0)
	}
}

func TestGraphQLMutationHooksInvalidationFactsAndTransactionsMatchGoCaller(t *testing.T) {
	counters := &p5MutationHookCounters{}
	harness := newP5MutationHarness(t, counters, true)
	ctx := context.Background()
	facts := p5MutationFactCount(t, harness)

	response := harness.execute(t, ctx, fmt.Sprintf(`mutation {
	  createPost(data: {
	    id: %q, title: "graphql-original", author: { connect: { id: %q } },
	    bigInt: "10", decimal: "1.25"
	  }) { id title }
}`, mutationResultUUIDText(241), mutationResultUUIDText(1)))
	if response["createPost"].(map[string]any)["title"] != "hooked" {
		t.Fatalf("GraphQL create result=%#v", response)
	}
	assertP5PersistedPost(t, harness, 241, "hooked", 10, p5Int64Pointer(7))
	graphqlFactDelta := p5MutationFactCount(t, harness) - facts
	if graphqlFactDelta < 1 || harness.capture.invalidationEpoch() != 1 || counters.afterCommit.Load() != 1 {
		t.Fatalf("GraphQL commit facts=%d epoch=%d before=%d after=%d afterCommit=%d", graphqlFactDelta, harness.capture.invalidationEpoch(), counters.before.Load(), counters.after.Load(), counters.afterCommit.Load())
	}
	assertP5FactEnvelopeAndTransaction(t, harness, facts, graphqlFactDelta)

	caller := mustMutationResultCaller(t, harness.mutationResultFixture)
	beforeGoFacts := p5MutationFactCount(t, harness)
	_, err := CallerCreate(ctx, caller, harness.postDescriptor, harness.createExactPost(t, 242, "go-original", false))
	if err != nil {
		t.Fatal(err)
	}
	assertP5PersistedPost(t, harness, 242, "hooked", 10, p5Int64Pointer(7))
	goFactDelta := p5MutationFactCount(t, harness) - beforeGoFacts
	if goFactDelta != graphqlFactDelta || caller.executor.invalidationEpoch() != 1 || counters.afterCommit.Load() != 2 {
		t.Fatalf("Go/GraphQL commit mismatch graphql-facts=%d go-facts=%d epoch=%d afterCommit=%d", graphqlFactDelta, goFactDelta, caller.executor.invalidationEpoch(), counters.afterCommit.Load())
	}

	rollbackFacts := p5MutationFactCount(t, harness)
	vetoContext := context.WithValue(ctx, p5MutationVetoKey{}, true)
	failed := harness.server.Execute(vetoContext, mutationResultPrincipal{}, publicgraphql.Request{Query: fmt.Sprintf(`mutation {
	  createPost(data: {
	    id: %q, title: "graphql-veto", author: { connect: { id: %q } },
	    bigInt: "10", decimal: "1.25"
	  }) { id title }
}`, mutationResultUUIDText(243), mutationResultUUIDText(1))})
	if failed.Data != nil || len(failed.Errors) != 1 || failed.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Fatalf("GraphQL rollback disclosure=%#v", failed)
	}
	assertP5MutationRollback(t, harness, 243, rollbackFacts)
	if harness.capture.invalidationEpoch() != 0 {
		t.Fatalf("GraphQL rollback invalidation epoch=%d want=0", harness.capture.invalidationEpoch())
	}

	_, err = CallerCreate(vetoContext, caller, harness.postDescriptor, harness.createExactPost(t, 244, "go-veto", false))
	var publicFailure *golem.Error
	if !errors.As(err, &publicFailure) || publicFailure.Code != golem.CodeBadUserInput {
		t.Fatalf("Go caller rollback error=%#v (%v)", publicFailure, err)
	}
	assertP5MutationRollback(t, harness, 244, rollbackFacts)
	if caller.executor.invalidationEpoch() != 1 {
		t.Fatalf("Go caller rollback invalidation epoch=%d want committed epoch 1", caller.executor.invalidationEpoch())
	}
	if counters.before.Load() != 4 || counters.after.Load() != 4 || counters.afterCommit.Load() != 2 {
		t.Fatalf("hook phase counts before=%d after=%d afterCommit=%d", counters.before.Load(), counters.after.Load(), counters.afterCommit.Load())
	}
}

func newP5MutationHarness(t *testing.T, counters *p5MutationHookCounters, vetoAfter bool) p5MutationHarness {
	t.Helper()
	ctx := context.Background()
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "p5-graphql-mutation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	schema := schematest.NewMutationVocabulary(t)
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	bundle, compilation := p5GraphQLBundle(t, schema.Bundle)
	schema.Bundle = bundle
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}

	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle, schema.PostBigInt, schema.PostDecimal, schema.PostOptionalInt}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
	postTitle := golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle)
	bigInt := golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostBigInt)
	optionalInt := golem.GeneratedNullableOrderedField[mutationResultPost, int64](schema.PostOptionalInt)
	decimal := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal)
	optionalCreate := golem.GeneratedCreateFieldCapability(schema.Post, optionalInt)
	titleCreate := golem.GeneratedCreateFieldCapability(schema.Post, postTitle)
	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
			counters.before.Add(1)
			frozen, freezeErr := golem.RuntimeFreezeCreateInput(request.Input())
			if freezeErr != nil {
				return freezeErr
			}
			present, explicitNull := false, false
			for _, field := range frozen.Fields() {
				if field.FieldID() == schema.PostOptionalInt {
					present = true
					explicitNull = field.Operation() == golem.MutationFieldNull
				}
			}
			if !present {
				counters.omitted.Add(1)
				if setErr := golem.SetCreate(request, optionalCreate, int64(7)); setErr != nil {
					return setErr
				}
			} else if explicitNull {
				counters.explicitNil.Add(1)
			}
			if vetoAfter {
				return golem.SetCreate(request, titleCreate, "hooked")
			}
			return nil
		}),
	}
	if vetoAfter {
		hooks = append(hooks,
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, _ golem.CreateHookResult[mutationResultPost]) error {
				counters.after.Add(1)
				if hookContext.Value(p5MutationVetoKey{}) != nil {
					return errors.New("P5 mutation oracle veto")
				}
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
				counters.afterCommit.Add(1)
				return nil
			}),
		)
	}
	allowUser := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	allowPost := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUser, allowPost}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[mutationResultPrincipal, mutationResultActor]{
		DB: database, Provider: golem.SQLite, Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := mutationResultFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID), userName: userName,
		postID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID), authorID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID),
		title: postTitle, author: golem.GeneratedToOne[mutationResultPost, mutationResultUser](schema.PostAuthor, schema.Authorship, schema.User),
	}
	vocabulary := mutationVocabularyFixture{mutationResultFixture: base, bigInt: bigInt, optionalInt: optionalInt}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	capture := &p5MutationExecutionCapture{}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[mutationResultPrincipal]{
		Bundle: bundle,
		BeginCaller: func(operationContext context.Context, principal mutationResultPrincipal) (publicgraphql.CallerExecution, error) {
			caller, callerErr := app.ForPrincipal(operationContext, principal)
			if callerErr != nil {
				return nil, callerErr
			}
			execution, executionErr := NewCallerMutationExecution(caller,
				CallerMutationModel[mutationResultPrincipal, mutationResultActor](userDescriptor),
				CallerMutationModel[mutationResultPrincipal, mutationResultActor](postDescriptor),
			)
			if executionErr != nil {
				return nil, executionErr
			}
			capture.Lock()
			capture.execution = execution
			capture.err = nil
			capture.Unlock()
			return capture, nil
		},
		ReportInternalError: func(_ context.Context, report error) { t.Errorf("unexpected GraphQL executor error: %v", report) },
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[mutationResultPrincipal]{
		PrincipalFromContext: func(context.Context) (mutationResultPrincipal, bool) { return mutationResultPrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError:  func(_ context.Context, report error) { t.Errorf("unexpected GraphQL server error: %v", report) },
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return p5MutationHarness{mutationVocabularyFixture: vocabulary, server: server, decimal: decimal, sdl: document.SDL, compile: compilation, capture: capture}
}

func (h p5MutationHarness) execute(t *testing.T, ctx context.Context, query string) map[string]any {
	t.Helper()
	response := h.server.Execute(ctx, mutationResultPrincipal{}, publicgraphql.Request{Query: query})
	if len(response.Errors) != 0 || response.Data == nil {
		schema, schemaErr := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: h.sdl})
		document, parseErrs := gqlparser.LoadQuery(schema, query)
		var compileErr error
		if schemaErr == nil && len(parseErrs) == 0 {
			compiler, newErr := graphqloperation.New(h.compile, graphqloperation.Limits{})
			if newErr != nil {
				compileErr = newErr
			} else {
				_, compileErr = compiler.Compile(document, document.Operations[0], nil)
			}
		}
		t.Fatalf("GraphQL mutation failed: query=%s response=%#v schema=%v parse=%v compile=%v execute=%s", query, response, schemaErr, parseErrs, compileErr, p5ErrorChain(h.capture.lastError()))
	}
	return response.Data.(map[string]any)
}

func (h p5MutationHarness) createExactPost(t *testing.T, id byte, title string, explicitNull bool) golem.CreateInput[mutationResultPost] {
	t.Helper()
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	values := []golem.CreateValue[mutationResultPost]{
		golem.GeneratedCreateFieldValue(h.schema.Post, h.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(h.schema.Post, h.title, title),
		golem.GeneratedCreateFieldValue(h.schema.Post, h.bigInt, int64(10)),
		golem.GeneratedCreateFieldValue(h.schema.Post, h.decimal, decimal),
	}
	if explicitNull {
		values = append(values, golem.GeneratedCreateNullFieldValue(h.schema.Post, h.optionalInt))
	}
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](h.schema.User, h.schema.UserKey,
		golem.GeneratedSelectorComponent(h.schema.UserID, golem.UUID{15: 1}),
	)
	values = append(values, golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](h.schema.Post, h.schema.PostAuthor, h.schema.Authorship, h.schema.User, userTarget))
	return golem.GeneratedCreateInput(h.schema.Post, values...)
}

func assertP5MutationRow(t *testing.T, row golem.Row[mutationResultPost], h p5MutationHarness, title string, big int64, optionalNull bool) {
	t.Helper()
	if value, ok := golem.Value(row, h.title).Get(); !ok || value != title {
		t.Fatalf("title=%q present=%t want=%q", value, ok, title)
	}
	if value, ok := golem.Value(row, h.bigInt).Get(); !ok || value != big {
		t.Fatalf("bigInt=%d present=%t want=%d", value, ok, big)
	}
	optional := golem.Value(row, h.optionalInt)
	if optionalNull && (!optional.IsSelected() || !optional.IsNull()) {
		t.Fatalf("optionalInt state=%d, want selected null", optional.State())
	}
}

func assertP5PersistedPost(t *testing.T, h p5MutationHarness, id byte, title string, big int64, optional *int64) {
	t.Helper()
	var gotTitle string
	var gotBig int64
	var gotOptional *int64
	if err := h.app.database.QueryRowxContext(context.Background(), `SELECT "title","big_int","optional_int" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(id)).Scan(&gotTitle, &gotBig, &gotOptional); err != nil {
		t.Fatal(err)
	}
	if gotTitle != title || gotBig != big || (gotOptional == nil) != (optional == nil) || gotOptional != nil && *gotOptional != *optional {
		t.Fatalf("persisted post %d title=%q big=%d optional=%v want title=%q big=%d optional=%v", id, gotTitle, gotBig, gotOptional, title, big, optional)
	}
}

func p5MutationFactCount(t *testing.T, h p5MutationHarness) int {
	t.Helper()
	var count int
	if err := h.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertP5FactEnvelopeAndTransaction(t *testing.T, h p5MutationHarness, offset, amount int) {
	t.Helper()
	rows, err := h.app.database.QueryxContext(context.Background(), `SELECT "causation_id","transaction_ordinal","metadata" FROM "_golem_outbox" ORDER BY "recorded_at","event_id" LIMIT ? OFFSET ?`, amount, offset)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var causation string
	ordinal := int64(0)
	count := 0
	for rows.Next() {
		var current string
		var next int64
		var metadata []byte
		if err := rows.Scan(&current, &next, &metadata); err != nil {
			t.Fatal(err)
		}
		if causation == "" {
			causation = current
		}
		if current != causation || next != ordinal+1 || len(metadata) < 11 || !bytes.Equal(metadata[:9], []byte("GOLEMFACT")) {
			t.Fatalf("fact transaction current=%q causation=%q ordinal=%d previous=%d metadata=%x", current, causation, next, ordinal, metadata)
		}
		ordinal, count = next, count+1
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != amount {
		t.Fatalf("fact transaction rows=%d want=%d", count, amount)
	}
}

func assertP5MutationRollback(t *testing.T, h p5MutationHarness, id byte, facts int) {
	t.Helper()
	var rows int
	if err := h.app.database.GetContext(context.Background(), &rows, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || p5MutationFactCount(t, h) != facts {
		t.Fatalf("rollback id=%d rows=%d facts=%d/%d", id, rows, p5MutationFactCount(t, h), facts)
	}
}

func p5Int64Pointer(value int64) *int64 { return &value }

func p5ErrorChain(err error) string {
	if err == nil {
		return "<nil>"
	}
	result := ""
	for err != nil {
		if result != "" {
			result += " <- "
		}
		result += fmt.Sprintf("%T: %v", err, err)
		err = errors.Unwrap(err)
	}
	return result
}
