package schema_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

type captureUser struct{}
type capturePost struct{}
type captureComment struct{}
type captureDenied struct{}

func TestPolicyCaptureInventoryIncludesEveryStaticOwnerAndDeadNegativeBody(t *testing.T) {
	for _, dead := range []bool{false, true} {
		fixture, compilation := captureInventoryFixture(t, dead)
		registry := fixture.Registry
		captureRoots := registry.PolicyCaptureRoots()
		captureFingerprint := registry.PolicyCaptureFingerprint()
		if captureFingerprint == (golem.SchemaDigest{}) {
			t.Fatal("template inventory has no fingerprint")
		}
		captureProjections := map[golem.ModelID][]scopecontract.CaptureProjection{}
		for _, root := range captureRoots {
			captureProjections[root] = registry.PolicyCaptureProjections(root)
		}
		want := map[golem.ModelID][]golem.FieldID{
			fixture.User:    {fixture.UserID},
			fixture.Post:    {fixture.PostID, fixture.AuthorID, fixture.PostTitle, fixture.PostUpdatedAt},
			fixture.Comment: {fixture.CommentID, fixture.CommentPostID, fixture.CommentBody},
		}
		for model, fields := range want {
			sort.Slice(fields, func(a, b int) bool { return bytes.Compare(fields[a][:], fields[b][:]) < 0 })
			actual := registry.PolicyCaptureFields(model)
			if !reflect.DeepEqual(actual, fields) {
				t.Fatalf("dead=%v model=%x capture=%x want=%x", dead, model, actual, fields)
			}
			actual[0] = golem.FieldID{0xff}
			actual = append(actual, golem.FieldID{0xfe})
			if !reflect.DeepEqual(registry.PolicyCaptureFields(model), fields) {
				t.Fatal("capture inventory accessor exposed private registry storage")
			}
		}
		user, _ := registry.Model(fixture.User)
		var authority compilerir.ScopeTemplateIR
		for _, candidate := range user.ScopeTemplates() {
			if candidate.Name == "owner" {
				authority = candidate
			}
		}
		if authority.Expression.Operator != "true" {
			t.Fatal("fixture supplied hidden data paths through an authority approximation")
		}
		fact := user.FactTemplates()[0]
		title := golem.GeneratedEqualField[capturePost, string](fixture.PostTitle)
		posts := golem.GeneratedToMany[captureUser, capturePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
		comments := golem.GeneratedToMany[capturePost, captureComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
		body := golem.GeneratedEqualField[captureComment, string](fixture.CommentBody)
		for _, titles := range [][]string{nil, {"ready"}, {"different", "extra"}} {
			rules := golem.NewRules[captureUser]()
			rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.User, string(authority.ID), captureInventoryDigest(t, string(authority.Fingerprint)), golem.All[captureUser]()))
			predicate := posts.None(title.In(titles...).And(comments.Some(body.Eq("ready"))))
			if dead {
				predicate = golem.None[captureUser]()
			}
			// The fact is intentionally unused, and the actual caller policy
			// denies reading the source. Neither affects producer inventory.
			rules.BindFact(golem.GeneratedBoundFact(fixture.User, string(fact.ID), captureInventoryDigest(t, string(fact.Fingerprint)), fact.Name, predicate, golem.GeneratedTemplateSetArgument("Titles", titles)))
			rules.CanRead(golem.None[captureUser]())
			frozen, err := rules.Freeze(fixture.User)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := policybind.Policy(frozen, registry, ir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			row, err := resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(fixture.User))
			if err != nil {
				t.Fatal(err)
			}
			if truth, constant := row.Constant(); !constant || truth {
				t.Fatal("fixture source read policy was not deny-all")
			}
			for model, fields := range want {
				if !reflect.DeepEqual(registry.PolicyCaptureFields(model), fields) {
					t.Fatal("actor arguments or unused policy truth changed producer capture")
				}
			}
			if registry.PolicyCaptureFingerprint() != captureFingerprint {
				t.Fatal("actor binding changed capture fingerprint")
			}
			if !reflect.DeepEqual(captureRoots, registry.PolicyCaptureRoots()) {
				t.Fatal("actor arguments or unused policy truth changed capture roots")
			}
			for root, projections := range captureProjections {
				if !reflect.DeepEqual(projections, registry.PolicyCaptureProjections(root)) {
					t.Fatal("actor arguments or unused policy truth changed capture projections")
				}
			}
		}
		for _, model := range []golem.ModelID{fixture.Post, fixture.Comment} {
			rules := golem.NewRules[captureDenied]()
			rules.CanRead(golem.None[captureDenied]())
			frozen, err := rules.Freeze(model)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := policybind.Policy(frozen, registry, ir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			row, err := resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(model))
			if err != nil {
				t.Fatal(err)
			}
			if truth, constant := row.Constant(); !constant || truth || len(registry.PolicyCaptureFields(model)) == 0 {
				t.Fatal("target read denial removed static physical capture")
			}
		}
		// Re-encoding unchanged static metadata yields the same inventory; no
		// subscriber or actor state is part of either bootstrap call.
		again, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
		if err != nil {
			t.Fatal(err)
		}
		for model, fields := range want {
			if !reflect.DeepEqual(again.PolicyCaptureFields(model), fields) {
				t.Fatal("capture derivation is not deterministic")
			}
		}
	}
}

func TestPolicyCaptureInventoryHasNoTemplateOrUnknownModelFallback(t *testing.T) {
	fixture := schematest.NewSubscribedGraph(t)
	if fixture.Registry.PolicyCaptureFingerprint() != (golem.SchemaDigest{}) {
		t.Fatal("no-template registry has capture fingerprint")
	}
	if fixture.Registry.PolicyCaptureRoots() != nil {
		t.Fatal("subscribed models without templates became capture roots")
	}
	for _, model := range []golem.ModelID{fixture.User, fixture.Post, fixture.Comment, {0xff}} {
		if fields := fixture.Registry.PolicyCaptureFields(model); fields != nil {
			t.Fatalf("no-template model gained capture fields: %x", fields)
		}
		if fixture.Registry.PolicyCaptureProjections(model) != nil {
			t.Fatal("no-template model gained capture projections")
		}
	}
	var absent *schema.Registry
	if absent.PolicyCaptureFingerprint() != (golem.SchemaDigest{}) {
		t.Fatal("nil registry has capture fingerprint")
	}
	if absent.PolicyCaptureFields(golem.ModelID{}) != nil || absent.PolicyCaptureRoots() != nil || absent.PolicyCaptureProjections(golem.ModelID{}) != nil {
		t.Fatal("nil registry exposed capture fields")
	}
	templated, _ := captureInventoryFixture(t, false)
	if templated.Registry.PolicyCaptureFields(golem.ModelID{0xff}) != nil || templated.Registry.PolicyCaptureProjections(golem.ModelID{0xff}) != nil {
		t.Fatal("unknown model inherited another owner's capture fields")
	}
}

func TestPolicyCaptureBootstrapRejectsForgedFactMetadataBeforePublishingInventory(t *testing.T) {
	fixture, compilation := captureInventoryFixture(t, false)
	for index := range compilation.Contract.Models {
		if len(compilation.Contract.Models[index].FactTemplates) != 0 {
			compilation.Contract.Models[index].FactTemplates[0].Fingerprint = "forged"
		}
	}
	_, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
	var failure *schema.Error
	if !errors.As(err, &failure) || failure.Code != schema.CodeContract || failure.Path != "contract.scopeTemplates" {
		t.Fatalf("forged static metadata crossed capture bootstrap: %v", err)
	}
}

func captureInventoryFixture(t *testing.T, dead bool) (schematest.GraphFixture, compilerir.CompilationIR) {
	t.Helper()
	fixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	relationID := func(id golem.RelationID) compilerir.RelationID {
		return compilerir.RelationID(hex.EncodeToString(id[:]))
	}
	user, post, comment := modelID(fixture.User), modelID(fixture.Post), modelID(fixture.Comment)
	updatedAt, _ := fixture.Registry.Field(fixture.Post, fixture.PostUpdatedAt)
	title, _ := fixture.Registry.Field(fixture.Post, fixture.PostTitle)
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "CaptureArgs"}
	scopes := []scopecontract.Declaration{
		{ModelID: user, Template: compilerir.ScopeTemplateIR{Name: "owner", Arguments: args, Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "true"}}},
		// This separate owner's scope is also unused by every policy below.
		{ModelID: post, Template: compilerir.ScopeTemplateIR{Name: "timestamp", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "When", Type: updatedAt.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{ModelID: post, Operator: "eq", FieldID: fieldID(fixture.PostUpdatedAt), Parameter: "When"}}},
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	expression := compilerir.ScopeExpressionIR{ModelID: user, Operator: "none", FieldID: fieldID(fixture.UserPosts), RelationID: relationID(fixture.Authorship), TargetModelID: post, Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "and", Children: []compilerir.ScopeExpressionIR{
		{ModelID: post, Operator: "in", FieldID: fieldID(fixture.PostTitle), Parameter: "Titles"},
		{ModelID: post, Operator: "some", FieldID: fieldID(fixture.PostComments), RelationID: relationID(fixture.Commenting), TargetModelID: comment, Children: []compilerir.ScopeExpressionIR{{ModelID: comment, Operator: "eq", FieldID: fieldID(fixture.CommentBody), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "ready"}}}}},
	}}}}
	if dead {
		expression = compilerir.ScopeExpressionIR{ModelID: user, Operator: "and", Children: []compilerir.ScopeExpressionIR{{ModelID: user, Operator: "false"}, expression}}
	}
	fact := scopecontract.FactDeclaration{ModelID: user, ScopeName: "owner", Template: compilerir.FactTemplateIR{Name: "negative", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Titles", Type: title.LogicalType(), Many: true}}, Expression: expression}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{fact}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	fixture.Bundle = captureInventoryBundle(t, fixture.Bundle, compilation.Contract)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, compilation
}

func captureInventoryBundle(t *testing.T, original golem.SchemaBundle, contract compilerir.ContractIR) golem.SchemaBundle {
	t.Helper()
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), captureInventoryDigest(t, string(fingerprint)), payload)
	return golem.GeneratedSchemaBundle(original.GenerationDigest(), original.GeneratorVersion(), original.TemplateABIVersion(), original.Model(), document, original.Providers()...)
}

func captureInventoryDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("invalid fixture digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}
