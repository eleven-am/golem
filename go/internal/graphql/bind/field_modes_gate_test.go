package bind

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func fieldModesCompilation(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../testdata/fieldmodes", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("field-mode fixture diagnostics = %#v", compiled.Diagnostics)
	}
	return *compiled.Compilation
}

func TestWriteOnlyAndHiddenFieldsAreUnreadableThroughEverySurfaceThatAsksIrModesReadable(t *testing.T) {
	compilation := fieldModesCompilation(t)
	account, contract := modelNamed(t, compilation, "Account")

	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("sdl", func(t *testing.T) {
		object := strings.SplitN(strings.SplitN(document.SDL, "type Account {", 2)[1], "}", 2)[0]
		for _, unreadable := range []string{"secret", "email"} {
			if strings.Contains(object, unreadable+":") {
				t.Errorf("SDL type Account exposes unreadable field %q: %s", unreadable, object)
			}
		}
		if !strings.Contains(object, "handle:") || !strings.Contains(object, "sequence:") {
			t.Errorf("SDL type Account lost its readable fields: %s", object)
		}
	})

	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("binder", func(t *testing.T) {
		for _, unreadable := range []string{"secret", "email"} {
			_, bindErr := binder.Query(QueryInput{
				Operation:  readir.FindMany,
				Model:      account.ID,
				Arguments:  map[string]any{"where": map[string]any{unreadable: map[string]any{"equals": "x"}}},
				Selections: []readir.Selection{scalarSelection(t, contract.Fields[0].FieldID)},
			})
			if bindErr == nil || !strings.Contains(bindErr.Error(), "P5_BIND_FIELD") {
				t.Errorf("binder accepted a where predicate on unreadable field %q: %v", unreadable, bindErr)
			}
		}
		if _, bindErr := binder.Query(QueryInput{
			Operation:  readir.FindMany,
			Model:      account.ID,
			Arguments:  map[string]any{"where": map[string]any{"handle": map[string]any{"equals": "x"}}},
			Selections: []readir.Selection{scalarSelection(t, contract.Fields[0].FieldID)},
		}); bindErr != nil {
			t.Errorf("binder rejected a readable field: %v", bindErr)
		}
	})

	if _, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL}); err != nil {
		t.Fatal(err)
	}
	t.Run("selection", func(t *testing.T) {
		for _, unreadable := range []string{"secret", "email"} {
			selections := rawSelections(t, unreadable)
			if _, selectErr := selectset.Compile(selectset.Request{Compilation: compilation, Model: account.ID, Selections: selections}); selectErr == nil || !strings.Contains(selectErr.Error(), "P5_SELECT_FIELD") {
				t.Errorf("selection compiler accepted unreadable field %q: %v", unreadable, selectErr)
			}
		}
		if _, selectErr := selectset.Compile(selectset.Request{Compilation: compilation, Model: account.ID, Selections: rawSelections(t, "handle")}); selectErr != nil {
			t.Errorf("selection compiler rejected a readable field: %v", selectErr)
		}
	})
}

func rawSelections(t *testing.T, field string) ast.SelectionSet {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "gate.graphql", Input: "query { account { " + field + " } }"})
	if err != nil {
		t.Fatal(err)
	}
	return document.Operations[0].SelectionSet[0].(*ast.Field).SelectionSet
}
