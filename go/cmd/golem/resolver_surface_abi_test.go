package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/codegen/surface"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
)

func assertSurfaceDeclaresThePublishedABI(t *testing.T, application, pattern string) {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: application, Pattern: pattern})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics: %#v", compiled.Diagnostics)
	}
	var app modelcodegen.PackageSpec
	for _, spec := range compiled.Packages {
		if spec.ImportPath == compiled.Compilation.Model.Schema.PackagePath {
			app = spec
		}
	}
	emitted, err := surface.Emit(surface.Request{Compilation: *compiled.Compilation, Packages: compiled.Packages, AppPackage: app})
	if err != nil {
		t.Fatal(err)
	}
	shells, err := bindings.EmitShells(bindings.Request{Compilation: *compiled.Compilation, Packages: compiled.Packages})
	if err != nil {
		t.Fatal(err)
	}
	surfaceFiles := map[string][][]byte{}
	for _, file := range emitted.Files {
		surfaceFiles[filepath.Dir(file.Path)] = append(surfaceFiles[filepath.Dir(file.Path)], file.Source)
	}
	for _, shell := range shells {
		surfaceFiles[filepath.Dir(shell.Path)] = append(surfaceFiles[filepath.Dir(shell.Path)], shell.Source)
	}
	for _, spec := range compiled.Packages {
		matches, err := filepath.Glob(filepath.Join(spec.Directory, "zz_golem_*.gen.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			continue
		}
		var published [][]byte
		for _, match := range matches {
			source, err := os.ReadFile(match)
			if err != nil {
				t.Fatal(err)
			}
			published = append(published, source)
		}
		want := generatedABI(t, published, true)
		delete(want, "GolemGeneratedBindings")
		got := generatedABI(t, surfaceFiles[spec.Directory], false)
		var missing, changed, extra []string
		for key, declaration := range want {
			value, present := got[key]
			switch {
			case !present:
				missing = append(missing, key)
			case value != declaration:
				changed = append(changed, key+"\n  published: "+declaration+"\n  surface:   "+value)
			}
		}
		for key := range got {
			name := key[strings.LastIndexAny(key, " .")+1:]
			if _, present := want[key]; !present && ast.IsExported(name) {
				extra = append(extra, key)
			}
		}
		sort.Strings(missing)
		sort.Strings(changed)
		sort.Strings(extra)
		if len(missing)+len(changed)+len(extra) != 0 {
			t.Fatalf("surface of %s differs from the published ABI\nmissing: %v\nchanged: %v\nextra: %v", spec.ImportPath, missing, changed, extra)
		}
		if len(want) < 40 {
			t.Fatalf("published ABI of %s has only %d declarations", spec.ImportPath, len(want))
		}
	}
}

func generatedABI(t *testing.T, sources [][]byte, published bool) map[string]string {
	t.Helper()
	result := map[string]string{}
	fset := token.NewFileSet()
	print := func(node any) string {
		var buffer bytes.Buffer
		if err := printer.Fprint(&buffer, fset, node); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(buffer.String()), " ")
	}
	for _, source := range sources {
		file, err := parser.ParseFile(fset, "generated.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		adapter := bytes.Contains(source, []byte("golemGeneratedGraphQLABI"))
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(value.Name.Name) {
					continue
				}
				key := value.Name.Name
				if value.Recv != nil {
					receiver := receiverName(value.Recv.List[0].Type)
					if published && adapter && !ast.IsExported(receiver) {
						continue
					}
					key = receiver + "." + key
				}
				result[key] = print(&ast.FuncDecl{Recv: value.Recv, Name: value.Name, Type: value.Type})
			case *ast.GenDecl:
				for _, spec := range value.Specs {
					switch item := spec.(type) {
					case *ast.TypeSpec:
						if published && adapter && !ast.IsExported(item.Name.Name) {
							continue
						}
						result["type "+item.Name.Name] = print(item)
					case *ast.ValueSpec:
						for index, name := range item.Names {
							if !ast.IsExported(name.Name) {
								continue
							}
							shape := ""
							if item.Type != nil {
								shape = print(item.Type)
							} else if index < len(item.Values) {
								if literal, ok := item.Values[index].(*ast.CompositeLit); ok {
									shape = print(literal.Type)
								}
							}
							result[value.Tok.String()+" "+name.Name] = shape
						}
					}
				}
			}
		}
	}
	return result
}

func receiverName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.IndexListExpr:
		return receiverName(value.X)
	case *ast.Ident:
		return value.Name
	}
	return ""
}
