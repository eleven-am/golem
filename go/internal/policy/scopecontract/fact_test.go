package scopecontract

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestFactTemplateCapturesBodyIndependentlyOfUnrestrictedAuthorityAndDeadTruth(t *testing.T) {
	for _, dead := range []bool{false, true} {
		compilation, declaration := factContractFixture(t)
		if dead {
			declaration.Template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "and", Children: []ir.ScopeExpressionIR{{ModelID: "media", Operator: "false"}, declaration.Template.Expression}}
		}
		if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		if err := Validate(compilation.Model, compilation.Contract); err != nil {
			t.Fatal(err)
		}
		owner := compilation.Contract.Models[0]
		fact := owner.FactTemplates[0]
		if fact.ScopeTemplateID != owner.ScopeTemplates[0].ID || fact.ID == "" || fact.Fingerprint == "" {
			t.Fatal("fact lost exact static authority ownership")
		}
		scopeProjection, err := Projection(compilation.Model, owner.ScopeTemplates[0])
		if err != nil {
			t.Fatal(err)
		}
		bodyProjection, err := FactProjection(compilation.Model, fact)
		if err != nil {
			t.Fatal(err)
		}
		if len(scopeProjection) != 1 || len(bodyProjection) != 3 {
			t.Fatalf("All scope or dead data body changed capture paths: scope=%d body=%d", len(scopeProjection), len(bodyProjection))
		}
		for _, projection := range bodyProjection {
			if projection.ModelID == "copy" && !reflect.DeepEqual(projection.Fields, []ir.FieldID{"copy.id", "copy.state", "copy.storage_id", "copy.video_id"}) {
				t.Fatal("data capture omitted hidden witness fields")
			}
		}
		before := scopeJSON(t, compilation.Contract)
		if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 || before != scopeJSON(t, compilation.Contract) {
			t.Fatalf("fact normalization is not idempotent: %v", diagnostics)
		}
	}
}

func TestFactTemplateProjectionPreservesIndependentRelationOccurrences(t *testing.T) {
	compilation, declaration := factContractFixture(t)
	body := declaration.Template.Expression
	declaration.Template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "and", Children: []ir.ScopeExpressionIR{body, body}}
	if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	projection, err := FactProjection(compilation.Model, compilation.Contract.Models[0].FactTemplates[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(projection) != 5 {
		t.Fatalf("independent repeated quantifier paths merged: %d", len(projection))
	}
	occurrences := map[string]bool{}
	for _, path := range projection {
		if path.ModelID == "video" {
			occurrence := path.Path[0].Occurrence
			if occurrence == "" || occurrences[occurrence] {
				t.Fatal("relation occurrence identity was lost")
			}
			occurrences[occurrence] = true
		}
	}
}

func TestFactTemplateAllowsDataNegationAndRelationPresenceWithoutWideningAuthority(t *testing.T) {
	for _, operation := range []string{"none", "every", "isnot", "isnull", "isnotnull", "ne", "notin", "lt", "lte", "gt", "gte"} {
		t.Run(operation, func(t *testing.T) {
			compilation, declaration := factContractFixture(t)
			switch operation {
			case "none", "every":
				declaration.Template.Expression.Children[0].Operator = operation
			case "isnot", "isnull", "isnotnull":
				declaration.ModelID = "video"
				declaration.Template.Parameters = nil
				declaration.Template.Expression = ir.ScopeExpressionIR{ModelID: "video", Operator: operation, FieldID: "video.media", RelationID: "authorship", TargetModelID: "media"}
				if operation == "isnot" {
					declaration.Template.Expression.Children = []ir.ScopeExpressionIR{{ModelID: "media", Operator: "eq", FieldID: "media.id", Literals: []ir.TypedLiteralIR{{Kind: ir.LiteralString, Canonical: "title"}}}}
				}
				if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "video", Template: ir.ScopeTemplateIR{Name: "owner", Arguments: declaration.Template.Arguments, Expression: ir.ScopeExpressionIR{ModelID: "video", Operator: "true"}}}}); len(diagnostics) != 0 {
					t.Fatal(diagnostics)
				}
			default:
				declaration.Template.Parameters = nil
				declaration.Template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: operation, FieldID: "media.id", Literals: []ir.TypedLiteralIR{{Kind: ir.LiteralString, Canonical: "title"}}}
			}
			if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			if err := Validate(compilation.Model, compilation.Contract); err != nil {
				t.Fatal(err)
			}
			for _, owner := range compilation.Contract.Models {
				for _, fact := range owner.FactTemplates {
					if _, err := FactProjection(compilation.Model, fact); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestFactTemplateRejectsMissingAuthorityUncapturedBodyAndForgedMetadata(t *testing.T) {
	for _, failure := range []string{"scope-name", "scope-id", "subscription", "parameter", "ordered-bool", "reference"} {
		t.Run(failure, func(t *testing.T) {
			compilation, declaration := factContractFixture(t)
			switch failure {
			case "scope-name":
				declaration.ScopeName = "foreign"
			case "scope-id":
				declaration.Template.ScopeTemplateID = "foreign"
			case "subscription":
				compilation.Contract.Models[2].Subscriptions = false
			case "parameter":
				declaration.Template.Parameters[0].Many = false
			case "ordered-bool":
				compilation.Model.Models[0].Fields = append(compilation.Model.Models[0].Fields, scopeScalar("media.flag", ir.LogicalTypeIR{Kind: ir.TypeBool}))
				declaration.Template.Parameters = nil
				declaration.Template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "lt", FieldID: "media.flag", Literals: []ir.TypedLiteralIR{{Kind: ir.LiteralBool, Canonical: "true"}}}
			case "reference":
				declaration.Template.Expression.Operator = "fact"
			}
			before := scopeJSON(t, compilation.Contract)
			if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) == 0 {
				t.Fatal("unproven fact capture was accepted")
			}
			if before != scopeJSON(t, compilation.Contract) {
				t.Fatal("failed fact normalization changed contract")
			}
		})
	}
	for _, failure := range []string{"fingerprint", "scope", "expression", "inventory"} {
		t.Run("forged-"+failure, func(t *testing.T) {
			compilation, declaration := factContractFixture(t)
			if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			fact := &compilation.Contract.Models[0].FactTemplates[0]
			switch failure {
			case "fingerprint":
				fact.Fingerprint = "forged"
			case "scope":
				fact.ScopeTemplateID = "foreign"
			case "expression":
				fact.Expression.Operator = "true"
			case "inventory":
				fact.Parameters = nil
			}
			if err := Validate(compilation.Model, compilation.Contract); err == nil {
				t.Fatal("forged fact contract was accepted")
			}
		})
	}
}

func TestFactTemplateFingerprintIncludesAssociatedAuthority(t *testing.T) {
	compilation, declaration := factContractFixture(t)
	if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	before := compilation.Contract.Models[0].FactTemplates[0]
	scope := compilation.Contract.Models[0].ScopeTemplates[0]
	scope.Expression.Operator = "false"
	if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: scope}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := NormalizeFacts(&compilation, []FactDeclaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	after := compilation.Contract.Models[0].FactTemplates[0]
	if before.ID != after.ID || before.ScopeTemplateID != after.ScopeTemplateID || before.Fingerprint == after.Fingerprint {
		t.Fatal("authority change did not preserve identity and change fact certification")
	}
}

func factContractFixture(t *testing.T) (ir.CompilationIR, FactDeclaration) {
	t.Helper()
	compilation, body := scopeFixture()
	owner := ir.ScopeTemplateIR{Name: "owner", Arguments: ir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "EmptyArgs"}, Expression: ir.ScopeExpressionIR{ModelID: "media", Operator: "true"}}
	if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: owner}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	return compilation, FactDeclaration{ModelID: "media", ScopeName: "owner", Template: ir.FactTemplateIR{Name: "ready", Arguments: body.Arguments, Parameters: body.Parameters, Expression: ir.ScopeExpressionIR{ModelID: "media", Operator: "not", Children: []ir.ScopeExpressionIR{body.Expression}}}}
}
