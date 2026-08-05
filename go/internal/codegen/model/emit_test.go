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
		"var Posts = golemGeneratedPostFields{",
		"Author golem.ToOne[Post, User]",
		"golem.ToMany[User, Post]",
		"Manager golem.ToOne[User, User]",
		"golem.GeneratedScalarField[Post, string](golem.FieldID{",
		"golem.GeneratedToOne[Post, User](golem.RelationID{",
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
	assertManifestSymbol(t, result.Manifest, "example.test/app/social", "Posts", "ByID", SymbolSelector, id(2), "", "", id(61))

	compileGenerated(t, directory, map[string]string{
		"models.go": "package social\n\ntype User struct{}\ntype Post struct{}\n",
	}, result.Files)
}

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

func TestEmitIsDeterministicUnderInputShuffle(t *testing.T) {
	firstCompilation := socialCompilation()
	secondCompilation := socialCompilation()
	reverseModels(secondCompilation.Model.Models)
	for index := range secondCompilation.Model.Models {
		reverseFields(secondCompilation.Model.Models[index].Fields)
	}
	reverseRelations(secondCompilation.Model.Relations)
	reverseContracts(secondCompilation.Contract.Models)

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

func TestEmitRejectsNonCanonicalStringIDs(t *testing.T) {
	compilation := socialCompilation()
	compilation.Model.Models[0].Fields[0].ID = "user-provided-id"
	_, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
	if err == nil || !strings.Contains(err.Error(), "canonical 128-bit hexadecimal ID") {
		t.Fatalf("error = %v; want canonical fixed-width ID rejection", err)
	}
}

func socialCompilation() ir.CompilationIR {
	userID, postID := ir.ModelID(id(1)), ir.ModelID(id(2))
	authorRelation, managerRelation := ir.RelationID(id(40)), ir.RelationID(id(41))
	postFields := []ir.FieldIR{
		scalarField(id(21), "ID", 0, ir.TypeString),
		scalarField(id(22), "Title", 1, ir.TypeString),
		relationField(id(23), "Author", 2, authorRelation, ir.RelationSource, ir.RelationBelongsTo),
	}
	userFields := []ir.FieldIR{
		scalarField(id(11), "ID", 0, ir.TypeUUID),
		relationField(id(12), "Posts", 1, authorRelation, ir.RelationInverse, ir.RelationHasMany),
		relationField(id(13), "Manager", 2, managerRelation, ir.RelationSource, ir.RelationBelongsTo),
	}
	authorInverse := ir.FieldID(id(12))
	return ir.CompilationIR{
		Model: ir.ModelIR{
			Models: []ir.ModelDeclIR{
				{ID: postID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "Post"}, LogicalName: "Post", Fields: postFields},
				{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "User"}, LogicalName: "User", Fields: userFields},
			},
			Relations: []ir.RelationIR{
				{ID: authorRelation, SourceModel: postID, TargetModel: userID, SourceField: ir.FieldID(id(23)), InverseField: &authorInverse},
				{ID: managerRelation, SourceModel: userID, TargetModel: userID, SourceField: ir.FieldID(id(13))},
			},
		},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{
			{ModelID: postID, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(id(61)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(id(21))}}}},
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
	module := fmt.Sprintf("module example.test/app\n\ngo 1.23\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", golemRoot)
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
