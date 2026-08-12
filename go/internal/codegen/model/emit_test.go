package model

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestEmitSamePackageSocialAndSelfRelationsCompiles(t *testing.T) {
	compilation := socialCompilation()
	directory := t.TempDir()
	result, err := Emit(Request{
		Compilation: compilation,
		Packages: []PackageSpec{{
			ImportPath:  "example.test/app/social",
			PackageName: "social",
			Directory:   directory,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %d; want 1", len(result.Files))
	}
	source := string(result.Files[0].Source)
	for _, fragment := range []string{
		"var GolemGeneratedPostDescriptor = golem.GeneratedModelDescriptor[Post](golem.ModelID{0x00",
		"type golemGeneratedPostFields struct",
		"func (golemGeneratedPostFields) Where(predicate golem.Predicate[Post]) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) OrderBy(terms ...golem.OrderTerm[Post]) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) Take(value int) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) Skip(value int) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) Distinct(fields ...golem.Column[Post]) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) Cursor(selector golem.UniqueSelectorValue[Post]) golem.ReadOption[Post]",
		"func (golemGeneratedPostFields) Select(fields ...golem.Selection[Post]) golem.Projection[Post]",
		"func (golemGeneratedPostFields) Include(relations ...golem.RelationInclusion[Post]) golem.Projection[Post]",
		"func (golemGeneratedPostFields) Omit(fields ...golem.Column[Post]) golem.Projection[Post]",
		"var Posts = golemGeneratedPostFields{",
		"Author    golemGeneratedPostAuthorMutationRelation",
		"golem.ToMany[User, Post]",
		"Manager       golemGeneratedUserManagerMutationRelation",
		"golem.GeneratedModeTextField[Post, string](golem.FieldID{",
		"golem.GeneratedToOne[Post, User](golem.FieldID{",
		"golem.GeneratedToMany[User, Post](golem.FieldID{",
		"}, golem.RelationID{",
		"ByIDTitle golemGeneratedPostByIDTitleSelector",
		"func (golemGeneratedPostByIDSelector) Value(value0 string) golem.UniqueSelectorValue[Post]",
		"golem.GeneratedUniqueSelectorValue[Post](golem.ModelID{",
		"func GolemGeneratedDescriptors() golem.PackageDescriptors",
		"golem.GeneratedDescriptorShape(",
		"golem.GeneratedRelationMetadata(",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated source does not contain %q:\n%s", fragment, source)
		}
	}
	for _, forbidden := range []string{"FieldID(\"", "RelationID(\"", "ModelID(\"", "type Posts ", "Posts.CreateRequest"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("generated source contains forbidden type/string namespace form %q:\n%s", forbidden, source)
		}
	}

	overlay := result.Overlay()
	if got := overlay[filepath.Join(directory, BootstrapFilename)]; !bytes.Equal(got, result.Files[0].Source) {
		t.Fatalf("overlay source does not match emitted file")
	}
	assertManifestSymbol(t, result.Manifest, "example.test/app/social", "Posts", "ID", SymbolField, id(2), id(21), "", "")
	assertManifestSymbol(t, result.Manifest, "example.test/app/social", "Posts", "Author", SymbolRelation, id(2), id(23), id(40), "")
	assertManifestSymbol(t, result.Manifest, "example.test/app/social", "Posts", "ByIDTitle", SymbolSelector, id(2), "", "", id(63))
	assertManifestSymbol(t, result.Manifest, "example.test/app/social", "Posts", "ByID", SymbolSelector, id(2), "", "", id(61))
	for _, symbol := range result.Manifest.Symbols {
		if symbol.Kind == SymbolSelector && symbol.KeyID == ir.KeyID(id(63)) {
			if len(symbol.Fields) != 2 || symbol.Fields[0] != ir.FieldID(id(21)) || symbol.Fields[1] != ir.FieldID(id(22)) {
				t.Fatalf("composite selector manifest fields=%v", symbol.Fields)
			}
		}
	}

	compileGenerated(t, directory, map[string]string{
		"models.go": "package social\n\ntype User struct{}\ntype Post struct{}\n",
	}, result.Files)
}

func TestScalarHandleMappingIsExactForEveryLogicalFamily(t *testing.T) {
	const enumID ir.EnumID = "90000000000000000000000000000000"
	enums := map[ir.EnumID]ir.EnumIR{
		enumID: {ID: enumID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "Visibility"}},
	}
	element := ir.LogicalTypeIR{Kind: ir.TypeString}
	enumElement := ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: pointer(enumID)}
	tests := []struct {
		name     string
		logical  ir.LogicalTypeIR
		nullable bool
		wantType string
		wantInit string
	}{
		{"bool", ir.LogicalTypeIR{Kind: ir.TypeBool}, false, "golem.EqualField[Post, bool]", "golem.GeneratedEqualField[Post, bool]"},
		{"nullable bool", ir.LogicalTypeIR{Kind: ir.TypeBool}, true, "golem.NullableEqualField[Post, bool]", "golem.GeneratedNullableEqualField[Post, bool]"},
		{"uuid", ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, "golem.EqualField[Post, golem.UUID]", "golem.GeneratedEqualField[Post, golem.UUID]"},
		{"enum", ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: pointer(enumID)}, false, "golem.EqualField[Post, Visibility]", "golem.GeneratedEqualField[Post, Visibility]"},
		{"int16", ir.LogicalTypeIR{Kind: ir.TypeInt16}, false, "golem.OrderedField[Post, int16]", "golem.GeneratedOrderedField[Post, int16]"},
		{"int32", ir.LogicalTypeIR{Kind: ir.TypeInt32}, false, "golem.OrderedField[Post, int32]", "golem.GeneratedOrderedField[Post, int32]"},
		{"int64", ir.LogicalTypeIR{Kind: ir.TypeInt64}, false, "golem.OrderedField[Post, int64]", "golem.GeneratedOrderedField[Post, int64]"},
		{"float32", ir.LogicalTypeIR{Kind: ir.TypeFloat32}, false, "golem.OrderedField[Post, float32]", "golem.GeneratedOrderedField[Post, float32]"},
		{"float64", ir.LogicalTypeIR{Kind: ir.TypeFloat64}, false, "golem.OrderedField[Post, float64]", "golem.GeneratedOrderedField[Post, float64]"},
		{"decimal", ir.LogicalTypeIR{Kind: ir.TypeDecimal}, false, "golem.OrderedField[Post, golem.Decimal]", "golem.GeneratedOrderedField[Post, golem.Decimal]"},
		{"date", ir.LogicalTypeIR{Kind: ir.TypeDate}, false, "golem.OrderedField[Post, golem.Date]", "golem.GeneratedOrderedField[Post, golem.Date]"},
		{"time", ir.LogicalTypeIR{Kind: ir.TypeTime}, false, "golem.OrderedField[Post, golem.Time]", "golem.GeneratedOrderedField[Post, golem.Time]"},
		{"datetime", ir.LogicalTypeIR{Kind: ir.TypeDateTime}, true, "golem.NullableOrderedField[Post, time.Time]", "golem.GeneratedNullableOrderedField[Post, time.Time]"},
		{"string", ir.LogicalTypeIR{Kind: ir.TypeString}, false, "golem.ModeTextField[Post, string]", "golem.GeneratedModeTextField[Post, string]"},
		{"nullable string", ir.LogicalTypeIR{Kind: ir.TypeString}, true, "golem.NullableModeTextField[Post, string]", "golem.GeneratedNullableModeTextField[Post, string]"},
		{"bytes", ir.LogicalTypeIR{Kind: ir.TypeBytes}, false, "golem.BytesField[Post]", "golem.GeneratedBytesField[Post]"},
		{"nullable bytes", ir.LogicalTypeIR{Kind: ir.TypeBytes}, true, "golem.NullableBytesField[Post]", "golem.GeneratedNullableBytesField[Post]"},
		{"list", ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &element}, false, "golem.ListField[Post, string]", "golem.GeneratedListField[Post, string]"},
		{"nullable list", ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &element}, true, "golem.NullableListField[Post, string]", "golem.GeneratedNullableListField[Post, string]"},
		{"enum list", ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &enumElement}, false, "golem.ListField[Post, Visibility]", "golem.GeneratedListField[Post, Visibility]"},
		{"json", ir.LogicalTypeIR{Kind: ir.TypeJSON}, false, "golem.ModeJSONField[Post]", "golem.GeneratedModeJSONField[Post]"},
		{"nullable json", ir.LogicalTypeIR{Kind: ir.TypeJSON}, true, "golem.NullableModeJSONField[Post]", "golem.GeneratedNullableModeJSONField[Post]"},
	}
	models := map[ir.ModelID]ir.ModelDeclIR{ir.ModelID(id(2)): {ID: ir.ModelID(id(2)), Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "Post"}}}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := ir.FieldIR{ID: ir.FieldID(id(100 + index)), Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: test.logical, Nullable: test.nullable}}
			imports := newImports("example.test/app/social", DefaultGolemImportPath)
			gotType, err := fieldHandle(field, ir.ModelID(id(2)), models, enums, nil, imports)
			if err != nil {
				t.Fatal(err)
			}
			gotInit, err := fieldInitializer(field, ir.ModelID(id(2)), models, enums, nil, imports, "")
			if err != nil {
				t.Fatal(err)
			}
			if gotType != test.wantType || !strings.HasPrefix(gotInit, test.wantInit+"(") {
				t.Fatalf("type=%q init=%q; want type=%q init prefix=%q", gotType, gotInit, test.wantType, test.wantInit)
			}
		})
	}
}

func TestMutationSurfaceCapabilitiesCompileAndIllegalUsesDoNot(t *testing.T) {
	compilation := socialCompilation()
	var post *ir.ModelDeclIR
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].LogicalName == "Post" {
			post = &compilation.Model.Models[index]
			break
		}
	}
	if post == nil {
		t.Fatal("post fixture missing")
	}
	secret := scalarField(id(81), "Secret", 10, ir.TypeString)
	password := scalarField(id(82), "Password", 11, ir.TypeString)
	locked := scalarField(id(83), "Locked", 12, ir.TypeString)
	counter := scalarField(id(84), "Counter", 13, ir.TypeInt64)
	counter.Scalar.Nullable = true
	readOnly := scalarField(id(85), "ReadOnly", 14, ir.TypeString)
	generated := scalarField(id(86), "Generated", 15, ir.TypeString)
	generated.Scalar.Generation = &ir.GeneratedColumnIR{Storage: ir.GeneratedStored, Provider: ir.ProviderScopePortable}
	post.Fields = append(post.Fields, secret, password, locked, counter, readOnly, generated)
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].ModelID != post.ID {
			continue
		}
		compilation.Contract.Models[index].Fields = append(compilation.Contract.Models[index].Fields,
			ir.FieldContractIR{FieldID: secret.ID, Modes: []ir.FieldMode{ir.ModeHidden}},
			ir.FieldContractIR{FieldID: password.ID, Modes: []ir.FieldMode{ir.ModeWriteOnly, ir.ModeImmutable}},
			ir.FieldContractIR{FieldID: locked.ID, Modes: []ir.FieldMode{ir.ModeImmutable}},
			ir.FieldContractIR{FieldID: counter.ID, Modes: []ir.FieldMode{ir.ModeVisible}},
			ir.FieldContractIR{FieldID: readOnly.ID, Modes: []ir.FieldMode{ir.ModeReadOnly}},
			ir.FieldContractIR{FieldID: generated.ID, Modes: []ir.FieldMode{ir.ModeVisible}},
		)
	}
	root := t.TempDir()
	result, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social", Directory: root}}})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	for _, required := range []string{
		"func (golemGeneratedPostFields) Create(values ...golem.CreateValue[Post]) PostCreateInput",
		"func (golemGeneratedPostFields) Update(first golem.UpdateValue[Post], rest ...golem.UpdateValue[Post]) PostUpdateInput",
		"func (golemGeneratedPostFields) UpdateMany(first golem.UpdateManyValue[Post], rest ...golem.UpdateManyValue[Post]) PostUpdateManyInput",
		"func (field golemGeneratedPostPasswordMutationField) Create(value string)",
		"func (field golemGeneratedPostLockedMutationField) Create(value string)",
		"func (field golemGeneratedPostCounterMutationField) Null()",
		"func (field golemGeneratedPostCounterMutationField) Increment(value int64)",
		"func (field golemGeneratedPostCounterMutationField) Decrement(value int64)",
		"CreateFieldCapability[Post, string]",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("generated mutation surface missing %q:\n%s", required, source)
		}
	}
	for _, forbidden := range []string{
		"Secret ",
		"func (field golemGeneratedPostPasswordMutationField) Set(",
		"func (field golemGeneratedPostLockedMutationField) Set(",
		"golemGeneratedPostReadOnlyMutationField",
		"golemGeneratedPostGeneratedMutationField",
		"golemGeneratedPostTitleMutationField",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("generated mutation surface contains forbidden capability %q:\n%s", forbidden, source)
		}
	}
	compileGenerated(t, root, map[string]string{
		"models.go": "package social\n\ntype User struct{}\ntype Post struct{}\n",
		"usage.go": `package social

import golem "github.com/eleven-am/golem/go/golem"

var _ PostCreateInput = Posts.Create(Posts.ID.Create("id"), Posts.Password.Create("secret"), Posts.Locked.Create("once"), Posts.Counter.Create(1))
var _ PostUpdateInput = Posts.Update(Posts.ID.Set("next"), Posts.Counter.Increment(1), Posts.Counter.Decrement(1), Posts.Counter.Null())
var _ PostUpdateManyInput = Posts.UpdateMany(Posts.Counter.Set(2))
var _ golem.Projection[Post] = Posts.Select(Posts.ID, Posts.ReadOnly)
var _ golem.ReadOption[Post] = Posts.Omit(Posts.ReadOnly)

func mutateCreateHook(request *PostCreateHookRequest) error {
	return golem.SetCreate(request, Posts.Password, "changed")
}
`,
	}, result.Files)

	negative := []struct {
		name, usage, want string
	}{
		{"hidden", "var _ = Posts.Secret\n", "Posts.Secret undefined"},
		{"read only create", "var _ = Posts.ReadOnly.Create(\"x\")\n", "Posts.ReadOnly.Create undefined"},
		{"generated create", "var _ = Posts.Generated.Create(\"x\")\n", "Posts.Generated.Create undefined"},
		{"database read only create", "var _ = Posts.Title.Create(\"x\")\n", "Posts.Title.Create undefined"},
		{"immutable update", "var _ = Posts.Locked.Set(\"x\")\n", "Posts.Locked.Set undefined"},
		{"write only projection", "var _ = Posts.Select(Posts.Password)\n", "does not implement golem.Selection"},
		{"read only hook write", "func bad(request *PostCreateHookRequest) error { return golem.SetCreate(request, Posts.ReadOnly, \"x\") }\n", "does not match inferred type golem.CreateFieldCapability"},
		{"cross model input", "var _ = Users.Create(Posts.ID.Create(\"x\"))\n", "does not implement golem.CreateValue[User]"},
		{"non projection option", "var _ golem.Projection[Post] = Posts.Where(Posts.ID.Eq(\"x\"))\n", "does not implement golem.Projection"},
		{"empty update", "var _ = Posts.Update()\n", "not enough arguments"},
		{"empty update many", "var _ = Posts.UpdateMany()\n", "not enough arguments"},
	}
	for _, test := range negative {
		t.Run(test.name, func(t *testing.T) {
			compileGeneratedFailure(t, result.Files, test.usage, test.want)
		})
	}
}

func TestNestedMutationSurfaceCompilesExactCardinalityRequirednessOwnershipAndExposure(t *testing.T) {
	compilation := socialCompilation()
	var post *ir.ModelDeclIR
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].LogicalName == "Post" {
			post = &compilation.Model.Models[index]
			break
		}
	}
	if post == nil {
		t.Fatal("post fixture missing")
	}
	authorID := scalarField(id(87), "AuthorID", 20, ir.TypeUUID)
	reviewerID := scalarField(id(88), "ReviewerID", 21, ir.TypeUUID)
	reviewerID.Scalar.Nullable = true
	post.Fields = append(post.Fields, authorID, reviewerID)
	for index := range compilation.Model.Relations {
		relation := &compilation.Model.Relations[index]
		switch relation.ID {
		case ir.RelationID(id(40)):
			relation.LocalFields = []ir.FieldID{authorID.ID}
			relation.RemoteFields = []ir.FieldID{ir.FieldID(id(11))}
		case ir.RelationID(id(42)):
			relation.LocalFields = []ir.FieldID{reviewerID.ID}
			relation.RemoteFields = []ir.FieldID{ir.FieldID(id(11))}
		}
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		if contract.ModelID == ir.ModelID(id(1)) {
			contract.Fields = append(contract.Fields,
				ir.FieldContractIR{FieldID: ir.FieldID(id(13)), Modes: []ir.FieldMode{ir.ModeImmutable}},
				ir.FieldContractIR{FieldID: ir.FieldID(id(14)), Modes: []ir.FieldMode{ir.ModeWriteOnly}},
				ir.FieldContractIR{FieldID: ir.FieldID(id(15)), Modes: []ir.FieldMode{ir.ModeHidden}},
			)
		}
	}
	root := t.TempDir()
	result, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social", Directory: root}}})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	for _, required := range []string{
		"func (golemGeneratedPostAuthorMutationRelation) Connect(target golem.MutationTarget[User])",
		"func (golemGeneratedPostReviewerMutationRelation) Disconnect()",
		"func (golemGeneratedUserPostsMutationRelation) CreateMany(first golem.CreateInput[Post], rest ...golem.CreateInput[Post])",
		"func (golemGeneratedUserPostsMutationRelation) UpdateMany(predicate golem.Predicate[Post]",
		"func (golemGeneratedUserPostsMutationRelation) DeleteMany(predicate golem.Predicate[Post])",
		"func (golemGeneratedUserReportsMutationRelation) Connect(first golem.MutationTarget[User], rest ...golem.MutationTarget[User])",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("generated nested mutation surface missing %q:\n%s", required, source)
		}
	}
	for _, forbidden := range []string{
		"func (golemGeneratedPostAuthorMutationRelation) Disconnect()",
		"func (golemGeneratedPostAuthorMutationRelation) Delete()",
		"func (golemGeneratedPostAuthorMutationRelation) CreateMany(",
		"ReviewedPosts ",
		"func (golemGeneratedPostFields) CreateMany(",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("generated nested mutation surface contains forbidden capability %q:\n%s", forbidden, source)
		}
	}
	compileGenerated(t, root, map[string]string{
		"models.go": "package social\n\ntype User struct{}\ntype Post struct{}\n",
		"usage.go": `package social

import golem "github.com/eleven-am/golem/go/golem"

var userID = golem.NewUUID([16]byte{1})
var userTarget = Users.ByID.Value(userID)
var postTarget = Posts.ByID.Value("post")
var compoundPostTarget = Posts.ByIDTitle.Value("post", "title")
var userCreate = Users.Create(Users.ID.Create(userID))
var userUpdate = Users.Update(Users.ID.Set(userID))
var postCreate = Posts.Create(Posts.ID.Create("created"))
var postUpdate = Posts.Update(Posts.ID.Set("updated"))
var postUpdateMany = Posts.UpdateMany(Posts.ID.Set("many"))

var _ PostCreateInput = Posts.Create(Posts.Author.Connect(userTarget), Posts.Reviewer.ConnectOrCreate(userTarget, userCreate))
var _ PostUpdateInput = Posts.Update(Posts.Author.Update(userUpdate), Posts.Author.Upsert(userCreate, userUpdate), Posts.Reviewer.Disconnect(), Posts.Reviewer.Delete())
var _ UserCreateInput = Users.Create(Users.Posts.Create(postCreate), Users.Posts.CreateMany(postCreate), Users.Posts.Connect(postTarget, compoundPostTarget), Users.Posts.ConnectOrCreate(postTarget, postCreate))
var _ UserCreateInput = Users.Create(Users.Manager.Connect(userTarget))
var _ UserCreateInput = Users.Create(Users.Reports.Connect(userTarget))
var _ UserUpdateInput = Users.Update(
	Users.Posts.Disconnect(postTarget),
	Users.Posts.Set(postTarget),
	Users.Posts.Update(postTarget, postUpdate),
	Users.Posts.UpdateMany(Posts.ID.Eq("post"), postUpdateMany),
	Users.Posts.Upsert(postTarget, postCreate, postUpdate),
	Users.Posts.Delete(postTarget),
	Users.Posts.DeleteMany(Posts.ID.Eq("post")),
)
`,
	}, result.Files)

	negative := []struct {
		name, usage, want string
	}{
		{"required disconnect", "var _ = Posts.Author.Disconnect()\n", "Posts.Author.Disconnect undefined"},
		{"to one list connect", "var _ = Posts.Author.Connect(Users.ByID.Value(golem.NewUUID([16]byte{})), Users.ByID.Value(golem.NewUUID([16]byte{})))\n", "too many arguments"},
		{"to one create many", "var _ = Posts.Author.CreateMany()\n", "Posts.Author.CreateMany undefined"},
		{"to one set", "var _ = Posts.Author.Set()\n", "Posts.Author.Set undefined"},
		{"cross model selector", "var _ = Posts.Author.Connect(Posts.ByID.Value(\"post\"))\n", "cannot use Posts.ByID.Value"},
		{"cross model input", "var _ = Users.Posts.Create(Users.Create())\n", "cannot use Users.Create()"},
		{"cross model predicate", "var _ = Users.Posts.DeleteMany(Users.ID.Eq(golem.NewUUID([16]byte{})))\n", "cannot use Users.ID.Eq"},
		{"top level update many relation", "var _ = Posts.UpdateMany(Posts.Reviewer.Disconnect())\n", "does not implement golem.UpdateManyValue[Post]"},
		{"hidden relation", "var _ = Users.ReviewedPosts\n", "Users.ReviewedPosts undefined"},
		{"write only relation projection", "var _ = Users.Select(Users.Reports)\n", "does not implement golem.Selection"},
		{"immutable relation update", "var _ = Users.Update(Users.Manager.Connect(Users.ByID.Value(golem.NewUUID([16]byte{}))))\n", "does not implement golem.UpdateValue[User]"},
		{"top level create many", "var _ = Posts.CreateMany()\n", "Posts.CreateMany undefined"},
		{"to many create requires input", "var _ = Users.Posts.Create()\n", "not enough arguments"},
		{"to many create-many requires input", "var _ = Users.Posts.CreateMany()\n", "not enough arguments"},
		{"to many connect requires target", "var _ = Users.Posts.Connect()\n", "not enough arguments"},
		{"to many disconnect requires target", "var _ = Users.Posts.Disconnect()\n", "not enough arguments"},
	}
	for _, test := range negative {
		t.Run(test.name, func(t *testing.T) {
			compileGeneratedFailure(t, result.Files, test.usage, test.want)
		})
	}
}

func TestInverseHasOneRequirednessComesFromSourceCorrelationFields(t *testing.T) {
	compilation := socialCompilation()
	post, user := &compilation.Model.Models[0], &compilation.Model.Models[1]
	authorID := scalarField(id(87), "AuthorID", 20, ir.TypeUUID)
	post.Fields = append(post.Fields, authorID)
	for index := range user.Fields {
		if user.Fields[index].ID == ir.FieldID(id(12)) {
			user.Fields[index].Relation.Kind = ir.RelationHasOne
		}
	}
	for index := range compilation.Model.Relations {
		if compilation.Model.Relations[index].ID == ir.RelationID(id(40)) {
			compilation.Model.Relations[index].LocalFields = []ir.FieldID{authorID.ID}
			compilation.Model.Relations[index].RemoteFields = []ir.FieldID{ir.FieldID(id(11))}
		}
	}
	emit := func(t *testing.T, value ir.CompilationIR) string {
		t.Helper()
		result, err := Emit(Request{Compilation: value, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(result.Files[0].Source)
	}
	required := emit(t, compilation)
	if forbidden := "func (golemGeneratedUserPostsMutationRelation) Disconnect()"; strings.Contains(required, forbidden) {
		t.Fatalf("required inverse has-one exposed optional-only method %q", forbidden)
	}
	if method := "func (golemGeneratedUserPostsMutationRelation) Delete()"; !strings.Contains(required, method) {
		t.Fatalf("required inverse has-one omitted safe child delete %q", method)
	}
	post.Fields[len(post.Fields)-1].Scalar.Nullable = true
	optional := emit(t, compilation)
	for _, requiredMethod := range []string{
		"func (golemGeneratedUserPostsMutationRelation) Disconnect()",
		"func (golemGeneratedUserPostsMutationRelation) Delete()",
	} {
		if !strings.Contains(optional, requiredMethod) {
			t.Fatalf("optional inverse has-one omitted %q", requiredMethod)
		}
	}
}

func pointer[T any](value T) *T { return &value }

func TestEmitCrossPackageRelationCompilesWithoutDescriptorPointers(t *testing.T) {
	root := t.TempDir()
	accountsDir := filepath.Join(root, "accounts")
	contentDir := filepath.Join(root, "content")
	compilation := crossPackageCompilation()
	result, err := Emit(Request{
		Compilation: compilation,
		Packages: []PackageSpec{
			{ImportPath: "example.test/app/content", PackageName: "content", Directory: contentDir},
			{ImportPath: "example.test/app/accounts", PackageName: "accounts", Directory: accountsDir},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("files = %d; want 2", len(result.Files))
	}
	var contentSource string
	for _, file := range result.Files {
		if file.ImportPath == "example.test/app/content" {
			contentSource = string(file.Source)
		}
	}
	if !strings.Contains(contentSource, "golem.ToOne[Post, accounts.User]") {
		t.Fatalf("cross-package target is not typed correctly:\n%s", contentSource)
	}
	for _, forbidden := range []string{"&accounts.", "accounts.Users", "*golem.ModelDescriptor"} {
		if strings.Contains(contentSource, forbidden) {
			t.Fatalf("relation bootstrap contains initialization pointer dependency %q:\n%s", forbidden, contentSource)
		}
	}
	compileGenerated(t, root, map[string]string{
		"accounts/models.go": "package accounts\n\ntype User struct{}\n",
		"content/models.go":  "package content\n\ntype Post struct{}\n",
	}, result.Files)
}

func TestFinalDescriptorAccessorCarriesGenerationDigest(t *testing.T) {
	result, err := Emit(Request{
		Compilation: socialCompilation(),
		Packages:    []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}},
		FinalStamp:  &FinalStamp{GenerationDigest: strings.Repeat("a", 64), GeneratorVersion: "generator", TemplateABIVersion: "template"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	if !strings.Contains(source, "golem.GeneratedStampedPackageDescriptors(golem.SchemaDigest{0xaa") {
		t.Fatalf("final descriptor accessor lacks fixed-width generation stamp:\n%s", source)
	}
	if !strings.Contains(source, "golem.GeneratedStampedModelDescriptor[Post](golem.SchemaDigest{0xaa") {
		t.Fatalf("final model descriptor lacks its immutable generation stamp:\n%s", source)
	}
	if !strings.Contains(source, "golem.GeneratedStampedModeTextField[Post, string](golem.SchemaDigest{0xaa") || !strings.Contains(source, "golem.GeneratedStampedToOne[Post, User](golem.SchemaDigest{0xaa") {
		t.Fatalf("final scalar or relation field lacks its immutable generation stamp:\n%s", source)
	}
}

func TestEmitIsDeterministicUnderInputShuffle(t *testing.T) {
	firstCompilation := socialCompilation()
	secondCompilation := socialCompilation()
	reverseModels(secondCompilation.Model.Models)
	for index := range secondCompilation.Model.Models {
		reverseFields(secondCompilation.Model.Models[index].Fields)
	}
	reverseRelations(secondCompilation.Model.Relations)
	reverseContracts(secondCompilation.Contract.Models)
	for index := range secondCompilation.Contract.Models {
		reverseSelectors(secondCompilation.Contract.Models[index].Selectors)
	}

	first, err := Emit(Request{Compilation: firstCompilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Emit(Request{Compilation: secondCompilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != len(second.Files) || !bytes.Equal(first.Files[0].Source, second.Files[0].Source) {
		t.Fatalf("shuffle changed source:\n--- first ---\n%s\n--- second ---\n%s", first.Files[0].Source, second.Files[0].Source)
	}
	if fmt.Sprintf("%#v", first.Manifest) != fmt.Sprintf("%#v", second.Manifest) {
		t.Fatalf("shuffle changed manifest:\nfirst: %#v\nsecond: %#v", first.Manifest, second.Manifest)
	}
}

func TestDescriptorShapePinsScanAndWriteFieldOrder(t *testing.T) {
	compilation := socialCompilation()
	models := map[ir.ModelID]ir.ModelDeclIR{}
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	relations := map[ir.RelationID]ir.RelationIR{}
	for _, relation := range compilation.Model.Relations {
		relations[relation.ID] = relation
	}
	post := models[ir.ModelID(id(2))]
	var contract ir.ModelContractIR
	for _, candidate := range compilation.Contract.Models {
		if candidate.ModelID == post.ID {
			contract = candidate
		}
	}
	shape, err := descriptorShapeLiteral(post, contract, models, relations)
	if err != nil {
		t.Fatal(err)
	}
	scan, _ := fieldIDSliceLiteral([]ir.FieldID{ir.FieldID(id(21)), ir.FieldID(id(22))})
	write, _ := fieldIDSliceLiteral([]ir.FieldID{ir.FieldID(id(21))})
	if !strings.HasPrefix(shape, "golem.GeneratedDescriptorShape("+scan+", "+write+",") {
		t.Fatalf("scan/write order not pinned:\n%s", shape)
	}
	if !strings.Contains(shape, "golem.RelationSource, golem.RelationToOne") {
		t.Fatalf("source relation endpoint metadata missing:\n%s", shape)
	}
	user := models[ir.ModelID(id(1))]
	var userContract ir.ModelContractIR
	for _, candidate := range compilation.Contract.Models {
		if candidate.ModelID == user.ID {
			userContract = candidate
		}
	}
	userShape, err := descriptorShapeLiteral(user, userContract, models, relations)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"golem.RelationSource, golem.RelationToOne", "golem.RelationInverse, golem.RelationToMany"} {
		if !strings.Contains(userShape, fragment) {
			t.Fatalf("recursive relation endpoint metadata missing %q:\n%s", fragment, userShape)
		}
	}
}

func TestEmitRejectsNonCanonicalStringIDs(t *testing.T) {
	compilation := socialCompilation()
	compilation.Model.Models[0].Fields[0].ID = "user-provided-id"
	_, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "canonical 128-bit hexadecimal ID") {
		t.Fatalf("error = %v; want canonical fixed-width ID rejection", err)
	}
}

func TestEmitRejectsSelectorNamespaceAndModelIDCollisions(t *testing.T) {
	compilation := socialCompilation()
	compilation.Model.Models[0].Fields[0].GoName = "Where"
	_, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "read method") {
		t.Fatalf("read method collision error=%v", err)
	}

	compilation = socialCompilation()
	compilation.Contract.Models[0].Selectors[1].Name = "ID"
	_, err = Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("selector collision error=%v", err)
	}

	compilation = socialCompilation()
	compilation.Model.Models[1].ID = compilation.Model.Models[0].ID
	_, err = Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate model ID") {
		t.Fatalf("model ID collision error=%v", err)
	}

	compilation = socialCompilation()
	compilation.Contract.Models[0].Selectors[1].Fields = nil
	_, err = Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "no identity fields") {
		t.Fatalf("empty selector error=%v", err)
	}
}

func socialCompilation() ir.CompilationIR {
	userID, postID := ir.ModelID(id(1)), ir.ModelID(id(2))
	authorRelation, managerRelation, reviewerRelation := ir.RelationID(id(40)), ir.RelationID(id(41)), ir.RelationID(id(42))
	postFields := []ir.FieldIR{
		scalarField(id(21), "ID", 0, ir.TypeString),
		scalarField(id(22), "Title", 1, ir.TypeString),
		relationField(id(23), "Author", 2, authorRelation, ir.RelationSource, ir.RelationBelongsTo),
		relationField(id(24), "Reviewer", 3, reviewerRelation, ir.RelationSource, ir.RelationBelongsTo),
	}
	postFields[1].Scalar.DatabaseReadOnly = true
	userFields := []ir.FieldIR{
		scalarField(id(11), "ID", 0, ir.TypeUUID),
		relationField(id(12), "Posts", 1, authorRelation, ir.RelationInverse, ir.RelationHasMany),
		relationField(id(13), "Manager", 2, managerRelation, ir.RelationSource, ir.RelationBelongsTo),
		relationField(id(14), "Reports", 3, managerRelation, ir.RelationInverse, ir.RelationHasMany),
		relationField(id(15), "ReviewedPosts", 4, reviewerRelation, ir.RelationInverse, ir.RelationHasMany),
	}
	authorInverse, managerInverse, reviewerInverse := ir.FieldID(id(12)), ir.FieldID(id(14)), ir.FieldID(id(15))
	return ir.CompilationIR{
		Model: ir.ModelIR{
			Models: []ir.ModelDeclIR{
				{ID: postID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "Post"}, LogicalName: "Post", Fields: postFields},
				{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "User"}, LogicalName: "User", Fields: userFields},
			},
			Relations: []ir.RelationIR{
				{ID: authorRelation, SourceModel: postID, TargetModel: userID, SourceField: ir.FieldID(id(23)), InverseField: &authorInverse},
				{ID: managerRelation, SourceModel: userID, TargetModel: userID, SourceField: ir.FieldID(id(13)), InverseField: &managerInverse},
				{ID: reviewerRelation, SourceModel: postID, TargetModel: userID, SourceField: ir.FieldID(id(24)), InverseField: &reviewerInverse},
			},
		},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{
			{ModelID: postID, Fields: []ir.FieldContractIR{{FieldID: ir.FieldID(id(22)), Modes: []ir.FieldMode{ir.ModeReadOnly}}}, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(id(61)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(id(21))}}, {KeyID: ir.KeyID(id(63)), Kind: ir.KeyUnique, Name: "ByIDTitle", Fields: []ir.FieldID{ir.FieldID(id(21)), ir.FieldID(id(22))}}}},
			{ModelID: userID, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(id(62)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(id(11))}}}},
		}},
	}
}

func crossPackageCompilation() ir.CompilationIR {
	userID, postID := ir.ModelID(id(71)), ir.ModelID(id(72))
	relationID := ir.RelationID(id(73))
	return ir.CompilationIR{Model: ir.ModelIR{
		Models: []ir.ModelDeclIR{
			{ID: postID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/content", Name: "Post"}, LogicalName: "Post", Fields: []ir.FieldIR{
				scalarField(id(74), "ID", 0, ir.TypeString),
				relationField(id(75), "Author", 1, relationID, ir.RelationSource, ir.RelationBelongsTo),
			}},
			{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/accounts", Name: "User"}, LogicalName: "User", Fields: []ir.FieldIR{scalarField(id(76), "ID", 0, ir.TypeUUID)}},
		},
		Relations: []ir.RelationIR{{ID: relationID, SourceModel: postID, TargetModel: userID, SourceField: ir.FieldID(id(75))}},
	}}
}

func scalarField(identifier, name string, order uint32, kind ir.LogicalTypeKind) ir.FieldIR {
	return ir.FieldIR{ID: ir.FieldID(identifier), GoName: name, DeclarationOrder: order, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: kind}}}
}

func relationField(identifier, name string, order uint32, relationID ir.RelationID, role ir.RelationEndpointRole, kind ir.RelationKind) ir.FieldIR {
	return ir.FieldIR{ID: ir.FieldID(identifier), GoName: name, DeclarationOrder: order, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relationID, Role: role, Kind: kind}}
}

func id(value int) string { return fmt.Sprintf("%032x", value) }

func assertManifestSymbol(t *testing.T, manifest Manifest, packagePath, namespace, name string, kind SymbolKind, modelID, fieldID, relationID, keyID string) {
	t.Helper()
	for _, symbol := range manifest.Symbols {
		if symbol.PackagePath == packagePath && symbol.Namespace == namespace && symbol.Name == name && symbol.Kind == kind {
			if symbol.ModelID != ir.ModelID(modelID) || symbol.FieldID != ir.FieldID(fieldID) || symbol.RelationID != ir.RelationID(relationID) || symbol.KeyID != ir.KeyID(keyID) {
				t.Fatalf("manifest symbol = %#v; IDs do not match", symbol)
			}
			return
		}
	}
	t.Fatalf("manifest symbol %s.%s (%s) not found in %#v", namespace, name, kind, manifest.Symbols)
}

func compileGenerated(t *testing.T, root string, handwritten map[string]string, files []File) {
	t.Helper()
	_, currentFile, _, _ := runtime.Caller(0)
	golemRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	module := fmt.Sprintf("module example.test/app\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", golemRoot)
	writeTestFile(t, filepath.Join(root, "go.mod"), module)
	for name, source := range handwritten {
		writeTestFile(t, filepath.Join(root, name), source)
	}
	for _, file := range files {
		writeTestFile(t, file.Path, string(file.Source))
	}
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated packages do not compile: %v\n%s", err, output)
	}
}

func compileGeneratedFailure(t *testing.T, files []File, usage, want string) {
	t.Helper()
	root := t.TempDir()
	_, currentFile, _, _ := runtime.Caller(0)
	golemRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	module := fmt.Sprintf("module example.test/app\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", golemRoot)
	writeTestFile(t, filepath.Join(root, "go.mod"), module)
	writeTestFile(t, filepath.Join(root, "models.go"), "package social\n\ntype User struct{}\ntype Post struct{}\n")
	writeTestFile(t, filepath.Join(root, "usage.go"), "package social\n\nimport golem \"github.com/eleven-am/golem/go/golem\"\n\nvar _ = golem.All[Post]\n"+usage)
	for _, file := range files {
		writeTestFile(t, filepath.Join(root, filepath.Base(file.Path)), string(file.Source))
	}
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("illegal generated usage compiled successfully:\n%s", usage)
	}
	if !strings.Contains(string(output), want) {
		t.Fatalf("compile failure missing %q: %v\n%s", want, err, output)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func reverseModels(values []ir.ModelDeclIR) {
	sort.SliceStable(values, func(i, j int) bool { return i > j })
}
func reverseFields(values []ir.FieldIR) {
	sort.SliceStable(values, func(i, j int) bool { return i > j })
}
func reverseRelations(values []ir.RelationIR) {
	sort.SliceStable(values, func(i, j int) bool { return i > j })
}
func reverseContracts(values []ir.ModelContractIR) {
	sort.SliceStable(values, func(i, j int) bool { return i > j })
}

func reverseSelectors(values []ir.SelectorContractIR) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
