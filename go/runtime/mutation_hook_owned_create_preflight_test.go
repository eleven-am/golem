package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

func TestCallerCreatePreHookDefersOnlyGeneratedHookOwnedFields(t *testing.T) {
	ctx := context.Background()
	missingAuthor := func(fixture mutationResultFixture, id byte, title ...string) golem.CreateInput[mutationResultPost] {
		values := []golem.CreateValue[mutationResultPost]{
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		}
		if len(title) != 0 {
			values = append(values, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title[0]))
		}
		return golem.GeneratedCreateInput(fixture.schema.Post, values...)
	}

	t.Run("relation-free-create-defers-until-before-create", func(t *testing.T) {
		var before atomic.Int64
		fixture := mutationResultHookOwnedFixture(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
			capability := golem.GeneratedCreateFieldCapability(schema.Post, author)
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
					before.Add(1)
					return golem.SetCreate(request, capability, golem.UUID{15: 1})
				}),
			}
		})
		row, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, missingAuthor(fixture, 81, "plain"), golem.Select[mutationResultPost](fixture.authorID, fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		if author, present := golem.Value(row, fixture.authorID).Get(); !present || author != (golem.UUID{15: 1}) || before.Load() != 1 {
			t.Fatalf("hook-owned plain create author=%v present=%t before=%d", author, present, before.Load())
		}
	})

	t.Run("ordinary-missing-required-field-refuses-before-hook-sql", func(t *testing.T) {
		var before atomic.Int64
		fixture := mutationResultHookOwnedFixture(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
			capability := golem.GeneratedCreateFieldCapability(schema.Post, author)
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
					before.Add(1)
					return golem.SetCreate(request, capability, golem.UUID{15: 1})
				}),
			}
		})
		collector := &p8ObservationCollector{}
		fixture.app.observer = collector
		_, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, missingAuthor(fixture, 82))
		assertHookOwnedCreatePreflightRefusal(t, err, before.Load(), collector)
	})

	t.Run("before-create-omission-refuses-during-strict-post-hook-bind", func(t *testing.T) {
		var before atomic.Int64
		fixture := mutationResultHookOwnedFixture(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
					before.Add(1)
					return nil
				}),
			}
		})
		collector := &p8ObservationCollector{}
		fixture.app.observer = collector
		_, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, missingAuthor(fixture, 85, "omitted"))
		var public *golem.Error
		values := collector.matching(observe.KindMutation, observe.OperationMutationCreate)
		if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || before.Load() != 1 || len(values) != 1 || values[0].statements != 0 || values[0].outcome != observe.OutcomeRefused {
			t.Fatalf("post-hook strict refusal error=%v before=%d observations=%+v", err, before.Load(), values)
		}
	})

	t.Run("hook-owned-contract-without-hook-refuses-before-sql", func(t *testing.T) {
		fixture := newMutationResultFixtureWithHooksAndDatabaseMode(t, MutationLimits{}, nil, nil, nil, true, true)
		collector := &p8ObservationCollector{}
		fixture.app.observer = collector
		_, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, missingAuthor(fixture, 83, "no-hook"))
		assertHookOwnedCreatePreflightRefusal(t, err, 0, collector)
	})

	t.Run("system-create-never-receives-caller-hook-deferral", func(t *testing.T) {
		var before atomic.Int64
		fixture := mutationResultHookOwnedFixture(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
					before.Add(1)
					return nil
				}),
			}
		})
		_, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, missingAuthor(fixture, 84, "system"))
		var public *golem.Error
		if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || before.Load() != 0 {
			t.Fatalf("system hook-owned create error=%v before=%d", err, before.Load())
		}
	})
}

func TestCallerNestedCreatePreHookDefersHookOwnedRootAndRemainsStrictAfterHook(t *testing.T) {
	ctx := context.Background()
	schema := withGraphHookOwnedPostAuthor(t, schematest.NewSubscribedGraph(t))
	var before atomic.Int64
	author := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.AuthorID)
	capability := golem.GeneratedCreateFieldCapability(schema.Post, author)
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationPost]) error {
			before.Add(1)
			return golem.SetCreate(request, capability, golem.UUID{15: 91})
		}),
	}
	fixture := newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 91}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "hook-author"),
	)); err != nil {
		t.Fatal(err)
	}
	comment := golem.GeneratedCreateInput[graphMutationComment](schema.Comment,
		golem.GeneratedCreateFieldValue(schema.Comment, fixture.commentID, golem.UUID{15: 93}),
		golem.GeneratedCreateFieldValue(schema.Comment, fixture.commentBody, "nested-comment"),
	)
	post := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 92}),
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "nested-post"),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationComment](schema.Post, schema.PostComments, schema.Commenting, schema.Comment, comment),
	)
	if _, err := CallerCreate(ctx, mustGraphMutationCaller(t, fixture), fixture.postDescriptor, post); err != nil {
		t.Fatal(err)
	}
	if before.Load() != 1 {
		t.Fatalf("nested root BeforeCreate calls=%d want=1", before.Load())
	}
	var storedAuthor, commentPost string
	if err := fixture.app.database.GetContext(ctx, &storedAuthor, `SELECT "author_id" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(92)); err != nil || storedAuthor != mutationResultUUIDText(91) {
		t.Fatalf("nested hook-owned author=%q error=%v", storedAuthor, err)
	}
	if err := fixture.app.database.GetContext(ctx, &commentPost, `SELECT "post_id" FROM "comments" WHERE "id"=?`, mutationResultUUIDText(93)); err != nil || commentPost != mutationResultUUIDText(92) {
		t.Fatalf("nested comment post=%q error=%v", commentPost, err)
	}
}

func assertHookOwnedCreatePreflightRefusal(t testing.TB, err error, before int64, collector *p8ObservationCollector) {
	t.Helper()
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || before != 0 {
		t.Fatalf("hook-owned create refusal error=%v before=%d", err, before)
	}
	values := collector.matching(observe.KindMutation, observe.OperationMutationCreate)
	if len(values) != 1 || values[0].statements != 0 || values[0].outcome != observe.OutcomeRefused {
		t.Fatalf("hook-owned create refusal observations=%+v", values)
	}
}

func mustGraphMutationCaller(t testing.TB, fixture graphMutationFixture) *Caller[graphMutationPrincipal, graphMutationActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func withGraphHookOwnedPostAuthor(t testing.TB, fixture schematest.GraphFixture) schematest.GraphFixture {
	t.Helper()
	contractDocument := fixture.Bundle.Contract()
	var contract compilerir.ContractIR
	if err := json.Unmarshal(contractDocument.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	found := false
	for index := range contract.Models {
		if contract.Models[index].ModelID == compilerir.ModelID(fmt.Sprintf("%x", fixture.Post[:])) {
			contract.Models[index].HookOwnedCreateFields = []compilerir.FieldID{compilerir.FieldID(fmt.Sprintf("%x", fixture.AuthorID[:]))}
			found = true
		}
	}
	if !found {
		t.Fatal("graph Post contract is absent")
	}
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("contract fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	contractDocument = golem.GeneratedSchemaDocument(contractDocument.FormatVersion(), contractDocument.CanonicalVersion(), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), contractDocument, fixture.Bundle.Providers()...,
	)
	registry, err := policyschema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Registry = registry
	return fixture
}
