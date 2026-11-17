package capture_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/capture"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type user struct{}
type post struct{}

func TestValidatorUsesCallerCancellationEvenForCoveredQuestions(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	name := golem.GeneratedTextField[user, string](fixture.UserName)
	rules := golem.NewRules[user]()
	rules.FactScope(name.Eq("source"))
	rules.CanRead(rules.Fact("local", name.Eq("ready")))
	frozen, err := rules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := bind.Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	steps := 0
	validator := capture.NewValidator(fixture.Registry, func(count int) error {
		steps += count
		return ctx.Err()
	})
	definition := policy.Facts()[0]
	if err := validator.Owner(policy.FactScope(), definition.Body(), definition.ID(), nil); err != nil || steps == 0 {
		t.Fatalf("first coverage proof: steps=%d err=%v", steps, err)
	}
	cancel()
	if err := validator.Owner(policy.FactScope(), definition.Body(), definition.ID(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("memoized static coverage bypassed caller cancellation: %v", err)
	}
}

func TestCapturedLocalScopeDoesNotRequireOrInventAnOwnerRoot(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	author := golem.GeneratedEqualField[post, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[post, string](fixture.PostTitle)
	rules := golem.NewRules[post]()
	rules.FactScope(author.Eq(golem.UUID{15: 1}))
	rules.CanRead(rules.Fact("local", title.Eq("ready")))
	frozen, err := rules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := bind.Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	validator := capture.NewValidator(fixture.Registry, nil)
	if err := validator.CapturedScope(policy.FactScope()); err != nil {
		t.Fatalf("local captured-row scope was forced into a root template: %v", err)
	}
	definition := policy.Facts()[0]
	var missing *capture.Error
	if err := validator.Owner(policy.FactScope(), definition.Body(), definition.ID(), nil); !errors.As(err, &missing) {
		t.Fatalf("captured-row proof invented rooted definition coverage: %v", err)
	}
	// The validator neither changes producer inventory nor claims that a row
	// exists; the ordinary occurrence still needs its exact graph evaluation.
	if len(fixture.Registry.PolicyCaptureProjections(fixture.Post)) != 0 {
		t.Fatal("captured participant was promoted into a static owner")
	}
}

func TestPolicyCoverageHasOneAggregateDeclarationBudget(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	model := ir.ModelID(fixture.User)
	truth, err := ir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ir.NewFactScope(truth)
	if err != nil {
		t.Fatal(err)
	}
	definitions := make([]ir.FactDefinition, 5000)
	for index := range definitions {
		definitions[index], err = ir.NewFactDefinition(model, fmt.Sprintf("unused%d", index), truth)
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := ir.NewPolicyWithFacts(model, nil, definitions, scope)
	if err != nil {
		t.Fatal(err)
	}
	var limit *ir.FactLimitError
	if err := capture.ValidatePolicy(fixture.Registry, policy); !errors.As(err, &limit) || limit.Limit != ir.FactExpansionNodes {
		t.Fatalf("separate cheap definitions reset the aggregate work budget: %v", err)
	}
}
