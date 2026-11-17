package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/continuation"
	"github.com/eleven-am/golem/go/internal/operationargs"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
)

type pageRuntimeActor struct{ Subject, Profile, Revision int32 }
type pageRuntimeArguments struct{ Seed int32 }

func pageRuntimeResolver(ctx context.Context, caller *Caller[string, pageRuntimeActor], request golem.PageRequest[pageRuntimeArguments]) (golem.PageBatch[int32], error) {
	actor := golem.ActorFrom[pageRuntimeActor](ctx)
	if actor != caller.actor {
		return golem.PageBatch[int32]{}, fmt.Errorf("resolver did not receive the admitted actor")
	}
	return golem.PageItems([]int32{request.Arguments().Seed + int32(request.Consumed())}, 1, []byte(fmt.Sprint(request.Consumed()+1))), nil
}

// This is the dispatch/admission unit boundary, not a database authorization or
// ranked-feed fixture. The callback uses the actual typed resolver signature.
func pageRuntimeFixture(t *testing.T) (*Caller[string, pageRuntimeActor], func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error), *int) {
	t.Helper()
	item := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}
	pagination := compilerir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "rank-1", Item: item}
	operation := compilerir.CustomOperationContractIR{
		ExtensionID: "page", Operation: compilerir.CustomOperationQuery, Name: "feed", Pagination: &pagination,
		Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "FeedPage"}, Capability: compilerir.CustomOperationCallerOnly,
		Resolver: compilerir.AttachedMethodIR{PackagePath: "example.test/pages", Name: "pageRuntimeResolver", Kind: "customquery"},
		Arguments: []compilerir.GraphQLArgumentContractIR{
			{Name: "seed", Type: item},
			{Name: "take", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "after", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}},
		},
	}
	var compilation compilerir.CompilationIR
	compilation.Model.Schema.PackagePath = "example.test/pages"
	compilation.Contract.DataIncarnation = &compilerir.DataIncarnationContractIR{Kind: "provider", Version: 1}
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{operation}
	compilation.Contract.GraphQLTypes = []compilerir.GraphQLTypeDefinitionIR{compilerir.GraphQLPageDefinition("FeedPage", pagination)}
	registry, err := operationargs.New(compilation)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := continuation.New(continuation.Config{ActiveKey: "current", Keys: []continuation.Key{{ID: "current", Material: [32]byte{1}}}, MaxAge: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := golem.GeneratedApplicationBindings[pageRuntimeActor](golem.SchemaDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err = golem.GeneratedApplicationPageQueries(bindings, golem.GeneratedPageQueryOperation("page", pageRuntimeResolver))
	if err != nil {
		t.Fatal(err)
	}
	caller := &Caller[string, pageRuntimeActor]{execution: 1, actor: pageRuntimeActor{1, 2, 3}, policies: &policyruntime.Set{}, app: &App[string, pageRuntimeActor]{bindings: bindings}}
	caller.app.pages = &pageRuntime[pageRuntimeActor]{codec: codec, registry: registry, operations: map[string]compilerir.CustomOperationContractIR{"page": operation}, contract: "contract", incarnation: "database-1", scope: func(ctx context.Context, actor pageRuntimeActor) (string, error) {
		contextActor := golem.ActorFrom[pageRuntimeActor](ctx)
		if contextActor != actor {
			return "", fmt.Errorf("scope did not receive the admitted actor")
		}
		return fmt.Sprintf("%d/%d/%d", actor.Subject, actor.Profile, actor.Revision), nil
	}}
	calls := 0
	run := func(ctx context.Context, state golem.RuntimePageState, arguments map[string]any) (golem.PageBatch[any], error) {
		calls++
		args := pageRuntimeArguments{Seed: arguments["seed"].(int32)}
		batch, err := pageRuntimeResolver(ctx, caller, golem.RuntimePageRequestValue(args, state))
		if err != nil {
			return golem.PageBatch[any]{}, err
		}
		items, consumed, next, _ := golem.RuntimePageBatchParts(batch)
		erased := make([]any, len(items))
		for index := range items {
			erased[index] = items[index]
		}
		return golem.PageItems(erased, consumed, next), nil
	}
	return caller, run, &calls
}

func TestPageDispatchAdmitsActorAndFinalizesOnce(t *testing.T) {
	caller, run, calls := pageRuntimeFixture(t)
	ctx := golem.RuntimeContextWithActor(context.Background(), pageRuntimeActor{9, 9, 9})
	result, err := DispatchCallerPage(ctx, caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{}, run)
	if err != nil || *calls != 1 || len(result.Items()) != 1 || result.Items()[0] != int32(7) {
		t.Fatalf("first dispatch failed: calls=%d err=%v", *calls, err)
	}
	token, err := result.Finalize()
	if err != nil || token == "" {
		t.Fatalf("checkpoint not finalized: %v", err)
	}
	if retry, err := result.Finalize(); err != nil || retry != token {
		t.Fatalf("result finalized twice with different state: %v", err)
	}
	for range 2 {
		next, err := DispatchCallerPage(ctx, caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{After: token}, run)
		if err != nil || next.Items()[0] != int32(8) {
			t.Fatalf("retryable continuation failed: %v", err)
		}
	}
}

func TestPageDispatchRejectsMismatchesBeforeResolver(t *testing.T) {
	caller, run, calls := pageRuntimeFixture(t)
	first, err := DispatchCallerPage(context.Background(), caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{}, run)
	if err != nil {
		t.Fatal(err)
	}
	token, err := first.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Caller[string, pageRuntimeActor], map[string]any, *golem.PageInput){
		"arguments": func(_ *Caller[string, pageRuntimeActor], args map[string]any, _ *golem.PageInput) {
			args["seed"] = int32(8)
		},
		"take":     func(_ *Caller[string, pageRuntimeActor], _ map[string]any, input *golem.PageInput) { input.Take = 26 },
		"profile":  func(c *Caller[string, pageRuntimeActor], _ map[string]any, _ *golem.PageInput) { c.actor.Profile++ },
		"revision": func(c *Caller[string, pageRuntimeActor], _ map[string]any, _ *golem.PageInput) { c.actor.Revision++ },
		"tamper": func(_ *Caller[string, pageRuntimeActor], _ map[string]any, input *golem.PageInput) {
			input.After += "!"
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := *caller
			args, input := map[string]any{"seed": int32(7)}, golem.PageInput{After: token}
			mutate(&copy, args, &input)
			before := *calls
			_, err := DispatchCallerPage(context.Background(), &copy, "page", pageRuntimeResolver, args, input, run)
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Reason != golem.ReasonInvalidValue || failure.InputField != "after" || *calls != before {
				t.Fatalf("mismatch reached resolver or lost public error: calls=%d err=%v", *calls, err)
			}
		})
	}
	before := *calls
	if _, err := DispatchCallerPage(context.Background(), caller, "other", pageRuntimeResolver, nil, golem.PageInput{}, run); err == nil || *calls != before {
		t.Fatal("unregistered operation reached resolver")
	}
}

func TestPageDispatchEnforcesBudgetsAndEmptyTraversalProgress(t *testing.T) {
	caller, _, _ := pageRuntimeFixture(t)
	for name, batch := range map[string]golem.PageBatch[any]{
		"too many items":      golem.PageItems(make([]any, 26), 26, nil),
		"too many candidates": golem.PageItems[any](nil, 201, []byte("next")),
		"unconsumed items":    golem.PageItems([]any{int32(1)}, 0, nil),
		"zero progress":       golem.PageItems[any](nil, 0, []byte("next")),
		"empty checkpoint":    golem.PageItems[any](nil, 1, []byte{}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DispatchCallerPage(context.Background(), caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{}, func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error) {
				return batch, nil
			}); err == nil {
				t.Fatal("invalid result admitted")
			}
		})
	}
	result, err := DispatchCallerPage(context.Background(), caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{}, func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error) {
		return golem.PageItems[any](nil, 25, []byte("25")), nil
	})
	if err != nil || len(result.Items()) != 0 {
		t.Fatalf("empty nonterminal page refused: %v", err)
	}
	token, err := result.Finalize()
	if err != nil || token == "" {
		t.Fatalf("empty page lost traversal checkpoint: %v", err)
	}
	_, err = DispatchCallerPage(context.Background(), caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{After: token}, func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error) {
		return golem.PageItems[any](nil, 1, []byte("25")), nil
	})
	if err == nil {
		t.Fatal("unchanged private checkpoint admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pending, err := DispatchCallerPage(ctx, caller, "page", pageRuntimeResolver, map[string]any{"seed": int32(7)}, golem.PageInput{}, func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error) {
		return golem.PageItems[any](nil, 1, []byte("1")), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if token, err := pending.Finalize(); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result issued a continuation: %v", err)
	}
	var failure *golem.Error
	if !errors.As(pageContinuationError("feed", continuation.ErrExpired), &failure) || failure.Reason != golem.ReasonContinuationExpired {
		t.Fatal("expired continuation lacks a typed restart reason")
	}
}

func TestPageDispatchBoundsRawArgumentIngressBeforeCopy(t *testing.T) {
	caller, run, calls := pageRuntimeFixture(t)
	wide := make(map[string]any, 100)
	for index := range 100 {
		wide[fmt.Sprint(index)] = int32(index)
	}
	for _, arguments := range []map[string]any{wide, {strings.Repeat("x", operationargs.MaxCanonicalArgumentBytes+1): int32(7)}} {
		_, err := DispatchCallerPage(context.Background(), caller, "page", pageRuntimeResolver, arguments, golem.PageInput{}, run)
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Reason != golem.ReasonLimitExceeded || *calls != 0 {
			t.Fatalf("unbounded raw argument ingress admitted or lost its limit error: %v", err)
		}
	}
}
