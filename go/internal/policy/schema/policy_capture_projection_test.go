package schema_test

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestPolicyCaptureRootsDistinguishTemplateOwnersFromPhysicalParticipants(t *testing.T) {
	for _, dead := range []bool{false, true} {
		fixture, compilation := captureInventoryFixture(t, dead)
		wantRoots := []golem.ModelID{fixture.User, fixture.Post}
		sort.Slice(wantRoots, func(i, j int) bool { return bytes.Compare(wantRoots[i][:], wantRoots[j][:]) < 0 })
		if !reflect.DeepEqual(fixture.Registry.PolicyCaptureRoots(), wantRoots) {
			t.Fatalf("capture roots are not the sorted template owners: %x", fixture.Registry.PolicyCaptureRoots())
		}
		if len(fixture.Registry.PolicyCaptureFields(fixture.Comment)) == 0 || fixture.Registry.PolicyCaptureProjections(fixture.Comment) != nil {
			t.Fatal("body-only descendant was dropped or incorrectly promoted to a capture root")
		}
		for _, root := range wantRoots {
			want := captureProjectionsFromContract(t, compilation, root)
			got := fixture.Registry.PolicyCaptureProjections(root)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("root %x lost declaration paths, keys, fields or correlations", root)
			}
		}
		// The unrestricted User scope has only its own identity projection.
		// Related evidence must come from its data fact, even in a dead branch.
		found := false
		for _, projection := range fixture.Registry.PolicyCaptureProjections(fixture.User) {
			if projection.ModelID == compilerir.ModelID(hex.EncodeToString(fixture.Comment[:])) && len(projection.Path) == 2 {
				found = true
			}
		}
		if !found {
			t.Fatal("root inventory omitted the fact-only two-hop descendant")
		}
	}
}

func TestPolicyCaptureProjectionAccessorsOwnEveryNestedSlice(t *testing.T) {
	fixture, compilation := captureInventoryFixture(t, false)
	registry := fixture.Registry
	fingerprint := registry.PolicyCaptureFingerprint()
	wantRoots := registry.PolicyCaptureRoots()
	roots := registry.PolicyCaptureRoots()
	roots[0] = golem.ModelID{255}
	roots = append(roots, golem.ModelID{254})
	if !reflect.DeepEqual(wantRoots, registry.PolicyCaptureRoots()) {
		t.Fatal("capture root accessor leaked registry storage")
	}
	for _, root := range wantRoots {
		want := captureProjectionsFromContract(t, compilation, root)
		projections := registry.PolicyCaptureProjections(root)
		for index := range projections {
			projection := &projections[index]
			projection.ModelID = "forged-model"
			projection.Fields[0] = "forged-field"
			projection.Fields = append(projection.Fields, "appended-field")
			for hop := range projection.Path {
				path := &projection.Path[hop]
				path.Occurrence, path.ModelID, path.TargetModelID = "forged", "forged-parent", "forged-target"
				path.FieldID, path.RelationID = "forged-relation-field", "forged-relation"
				path.ParentFields[0], path.ChildFields[0] = "forged-parent-key", "forged-child-key"
				path.ParentFields = append(path.ParentFields, "appended-parent-key")
				path.ChildFields = append(path.ChildFields, "appended-child-key")
			}
			projection.Path = append(projection.Path, scopecontract.ProjectionHop{Occurrence: "appended-hop"})
		}
		projections = append(projections, scopecontract.CaptureProjection{ModelID: "appended-model"})
		if !reflect.DeepEqual(want, registry.PolicyCaptureProjections(root)) {
			t.Fatal("projection accessor leaked a top-level, field, path or correlation slice")
		}
	}
	if registry.PolicyCaptureFingerprint() != fingerprint {
		t.Fatal("accessor mutations changed cached fingerprint")
	}
	// A second real bootstrap must reproduce the same static inventory without
	// retaining mutations made to returned projections or compilation metadata.
	again, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
	if err != nil {
		t.Fatal(err)
	}
	if again.PolicyCaptureFingerprint() != fingerprint {
		t.Fatal("capture fingerprint changed across bootstrap")
	}
	if !reflect.DeepEqual(wantRoots, again.PolicyCaptureRoots()) {
		t.Fatal("root inventory changed across bootstrap")
	}
	for _, root := range wantRoots {
		if !reflect.DeepEqual(registry.PolicyCaptureProjections(root), again.PolicyCaptureProjections(root)) {
			t.Fatal("projection inventory changed across bootstrap")
		}
	}
}

func TestPolicyCapturePreservesIndependentOccurrencesAndDuplicateDeclarations(t *testing.T) {
	fixture, compilation := captureInventoryFixture(t, false)
	root := compilerir.ModelID(hex.EncodeToString(fixture.User[:]))
	var original compilerir.FactTemplateIR
	for _, owner := range compilation.Contract.Models {
		if owner.ModelID == root {
			original = owner.FactTemplates[0]
		}
	}
	expression := compilerir.ScopeExpressionIR{ModelID: root, Operator: "and", Children: []compilerir.ScopeExpressionIR{original.Expression, original.Expression}}
	var declarations []scopecontract.FactDeclaration
	for _, name := range []string{"first", "second"} {
		declarations = append(declarations, scopecontract.FactDeclaration{ModelID: root, ScopeName: "owner", Template: compilerir.FactTemplateIR{Name: name, Arguments: original.Arguments, Parameters: original.Parameters, Expression: expression}})
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, declarations); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	registry, err := schema.New(captureInventoryBundle(t, fixture.Bundle, compilation.Contract))
	if err != nil {
		t.Fatal(err)
	}
	got := registry.PolicyCaptureProjections(fixture.User)
	if !reflect.DeepEqual(got, captureProjectionsFromContract(t, compilation, fixture.User)) {
		t.Fatal("root projection merged independently declared or repeated paths")
	}
	occurrences := map[string]int{}
	for _, projection := range got {
		if len(projection.Path) == 1 {
			occurrences[projection.Path[0].Occurrence]++
		}
	}
	if len(occurrences) != 2 {
		t.Fatalf("independent relation occurrences collapsed: %v", occurrences)
	}
	for occurrence, count := range occurrences {
		if occurrence == "" || count != 2 {
			t.Fatalf("duplicate declaration projection lost: %v", occurrences)
		}
	}
}

func captureProjectionsFromContract(t *testing.T, compilation compilerir.CompilationIR, root golem.ModelID) []scopecontract.CaptureProjection {
	t.Helper()
	id := compilerir.ModelID(hex.EncodeToString(root[:]))
	var result []scopecontract.CaptureProjection
	for _, owner := range compilation.Contract.Models {
		if owner.ModelID != id {
			continue
		}
		for _, template := range owner.ScopeTemplates {
			projection, err := scopecontract.Projection(compilation.Model, template)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, projection...)
		}
		for _, template := range owner.FactTemplates {
			projection, err := scopecontract.FactProjection(compilation.Model, template)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, projection...)
		}
	}
	return result
}
