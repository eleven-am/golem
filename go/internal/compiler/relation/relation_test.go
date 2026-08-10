package relation

import (
	"encoding/json"
	"math/rand"
	"slices"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const testPackage = "example.test/models"

type column struct {
	goName   string
	name     string
	typeKind ir.LogicalTypeKind
	nullable bool
}

func TestLinkBelongsToAndHasMany(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{
		{"ID", "id", ir.TypeInt64, false},
		{"AuthorID", "author_id", ir.TypeInt64, false},
	}, []string{"id"}, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{
		rawModel("User", relationField("Posts", ir.RawGoTypeSlice, "Post", 20, "relation", "has_many", "fields", "id", "references", "author_id")),
		rawModel("Post", relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "fields", "author_id", "references", "id")),
	}}

	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{post, user}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if len(result.Relations) != 1 {
		t.Fatalf("relations = %d, want 1", len(result.Relations))
	}
	edge := result.Relations[0]
	if edge.SourceModel != "post" || edge.TargetModel != "user" || edge.Cardinality != ir.RelationMany {
		t.Fatalf("unexpected edge endpoints/cardinality: %+v", edge)
	}
	if !slices.Equal(edge.LocalFields, []ir.FieldID{"post.AuthorID"}) || !slices.Equal(edge.RemoteFields, []ir.FieldID{"user.ID"}) {
		t.Fatalf("unexpected ordered mapping: %#v -> %#v", edge.LocalFields, edge.RemoteFields)
	}
	if edge.InverseField == nil || edge.ForeignKey == nil {
		t.Fatalf("paired relation must have inverse and FK: %+v", edge)
	}
	if edge.ForeignKey.OnUpdate != ir.ActionNoAction || edge.ForeignKey.OnDelete != ir.ActionNoAction || edge.ForeignKey.Match != ir.MatchSimple || edge.ForeignKey.Deferrable != ir.NotDeferrable {
		t.Fatalf("unexpected portable FK defaults: %+v", edge.ForeignKey)
	}
	if edge.ForeignKey.PhysicalName != "fk_posts_author_id" {
		t.Fatalf("FK name = %q", edge.ForeignKey.PhysicalName)
	}
	if len(result.Fragments) != 2 || len(result.Fragments[0].Fields) != 1 || len(result.Fragments[1].Fields) != 1 {
		t.Fatalf("unexpected model fragments: %#v", result.Fragments)
	}
}

func TestLinkCanonicalCrossPackageTarget(t *testing.T) {
	user := modelIn("example.test/accounts", "user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := modelIn("example.test/content", "post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	field := relationFieldTo("Author", ir.RawGoTypePointer, "example.test/accounts", "User", 10, "relation", "belongs_to", "fields", "author_id", "references", "id")
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{{PackagePath: "example.test/content", GoName: "Post", Fields: []ir.RawFieldDecl{field}}}}

	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if got := result.Relations[0].TargetModel; got != "user" {
		t.Fatalf("target = %q, want canonical cross-package User", got)
	}
}

func TestLinkSelfRelation(t *testing.T) {
	employee := model("employee", "Employee", "employees", []column{
		{"ID", "id", ir.TypeInt64, false},
		{"ManagerID", "manager_id", ir.TypeInt64, true},
	}, []string{"id"}, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Employee",
		relationField("Manager", ir.RawGoTypePointer, "Employee", 10, "relation", "belongs_to", "name", "Hierarchy", "fields", "manager_id", "references", "id"),
		relationField("Reports", ir.RawGoTypeSlice, "Employee", 20, "relation", "has_many", "name", "Hierarchy", "fields", "id", "references", "manager_id"),
	)}}

	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{employee}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if len(result.Relations) != 1 || result.Relations[0].InverseField == nil || result.Relations[0].Name != "Hierarchy" {
		t.Fatalf("self relation did not pair: %#v", result.Relations)
	}
}

func TestLinkSelfRelationRequiresName(t *testing.T) {
	employee := model("employee", "Employee", "employees", []column{{"ID", "id", ir.TypeInt64, false}, {"ManagerID", "manager_id", ir.TypeInt64, true}}, []string{"id"}, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Employee", relationField("Manager", ir.RawGoTypePointer, "Employee", 10, "relation", "belongs_to", "fields", "manager_id", "references", "id"))}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{employee}}, ir.NewIDRegistry()), codeNameRequired)
}

func TestLinkCompositeMapping(t *testing.T) {
	account := model("account", "Account", "accounts", []column{
		{"TenantID", "tenant_id", ir.TypeInt64, false},
		{"Number", "number", ir.TypeString, false},
	}, []string{"tenant_id", "number"}, nil)
	entry := model("entry", "Entry", "entries", []column{
		{"TenantID", "tenant_id", ir.TypeInt64, true},
		{"AccountNumber", "account_number", ir.TypeString, true},
	}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Entry", relationField("Account", ir.RawGoTypePointer, "Account", 10,
		"relation", "belongs_to", "fields", "tenant_id,account_number", "references", "tenant_id,number"))}}

	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{entry, account}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if got := result.Relations[0].LocalFields; !slices.Equal(got, []ir.FieldID{"entry.TenantID", "entry.AccountNumber"}) {
		t.Fatalf("composite mapping order = %#v", got)
	}
}

func TestLinkCompositeValidation(t *testing.T) {
	tests := []struct {
		name       string
		local      []column
		mapping    []string
		wantCode   string
		remoteType ir.LogicalTypeKind
	}{
		{name: "arity", local: []column{{"A", "a", ir.TypeInt64, false}}, mapping: []string{"a", "a,b"}, wantCode: codeArity, remoteType: ir.TypeInt64},
		{name: "type", local: []column{{"A", "a", ir.TypeString, false}, {"B", "b", ir.TypeInt64, false}}, mapping: []string{"a,b", "a,b"}, wantCode: codeTypeMismatch, remoteType: ir.TypeInt64},
		{name: "mixed nullability", local: []column{{"A", "a", ir.TypeInt64, true}, {"B", "b", ir.TypeInt64, false}}, mapping: []string{"a,b", "a,b"}, wantCode: codeLocalNullability, remoteType: ir.TypeInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := model("target", "Target", "targets", []column{{"A", "a", test.remoteType, false}, {"B", "b", ir.TypeInt64, false}}, []string{"a", "b"}, nil)
			source := model("source", "Source", "sources", test.local, nil, nil)
			raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Source", relationField("Target", ir.RawGoTypePointer, "Target", 10,
				"relation", "belongs_to", "fields", test.mapping[0], "references", test.mapping[1]))}}
			assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{source, target}}, ir.NewIDRegistry()), test.wantCode)
		})
	}
}

func TestLinkRemoteMustBePrimaryOrNonNullUnique(t *testing.T) {
	target := model("target", "Target", "targets", []column{{"Code", "code", ir.TypeString, true}}, nil, [][]string{{"code"}})
	source := model("source", "Source", "sources", []column{{"Code", "code", ir.TypeString, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Source", relationField("Target", ir.RawGoTypePointer, "Target", 10,
		"relation", "belongs_to", "fields", "code", "references", "code"))}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{source, target}}, ir.NewIDRegistry()), codeRemoteKey)
}

func TestLinkHasOneRequiresUniqueLocalForeignKey(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	profile := model("profile", "Profile", "profiles", []column{{"UserID", "user_id", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{
		rawModel("Profile", relationField("User", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "fields", "user_id", "references", "id")),
		rawModel("User", relationField("Profile", ir.RawGoTypePointer, "Profile", 20, "relation", "has_one", "fields", "id", "references", "user_id")),
	}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, profile}}, ir.NewIDRegistry()), codeOneToOneUnique)

	profile.Uniques = []ir.KeyIR{{ID: "profile.user", Kind: ir.KeyUnique, Fields: []ir.FieldID{"profile.UserID"}}}
	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, profile}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if result.Relations[0].Cardinality != ir.RelationOne {
		t.Fatalf("cardinality = %q", result.Relations[0].Cardinality)
	}
}

func TestLinkTwoNamedRelationsBetweenSameModels(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{
		{"AuthorID", "author_id", ir.TypeInt64, false},
		{"EditorID", "editor_id", ir.TypeInt64, false},
	}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{
		rawModel("Post",
			relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "name", "Authorship", "fields", "author_id", "references", "id"),
			relationField("Editor", ir.RawGoTypePointer, "User", 20, "relation", "belongs_to", "name", "Editing", "fields", "editor_id", "references", "id"),
		),
		rawModel("User",
			relationField("Authored", ir.RawGoTypeSlice, "Post", 30, "relation", "has_many", "name", "Authorship", "fields", "id", "references", "author_id"),
			relationField("Edited", ir.RawGoTypeSlice, "Post", 40, "relation", "has_many", "name", "Editing", "fields", "id", "references", "editor_id"),
		),
	}}
	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if len(result.Relations) != 2 {
		t.Fatalf("relations = %d", len(result.Relations))
	}
}

func TestLinkMissingTarget(t *testing.T) {
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Post", relationField("Author", ir.RawGoTypePointer, "Missing", 10,
		"relation", "belongs_to", "fields", "author_id", "references", "id"))}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{post}}, ir.NewIDRegistry()), codeTargetMissing)
}

func TestLinkAmbiguousInverse(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{
		rawModel("Post", relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "fields", "author_id", "references", "id")),
		rawModel("User",
			relationField("Posts", ir.RawGoTypeSlice, "Post", 20, "relation", "has_many", "fields", "id", "references", "author_id"),
			relationField("OtherPosts", ir.RawGoTypeSlice, "Post", 30, "relation", "has_many", "fields", "id", "references", "author_id"),
		),
	}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}, ir.NewIDRegistry()), codeInverseAmbiguous)
}

func TestLinkDuplicateMapping(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Post",
		relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "name", "A", "fields", "author_id", "references", "id"),
		relationField("Creator", ir.RawGoTypePointer, "User", 20, "relation", "belongs_to", "name", "B", "fields", "author_id", "references", "id"),
	)}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}, ir.NewIDRegistry()), codeDuplicateMapping)
}

func TestLinkRejectsRepeatedMappingComponent(t *testing.T) {
	target := model("target", "Target", "targets", []column{{"A", "a", ir.TypeInt64, false}, {"B", "b", ir.TypeInt64, false}}, []string{"a", "b"}, nil)
	source := model("source", "Source", "sources", []column{{"A", "a", ir.TypeInt64, false}, {"B", "b", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Source", relationField("Target", ir.RawGoTypePointer, "Target", 10,
		"relation", "belongs_to", "fields", "a,a", "references", "a,b"))}}
	assertCode(t, Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{source, target}}, ir.NewIDRegistry()), codeDuplicateMapping)
}

func TestLinkExplicitJoinModel(t *testing.T) {
	post := model("post", "Post", "posts", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	tag := model("tag", "Tag", "tags", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	postTag := model("post-tag", "PostTag", "post_tags", []column{
		{"PostID", "post_id", ir.TypeInt64, false},
		{"TagID", "tag_id", ir.TypeInt64, false},
	}, []string{"post_id", "tag_id"}, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("PostTag",
		relationField("Post", ir.RawGoTypePointer, "Post", 10, "relation", "belongs_to", "fields", "post_id", "references", "id"),
		relationField("Tag", ir.RawGoTypePointer, "Tag", 20, "relation", "belongs_to", "fields", "tag_id", "references", "id"),
	)}}
	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{post, tag, postTag}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, result)
	if len(result.Relations) != 2 || result.Relations[0].Through != nil || result.Relations[1].Through != nil {
		t.Fatalf("explicit join model must produce two ordinary FK edges: %#v", result.Relations)
	}
}

func TestLinkRefusesImplicitManyToMany(t *testing.T) {
	post := model("post", "Post", "posts", nil, nil, nil)
	tag := model("tag", "Tag", "tags", nil, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Post", relationField("Tags", ir.RawGoTypeSlice, "Tag", 10,
		"relation", "many_to_many", "through", "post_tags", "source", "post_id", "target", "tag_id"))}}
	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{post, tag}}, ir.NewIDRegistry())
	assertCode(t, result, codeImplicitM2M)
	if len(result.Relations) != 0 {
		t.Fatalf("implicit many-to-many partially emitted IR: %#v", result.Relations)
	}
}

func TestLinkRejectsBadRelationEvidence(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	field := relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "fields", "author_id", "references", "id", "writeonly", "")
	wrongDB := "author"
	field.DBTag = &wrongDB
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Post", field)}}
	result := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{post, user}}, ir.NewIDRegistry())
	assertCode(t, result, codeDBTag)
	assertCode(t, result, codeWriteOnly)
}

func TestLinkDeterministicUnderShuffledModelInput(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	rawModels := []ir.RawModelDecl{
		rawModel("Post", relationField("Author", ir.RawGoTypePointer, "User", 10, "relation", "belongs_to", "fields", "author_id", "references", "id")),
		rawModel("User", relationField("Posts", ir.RawGoTypeSlice, "Post", 20, "relation", "has_many", "fields", "id", "references", "author_id")),
	}
	baseModels := []ir.ModelDeclIR{user, post}
	var baseline []byte
	for seed := int64(0); seed < 50; seed++ {
		random := rand.New(rand.NewSource(seed))
		rawCopy := append([]ir.RawModelDecl(nil), rawModels...)
		baseCopy := append([]ir.ModelDeclIR(nil), baseModels...)
		random.Shuffle(len(rawCopy), func(i, j int) { rawCopy[i], rawCopy[j] = rawCopy[j], rawCopy[i] })
		random.Shuffle(len(baseCopy), func(i, j int) { baseCopy[i], baseCopy[j] = baseCopy[j], baseCopy[i] })
		encoded, err := json.Marshal(Link(ir.RawDeclIR{Models: rawCopy}, ir.ModelIR{Models: baseCopy}, ir.NewIDRegistry()))
		if err != nil {
			t.Fatal(err)
		}
		if seed == 0 {
			baseline = encoded
		} else if string(encoded) != string(baseline) {
			t.Fatalf("seed %d changed canonical output\nbase: %s\n got: %s", seed, baseline, encoded)
		}
	}
}

func TestApplyFragmentsIsAtomic(t *testing.T) {
	user := model("user", "User", "users", []column{{"ID", "id", ir.TypeInt64, false}}, []string{"id"}, nil)
	post := model("post", "Post", "posts", []column{{"AuthorID", "author_id", ir.TypeInt64, false}}, nil, nil)
	raw := ir.RawDeclIR{Models: []ir.RawModelDecl{rawModel("Post", relationField("Author", ir.RawGoTypePointer, "User", 10,
		"relation", "belongs_to", "fields", "author_id", "references", "id"))}}
	linked := Link(raw, ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}, ir.NewIDRegistry())
	assertNoDiagnostics(t, linked)
	base := ir.ModelIR{Models: []ir.ModelDeclIR{user, post}}
	if diagnostics := ApplyFragments(&base, linked.Relations, linked.Fragments); len(diagnostics) != 0 {
		t.Fatalf("apply diagnostics: %#v", diagnostics)
	}
	if len(base.Relations) != 1 || len(base.Models[1].Fields) != 2 {
		t.Fatalf("fragments not applied: %#v", base)
	}

	before, _ := json.Marshal(base)
	bad := ModelFragment{ModelID: "missing", Fields: linked.Fragments[0].Fields}
	if diagnostics := ApplyFragments(&base, nil, []ModelFragment{bad}); len(diagnostics) == 0 {
		t.Fatal("invalid fragment applied without diagnostics")
	}
	after, _ := json.Marshal(base)
	if string(before) != string(after) {
		t.Fatal("failed fragment batch mutated base ModelIR")
	}
}

func model(id ir.ModelID, goName string, table string, columns []column, primary []string, uniques [][]string) ir.ModelDeclIR {
	return modelIn(testPackage, id, goName, table, columns, primary, uniques)
}

func modelIn(pkg string, id ir.ModelID, goName string, table string, columns []column, primary []string, uniques [][]string) ir.ModelDeclIR {
	result := ir.ModelDeclIR{ID: id, Go: ir.GoNamedTypeIR{PackagePath: pkg, Name: goName}, LogicalName: goName, Table: ir.TableBindingIR{PhysicalName: ir.SQLIdentifier(table)}}
	byColumn := make(map[string]ir.FieldID)
	for index, column := range columns {
		fieldID := ir.FieldID(string(id) + "." + column.goName)
		byColumn[column.name] = fieldID
		result.Fields = append(result.Fields, ir.FieldIR{
			ID: fieldID, GoName: column.goName, LogicalName: column.goName, DeclarationOrder: uint32(index), Kind: ir.FieldScalar,
			Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(column.name), Type: ir.LogicalTypeIR{Kind: column.typeKind}, Nullable: column.nullable},
		})
	}
	if len(primary) != 0 {
		result.PrimaryKey = &ir.KeyIR{ID: ir.KeyID(string(id) + ".pk"), Kind: ir.KeyPrimary, Fields: idsForColumns(byColumn, primary)}
	}
	for index, unique := range uniques {
		result.Uniques = append(result.Uniques, ir.KeyIR{ID: ir.KeyID(string(id) + ".unique." + string(rune('a'+index))), Kind: ir.KeyUnique, Fields: idsForColumns(byColumn, unique)})
	}
	return result
}

func idsForColumns(fields map[string]ir.FieldID, names []string) []ir.FieldID {
	result := make([]ir.FieldID, len(names))
	for index, name := range names {
		result[index] = fields[name]
	}
	return result
}

func rawModel(goName string, fields ...ir.RawFieldDecl) ir.RawModelDecl {
	return ir.RawModelDecl{PackagePath: testPackage, GoName: goName, Fields: fields, Span: span(1)}
}

func relationField(goName string, container ir.RawGoTypeKind, target string, line uint32, attributes ...string) ir.RawFieldDecl {
	return relationFieldTo(goName, container, testPackage, target, line, attributes...)
}

func relationFieldTo(goName string, container ir.RawGoTypeKind, targetPackage, target string, line uint32, attributes ...string) ir.RawFieldDecl {
	db := "-"
	field := ir.RawFieldDecl{
		GoName: goName, TypeSyntax: goName, DBTag: &db, Span: span(line),
		GoType: ir.RawGoTypeRef{Kind: container, Args: []ir.RawGoTypeRef{{Kind: ir.RawGoTypeNamed, PackagePath: targetPackage, GoName: target, Args: []ir.RawGoTypeRef{}, Span: span(line)}}, Span: span(line)},
	}
	for index := 0; index < len(attributes); index += 2 {
		name, value := attributes[index], attributes[index+1]
		attribute := ir.RawAttribute{Name: name, Span: span(line)}
		if value != "" {
			copyValue := value
			attribute.RawValue = &copyValue
		}
		field.GolemAttrs = append(field.GolemAttrs, attribute)
	}
	return field
}

func span(line uint32) ir.SourceSpan {
	return ir.SourceSpan{ModulePath: "example.test", RelativeFile: "models.go", StartLine: line, StartColumn: 1, EndLine: line, EndColumn: 20}
}

func assertNoDiagnostics(t *testing.T, result Result) {
	t.Helper()
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func assertCode(t *testing.T, result Result, code string) {
	t.Helper()
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %s in %#v", code, result.Diagnostics)
}
