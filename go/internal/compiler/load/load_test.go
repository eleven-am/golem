package load

import (
	"context"
	"strings"
	"testing"
)

func TestPackageSyntaxDeterministicAndSkipsGenerated(t *testing.T) {
	pkg, errs := PackageSyntax(context.Background(), Config{Dir: "testdata/basic", Pattern: "."})
	if len(errs) != 0 {
		t.Fatalf("PackageSyntax errors: %v", errs)
	}
	if pkg.ImportPath != "github.com/eleven-am/golem/go/internal/compiler/load/testdata/basic" {
		t.Fatalf("ImportPath = %q", pkg.ImportPath)
	}
	if len(pkg.Files) != 2 || !strings.HasSuffix(pkg.Files[0].RelativePath, "a.go") || !strings.HasSuffix(pkg.Files[1].RelativePath, "z.go") {
		t.Fatalf("files are not sorted or generated file was included: %#v", pkg.Files)
	}
	file, line, column := pkg.Position(pkg.Files[0].AST.Package)
	if !strings.HasSuffix(file, "internal/compiler/load/testdata/basic/a.go") || line != 1 || column != 1 {
		t.Fatalf("position = %q:%d:%d", file, line, column)
	}
}

func TestPackageSyntaxRequiresOnePackage(t *testing.T) {
	_, errs := PackageSyntax(context.Background(), Config{Dir: "testdata", Pattern: "./..."})
	if len(errs) != 1 || errs[0].Code != "P1_LOAD_PACKAGE_COUNT" {
		t.Fatalf("errors = %#v", errs)
	}
}
