package analytics

import (
	"reflect"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCompilerDefaultsLimitsAndDeclinesUnknownRoot(t *testing.T) {
	compiler, err := New(compilerir.CompilationIR{Model: compilerir.ModelIR{Providers: []compilerir.Provider{compilerir.SQLite}}}, Limits{})
	if err != nil {
		t.Fatalf("construct empty analytics compiler: %v", err)
	}
	if compiler.limits.Bind.MaxInputDepth != 32 || compiler.limits.Bind.MaxInputNodes != 16_384 || compiler.limits.Bind.MaxListItems != 4_096 {
		t.Fatalf("bind defaults = %+v", compiler.limits.Bind)
	}
	if compiler.limits.MaxGroups != 10_000 || compiler.limits.ListItems != 4_096 {
		t.Fatalf("analytics defaults = %+v", compiler.limits)
	}
	root, matched, err := compiler.Compile(&ast.Field{Name: "unknownAnalyticsRoot"}, nil, nil)
	if err != nil {
		t.Fatalf("compile unknown root: %v", err)
	}
	if matched || !reflect.DeepEqual(root, Root{}) {
		t.Fatalf("unknown root matched: matched=%t root=%+v", matched, root)
	}
}
