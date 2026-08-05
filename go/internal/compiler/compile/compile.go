// Package compile composes the provider-neutral schema compiler passes.
package compile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/keyindex"
	"github.com/eleven-am/golem/go/internal/compiler/methods"
	"github.com/eleven-am/golem/go/internal/compiler/relation"
	"github.com/eleven-am/golem/go/internal/compiler/resolve"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
)

type Config struct {
	Dir           string
	Pattern       string
	Root          string
	PreviousModel *ir.ModelIR
}

type Result struct {
	Compilation         *ir.CompilationIR          `json:"compilation,omitempty"`
	ModelFingerprint    ir.Fingerprint             `json:"modelFingerprint,omitempty"`
	ContractFingerprint ir.Fingerprint             `json:"contractFingerprint,omitempty"`
	Packages            []modelcodegen.PackageSpec `json:"-"`
	ModulePath          string                     `json:"-"`
	ModuleDir           string                     `json:"-"`
	Diagnostics         []ir.Diagnostic            `json:"diagnostics"`
}

// Compile runs the accepted P1 passes using one generation-unit ID registry.
// An error result never exposes a partially accepted CompilationIR.
func Compile(ctx context.Context, config Config) Result {
	extracted := schema.Extract(ctx, schema.Config{Dir: config.Dir, Pattern: config.Pattern, Root: config.Root})
	if len(extracted.Diagnostics) != 0 {
		diagnostics := make([]ir.Diagnostic, len(extracted.Diagnostics))
		for index, diagnostic := range extracted.Diagnostics {
			diagnostics[index] = ir.NewError(diagnostic.Code, diagnostic.Message, diagnostic.Span)
		}
		ir.SortDiagnostics(diagnostics)
		return Result{Diagnostics: diagnostics}
	}
	if config.PreviousModel != nil {
		if _, err := ir.CanonicalModel(*config.PreviousModel); err != nil {
			return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_PREVIOUS_MODEL_INVALID", "previous reviewed ModelIR is invalid: "+err.Error(), extracted.Raw.Root.Span)}}
		}
	}
	prepared, renameDiagnostics := resolve.ApplyReviewedIdentities(extracted.Raw, config.PreviousModel)
	if len(renameDiagnostics) != 0 {
		return Result{Diagnostics: renameDiagnostics}
	}
	return compileWithMethods(ctx, prepared, extracted.Packages, config.Dir)
}

func compileWithMethods(ctx context.Context, raw ir.RawDeclIR, metadata []schema.PackageMetadata, dir string) Result {
	resolved := resolve.Base(raw)
	diagnostics := append([]ir.Diagnostic(nil), resolved.Diagnostics...)
	shapes := relation.Result{}
	if !hasErrors(diagnostics) {
		shapes = relation.Prelink(raw, resolved.Compilation.Model, resolved.IDs)
		diagnostics = append(diagnostics, shapes.Diagnostics...)
		if !hasErrors(shapes.Diagnostics) {
			diagnostics = append(diagnostics, relation.ApplyFragments(&resolved.Compilation.Model, shapes.Relations, shapes.Fragments)...)
		}
	}
	populateContractFields(raw, &resolved.Compilation)
	specs := make([]modelcodegen.PackageSpec, 0, len(metadata))
	modulePath := ""
	moduleDir := ""
	for _, item := range metadata {
		specs = append(specs, modelcodegen.PackageSpec{ImportPath: item.ImportPath, PackageName: item.Name, Directory: item.Directory})
		if modulePath == "" {
			modulePath = item.ModulePath
			moduleDir = item.ModuleDir
		} else if item.ModulePath != modulePath {
			diagnostics = append(diagnostics, ir.NewError("P1_METHOD_MODULE_MISMATCH", fmt.Sprintf("registered package %s belongs to module %s, expected %s", item.ImportPath, item.ModulePath, modulePath), raw.Root.Span))
		}
	}
	var interpreted methods.Result
	if !hasErrors(diagnostics) {
		bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: resolved.Compilation, Packages: specs})
		if err != nil {
			diagnostics = append(diagnostics, ir.NewError("P1_METHOD_EMIT", err.Error(), raw.Root.Span))
		} else {
			shells, shellErr := bindings.EmitShells(bindings.Request{Compilation: resolved.Compilation, Packages: specs})
			if shellErr != nil {
				diagnostics = append(diagnostics, ir.NewError("P1_METHOD_BINDING_SHELL_EMIT", shellErr.Error(), raw.Root.Span))
			} else {
				for _, shell := range shells {
					bootstrap.Files = append(bootstrap.Files, modelcodegen.File{ImportPath: shell.ImportPath, PackageName: shell.PackageName, Path: shell.Path, Source: shell.Source})
				}
			}
			if hasErrors(diagnostics) {
				return finishWithMetadata(raw, resolved.Compilation, diagnostics, specs, modulePath, moduleDir)
			}
			interpreted = methods.Interpret(ctx, methods.Config{Dir: dir, ModulePath: modulePath, Compilation: resolved.Compilation, Packages: specs, Bootstrap: bootstrap, Registry: schemaexpr.NewRegistry()})
			diagnostics = append(diagnostics, interpreted.Diagnostics...)
		}
	}
	if !hasErrors(diagnostics) {
		keys := keyindex.Link(raw, resolved.Compilation, interpreted.Advanced, resolved.IDs)
		diagnostics = append(diagnostics, keys.Diagnostics...)
		if !hasErrors(keys.Diagnostics) {
			diagnostics = append(diagnostics, keyindex.ApplyFragments(&resolved.Compilation, keys.Fragments)...)
		}
	}
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, relation.Validate(resolved.Compilation.Model, shapes.Relations)...)
		diagnostics = append(diagnostics, applyRelationOptions(&resolved.Compilation.Model, interpreted.RelationOptions)...)
	}
	populateContractFields(raw, &resolved.Compilation)
	return finishWithMetadata(raw, resolved.Compilation, diagnostics, specs, modulePath, moduleDir)
}

func finishWithMetadata(raw ir.RawDeclIR, compilation ir.CompilationIR, diagnostics []ir.Diagnostic, specs []modelcodegen.PackageSpec, modulePath, moduleDir string) Result {
	result := finish(raw, compilation, diagnostics)
	if result.Compilation != nil {
		result.Packages = append([]modelcodegen.PackageSpec(nil), specs...)
		result.ModulePath = modulePath
		result.ModuleDir = moduleDir
	}
	return result
}

// CompileRaw composes the semantic passes for an already extracted source IR.
// It is useful for deterministic tests and callers with a cached syntax pass.
func CompileRaw(raw ir.RawDeclIR) Result {
	return CompileRawWithPrevious(raw, nil)
}

// CompileRawWithPrevious is the source-independent history-aware compiler
// entrypoint used by deterministic rename tests and cached extraction callers.
func CompileRawWithPrevious(raw ir.RawDeclIR, previous *ir.ModelIR) Result {
	if previous != nil {
		if _, err := ir.CanonicalModel(*previous); err != nil {
			return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_PREVIOUS_MODEL_INVALID", "previous reviewed ModelIR is invalid: "+err.Error(), raw.Root.Span)}}
		}
	}
	prepared, identityDiagnostics := resolve.ApplyReviewedIdentities(raw, previous)
	if hasErrors(identityDiagnostics) {
		return Result{Diagnostics: identityDiagnostics}
	}
	raw = prepared
	resolved := resolve.Base(raw)
	diagnostics := append([]ir.Diagnostic(nil), resolved.Diagnostics...)
	if !hasErrors(diagnostics) {
		keys := keyindex.Link(raw, resolved.Compilation, nil, resolved.IDs)
		diagnostics = append(diagnostics, keys.Diagnostics...)
		if !hasErrors(keys.Diagnostics) {
			diagnostics = append(diagnostics, keyindex.ApplyFragments(&resolved.Compilation, keys.Fragments)...)
		}
	}
	if !hasErrors(diagnostics) {
		relations := relation.Link(raw, resolved.Compilation.Model, resolved.IDs)
		diagnostics = append(diagnostics, relations.Diagnostics...)
		if !hasErrors(relations.Diagnostics) {
			diagnostics = append(diagnostics, relation.ApplyFragments(&resolved.Compilation.Model, relations.Relations, relations.Fragments)...)
		}
	}
	if !hasErrors(diagnostics) {
		populateContractFields(raw, &resolved.Compilation)
	}
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, validateComplete(resolved.Compilation)...)
	}
	if hasErrors(diagnostics) {
		ir.SortDiagnostics(diagnostics)
		return Result{Diagnostics: diagnostics}
	}

	compilation, err := canonicalCompilation(resolved.Compilation)
	if err != nil {
		diagnostic := ir.NewError("P1_CANONICALIZATION_FAILED", err.Error(), raw.Root.Span)
		return Result{Diagnostics: []ir.Diagnostic{diagnostic}}
	}
	modelFingerprint, err := ir.ModelFingerprint(compilation.Model)
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_MODEL_FINGERPRINT_FAILED", err.Error(), raw.Root.Span)}}
	}
	contractFingerprint, err := ir.ContractFingerprint(compilation.Contract)
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_CONTRACT_FINGERPRINT_FAILED", err.Error(), raw.Root.Span)}}
	}
	return Result{Compilation: &compilation, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Diagnostics: []ir.Diagnostic{}}
}

func finish(raw ir.RawDeclIR, compilation ir.CompilationIR, diagnostics []ir.Diagnostic) Result {
	if !hasErrors(diagnostics) {
		diagnostics = append(diagnostics, validateComplete(compilation)...)
	}
	if hasErrors(diagnostics) {
		ir.SortDiagnostics(diagnostics)
		return Result{Diagnostics: diagnostics}
	}
	canonical, err := canonicalCompilation(compilation)
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_CANONICALIZATION_FAILED", err.Error(), raw.Root.Span)}}
	}
	modelFingerprint, err := ir.ModelFingerprint(canonical.Model)
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_MODEL_FINGERPRINT_FAILED", err.Error(), raw.Root.Span)}}
	}
	contractFingerprint, err := ir.ContractFingerprint(canonical.Contract)
	if err != nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_CONTRACT_FINGERPRINT_FAILED", err.Error(), raw.Root.Span)}}
	}
	return Result{Compilation: &canonical, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Diagnostics: []ir.Diagnostic{}}
}

func applyRelationOptions(model *ir.ModelIR, options []methods.RelationOptionDeclaration) []ir.Diagnostic {
	byID := make(map[ir.RelationID]*ir.RelationIR, len(model.Relations))
	models := make(map[ir.ModelID]ir.ModelDeclIR, len(model.Models))
	for _, entry := range model.Models {
		models[entry.ID] = entry
	}
	for index := range model.Relations {
		byID[model.Relations[index].ID] = &model.Relations[index]
	}
	var diagnostics []ir.Diagnostic
	seen := make(map[ir.RelationID]struct{}, len(options))
	for _, option := range options {
		if option.Provider != ir.ProviderScopePortable {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_OPTION_PROVIDER_UNSUPPORTED", "provider-scoped relation actions cannot be represented by portable ForeignKeyIR", option.Span))
			continue
		}
		edge := byID[option.RelationID]
		if edge == nil || edge.ForeignKey == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_OPTION_MISSING", fmt.Sprintf("relation option references unknown relation %s", option.RelationID), option.Span))
			continue
		}
		if _, duplicate := seen[option.RelationID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_OPTION_DUPLICATE", fmt.Sprintf("relation %s is refined more than once", option.RelationID), option.Span))
		}
		seen[option.RelationID] = struct{}{}
		if option.OnUpdate == nil && option.OnDelete == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_OPTION_EMPTY", "relation refinement must set an update or delete action", option.Span))
		}
		if option.ModelID != edge.SourceModel || option.RelationField != edge.SourceField {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_OPTION_OWNER", fmt.Sprintf("relation option does not belong to source field %s", edge.SourceField), option.Span))
		}
		for _, action := range []*ir.ReferentialAction{option.OnUpdate, option.OnDelete} {
			if action == nil {
				continue
			}
			for _, fieldID := range edge.LocalFields {
				field := modelField(models[edge.SourceModel], fieldID)
				if *action == ir.ActionSetNull && (field == nil || field.Scalar == nil || !field.Scalar.Nullable) {
					diagnostics = append(diagnostics, ir.NewError("P1_RELATION_SET_NULL", "SetNull requires every local foreign-key field to be nullable", option.Span))
				}
				if *action == ir.ActionSetDefault && (field == nil || field.Scalar == nil || field.Scalar.Default == nil || field.Scalar.Default.Producer == ir.ProducerApplication) {
					diagnostics = append(diagnostics, ir.NewError("P1_RELATION_SET_DEFAULT", "SetDefault requires database/provider-produced defaults on every local foreign-key field", option.Span))
				}
			}
		}
	}
	ir.SortDiagnostics(diagnostics)
	if len(diagnostics) != 0 {
		return diagnostics
	}
	for _, option := range options {
		edge := byID[option.RelationID]
		if option.OnUpdate != nil {
			edge.ForeignKey.OnUpdate = *option.OnUpdate
		}
		if option.OnDelete != nil {
			edge.ForeignKey.OnDelete = *option.OnDelete
		}
	}
	return diagnostics
}

func modelField(model ir.ModelDeclIR, id ir.FieldID) *ir.FieldIR {
	for index := range model.Fields {
		if model.Fields[index].ID == id {
			return &model.Fields[index]
		}
	}
	return nil
}

func populateContractFields(raw ir.RawDeclIR, compilation *ir.CompilationIR) {
	rawModels := make(map[string]ir.RawModelDecl, len(raw.Models))
	for _, model := range raw.Models {
		rawModels[model.PackagePath+"\x00"+model.GoName] = model
	}
	models := make(map[ir.ModelID]ir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		model := models[contract.ModelID]
		existing := make(map[ir.FieldID]ir.FieldContractIR, len(contract.Fields)+len(contract.FieldModes))
		for _, field := range contract.Fields {
			existing[field.FieldID] = field
		}
		for _, field := range contract.FieldModes {
			existing[field.FieldID] = field
		}
		rawModel := rawModels[model.Go.PackagePath+"\x00"+model.Go.Name]
		rawByName := make(map[string]ir.RawFieldDecl, len(rawModel.Fields))
		for _, field := range rawModel.Fields {
			rawByName[field.GoName] = field
		}
		contract.Fields = contract.Fields[:0]
		for _, field := range model.Fields {
			entry, exists := existing[field.ID]
			if !exists {
				entry = ir.FieldContractIR{FieldID: field.ID, Modes: []ir.FieldMode{ir.ModeVisible}}
			}
			entry.GraphQLName = field.GoName
			if rawField, ok := rawByName[field.GoName]; ok {
				if graphql, ok := rawAttribute(rawField.GolemAttrs, "graphql"); ok {
					entry.GraphQLName = graphql
				}
				if field.Kind == ir.FieldRelation {
					entry.Modes = relationModes(rawField)
				}
			}
			if entry.Modes == nil {
				entry.Modes = []ir.FieldMode{ir.ModeVisible}
			}
			contract.Fields = append(contract.Fields, entry)
		}
		contract.FieldModes = nil
	}
}

func relationModes(field ir.RawFieldDecl) []ir.FieldMode {
	if _, ok := rawAttribute(field.GolemAttrs, "hidden"); ok {
		return []ir.FieldMode{ir.ModeHidden}
	}
	if _, ok := rawAttribute(field.GolemAttrs, "readonly"); ok {
		return []ir.FieldMode{ir.ModeReadOnly}
	}
	if _, ok := rawAttribute(field.GolemAttrs, "immutable"); ok {
		return []ir.FieldMode{ir.ModeImmutable}
	}
	return []ir.FieldMode{ir.ModeVisible}
}

func rawAttribute(attributes []ir.RawAttribute, name string) (string, bool) {
	for _, attribute := range attributes {
		if attribute.Name == name {
			if attribute.RawValue == nil {
				return "", true
			}
			return *attribute.RawValue, true
		}
	}
	return "", false
}

func canonicalCompilation(compilation ir.CompilationIR) (ir.CompilationIR, error) {
	model, err := ir.CanonicalModel(compilation.Model)
	if err != nil {
		return ir.CompilationIR{}, err
	}
	contract, err := ir.CanonicalContract(compilation.Contract)
	if err != nil {
		return ir.CompilationIR{}, err
	}
	var result ir.CompilationIR
	if err := json.Unmarshal(model, &result.Model); err != nil {
		return ir.CompilationIR{}, err
	}
	if err := json.Unmarshal(contract, &result.Contract); err != nil {
		return ir.CompilationIR{}, err
	}
	return result, nil
}

func validateComplete(compilation ir.CompilationIR) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	modelIDs := make(map[ir.ModelID]ir.ModelDeclIR, len(compilation.Model.Models))
	fieldModels := make(map[ir.FieldID]ir.ModelID)
	for _, model := range compilation.Model.Models {
		if _, duplicate := modelIDs[model.ID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_MODEL_ID_DUPLICATE", fmt.Sprintf("model ID %s is duplicated", model.ID), ir.SourceSpan{}))
		}
		modelIDs[model.ID] = model
		if model.PrimaryKey == nil || len(model.PrimaryKey.Fields) == 0 {
			diagnostics = append(diagnostics, ir.NewError("P1_PRIMARY_KEY_MISSING", fmt.Sprintf("model %s requires a primary key", model.LogicalName), ir.SourceSpan{}))
		}
		for _, field := range model.Fields {
			if owner, duplicate := fieldModels[field.ID]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P1_FIELD_ID_DUPLICATE", fmt.Sprintf("field ID %s is shared by models %s and %s", field.ID, owner, model.ID), ir.SourceSpan{}))
			}
			fieldModels[field.ID] = model.ID
			if field.Kind == ir.FieldRelation && field.Relation == nil || field.Kind != ir.FieldRelation && field.Scalar == nil {
				diagnostics = append(diagnostics, ir.NewError("P1_FIELD_IR_INCOMPLETE", fmt.Sprintf("field %s has incomplete normalized metadata", field.ID), ir.SourceSpan{}))
			}
		}
	}
	contractModels := make(map[ir.ModelID]struct{}, len(compilation.Contract.Models))
	for _, model := range compilation.Contract.Models {
		persisted := modelIDs[model.ModelID]
		if persisted.ID == "" {
			diagnostics = append(diagnostics, ir.NewError("P1_CONTRACT_MODEL_MISSING", fmt.Sprintf("contract references unknown model %s", model.ModelID), ir.SourceSpan{}))
		}
		contractModels[model.ModelID] = struct{}{}
		contractFields := make(map[ir.FieldID]struct{}, len(model.Fields))
		for _, field := range model.Fields {
			if fieldModels[field.FieldID] != model.ModelID {
				diagnostics = append(diagnostics, ir.NewError("P1_CONTRACT_FIELD_MISSING", fmt.Sprintf("contract field %s does not belong to model %s", field.FieldID, model.ModelID), ir.SourceSpan{}))
			}
			contractFields[field.FieldID] = struct{}{}
		}
		for _, field := range persisted.Fields {
			if _, exists := contractFields[field.ID]; !exists {
				diagnostics = append(diagnostics, ir.NewError("P1_CONTRACT_FIELD_MISSING", fmt.Sprintf("field %s has no contract entry", field.ID), ir.SourceSpan{}))
			}
		}
	}
	for modelID := range modelIDs {
		if _, exists := contractModels[modelID]; !exists {
			diagnostics = append(diagnostics, ir.NewError("P1_CONTRACT_MODEL_MISSING", fmt.Sprintf("model %s has no contract entry", modelID), ir.SourceSpan{}))
		}
	}
	providers := append([]ir.Provider(nil), compilation.Model.Providers...)
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	for index := 1; index < len(providers); index++ {
		if providers[index] == providers[index-1] {
			diagnostics = append(diagnostics, ir.NewError("P1_PROVIDER_DUPLICATE", fmt.Sprintf("provider %s is duplicated", providers[index]), ir.SourceSpan{}))
		}
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}
