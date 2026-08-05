package bindings

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"golang.org/x/tools/go/packages"
)

type hookSpec struct {
	method    string
	operation HookOperation
	phase     HookPhase
	request   string
	result    string
}

var hookSpecs = buildHookSpecs()

func buildHookSpecs() map[string]hookSpec {
	result := map[string]hookSpec{}
	for _, operation := range operations {
		result["Before"+operation.Name] = hookSpec{"Before" + operation.Name, operation.Operation, PhaseBefore, operation.Name + "HookRequest", ""}
		result["After"+operation.Name] = hookSpec{"After" + operation.Name, operation.Operation, PhaseAfter, "", operation.Name + "HookResult"}
		if operation.Operation != OperationFindOne && operation.Operation != OperationFindFirst && operation.Operation != OperationFindMany {
			result["AfterCommit"+operation.Name] = hookSpec{"AfterCommit" + operation.Name, operation.Operation, PhaseAfterCommit, "", operation.Name + "HookResult"}
		}
	}
	return result
}

func discoverAndEmit(ctx context.Context, request DiscoveryRequest) Result {
	if request.GolemImportPath == "" {
		request.GolemImportPath = modelcodegen.DefaultGolemImportPath
	}
	shells, err := EmitShells(Request{Compilation: request.Compilation, Packages: request.Packages, GolemImportPath: request.GolemImportPath})
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_BINDING_SHELL_EMIT", err.Error(), ir.SourceSpan{})}}
	}
	overlay := map[string][]byte{}
	fresh := map[string]bool{}
	addFile := func(path string, source []byte) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(request.Dir, path)
		}
		absolute, _ := filepath.Abs(path)
		absolute = filepath.Clean(absolute)
		overlay[absolute], fresh[absolute] = append([]byte(nil), source...), true
	}
	for _, file := range request.ModelBootstrap.Files {
		addFile(file.Path, file.Source)
	}
	for _, file := range shells {
		addFile(file.Path, file.Source)
	}
	registered := map[string]string{}
	patternSet := map[string]bool{}
	for _, spec := range request.Packages {
		absolute, _ := filepath.Abs(spec.Directory)
		registered[filepath.Clean(absolute)] = spec.PackageName
		patternSet[spec.ImportPath] = true
	}
	patternSet[request.Compilation.Model.Schema.Actor.PackagePath] = true
	patterns := make([]string, 0, len(patternSet))
	for pattern := range patternSet {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	parseFile := func(fset *token.FileSet, filename string, source []byte) (*ast.File, error) {
		absolute, _ := filepath.Abs(filename)
		absolute = filepath.Clean(absolute)
		if packageName, ok := registered[filepath.Dir(absolute)]; ok && !fresh[absolute] && generated(source) {
			return parser.ParseFile(fset, filename, "package "+packageName+"\n", parser.AllErrors)
		}
		return parser.ParseFile(fset, filename, source, parser.ParseComments|parser.AllErrors)
	}
	environment := request.Env
	if len(environment) != 0 {
		environment = append(os.Environ(), environment...)
	}
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedModule
	loaded, loadErr := packages.Load(&packages.Config{Context: ctx, Dir: request.Dir, Env: environment, Mode: mode, Overlay: overlay, ParseFile: parseFile}, patterns...)
	if loadErr != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_BINDING_LOAD", loadErr.Error(), ir.SourceSpan{})}}
	}
	byPath := map[string]*packages.Package{}
	var diagnostics []ir.Diagnostic
	for _, pkg := range loaded {
		byPath[pkg.PkgPath] = pkg
		for _, packageError := range pkg.Errors {
			if strings.HasPrefix(packageError.Msg, "# ") && strings.Contains(packageError.Msg, "\n") {
				continue
			}
			diagnostics = append(diagnostics, ir.NewError("P1_BINDING_TYPECHECK", packageError.Msg, packageErrorSpan(packageError.Pos, pkg, request)))
		}
	}
	if hasErrors(diagnostics) {
		ir.SortDiagnostics(diagnostics)
		return Result{Diagnostics: diagnostics}
	}
	actorPackage := findPackage(loaded, request.Compilation.Model.Schema.Actor.PackagePath)
	if actorPackage == nil || actorPackage.Types == nil {
		diagnostics = append(diagnostics, ir.NewError("P1_BINDING_ACTOR_PACKAGE", fmt.Sprintf("actor package %q was not loaded", request.Compilation.Model.Schema.Actor.PackagePath), ir.SourceSpan{}))
		return Result{Diagnostics: diagnostics}
	}
	actorName, _ := actorPackage.Types.Scope().Lookup(request.Compilation.Model.Schema.Actor.Name).(*types.TypeName)
	actorType := unaliasType(typeNameType(actorName))
	if named, ok := actorType.(*types.Named); !ok || named.Obj().Pkg() == nil {
		diagnostics = append(diagnostics, ir.NewError("P1_BINDING_ACTOR_TYPE", "schema actor must be an exported named non-alias type", ir.SourceSpan{}))
		return Result{Diagnostics: diagnostics}
	}
	contracts := map[ir.ModelID]ir.ModelContractIR{}
	for _, contract := range request.Compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	models := append([]ir.ModelDeclIR(nil), request.Compilation.Model.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	var entries []Entry
	for _, model := range models {
		pkg := byPath[model.Go.PackagePath]
		if pkg == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_BINDING_MODEL_PACKAGE", fmt.Sprintf("model package %q was not loaded", model.Go.PackagePath), ir.SourceSpan{}))
			continue
		}
		modelEntries, modelDiagnostics := discoverModel(pkg, model, actorType, request)
		entries = append(entries, modelEntries...)
		diagnostics = append(diagnostics, modelDiagnostics...)
		if contracts[model.ID].Exposed && !containsPolicy(modelEntries) {
			diagnostics = append(diagnostics, ir.NewError("P1_BINDING_POLICY_REQUIRED", fmt.Sprintf("exposed model %s has no DefinePolicy binding", model.Go.Name), ir.SourceSpan{ModulePath: request.ModulePath}))
		}
	}
	sortEntries(entries)
	ir.SortDiagnostics(diagnostics)
	if hasErrors(diagnostics) {
		return Result{Entries: entries, Methods: attachedMethods(entries, request.Compilation), Diagnostics: diagnostics}
	}
	files, emitErr := Emit(Request{Compilation: request.Compilation, Packages: request.Packages, Entries: entries, GolemImportPath: request.GolemImportPath, GenerationDigest: request.GenerationDigest, GeneratorVersion: request.GeneratorVersion, TemplateABIVersion: request.TemplateABIVersion})
	if emitErr != nil {
		diagnostics = append(diagnostics, ir.NewError("P1_BINDING_EMIT", emitErr.Error(), ir.SourceSpan{}))
		return Result{Entries: entries, Methods: attachedMethods(entries, request.Compilation), Diagnostics: diagnostics}
	}
	return Result{Files: files, Entries: entries, Methods: attachedMethods(entries, request.Compilation), Diagnostics: diagnostics}
}

func discoverModel(pkg *packages.Package, model ir.ModelDeclIR, actorType types.Type, request DiscoveryRequest) ([]Entry, []ir.Diagnostic) {
	modelName, _ := pkg.Types.Scope().Lookup(model.Go.Name).(*types.TypeName)
	modelType, _ := unaliasType(typeNameType(modelName)).(*types.Named)
	if modelType == nil {
		return nil, []ir.Diagnostic{ir.NewError("P1_BINDING_MODEL_TYPE", fmt.Sprintf("registered model %s is not a named type", model.Go.Name), ir.SourceSpan{})}
	}
	type candidate struct {
		declaration *ast.FuncDecl
		function    *types.Func
	}
	byName := map[string][]candidate{}
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			function, _ := pkg.TypesInfo.Defs[fn.Name].(*types.Func)
			if function == nil {
				continue
			}
			signature, _ := function.Type().(*types.Signature)
			if signature == nil || signature.Recv() == nil {
				continue
			}
			receiver := signature.Recv().Type()
			if pointer, ok := receiver.(*types.Pointer); ok {
				receiver = pointer.Elem()
			}
			receiverNamed, _ := unaliasType(receiver).(*types.Named)
			if receiverNamed != nil && receiverNamed.Obj() == modelType.Obj() {
				byName[fn.Name.Name] = append(byName[fn.Name.Name], candidate{fn, function})
			}
		}
	}
	var entries []Entry
	var diagnostics []ir.Diagnostic
	for name, candidates := range byName {
		if name != "DefinePolicy" {
			if _, recognized := hookSpecs[name]; !recognized {
				if forbiddenHookName(name) {
					diagnostics = append(diagnostics, bindingError("P1_BINDING_HOOK_FORBIDDEN", fmt.Sprintf("method %s is not a recognized P1 hook", name), candidates[0].declaration, pkg, request))
				}
				continue
			}
		}
		if len(candidates) != 1 {
			diagnostics = append(diagnostics, bindingError("P1_BINDING_METHOD_DUPLICATE", fmt.Sprintf("model %s has multiple %s methods", model.Go.Name, name), candidates[0].declaration, pkg, request))
			continue
		}
		candidate := candidates[0]
		if name == "DefinePolicy" {
			if validPolicy(candidate.function, modelType, actorType, request.GolemImportPath) {
				entries = append(entries, Entry{ModelID: model.ID, PackagePath: model.Go.PackagePath, Receiver: model.Go.Name, Method: name, Kind: BindingPolicy, Span: nodeSpan(candidate.declaration, pkg, request)})
			} else {
				diagnostics = append(diagnostics, bindingError("P1_BINDING_POLICY_SIGNATURE", fmt.Sprintf("%s.DefinePolicy must have signature func (%s) DefinePolicy(*golem.Rules[%s], %s)", model.Go.Name, model.Go.Name, model.Go.Name, request.Compilation.Model.Schema.Actor.Name), candidate.declaration, pkg, request))
			}
			continue
		}
		spec := hookSpecs[name]
		if validHook(candidate.function, modelType, spec, request.GolemImportPath) {
			entries = append(entries, Entry{ModelID: model.ID, PackagePath: model.Go.PackagePath, Receiver: model.Go.Name, Method: name, Kind: BindingHook, Operation: spec.operation, Phase: spec.phase, Span: nodeSpan(candidate.declaration, pkg, request)})
		} else {
			diagnostics = append(diagnostics, bindingError("P1_BINDING_HOOK_SIGNATURE", fmt.Sprintf("%s.%s has an invalid recognized hook signature", model.Go.Name, name), candidate.declaration, pkg, request))
		}
	}
	return entries, diagnostics
}

func validPolicy(function *types.Func, model *types.Named, actor types.Type, golemPath string) bool {
	signature, _ := function.Type().(*types.Signature)
	if signature == nil || signature.Variadic() || signature.Recv() == nil || signature.Params().Len() != 2 || signature.Results().Len() != 0 {
		return false
	}
	if _, pointer := signature.Recv().Type().(*types.Pointer); pointer {
		return false
	}
	if !types.Identical(unaliasType(signature.Recv().Type()), model) || !types.Identical(unaliasType(signature.Params().At(1).Type()), actor) {
		return false
	}
	rulesPointer, ok := unaliasType(signature.Params().At(0).Type()).(*types.Pointer)
	if !ok {
		return false
	}
	rules, ok := unaliasType(rulesPointer.Elem()).(*types.Named)
	return ok && namedOrigin(rules, golemPath, "Rules", model)
}

func validHook(function *types.Func, model *types.Named, spec hookSpec, golemPath string) bool {
	signature, _ := function.Type().(*types.Signature)
	if signature == nil || signature.Variadic() || signature.Recv() == nil || signature.Params().Len() != 2 || signature.Results().Len() != 1 {
		return false
	}
	if _, pointer := signature.Recv().Type().(*types.Pointer); pointer {
		return false
	}
	if !types.Identical(unaliasType(signature.Recv().Type()), model) || !isNamed(signature.Params().At(0).Type(), "context", "Context") || !types.Identical(unaliasType(signature.Results().At(0).Type()), types.Universe.Lookup("error").Type()) {
		return false
	}
	payload := unaliasType(signature.Params().At(1).Type())
	name := spec.result
	if spec.phase == PhaseBefore {
		pointer, ok := payload.(*types.Pointer)
		if !ok {
			return false
		}
		payload, name = unaliasType(pointer.Elem()), spec.request
	}
	named, ok := payload.(*types.Named)
	return ok && namedOrigin(named, golemPath, name, model)
}

func namedOrigin(named *types.Named, packagePath, name string, model types.Type) bool {
	origin := named.Origin()
	object := origin.Obj()
	return object.Pkg() != nil && object.Pkg().Path() == packagePath && object.Name() == name && named.TypeArgs().Len() == 1 && types.Identical(unaliasType(named.TypeArgs().At(0)), model)
}

func isNamed(value types.Type, packagePath, name string) bool {
	named, ok := unaliasType(value).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == packagePath && named.Obj().Name() == name
}

func forbiddenHookName(name string) bool {
	for _, prefix := range []string{"BeforeUpsert", "AfterUpsert", "AfterCommitUpsert", "BeforeAggregate", "AfterAggregate", "AfterCommitAggregate"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func attachedMethods(entries []Entry, compilation ir.CompilationIR) []ir.AttachedMethodIR {
	actor := compilation.Model.Schema.Actor
	result := make([]ir.AttachedMethodIR, 0, len(entries))
	for _, entry := range entries {
		modelID := entry.ModelID
		method := ir.AttachedMethodIR{ModelID: &modelID, Receiver: ir.GoNamedTypeIR{PackagePath: entry.PackagePath, Name: entry.Receiver}, Name: entry.Method, Kind: string(entry.Kind)}
		if entry.Kind == BindingPolicy {
			actorCopy := actor
			method.Actor = &actorCopy
		}
		result = append(result, method)
	}
	return result
}

func containsPolicy(entries []Entry) bool {
	for _, entry := range entries {
		if entry.Kind == BindingPolicy {
			return true
		}
	}
	return false
}

func findPackage(roots []*packages.Package, packagePath string) *packages.Package {
	seen := map[*packages.Package]bool{}
	var visit func(*packages.Package) *packages.Package
	visit = func(pkg *packages.Package) *packages.Package {
		if pkg == nil || seen[pkg] {
			return nil
		}
		seen[pkg] = true
		if pkg.PkgPath == packagePath {
			return pkg
		}
		paths := make([]string, 0, len(pkg.Imports))
		for path := range pkg.Imports {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if found := visit(pkg.Imports[path]); found != nil {
				return found
			}
		}
		return nil
	}
	for _, root := range roots {
		if found := visit(root); found != nil {
			return found
		}
	}
	return nil
}

func unaliasType(value types.Type) types.Type {
	if value == nil {
		return nil
	}
	return types.Unalias(value)
}
func typeNameType(value *types.TypeName) types.Type {
	if value == nil {
		return nil
	}
	return value.Type()
}
func hasErrors(items []ir.Diagnostic) bool {
	for _, item := range items {
		if item.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}

func bindingError(code, message string, node ast.Node, pkg *packages.Package, request DiscoveryRequest) ir.Diagnostic {
	return ir.NewError(code, message, nodeSpan(node, pkg, request))
}
func nodeSpan(node ast.Node, pkg *packages.Package, request DiscoveryRequest) ir.SourceSpan {
	start, end := pkg.Fset.Position(node.Pos()), pkg.Fset.Position(node.End())
	return sourceSpan(start.Filename, start.Line, start.Column, end.Line, end.Column, pkg, request)
}
func packageErrorSpan(position string, pkg *packages.Package, request DiscoveryRequest) ir.SourceSpan {
	file, line, column := position, 0, 0
	if at := strings.LastIndex(file, ":"); at >= 0 {
		column, _ = strconv.Atoi(file[at+1:])
		file = file[:at]
	}
	if at := strings.LastIndex(file, ":"); at >= 0 {
		line, _ = strconv.Atoi(file[at+1:])
		file = file[:at]
	}
	return sourceSpan(file, line, column, line, column, pkg, request)
}
func sourceSpan(filename string, startLine, startColumn, endLine, endColumn int, pkg *packages.Package, request DiscoveryRequest) ir.SourceSpan {
	modulePath, moduleDir := request.ModulePath, request.Dir
	if pkg != nil && pkg.Module != nil {
		if modulePath == "" {
			modulePath = pkg.Module.Path
		}
		if pkg.Module.Dir != "" {
			moduleDir = pkg.Module.Dir
		}
	}
	relative := filename
	if filepath.IsAbs(relative) {
		if value, err := filepath.Rel(moduleDir, relative); err == nil && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) {
			relative = value
		} else {
			relative = filepath.Base(relative)
		}
	}
	return ir.NormalizeSourceSpan(ir.SourceSpan{ModulePath: modulePath, RelativeFile: filepath.ToSlash(relative), StartLine: uint32(max(startLine, 0)), StartColumn: uint32(max(startColumn, 0)), EndLine: uint32(max(endLine, 0)), EndColumn: uint32(max(endColumn, 0))})
}
func generated(source []byte) bool {
	for _, line := range bytes.SplitN(source, []byte("\n"), 11) {
		text := strings.TrimSpace(string(line))
		if strings.HasPrefix(text, "// Code generated ") && strings.HasSuffix(text, " DO NOT EDIT.") {
			return true
		}
	}
	return false
}
