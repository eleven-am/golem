// Package contract normalizes the compiler-owned analytical contract. It is
// intentionally independent of SQL lowering: applying a patch can change only
// ContractIR and its fingerprint.
package contract

import (
	"fmt"
	"sort"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	DefaultGraphQLMaxGroups              uint32 = 100
	DefaultRelationMaxIntermediateGroups uint32 = 10_000
	HardMaxGraphQLMaxGroups              uint32 = 10_000
	HardMaxRelationIntermediateGroups    uint32 = 1_000_000
)

type ModelPatch struct {
	ModelID                       ir.ModelID
	Dimensions                    *[]ir.FieldID
	Measures                      *[]ir.FieldID
	RelationDimensions            []ir.RelationDimensionContractIR
	GraphQLMaxGroups              *uint32
	RelationMaxIntermediateGroups *uint32
	Enabled                       bool
	ScopedReads                   bool
	Span                          ir.SourceSpan
}

func SortPatches(values []ModelPatch) {
	sort.SliceStable(values, func(i, j int) bool { return values[i].ModelID < values[j].ModelID })
}

func Normalize(compilation *ir.CompilationIR, patches []ModelPatch) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P6_CONTRACT_REQUIRED", "analytics normalization requires a compilation", ir.SourceSpan{})}
	}
	byModel := map[ir.ModelID]ModelPatch{}
	var diagnostics []ir.Diagnostic
	for _, patch := range patches {
		if _, exists := byModel[patch.ModelID]; exists {
			diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_MODEL_DUPLICATE", fmt.Sprintf("model %s declares analytics more than once", patch.ModelID), patch.Span))
		}
		byModel[patch.ModelID] = patch
	}
	models := map[ir.ModelID]ir.ModelDeclIR{}
	relations := map[ir.RelationID]ir.RelationIR{}
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	for _, relation := range compilation.Model.Relations {
		relations[relation.ID] = relation
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		patch, exists := byModel[contract.ModelID]
		if !exists {
			continue
		}
		contract.ScopedReads = patch.ScopedReads
		if !patch.Enabled {
			continue
		}
		aggregation := &ir.AggregationContractIR{
			Enabled:                       true,
			GraphQLMaxGroups:              DefaultGraphQLMaxGroups,
			RelationMaxIntermediateGroups: DefaultRelationMaxIntermediateGroups,
		}
		if patch.Dimensions != nil {
			aggregation.Dimensions = append([]ir.FieldID(nil), (*patch.Dimensions)...)
			aggregation.DimensionsExplicit = true
		}
		if patch.Measures != nil {
			aggregation.Measures = append([]ir.FieldID(nil), (*patch.Measures)...)
			aggregation.MeasuresExplicit = true
		}
		aggregation.RelationDimensions = append([]ir.RelationDimensionContractIR(nil), patch.RelationDimensions...)
		if patch.GraphQLMaxGroups != nil {
			aggregation.GraphQLMaxGroups = *patch.GraphQLMaxGroups
		}
		if patch.RelationMaxIntermediateGroups != nil {
			aggregation.RelationMaxIntermediateGroups = *patch.RelationMaxIntermediateGroups
		}
		contract.Aggregation = aggregation
		diagnostics = append(diagnostics, validate(models[contract.ModelID], *aggregation, models, relations, patch.Span)...)
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func validate(root ir.ModelDeclIR, value ir.AggregationContractIR, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	if root.ID == "" {
		diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_MODEL", "analytics refers to an unknown model", span))
	}
	if value.GraphQLMaxGroups == 0 || value.RelationMaxIntermediateGroups == 0 {
		diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_LIMIT", "analytics limits must both be positive", span))
	}
	if value.GraphQLMaxGroups > HardMaxGraphQLMaxGroups {
		diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_LIMIT", fmt.Sprintf("GraphQL maxGroups exceeds hard maximum %d", HardMaxGraphQLMaxGroups), span))
	}
	if value.RelationMaxIntermediateGroups > HardMaxRelationIntermediateGroups {
		diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_LIMIT", fmt.Sprintf("relation intermediate-group limit exceeds hard maximum %d", HardMaxRelationIntermediateGroups), span))
	}
	fields := map[ir.FieldID]ir.FieldIR{}
	for _, field := range root.Fields {
		fields[field.ID] = field
	}
	checkFields := func(kind string, values []ir.FieldID) {
		seen := map[ir.FieldID]bool{}
		for _, id := range values {
			field, ok := fields[id]
			if !ok || !analyticsCapable(field) {
				diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_FIELD", fmt.Sprintf("%s field %s is not a scalar field of model %s", kind, id, root.ID), span))
			}
			if seen[id] {
				diagnostics = append(diagnostics, ir.NewError("P6_ANALYTICS_FIELD_DUPLICATE", fmt.Sprintf("%s field %s is repeated", kind, id), span))
			}
			seen[id] = true
		}
	}
	checkFields("dimension", value.Dimensions)
	checkFields("measure", value.Measures)
	seenNames := map[string]bool{}
	var declaredPath []ir.RelationID
	for _, dimension := range value.RelationDimensions {
		if !validGraphQLName(dimension.Name) || seenNames[dimension.Name] {
			diagnostics = append(diagnostics, ir.NewError("P6_RELATION_DIMENSION_NAME", fmt.Sprintf("relation dimension name %q is invalid or duplicated", dimension.Name), span))
		}
		seenNames[dimension.Name] = true
		if declaredPath == nil {
			declaredPath = append([]ir.RelationID(nil), dimension.Path...)
		} else if !sameRelationPath(declaredPath, dimension.Path) {
			diagnostics = append(diagnostics, ir.NewError("P6_RELATION_DIMENSION_PATH", "all relation dimensions on a model must use the same forward to-one path", span))
		}
		current := root.ID
		for _, relationID := range dimension.Path {
			relation, ok := relations[relationID]
			if !ok || relation.SourceModel != current || !forwardToOne(models[current], relationID) {
				diagnostics = append(diagnostics, ir.NewError("P6_RELATION_DIMENSION_PATH", fmt.Sprintf("relation %s is not a forward to-one hop from model %s", relationID, current), span))
				current = ""
				break
			}
			current = relation.TargetModel
		}
		terminal, ok := models[current]
		found := false
		if ok {
			for _, field := range terminal.Fields {
				if field.ID == dimension.TerminalField && field.Kind != ir.FieldRelation {
					found = true
				}
			}
		}
		if len(dimension.Path) == 0 || !found {
			diagnostics = append(diagnostics, ir.NewError("P6_RELATION_DIMENSION_TERMINAL", fmt.Sprintf("terminal field %s does not belong to the path target", dimension.TerminalField), span))
		}
	}
	return diagnostics
}

func sameRelationPath(left, right []ir.RelationID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func analyticsCapable(field ir.FieldIR) bool {
	if field.Kind == ir.FieldEnum {
		return true
	}
	if field.Kind != ir.FieldScalar || field.Scalar == nil {
		return false
	}
	switch field.Scalar.Type.Kind {
	case ir.TypeBytes, ir.TypeJSON, ir.TypeScalarList:
		return false
	default:
		return true
	}
}

func forwardToOne(model ir.ModelDeclIR, relation ir.RelationID) bool {
	for _, field := range model.Fields {
		if field.Relation != nil && field.Relation.RelationID == relation && field.Relation.Role == ir.RelationSource && field.Relation.Kind != ir.RelationHasMany {
			return true
		}
	}
	return false
}

func validGraphQLName(value string) bool {
	if value == "" || value[0] == '_' {
		return false
	}
	for index, character := range value {
		if index == 0 && !unicode.IsLower(character) {
			return false
		}
		if index > 0 && character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}
