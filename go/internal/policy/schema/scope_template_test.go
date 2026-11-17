package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestRegistryScopeTemplatesOwnTheirCompleteImmutableShape(t *testing.T) {
	bundle, ids := scopeTemplateTestBundle(t)
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	post, present := registry.Model(ids.post)
	if !present {
		t.Fatal("real bundle lost the Post model")
	}
	templates := post.ScopeTemplates()
	if len(templates) != 1 || templates[0].Name != "account" || len(templates[0].Parameters) != 3 || len(templates[0].Expression.Children) != 4 {
		t.Fatalf("scope template inventory is incomplete: %+v", templates)
	}
	user, _ := registry.Model(ids.user)
	if len(user.ScopeTemplates()) != 0 || user.SubscriptionsEnabled() {
		t.Fatal("root-local scope capture changed the unsubscribed User inventory")
	}
	before := scopeTemplateJSON(t, templates)
	mutatedPointers := 0
	for index := range templates[0].Parameters {
		parameter := &templates[0].Parameters[index]
		if parameter.Type.Precision != nil {
			*parameter.Type.Precision = 1
			mutatedPointers++
		}
		if parameter.Type.Scale != nil {
			*parameter.Type.Scale = 0
			mutatedPointers++
		}
		if parameter.Type.MaxLength != nil {
			*parameter.Type.MaxLength = 1
			mutatedPointers++
		}
		parameter.Name = "Forged"
		parameter.Many = !parameter.Many
	}
	if mutatedPointers != 3 {
		t.Fatal("fixture did not exercise decimal and bounded string pointer metadata")
	}
	mutatedLiteral := false
	for index := range templates[0].Expression.Children {
		child := &templates[0].Expression.Children[index]
		if len(child.Literals) != 0 {
			child.Literals[0].Canonical = "forged"
			child.Literals = append(child.Literals, compilerir.TypedLiteralIR{Kind: compilerir.LiteralString, Canonical: "appended"})
			mutatedLiteral = true
		}
		child.FieldID = "forged"
		child.Parameter = "Forged"
	}
	if !mutatedLiteral {
		t.Fatal("fixture did not exercise nested literal ownership")
	}
	templates[0].Expression.Children = append(templates[0].Expression.Children, compilerir.ScopeExpressionIR{Operator: "false"})
	templates[0].Parameters = append(templates[0].Parameters, compilerir.ScopeParameterIR{Name: "Appended"})
	templates[0].Arguments.Name = "ForgedArgs"
	templates[0].ID, templates[0].Name, templates[0].Fingerprint = "forged", "forged", "forged"
	templates = append(templates, compilerir.ScopeTemplateIR{Name: "appended"})
	if bytes.Equal(before, scopeTemplateJSON(t, templates)) {
		t.Fatal("fixture did not mutate the returned template inventory")
	}
	if !bytes.Equal(before, scopeTemplateJSON(t, post.ScopeTemplates())) {
		t.Fatal("model scope accessor leaked mutable registry truth")
	}
	fresh, _ := registry.Model(ids.post)
	if !bytes.Equal(before, scopeTemplateJSON(t, fresh.ScopeTemplates())) {
		t.Fatal("re-fetching the model retained mutations from a prior accessor")
	}
}

func TestRegistryRejectsForgedScopeTemplatesWithValidOuterFingerprint(t *testing.T) {
	for _, mutation := range []string{"expression", "fingerprint", "identity", "parameter"} {
		t.Run(mutation, func(t *testing.T) {
			bundle, _ := scopeTemplateTestBundle(t)
			compilation, err := DecodeActive(bundle)
			if err != nil {
				t.Fatal(err)
			}
			var template *compilerir.ScopeTemplateIR
			for index := range compilation.Contract.Models {
				if len(compilation.Contract.Models[index].ScopeTemplates) != 0 {
					template = &compilation.Contract.Models[index].ScopeTemplates[0]
				}
			}
			if template == nil {
				t.Fatal("fixture lost its normalized scope template")
			}
			switch mutation {
			case "expression":
				template.Expression.Children[0].Operator = "false"
			case "fingerprint":
				template.Fingerprint = "forged"
			case "identity":
				template.ID = "forged"
			case "parameter":
				template.Parameters[0].Many = !template.Parameters[0].Many
			}
			forged := scopeTemplateContractBundle(t, bundle, compilation.Contract)
			// DecodeActive checks the exact canonical document and its outer
			// fingerprint. Rejection below must come from semantic bootstrap.
			if _, err := DecodeActive(forged); err != nil {
				t.Fatalf("forged outer contract was not canonically re-fingerprinted: %v", err)
			}
			_, err = New(forged)
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodeContract || failure.Path != "contract.scopeTemplates" {
				t.Fatalf("forged template crossed the registry authority boundary: %v", err)
			}
		})
	}
}

func scopeTemplateTestBundle(t *testing.T) (golem.SchemaBundle, testIDs) {
	t.Helper()
	bundle, ids := testBundle(t)
	compilation, err := DecodeActive(bundle)
	if err != nil {
		t.Fatal(err)
	}
	postID := compilerir.ModelID(testID(2))
	postKey, author, price := compilerir.FieldID(testID(24)), compilerir.FieldID(testID(22)), compilerir.FieldID(testID(21))
	types := map[compilerir.FieldID]compilerir.LogicalTypeIR{}
	for _, model := range compilation.Model.Models {
		if model.ID == postID {
			for _, field := range model.Fields {
				if field.Scalar != nil {
					types[field.ID] = field.Scalar.Type
				}
			}
		}
	}
	template := compilerir.ScopeTemplateIR{
		Name:      "account",
		Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "PostScopeArgs"},
		Parameters: []compilerir.ScopeParameterIR{
			{Name: "AccountID", Type: types[author]},
			{Name: "PostID", Type: types[postKey]},
			{Name: "Price", Type: types[price]},
		},
		Expression: compilerir.ScopeExpressionIR{ModelID: postID, Operator: "and", Children: []compilerir.ScopeExpressionIR{
			{ModelID: postID, Operator: "eq", FieldID: author, Parameter: "AccountID"},
			{ModelID: postID, Operator: "eq", FieldID: postKey, Parameter: "PostID"},
			{ModelID: postID, Operator: "eq", FieldID: price, Parameter: "Price"},
			{ModelID: postID, Operator: "in", FieldID: postKey, Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "one"}, {Kind: compilerir.LiteralString, Canonical: "two"}}},
		}},
	}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: postID, Template: template}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	return scopeTemplateContractBundle(t, bundle, compilation.Contract), ids
}

func scopeTemplateContractBundle(t *testing.T, original golem.SchemaBundle, contract compilerir.ContractIR) golem.SchemaBundle {
	t.Helper()
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(fingerprint)), payload)
	return golem.GeneratedSchemaBundle(original.GenerationDigest(), original.GeneratorVersion(), original.TemplateABIVersion(), original.Model(), document, original.Providers()...)
}

func scopeTemplateJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
