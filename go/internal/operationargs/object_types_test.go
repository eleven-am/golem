package operationargs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestRegistryAcceptsCoercedJSONInputsAndKeepsOutputWitness(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../compiler/compile/testdata/objecttypes", Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", compiled.Diagnostics)
	}
	registry, err := New(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"exact": json.Number("9007199254740993")}
	if _, err := registry.Prepare(compilerir.CustomOperationQuery, "inputState", map[string]any{"patch": map[string]any{"document": document}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.validateValue(compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "JSON"}, document, false); err == nil {
		t.Fatal("raw JSON output bypassed its native document witness")
	}
}

func TestRegisteredObjectsValidateNestedInputsResultsAndFieldBounds(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../compiler/compile/testdata/objecttypes", Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("compile diagnostics: %#v", compiled.Diagnostics)
	}
	registry, err := New(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	request, err := registry.Prepare(compilerir.CustomOperationQuery, "homeFeed", map[string]any{"input": map[string]any{"range": map[string]any{"seed": int32(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	values := request.Arguments()[0].Value().(map[string]any)
	values["range"].(map[string]any)["seed"] = int32(99)
	if got := request.Arguments()[0].Value().(map[string]any)["range"].(map[string]any)["seed"]; got != int32(7) {
		t.Fatalf("argument alias: %v", got)
	}
	page := func(results any) map[string]any {
		return map[string]any{"page": int32(1), "totalPages": int32(1), "totalResults": int32(1), "exhausted": true, "results": results}
	}
	valid := page([]any{map[string]any{"kind": "BASIC_PICK", "index": int32(0)}})
	result, err := registry.Finish(request, valid)
	if err != nil {
		t.Fatal(err)
	}
	valid["results"].([]any)[0].(map[string]any)["kind"] = "changed"
	if result.Value().(map[string]any)["results"].([]any)[0].(map[string]any)["kind"] != "BASIC_PICK" {
		t.Fatal("result aliases resolver data")
	}
	unknown := page([]any{})
	unknown["undeclared"] = true
	for _, invalid := range []any{
		map[string]any{}, page(nil), page([]any{map[string]any{"kind": "BASIC_PICK", "index": int64(0)}}),
		page([]any{map[string]any{"kind": "not-an-enum", "index": int32(0)}}), unknown,
	} {
		if _, err := registry.Finish(request, invalid); err == nil {
			t.Fatalf("accepted result %#v", invalid)
		}
	}
	for _, invalid := range []map[string]any{
		{"input": map[string]any{"range": map[string]any{}}}, {"input": map[string]any{"unknown": true}},
	} {
		if _, err := registry.Prepare(compilerir.CustomOperationQuery, "homeFeed", invalid); err == nil {
			t.Fatalf("accepted input %#v", invalid)
		}
	}
	// The count must be rejected before attempting to inspect malformed items.
	_, err = registry.Finish(request, map[string]any{"results": make([]any, 26)})
	var limit *golem.Error
	if !errors.As(err, &limit) || limit.Code != golem.CodeLimitExceeded || limit.LimitMax != 25 {
		t.Fatalf("nested list limit: %v", err)
	}
}
