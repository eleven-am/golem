package golemtest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

var secretCorpus = []string{
	"alice@example.invalid",
	"Bearer sk-live-9911aabbcc",
	"session-8ff21c0d",
	"tenant-7f3a",
	"golem_production_db",
	"row-value-77",
	"hunter2",
	"gamma-panic-payload",
	"delta-factory-cause",
	"epsilon-nested-cause",
	"zeta-stringer-payload",
	"192.0.2.44",
}

var internalNameCorpus = []string{
	"internal/policy",
	"schema.Registry",
	"ir.Policy",
	"ir.Condition",
	"frozenRule",
	"frozenCondition",
	"frozenPolicyView",
	"kitError",
	"GeneratedPolicySet",
	"BuildGeneratedPolicySet",
	"golem.FrozenPolicy",
	"PolicyFactory",
	"syntheticActor",
	"runtime/testdata",
	"policy set: model",
	"generated policy set",
}

type secretStringer struct{ value string }

func (value secretStringer) String() string { return value.value }

func redactionActor() syntheticActor {
	return syntheticActor{
		Email:    "alice@example.invalid",
		Token:    "Bearer sk-live-9911aabbcc",
		Tenant:   "tenant-7f3a",
		Session:  "session-8ff21c0d",
		Database: "golem_production_db",
		Row:      "row-value-77",
	}
}

func assertRedacted(t *testing.T, err error, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error was produced", context)
	}
	renderings := []string{
		err.Error(),
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%s", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%q", err),
		fmt.Sprintf("%#v", err),
	}
	for _, rendering := range renderings {
		for _, secret := range secretCorpus {
			if strings.Contains(rendering, secret) {
				t.Fatalf("%s: an error rendering leaked a redacted value", context)
			}
		}
		for _, name := range internalNameCorpus {
			if strings.Contains(rendering, name) {
				t.Fatalf("%s: an error rendering leaked the internal name %q", context, name)
			}
		}
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Fatalf("%s: the error exposes a wrapped cause", context)
	}
}

func TestPolicyTestKitFactoryPanicAndErrorsAreClosedAndRedacted(t *testing.T) {
	everyModel := func(build golem.PolicyFactory[syntheticActor]) func(golem.ModelID) golem.PolicyFactory[syntheticActor] {
		return func(golem.ModelID) golem.PolicyFactory[syntheticActor] { return build }
	}

	factories := map[string]func(golem.ModelID) golem.PolicyFactory[syntheticActor]{
		"PanicWithString": everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			panic("gamma-panic-payload")
		}),
		"PanicWithError": everyModel(func(actor syntheticActor) (golem.FrozenPolicy, error) {
			panic(fmt.Errorf("gamma-panic-payload for %s with %s", actor.Email, actor.Token))
		}),
		"PanicWithActor": everyModel(func(actor syntheticActor) (golem.FrozenPolicy, error) {
			panic(actor)
		}),
		"PanicWithStringer": everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			panic(secretStringer{value: "zeta-stringer-payload"})
		}),
		"PanicWithNil": everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			panic(nil)
		}),
		"ReturnsCause": everyModel(func(actor syntheticActor) (golem.FrozenPolicy, error) {
			return golem.FrozenPolicy{}, fmt.Errorf("delta-factory-cause: tenant %s session %s row %s", actor.Tenant, actor.Session, actor.Row)
		}),
		"ReturnsNestedCause": everyModel(func(actor syntheticActor) (golem.FrozenPolicy, error) {
			inner := fmt.Errorf("epsilon-nested-cause on %s", actor.Database)
			return golem.FrozenPolicy{}, fmt.Errorf("delta-factory-cause: %w", inner)
		}),
		"ReturnsForeignModelPolicy": everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[p6metrics.Category]()
			rules.CanRead(golem.All[p6metrics.Category]())
			policy, err := rules.Freeze(golem.ModelID{0x99, 0x98, 0x97, 0x96, 0x95, 0x94, 0x93, 0x92, 0x91, 0x90, 0x8f, 0x8e, 0x8d, 0x8c, 0x8b, 0x8a})
			return policy, err
		}),
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			kit := syntheticKit(t, factory)
			_, err := kit.ForActor(redactionActor())
			assertRedacted(t, err, name)
			code, ok := CodeOf(err)
			if !ok {
				t.Fatalf("%s: error carries no classification", name)
			}
			if code != ErrorPolicyFactory {
				t.Fatalf("%s: classified %s, want %s", name, code, ErrorPolicyFactory)
			}
		})
	}

	t.Run("SucceedingFactoryIsUnaffected", func(t *testing.T) {
		kit := syntheticKit(t, syntheticOpenFactory(t))
		policies, err := kit.ForActor(redactionActor())
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		if _, err := Model(policies, syntheticDescriptor()); err != nil {
			t.Fatalf("Model: %v", err)
		}
	})

	t.Run("ClassificationSurvivesOrdinaryWrapping", func(t *testing.T) {
		kit := syntheticKit(t, everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			panic("gamma-panic-payload")
		}))
		_, err := kit.ForActor(redactionActor())
		if err == nil {
			t.Fatal("a panicking factory produced no error")
		}
		wrapped := fmt.Errorf("application layer: %w", err)
		doubled := fmt.Errorf("transport layer: %w", wrapped)
		joined := errors.Join(errors.New("unrelated"), doubled)
		for label, candidate := range map[string]error{"single": wrapped, "double": doubled, "joined": joined} {
			code, ok := CodeOf(candidate)
			if !ok {
				t.Fatalf("%s wrapping lost the classification", label)
			}
			if code != ErrorPolicyFactory {
				t.Fatalf("%s wrapping classified %s, want %s", label, code, ErrorPolicyFactory)
			}
		}
	})

	t.Run("UnrelatedErrorsCarryNoClassification", func(t *testing.T) {
		if _, ok := CodeOf(nil); ok {
			t.Fatal("nil carries a classification")
		}
		if _, ok := CodeOf(errors.New("application failure")); ok {
			t.Fatal("an unrelated error carries a classification")
		}
		if _, ok := CodeOf(fmt.Errorf("wrapped: %w", errors.New("application failure"))); ok {
			t.Fatal("an unrelated wrapped error carries a classification")
		}
	})

	t.Run("ConstructionErrorsAreClosedAndRedacted", func(t *testing.T) {
		generation := syntheticGeneration()
		metadata := syntheticDescriptor().Metadata()
		descriptors, err := golem.GeneratedApplicationDescriptors(
			generation,
			golem.GeneratedStampedPackageDescriptors(generation, metadata, metadata),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, buildErr := New(syntheticBindings(t, syntheticOpenFactory(t)), descriptors, syntheticBundle())
		assertRedacted(t, buildErr, "duplicate model descriptor")
		if code, ok := CodeOf(buildErr); !ok || code != ErrorInvalidInput {
			t.Fatalf("duplicate model descriptor classified %s ok=%v, want %s", code, ok, ErrorInvalidInput)
		}
	})

	t.Run("RealApplicationErrorsAreClosedAndRedacted", func(t *testing.T) {
		kit := socialKit(t)
		policies, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"})
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		_, modelErr := Model(policies, syntheticDescriptor())
		assertRedacted(t, modelErr, "foreign descriptor")
		if code, ok := CodeOf(modelErr); !ok || code != ErrorGenerationMismatch {
			t.Fatalf("foreign descriptor classified %s ok=%v, want %s", code, ok, ErrorGenerationMismatch)
		}
	})

	t.Run("ErrorTextIsTheClosedCodeVocabulary", func(t *testing.T) {
		kit := syntheticKit(t, everyModel(func(syntheticActor) (golem.FrozenPolicy, error) {
			panic("gamma-panic-payload")
		}))
		_, err := kit.ForActor(redactionActor())
		if err == nil {
			t.Fatal("a panicking factory produced no error")
		}
		if !strings.HasPrefix(err.Error(), "golemtest: "+string(ErrorPolicyFactory)+": ") {
			t.Fatalf("error text does not open with its closed classification")
		}
	})
}
