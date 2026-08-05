package ir

import "testing"

func TestDiagnosticsSortByModuleRelativeLocationAndCode(t *testing.T) {
	diagnostics := []Diagnostic{
		{
			Code:     "P1_Z",
			Severity: SeverityError,
			Message:  "later",
			Primary:  SourceSpan{RelativeFile: `model\z.go`, StartLine: 2},
			Related: []DiagnosticLabel{
				{StableID: "b", Span: SourceSpan{RelativeFile: `model\related.go`, StartLine: 2}},
				{StableID: "a", Span: SourceSpan{RelativeFile: `model\related.go`, StartLine: 1}},
			},
		},
		NewError("P1_B", "second", SourceSpan{RelativeFile: "model/a.go", StartLine: 1}),
		NewError("P1_A", "first", SourceSpan{RelativeFile: "./model/a.go", StartLine: 1}),
	}
	SortDiagnostics(diagnostics)
	if diagnostics[0].Code != "P1_A" || diagnostics[1].Code != "P1_B" || diagnostics[2].Code != "P1_Z" {
		t.Fatalf("unexpected diagnostic order: %#v", diagnostics)
	}
	if diagnostics[2].Primary.RelativeFile != "model/z.go" {
		t.Fatalf("path was not normalized: %q", diagnostics[2].Primary.RelativeFile)
	}
	if got := diagnostics[2].Related[0].Span.RelativeFile; got != "model/related.go" {
		t.Fatalf("related path was not normalized: %q", got)
	}
	if got := diagnostics[2].Related[0].StableID; got != "a" {
		t.Fatalf("related labels were not sorted after normalization: %q", got)
	}
}
