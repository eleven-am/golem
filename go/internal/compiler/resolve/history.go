package resolve

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// ApplyReviewedIdentities resolves one-time renameFrom declarations against the
// immediately previous reviewed ModelIR. It rewrites accepted transfers into
// ordinary explicit IDs so every later compiler pass shares the same collision
// registry and never needs to infer a rename from spelling.
func ApplyReviewedIdentities(raw ir.RawDeclIR, previous *ir.ModelIR) (ir.RawDeclIR, []ir.Diagnostic) {
	prepared := cloneIdentityRaw(raw)
	resolver := historyResolver{raw: &prepared, previous: previous}
	resolver.resolve()
	ir.SortDiagnostics(resolver.diagnostics)
	return prepared, resolver.diagnostics
}

type previousFieldIdentity struct {
	owner ir.ModelID
	field ir.FieldIR
}

type currentModelIdentity struct {
	index      int
	authored   string
	final      string
	rename     string
	renameSpan ir.SourceSpan
	explicit   bool
	previous   *ir.ModelDeclIR
}

type currentFieldIdentity struct {
	modelIndex int
	fieldIndex int
	owner      ir.ModelID
	authored   string
	final      string
	rename     string
	renameSpan ir.SourceSpan
	explicit   bool
}

type historyResolver struct {
	raw                       *ir.RawDeclIR
	previous                  *ir.ModelIR
	diagnostics               []ir.Diagnostic
	models                    []currentModelIdentity
	previousModelsByReference map[string][]ir.ModelDeclIR
	previousModelsByGo        map[string][]ir.ModelDeclIR
	previousModelsByID        map[ir.ModelID]ir.ModelDeclIR
	previousFieldsByReference map[string][]previousFieldIdentity
}

func (r *historyResolver) resolve() {
	if r.previous != nil {
		r.indexPrevious()
		if r.previous.Schema.StableName != "" && r.previous.Schema.StableName != r.raw.Root.SchemaName {
			r.error("P1_RENAME_SCHEMA_SCOPE", r.raw.Root.Span, "previous reviewed ModelIR belongs to schema %q, current schema is %q", r.previous.Schema.StableName, r.raw.Root.SchemaName)
		}
	}
	r.resolveModels()
	r.resolveFields()
}

func (r *historyResolver) indexPrevious() {
	r.previousModelsByReference = map[string][]ir.ModelDeclIR{}
	r.previousModelsByGo = map[string][]ir.ModelDeclIR{}
	r.previousModelsByID = map[ir.ModelID]ir.ModelDeclIR{}
	r.previousFieldsByReference = map[string][]previousFieldIdentity{}
	for _, model := range r.previous.Models {
		if model.CanonicalIdentity == "" {
			r.error("P1_PREVIOUS_IDENTITY_MISSING", r.raw.Root.Span, "previous reviewed model %s lacks canonicalIdentity", model.ID)
		} else if derivedModelID(model.CanonicalIdentity) != model.ID {
			r.error("P1_PREVIOUS_IDENTITY_MISMATCH", r.raw.Root.Span, "previous reviewed model %s does not match canonicalIdentity %q", model.ID, model.CanonicalIdentity)
		}
		r.addPreviousModelReference(model.CanonicalIdentity, model)
		if model.CanonicalIdentity != string(model.ID) {
			r.addPreviousModelReference(string(model.ID), model)
		}
		r.previousModelsByGo[goIdentity(model.Go.PackagePath, model.Go.Name)] = append(r.previousModelsByGo[goIdentity(model.Go.PackagePath, model.Go.Name)], model)
		if existing, duplicate := r.previousModelsByID[model.ID]; duplicate && existing.CanonicalIdentity != model.CanonicalIdentity {
			r.error("P1_PREVIOUS_IDENTITY_AMBIGUOUS", r.raw.Root.Span, "previous reviewed model ID %s has multiple canonical identities", model.ID)
		}
		r.previousModelsByID[model.ID] = model
		for _, field := range model.Fields {
			if field.CanonicalIdentity == "" {
				r.error("P1_PREVIOUS_IDENTITY_MISSING", r.raw.Root.Span, "previous reviewed field %s.%s lacks canonicalIdentity", model.ID, field.ID)
			} else if derivedFieldID(field.CanonicalIdentity) != field.ID {
				r.error("P1_PREVIOUS_IDENTITY_MISMATCH", r.raw.Root.Span, "previous reviewed field %s does not match canonicalIdentity %q", field.ID, field.CanonicalIdentity)
			}
			previousField := previousFieldIdentity{owner: model.ID, field: field}
			r.addPreviousFieldReference(field.CanonicalIdentity, previousField)
			if field.CanonicalIdentity != string(field.ID) {
				r.addPreviousFieldReference(string(field.ID), previousField)
			}
		}
	}
	for reference, models := range r.previousModelsByReference {
		if reference != "" && len(models) > 1 {
			r.error("P1_PREVIOUS_IDENTITY_AMBIGUOUS", r.raw.Root.Span, "previous reviewed model reference %q is ambiguous", reference)
		}
	}
	for reference, fields := range r.previousFieldsByReference {
		if reference != "" && len(fields) > 1 {
			r.error("P1_PREVIOUS_IDENTITY_AMBIGUOUS", r.raw.Root.Span, "previous reviewed field reference %q is ambiguous", reference)
		}
	}
}

func (r *historyResolver) addPreviousModelReference(reference string, model ir.ModelDeclIR) {
	if reference == "" {
		return
	}
	r.previousModelsByReference[reference] = append(r.previousModelsByReference[reference], model)
}

func (r *historyResolver) addPreviousFieldReference(reference string, field previousFieldIdentity) {
	if reference == "" {
		return
	}
	r.previousFieldsByReference[reference] = append(r.previousFieldsByReference[reference], field)
}

func (r *historyResolver) resolveModels() {
	claims := map[ir.ModelID][]int{}
	for index := range r.raw.Models {
		model := &r.raw.Models[index]
		explicit, _ := attributeValue(model.Marker, "id")
		rename, renameSpan := attributeValue(model.Marker, "renameFrom")
		authored := explicit
		if authored == "" {
			authored = ir.ModelIdentity(r.raw.Root.SchemaName, model.PackagePath+"."+model.GoName)
		}
		current := currentModelIdentity{index: index, authored: authored, final: authored, rename: rename, renameSpan: renameSpan, explicit: explicit != ""}
		if rename != "" {
			if explicit != "" {
				r.error("P1_RENAME_EXPLICIT_ID", renameSpan, "model %s cannot declare both id and renameFrom", model.GoName)
			}
			if r.previous == nil {
				r.error("P1_RENAME_HISTORY_REQUIRED", renameSpan, "model %s renameFrom=%q requires the previous reviewed ModelIR", model.GoName, rename)
			} else if len(r.previousModelsByReference[rename]) == 0 {
				if len(r.previousFieldsByReference[rename]) != 0 {
					r.error("P1_RENAME_WRONG_KIND", renameSpan, "model %s renameFrom=%q targets a previous field", model.GoName, rename)
				} else {
					r.error("P1_RENAME_TARGET_MISSING", renameSpan, "model %s renameFrom=%q does not resolve in the previous reviewed ModelIR", model.GoName, rename)
				}
			} else if len(r.previousModelsByReference[rename]) == 1 {
				target := r.previousModelsByReference[rename][0]
				current.final = target.CanonicalIdentity
				current.previous = &target
				claims[target.ID] = append(claims[target.ID], index)
			}
		} else if explicit == "" && r.previous != nil {
			matches := r.previousModelsByGo[goIdentity(model.PackagePath, model.GoName)]
			if len(matches) == 1 {
				target := matches[0]
				current.final = target.CanonicalIdentity
				current.previous = &target
			} else if len(matches) > 1 {
				r.error("P1_PREVIOUS_IDENTITY_AMBIGUOUS", model.Span, "previous reviewed declaration %s.%s is ambiguous", model.PackagePath, model.GoName)
			}
		}
		r.models = append(r.models, current)
	}
	for targetID, indexes := range claims {
		if len(indexes) > 1 {
			for _, index := range indexes {
				r.error("P1_RENAME_DUPLICATE_CLAIM", r.raw.Models[index].Span, "previous model %s is claimed by multiple renameFrom declarations", targetID)
			}
		}
		for _, current := range r.models {
			if derivedModelID(current.authored) == targetID {
				r.error("P1_RENAME_TARGET_PRESENT", r.raw.Models[current.index].Span, "previous model %s is still authored in the current schema", targetID)
			}
		}
	}
	for index := range r.models {
		current := &r.models[index]
		if current.final == "" {
			continue
		}
		model := &r.raw.Models[current.index]
		if current.rename != "" || (!current.explicit && current.final != current.authored) {
			model.Marker = replaceIdentityAttributes(model.Marker, current.final, current.renameSpan, model.Span)
		}
		if current.previous == nil && r.previous != nil {
			if previous, ok := r.previousModelsByID[derivedModelID(current.final)]; ok {
				copy := previous
				current.previous = &copy
			}
		}
	}
}

func (r *historyResolver) resolveFields() {
	type fieldCandidate struct {
		current currentFieldIdentity
		span    ir.SourceSpan
		name    string
	}
	var fields []fieldCandidate
	claims := map[ir.FieldID][]int{}
	for modelPosition := range r.models {
		modelIdentity := r.models[modelPosition]
		rawModel := &r.raw.Models[modelIdentity.index]
		ownerID := derivedModelID(modelIdentity.final)
		var previousModel *ir.ModelDeclIR
		if previous, ok := r.previousModelsByID[ownerID]; ok {
			copy := previous
			previousModel = &copy
		}
		for fieldIndex := range rawModel.Fields {
			rawField := &rawModel.Fields[fieldIndex]
			rename, renameSpan := attributeValue(rawField.GolemAttrs, "renameFrom")
			if !persistedRawField(*rawField) {
				if rename != "" {
					r.error("P1_RENAME_WRONG_KIND", renameSpan, "field %s.%s is not a persisted model field", rawModel.GoName, rawField.GoName)
				}
				continue
			}
			explicit, _ := attributeValue(rawField.GolemAttrs, "id")
			authored := explicit
			if authored == "" {
				authored = ir.OwnedIdentity(string(ownerID), rawField.GoName)
			}
			current := currentFieldIdentity{modelIndex: modelIdentity.index, fieldIndex: fieldIndex, owner: ownerID, authored: authored, final: authored, rename: rename, renameSpan: renameSpan, explicit: explicit != ""}
			if rename != "" {
				if explicit != "" {
					r.error("P1_RENAME_EXPLICIT_ID", renameSpan, "field %s.%s cannot declare both id and renameFrom", rawModel.GoName, rawField.GoName)
				}
				if r.previous == nil {
					r.error("P1_RENAME_HISTORY_REQUIRED", renameSpan, "field %s.%s renameFrom=%q requires the previous reviewed ModelIR", rawModel.GoName, rawField.GoName, rename)
				} else if len(r.previousFieldsByReference[rename]) == 0 {
					if len(r.previousModelsByReference[rename]) != 0 {
						r.error("P1_RENAME_WRONG_KIND", renameSpan, "field %s.%s renameFrom=%q targets a previous model", rawModel.GoName, rawField.GoName, rename)
					} else {
						r.error("P1_RENAME_TARGET_MISSING", renameSpan, "field %s.%s renameFrom=%q does not resolve in the previous reviewed ModelIR", rawModel.GoName, rawField.GoName, rename)
					}
				} else if len(r.previousFieldsByReference[rename]) == 1 {
					target := r.previousFieldsByReference[rename][0]
					if target.owner != ownerID {
						r.error("P1_RENAME_SCOPE", renameSpan, "field %s.%s renameFrom=%q belongs to previous model %s, not %s", rawModel.GoName, rawField.GoName, rename, target.owner, ownerID)
					} else {
						current.final = target.field.CanonicalIdentity
						claims[target.field.ID] = append(claims[target.field.ID], len(fields))
					}
				}
			} else if explicit == "" && previousModel != nil {
				var matches []ir.FieldIR
				for _, previousField := range previousModel.Fields {
					if previousField.GoName == rawField.GoName {
						matches = append(matches, previousField)
					}
				}
				if len(matches) == 1 {
					current.final = matches[0].CanonicalIdentity
				} else if len(matches) > 1 {
					r.error("P1_PREVIOUS_IDENTITY_AMBIGUOUS", rawField.Span, "previous reviewed field %s.%s is ambiguous", rawModel.GoName, rawField.GoName)
				}
			}
			fields = append(fields, fieldCandidate{current: current, span: rawField.Span, name: rawModel.GoName + "." + rawField.GoName})
		}
	}
	for targetID, indexes := range claims {
		if len(indexes) > 1 {
			for _, index := range indexes {
				r.error("P1_RENAME_DUPLICATE_CLAIM", fields[index].span, "previous field %s is claimed by multiple renameFrom declarations", targetID)
			}
		}
		for _, field := range fields {
			if derivedFieldID(field.current.authored) == targetID {
				r.error("P1_RENAME_TARGET_PRESENT", field.span, "previous field %s is still authored by %s", targetID, field.name)
			}
		}
	}
	for _, field := range fields {
		current := field.current
		if current.final == "" {
			continue
		}
		rawField := &r.raw.Models[current.modelIndex].Fields[current.fieldIndex]
		if current.rename != "" || (!current.explicit && current.final != current.authored) {
			rawField.GolemAttrs = replaceIdentityAttributes(rawField.GolemAttrs, current.final, current.renameSpan, rawField.Span)
		}
	}
}

func persistedRawField(field ir.RawFieldDecl) bool {
	if hasAttribute(field.GolemAttrs, "relation") {
		return true
	}
	return field.DBTag != nil && *field.DBTag != "-"
}

func replaceIdentityAttributes(attributes []ir.RawAttribute, canonical string, renameSpan, fallback ir.SourceSpan) []ir.RawAttribute {
	result := make([]ir.RawAttribute, 0, len(attributes)+1)
	span := renameSpan
	if span == (ir.SourceSpan{}) {
		span = fallback
	}
	for _, attribute := range attributes {
		if attribute.Name == "id" || attribute.Name == "renameFrom" {
			continue
		}
		result = append(result, attribute)
	}
	value := canonical
	result = append(result, ir.RawAttribute{Name: "id", RawValue: &value, Span: span})
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func cloneIdentityRaw(raw ir.RawDeclIR) ir.RawDeclIR {
	result := raw
	result.Models = make([]ir.RawModelDecl, len(raw.Models))
	for modelIndex, model := range raw.Models {
		result.Models[modelIndex] = model
		result.Models[modelIndex].Marker = append([]ir.RawAttribute(nil), model.Marker...)
		result.Models[modelIndex].Fields = make([]ir.RawFieldDecl, len(model.Fields))
		for fieldIndex, field := range model.Fields {
			result.Models[modelIndex].Fields[fieldIndex] = field
			result.Models[modelIndex].Fields[fieldIndex].GolemAttrs = append([]ir.RawAttribute(nil), field.GolemAttrs...)
		}
	}
	return result
}

func derivedModelID(canonical string) ir.ModelID {
	identity, diagnostic := ir.NewIDRegistry().Register(ir.ObjectModel, canonical, ir.SourceSpan{})
	if diagnostic != nil {
		return ""
	}
	return ir.ModelIDFrom(identity)
}

func derivedFieldID(canonical string) ir.FieldID {
	identity, diagnostic := ir.NewIDRegistry().Register(ir.ObjectField, canonical, ir.SourceSpan{})
	if diagnostic != nil {
		return ""
	}
	return ir.FieldIDFrom(identity)
}

func goIdentity(pkg, name string) string { return pkg + "\x00" + name }

func (r *historyResolver) error(code string, span ir.SourceSpan, format string, values ...any) {
	r.diagnostics = append(r.diagnostics, ir.NewError(code, fmt.Sprintf(format, values...), span))
}
