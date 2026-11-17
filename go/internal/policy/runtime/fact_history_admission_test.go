package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

// A reader-only binary must not activate newly introduced named facts through
// the legacy image/live-join observer. Registered capture metadata and enabled
// subscriptions are necessary evidence inventory, not writer activation.
func TestNamedFactAdmissionRequiresHistoricalMode(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	if fixture.Registry.PolicyCaptureFingerprint() == (golem.SchemaDigest{}) {
		t.Fatal("fixture must have a real registered capture contract")
	}
	for _, id := range []golem.ModelID{fixture.User, fixture.Post, fixture.Comment} {
		model, present := fixture.Registry.Model(id)
		if !present || !model.SubscriptionsEnabled() {
			t.Fatal("fixture fact participants must all be subscribed")
		}
	}
	name := golem.GeneratedModeTextField[runtimeUser, string](fixture.UserName)
	title := golem.GeneratedModeTextField[runtimePost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	owner, _ := fixture.Registry.Model(fixture.User)
	if len(owner.ScopeTemplates()) != 1 || len(owner.FactTemplates()) != 1 {
		t.Fatal("fixture must retain its actual named/ready templates")
	}
	scopeTemplate, factTemplate := owner.ScopeTemplates()[0], owner.FactTemplates()[0]
	for _, declaration := range []string{"related-body", "bound-related-body", "scalar-body", "unused", "constant-false", "operation-only", "ordinary"} {
		t.Run(declaration, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.User, func(actor runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				if declaration == "ordinary" {
					rules.CanRead(name.Eq(actor.Name))
					return rules.Freeze(fixture.User)
				}
				body := posts.Some(title.Eq("ready"))
				if declaration == "scalar-body" {
					body = name.Eq("ready")
				} else if declaration == "constant-false" {
					body = golem.None[runtimeUser]()
				}
				var fact golem.Predicate[runtimeUser]
				if declaration == "bound-related-body" {
					rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.User, string(scopeTemplate.ID), runtimeDigest(t, string(scopeTemplate.Fingerprint)), name.Eq(actor.Name), golem.GeneratedTemplateArgument("Name", actor.Name)))
					fact = rules.BindFact(golem.GeneratedBoundFact(fixture.User, string(factTemplate.ID), runtimeDigest(t, string(factTemplate.Fingerprint)), factTemplate.Name, body, golem.GeneratedTemplateArgument("State", "ready")))
				} else {
					rules.FactScope(name.Eq(actor.Name))
					fact = rules.Fact("ready", body)
				}
				switch declaration {
				case "unused":
					rules.CanRead(golem.All[runtimeUser]())
				case "operation-only":
					rules.CanRead(golem.All[runtimeUser]())
					golem.Within(rules, runtimeInviteOperation).CanCreate(fact)
				default:
					rules.CanRead(fact)
				}
				return rules.Freeze(fixture.User)
			})
			bindings := runtimeBindings(t, fixture.Registry.GenerationDigest(), binding)
			if declaration == "operation-only" {
				var err error
				bindings, err = golem.GeneratedApplicationOperations(bindings, golem.GeneratedCustomMutationOperation("invite", runtimeInviteOperation))
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, historical := range []bool{false, true} {
				mode := "reader-only"
				constructor := Build[runtimeActor]
				if historical {
					mode, constructor = "historical", BuildHistorical[runtimeActor]
				}
				t.Run(mode, func(t *testing.T) {
					set, err := constructor(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{Name: "source"}, Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, fixture.Registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
					if err != nil {
						t.Fatal(err)
					}
					policy, present := set.Policy(policyir.ModelID(fixture.User))
					failure := runtimePolicyError(t, set, policyir.ModelID(fixture.User))
					if !historical && declaration != "ordinary" {
						var evidence *policyir.FactScopeEvidenceError
						if present || !errors.As(failure, &evidence) || evidence.ModelID != policyir.ModelID(fixture.User) {
							t.Fatalf("reader-only policy activated named fact %q: present=%v err=%v", declaration, present, failure)
						}
						return
					}
					if !present || failure != nil || (len(policy.Facts()) != 0) != (declaration != "ordinary") {
						t.Fatalf("admitted control changed: declaration=%s present=%v facts=%d err=%v", declaration, present, len(policy.Facts()), failure)
					}
					if declaration == "operation-only" {
						operation, found := bindings.Operation(runtimeInviteOperation)
						if !found {
							t.Fatal("operation inventory is missing")
						}
						scoped, release := set.Within(operation)
						defer release()
						if policy, present := scoped.Policy(policyir.ModelID(fixture.User)); !present || len(policy.Facts()) == 0 {
							t.Fatal("historical operation lost its fact definition")
						}
					}
				})
			}
		})
	}
}
