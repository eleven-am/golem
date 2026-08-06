package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/codegen/registry"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcodegen "github.com/eleven-am/golem/go/internal/graphql/codegen"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"golang.org/x/tools/go/packages"
)

func build(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	compiled := compile.Compile(ctx, request.Compile)
	if len(compiled.Diagnostics) != 0 {
		return Result{}, diagnosticsError(compiled.Diagnostics)
	}
	if compiled.Compilation == nil || compiled.ModuleDir == "" || compiled.ModulePath == "" {
		return Result{}, fmt.Errorf("generation compile returned incomplete package metadata")
	}
	appPackage, err := resolveAppPackage(request.AppPackage, compiled)
	if err != nil {
		return Result{}, err
	}
	request.AppPackage = appPackage

	compilation := *compiled.Compilation
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: compiled.Packages, GolemImportPath: request.GolemImportPath})
	if err != nil {
		return Result{}, fmt.Errorf("emit model bootstrap: %w", err)
	}
	registryShell, err := registry.EmitShell(registry.ShellRequest{AppPackage: request.AppPackage, Actor: compilation.Model.Schema.Actor, Model: compilation.Model, GolemImportPath: request.GolemImportPath})
	if err != nil {
		return Result{}, fmt.Errorf("emit registry bootstrap: %w", err)
	}
	bootstrap.Files = append(bootstrap.Files, modelcodegen.File{ImportPath: registryShell.ImportPath, PackageName: registryShell.PackageName, Path: registryShell.Path, Source: registryShell.Source})
	discovered := bindings.DiscoverAndEmit(ctx, bindings.DiscoveryRequest{
		Dir: request.Compile.Dir, ModulePath: compiled.ModulePath, Env: request.Env,
		Compilation: compilation, Packages: compiled.Packages, ModelBootstrap: bootstrap,
		GolemImportPath: request.GolemImportPath,
	})
	if len(discovered.Diagnostics) != 0 {
		return Result{}, diagnosticsError(discovered.Diagnostics)
	}
	compilation.Contract.Methods = mergeBindingMethods(compilation.Contract.Methods, discovered.Methods)
	canonicalContract, err := canonicalContract(compilation.Contract)
	if err != nil {
		return Result{}, fmt.Errorf("canonicalize discovered contract: %w", err)
	}
	compilation.Contract = canonicalContract
	modelFingerprint, err := ir.ModelFingerprint(compilation.Model)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint final model: %w", err)
	}
	if modelFingerprint != compiled.ModelFingerprint {
		return Result{}, fmt.Errorf("typed binding discovery changed persisted ModelIR")
	}
	contractFingerprint, err := ir.ContractFingerprint(compilation.Contract)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint discovered contract: %w", err)
	}

	providers, err := lowerProviders(ctx, compilation.Model, request.Lowerers, request.LowerOptions)
	if err != nil {
		return Result{}, err
	}
	generatorVersion := request.GeneratorVersion
	if generatorVersion == "" {
		generatorVersion = manifest.GeneratorVersion
	}
	templateABI := request.TemplateABIVersion
	if templateABI == "" {
		templateABI = manifest.TemplateABIVersion
	}

	emit := func(digest string) ([]manifest.Artifact, error) {
		return emitArtifacts(compiled, request, compilation, discovered.Entries, providers, modelFingerprint, contractFingerprint, digest, generatorVersion, templateABI)
	}
	provisionalArtifacts, err := emit(strings.Repeat("0", 64))
	if err != nil {
		return Result{}, err
	}
	provisional, err := buildManifest(modelFingerprint, contractFingerprint, providers, provisionalArtifacts, generatorVersion, templateABI)
	if err != nil {
		return Result{}, err
	}
	finalArtifacts, err := emit(provisional.GenerationDigest)
	if err != nil {
		return Result{}, err
	}
	prospective, err := buildManifest(modelFingerprint, contractFingerprint, providers, finalArtifacts, generatorVersion, templateABI)
	if err != nil {
		return Result{}, err
	}
	if prospective.GenerationDigest != provisional.GenerationDigest {
		return Result{}, fmt.Errorf("generation digest changed after stamping final artifacts")
	}
	if err := compileProspective(ctx, compiled, request, prospective.Artifacts); err != nil {
		return Result{}, err
	}
	return Result{
		Prospective: prospective, Compilation: compilation, ModelFingerprint: modelFingerprint,
		ContractFingerprint: contractFingerprint, ModulePath: compiled.ModulePath, ModuleDir: compiled.ModuleDir,
		Bindings: append([]bindings.Entry(nil), discovered.Entries...), Providers: providers,
	}, nil
}

func diagnosticsError(diagnostics []ir.Diagnostic) error {
	values := append([]ir.Diagnostic(nil), diagnostics...)
	ir.SortDiagnostics(values)
	return &DiagnosticsError{Diagnostics: values}
}

func resolveAppPackage(app modelcodegen.PackageSpec, compiled compile.Result) (modelcodegen.PackageSpec, error) {
	if app == (modelcodegen.PackageSpec{}) {
		var matches []modelcodegen.PackageSpec
		for _, spec := range compiled.Packages {
			if spec.ImportPath == compiled.Compilation.Model.Schema.PackagePath {
				matches = append(matches, spec)
			}
		}
		if len(matches) != 1 {
			return modelcodegen.PackageSpec{}, fmt.Errorf("schema-root application package %q resolved to %d package specifications", compiled.Compilation.Model.Schema.PackagePath, len(matches))
		}
		app = matches[0]
	}
	if app.ImportPath == "" || !token.IsIdentifier(app.PackageName) || app.Directory == "" {
		return modelcodegen.PackageSpec{}, fmt.Errorf("generation requires a complete application package specification")
	}
	appDir, err := filepath.Abs(app.Directory)
	if err != nil {
		return modelcodegen.PackageSpec{}, err
	}
	moduleDir, err := filepath.Abs(compiled.ModuleDir)
	if err != nil {
		return modelcodegen.PackageSpec{}, err
	}
	relative, err := filepath.Rel(moduleDir, appDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return modelcodegen.PackageSpec{}, fmt.Errorf("application package directory is outside the compiled module")
	}
	expectedPath := compiled.ModulePath
	if relative != "." {
		expectedPath += "/" + filepath.ToSlash(relative)
	}
	if app.ImportPath != expectedPath {
		return modelcodegen.PackageSpec{}, fmt.Errorf("application package path %q does not match directory identity %q", app.ImportPath, expectedPath)
	}
	for _, spec := range compiled.Packages {
		if spec.ImportPath == app.ImportPath && spec.PackageName != app.PackageName {
			return modelcodegen.PackageSpec{}, fmt.Errorf("application package name %q does not match loaded package name %q", app.PackageName, spec.PackageName)
		}
	}
	return app, nil
}

func mergeBindingMethods(existing, replacement []ir.AttachedMethodIR) []ir.AttachedMethodIR {
	result := make([]ir.AttachedMethodIR, 0, len(existing)+len(replacement))
	for _, method := range existing {
		if method.Kind != string(bindings.BindingPolicy) && method.Kind != string(bindings.BindingHook) {
			result = append(result, method)
		}
	}
	result = append(result, replacement...)
	return result
}

func canonicalContract(contract ir.ContractIR) (ir.ContractIR, error) {
	encoded, err := ir.CanonicalContract(contract)
	if err != nil {
		return ir.ContractIR{}, err
	}
	var result ir.ContractIR
	if err := json.Unmarshal(encoded, &result); err != nil {
		return ir.ContractIR{}, err
	}
	return result, nil
}

func lowerProviders(ctx context.Context, model ir.ModelIR, lowerers []physical.Lowerer, options []ProviderOptions) ([]ProviderResult, error) {
	byProvider := map[ir.Provider]physical.Lowerer{}
	for _, lowerer := range lowerers {
		if lowerer == nil {
			return nil, fmt.Errorf("generation lowerer is nil")
		}
		provider := lowerer.Manifest().Provider
		if provider == "" || byProvider[provider] != nil {
			return nil, fmt.Errorf("generation has empty or duplicate lowerer provider %q", provider)
		}
		byProvider[provider] = lowerer
	}
	optionByProvider := map[ir.Provider]physical.LowerOptions{}
	for _, option := range options {
		if option.Provider == "" {
			return nil, fmt.Errorf("generation has lower options without a provider")
		}
		if _, duplicate := optionByProvider[option.Provider]; duplicate {
			return nil, fmt.Errorf("generation has duplicate lower options for %s", option.Provider)
		}
		optionByProvider[option.Provider] = option.Options
	}
	targets := append([]ir.Provider(nil), model.Providers...)
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	declared := make(map[ir.Provider]bool, len(targets))
	for _, provider := range targets {
		declared[provider] = true
	}
	for provider := range optionByProvider {
		if !declared[provider] {
			return nil, fmt.Errorf("generation has lower options for undeclared provider %s", provider)
		}
	}
	result := make([]ProviderResult, 0, len(targets))
	for _, provider := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lowerer := byProvider[provider]
		if lowerer == nil {
			return nil, fmt.Errorf("generation has no lowerer for declared provider %s", provider)
		}
		schema, err := lowerer.Lower(ctx, model, optionByProvider[provider])
		if err != nil {
			return nil, fmt.Errorf("lower provider %s: %w", provider, err)
		}
		normalized, err := physical.Normalize(schema)
		if err != nil {
			return nil, fmt.Errorf("normalize provider %s: %w", provider, err)
		}
		expectedProvider, err := normalizedProviderManifest(lowerer.Manifest(), normalized.Namespace)
		if err != nil {
			return nil, fmt.Errorf("normalize lowerer %s manifest: %w", provider, err)
		}
		if !reflect.DeepEqual(normalized.Provider, expectedProvider) || normalized.Provider.Provider != provider {
			return nil, fmt.Errorf("lowerer %s returned a schema with inconsistent provider manifest", provider)
		}
		fingerprint, err := physical.PhysicalFingerprint(normalized)
		if err != nil {
			return nil, fmt.Errorf("fingerprint provider %s: %w", provider, err)
		}
		systemFingerprint, err := physical.SystemFingerprint(normalized.Provider, normalized.System)
		if err != nil {
			return nil, fmt.Errorf("fingerprint provider %s system schema: %w", provider, err)
		}
		result = append(result, ProviderResult{Provider: normalized.Provider, Schema: normalized, Fingerprint: ir.Fingerprint(fingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String())})
	}
	return result, nil
}

func normalizedProviderManifest(provider physical.ProviderManifest, namespace physical.Namespace) (physical.ProviderManifest, error) {
	schema, err := physical.Normalize(physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: provider, Namespace: namespace,
	})
	if err != nil {
		return physical.ProviderManifest{}, err
	}
	return schema.Provider, nil
}

func emitArtifacts(compiled compile.Result, request Request, compilation ir.CompilationIR, entries []bindings.Entry, providers []ProviderResult, modelFingerprint, contractFingerprint ir.Fingerprint, digest, generatorVersion, templateABI string) ([]manifest.Artifact, error) {
	stamp := &modelcodegen.FinalStamp{GenerationDigest: digest, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI}
	models, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: compiled.Packages, GolemImportPath: request.GolemImportPath, FinalStamp: stamp})
	if err != nil {
		return nil, fmt.Errorf("emit final model descriptors: %w", err)
	}
	bound, err := bindings.Emit(bindings.Request{Compilation: compilation, Packages: compiled.Packages, Entries: entries, GolemImportPath: request.GolemImportPath, GenerationDigest: digest, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI})
	if err != nil {
		return nil, fmt.Errorf("emit final bindings: %w", err)
	}
	modelPackages := modelPackageSpecs(compilation, compiled.Packages)
	providerInputs := make([]registry.ProviderInput, len(providers))
	for index, provider := range providers {
		providerInputs[index] = registry.ProviderInput{Schema: provider.Schema, Fingerprint: provider.Fingerprint, SystemFingerprint: provider.SystemFingerprint}
	}
	application, err := registry.Emit(registry.Request{
		AppPackage: request.AppPackage, ModelPackages: modelPackages, Actor: compilation.Model.Schema.Actor,
		GolemImportPath: request.GolemImportPath, GenerationDigest: digest, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI,
		Schema: registry.SchemaInput{Model: compilation.Model, Contract: compilation.Contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Providers: providerInputs},
	})
	if err != nil {
		return nil, fmt.Errorf("emit application registry: %w", err)
	}
	graphqlDocument, err := graphqlschema.Build(compilation)
	if err != nil {
		return nil, fmt.Errorf("emit GraphQL schema: %w", err)
	}
	graphqlAdapter, err := graphqlcodegen.Emit(graphqlcodegen.Request{
		PackageName: request.AppPackage.PackageName, AppImportPath: request.AppPackage.ImportPath,
		ModuleDir: compiled.ModuleDir,
		SDL:       graphqlDocument.SDL, ContractFingerprint: contractFingerprint, Actor: compilation.Model.Schema.Actor,
		MutationModels: graphqlMutationModels(compilation), Compilation: &compilation, GolemImportPath: request.GolemImportPath,
		GenerationDigest: digest, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI,
	})
	if err != nil {
		return nil, fmt.Errorf("emit GraphQL Go adapter: %w", err)
	}
	artifacts := make([]manifest.Artifact, 0, len(models.Files)+len(bound)+len(providers)+4+len(graphqlAdapter.Files))
	for _, file := range models.Files {
		artifact, err := goArtifact(compiled.ModuleDir, file.Path, manifest.ArtifactModelGo, file.Source)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	for _, file := range bound {
		artifact, err := goArtifact(compiled.ModuleDir, file.Path, manifest.ArtifactBindingsGo, file.Source)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	artifact, err := goArtifact(compiled.ModuleDir, application.Path, manifest.ArtifactRegistryGo, application.Source)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, artifact)
	graphqlGoPath := filepath.Join(request.AppPackage.Directory, graphqlAdapter.Filename)
	graphqlGoArtifact, err := goArtifact(compiled.ModuleDir, graphqlGoPath, manifest.ArtifactGraphQLGo, graphqlAdapter.Source)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, graphqlGoArtifact)
	for _, file := range graphqlAdapter.Files {
		path := filepath.Join(request.AppPackage.Directory, graphqlcodegen.ExecutablePackageDirectory, file.Filename)
		artifact, err := goArtifact(compiled.ModuleDir, path, manifest.ArtifactGraphQLGo, file.Source)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	graphqlSDLPath, err := moduleRelative(compiled.ModuleDir, filepath.Join(request.AppPackage.Directory, graphqlcodegen.SDLFilename))
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, manifest.Artifact{
		Path: graphqlSDLPath, Kind: manifest.ArtifactGraphQLSDL,
		Content: append([]byte(graphqlGeneratedHeader+"\n"), []byte(graphqlDocument.SDL)...), GeneratedHeader: graphqlGeneratedHeader,
	})
	modelBytes, _ := ir.CanonicalModel(compilation.Model)
	contractBytes, _ := ir.CanonicalContract(compilation.Contract)
	modelEnvelope, err := metadataEnvelope("model_snapshot", modelFingerprint, contractFingerprint, "", "", "", digest, generatorVersion, templateABI, modelBytes)
	if err != nil {
		return nil, err
	}
	contractEnvelope, err := metadataEnvelope("contract_metadata", modelFingerprint, contractFingerprint, "", "", "", digest, generatorVersion, templateABI, contractBytes)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts,
		manifest.Artifact{Path: ".golem/generated/model.snapshot.json", Kind: manifest.ArtifactSnapshot, Content: modelEnvelope, GeneratedHeader: metadataHeader},
		manifest.Artifact{Path: ".golem/generated/contract.metadata.json", Kind: manifest.ArtifactMetadata, Content: contractEnvelope, GeneratedHeader: metadataHeader},
	)
	for _, provider := range providers {
		payload, err := json.Marshal(provider.Schema)
		if err != nil {
			return nil, fmt.Errorf("encode provider %s snapshot: %w", provider.Provider.Provider, err)
		}
		encoded, err := metadataEnvelope("physical_snapshot", modelFingerprint, contractFingerprint, provider.Provider.Provider, provider.Fingerprint, provider.SystemFingerprint, digest, generatorVersion, templateABI, payload)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, manifest.Artifact{Path: ".golem/generated/" + string(provider.Provider.Provider) + ".physical.snapshot.json", Kind: manifest.ArtifactProvider, Content: encoded, GeneratedHeader: metadataHeader})
	}
	return artifacts, nil
}

const metadataHeader = `{"_golemGenerated":"golem:p1:artifact:v1",`
const graphqlGeneratedHeader = `# Code generated by golem. DO NOT EDIT.`

type generatedEnvelope struct {
	Kind                string          `json:"kind"`
	FormatVersion       uint16          `json:"formatVersion"`
	ModelFingerprint    ir.Fingerprint  `json:"modelFingerprint"`
	ContractFingerprint ir.Fingerprint  `json:"contractFingerprint"`
	Provider            ir.Provider     `json:"provider,omitempty"`
	PhysicalFingerprint ir.Fingerprint  `json:"physicalFingerprint,omitempty"`
	SystemFingerprint   ir.Fingerprint  `json:"systemFingerprint,omitempty"`
	GenerationDigest    string          `json:"generationDigest"`
	GeneratorVersion    string          `json:"generatorVersion"`
	TemplateABIVersion  string          `json:"templateAbiVersion"`
	Payload             json.RawMessage `json:"payload"`
}

func metadataEnvelope(kind string, modelFingerprint, contractFingerprint ir.Fingerprint, provider ir.Provider, physicalFingerprint, systemFingerprint ir.Fingerprint, digest, generatorVersion, templateABI string, payload []byte) ([]byte, error) {
	value := generatedEnvelope{Kind: kind, FormatVersion: 1, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Provider: provider, PhysicalFingerprint: physicalFingerprint, SystemFingerprint: systemFingerprint, GenerationDigest: digest, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI, Payload: append(json.RawMessage(nil), payload...)}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode generated %s envelope: %w", kind, err)
	}
	if len(encoded) < 2 || string(encoded[:2]) != "{\n" {
		return nil, fmt.Errorf("encode generated %s envelope: noncanonical object", kind)
	}
	result := append([]byte(metadataHeader+"\n"), encoded[2:]...)
	result = append(result, '\n')
	return result, nil
}

func modelPackageSpecs(compilation ir.CompilationIR, specs []modelcodegen.PackageSpec) []modelcodegen.PackageSpec {
	wanted := map[string]bool{}
	for _, model := range compilation.Model.Models {
		wanted[model.Go.PackagePath] = true
	}
	var result []modelcodegen.PackageSpec
	for _, spec := range specs {
		if wanted[spec.ImportPath] {
			result = append(result, spec)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ImportPath < result[j].ImportPath })
	return result
}

func graphqlMutationModels(compilation ir.CompilationIR) []graphqlcodegen.MutationModel {
	mutable := map[ir.ModelID]bool{}
	for _, contract := range compilation.Contract.Models {
		if !contract.Exposed {
			continue
		}
		for _, operation := range contract.Operations {
			switch operation {
			case ir.OperationCreate, ir.OperationUpdate, ir.OperationUpsert, ir.OperationDelete, ir.OperationUpdateMany, ir.OperationDeleteMany:
				mutable[contract.ModelID] = true
			}
		}
	}
	var result []graphqlcodegen.MutationModel
	for _, model := range compilation.Model.Models {
		if mutable[model.ID] {
			result = append(result, graphqlcodegen.MutationModel{PackagePath: model.Go.PackagePath, GoName: model.Go.Name})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PackagePath != result[j].PackagePath {
			return result[i].PackagePath < result[j].PackagePath
		}
		return result[i].GoName < result[j].GoName
	})
	return result
}

func goArtifact(moduleDir, filePath string, kind manifest.ArtifactKind, content []byte) (manifest.Artifact, error) {
	relative, err := moduleRelative(moduleDir, filePath)
	if err != nil {
		return manifest.Artifact{}, err
	}
	return manifest.Artifact{Path: relative, Kind: kind, Content: append([]byte(nil), content...), GeneratedHeader: manifest.GeneratedHeader}, nil
}

func moduleRelative(moduleDir, value string) (string, error) {
	if !filepath.IsAbs(value) {
		value = filepath.Join(moduleDir, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(moduleDir)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("generated artifact %q is outside module root", value)
	}
	return filepath.ToSlash(relative), nil
}

func buildManifest(modelFingerprint, contractFingerprint ir.Fingerprint, providers []ProviderResult, artifacts []manifest.Artifact, generatorVersion, templateABI string) (manifest.Result, error) {
	fingerprints := make([]manifest.ProviderFingerprint, len(providers))
	for index, provider := range providers {
		fingerprints[index] = manifest.ProviderFingerprint{Provider: provider.Provider.Provider, Fingerprint: provider.Fingerprint, SystemFingerprint: provider.SystemFingerprint}
	}
	return manifest.Build(manifest.Request{ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, ProviderFingerprints: fingerprints, GeneratorVersion: generatorVersion, TemplateABIVersion: templateABI, Artifacts: artifacts})
}

func compileProspective(ctx context.Context, compiled compile.Result, request Request, artifacts []manifest.Artifact) error {
	overlay := map[string][]byte{}
	retained := map[string]bool{}
	packageByDir := map[string]string{}
	patterns := map[string]bool{request.AppPackage.ImportPath: true, compiled.Compilation.Model.Schema.Actor.PackagePath: true}
	for _, spec := range append(append([]modelcodegen.PackageSpec(nil), compiled.Packages...), request.AppPackage) {
		directory, _ := filepath.Abs(spec.Directory)
		packageByDir[filepath.Clean(directory)] = spec.PackageName
	}
	for _, model := range compiled.Compilation.Model.Models {
		patterns[model.Go.PackagePath] = true
	}
	for _, artifact := range artifacts {
		if !isGoArtifact(artifact.Kind) {
			continue
		}
		absolute := filepath.Join(compiled.ModuleDir, filepath.FromSlash(artifact.Path))
		overlay[filepath.Clean(absolute)] = append([]byte(nil), artifact.Content...)
		retained[artifact.Path] = true
	}
	if request.PreviousManifest != nil {
		for _, entry := range request.PreviousManifest.Artifacts {
			if retained[entry.Path] || !isGoArtifact(entry.Kind) {
				continue
			}
			absolute := filepath.Join(compiled.ModuleDir, filepath.FromSlash(entry.Path))
			canonical, canonicalErr := moduleRelative(compiled.ModuleDir, absolute)
			if canonicalErr != nil || canonical != entry.Path {
				return fmt.Errorf("refusing noncanonical stale generated path %q", entry.Path)
			}
			content, err := os.ReadFile(absolute)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect stale generated file %s: %w", entry.Path, err)
			}
			if entry.GeneratedHeader == "" || firstLine(content) != entry.GeneratedHeader {
				return fmt.Errorf("refusing to exclude stale generated file %q without its exact ownership header", entry.Path)
			}
			packageName := packageByDir[filepath.Clean(filepath.Dir(absolute))]
			if packageName == "" {
				parsed, parseErr := parser.ParseFile(token.NewFileSet(), absolute, content, parser.PackageClauseOnly)
				if parseErr != nil {
					return fmt.Errorf("cannot identify package for stale generated file %q: %w", entry.Path, parseErr)
				}
				if parsed.Name == nil || !token.IsIdentifier(parsed.Name.Name) {
					return fmt.Errorf("cannot identify package for stale generated file %q", entry.Path)
				}
				packageName = parsed.Name.Name
			}
			overlay[filepath.Clean(absolute)] = []byte("package " + packageName + "\n")
		}
	}
	orderedPatterns := make([]string, 0, len(patterns))
	for pattern := range patterns {
		orderedPatterns = append(orderedPatterns, pattern)
	}
	sort.Strings(orderedPatterns)
	environment := request.Env
	if len(environment) != 0 {
		environment = append(os.Environ(), environment...)
	}
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedModule
	modfile, cleanupModfile, err := prospectiveModfile(compiled.ModuleDir)
	if err != nil {
		return err
	}
	defer cleanupModfile()
	loaded, err := packages.Load(&packages.Config{Context: ctx, Dir: compiled.ModuleDir, Env: environment, Mode: mode, Overlay: overlay, BuildFlags: []string{"-mod=mod", "-modfile=" + modfile}}, orderedPatterns...)
	if err != nil {
		return fmt.Errorf("prospective package load: %w", err)
	}
	loadedByPath := map[string]bool{}
	seenPackages := map[*packages.Package]bool{}
	var allPackages []*packages.Package
	var visit func(*packages.Package)
	visit = func(pkg *packages.Package) {
		if pkg == nil || seenPackages[pkg] {
			return
		}
		seenPackages[pkg] = true
		loadedByPath[pkg.PkgPath] = true
		allPackages = append(allPackages, pkg)
		paths := make([]string, 0, len(pkg.Imports))
		for importPath := range pkg.Imports {
			paths = append(paths, importPath)
		}
		sort.Strings(paths)
		for _, importPath := range paths {
			visit(pkg.Imports[importPath])
		}
	}
	for _, pkg := range loaded {
		visit(pkg)
	}
	var messages []string
	for _, pattern := range orderedPatterns {
		if !loadedByPath[pattern] {
			messages = append(messages, "requested root "+pattern+" was not loaded")
		}
	}
	for _, pkg := range allPackages {
		for _, packageError := range pkg.Errors {
			if strings.HasPrefix(packageError.Msg, "# ") && strings.Contains(packageError.Msg, "\n") {
				continue
			}
			messages = append(messages, pkg.PkgPath+": "+packageError.Msg)
		}
	}
	sort.Strings(messages)
	if len(messages) != 0 {
		return fmt.Errorf("prospective generated graph does not compile: %s", strings.Join(messages, "; "))
	}
	return nil
}

// prospectiveModfile gives go/packages an isolated dependency workspace. A
// freshly authored consumer module often has no go.sum entries for Golem's
// transitive runtime drivers yet; prospective compilation may resolve those
// dependencies, but must not mutate the user's go.mod/go.sum during inspect,
// check, or a failed publication.
func prospectiveModfile(moduleDir string) (string, func(), error) {
	content, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return "", func() {}, fmt.Errorf("read module file for prospective compilation: %w", err)
	}
	file, err := os.CreateTemp(moduleDir, ".golem-prospective-*.mod")
	if err != nil {
		return "", func() {}, fmt.Errorf("create prospective module file: %w", err)
	}
	name := file.Name()
	cleanup := func() {
		_ = os.Remove(name)
		_ = os.Remove(strings.TrimSuffix(name, ".mod") + ".sum")
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write prospective module file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close prospective module file: %w", err)
	}
	if sum, readErr := os.ReadFile(filepath.Join(moduleDir, "go.sum")); readErr == nil {
		if writeErr := os.WriteFile(strings.TrimSuffix(name, ".mod")+".sum", sum, 0o600); writeErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("write prospective sum file: %w", writeErr)
		}
	} else if !os.IsNotExist(readErr) {
		cleanup()
		return "", func() {}, fmt.Errorf("read module sums for prospective compilation: %w", readErr)
	}
	return name, cleanup, nil
}

func isGoArtifact(kind manifest.ArtifactKind) bool {
	return kind == manifest.ArtifactModelGo || kind == manifest.ArtifactBindingsGo || kind == manifest.ArtifactRegistryGo || kind == manifest.ArtifactGraphQLGo
}

func firstLine(content []byte) string {
	line := string(content)
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSuffix(line, "\r")
}
