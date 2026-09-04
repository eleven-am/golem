package resolve_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/resolve"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
)

func systemModeRaw(t *testing.T, target string, attrs ...string) resolve.Result {
	t.Helper()
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	raw := rawResult.Raw
	applied := false
	for modelIndex := range raw.Models {
		if raw.Models[modelIndex].GoName != "User" {
			continue
		}
		for fieldIndex := range raw.Models[modelIndex].Fields {
			field := &raw.Models[modelIndex].Fields[fieldIndex]
			if field.GoName != target {
				continue
			}
			for _, name := range attrs {
				field.GolemAttrs = append(field.GolemAttrs, flag(name, field.Span))
			}
			applied = true
		}
	}
	if !applied {
		t.Fatalf("field %s was not found in the social fixture", target)
	}
	return resolve.Base(raw)
}

func systemModeContract(t *testing.T, result resolve.Result, target string) []ir.FieldMode {
	t.Helper()
	if len(result.Diagnostics) != 0 {
		t.Fatalf("resolve diagnostics: %#v", result.Diagnostics)
	}
	for _, model := range result.Compilation.Model.Models {
		if model.Go.Name != "User" {
			continue
		}
		for _, field := range model.Fields {
			if field.GoName != target {
				continue
			}
			for _, contract := range result.Compilation.Contract.Models {
				if contract.ModelID != model.ID {
					continue
				}
				for _, entry := range contract.Fields {
					if entry.FieldID == field.ID {
						return entry.Modes
					}
				}
			}
		}
	}
	t.Fatalf("no contract modes for field %s", target)
	return nil
}

func TestSystemModeResolvesAlone(t *testing.T) {
	modes := systemModeContract(t, systemModeRaw(t, "Nickname", "system"), "Nickname")
	if !reflect.DeepEqual(modes, []ir.FieldMode{ir.ModeSystem}) {
		t.Fatalf("system modes = %#v", modes)
	}
}

func TestSystemModeComposesWithImmutable(t *testing.T) {
	modes := systemModeContract(t, systemModeRaw(t, "Nickname", "system", "immutable"), "Nickname")
	if !reflect.DeepEqual(modes, []ir.FieldMode{ir.ModeSystem, ir.ModeImmutable}) {
		t.Fatalf("system immutable modes = %#v", modes)
	}
}

func TestSystemModeRefusesReadOnlyNamingBothModes(t *testing.T) {
	result := systemModeRaw(t, "Nickname", "system", "readonly")
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "P1_EXPOSURE_SYSTEM_READONLY" {
			continue
		}
		found = true
		if !strings.Contains(diagnostic.Message, "system") || !strings.Contains(diagnostic.Message, "readonly") {
			t.Fatalf("diagnostic message does not name both modes: %q", diagnostic.Message)
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestSystemTagIsAcceptedByTheStructTagParser(t *testing.T) {
	result := schema.Extract(context.Background(), schema.Config{Dir: "testdata/systemowned", Pattern: "."})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", result.Diagnostics)
	}
	found := false
	for _, model := range result.Raw.Models {
		for _, field := range model.Fields {
			if field.GoName != "TagCount" {
				continue
			}
			for _, attr := range field.GolemAttrs {
				if attr.Name == "system" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("system attribute did not survive struct tag extraction")
	}
}

func TestSystemReadOnlyStructTagFailsToResolveNamingBothModes(t *testing.T) {
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/systemreadonly", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	result := resolve.Base(rawResult.Raw)
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "P1_EXPOSURE_SYSTEM_READONLY" {
			continue
		}
		if !strings.Contains(diagnostic.Message, "system") || !strings.Contains(diagnostic.Message, "readonly") {
			t.Fatalf("diagnostic message does not name both modes: %q", diagnostic.Message)
		}
		return
	}
	t.Fatalf("diagnostics = %#v", result.Diagnostics)
}
