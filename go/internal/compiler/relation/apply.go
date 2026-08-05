package relation

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// ApplyFragments installs linked edges and relation fields into a base ModelIR.
// The full batch is validated before mutation, so an error leaves base intact.
func ApplyFragments(base *ir.ModelIR, relations []ir.RelationIR, fragments []ModelFragment) []ir.Diagnostic {
	if base == nil {
		return []ir.Diagnostic{ir.NewError("P1_MODEL_IR_REQUIRED", "cannot apply relation fragments to nil ModelIR", ir.SourceSpan{})}
	}
	models := make(map[ir.ModelID]*ir.ModelDeclIR, len(base.Models))
	fields := make(map[ir.ModelID]map[ir.FieldID]ir.FieldIR, len(base.Models))
	goNames := make(map[ir.ModelID]map[string]struct{}, len(base.Models))
	for index := range base.Models {
		model := &base.Models[index]
		models[model.ID] = model
		fields[model.ID] = make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
		goNames[model.ID] = make(map[string]struct{}, len(model.Fields))
		for _, field := range model.Fields {
			fields[model.ID][field.ID] = field
			goNames[model.ID][field.GoName] = struct{}{}
		}
	}

	var diagnostics []ir.Diagnostic
	seenFragments := make(map[ir.ModelID]struct{}, len(fragments))
	for _, fragment := range fragments {
		if models[fragment.ModelID] == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_FRAGMENT_MODEL_MISSING", fmt.Sprintf("relation fragment references unknown model ID %s", fragment.ModelID), ir.SourceSpan{}))
			continue
		}
		if _, duplicate := seenFragments[fragment.ModelID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_FRAGMENT_DUPLICATE", fmt.Sprintf("multiple relation fragments target model ID %s", fragment.ModelID), ir.SourceSpan{}))
			continue
		}
		seenFragments[fragment.ModelID] = struct{}{}
		for _, field := range fragment.Fields {
			if field.Kind != ir.FieldRelation || field.Relation == nil || field.Scalar != nil {
				diagnostics = append(diagnostics, ir.NewError("P1_RELATION_FRAGMENT_FIELD", fmt.Sprintf("field %s in model %s is not a normalized relation field", field.ID, fragment.ModelID), ir.SourceSpan{}))
				continue
			}
			if _, duplicate := fields[fragment.ModelID][field.ID]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P1_RELATION_FIELD_DUPLICATE", fmt.Sprintf("relation field ID %s already exists in model %s", field.ID, fragment.ModelID), ir.SourceSpan{}))
				continue
			}
			if _, duplicate := goNames[fragment.ModelID][field.GoName]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P1_RELATION_FIELD_DUPLICATE", fmt.Sprintf("relation field Go name %s already exists in model %s", field.GoName, fragment.ModelID), ir.SourceSpan{}))
				continue
			}
			fields[fragment.ModelID][field.ID] = field
			goNames[fragment.ModelID][field.GoName] = struct{}{}
		}
	}

	relationIDs := make(map[ir.RelationID]struct{}, len(base.Relations)+len(relations))
	for _, relation := range base.Relations {
		relationIDs[relation.ID] = struct{}{}
	}
	for _, relation := range relations {
		if _, duplicate := relationIDs[relation.ID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_DUPLICATE", fmt.Sprintf("relation ID %s already exists", relation.ID), ir.SourceSpan{}))
			continue
		}
		relationIDs[relation.ID] = struct{}{}
		if models[relation.SourceModel] == nil || models[relation.TargetModel] == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_MODEL_MISSING", fmt.Sprintf("relation %s references unavailable source or target model", relation.ID), ir.SourceSpan{}))
			continue
		}
		sourceField, sourceExists := fields[relation.SourceModel][relation.SourceField]
		if !sourceExists || sourceField.Relation == nil || sourceField.Relation.RelationID != relation.ID || sourceField.Relation.Role != ir.RelationSource {
			diagnostics = append(diagnostics, ir.NewError("P1_RELATION_SOURCE_FIELD_MISSING", fmt.Sprintf("relation %s has no matching source field %s", relation.ID, relation.SourceField), ir.SourceSpan{}))
		}
		if relation.InverseField != nil {
			inverse, inverseExists := fields[relation.TargetModel][*relation.InverseField]
			if !inverseExists || inverse.Relation == nil || inverse.Relation.RelationID != relation.ID || inverse.Relation.Role != ir.RelationInverse {
				diagnostics = append(diagnostics, ir.NewError("P1_RELATION_INVERSE_FIELD_MISSING", fmt.Sprintf("relation %s has no matching inverse field %s", relation.ID, *relation.InverseField), ir.SourceSpan{}))
			}
		}
		validateAppliedMapping(relation, fields, &diagnostics)
	}

	if len(diagnostics) != 0 {
		ir.SortDiagnostics(diagnostics)
		return diagnostics
	}

	orderedFragments := append([]ModelFragment(nil), fragments...)
	sort.Slice(orderedFragments, func(i, j int) bool { return orderedFragments[i].ModelID < orderedFragments[j].ModelID })
	for _, fragment := range orderedFragments {
		model := models[fragment.ModelID]
		model.Fields = append(model.Fields, fragment.Fields...)
		sort.Slice(model.Fields, func(i, j int) bool {
			if model.Fields[i].DeclarationOrder != model.Fields[j].DeclarationOrder {
				return model.Fields[i].DeclarationOrder < model.Fields[j].DeclarationOrder
			}
			return model.Fields[i].ID < model.Fields[j].ID
		})
	}
	base.Relations = append(base.Relations, relations...)
	sort.Slice(base.Relations, func(i, j int) bool { return base.Relations[i].ID < base.Relations[j].ID })
	return nil
}

func validateAppliedMapping(relation ir.RelationIR, fields map[ir.ModelID]map[ir.FieldID]ir.FieldIR, diagnostics *[]ir.Diagnostic) {
	if len(relation.LocalFields) == 0 || len(relation.LocalFields) != len(relation.RemoteFields) {
		*diagnostics = append(*diagnostics, ir.NewError("P1_RELATION_ARITY", fmt.Sprintf("relation %s has an invalid mapping arity", relation.ID), ir.SourceSpan{}))
		return
	}
	for index := range relation.LocalFields {
		local, localExists := fields[relation.SourceModel][relation.LocalFields[index]]
		remote, remoteExists := fields[relation.TargetModel][relation.RemoteFields[index]]
		if !localExists || local.Scalar == nil || !remoteExists || remote.Scalar == nil {
			*diagnostics = append(*diagnostics, ir.NewError("P1_RELATION_MAPPING_FIELD", fmt.Sprintf("relation %s mapping component %d references unavailable scalar fields", relation.ID, index+1), ir.SourceSpan{}))
		}
	}
}
