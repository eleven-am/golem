package schema_test

import (
	"encoding/hex"
	"reflect"
	"slices"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestPolicyCaptureFingerprintCanonicalBootstrapAndBodyOnlyPathChange(t *testing.T) {
	fixture, compilation := captureInventoryFixture(t, false)
	original := fixture.Registry.PolicyCaptureFingerprint()
	// Bundle encoding owns canonical model/template ordering; caller ordering
	// must not become part of the derived projection fingerprint.
	slices.Reverse(compilation.Contract.Models)
	for index := range compilation.Contract.Models {
		slices.Reverse(compilation.Contract.Models[index].ScopeTemplates)
		slices.Reverse(compilation.Contract.Models[index].FactTemplates)
	}
	reordered, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
	if err != nil {
		t.Fatal(err)
	}
	if reordered.PolicyCaptureFingerprint() != original || !reflect.DeepEqual(reordered.PolicyCaptureRoots(), fixture.Registry.PolicyCaptureRoots()) {
		t.Fatal("canonical bootstrap changed fingerprint or sorted roots")
	}
	root := compilerir.ModelID(hex.EncodeToString(fixture.User[:]))
	var fact compilerir.FactTemplateIR
	for _, owner := range compilation.Contract.Models {
		if owner.ModelID == root {
			fact = owner.FactTemplates[0]
		}
	}
	// Remove just the fact body's Comment descendant. Its authority scope is
	// still true and owns no relation paths, so scope-only hashing would miss it.
	expression := fact.Expression
	expression.Children = nil
	for _, child := range fact.Expression.Children[0].Children {
		if child.Operator == "in" {
			expression.Children = []compilerir.ScopeExpressionIR{child}
		}
	}
	if len(expression.Children) != 1 {
		t.Fatal("fixture has no post title predicate")
	}
	declaration := scopecontract.FactDeclaration{ModelID: root, ScopeName: "owner", Template: compilerir.FactTemplateIR{
		Name: fact.Name, Arguments: fact.Arguments, Parameters: fact.Parameters, Expression: expression,
	}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	changed, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
	if err != nil {
		t.Fatal(err)
	}
	if changed.ModelFingerprint() != fixture.Registry.ModelFingerprint() || changed.PolicyCaptureFingerprint() == original {
		t.Fatal("body-only path change was not independently bound")
	}
	if len(changed.PolicyCaptureFields(fixture.Comment)) != 0 {
		t.Fatal("fixture did not remove its body-only descendant")
	}
}
