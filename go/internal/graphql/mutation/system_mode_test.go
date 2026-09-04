package mutation

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func systemModeBinder(t *testing.T, field compilerir.FieldID, modes ...compilerir.FieldMode) (*MapBinder, mutationIDs) {
	t.Helper()
	compilation, ids := mutationCompilation()
	applied := false
	for modelIndex := range compilation.Contract.Models {
		for fieldIndex := range compilation.Contract.Models[modelIndex].Fields {
			if compilation.Contract.Models[modelIndex].Fields[fieldIndex].FieldID != field {
				continue
			}
			compilation.Contract.Models[modelIndex].Fields[fieldIndex].Modes = modes
			applied = true
		}
	}
	if !applied {
		t.Fatalf("field %s was not found in the fixture contract", field)
	}
	binder, err := NewMapBinder(compilation, Limits{MaxInputDepth: 2, MaxInputNodes: 8, MaxListItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	return binder, ids
}

func TestGraphQLInputRefusesSystemModeScalarOnCreateAndUpdate(t *testing.T) {
	binder, ids := systemModeBinder(t, compilerir.FieldID(hexID(6)), compilerir.ModeSystem)
	create := map[string]any{"data": map[string]any{"name": "x"}}
	if _, err := binder.LowerValues(Create, ids.user, create, nil); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("create accepted a system field: %v", err)
	}
	update := map[string]any{"data": map[string]any{"name": map[string]any{"set": "x"}}, "where": map[string]any{"id": int32(1)}}
	if _, err := binder.LowerValues(Update, ids.user, update, nil); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("update accepted a system field: %v", err)
	}
}

func TestGraphQLInputRefusesSystemImmutableScalarOnCreate(t *testing.T) {
	binder, ids := systemModeBinder(t, compilerir.FieldID(hexID(6)), compilerir.ModeSystem, compilerir.ModeImmutable)
	create := map[string]any{"data": map[string]any{"name": "x"}}
	if _, err := binder.LowerValues(Create, ids.user, create, nil); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("create accepted a system immutable field: %v", err)
	}
}
