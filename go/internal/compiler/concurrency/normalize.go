// Package concurrency owns compiler normalization for the explicit optimistic
// concurrency declaration. It validates only linked, provider-neutral facts
// and installs the selected stable field identity in ModelIR together with its
// validated ContractIR projection atomically.
package concurrency

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// Declaration is the typed pending result of method interpretation. FieldID is
// manifest-backed; eligibility is intentionally deferred until keys,
// generated columns, relations, and field exposure are linked.
type Declaration struct {
	ModelID ir.ModelID
	FieldID ir.FieldID
	Span    ir.SourceSpan
}

// Apply validates every declaration against the completed compiler graph and
// installs them as one atomic IR update. Any diagnostic leaves every model and
// contract unchanged so a malformed declaration cannot publish partial
// ownership or projection state.
func Apply(compilation *ir.CompilationIR, declarations []Declaration) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P1_CONCURRENCY_IR_REQUIRED", "optimistic concurrency requires a compilation IR", ir.SourceSpan{})}
	}
	models := make(map[ir.ModelID]*ir.ModelDeclIR, len(compilation.Model.Models))
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		models[model.ID] = model
	}
	contracts := make(map[ir.ModelID][]*ir.ModelContractIR, len(compilation.Contract.Models))
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		contracts[contract.ModelID] = append(contracts[contract.ModelID], contract)
	}

	seen := make(map[ir.ModelID]struct{}, len(declarations))
	var diagnostics []ir.Diagnostic
	for _, declaration := range declarations {
		model := models[declaration.ModelID]
		if model == nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_MODEL", declaration.Span, "optimistic concurrency references unknown model %s", declaration.ModelID))
			continue
		}
		if _, duplicate := seen[declaration.ModelID]; duplicate {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_DUPLICATE", declaration.Span, "model %s declares optimistic concurrency more than once", model.LogicalName))
			continue
		}
		seen[declaration.ModelID] = struct{}{}
		if model.OptimisticConcurrency != nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_ALREADY_OWNED", declaration.Span, "model %s already owns an optimistic concurrency field", model.LogicalName))
			continue
		}

		field, exists := fieldByID(*model, declaration.FieldID)
		if !exists {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_FIELD", declaration.Span, "optimistic concurrency field %s does not belong to model %s", declaration.FieldID, model.LogicalName))
			continue
		}
		if field.Kind != ir.FieldScalar || field.Scalar == nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_SCALAR", declaration.Span, "optimistic concurrency field %s must be a stored scalar", field.GoName))
			continue
		}
		if field.Scalar.Type.Kind != ir.TypeInt64 {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_TYPE", declaration.Span, "optimistic concurrency field %s must have logical type int64", field.GoName))
		}
		if field.Scalar.Nullable {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_NULLABLE", declaration.Span, "optimistic concurrency field %s must be non-null", field.GoName))
		}
		if field.Scalar.Default != nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_DEFAULT", declaration.Span, "optimistic concurrency field %s cannot have an authored default", field.GoName))
		}
		if field.Scalar.Generation != nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_GENERATED", declaration.Span, "optimistic concurrency field %s cannot be generated", field.GoName))
		}
		if field.Scalar.Updated {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_UPDATED", declaration.Span, "optimistic concurrency field %s cannot have updated behavior", field.GoName))
		}
		if field.Scalar.DatabaseReadOnly {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_DATABASE_READ_ONLY", declaration.Span, "optimistic concurrency field %s cannot already be database-read-only", field.GoName))
		}

		contractEntries := contracts[model.ID]
		if len(contractEntries) != 1 {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_MODEL", declaration.Span, "optimistic concurrency model %s requires one exact contract model", model.LogicalName))
			continue
		}
		if contractEntries[0].OptimisticConcurrency != nil {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_PREOWNED", declaration.Span, "ContractIR cannot author optimistic concurrency for model %s", model.LogicalName))
			continue
		}
		modes, fieldContractExists := contractFieldModes(*contractEntries[0], field.ID)
		if !fieldContractExists {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_FIELD", declaration.Span, "optimistic concurrency field %s requires one exact field contract", field.GoName))
		} else {
			visible := false
			for _, mode := range modes {
				switch mode {
				case ir.ModeVisible:
					visible = true
				case ir.ModeHidden:
					diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_HIDDEN", declaration.Span, "optimistic concurrency field %s cannot be hidden", field.GoName))
				case ir.ModeWriteOnly:
					diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_WRITE_ONLY", declaration.Span, "optimistic concurrency field %s cannot be write-only", field.GoName))
				case ir.ModeImmutable:
					diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_IMMUTABLE", declaration.Span, "optimistic concurrency field %s cannot already be immutable", field.GoName))
				case ir.ModeReadOnly:
					diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_READ_ONLY", declaration.Span, "optimistic concurrency field %s cannot already be read-only", field.GoName))
				case ir.ModeSystem:
					diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_SYSTEM", declaration.Span, "optimistic concurrency field %s cannot already be system owned", field.GoName))
				}
			}
			if !visible && len(modes) == 0 {
				diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_READ_EXPOSURE", declaration.Span, "optimistic concurrency field %s must have ordinary read exposure", field.GoName))
			}
		}
		if keyContains(model.PrimaryKey, field.ID) || keysContain(model.Uniques, field.ID) {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_KEY", declaration.Span, "optimistic concurrency field %s cannot participate in an identity or key", field.GoName))
		}
		if foreignKeyContains(compilation.Model.Relations, model.ID, field.ID) {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_FOREIGN_KEY", declaration.Span, "optimistic concurrency field %s cannot participate in a foreign key", field.GoName))
		}
	}
	if len(diagnostics) != 0 {
		ir.SortDiagnostics(diagnostics)
		return diagnostics
	}

	ordered := append([]Declaration(nil), declarations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ModelID < ordered[j].ModelID })
	for _, declaration := range ordered {
		modelFieldID := declaration.FieldID
		contractFieldID := declaration.FieldID
		models[declaration.ModelID].OptimisticConcurrency = &modelFieldID
		contracts[declaration.ModelID][0].OptimisticConcurrency = &contractFieldID
	}
	return []ir.Diagnostic{}
}

// ValidateAgreement proves that ContractIR is an exact projection of the
// ModelIR authority. It accepts neither an orphan projection nor a missing,
// duplicated, or different projection.
func ValidateAgreement(compilation ir.CompilationIR) []ir.Diagnostic {
	models := make(map[ir.ModelID][]ir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = append(models[model.ID], model)
	}
	contracts := make(map[ir.ModelID][]ir.ModelContractIR, len(compilation.Contract.Models))
	for _, contract := range compilation.Contract.Models {
		contracts[contract.ModelID] = append(contracts[contract.ModelID], contract)
	}

	var diagnostics []ir.Diagnostic
	seen := make(map[ir.ModelID]struct{}, len(models)+len(contracts))
	for modelID := range models {
		seen[modelID] = struct{}{}
	}
	for modelID := range contracts {
		seen[modelID] = struct{}{}
	}
	modelIDs := make([]ir.ModelID, 0, len(seen))
	for modelID := range seen {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Slice(modelIDs, func(i, j int) bool { return modelIDs[i] < modelIDs[j] })

	for _, modelID := range modelIDs {
		modelEntries := models[modelID]
		contractEntries := contracts[modelID]
		modelRelevant := len(modelEntries) == 1 && modelEntries[0].OptimisticConcurrency != nil
		contractRelevant := false
		for _, contract := range contractEntries {
			contractRelevant = contractRelevant || contract.OptimisticConcurrency != nil
		}
		if !modelRelevant && !contractRelevant {
			continue
		}
		if len(modelEntries) != 1 {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_MODEL_PROJECTION", ir.SourceSpan{}, "optimistic concurrency projection for model %s requires one exact ModelIR owner", modelID))
			continue
		}
		if len(contractEntries) == 0 {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_MISSING", ir.SourceSpan{}, "optimistic concurrency model %s is missing its ContractIR projection", modelID))
			continue
		}
		if len(contractEntries) != 1 {
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_DUPLICATE", ir.SourceSpan{}, "optimistic concurrency model %s has duplicate ContractIR projections", modelID))
			continue
		}
		modelField := modelEntries[0].OptimisticConcurrency
		contractField := contractEntries[0].OptimisticConcurrency
		switch {
		case modelField == nil && contractField != nil:
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_ORPHAN", ir.SourceSpan{}, "ContractIR optimistic concurrency field %s has no ModelIR owner", *contractField))
		case modelField != nil && contractField == nil:
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_MISSING", ir.SourceSpan{}, "optimistic concurrency model %s is missing its ContractIR projection", modelID))
		case modelField != nil && contractField != nil && *modelField != *contractField:
			diagnostics = append(diagnostics, diagnostic("P1_CONCURRENCY_CONTRACT_MISMATCH", ir.SourceSpan{}, "optimistic concurrency ModelIR and ContractIR fields disagree for model %s", modelID))
		}
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func fieldByID(model ir.ModelDeclIR, fieldID ir.FieldID) (ir.FieldIR, bool) {
	for _, field := range model.Fields {
		if field.ID == fieldID {
			return field, true
		}
	}
	return ir.FieldIR{}, false
}

func contractFieldModes(contract ir.ModelContractIR, fieldID ir.FieldID) ([]ir.FieldMode, bool) {
	var result []ir.FieldMode
	count := 0
	for _, field := range contract.Fields {
		if field.FieldID == fieldID {
			result = field.Modes
			count++
		}
	}
	return result, count == 1
}

func keyContains(key *ir.KeyIR, fieldID ir.FieldID) bool {
	if key == nil {
		return false
	}
	for _, component := range key.Fields {
		if component == fieldID {
			return true
		}
	}
	return false
}

func keysContain(keys []ir.KeyIR, fieldID ir.FieldID) bool {
	for index := range keys {
		if keyContains(&keys[index], fieldID) {
			return true
		}
	}
	return false
}

func foreignKeyContains(relations []ir.RelationIR, modelID ir.ModelID, fieldID ir.FieldID) bool {
	for _, relation := range relations {
		if relation.ForeignKey == nil {
			continue
		}
		if relation.SourceModel == modelID && containsField(relation.LocalFields, fieldID) {
			return true
		}
		if relation.TargetModel == modelID && containsField(relation.RemoteFields, fieldID) {
			return true
		}
	}
	return false
}

func containsField(fields []ir.FieldID, fieldID ir.FieldID) bool {
	for _, candidate := range fields {
		if candidate == fieldID {
			return true
		}
	}
	return false
}

func diagnostic(code string, span ir.SourceSpan, format string, values ...any) ir.Diagnostic {
	return ir.NewError(code, fmt.Sprintf(format, values...), span)
}
