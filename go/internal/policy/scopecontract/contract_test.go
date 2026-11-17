package scopecontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestNormalizeScopeTemplateDerivesOneCanonicalContractAndExactProjection(t *testing.T) {
	compilation, template := scopeFixture()
	declaration := Declaration{ModelID: "media", Template: template}
	if diagnostics := Normalize(&compilation, []Declaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	normalized := compilation.Contract.Models[0].ScopeTemplates[0]
	if normalized.ID == "" || normalized.Fingerprint == "" || normalized.Parameters[0].Name != "StorageIDs" {
		t.Fatal("normalization omitted template identity or typed parameter ownership")
	}
	if err := Validate(compilation.Model, compilation.Contract); err != nil {
		t.Fatal(err)
	}
	before := scopeJSON(t, compilation.Contract)
	if diagnostics := Normalize(&compilation, []Declaration{declaration}); len(diagnostics) != 0 || scopeJSON(t, compilation.Contract) != before {
		t.Fatalf("normalization is not idempotent: %v", diagnostics)
	}
	// Reordering independent OR/AND branches preserves the normalized contract.
	reordered := template
	reordered.Expression.Children = append([]ir.ScopeExpressionIR(nil), template.Expression.Children...)
	branches := append([]ir.ScopeExpressionIR(nil), reordered.Expression.Children[0].Children...)
	branches[0], branches[1] = branches[1], branches[0]
	reordered.Expression.Children[0].Children = branches
	if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: reordered}}); len(diagnostics) != 0 || scopeJSON(t, compilation.Contract) != before {
		t.Fatalf("branch order changed the canonical scope: %v", diagnostics)
	}
	projections, err := Projection(compilation.Model, normalized)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 3 {
		t.Fatalf("projection paths=%d want root, Video and StorageObject only", len(projections))
	}
	want := map[ir.ModelID][]ir.FieldID{
		"media": {"media.id", "media.tenant"},
		"video": {"video.id", "video.media_id", "video.storage_id", "video.tenant"},
		"copy":  {"copy.id", "copy.state", "copy.storage_id", "copy.video_id"},
	}
	for _, projection := range projections {
		if !reflect.DeepEqual(projection.Fields, want[projection.ModelID]) {
			t.Fatalf("%s projection=%v want=%v", projection.ModelID, projection.Fields, want[projection.ModelID])
		}
		if len(projection.Path) != 0 {
			hop := projection.Path[0]
			if !reflect.DeepEqual(hop.ParentFields, []ir.FieldID{"media.tenant", "media.id"}) || !reflect.DeepEqual(hop.ChildFields, []ir.FieldID{"video.tenant", "video.media_id"}) {
				t.Fatal("inverse composite correlation lost component order")
			}
		}
	}
	projections[0].Fields[0] = "forged"
	fresh, err := Projection(compilation.Model, normalized)
	if err != nil || fresh[0].Fields[0] == "forged" {
		t.Fatalf("derived projection aliases prior output: %v", err)
	}
}

func TestScopeTemplateValidationRejectsForgedMetadata(t *testing.T) {
	for _, name := range []string{"id", "fingerprint", "parameter", "expression", "missing-subscription", "duplicate-template", "correlation"} {
		t.Run(name, func(t *testing.T) {
			compilation, template := scopeFixture()
			if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: template}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			owner := &compilation.Contract.Models[0]
			switch name {
			case "id":
				owner.ScopeTemplates[0].ID = "forged"
			case "fingerprint":
				owner.ScopeTemplates[0].Fingerprint = "forged"
			case "parameter":
				owner.ScopeTemplates[0].Parameters[0].Many = false
			case "expression":
				owner.ScopeTemplates[0].Expression.FieldID = "video.copies"
			case "missing-subscription":
				compilation.Contract.Models[2].Subscriptions = false
			case "duplicate-template":
				owner.ScopeTemplates = append(owner.ScopeTemplates, owner.ScopeTemplates[0])
			case "correlation":
				compilation.Model.Relations[0].LocalFields[0] = "video.id"
			}
			if err := Validate(compilation.Model, compilation.Contract); err == nil {
				t.Fatal("forged normalized scope contract was accepted")
			}
		})
	}
}

func TestScopeTemplateRejectsUnprovenAuthorityAndInvalidOperandsAtomically(t *testing.T) {
	for _, name := range []string{
		"negative-relation", "nested-negative-relation", "every", "none", "is-not", "wrong-root", "wrong-field", "wrong-target", "wrong-cardinality", "wrong-correlation", "wrong-endpoint", "through", "missing-key", "missing-subscription", "missing-owner-contract", "missing-slot", "unused-slot", "duplicate-slot", "wrong-slot-type", "wrong-slot-cardinality", "mixed-slot-cardinality", "literal-kind", "literal-value", "enum-value", "mixed-operands", "bad-logical-shape", "too-deep", "too-wide", "too-many-literals", "too-many-parameters",
	} {
		t.Run(name, func(t *testing.T) {
			compilation, template := scopeFixture()
			switch name {
			case "negative-relation":
				template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "not", Children: []ir.ScopeExpressionIR{template.Expression}}
			case "nested-negative-relation":
				child := template.Expression.Children[0].Children[1]
				template.Expression.Children[0].Children[1] = ir.ScopeExpressionIR{ModelID: "video", Operator: "not", Children: []ir.ScopeExpressionIR{child}}
			case "every", "none", "is-not":
				template.Expression.Operator = name
			case "wrong-root":
				template.Expression.ModelID = "video"
			case "wrong-field":
				template.Expression.Children[0].Children[0].FieldID = "copy.storage_id"
			case "wrong-target":
				template.Expression.TargetModelID = "copy"
			case "wrong-cardinality":
				template.Expression.Operator = "is"
			case "wrong-correlation":
				compilation.Model.Relations[0].RemoteFields = []ir.FieldID{"media.id"}
			case "wrong-endpoint":
				compilation.Model.Relations[0].SourceField = "video.storage_id"
			case "through":
				compilation.Model.Relations[0].Through = &ir.ThroughRelationIR{ModelID: "copy"}
			case "missing-key":
				compilation.Model.Models[1].PrimaryKey = nil
			case "missing-subscription":
				compilation.Contract.Models[2].Subscriptions = false
			case "missing-owner-contract":
				compilation.Contract.Models = compilation.Contract.Models[1:]
			case "missing-slot":
				template.Parameters = nil
			case "unused-slot":
				template.Parameters = append(template.Parameters, ir.ScopeParameterIR{Name: "Unused", Type: ir.LogicalTypeIR{Kind: ir.TypeString}})
			case "duplicate-slot":
				template.Parameters = append(template.Parameters, template.Parameters[0])
			case "wrong-slot-type":
				template.Parameters[0].Type.Kind = ir.TypeInt64
			case "wrong-slot-cardinality":
				template.Parameters[0].Many = false
			case "mixed-slot-cardinality":
				template.Expression.Children[0].Children[0].Operator = "eq"
			case "literal-kind":
				template.Expression.Children[0].Children[1].Children[0].Children[0].Literals[0].Kind = ir.LiteralString
			case "literal-value", "enum-value":
				template.Expression.Children[0].Children[1].Children[0].Children[0].Literals[0].Canonical = "UNKNOWN"
			case "mixed-operands":
				template.Expression.Children[0].Children[0].Literals = []ir.TypedLiteralIR{{Kind: ir.LiteralString, Canonical: "extra"}}
			case "bad-logical-shape":
				template.Expression.Children[0].FieldID = "video.id"
			case "too-deep":
				for range MaxDepth + 1 {
					template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "and", Children: []ir.ScopeExpressionIR{template.Expression}}
				}
			case "too-wide":
				children := make([]ir.ScopeExpressionIR, MaxNodes+1)
				for index := range children {
					children[index] = ir.ScopeExpressionIR{ModelID: "media", Operator: "true"}
				}
				template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "and", Children: children}
			case "too-many-literals":
				template.Expression.Children[0].Children[1].Children[0].Children[0].Literals = make([]ir.TypedLiteralIR, MaxLiteralValues+1)
			case "too-many-parameters":
				template.Parameters = make([]ir.ScopeParameterIR, MaxParameters+1)
			}
			before := scopeJSON(t, compilation)
			if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: template}}); len(diagnostics) == 0 {
				t.Fatal("unproven scope template was accepted")
			}
			if scopeJSON(t, compilation) != before {
				t.Fatal("failed normalization published partial metadata")
			}
		})
	}
}

func TestScopeTemplateNoOpPreservesExistingSchemaValidationOwnership(t *testing.T) {
	compilation, _ := scopeFixture()
	// With no templates, duplicate inventory remains the ordinary compiler and
	// registry validators' responsibility, rather than changing error priority.
	compilation.Model.Models = append(compilation.Model.Models, compilation.Model.Models[0])
	if err := Validate(compilation.Model, compilation.Contract); err != nil {
		t.Fatal(err)
	}
	if diagnostics := Normalize(&compilation, nil); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	compilation, template := scopeFixture()
	if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: template}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	before := scopeJSON(t, compilation.Contract)
	if diagnostics := Normalize(&compilation, nil); len(diagnostics) != 0 || before != scopeJSON(t, compilation.Contract) {
		t.Fatalf("empty declaration pass discarded existing templates: %v", diagnostics)
	}
	compilation.Contract.Models[0].ScopeTemplates[0].Fingerprint = "forged"
	if diagnostics := Normalize(&compilation, nil); len(diagnostics) == 0 {
		t.Fatal("empty declaration pass skipped existing template validation")
	}
}

func TestScopeTemplatePositivePolarityAndEmptyLiteralSet(t *testing.T) {
	for _, name := range []string{"double-not", "negative-scalar", "unary-and", "unary-or", "empty-in", "forward-is", "enum-keyword"} {
		t.Run(name, func(t *testing.T) {
			compilation, template := scopeFixture()
			switch name {
			case "double-not":
				for range 2 {
					template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: "not", Children: []ir.ScopeExpressionIR{template.Expression}}
				}
			case "negative-scalar":
				leaf := template.Expression.Children[0].Children[0]
				template.Expression.Children[0].Children[0] = ir.ScopeExpressionIR{ModelID: "video", Operator: "not", Children: []ir.ScopeExpressionIR{leaf}}
			case "unary-and", "unary-or":
				template.Expression = ir.ScopeExpressionIR{ModelID: "media", Operator: strings.TrimPrefix(name, "unary-"), Children: []ir.ScopeExpressionIR{template.Expression}}
			case "empty-in":
				template.Expression.Children[0].Children[0].Parameter = ""
			case "forward-is":
				template.Expression = ir.ScopeExpressionIR{ModelID: "video", Operator: "is", FieldID: "video.media", RelationID: "authorship", TargetModelID: "media", Children: []ir.ScopeExpressionIR{{ModelID: "media", Operator: "eq", FieldID: "media.id", Parameter: "StorageIDs"}}}
				template.Parameters[0].Many = false
			case "enum-keyword":
				compilation.Model.Enums[0].Values = append(compilation.Model.Enums[0].Values, ir.EnumValueIR{ID: "state.uuid", WireValue: "uuid"})
				template.Expression.Children[0].Children[1].Children[0].Children[0].Literals = []ir.TypedLiteralIR{{Kind: ir.LiteralEnum, Canonical: "uuid"}}
			}
			root := template.Expression.ModelID
			if diagnostics := Normalize(&compilation, []Declaration{{ModelID: root, Template: template}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			if err := Validate(compilation.Model, compilation.Contract); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScopeTemplateIdentityIncludesCaptureCorrelationAndScalarShape(t *testing.T) {
	base, template := scopeFixture()
	if diagnostics := Normalize(&base, []Declaration{{ModelID: "media", Template: template}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	before := base.Contract.Models[0].ScopeTemplates[0]
	for _, change := range []string{"correlation", "nullable", "type"} {
		t.Run(change, func(t *testing.T) {
			compilation, template := scopeFixture()
			switch change {
			case "correlation":
				compilation.Model.Models[1].Fields = append(compilation.Model.Models[1].Fields, scopeScalar("video.tenant_alt", ir.LogicalTypeIR{Kind: ir.TypeString}))
				compilation.Model.Relations[0].LocalFields[0] = "video.tenant_alt"
			case "nullable":
				compilation.Model.Models[1].Fields[3].Scalar.Nullable = true
			case "type":
				limit := uint32(128)
				compilation.Model.Models[1].Fields[3].Scalar.Type.MaxLength = &limit
				compilation.Model.Models[2].Fields[2].Scalar.Type.MaxLength = &limit
				template.Parameters[0].Type.MaxLength = &limit
			}
			if diagnostics := Normalize(&compilation, []Declaration{{ModelID: "media", Template: template}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			after := compilation.Contract.Models[0].ScopeTemplates[0]
			if before.ID != after.ID || before.Fingerprint == after.Fingerprint {
				t.Fatal("capture schema change did not preserve name identity and change fingerprint")
			}
		})
	}
}

func scopeFixture() (ir.CompilationIR, ir.ScopeTemplateIR) {
	text := ir.LogicalTypeIR{Kind: ir.TypeString}
	stateID := ir.EnumID("state")
	state := ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &stateID}
	media := ir.ModelDeclIR{ID: "media", PrimaryKey: &ir.KeyIR{ID: "media.pk", Kind: ir.KeyPrimary, Fields: []ir.FieldID{"media.tenant", "media.id"}}, Fields: []ir.FieldIR{scopeScalar("media.id", text), scopeScalar("media.tenant", text), scopeRelation("media.videos", "authorship", ir.RelationInverse, ir.RelationHasMany)}}
	video := ir.ModelDeclIR{ID: "video", PrimaryKey: &ir.KeyIR{ID: "video.pk", Kind: ir.KeyPrimary, Fields: []ir.FieldID{"video.id"}}, Fields: []ir.FieldIR{scopeScalar("video.id", text), scopeScalar("video.tenant", text), scopeScalar("video.media_id", text), scopeScalar("video.storage_id", text), scopeRelation("video.media", "authorship", ir.RelationSource, ir.RelationBelongsTo), scopeRelation("video.copies", "copies", ir.RelationInverse, ir.RelationHasMany)}}
	copyModel := ir.ModelDeclIR{ID: "copy", PrimaryKey: &ir.KeyIR{ID: "copy.pk", Kind: ir.KeyPrimary, Fields: []ir.FieldID{"copy.id"}}, Fields: []ir.FieldIR{scopeScalar("copy.id", text), scopeScalar("copy.video_id", text), scopeScalar("copy.storage_id", text), scopeScalar("copy.state", state), scopeRelation("copy.video", "copies", ir.RelationSource, ir.RelationBelongsTo)}}
	mediaVideos, videoCopies := ir.FieldID("media.videos"), ir.FieldID("video.copies")
	compilation := ir.CompilationIR{Model: ir.ModelIR{Models: []ir.ModelDeclIR{media, video, copyModel}, Enums: []ir.EnumIR{{ID: stateID, Values: []ir.EnumValueIR{{ID: "state.present", WireValue: "PRESENT"}, {ID: "state.missing", WireValue: "MISSING"}}}}, Relations: []ir.RelationIR{
		{ID: "authorship", SourceModel: "video", TargetModel: "media", SourceField: "video.media", InverseField: &mediaVideos, Cardinality: ir.RelationMany, LocalFields: []ir.FieldID{"video.tenant", "video.media_id"}, RemoteFields: []ir.FieldID{"media.tenant", "media.id"}},
		{ID: "copies", SourceModel: "copy", TargetModel: "video", SourceField: "copy.video", InverseField: &videoCopies, Cardinality: ir.RelationMany, LocalFields: []ir.FieldID{"copy.video_id"}, RemoteFields: []ir.FieldID{"video.id"}},
	}}, Contract: ir.ContractIR{Models: []ir.ModelContractIR{{ModelID: "media", Subscriptions: true}, {ModelID: "video", Subscriptions: true}, {ModelID: "copy", Subscriptions: true}}}}
	storage := func(model ir.ModelID, field ir.FieldID) ir.ScopeExpressionIR {
		return ir.ScopeExpressionIR{ModelID: model, Operator: "in", FieldID: field, Parameter: "StorageIDs"}
	}
	template := ir.ScopeTemplateIR{Name: "libraries", Arguments: ir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "LibraryScopeArgs"}, Parameters: []ir.ScopeParameterIR{{Name: "StorageIDs", Type: text, Many: true}}, Expression: ir.ScopeExpressionIR{ModelID: "media", Operator: "some", FieldID: "media.videos", RelationID: "authorship", TargetModelID: "video", Children: []ir.ScopeExpressionIR{{ModelID: "video", Operator: "or", Children: []ir.ScopeExpressionIR{storage("video", "video.storage_id"), {ModelID: "video", Operator: "some", FieldID: "video.copies", RelationID: "copies", TargetModelID: "copy", Children: []ir.ScopeExpressionIR{{ModelID: "copy", Operator: "and", Children: []ir.ScopeExpressionIR{{ModelID: "copy", Operator: "in", FieldID: "copy.state", Literals: []ir.TypedLiteralIR{{Kind: ir.LiteralEnum, Canonical: "PRESENT"}, {Kind: ir.LiteralEnum, Canonical: "MISSING"}}}, storage("copy", "copy.storage_id")}}}}}}}}}
	return compilation, template
}

func scopeScalar(id ir.FieldID, typ ir.LogicalTypeIR) ir.FieldIR {
	kind := ir.FieldScalar
	if typ.Kind == ir.TypeEnum {
		kind = ir.FieldEnum
	}
	return ir.FieldIR{ID: id, Kind: kind, Scalar: &ir.ScalarFieldIR{Type: typ}}
}

func scopeRelation(id ir.FieldID, relation ir.RelationID, role ir.RelationEndpointRole, kind ir.RelationKind) ir.FieldIR {
	return ir.FieldIR{ID: id, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relation, Role: role, Kind: kind}}
}

func scopeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
