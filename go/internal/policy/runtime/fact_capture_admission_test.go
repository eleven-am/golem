package runtime

import (
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// A nonzero application fingerprint cannot certify a factory's different
// authored footprint. Admission must inspect unused and Boolean-dead bodies
// before normalization hides the missing physical path.
func TestHistoricalFactAdmissionRequiresCompleteAuthoredCapture(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	for _, id := range []golem.ModelID{fixture.User, fixture.Post, fixture.Comment} {
		model, present := fixture.Registry.Model(id)
		if !present || !model.SubscriptionsEnabled() {
			t.Fatal("all authored participants must pass subscription admission")
		}
	}
	if fixture.Registry.PolicyCaptureFingerprint() == (golem.SchemaDigest{}) || len(fixture.Registry.PolicyCaptureFields(fixture.Comment)) != 0 {
		t.Fatal("fixture must capture User/Posts but not Comments")
	}
	userID := golem.GeneratedEqualField[runtimeUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedModeTextField[runtimeUser, string](fixture.UserName)
	title := golem.GeneratedModeTextField[runtimePost, string](fixture.PostTitle)
	body := golem.GeneratedModeTextField[runtimeFactComment, string](fixture.CommentBody)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	comments := golem.GeneratedToMany[runtimePost, runtimeFactComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
	owner, _ := fixture.Registry.Model(fixture.User)
	scopeTemplate, factTemplate := owner.ScopeTemplates()[0], owner.FactTemplates()[0]
	for _, declaration := range []string{"active-missing-path", "unused-missing-path", "unused-reference", "bound-dead-missing-path", "covered-inline", "covered-bound"} {
		t.Run(declaration, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.User, func(runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				missing := posts.Some(comments.Some(body.Eq("private-missing-path")))
				covered := posts.Some(title.Eq("ready"))
				var fact golem.Predicate[runtimeUser]
				if declaration == "bound-dead-missing-path" || declaration == "covered-bound" {
					rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.User, string(scopeTemplate.ID), runtimeDigest(t, string(scopeTemplate.Fingerprint)), name.Eq("source"), golem.GeneratedTemplateArgument("Name", "source")))
					actual := covered
					if declaration == "bound-dead-missing-path" {
						actual = covered.And(golem.All[runtimeUser]().Or(missing))
					}
					fact = rules.BindFact(golem.GeneratedBoundFact(fixture.User, string(factTemplate.ID), runtimeDigest(t, string(factTemplate.Fingerprint)), factTemplate.Name, actual, golem.GeneratedTemplateArgument("State", "ready")))
				} else {
					rules.FactScope(userID.Eq(golem.UUID{15: 1}))
					actual := missing
					if declaration == "covered-inline" {
						actual = covered
					}
					fact = rules.Fact("ready", actual)
					if declaration == "unused-reference" {
						rules.Fact("indirect", fact.Not())
					}
				}
				if declaration == "unused-missing-path" || declaration == "unused-reference" {
					rules.CanRead(golem.All[runtimeUser]())
				} else {
					rules.CanRead(fact)
				}
				return rules.Freeze(fixture.User)
			})
			set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.Registry.GenerationDigest(), binding), Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, fixture.Registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
			if err != nil {
				t.Fatal(err)
			}
			_, present := set.Policy(policyir.ModelID(fixture.User))
			failure := runtimePolicyError(t, set, policyir.ModelID(fixture.User))
			if declaration == "covered-inline" || declaration == "covered-bound" {
				if !present || failure != nil {
					t.Fatalf("registered capture control refused: present=%v err=%v", present, failure)
				}
				return
			}
			if present || failure == nil {
				t.Fatalf("historical policy admitted uncaptured authored evidence: present=%v err=%v", present, failure)
			}
		})
	}
}

func TestHistoricalFactAdmissionKeepsReferencePhysicalPrefix(t *testing.T) {
	fixture := runtimeFactReferenceCaptureFixture(t)
	posts := golem.GeneratedToMany[runtimeUser, runtimePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[runtimePost, runtimeUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	userID := golem.GeneratedEqualField[runtimeUser, golem.UUID](fixture.UserID)
	title := golem.GeneratedModeTextField[runtimePost, string](fixture.PostTitle)
	for _, shifted := range []bool{false, true} {
		label := "covered-reference"
		if shifted {
			label = "same-reference-at-uncaptured-suffix"
		}
		t.Run(label, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.User, func(runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				rules.FactScope(userID.Eq(golem.UUID{15: 1}))
				leaf := rules.Fact("leaf", posts.Some(title.Eq("ready")))
				body := leaf.Not()
				if shifted {
					// User->Posts->Author itself is registered. Only the referenced
					// leaf's further Posts path is missing at this occurrence.
					body = posts.Some(author.Is(leaf))
				}
				rules.CanRead(rules.Fact("parent", body))
				return rules.Freeze(fixture.User)
			})
			set, err := BuildHistorical(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.Registry.GenerationDigest(), binding), Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, fixture.Registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
			if err != nil {
				t.Fatal(err)
			}
			_, present := set.Policy(policyir.ModelID(fixture.User))
			failure := runtimePolicyError(t, set, policyir.ModelID(fixture.User))
			if !shifted {
				if !present || failure != nil {
					t.Fatalf("same-root reference refused: %v", failure)
				}
				return
			}
			if present || failure == nil {
				t.Fatalf("root coverage was reused for a different reference prefix: present=%v err=%v", present, failure)
			}
		})
	}
}

func runtimeFactReferenceCaptureFixture(t *testing.T) schematest.GraphFixture {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	relation := compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:]))
	var declarations []scopecontract.Declaration
	for _, owner := range compilation.Contract.Models {
		for _, template := range owner.ScopeTemplates {
			declarations = append(declarations, scopecontract.Declaration{ModelID: owner.ModelID, Template: template})
		}
	}
	id, _ := fixture.Registry.Field(fixture.User, fixture.UserID)
	declarations = append(declarations, scopecontract.Declaration{ModelID: model(fixture.User), Template: compilerir.ScopeTemplateIR{
		Name: "recorded_author", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "AuthorArgs"}, Parameters: []compilerir.ScopeParameterIR{{Name: "ID", Type: id.LogicalType()}},
		Expression: compilerir.ScopeExpressionIR{ModelID: model(fixture.User), Operator: "some", FieldID: field(fixture.UserPosts), RelationID: relation, TargetModelID: model(fixture.Post), Children: []compilerir.ScopeExpressionIR{
			{ModelID: model(fixture.Post), Operator: "is", FieldID: field(fixture.PostAuthor), RelationID: relation, TargetModelID: model(fixture.User), Children: []compilerir.ScopeExpressionIR{
				{ModelID: model(fixture.User), Operator: "eq", FieldID: field(fixture.UserID), Parameter: "ID"},
			}},
		}},
	}})
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
	return fixture
}
