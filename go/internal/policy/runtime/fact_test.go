package runtime

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestRelatedAuthorityCannotActivateWithoutHistoricalEvidence(t *testing.T) {
	registry, fixture := runtimeSubscribedFactFixture(t)
	var author golem.FieldID
	var relation golem.RelationID
	model, _ := registry.Model(fixture.post)
	for _, id := range model.Fields() {
		field, _ := registry.Field(fixture.post, id)
		if related, present := field.RelationID(); present {
			endpoint, ok := registry.RelationEndpoint(fixture.post, id, related)
			if ok && endpoint.TargetModelID() == fixture.user {
				author, relation = id, related
			}
		}
	}
	if author == (golem.FieldID{}) || relation == (golem.RelationID{}) {
		t.Fatal("fixture is missing its real Post-to-User relation")
	}
	path := golem.GeneratedToOne[runtimePost, runtimeUser](author, relation, fixture.user)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimePost](fixture.post, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimePost]()
		rules.FactScope(path.Is(golem.All[runtimeUser]()))
		rules.Fact("constant", golem.All[runtimePost]())
		rules.CanRead(golem.All[runtimePost]())
		return rules.Freeze(fixture.post)
	})
	set, err := Build(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.generation, binding), Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	var evidence *policyir.FactScopeEvidenceError
	if err := runtimePolicyError(t, set, policyir.ModelID(fixture.post)); !errors.As(err, &evidence) {
		t.Fatalf("related scope activated without historical route evidence: %v", err)
	}
}

func TestFactFactoriesActivateAndKeepFreshActorScopes(t *testing.T) {
	registry, fixture := runtimeSubscribedFactFixture(t)
	handle := golem.GeneratedModeTextField[runtimeUser, string](fixture.userHandle)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(actor runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.FactScope(handle.Eq(actor.Name))
		ready := rules.Fact("ready", handle.Eq("ada"))
		rules.CanRead(ready.Not())
		return rules.Freeze(fixture.user)
	})
	bindings := runtimeBindings(t, fixture.generation, binding)
	for _, actor := range []string{"ada", "grace", "ada"} {
		set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{Name: actor}, Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
		if err != nil {
			t.Fatal(err)
		}
		policy, ok := set.Policy(policyir.ModelID(fixture.user))
		if !ok {
			t.Fatal(runtimePolicyError(t, set, policyir.ModelID(fixture.user)))
		}
		condition, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(fixture.user))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ada", "grace"} {
			value, _ := policyir.StringValue(name)
			field, _ := evaluate.ValueField(policyir.FieldID(fixture.userHandle), value)
			record, _ := evaluate.NewRecord(policyir.ModelID(fixture.user), field)
			got, err := evaluate.Condition(condition, record, policyir.PortableProviders())
			if want := name == actor && name != "ada"; err != nil || got != want {
				t.Fatalf("actor=%q name=%q result=%v want=%v err=%v", actor, name, got, want, err)
			}
		}
	}
}

func TestFactActivationChecksScopeAndUnusedDefinitionCapabilities(t *testing.T) {
	registry, fixture := runtimeSubscribedFactFixture(t)
	handle := golem.GeneratedModeTextField[runtimeUser, string](fixture.userHandle)
	for _, scopeNeedsText := range []bool{false, true} {
		name := "unused-definition"
		if scopeNeedsText {
			name = "scope"
		}
		t.Run(name, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(actor runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				scope, body := golem.All[runtimeUser](), handle.Eq(actor.Name)
				if scopeNeedsText {
					scope, body = body, scope
				}
				rules.FactScope(scope)
				rules.Fact("unused", body)
				rules.CanRead(golem.All[runtimeUser]())
				return rules.Freeze(fixture.user)
			})
			bindings := runtimeBindings(t, fixture.generation, binding)
			set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{Name: "ada"}, Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL)})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtimePolicyError(t, set, policyir.ModelID(fixture.user)); err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_CAPABILITY") {
				t.Fatalf("scope=%v missing fact capability was accepted: %v", scopeNeedsText, err)
			}
		})
	}
}

type runtimeFactComment struct{}

func TestFactActivationRequiresSubscribedOwnerAndEveryAuthoredParticipant(t *testing.T) {
	fixture := schematest.NewSubscribedGraph(t)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[runtimePost, runtimeUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	comments := golem.GeneratedToMany[runtimePost, runtimeFactComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
	bodyField := golem.GeneratedModeTextField[runtimeFactComment, string](fixture.CommentBody)
	for _, test := range []struct {
		name     string
		disabled golem.ModelID
	}{
		{"constant-owner", fixture.Post},
		{"false-owner", fixture.Post},
		{"used-body", fixture.Comment},
		{"unused-body", fixture.Comment},
		{"unused-reference-chain", fixture.Comment},
		{"dead-body-branch", fixture.Comment},
		{"empty-quantifier-body", fixture.Comment},
		{"negative-body", fixture.Comment},
		{"intermediate-body", fixture.User},
		{"deep-body", fixture.Comment},
		{"related-scope", fixture.User},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := runtimeFactRegistryWithoutSubscriptions(t, fixture, test.disabled)
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimePost](fixture.Post, func(runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimePost]()
				scope := golem.All[runtimePost]()
				body := comments.Some(bodyField.Eq("ready"))
				switch test.name {
				case "constant-owner":
					body = golem.All[runtimePost]()
				case "false-owner":
					body = golem.None[runtimePost]()
				case "dead-body-branch":
					body = golem.All[runtimePost]().Or(body)
				case "empty-quantifier-body":
					body = comments.Some(golem.All[runtimeFactComment]())
				case "negative-body":
					body = comments.None(bodyField.Eq("ready"))
				case "intermediate-body", "deep-body":
					body = author.Is(posts.Some(body))
				case "related-scope":
					scope, body = author.Is(golem.All[runtimeUser]()), golem.All[runtimePost]()
				}
				rules.FactScope(scope)
				local := rules.Fact("local", golem.All[runtimePost]())
				hidden := rules.Fact("hidden", body)
				if test.name == "unused-reference-chain" {
					rules.Fact("unused-wrapper", hidden.Not())
				}
				if test.name == "used-body" || test.name == "negative-body" {
					rules.CanRead(hidden)
				} else {
					rules.CanRead(local)
				}
				return rules.Freeze(fixture.Post)
			})
			set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, registry.GenerationDigest(), binding), Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtimePolicyError(t, set, policyir.ModelID(fixture.Post)); err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_FACT_OBSERVABILITY") {
				t.Fatalf("unobservable fact participant activated, or failed outside observation admission: %v", err)
			}
			if _, present := set.Policy(policyir.ModelID(fixture.Post)); present {
				t.Fatal("unobservable fact policy remained available after admission failure")
			}
		})
	}
}

func TestFactActivationAcceptsSubscribedParticipantsWithoutTargetReadPolicies(t *testing.T) {
	fixture := runtimePostFactCaptureFixture(t)
	comments := golem.GeneratedToMany[runtimePost, runtimeFactComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
	body := golem.GeneratedModeTextField[runtimeFactComment, string](fixture.CommentBody)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimePost](fixture.Post, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimePost]()
		rules.FactScope(golem.All[runtimePost]())
		hidden := rules.Fact("hidden", comments.None(body.Eq("ready")))
		rules.Fact("unused", hidden.Not())
		rules.CanRead(hidden)
		return rules.Freeze(fixture.Post)
	})
	set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.Registry.GenerationDigest(), binding), Registry: fixture.Registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, fixture.Registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := set.Policy(policyir.ModelID(fixture.Post)); !present {
		t.Fatal(runtimePolicyError(t, set, policyir.ModelID(fixture.Post)))
	}
}

func TestBoundFactActivationRetainsAuthoredDeadParticipantsUntilAdmission(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	registry := runtimeFactRegistryWithoutSubscriptions(t, fixture, fixture.Comment)
	owner, present := registry.Model(fixture.User)
	if !present || len(owner.ScopeTemplates()) != 1 || len(owner.FactTemplates()) != 1 {
		t.Fatal("fixture lost its registered named/ready capture contract")
	}
	scopeTemplate, factTemplate := owner.ScopeTemplates()[0], owner.FactTemplates()[0]
	name := golem.GeneratedModeTextField[runtimeUser, string](fixture.UserName)
	title := golem.GeneratedModeTextField[runtimePost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	comments := golem.GeneratedToMany[runtimePost, runtimeFactComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
	// Comment is intentionally outside the static named/ready templates. The
	// supplied body remains Boolean-equivalent, but authored observation
	// participants must survive binding until registry admission checks them.
	dead := golem.All[runtimeUser]().Or(posts.Some(comments.Some(golem.All[runtimeFactComment]())))
	for _, declaration := range []string{"scope", "fact"} {
		t.Run(declaration, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.User, func(runtimeActor) (golem.FrozenPolicy, error) {
				scopeBody, factBody := name.Eq("source"), posts.Some(title.Eq("ready"))
				if declaration == "scope" {
					scopeBody = scopeBody.And(dead)
				} else {
					factBody = factBody.And(dead)
				}
				rules := golem.NewRules[runtimeUser]()
				rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.User, string(scopeTemplate.ID), runtimeDigest(t, string(scopeTemplate.Fingerprint)), scopeBody, golem.GeneratedTemplateArgument("Name", "source")))
				rules.BindFact(golem.GeneratedBoundFact(fixture.User, string(factTemplate.ID), runtimeDigest(t, string(factTemplate.Fingerprint)), factTemplate.Name, factBody, golem.GeneratedTemplateArgument("State", "ready")))
				rules.CanRead(golem.All[runtimeUser]())
				return rules.Freeze(fixture.User)
			})
			set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, registry.GenerationDigest(), binding), Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtimePolicyError(t, set, policyir.ModelID(fixture.User)); err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_FACT_OBSERVABILITY") {
				t.Fatalf("bound %s erased its unsubscribed authored participant before admission: %v", declaration, err)
			}
		})
	}
}

func TestOrdinaryPoliciesDoNotRequireSubscriptions(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		return rules.Freeze(fixture.user)
	})
	model, _ := registry.Model(fixture.user)
	if model.SubscriptionsEnabled() {
		t.Fatal("ordinary control fixture unexpectedly has subscriptions")
	}
	set, err := Build(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.generation, binding), Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := set.Policy(policyir.ModelID(fixture.user)); !present {
		t.Fatal(runtimePolicyError(t, set, policyir.ModelID(fixture.user)))
	}
}

// Inline facts now require configured observation at policy activation. Keep
// the existing actor/capability tests equivalent using real subscribed models;
// the shared ordinary-policy fixture remains deliberately unsubscribed.
func runtimeSubscribedFactFixture(t *testing.T) (*schema.Registry, runtimeFixture) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	return fixture.Registry, runtimeFixture{generation: fixture.Registry.GenerationDigest(), user: fixture.User, post: fixture.Post, userHandle: fixture.UserName}
}

// This Post-owned data fact has a real static owner contract. Its physical
// Comment participants still need no caller-readable policy of their own.
func runtimePostFactCaptureFixture(t *testing.T) schematest.GraphFixture {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	user, _ := fixture.Registry.Model(fixture.User)
	scopes := []scopecontract.Declaration{{ModelID: model(fixture.User), Template: user.ScopeTemplates()[0]}, {
		ModelID: model(fixture.Post), Template: compilerir.ScopeTemplateIR{Name: "open", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "OpenArgs"}, Expression: compilerir.ScopeExpressionIR{ModelID: model(fixture.Post), Operator: "true"}},
	}}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	body, _ := fixture.Registry.Field(fixture.Comment, fixture.CommentBody)
	facts := []scopecontract.FactDeclaration{{ModelID: model(fixture.User), ScopeName: "named", Template: user.FactTemplates()[0]}, {
		ModelID: model(fixture.Post), ScopeName: "open", Template: compilerir.FactTemplateIR{
			Name: "comments", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "CommentArgs"}, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: body.LogicalType()}},
			Expression: compilerir.ScopeExpressionIR{ModelID: model(fixture.Post), Operator: "some", FieldID: field(fixture.PostComments), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Commenting[:])), TargetModelID: model(fixture.Comment), Children: []compilerir.ScopeExpressionIR{{ModelID: model(fixture.Comment), Operator: "eq", FieldID: field(fixture.CommentBody), Parameter: "State"}}},
		},
	}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	bundle := fixture.Bundle
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), runtimeDigest(t, string(fingerprint)), payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contract, bundle.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func runtimeFactRegistryWithoutSubscriptions(t *testing.T, fixture schematest.GraphFixture, disabled golem.ModelID) *schema.Registry {
	t.Helper()
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for index := range compilation.Contract.Models {
		entry := &compilation.Contract.Models[index]
		if runtimeModelID(t, entry.ModelID) == disabled {
			entry.Subscriptions, entry.Event = false, nil
			changed = true
		}
	}
	if !changed {
		t.Fatal("disabled model is absent from real fixture")
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), runtimeDigest(t, string(fingerprint)), payload)
	bundle := fixture.Bundle
	registry, err := schema.New(golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contract, bundle.Providers()...))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
