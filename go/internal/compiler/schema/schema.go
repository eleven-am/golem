// Package schema extracts source-located unresolved declarations from the
// accepted closed DefineSchema function and registered model source.
package schema

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/load"
)

// Config selects one schema root.
type Config struct {
	Dir     string
	Pattern string
	Root    string
}

type Severity string

const SeverityError Severity = "error"

// Diagnostic is deterministic and contains module-relative source evidence.
type Diagnostic struct {
	Code     string
	Severity Severity
	Message  string
	Span     ir.SourceSpan
}

// Result contains Raw only when no error diagnostics were produced.
type Result struct {
	Raw         ir.RawDeclIR
	Packages    []PackageMetadata
	Diagnostics []Diagnostic
}

type PackageMetadata struct {
	ImportPath string
	Name       string
	Directory  string
	ModulePath string
	ModuleDir  string
}

// Extract performs the syntax/declaration pass. It never type-checks or runs
// application code.
func Extract(ctx context.Context, config Config) Result {
	rootName := config.Root
	if rootName == "" {
		rootName = "DefineSchema"
	}
	pkg, loadErrs := load.PackageSyntax(ctx, load.Config{Dir: config.Dir, Pattern: config.Pattern})
	if len(loadErrs) != 0 {
		result := Result{}
		for _, err := range loadErrs {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Code: err.Code, Severity: SeverityError, Message: err.Text,
				Span: ir.SourceSpan{RelativeFile: err.RelativeFile, StartLine: uint32(err.Line), StartColumn: uint32(err.Column)},
			})
		}
		sortDiagnostics(result.Diagnostics)
		return result
	}

	extractor := compiler{
		ctx:      ctx,
		config:   config,
		rootName: rootName,
		packages: map[string]*load.Package{pkg.ImportPath: pkg},
	}
	root := extractor.extractRoot(pkg)
	if root != nil {
		extractor.extractRegisteredModels(*root)
	}
	sortDiagnostics(extractor.diagnostics)
	if len(extractor.diagnostics) != 0 {
		return Result{Diagnostics: extractor.diagnostics}
	}
	packages := make([]PackageMetadata, 0, len(extractor.packages))
	for _, pkg := range extractor.packages {
		packages = append(packages, PackageMetadata{ImportPath: pkg.ImportPath, Name: pkg.Name, Directory: pkg.Dir, ModulePath: pkg.ModulePath, ModuleDir: pkg.ModuleDir})
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
	return Result{Packages: packages, Raw: ir.RawDeclIR{
		FormatVersion: ir.RawDeclFormatVersion,
		Root:          *root,
		Models:        extractor.models,
		Enums:         extractor.enums,
		Methods:       extractor.methods,
	}}
}

type compiler struct {
	ctx         context.Context
	config      Config
	rootName    string
	packages    map[string]*load.Package
	diagnostics []Diagnostic
	models      []ir.RawModelDecl
	enums       []ir.RawEnumDecl
	methods     []ir.RawMethodDecl
}

func (c *compiler) error(pkg *load.Package, code, message string, node interface {
	Pos() token.Pos
	End() token.Pos
}) {
	c.diagnostics = append(c.diagnostics, Diagnostic{Code: code, Severity: SeverityError, Message: message, Span: sourceSpan(pkg, node.Pos(), node.End())})
}

func sourceSpan(pkg *load.Package, start, end token.Pos) ir.SourceSpan {
	startFile, startLine, startColumn := pkg.Position(start)
	endFile, endLine, endColumn := pkg.Position(end)
	if endFile != startFile {
		endLine, endColumn = startLine, startColumn
	}
	return ir.SourceSpan{
		ModulePath: pkg.ModulePath, RelativeFile: startFile,
		StartLine: uint32(startLine), StartColumn: uint32(startColumn),
		EndLine: uint32(endLine), EndColumn: uint32(endColumn),
	}
}

func nodeText(pkg *load.Package, node any) string {
	var out bytes.Buffer
	if err := format.Node(&out, pkg.FileSet, node); err != nil {
		return ""
	}
	return out.String()
}

func sortDiagnostics(items []Diagnostic) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.Span.RelativeFile != right.Span.RelativeFile {
			return left.Span.RelativeFile < right.Span.RelativeFile
		}
		if left.Span.StartLine != right.Span.StartLine {
			return left.Span.StartLine < right.Span.StartLine
		}
		if left.Span.StartColumn != right.Span.StartColumn {
			return left.Span.StartColumn < right.Span.StartColumn
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Message < right.Message
	})
}

func (c *compiler) loadPackage(importPath string) *load.Package {
	if pkg := c.packages[importPath]; pkg != nil {
		return pkg
	}
	pkg, errs := load.PackageSyntax(c.ctx, load.Config{Dir: c.config.Dir, Pattern: importPath})
	for _, err := range errs {
		c.diagnostics = append(c.diagnostics, Diagnostic{
			Code: err.Code, Severity: SeverityError,
			Message: fmt.Sprintf("load registered package %q: %s", importPath, err.Text),
			Span:    ir.SourceSpan{RelativeFile: err.RelativeFile, StartLine: uint32(err.Line), StartColumn: uint32(err.Column)},
		})
	}
	if pkg != nil {
		c.packages[importPath] = pkg
	}
	return pkg
}

func importAliases(file *ast.File) map[string]string {
	aliases := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		path := unquoteString(spec.Path.Value)
		if path == "" || (spec.Name != nil && (spec.Name.Name == "." || spec.Name.Name == "_")) {
			continue
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			for i := len(path) - 1; i >= 0; i-- {
				if path[i] == '/' {
					name = path[i+1:]
					break
				}
			}
			if name == "" {
				name = path
			}
		}
		aliases[name] = path
	}
	return aliases
}
