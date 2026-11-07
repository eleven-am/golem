package bind

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestInternalFieldIsAbsentFromSchemaAndRefusedByEveryReadSurface(t *testing.T) {
	compilation := fieldModesCompilation(t)
	account, contract := modelNamed(t, compilation, "Account")
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	inspected := 0
	for name, definition := range parsed.Types {
		if !strings.HasPrefix(name, "Account") {
			continue
		}
		inspected++
		if definition.Fields.ForName("token") != nil {
			t.Errorf("%s exposes token", name)
		}
		for _, value := range definition.EnumValues {
			if strings.EqualFold(value.Name, "token") {
				t.Errorf("%s enum exposes token", name)
			}
		}
	}
	if inspected < 5 {
		t.Fatalf("inspected only %d Account types", inspected)
	}

	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	selection := []readir.Selection{scalarSelection(t, contract.Fields[0].FieldID)}
	primary := ""
	for _, selector := range contract.Selectors {
		if !compilerir.SelectorHasInternalField(contract, selector) {
			primary = selector.Name
		}
	}
	if primary == "" {
		t.Fatal("no non-internal selector")
	}
	cases := map[string]struct {
		operation readir.Operation
		arguments map[string]any
		ok        map[string]any
	}{
		"where":       {readir.FindMany, map[string]any{"where": map[string]any{"token": map[string]any{"equals": "x"}}}, map[string]any{"where": map[string]any{"handle": map[string]any{"equals": "x"}}}},
		"orderBy":     {readir.FindMany, map[string]any{"orderBy": []any{map[string]any{"token": "asc"}}}, map[string]any{"orderBy": []any{map[string]any{"handle": "asc"}}}},
		"distinct":    {readir.FindMany, map[string]any{"distinct": []any{"token"}}, map[string]any{"distinct": []any{"handle"}}},
		"whereUnique": {readir.FindUnique, map[string]any{"where": map[string]any{"token": "x"}}, map[string]any{"where": map[string]any{primary: "00000000-0000-0000-0000-000000000001"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, bindErr := binder.Query(QueryInput{Operation: tc.operation, Model: account.ID, Arguments: tc.arguments, Selections: selection})
			if !isKind(bindErr, compileerr.KindFieldUnavailable) {
				t.Fatalf("internal field accepted: %v", bindErr)
			}
			if _, bindErr = binder.Query(QueryInput{Operation: tc.operation, Model: account.ID, Arguments: tc.ok, Selections: selection}); bindErr != nil {
				t.Fatalf("control rejected: %v", bindErr)
			}
		})
	}
	if _, selectErr := selectset.Compile(selectset.Request{Compilation: compilation, Model: account.ID, Selections: rawSelections(t, "token")}); !isKind(selectErr, compileerr.KindFieldUnavailable) {
		t.Fatalf("selection accepted internal field: %v", selectErr)
	}
}
