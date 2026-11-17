package runtime

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestHistoricalFactAdmissionRequiresBoundRelatedScope(t *testing.T) {
	fixture, template := runtimeHistoricalScopeFixture(t)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	title := golem.GeneratedModeTextField[runtimePost, string](fixture.PostTitle)
	for _, test := range []struct {
		name              string
		historical, bound bool
	}{
		{"legacy-bound", false, true},
		{"historical-unbound", true, false},
		{"historical-bound", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.User, func(runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				domain := posts.Some(title.Eq("ready"))
				if test.bound {
					rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.User, string(template.ID), runtimeDigest(t, string(template.Fingerprint)), domain, golem.GeneratedTemplateArgument("State", "ready")))
				} else {
					rules.FactScope(domain)
				}
				local := rules.Fact("local", golem.All[runtimeUser]())
				rules.CanRead(local)
				return rules.Freeze(fixture.User)
			})
			request := BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.Registry.GenerationDigest(), binding), Registry: fixture.Registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, fixture.Registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)}
			build := Build[runtimeActor]
			if test.historical {
				build = BuildHistorical[runtimeActor]
			}
			set, err := build(request)
			if err != nil {
				t.Fatal(err)
			}
			policy, present := set.Policy(policyir.ModelID(fixture.User))
			if test.historical && test.bound {
				if !present {
					t.Fatalf("bound related scope was refused by historical admission: %v", runtimePolicyError(t, set, policyir.ModelID(fixture.User)))
				}
				if _, bound := policy.FactScope().Binding(); !bound || !policy.FactScope().UsesRelations() {
					t.Fatal("historical admission lost the exact bound related authority")
				}
				return
			}
			var refusal *policyir.FactScopeEvidenceError
			if present || !errors.As(runtimePolicyError(t, set, policyir.ModelID(fixture.User)), &refusal) {
				t.Fatalf("unsupported related authority activated: historical=%v bound=%v", test.historical, test.bound)
			}
		})
	}
}

func runtimeHistoricalScopeFixture(t *testing.T) (schematest.GraphFixture, compilerir.ScopeTemplateIR) {
	t.Helper()
	fixture := schematest.NewSubscribedGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	title, _ := fixture.Registry.Field(fixture.Post, fixture.PostTitle)
	declarations := []scopecontract.Declaration{{ModelID: model(fixture.User), Template: compilerir.ScopeTemplateIR{
		Name: "related", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "RelatedArgs"},
		Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: title.LogicalType()}},
		Expression: compilerir.ScopeExpressionIR{ModelID: model(fixture.User), Operator: "some", FieldID: field(fixture.UserPosts), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])), TargetModelID: model(fixture.Post), Children: []compilerir.ScopeExpressionIR{{ModelID: model(fixture.Post), Operator: "eq", FieldID: field(fixture.PostTitle), Parameter: "State"}}},
	}}}
	if diagnostics := scopecontract.Normalize(&compilation, declarations); len(diagnostics) != 0 {
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
	for _, owner := range compilation.Contract.Models {
		if runtimeModelID(t, owner.ModelID) != fixture.User {
			continue
		}
		for _, template := range owner.ScopeTemplates {
			if template.Name == "related" {
				return fixture, template
			}
		}
	}
	t.Fatal("normalized related scope template is absent")
	return fixture, compilerir.ScopeTemplateIR{}
}
