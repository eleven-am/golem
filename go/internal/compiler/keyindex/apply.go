package keyindex

import (
	"fmt"
	"slices"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// ApplyFragments installs persistence and contract fragments as one atomic
// compilation update. A diagnostic leaves both ModelIR and ContractIR unchanged.
func ApplyFragments(base *ir.CompilationIR, fragments []ModelFragment) []ir.Diagnostic {
	if base == nil {
		return []ir.Diagnostic{ir.NewError("P1_MODEL_IR_REQUIRED", "cannot apply key/index fragments to nil ModelIR", ir.SourceSpan{})}
	}
	models := make(map[ir.ModelID]*ir.ModelDeclIR, len(base.Model.Models))
	for index := range base.Model.Models {
		models[base.Model.Models[index].ID] = &base.Model.Models[index]
	}
	contracts := make(map[ir.ModelID]*ir.ModelContractIR, len(base.Contract.Models))
	for index := range base.Contract.Models {
		contracts[base.Contract.Models[index].ModelID] = &base.Contract.Models[index]
	}
	seen := make(map[ir.ModelID]struct{}, len(fragments))
	var diagnostics []ir.Diagnostic
	for _, fragment := range fragments {
		model := models[fragment.ModelID]
		if model == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_FRAGMENT_MODEL_MISSING", fmt.Sprintf("fragment references unknown model ID %s", fragment.ModelID), ir.SourceSpan{}))
			continue
		}
		if _, duplicate := seen[fragment.ModelID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P1_FRAGMENT_DUPLICATE", fmt.Sprintf("multiple fragments target model ID %s", fragment.ModelID), ir.SourceSpan{}))
		}
		seen[fragment.ModelID] = struct{}{}
		if fragment.PrimaryKey != nil && model.PrimaryKey != nil {
			diagnostics = append(diagnostics, ir.NewError("P1_PRIMARY_KEY_DUPLICATE", fmt.Sprintf("model %s already has a primary key", model.LogicalName), ir.SourceSpan{}))
		}
		if len(fragment.Selectors) != 0 && contracts[fragment.ModelID] == nil {
			diagnostics = append(diagnostics, ir.NewError("P1_FRAGMENT_CONTRACT_MISSING", fmt.Sprintf("selector fragment references model ID %s without a contract destination", fragment.ModelID), ir.SourceSpan{}))
		}
		fieldByID := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fieldByID[field.ID] = field
		}
		for _, generated := range fragment.Generated {
			field, exists := fieldByID[generated.FieldID]
			if !exists || field.Scalar == nil {
				diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_FIELD_MISSING", fmt.Sprintf("generated assignment references unavailable field ID %s", generated.FieldID), ir.SourceSpan{}))
			} else if field.Scalar.Generation != nil {
				diagnostics = append(diagnostics, ir.NewError("P1_GENERATED_DUPLICATE", fmt.Sprintf("field %s already has a generated expression", field.GoName), ir.SourceSpan{}))
			}
		}
		keys := make(map[ir.KeyID]ir.KeyIR, len(model.Uniques)+len(fragment.Uniques)+2)
		if model.PrimaryKey != nil {
			keys[model.PrimaryKey.ID] = *model.PrimaryKey
		}
		if fragment.PrimaryKey != nil {
			keys[fragment.PrimaryKey.ID] = *fragment.PrimaryKey
		}
		for _, key := range model.Uniques {
			keys[key.ID] = key
		}
		for _, key := range fragment.Uniques {
			keys[key.ID] = key
		}
		indexes := make(map[ir.IndexID]ir.IndexIR, len(model.Indexes)+len(fragment.Indexes))
		for _, index := range model.Indexes {
			indexes[index.ID] = index
		}
		for _, index := range fragment.Indexes {
			indexes[index.ID] = index
		}
		selectorNames := make(map[string]ir.KeyID)
		selectorKeys := make(map[ir.KeyID]struct{})
		if contract := contracts[fragment.ModelID]; contract != nil {
			for _, selector := range contract.Selectors {
				selectorNames[selector.Name] = selector.KeyID
				selectorKeys[selector.KeyID] = struct{}{}
			}
		}
		for _, selector := range fragment.Selectors {
			key, exists := keys[selector.KeyID]
			if !exists || key.Kind != selector.Kind || !slices.Equal(key.Fields, selector.Fields) {
				diagnostics = append(diagnostics, ir.NewError("P1_SELECTOR_KEY_MISMATCH", fmt.Sprintf("selector %q does not match key ID %s and its ordered fields", selector.Name, selector.KeyID), ir.SourceSpan{}))
			}
			if selector.Name == "" {
				diagnostics = append(diagnostics, ir.NewError("P1_SELECTOR_NAME_EMPTY", "selector name cannot be empty", ir.SourceSpan{}))
			} else if existing, duplicate := selectorNames[selector.Name]; duplicate && existing != selector.KeyID {
				diagnostics = append(diagnostics, ir.NewError("P1_SELECTOR_NAME_COLLISION", fmt.Sprintf("selector name %q is already owned by key %s", selector.Name, existing), ir.SourceSpan{}))
			}
			if _, duplicate := selectorKeys[selector.KeyID]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P1_SELECTOR_DUPLICATE", fmt.Sprintf("key %s has more than one selector entry", selector.KeyID), ir.SourceSpan{}))
			}
			selectorNames[selector.Name] = selector.KeyID
			selectorKeys[selector.KeyID] = struct{}{}
		}
		equalitySeen := make(map[string]struct{}, len(model.EqualityIndexes)+len(fragment.EqualityIndexes))
		for _, equality := range model.EqualityIndexes {
			equalitySeen[equalityEntryKey(equality)] = struct{}{}
		}
		for _, equality := range fragment.EqualityIndexes {
			if _, exists := fieldByID[equality.FieldID]; !exists {
				diagnostics = append(diagnostics, ir.NewError("P1_EQUALITY_FIELD_MISSING", fmt.Sprintf("EqualityIndexed references unknown field ID %s", equality.FieldID), ir.SourceSpan{}))
			}
			validSource := false
			switch equality.Kind {
			case ir.EqualityViaKey:
				if equality.KeyID != nil && equality.IndexID == nil {
					if key, exists := keys[*equality.KeyID]; exists && len(key.Fields) != 0 && key.Fields[0] == equality.FieldID {
						validSource = true
					}
				}
			case ir.EqualityViaIndex:
				if equality.IndexID != nil && equality.KeyID == nil {
					if index, exists := indexes[*equality.IndexID]; exists && index.Method == ir.IndexBTree && len(index.Keys) != 0 && index.Keys[0].Column != nil && index.Keys[0].Expr == nil && *index.Keys[0].Column == equality.FieldID {
						validSource = true
					}
				}
			}
			if !validSource {
				diagnostics = append(diagnostics, ir.NewError("P1_EQUALITY_SOURCE_MISMATCH", fmt.Sprintf("EqualityIndexed field %s has no matching leading key/index source", equality.FieldID), ir.SourceSpan{}))
			}
			entryKey := equalityEntryKey(equality)
			if _, duplicate := equalitySeen[entryKey]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P1_EQUALITY_DUPLICATE", fmt.Sprintf("duplicate EqualityIndexed entry for field %s", equality.FieldID), ir.SourceSpan{}))
			}
			equalitySeen[entryKey] = struct{}{}
		}
	}
	if len(diagnostics) != 0 {
		ir.SortDiagnostics(diagnostics)
		return diagnostics
	}

	ordered := append([]ModelFragment(nil), fragments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ModelID < ordered[j].ModelID })
	for _, fragment := range ordered {
		model := models[fragment.ModelID]
		if fragment.PrimaryKey != nil {
			key := *fragment.PrimaryKey
			model.PrimaryKey = &key
		}
		model.Uniques = append(model.Uniques, fragment.Uniques...)
		model.Indexes = append(model.Indexes, fragment.Indexes...)
		model.Checks = append(model.Checks, fragment.Checks...)
		model.EqualityIndexes = append(model.EqualityIndexes, fragment.EqualityIndexes...)
		sort.Slice(model.Uniques, func(i, j int) bool { return model.Uniques[i].ID < model.Uniques[j].ID })
		sort.Slice(model.Indexes, func(i, j int) bool { return model.Indexes[i].ID < model.Indexes[j].ID })
		sort.Slice(model.Checks, func(i, j int) bool { return model.Checks[i].ID < model.Checks[j].ID })
		sort.Slice(model.EqualityIndexes, func(i, j int) bool {
			left, right := model.EqualityIndexes[i], model.EqualityIndexes[j]
			if left.FieldID != right.FieldID {
				return left.FieldID < right.FieldID
			}
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			return equalityIdentity(left) < equalityIdentity(right)
		})
		if contract := contracts[fragment.ModelID]; contract != nil {
			contract.Selectors = append(contract.Selectors, fragment.Selectors...)
			sort.Slice(contract.Selectors, func(i, j int) bool {
				if contract.Selectors[i].Name != contract.Selectors[j].Name {
					return contract.Selectors[i].Name < contract.Selectors[j].Name
				}
				return contract.Selectors[i].KeyID < contract.Selectors[j].KeyID
			})
		}
		for _, generated := range fragment.Generated {
			for fieldIndex := range model.Fields {
				field := &model.Fields[fieldIndex]
				if field.ID == generated.FieldID {
					generation := generated.Generation
					field.Scalar.Generation = &generation
					field.Scalar.DatabaseReadOnly = true
					break
				}
			}
		}
	}
	return nil
}

func equalityEntryKey(value ir.EqualityIndexIR) string {
	return string(value.FieldID) + "\x00" + string(value.Kind) + "\x00" + equalityIdentity(value)
}
