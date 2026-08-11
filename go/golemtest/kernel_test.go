package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

type kernelActor struct {
	Author golem.UUID
}

func kernelSecretSuffix() string { return "-secret" }

func kernelPostModel(t *testing.T) golem.ModelID {
	t.Helper()
	return p5social.GolemGeneratedPostDescriptor.Metadata().ModelID()
}

func kernelFactory(model, post golem.ModelID) golem.PolicyFactory[kernelActor] {
	return func(actor kernelActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p5social.Post]()
		rules.CanRead(golem.All[p5social.Post]())
		if model == post {
			rules.CannotRead(p5social.Posts.Title.EndsWith(kernelSecretSuffix()))
			rules.CanCreate(p5social.Posts.AuthorID.Eq(actor.Author))
			rules.CanUpdate(p5social.Posts.AuthorID.Eq(actor.Author))
		}
		return rules.Freeze(model)
	}
}

func kernelBindings(t *testing.T) golem.ApplicationBindings[kernelActor] {
	t.Helper()
	descriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	generation := p5social.GolemGeneratedSchemaBundle().GenerationDigest()
	post := kernelPostModel(t)
	policies := make([]golem.PolicyBinding[kernelActor], 0)
	for _, metadata := range descriptors.Models() {
		policies = append(policies, golem.GeneratedPolicyBinding[kernelActor, p5social.Post](metadata.ModelID(), kernelFactory(metadata.ModelID(), post)))
	}
	bindings, err := golem.GeneratedApplicationBindings(
		generation,
		golem.GeneratedStampedPackageBindings(generation, policies, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}

func kernelKit(t *testing.T) *Kit[kernelActor] {
	t.Helper()
	descriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	kit, err := New(kernelBindings(t), descriptors, p5social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return kit
}

func productionRegistry(t *testing.T, bundle golem.SchemaBundle) (*schema.Registry, ir.ProviderSet) {
	t.Helper()
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	providers := policysql.SchemaResolver(registry).Providers()
	if !providers.Valid() {
		t.Fatal("the production schema resolver produced no valid provider set")
	}
	return registry, providers
}

func productionPolicy[A any](t *testing.T, bindings golem.ApplicationBindings[A], actor A, bundle golem.SchemaBundle, model golem.ModelID) (ir.Policy, *schema.Registry, ir.ProviderSet) {
	t.Helper()
	registry, providers := productionRegistry(t, bundle)
	built, err := golem.BuildGeneratedPolicySet(bindings, actor)
	if err != nil {
		t.Fatalf("BuildGeneratedPolicySet: %v", err)
	}
	for _, frozen := range built.Policies() {
		if frozen.View().ModelID() != model {
			continue
		}
		bound, bindErr := bind.Policy(frozen, registry, providers)
		if bindErr != nil {
			t.Fatalf("bind.Policy: %v", bindErr)
		}
		normalized, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			t.Fatalf("normalize.Policy: %v", normalizeErr)
		}
		return normalized, registry, providers
	}
	t.Fatal("the production builder produced no policy for the requested model")
	return ir.Policy{}, nil, 0
}

func productionRowConstraint[A any](t *testing.T, bindings golem.ApplicationBindings[A], actor A, bundle golem.SchemaBundle, model golem.ModelID, action ir.Action) ir.Condition {
	t.Helper()
	policy, _, _ := productionPolicy(t, bindings, actor, bundle, model)
	condition, err := resolve.RowConstraint(policy, action, ir.ModelID(model))
	if err != nil {
		t.Fatalf("resolve.RowConstraint: %v", err)
	}
	return condition
}

func canonical(t *testing.T, condition ir.Condition) string {
	t.Helper()
	encoded, err := ir.CanonicalCondition(condition)
	if err != nil {
		t.Fatalf("ir.CanonicalCondition: %v", err)
	}
	return string(encoded)
}

func boundPredicate[M any](t *testing.T, predicate golem.Predicate[M], descriptor golem.ModelDescriptor[M], registry *schema.Registry, providers ir.ProviderSet) ir.Condition {
	t.Helper()
	frozen, err := predicate.Freeze(descriptor)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	condition, err := bind.Predicate(frozen, registry, providers)
	if err != nil {
		t.Fatalf("bind.Predicate: %v", err)
	}
	normalized, err := normalize.Condition(condition)
	if err != nil {
		t.Fatalf("normalize.Condition: %v", err)
	}
	return normalized
}
