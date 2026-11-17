package schema

import (
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func fingerprintInventory() policyCaptureInventory {
	root := golem.ModelID{1}
	return policyCaptureInventory{roots: []golem.ModelID{root}, projections: map[golem.ModelID][]scopecontract.CaptureProjection{
		root: {{ModelID: "child", Fields: []compilerir.FieldID{"id", "value"}, Path: []scopecontract.ProjectionHop{{
			Occurrence: "0.1", ModelID: "parent", FieldID: "children", RelationID: "relation", TargetModelID: "child",
			ParentFields: []compilerir.FieldID{"parent-a", "parent-b"}, ChildFields: []compilerir.FieldID{"child-a", "child-b"},
		}}}},
	}}
}

func TestPolicyCaptureFingerprintBindsEveryProjectionComponent(t *testing.T) {
	model := golem.SchemaDigest{2}
	want := fingerprintPolicyCapture(model, fingerprintInventory())
	for name, change := range map[string]func(*policyCaptureInventory){
		"root": func(inventory *policyCaptureInventory) { inventory.roots[0] = golem.ModelID{3} },
		"declaration-count": func(inventory *policyCaptureInventory) {
			root := inventory.roots[0]
			inventory.projections[root] = append(inventory.projections[root], inventory.projections[root][0])
		},
		"projection-model": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].ModelID = "other"
		},
		"projected-field": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Fields[1] = "other"
		},
		"occurrence": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Path[0].Occurrence = "0.2"
		},
		"parent-model": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Path[0].ModelID = "other"
		},
		"relation-field": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Path[0].FieldID = "other"
		},
		"relation": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Path[0].RelationID = "other"
		},
		"target": func(inventory *policyCaptureInventory) {
			inventory.projections[inventory.roots[0]][0].Path[0].TargetModelID = "other"
		},
		"ordered-parent-correlation": func(inventory *policyCaptureInventory) {
			fields := inventory.projections[inventory.roots[0]][0].Path[0].ParentFields
			fields[0], fields[1] = fields[1], fields[0]
		},
		"ordered-child-correlation": func(inventory *policyCaptureInventory) {
			fields := inventory.projections[inventory.roots[0]][0].Path[0].ChildFields
			fields[0], fields[1] = fields[1], fields[0]
		},
		"body-path": func(inventory *policyCaptureInventory) { inventory.projections[inventory.roots[0]][0].Path = nil },
	} {
		t.Run(name, func(t *testing.T) {
			inventory := fingerprintInventory()
			change(&inventory)
			if got := fingerprintPolicyCapture(model, inventory); got == want {
				t.Fatal("projection change retained fingerprint")
			}
		})
	}
	left, right := fingerprintInventory(), fingerprintInventory()
	left.projections[left.roots[0]][0].Fields = []compilerir.FieldID{"ab", "c"}
	right.projections[right.roots[0]][0].Fields = []compilerir.FieldID{"a", "bc"}
	if fingerprintPolicyCapture(model, left) == fingerprintPolicyCapture(model, right) {
		t.Fatal("field string boundaries were not framed")
	}
}

func TestPolicyCaptureFingerprintBindsCanonicalLogicalTypesAndCorrelations(t *testing.T) {
	logical := compilerir.CanonicalEmptyModel()
	logical.Models = []compilerir.ModelDeclIR{{ID: "model", Fields: []compilerir.FieldIR{{ID: "field", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}}}}}
	logical.Relations = []compilerir.RelationIR{{ID: "relation", LocalFields: []compilerir.FieldID{"a", "b"}, RemoteFields: []compilerir.FieldID{"c", "d"}}}
	digest := func() golem.SchemaDigest {
		fingerprint, err := compilerir.ModelFingerprint(logical)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := hex.DecodeString(string(fingerprint))
		if err != nil {
			t.Fatal(err)
		}
		var model golem.SchemaDigest
		copy(model[:], encoded)
		return fingerprintPolicyCapture(model, fingerprintInventory())
	}
	original := digest()
	logical.Models[0].Fields[0].Scalar.Type.Kind = compilerir.TypeInt32
	if digest() == original {
		t.Fatal("logical scalar type was not bound")
	}
	logical.Models[0].Fields[0].Scalar.Type.Kind = compilerir.TypeString
	logical.Relations[0].RemoteFields[0], logical.Relations[0].RemoteFields[1] = logical.Relations[0].RemoteFields[1], logical.Relations[0].RemoteFields[0]
	if digest() == original {
		t.Fatal("logical correlation pairing was not bound")
	}
}
