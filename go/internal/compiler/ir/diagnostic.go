package ir

import (
	"path"
	"sort"
	"strings"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type DiagnosticLabel struct {
	Message  string     `json:"message"`
	Span     SourceSpan `json:"span"`
	StableID string     `json:"stableId,omitempty"`
}

type Diagnostic struct {
	Code     string            `json:"code"`
	Severity Severity          `json:"severity"`
	Message  string            `json:"message"`
	Primary  SourceSpan        `json:"primary"`
	Related  []DiagnosticLabel `json:"related"`
	Hint     string            `json:"hint,omitempty"`
}

func NewError(code, message string, span SourceSpan) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityError, Message: message, Primary: span}
}

// NormalizeSourceSpan removes host-specific separators and cleans only the
// module-relative path. Callers must reject absolute source paths separately.
func NormalizeSourceSpan(span SourceSpan) SourceSpan {
	// Source spans can have been serialized on a host whose separator differs
	// from the current compiler host. Normalize both forms before path.Clean;
	// filepath.Clean alone treats a backslash as an ordinary byte on Unix.
	span.RelativeFile = strings.ReplaceAll(span.RelativeFile, `\`, "/")
	span.RelativeFile = path.Clean(span.RelativeFile)
	if span.RelativeFile == "." {
		span.RelativeFile = ""
	}
	span.RelativeFile = strings.TrimPrefix(span.RelativeFile, "./")
	return span
}

func SortDiagnostics(diagnostics []Diagnostic) {
	for i := range diagnostics {
		diagnostics[i].Primary = NormalizeSourceSpan(diagnostics[i].Primary)
		for relatedIndex := range diagnostics[i].Related {
			diagnostics[i].Related[relatedIndex].Span = NormalizeSourceSpan(diagnostics[i].Related[relatedIndex].Span)
		}
		sort.SliceStable(diagnostics[i].Related, func(a, b int) bool {
			left, right := diagnostics[i].Related[a], diagnostics[i].Related[b]
			if left.Span.RelativeFile != right.Span.RelativeFile {
				return left.Span.RelativeFile < right.Span.RelativeFile
			}
			if left.Span.StartLine != right.Span.StartLine {
				return left.Span.StartLine < right.Span.StartLine
			}
			if left.Span.StartColumn != right.Span.StartColumn {
				return left.Span.StartColumn < right.Span.StartColumn
			}
			return left.StableID < right.StableID
		})
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i], diagnostics[j]
		if left.Primary.RelativeFile != right.Primary.RelativeFile {
			return left.Primary.RelativeFile < right.Primary.RelativeFile
		}
		if left.Primary.StartLine != right.Primary.StartLine {
			return left.Primary.StartLine < right.Primary.StartLine
		}
		if left.Primary.StartColumn != right.Primary.StartColumn {
			return left.Primary.StartColumn < right.Primary.StartColumn
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return firstRelatedID(left) < firstRelatedID(right)
	})
}

func firstRelatedID(diagnostic Diagnostic) string {
	if len(diagnostic.Related) == 0 {
		return ""
	}
	return diagnostic.Related[0].StableID
}
