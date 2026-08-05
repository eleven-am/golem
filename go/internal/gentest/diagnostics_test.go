package gentest

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestModuleRelativePathIsStableAndRefusesOutsideFiles(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "internal", "social", "models.go")
	got, err := ModuleRelativePath(root, inside)
	if err != nil {
		t.Fatal(err)
	}
	if got != "internal/social/models.go" {
		t.Fatalf("relative path = %q", got)
	}

	outside := filepath.Join(filepath.Dir(root), "elsewhere", "models.go")
	if _, err := ModuleRelativePath(root, outside); err == nil {
		t.Fatal("outside file was accepted")
	}
}

func TestSortDiagnosticsIsCanonicalAndDoesNotMutateInput(t *testing.T) {
	input := []DiagnosticKey{
		{PackagePath: "z", File: "b.go", Offset: 4, Code: "P1B"},
		{PackagePath: "a", File: "b.go", Offset: 8, Code: "P1C"},
		{PackagePath: "a", File: "a.go", Offset: 8, Code: "P1A"},
	}
	original := append([]DiagnosticKey(nil), input...)
	got := SortDiagnostics(input, func(value DiagnosticKey) DiagnosticKey { return value })
	want := []string{"P1A", "P1C", "P1B"}
	for index, code := range want {
		if got[index].Code != code {
			t.Fatalf("diagnostic %d code = %q, want %q", index, got[index].Code, code)
		}
	}
	if !slices.Equal(input, original) {
		t.Fatalf("input mutated: got %#v, want %#v", input, original)
	}
}
