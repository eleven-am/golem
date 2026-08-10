package methods

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"golang.org/x/tools/go/packages"
)

type loaded struct {
	packages map[string]*packages.Package
	overlay  modelcodegen.Result
}

func loadTyped(ctx context.Context, config Config) (loaded, []ir.Diagnostic) {
	packageSpecs, packageDiagnostics := discoverPackageSpecs(ctx, config)
	if hasErrors(packageDiagnostics) {
		return loaded{}, packageDiagnostics
	}
	config.Packages = packageSpecs
	bootstrap := config.Bootstrap
	if len(bootstrap.Files) == 0 {
		var err error
		bootstrap, err = modelcodegen.Emit(modelcodegen.Request{
			Compilation:     config.Compilation,
			Packages:        config.Packages,
			GolemImportPath: config.GolemImportPath,
		})
		if err != nil {
			return loaded{}, []ir.Diagnostic{ir.NewError("P1_METHOD_EMIT", err.Error(), ir.SourceSpan{})}
		}
	}

	specs := make(map[string]modelcodegen.PackageSpec, len(config.Packages))
	for _, spec := range config.Packages {
		specs[spec.ImportPath] = spec
	}
	overlay := make(map[string][]byte, len(bootstrap.Files))
	fresh := make(map[string]bool, len(bootstrap.Files))
	registeredDirs := make(map[string]string, len(config.Packages))
	for _, spec := range config.Packages {
		if spec.Directory != "" {
			absolute, _ := filepath.Abs(spec.Directory)
			registeredDirs[filepath.Clean(absolute)] = spec.PackageName
		}
	}
	for index := range bootstrap.Files {
		file := &bootstrap.Files[index]
		path := file.Path
		if path == "" {
			if spec := specs[file.ImportPath]; spec.Directory != "" {
				path = filepath.Join(spec.Directory, modelcodegen.BootstrapFilename)
			} else {
				return loaded{}, []ir.Diagnostic{ir.NewError("P1_METHOD_OVERLAY_PATH", fmt.Sprintf("generated package %q has no source directory", file.ImportPath), ir.SourceSpan{})}
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(config.Dir, path)
		}
		path, _ = filepath.Abs(path)
		file.Path = filepath.Clean(path)
		overlay[file.Path] = append([]byte(nil), file.Source...)
		fresh[file.Path] = true
	}

	patterns := make([]string, 0, len(config.Packages))
	seen := map[string]bool{}
	for _, spec := range config.Packages {
		if !seen[spec.ImportPath] {
			seen[spec.ImportPath] = true
			patterns = append(patterns, spec.ImportPath)
		}
	}
	sort.Strings(patterns)
	if len(patterns) == 0 {
		return loaded{}, []ir.Diagnostic{ir.NewError("P1_METHOD_PACKAGES", "no registered model packages were supplied", ir.SourceSpan{})}
	}

	parseFile := func(fset *token.FileSet, filename string, source []byte) (*ast.File, error) {
		absolute, _ := filepath.Abs(filename)
		absolute = filepath.Clean(absolute)
		packageName, registered := registeredDirs[filepath.Dir(absolute)]
		if registered && !fresh[absolute] && generatedSource(source) {
			return parser.ParseFile(fset, filename, "package "+packageName+"\n", parser.AllErrors)
		}
		return parser.ParseFile(fset, filename, source, parser.ParseComments|parser.AllErrors)
	}
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedImports | packages.NeedDeps | packages.NeedExportFile |
		packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo |
		packages.NeedTypesSizes | packages.NeedModule
	environment := config.Env
	if len(environment) != 0 {
		environment = append(os.Environ(), environment...)
	}
	loadedPackages, err := packages.Load(&packages.Config{
		Context: ctx, Dir: config.Dir, Env: environment,
		Mode: mode, Overlay: overlay, ParseFile: parseFile, Tests: false,
	}, patterns...)
	if err != nil {
		return loaded{}, []ir.Diagnostic{ir.NewError("P1_METHOD_LOAD", err.Error(), ir.SourceSpan{})}
	}
	byPath := make(map[string]*packages.Package, len(loadedPackages))
	var diagnostics []ir.Diagnostic
	for _, pkg := range loadedPackages {
		byPath[pkg.PkgPath] = pkg
		for _, packageError := range pkg.Errors {
			if strings.HasPrefix(packageError.Msg, "# ") && strings.Contains(packageError.Msg, "\n") {
				continue
			}
			diagnostics = append(diagnostics, ir.NewError("P1_METHOD_TYPECHECK", packageError.Msg, errorSpan(packageError.Pos, pkg, config)))
		}
	}
	ir.SortDiagnostics(diagnostics)
	return loaded{packages: byPath, overlay: bootstrap}, diagnostics
}

func discoverPackageSpecs(ctx context.Context, config Config) ([]modelcodegen.PackageSpec, []ir.Diagnostic) {
	byPath := make(map[string]modelcodegen.PackageSpec, len(config.Packages))
	for _, spec := range config.Packages {
		byPath[spec.ImportPath] = spec
	}
	var missing []string
	for _, model := range config.Compilation.Model.Models {
		spec, exists := byPath[model.Go.PackagePath]
		if !exists {
			spec = modelcodegen.PackageSpec{ImportPath: model.Go.PackagePath}
			byPath[model.Go.PackagePath] = spec
		}
		if spec.PackageName == "" || spec.Directory == "" {
			missing = append(missing, model.Go.PackagePath)
		}
	}
	sort.Strings(missing)
	missing = compactStrings(missing)
	if len(missing) != 0 {
		environment := config.Env
		if len(environment) != 0 {
			environment = append(os.Environ(), environment...)
		}
		listed, err := packages.Load(&packages.Config{Context: ctx, Dir: config.Dir, Env: environment, Mode: packages.NeedName | packages.NeedFiles | packages.NeedModule}, missing...)
		if err != nil {
			return nil, []ir.Diagnostic{ir.NewError("P1_METHOD_PACKAGE_DISCOVERY", err.Error(), ir.SourceSpan{})}
		}
		for _, pkg := range listed {
			if len(pkg.Errors) != 0 || pkg.Name == "" || len(pkg.GoFiles) == 0 {
				message := fmt.Sprintf("cannot discover package metadata for %q", pkg.PkgPath)
				if len(pkg.Errors) != 0 {
					message = pkg.Errors[0].Msg
				}
				return nil, []ir.Diagnostic{ir.NewError("P1_METHOD_PACKAGE_DISCOVERY", message, errorSpan(firstErrorPosition(pkg.Errors), pkg, config))}
			}
			spec := byPath[pkg.PkgPath]
			spec.PackageName = pkg.Name
			spec.Directory = filepath.Dir(pkg.GoFiles[0])
			byPath[pkg.PkgPath] = spec
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]modelcodegen.PackageSpec, 0, len(paths))
	for _, path := range paths {
		spec := byPath[path]
		if spec.PackageName == "" || spec.Directory == "" {
			return nil, []ir.Diagnostic{ir.NewError("P1_METHOD_PACKAGE_SPEC", fmt.Sprintf("package specification for %q requires package name and directory", path), ir.SourceSpan{})}
		}
		result = append(result, spec)
	}
	return result, nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func firstErrorPosition(errors []packages.Error) string {
	if len(errors) == 0 {
		return ""
	}
	return errors[0].Pos
}

func generatedSource(source []byte) bool {
	for _, line := range bytes.SplitN(source, []byte("\n"), 11) {
		text := strings.TrimSpace(string(line))
		if strings.HasPrefix(text, "// Code generated ") && strings.HasSuffix(text, " DO NOT EDIT.") {
			return true
		}
	}
	return false
}

func errorSpan(position string, pkg *packages.Package, config Config) ir.SourceSpan {
	if position == "" {
		return ir.SourceSpan{ModulePath: config.ModulePath}
	}
	file, line, column := position, 0, 0
	if at := strings.LastIndex(file, ":"); at >= 0 {
		column, _ = strconv.Atoi(file[at+1:])
		file = file[:at]
	}
	if at := strings.LastIndex(file, ":"); at >= 0 {
		line, _ = strconv.Atoi(file[at+1:])
		file = file[:at]
	}
	return sourceSpan(file, line, column, line, column, pkg, config)
}

func sourceSpan(filename string, startLine, startColumn, endLine, endColumn int, pkg *packages.Package, config Config) ir.SourceSpan {
	modulePath, moduleDir := config.ModulePath, config.Dir
	if pkg != nil && pkg.Module != nil {
		if modulePath == "" {
			modulePath = pkg.Module.Path
		}
		if pkg.Module.Dir != "" {
			moduleDir = pkg.Module.Dir
		}
	}
	relative := filename
	if relative != "" && filepath.IsAbs(relative) {
		if value, err := filepath.Rel(moduleDir, relative); err == nil && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) {
			relative = value
		} else {
			relative = filepath.Base(relative)
		}
	}
	return ir.NormalizeSourceSpan(ir.SourceSpan{ModulePath: modulePath, RelativeFile: filepath.ToSlash(relative), StartLine: uint32(max(startLine, 0)), StartColumn: uint32(max(startColumn, 0)), EndLine: uint32(max(endLine, 0)), EndColumn: uint32(max(endColumn, 0))})
}
