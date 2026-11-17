package extension

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func resolveInvalidationDeclarations(compilation *ir.CompilationIR, custom []CustomOperationDeclaration) ([]CustomOperationDeclaration, []ir.Diagnostic) {
	result := append([]CustomOperationDeclaration(nil), custom...)
	var diagnostics []ir.Diagnostic
	for index, declaration := range result {
		if declaration.Operation.Invalidation == nil {
			continue
		}
		owned := *declaration.Operation.Invalidation
		owned.Dependencies = append([]ir.CustomInvalidationDependencyIR(nil), owned.Dependencies...)
		for position := range owned.Dependencies {
			dependency := &owned.Dependencies[position]
			fail := func(detail string) {
				diagnostics = append(diagnostics, ir.NewError("P07_INVALIDATION_DEPENDENCY", fmt.Sprintf("invalidation %s: %s", dependency.Name, detail), declaration.Span))
			}
			var model *ir.ModelDeclIR
			var contract *ir.ModelContractIR
			for candidate := range compilation.Model.Models {
				if compilation.Model.Models[candidate].ID == dependency.ModelID {
					model = &compilation.Model.Models[candidate]
				}
			}
			for candidate := range compilation.Contract.Models {
				if compilation.Contract.Models[candidate].ModelID == dependency.ModelID {
					contract = &compilation.Contract.Models[candidate]
				}
			}
			if model == nil || contract == nil {
				fail("source model is not registered")
				continue
			}
			if dependency.Binder.Actor == nil || *dependency.Binder.Actor != compilation.Model.Schema.Actor {
				fail("binder actor differs from the schema actor")
			}
			if dependency.Kind == ir.CustomInvalidationReadableRows {
				for _, id := range dependency.Fields {
					found := false
					for _, field := range model.Fields {
						found = found || field.ID == id && field.Kind != ir.FieldRelation
					}
					if !found {
						fail("watched field must be a scalar of the declared source model")
					}
				}
				continue
			}
			names, pending := declaration.InvalidationTemplates[dependency.ID]
			if !pending {
				continue
			} // Already-normalized generated contract.
			for _, scope := range contract.ScopeTemplates {
				if scope.Name == names.Scope {
					dependency.ScopeTemplateID = scope.ID
				}
			}
			if dependency.ScopeTemplateID == "" {
				fail("scope template is not registered on its source model")
				continue
			}
			dependency.FactTemplateIDs = nil
			for _, name := range names.Facts {
				found := false
				for _, fact := range contract.FactTemplates {
					if fact.Name == name && fact.ScopeTemplateID == dependency.ScopeTemplateID {
						dependency.FactTemplateIDs = append(dependency.FactTemplateIDs, fact.ID)
						found = true
					}
				}
				if !found {
					fail("fact template is absent or belongs to a different scope")
				}
			}
		}
		sort.Slice(owned.Dependencies, func(i, j int) bool { return owned.Dependencies[i].ID < owned.Dependencies[j].ID })
		for position := range owned.Dependencies {
			dependency := &owned.Dependencies[position]
			dependency.Fields = append([]ir.FieldID(nil), dependency.Fields...)
			dependency.FactTemplateIDs = append([]ir.FactTemplateID(nil), dependency.FactTemplateIDs...)
			sort.Slice(dependency.Fields, func(i, j int) bool { return dependency.Fields[i] < dependency.Fields[j] })
			sort.Slice(dependency.FactTemplateIDs, func(i, j int) bool { return dependency.FactTemplateIDs[i] < dependency.FactTemplateIDs[j] })
		}
		result[index].Operation.Invalidation = &owned
	}
	return result, diagnostics
}
