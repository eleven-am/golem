package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type hookErrorActor struct{}
type hookErrorModel struct{}
type hookErrorRequest struct{}

func TestReadHookVetoUsesStableBadUserInputError(t *testing.T) {
	model := golem.ModelID{15: 1}
	digest := golem.SchemaDigest{0: 1}
	want := errors.New("application veto")
	hook := golem.GeneratedBeforeHookBinding[hookErrorActor, hookErrorModel, hookErrorRequest](model, golem.HookFindMany, func(context.Context, *hookErrorRequest) error {
		return want
	})
	pkg := golem.GeneratedStampedPackageBindings[hookErrorActor](digest, nil, []golem.HookBinding[hookErrorActor]{hook})
	bindings, err := golem.GeneratedApplicationBindings(digest, pkg)
	if err != nil {
		t.Fatal(err)
	}
	err = invokeReadHook(context.Background(), bindings, model, golem.ReadFindMany, golem.HookFindMany, golem.HookBefore, &hookErrorRequest{})
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || !errors.Is(err, want) {
		t.Fatalf("hook error=%v", err)
	}
	if public.Operation != "findMany" || public.Model != model || public.Message != "read hook rejected the operation" {
		t.Fatalf("public hook error=%#v", public)
	}
}

func TestMaskEvaluationFailureIsAnInternalInvariantNotAForbiddenDecision(t *testing.T) {
	want := errors.New("dependency missing")
	err := maskInvariantError(policyir.ModelID{15: 1}, policyir.FieldID{15: 2}, "field", want)
	var public *golem.Error
	if errors.As(err, &public) {
		t.Fatalf("mask invariant was mislabeled as public denial: %#v", public)
	}
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "P3_RUNTIME_MASK") {
		t.Fatalf("mask invariant error=%v", err)
	}
}
