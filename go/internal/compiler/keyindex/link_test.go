package keyindex

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestLinkCommonCompositeObjectsPreservesOrder(t *testing.T) {
	raw, base, ids := commonFixture()
	result := Link(raw, base, nil, ir.NewIDRegistry())
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	fragment := result.Fragments[0]
	if fragment.PrimaryKey == nil || !reflect.DeepEqual(fragment.PrimaryKey.Fields, []ir.FieldID{ids.tenant, ids.user}) {
		t.Fatalf("primary-key order changed: %#v", fragment.PrimaryKey)
	}
	if len(fragment.Uniques) != 1 || !reflect.DeepEqual(fragment.Uniques[0].Fields, []ir.FieldID{ids.tenant, ids.handle}) {
		t.Fatalf("unique order changed: %#v", fragment.Uniques)
	}
	if len(fragment.Selectors) != 2 || fragment.Selectors[0].Name == "" {
		t.Fatalf("unexpected selectors: %#v", fragment.Selectors)
	}
	if len(fragment.Indexes) != 1 || fragment.Indexes[0].Keys[0].Column == nil || *fragment.Indexes[0].Keys[0].Column != ids.user {
		t.Fatalf("unexpected ordinary index: %#v", fragment.Indexes)
	}
	if !fragment.EqualityIndexed(ids.tenant) || !fragment.EqualityIndexed(ids.user) || fragment.EqualityIndexed(ids.created) {
		t.Fatalf("unexpected EqualityIndexed metadata: %#v", fragment.EqualityIndexes)
	}
	if len(fragment.Selectors) != 2 {
		t.Fatalf("primary and non-null unique must create selectors: %#v", fragment.Selectors)
	}
}

func TestLinkIsDeterministicUnderShuffledInputs(t *testing.T) {
	raw, base, _ := commonFixture()
	left := Link(raw, base, nil, ir.NewIDRegistry())
	raw.Models[0].Directives[0], raw.Models[0].Directives[2] = raw.Models[0].Directives[2], raw.Models[0].Directives[0]
	base.Model.Models[0].Fields[0], base.Model.Models[0].Fields[3] = base.Model.Models[0].Fields[3], base.Model.Models[0].Fields[0]
	right := Link(raw, base, nil, ir.NewIDRegistry())
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("shuffled linking differs:\nleft=%#v\nright=%#v", left, right)
	}
}

func TestStableIDsExcludePhysicalNamesAndPreserveComponentOrder(t *testing.T) {
	raw, base, _ := commonFixture()
	left := Link(raw, base, nil, ir.NewIDRegistry())
	raw.Models[0].Directives[1].Name = "renamed_unique"
	raw.Models[0].Directives[2].Name = "renamed_index"
	right := Link(raw, base, nil, ir.NewIDRegistry())
	if left.Fragments[0].Uniques[0].ID != right.Fragments[0].Uniques[0].ID {
		t.Fatal("physical unique rename changed stable KeyID")
	}
	if !reflect.DeepEqual(left.Fragments[0].Selectors, right.Fragments[0].Selectors) {
		t.Fatal("physical unique rename changed stable selector identity or name")
	}
	if left.Fragments[0].Indexes[0].ID != right.Fragments[0].Indexes[0].ID {
		t.Fatal("physical index rename changed stable IndexID")
	}
	raw.Models[0].Directives[1].Components[0], raw.Models[0].Directives[1].Components[1] =
		raw.Models[0].Directives[1].Components[1], raw.Models[0].Directives[1].Components[0]
	ordered := Link(raw, base, nil, ir.NewIDRegistry())
	if ordered.Fragments[0].Uniques[0].ID == right.Fragments[0].Uniques[0].ID {
		t.Fatal("ordered unique component change did not change KeyID")
	}
}

func TestNullableUniqueAndUniqueIndexDoNotCreateSelectors(t *testing.T) {
	raw, base, ids := commonFixture()
	raw.Models[0].Directives = nil
	base.Model.Models[0].Fields[3].Scalar.Nullable = true
	advanced := []AdvancedModelDeclarations{{
		ModelID: base.Model.Models[0].ID,
		Keys:    []KeyDeclaration{{Kind: ir.KeyUnique, PhysicalName: "uq_created", Fields: []ir.FieldID{ids.created}}},
		Indexes: []IndexDeclaration{{
			PhysicalName: "uidx_handle", Unique: true,
			Keys: []ir.IndexKeyIR{{Column: fieldIDPointer(ids.handle)}},
		}},
	}}
	result := Link(raw, base, advanced, ir.NewIDRegistry())
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	fragment := result.Fragments[0]
	if len(fragment.Selectors) != 0 {
		t.Fatalf("nullable unique or unique index created a selector: %#v", fragment)
	}
	if !fragment.EqualityIndexed(ids.created) || !fragment.EqualityIndexed(ids.handle) {
		t.Fatalf("leading unique/index fields must still be EqualityIndexed: %#v", fragment.EqualityIndexes)
	}
}

func TestSingleFieldFlagsGenerateDeterministicPhysicalNames(t *testing.T) {
	raw, base, ids := commonFixture()
	raw.Models[0].Directives = nil
	raw.Models[0].Fields[0].GolemAttrs = []ir.RawAttribute{{Name: "pk"}}
	raw.Models[0].Fields[2].GolemAttrs = []ir.RawAttribute{{Name: "unique"}}
	result := Link(raw, base, nil, ir.NewIDRegistry())
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	fragment := result.Fragments[0]
	if got, want := fragment.PrimaryKey.PhysicalName, ir.SQLIdentifier("pk_memberships"); got != want {
		t.Fatalf("primary physical name %q, want %q", got, want)
	}
	if got, want := fragment.Uniques[0].PhysicalName, ir.SQLIdentifier("uq_memberships_handle"); got != want {
		t.Fatalf("unique physical name %q, want %q", got, want)
	}
	if !reflect.DeepEqual(fragment.PrimaryKey.Fields, []ir.FieldID{ids.tenant}) {
		t.Fatalf("unexpected primary key: %#v", fragment.PrimaryKey)
	}
}

func TestTypedAdvancedObjectsNormalizeAndApply(t *testing.T) {
	raw, base, ids := commonFixture()
	raw.Models[0].Directives = nil
	base.Model.Models[0].Fields[3].Scalar.DatabaseReadOnly = true
	advanced := []AdvancedModelDeclarations{{
		ModelID: base.Model.Models[0].ID,
		Keys:    []KeyDeclaration{{Kind: ir.KeyUnique, PhysicalName: "uq_tenant_handle", Fields: []ir.FieldID{ids.tenant, ids.handle}}},
		Indexes: []IndexDeclaration{{
			PhysicalName: "idx_handle_expr",
			Keys:         []ir.IndexKeyIR{{Expr: &ir.SchemaExprIR{Kind: "lower(handle)"}}},
			Predicate:    &ir.SchemaPredicateIR{Kind: "not_deleted"},
		}},
		Checks: []CheckDeclaration{{PhysicalName: "ck_handle", Predicate: ir.SchemaPredicateIR{Kind: "handle_nonempty"}}},
		Generated: []GeneratedDeclaration{{
			FieldID:                 ids.created,
			ExpressionProvenNonNull: true,
			Generation: ir.GeneratedColumnIR{
				Expr: ir.SchemaExprIR{Kind: "created_expression"},
			},
		}},
	}}
	result := Link(raw, base, advanced, ir.NewIDRegistry())
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	fragment := result.Fragments[0]
	if len(fragment.Checks) != 1 || fragment.Checks[0].Provider != ir.ProviderScopePortable {
		t.Fatalf("check was not normalized: %#v", fragment.Checks)
	}
	if len(fragment.Generated) != 1 || fragment.Generated[0].Generation.Storage != ir.GeneratedStored {
		t.Fatalf("generation was not normalized: %#v", fragment.Generated)
	}
	if fragment.EqualityIndexed(ids.handle) {
		t.Fatal("expression index must not satisfy EqualityIndexed")
	}
	if diagnostics := ApplyFragments(&base, result.Fragments); len(diagnostics) != 0 {
		t.Fatalf("apply failed: %#v", diagnostics)
	}
	model := base.Model.Models[0]
	if len(model.Uniques) != 1 || len(model.Indexes) != 1 || len(model.Checks) != 1 || len(model.EqualityIndexes) != 1 || model.Fields[3].Scalar.Generation == nil {
		t.Fatalf("fragment was not applied: %#v", model)
	}
	if selectors := base.Contract.Models[0].Selectors; len(selectors) != 1 || selectors[0].Name != "TenantID_Handle" {
		t.Fatalf("selector contract was not applied: %#v", selectors)
	}
}

func TestLinkReportsIndependentKeyAndIndexDiagnostics(t *testing.T) {
	raw, base, ids := commonFixture()
	base.Model.Models[0].Fields[0].Scalar.Nullable = true
	relationID := ir.FieldID("relation")
	base.Model.Models[0].Fields = append(base.Model.Models[0].Fields, ir.FieldIR{ID: relationID, GoName: "Owner", Kind: ir.FieldRelation})
	raw.Models[0].Directives = []ir.RawDirectiveDecl{
		{Kind: "primary", Name: "pk_one", Components: []string{"tenant_id"}, Span: span(2)},
		{Kind: "primary", Name: "pk_two", Components: []string{"user_id"}, Span: span(3)},
		{Kind: "unique", Name: "uq_missing", Components: []string{"absent"}, Span: span(4)},
		{Kind: "index", Name: "idx_repeat", Components: []string{"handle", "handle"}, Span: span(5)},
	}
	advanced := []AdvancedModelDeclarations{{
		ModelID: base.Model.Models[0].ID,
		Keys:    []KeyDeclaration{{Kind: ir.KeyUnique, PhysicalName: "uq_relation", Fields: []ir.FieldID{relationID}, Span: span(6)}},
		Indexes: []IndexDeclaration{{PhysicalName: "idx_bad", Keys: []ir.IndexKeyIR{{Column: fieldIDPointer(ids.user), Expr: &ir.SchemaExprIR{Kind: "both"}}}, Span: span(7)}},
	}}
	result := Link(raw, base, advanced, ir.NewIDRegistry())
	codes := diagnosticCodes(result.Diagnostics)
	for _, expected := range []string{
		"P1_PRIMARY_KEY_NULLABLE", "P1_KEY_COMPONENT_MISSING", "P1_INDEX_COMPONENT_DUPLICATE",
		"P1_KEY_COMPONENT_TYPE", "P1_INDEX_KEY_SHAPE",
	} {
		if !codes[expected] {
			t.Errorf("missing diagnostic %s in %#v", expected, result.Diagnostics)
		}
	}
}

func TestSelectorAndPhysicalNameCollisions(t *testing.T) {
	raw, base, ids := collisionFixture()
	advanced := []AdvancedModelDeclarations{{
		ModelID: base.Model.Models[0].ID,
		Keys: []KeyDeclaration{
			{Kind: ir.KeyUnique, PhysicalName: "uq_one", Fields: []ir.FieldID{ids[0]}, Span: span(2)},
			{Kind: ir.KeyUnique, PhysicalName: "uq_two", Fields: []ir.FieldID{ids[1], ids[2]}, Span: span(3)},
		},
		Indexes: []IndexDeclaration{{PhysicalName: "uq_one", Keys: []ir.IndexKeyIR{{Column: fieldIDPointer(ids[2])}}, Span: span(4)}},
	}}
	result := Link(raw, base, advanced, ir.NewIDRegistry())
	codes := diagnosticCodes(result.Diagnostics)
	if !codes["P1_SELECTOR_NAME_COLLISION"] || !codes["P1_PHYSICAL_NAME_DUPLICATE"] {
		t.Fatalf("expected selector and physical collisions: %#v", result.Diagnostics)
	}
}

func TestPhysicalNamesAreUniqueAcrossModels(t *testing.T) {
	raw, base, ids := commonFixture()
	raw.Models[0].Directives = []ir.RawDirectiveDecl{{Kind: "index", Name: "idx_shared", Components: []string{"tenant_id"}, Span: span(2)}}
	second := base.Model.Models[0]
	second.ID = "model-second"
	second.Go.Name = "Second"
	second.LogicalName = "Second"
	second.Table.PhysicalName = "seconds"
	second.Fields = []ir.FieldIR{scalarField("second-field", "TenantID", "tenant_id", false)}
	base.Model.Models = append(base.Model.Models, second)
	raw.Models = append(raw.Models, ir.RawModelDecl{
		PackagePath: "example/social", GoName: "Second",
		Directives: []ir.RawDirectiveDecl{{Kind: "index", Name: "idx_shared", Components: []string{"tenant_id"}, Span: span(3)}},
	})
	result := Link(raw, base, nil, ir.NewIDRegistry())
	if !diagnosticCodes(result.Diagnostics)["P1_PHYSICAL_NAME_DUPLICATE"] {
		t.Fatalf("cross-model duplicate physical name accepted: %#v (ids=%#v)", result.Diagnostics, ids)
	}
}

type fixtureIDs struct {
	tenant, user, handle, created ir.FieldID
}

func commonFixture() (ir.RawDeclIR, ir.CompilationIR, fixtureIDs) {
	ids := fixtureIDs{"field-tenant", "field-user", "field-handle", "field-created"}
	fields := []ir.FieldIR{
		scalarField(ids.tenant, "TenantID", "tenant_id", false),
		scalarField(ids.user, "UserID", "user_id", false),
		scalarField(ids.handle, "Handle", "handle", false),
		scalarField(ids.created, "Created", "created_at", false),
	}
	raw := ir.RawDeclIR{FormatVersion: ir.RawDeclFormatVersion, Models: []ir.RawModelDecl{{
		PackagePath: "example/social", GoName: "Membership",
		Fields: []ir.RawFieldDecl{{GoName: "TenantID"}, {GoName: "UserID"}, {GoName: "Handle"}, {GoName: "Created"}},
		Directives: []ir.RawDirectiveDecl{
			{Kind: "primary", Name: "pk_memberships", Components: []string{"tenant_id", "user_id"}, Span: span(2)},
			{Kind: "unique", Name: "uq_memberships_tenant_handle", Components: []string{"tenant_id", "handle"}, Span: span(3)},
			{Kind: "index", Name: "idx_memberships_user_created", Components: []string{"user_id", "created_at"}, Span: span(4)},
		},
	}}}
	base := ir.CompilationIR{
		Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{
			ID: "model-membership", Go: ir.GoNamedTypeIR{PackagePath: "example/social", Name: "Membership"},
			LogicalName: "Membership", Table: ir.TableBindingIR{PhysicalName: "memberships"}, Fields: fields,
		}}},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{ModelID: "model-membership"}}},
	}
	return raw, base, ids
}

func collisionFixture() (ir.RawDeclIR, ir.CompilationIR, []ir.FieldID) {
	ids := []ir.FieldID{"one", "two", "three"}
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{{PackagePath: "example", GoName: "Collision"}}}
	base := ir.CompilationIR{
		Model: ir.ModelIR{Models: []ir.ModelDeclIR{{
			ID: "collision", Go: ir.GoNamedTypeIR{PackagePath: "example", Name: "Collision"}, LogicalName: "Collision",
			Table: ir.TableBindingIR{PhysicalName: "collisions"},
			Fields: []ir.FieldIR{
				scalarFieldNamed(ids[0], "Combined", "A_B", "combined"),
				scalarFieldNamed(ids[1], "A", "A", "a"),
				scalarFieldNamed(ids[2], "B", "B", "b"),
			},
		}}},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{{ModelID: "collision"}}},
	}
	return raw, base, ids
}

func scalarField(id ir.FieldID, name string, column ir.SQLIdentifier, nullable bool) ir.FieldIR {
	return scalarFieldNamed(id, name, name, column, nullable)
}

func scalarFieldNamed(id ir.FieldID, goName, logicalName string, column ir.SQLIdentifier, nullable ...bool) ir.FieldIR {
	isNullable := false
	if len(nullable) != 0 {
		isNullable = nullable[0]
	}
	return ir.FieldIR{
		ID: id, GoName: goName, LogicalName: logicalName, Kind: ir.FieldScalar,
		Scalar: &ir.ScalarFieldIR{Column: column, Type: ir.LogicalTypeIR{Kind: ir.TypeString}, Nullable: isNullable},
	}
}

func fieldIDPointer(value ir.FieldID) *ir.FieldID { return &value }

func span(line uint32) ir.SourceSpan {
	return ir.SourceSpan{RelativeFile: "models.go", StartLine: line}
}

func diagnosticCodes(diagnostics []ir.Diagnostic) map[string]bool {
	result := make(map[string]bool, len(diagnostics))
	for _, diagnostic := range diagnostics {
		result[diagnostic.Code] = true
	}
	return result
}
