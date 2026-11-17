package bind_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestBoundFactTemplateRetainsDeadBodyIdentityThroughNormalizationAndResolution(t *testing.T) {
	fixture, scopes, facts := factTemplateFixture(t)
	var previousPolicy []byte
	var previousResolved ir.Fingerprint
	for _, state := range []string{"ready", "other"} {
		frozen := freezeFactTemplatePolicy(t, fixture, scopes["account"], facts["dead"], "dead", golem.None[testPost](), golem.GeneratedTemplateArgument("State", state))
		policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		binding, present := policy.Facts()[0].Binding()
		if !present || binding.ScopeTemplateID != bindFixedID(t, string(scopes["account"].ID)) {
			t.Fatal("bound fact lost its authority association")
		}
		for pass := 0; pass < 2; pass++ {
			policy, err = normalize.Policy(policy)
			if err != nil {
				t.Fatal(err)
			}
			retained, present := policy.Facts()[0].Binding()
			if !present || retained != binding {
				t.Fatal("policy normalization discarded fact provenance")
			}
		}
		ref, _ := ir.NewFactReference(policy.ModelID(), policy.Facts()[0].ID())
		resolved, err := resolve.Condition(policy, ref)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err = normalize.Condition(resolved)
		if err != nil {
			t.Fatal(err)
		}
		retained, present := resolved.FactBinding()
		if !present || retained != binding {
			t.Fatal("resolved fact lost its template identity")
		}
		encoded, err := ir.CanonicalPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := ir.ConditionFingerprint(resolved)
		if err != nil {
			t.Fatal(err)
		}
		if previousPolicy != nil && (bytes.Equal(previousPolicy, encoded) || previousResolved == identity) {
			t.Fatal("dead fact body erased distinct frozen argument identity")
		}
		previousPolicy, previousResolved = encoded, identity
	}
}

func TestBoundFactTemplateRejectsForgedBodyArgumentsNameAndAuthority(t *testing.T) {
	fixture, scopes, templates := factTemplateFixture(t)
	for _, name := range []string{"body", "argument", "name", "identity", "digest", "missing", "type", "authority"} {
		t.Run(name, func(t *testing.T) {
			template, scope := templates["ready"], scopes["account"]
			factName := "ready"
			body := golem.GeneratedEqualField[testPost, string](fixture.PostTitle).Eq("ready")
			arguments := []golem.TemplateArgument{golem.GeneratedTemplateArgument("State", "ready")}
			switch name {
			case "body":
				body = golem.All[testPost]()
			case "argument":
				arguments[0] = golem.GeneratedTemplateArgument("State", "other")
			case "name":
				factName = "unregistered"
			case "identity":
				template.ID = compilerir.FactTemplateID(strings.Repeat("cd", 16))
			case "digest":
				template.Fingerprint = compilerir.Fingerprint(strings.Repeat("ef", 32))
			case "missing":
				arguments = nil
			case "type":
				arguments[0] = golem.GeneratedTemplateArgument("State", int64(1))
			case "authority":
				scope = scopes["dead"]
			}
			frozen := freezeFactTemplatePolicy(t, fixture, scope, template, factName, body, arguments...)
			if _, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders()); err == nil {
				t.Fatal("forged fact template crossed runtime binding")
			}
		})
	}
}

func factTemplateFixture(t *testing.T) (schematest.GraphFixture, map[string]compilerir.ScopeTemplateIR, map[string]compilerir.FactTemplateIR) {
	t.Helper()
	fixture, scopes := scopeBindTestFixture(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := compilerir.ModelID(hex.EncodeToString(fixture.Post[:]))
	field := compilerir.FieldID(hex.EncodeToString(fixture.PostTitle[:]))
	title, _ := fixture.Registry.Field(fixture.Post, fixture.PostTitle)
	expression := compilerir.ScopeExpressionIR{ModelID: model, Operator: "eq", FieldID: field, Parameter: "State"}
	declarations := []scopecontract.FactDeclaration{}
	for _, name := range []string{"ready", "dead"} {
		body := expression
		if name == "dead" {
			body = compilerir.ScopeExpressionIR{ModelID: model, Operator: "and", Children: []compilerir.ScopeExpressionIR{expression, {ModelID: model, Operator: "false"}}}
		}
		declarations = append(declarations, scopecontract.FactDeclaration{ModelID: model, ScopeName: "account", Template: compilerir.FactTemplateIR{Name: name, Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "FactArgs"}, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: title.LogicalType()}}, Expression: body}})
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, declarations); len(diagnostics) != 0 {
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
	base := fixture.Bundle
	fixture.Bundle = golem.GeneratedSchemaBundle(base.GenerationDigest(), base.GeneratorVersion(), base.TemplateABIVersion(), base.Model(), golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), bindDigest(t, string(fingerprint)), payload), base.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := fixture.Registry.Model(fixture.Post)
	facts := map[string]compilerir.FactTemplateIR{}
	for _, template := range metadata.FactTemplates() {
		facts[template.Name] = template
	}
	return fixture, scopes, facts
}

func freezeFactTemplatePolicy(t *testing.T, fixture schematest.GraphFixture, scope compilerir.ScopeTemplateIR, fact compilerir.FactTemplateIR, name string, body golem.Predicate[testPost], arguments ...golem.TemplateArgument) golem.FrozenPolicy {
	t.Helper()
	rules := golem.NewRules[testPost]()
	actor, titles := golem.UUID{15: 7}, []string{"ready"}
	scopeBody := scopeBindTestBody(fixture, actor, titles)
	if scope.Name == "dead" {
		scopeBody = golem.None[testPost]()
	}
	rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.Post, string(scope.ID), bindDigest(t, string(scope.Fingerprint)), scopeBody, scopeBindTestArguments(actor, titles)...))
	reference := rules.BindFact(golem.GeneratedBoundFact(fixture.Post, string(fact.ID), bindDigest(t, string(fact.Fingerprint)), name, body, arguments...))
	rules.CanRead(reference.Not())
	frozen, err := rules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}
