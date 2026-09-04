package compile_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestSystemStructTagReachesTheCompiledContract(t *testing.T) {
	result := compile.Compile(context.Background(), compile.Config{Dir: "../resolve/testdata/systemowned", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	modes := map[string][]ir.FieldMode{}
	for _, model := range result.Compilation.Contract.Models {
		for _, field := range model.Fields {
			modes[field.GraphQLName] = field.Modes
		}
	}
	if !reflect.DeepEqual(modes["tagCount"], []ir.FieldMode{ir.ModeSystem}) {
		t.Fatalf("tagCount modes = %#v", modes["tagCount"])
	}
	if !reflect.DeepEqual(modes["createdBy"], []ir.FieldMode{ir.ModeImmutable, ir.ModeSystem}) {
		t.Fatalf("createdBy modes = %#v", modes["createdBy"])
	}
	if !reflect.DeepEqual(modes["title"], []ir.FieldMode{ir.ModeVisible}) {
		t.Fatalf("title modes = %#v", modes["title"])
	}
}
