package schema

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func systemModeCompilation(t *testing.T, modelName, graphqlField string, modes ...compilerir.FieldMode) compilerir.CompilationIR {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	applied := false
	for modelIndex := range compilation.Contract.Models {
		if compilation.Contract.Models[modelIndex].GraphQLName != modelName {
			continue
		}
		for fieldIndex := range compilation.Contract.Models[modelIndex].Fields {
			if compilation.Contract.Models[modelIndex].Fields[fieldIndex].GraphQLName != graphqlField {
				continue
			}
			compilation.Contract.Models[modelIndex].Fields[fieldIndex].Modes = modes
			applied = true
		}
	}
	if !applied {
		t.Fatalf("field %s.%s was not found in the social contract", modelName, graphqlField)
	}
	return compilation
}

func inputBlock(t *testing.T, sdl, name string) string {
	t.Helper()
	header := "input " + name + " {"
	start := strings.Index(sdl, header)
	if start < 0 {
		t.Fatalf("SDL has no %s", header)
	}
	rest := sdl[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("SDL block %s is unterminated", header)
	}
	return rest[:end]
}

func TestSystemModeFieldIsAbsentFromEveryGeneratedInputType(t *testing.T) {
	document, err := Build(systemModeCompilation(t, "Post", "body", compilerir.ModeSystem))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"PostCreateInput", "PostUpdateInput", "PostUpdateManyInput"} {
		block := inputBlock(t, document.SDL, input)
		if strings.Contains(block, "\n  body:") {
			t.Fatalf("system field survived in %s:\n%s", input, block)
		}
	}
}

func TestSystemModeFieldRemainsReadable(t *testing.T) {
	document, err := Build(systemModeCompilation(t, "Post", "body", compilerir.ModeSystem))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(document.SDL, "type Post {")
	if start < 0 {
		t.Fatal("SDL has no type Post")
	}
	block := document.SDL[start : start+strings.Index(document.SDL[start:], "\n}")]
	if !strings.Contains(block, "\n  body:") {
		t.Fatalf("system field lost its read exposure:\n%s", block)
	}
}

func TestSystemImmutableFieldIsAbsentFromEveryGeneratedInputType(t *testing.T) {
	document, err := Build(systemModeCompilation(t, "Post", "body", compilerir.ModeSystem, compilerir.ModeImmutable))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"PostCreateInput", "PostUpdateInput", "PostUpdateManyInput"} {
		block := inputBlock(t, document.SDL, input)
		if strings.Contains(block, "\n  body:") {
			t.Fatalf("system immutable field survived in %s:\n%s", input, block)
		}
	}
}
