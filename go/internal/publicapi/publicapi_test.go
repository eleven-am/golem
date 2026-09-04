package publicapi

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var publicPackages = []string{
	"embedding",
	"events",
	"events/cdctest",
	"events/nats",
	"events/transporttest",
	"golem",
	"golemtest",
	"graphql",
	"observe",
	"observe/otel",
	"observe/slog",
	"provider",
	"provider/postgresql",
	"provider/sqlite",
	"queryplan",
	"queue",
	"render",
	"runtime",
}

func TestPublicSurfaceMatchesItsRecord(t *testing.T) {
	root := moduleRoot(t)
	recorded, err := os.ReadFile(filepath.Join(root, "internal", "publicapi", "surface.txt"))
	if err != nil {
		t.Fatalf("the public surface record is unreadable: %v", err)
	}
	actual := strings.Join(exportedSurface(t, root), "\n") + "\n"
	if string(recorded) == actual {
		return
	}
	recordedLines := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	actualLines := strings.Split(strings.TrimSuffix(actual, "\n"), "\n")
	added, removed := difference(actualLines, recordedLines), difference(recordedLines, actualLines)
	if err := os.WriteFile(filepath.Join(root, "internal", "publicapi", "surface.actual.txt"), []byte(actual), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("the public surface changed.\nremoved (breaking):\n  %s\nadded:\n  %s\n\nIf this is intended, replace surface.txt with surface.actual.txt and say why in the commit.",
		strings.Join(limit(removed), "\n  "), strings.Join(limit(added), "\n  "))
}

func exportedSurface(t *testing.T, root string) []string {
	t.Helper()
	var surface []string
	for _, packagePath := range publicPackages {
		directory := filepath.Join(root, filepath.FromSlash(packagePath))
		set := token.NewFileSet()
		packages, err := parser.ParseDir(set, directory, func(entry os.FileInfo) bool {
			return !strings.HasSuffix(entry.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", packagePath, err)
		}
		for _, parsed := range packages {
			for _, file := range parsed.Files {
				for _, declaration := range file.Decls {
					surface = append(surface, declarationNames(packagePath, declaration)...)
				}
			}
		}
	}
	sort.Strings(surface)
	return surface
}

func declarationNames(packagePath string, declaration ast.Decl) []string {
	var names []string
	switch typed := declaration.(type) {
	case *ast.FuncDecl:
		if !typed.Name.IsExported() {
			return nil
		}
		receiver := ""
		if typed.Recv != nil && len(typed.Recv.List) > 0 {
			receiver = "(" + strings.TrimPrefix(exprString(typed.Recv.List[0].Type), "*") + ")."
			if !exportedReceiver(typed.Recv.List[0].Type) {
				return nil
			}
		}
		names = append(names, packagePath+"."+receiver+typed.Name.Name+signature(typed.Type))
	case *ast.GenDecl:
		for _, spec := range typed.Specs {
			switch value := spec.(type) {
			case *ast.TypeSpec:
				if value.Name.IsExported() {
					names = append(names, packagePath+"."+value.Name.Name+" "+underlying(value))
					names = append(names, structFieldNames(packagePath, value)...)
				}
			case *ast.ValueSpec:
				for _, name := range value.Names {
					if name.IsExported() {
						names = append(names, packagePath+"."+name.Name)
					}
				}
			}
		}
	}
	return names
}

func structFieldNames(packagePath string, spec *ast.TypeSpec) []string {
	structure, ok := spec.Type.(*ast.StructType)
	if !ok || structure.Fields == nil {
		return nil
	}
	var names []string
	for _, field := range structure.Fields.List {
		for _, name := range field.Names {
			if name.IsExported() {
				names = append(names, packagePath+"."+spec.Name.Name+"."+name.Name+" "+render(field.Type))
			}
		}
	}
	return names
}

func signature(function *ast.FuncType) string {
	rendered := render(function)
	return strings.TrimPrefix(rendered, "func")
}

func underlying(spec *ast.TypeSpec) string {
	switch spec.Type.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	}
	return render(spec.Type)
}

func render(node ast.Node) string {
	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, token.NewFileSet(), node); err != nil {
		return "?"
	}
	return strings.Join(strings.Fields(buffer.String()), " ")
}

func exportedReceiver(expression ast.Expr) bool {
	name := strings.TrimPrefix(exprString(expression), "*")
	if index := strings.IndexByte(name, '['); index >= 0 {
		name = name[:index]
	}
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

func exprString(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + exprString(typed.X)
	case *ast.IndexExpr:
		return exprString(typed.X)
	case *ast.IndexListExpr:
		return exprString(typed.X)
	}
	return ""
}

func difference(from, against []string) []string {
	present := make(map[string]bool, len(against))
	for _, value := range against {
		present[value] = true
	}
	var result []string
	for _, value := range from {
		if !present[value] {
			result = append(result, value)
		}
	}
	return result
}

func limit(values []string) []string {
	if len(values) == 0 {
		return []string{"(none)"}
	}
	if len(values) > 20 {
		return append(values[:20], "... and more")
	}
	return values
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}
