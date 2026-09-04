package model

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func systemOwnedCompilation() ir.CompilationIR {
	postID := ir.ModelID(id(2))
	fields := []ir.FieldIR{
		scalarField(id(21), "ID", 0, ir.TypeString),
		scalarField(id(22), "TagCount", 1, ir.TypeInt32),
		scalarField(id(23), "CreatedBy", 2, ir.TypeUUID),
	}
	return ir.CompilationIR{
		Model: ir.ModelIR{Models: []ir.ModelDeclIR{
			{ID: postID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Post"}, LogicalName: "Post", Fields: fields},
		}},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{{
			ModelID: postID,
			Fields: []ir.FieldContractIR{
				{FieldID: ir.FieldID(id(22)), Modes: []ir.FieldMode{ir.ModeSystem}},
				{FieldID: ir.FieldID(id(23)), Modes: []ir.FieldMode{ir.ModeSystem, ir.ModeImmutable}},
			},
			Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(id(61)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(id(21))}}},
		}}},
	}
}

func systemOwnedFiles(t *testing.T) []File {
	t.Helper()
	result, err := Emit(Request{
		Compilation: systemOwnedCompilation(),
		Packages:    []PackageSpec{{ImportPath: "example.test/app", PackageName: "social"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Files
}

func collapseSpacing(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func TestSystemFieldWithholdsCallerWriteBuildersAndKeepsSystemOnes(t *testing.T) {
	source := string(systemOwnedFiles(t)[0].Source)
	spaced := collapseSpacing(source)
	for _, forbidden := range []string{
		"func (golemGeneratedPostTagCountAnalyticsField) Set(",
		"func (golemGeneratedPostTagCountAnalyticsField) Increment(",
		"func (golemGeneratedPostTagCountMutationField)",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("caller surface kept %q:\n%s", forbidden, source)
		}
	}
	for _, fragment := range []string{
		"type golemGeneratedPostSystemFields struct",
		"func (golemGeneratedPostFields) System() golemGeneratedPostSystemFields",
		"TagCount golemGeneratedPostTagCountSystemMutationField",
		"CreatedBy golemGeneratedPostCreatedBySystemMutationField",
		"func (field golemGeneratedPostTagCountSystemMutationField) Increment(value int32) golem.UpdateManyValue[Post]",
		"func (field golemGeneratedPostTagCountSystemMutationField) Set(value int32) golem.UpdateManyValue[Post]",
		"func (field golemGeneratedPostCreatedBySystemMutationField) Create(value golem.UUID) golem.CreateValue[Post]",
	} {
		if !strings.Contains(spaced, collapseSpacing(fragment)) {
			t.Errorf("system surface missing %q:\n%s", fragment, source)
		}
	}
	if strings.Contains(source, "func (field golemGeneratedPostCreatedBySystemMutationField) Set(") {
		t.Errorf("system immutable field kept an update builder:\n%s", source)
	}
}

func TestSystemFieldWriteBuilderIsNotReachableFromTheCallerNamespace(t *testing.T) {
	files := systemOwnedFiles(t)
	compileGeneratedFailure(t, files, "var _ = Posts.TagCount.Increment(1)\n", "Increment undefined")
	compileGeneratedFailure(t, files, "var _ = Posts.System().CreatedBy.Set(golem.NewUUID([16]byte{1}))\n", "Set undefined")
}

func TestSystemFieldNamespaceCompiles(t *testing.T) {
	directory := t.TempDir()
	result, err := Emit(Request{
		Compilation: systemOwnedCompilation(),
		Packages:    []PackageSpec{{ImportPath: "example.test/app", PackageName: "social", Directory: directory}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compileGenerated(t, directory, map[string]string{
		"models.go": "package social\n\ntype Post struct{}\n",
		"usage.go": "package social\n\nimport golem \"github.com/eleven-am/golem/go/golem\"\n\n" +
			"var _ = Posts.UpdateMany(Posts.System().TagCount.Increment(1))\n" +
			"var _ = Posts.Create(Posts.System().CreatedBy.Create(golem.NewUUID([16]byte{1})))\n" +
			"var _ golem.Predicate[Post] = Posts.TagCount.Eq(1)\n",
	}, result.Files)
}
