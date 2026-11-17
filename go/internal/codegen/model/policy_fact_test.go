package model

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestEmitFactTemplatesUsesActualDataBodyNameAndSharedTypedArguments(t *testing.T) {
	compilation, scope := scopeEmissionFixture()
	body := scope.Template.Expression
	body.Children[3].Operator = "none"
	body.Children[3].Children[0].Children[0].Operator = "notin"
	parameters := scope.Template.Parameters
	scope.Template.Expression = ir.ScopeExpressionIR{ModelID: scope.ModelID, Operator: "true"}
	scope.Template.Parameters = nil
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{scope}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	declaration := scopecontract.FactDeclaration{ModelID: scope.ModelID, ScopeName: scope.Template.Name, Template: ir.FactTemplateIR{Name: "ready", Arguments: scope.Template.Arguments, Parameters: parameters, Expression: body}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	fact := compilation.Contract.Models[0].FactTemplates[0]
	for _, fragment := range []string{
		"type golemGeneratedUserFacts struct{}", "var UserFacts = golemGeneratedUserFacts{}",
		"func (golemGeneratedUserFacts) Ready(args ScopeArgs) golem.BoundFact[User]",
		"golem.GeneratedBoundFact[User](golem.ModelID{", `"` + string(fact.ID) + `", golem.SchemaDigest{`,
		`}, "ready", golem.And[User](`, ".None(", ".NotIn(args.Titles...)", ".Eq(args.Blob)", ".In(args.Blobs...)",
		`golem.GeneratedTemplateArgument[[]byte]("Blob", args.Blob)`, `golem.GeneratedTemplateSetArgument[[]byte]("Blobs", args.Blobs)`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("fact emitter omitted %q:\n%s", fragment, source)
		}
	}
	digest, err := schemaDigestLiteral(string(fact.Fingerprint))
	if err != nil || !strings.Contains(source, digest) {
		t.Fatalf("fact template digest was not pinned: %v", err)
	}
	if strings.Contains(source, "GeneratedScopeArgument") || strings.Contains(source, "FactTemplate(") {
		t.Fatal("emitter used retired argument spelling or executed source declaration")
	}
}

func TestEmitFactTemplateScalarNullAndOrderedDataOperators(t *testing.T) {
	for _, operation := range []string{"isnull", "isnotnull", "gte"} {
		t.Run(operation, func(t *testing.T) {
			compilation, scope := scopeEmissionFixture()
			scope.Template.Parameters = nil
			scope.Template.Expression = ir.ScopeExpressionIR{ModelID: scope.ModelID, Operator: "true"}
			leaf := ir.ScopeExpressionIR{ModelID: ir.ModelID(id(2)), Operator: operation, FieldID: ir.FieldID(id(22))}
			var parameters []ir.ScopeParameterIR
			if operation == "gte" {
				compilation.Model.Models[1].Fields = append(compilation.Model.Models[1].Fields, scalarField(id(26), "Height", 5, ir.TypeInt64))
				leaf.FieldID = ir.FieldID(id(26))
				leaf.Parameter = "Maximum"
				parameters = []ir.ScopeParameterIR{{Name: "Maximum", Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}}
			}
			if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{scope}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			fact := ir.FactTemplateIR{Name: "ready", Arguments: scope.Template.Arguments, Parameters: parameters, Expression: ir.ScopeExpressionIR{ModelID: scope.ModelID, Operator: "some", FieldID: ir.FieldID(id(12)), RelationID: ir.RelationID(id(40)), TargetModelID: ir.ModelID(id(2)), Children: []ir.ScopeExpressionIR{leaf}}}
			if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: scope.ModelID, ScopeName: scope.Template.Name, Template: fact}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			result, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
			if err != nil {
				t.Fatal(err)
			}
			source := string(result.Files[0].Source)
			if operation == "gte" {
				if !strings.Contains(source, "golem.GeneratedOrderedField[Post, int64]") || !strings.Contains(source, ".GTE(args.Maximum)") {
					t.Fatal("ordered data operator did not use ordinary ordered handle")
				}
			} else {
				if !strings.Contains(source, "golem.GeneratedNullableEqualField[Post, string]") || !strings.Contains(source, map[string]string{"isnull": ".IsNull()", "isnotnull": ".IsNotNull()"}[operation]) {
					t.Fatal("scalar null predicate did not use a nullable scalar handle")
				}
			}
		})
	}
}

func TestEmitFactTemplateRejectsMethodCollision(t *testing.T) {
	compilation, scope := scopeEmissionFixture()
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{scope}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	var declarations []scopecontract.FactDeclaration
	for _, name := range []string{"ready", "Ready"} {
		declarations = append(declarations, scopecontract.FactDeclaration{ModelID: scope.ModelID, ScopeName: scope.Template.Name, Template: ir.FactTemplateIR{Name: name, Arguments: scope.Template.Arguments, Parameters: scope.Template.Parameters, Expression: scope.Template.Expression}})
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, declarations); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if _, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}}); err == nil {
		t.Fatal("colliding fact template methods were emitted")
	}
}
