package gentest

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// DiagnosticKey is a transport-neutral stable ordering key for compiler and
// generator diagnostics.
type DiagnosticKey struct {
	PackagePath string
	File        string
	Offset      int
	Line        int
	Column      int
	Subject     string
	Code        string
}

// ModuleRelativePath converts filename to a slash-separated path below
// moduleRoot. Files outside the module are refused so temporary and host paths
// cannot enter goldens.
func ModuleRelativePath(moduleRoot, filename string) (string, error) {
	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		return "", fmt.Errorf("resolve module root: %w", err)
	}
	file, err := filepath.Abs(filename)
	if err != nil {
		return "", fmt.Errorf("resolve diagnostic filename: %w", err)
	}
	relative, err := filepath.Rel(root, file)
	if err != nil {
		return "", fmt.Errorf("make diagnostic path module-relative: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("diagnostic file %q is outside module root", filename)
	}
	return filepath.ToSlash(filepath.Clean(relative)), nil
}

// SortDiagnostics returns a copy in canonical diagnostic order.
func SortDiagnostics[T any](values []T, key func(T) DiagnosticKey) []T {
	result := append([]T(nil), values...)
	slices.SortStableFunc(result, func(left, right T) int {
		return compareDiagnosticKey(key(left), key(right))
	})
	return result
}

func compareDiagnosticKey(left, right DiagnosticKey) int {
	for _, pair := range [][2]string{
		{left.PackagePath, right.PackagePath},
		{left.File, right.File},
	} {
		if compared := strings.Compare(pair[0], pair[1]); compared != 0 {
			return compared
		}
	}
	for _, pair := range [][2]int{
		{left.Offset, right.Offset},
		{left.Line, right.Line},
		{left.Column, right.Column},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if compared := strings.Compare(left.Subject, right.Subject); compared != 0 {
		return compared
	}
	return strings.Compare(left.Code, right.Code)
}
