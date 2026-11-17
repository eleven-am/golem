package scopecontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type FactDeclaration struct {
	ModelID   ir.ModelID
	ScopeName string
	Template  ir.FactTemplateIR
	Span      ir.SourceSpan
}

// NormalizeFacts resolves authority by the same owner's static scope name.
// It never infers an authority predicate or discovers active subscriptions.
func NormalizeFacts(compilation *ir.CompilationIR, declarations []FactDeclaration) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P05_FACT_CONTRACT", "fact templates require a compilation", ir.SourceSpan{})}
	}
	if len(declarations) == 0 {
		for _, owner := range compilation.Contract.Models {
			if len(owner.FactTemplates) != 0 {
				if err := Validate(compilation.Model, compilation.Contract); err != nil {
					return []ir.Diagnostic{ir.NewError("P05_FACT_CONTRACT", err.Error(), ir.SourceSpan{})}
				}
				break
			}
		}
		return nil
	}
	c, err := newCatalog(compilation.Model)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P05_FACT_CONTRACT", err.Error(), ir.SourceSpan{})}
	}
	contracts, err := contractInventory(compilation.Contract)
	if err == nil {
		err = validateScopes(c, compilation.Contract, contracts)
	}
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P05_FACT_CONTRACT", err.Error(), ir.SourceSpan{})}
	}
	pending := map[ir.ModelID][]ir.FactTemplateIR{}
	seen := map[ir.FactTemplateID]bool{}
	var diagnostics []ir.Diagnostic
	for _, declaration := range declarations {
		owner := contracts[declaration.ModelID]
		var scope ir.ScopeTemplateIR
		for _, candidate := range owner.ScopeTemplates {
			if candidate.Name == declaration.ScopeName {
				scope = candidate
			}
		}
		var normalized ir.FactTemplateIR
		var projection []CaptureProjection
		var failure error
		if scope.ID == "" {
			failure = fmt.Errorf("fact template %q requires a scope template named %q on its own model", declaration.Template.Name, declaration.ScopeName)
		} else if declaration.Template.ScopeTemplateID != "" && declaration.Template.ScopeTemplateID != scope.ID {
			failure = fmt.Errorf("fact template authority association disagrees with its declaration")
		} else {
			normalized, projection, failure = c.normalizeFact(declaration.ModelID, declaration.Template, scope)
		}
		if failure == nil {
			failure = requireSubscriptions(contracts, projection)
		}
		if failure == nil && (seen[normalized.ID] || len(pending[declaration.ModelID]) >= MaxTemplatesPerModel) {
			failure = fmt.Errorf("fact templates require unique names and bounded per-model inventory")
		}
		if failure != nil {
			diagnostics = append(diagnostics, ir.NewError("P05_FACT_TEMPLATE", failure.Error(), declaration.Span))
			continue
		}
		seen[normalized.ID] = true
		pending[declaration.ModelID] = append(pending[declaration.ModelID], normalized)
	}
	if len(diagnostics) != 0 {
		ir.SortDiagnostics(diagnostics)
		return diagnostics
	}
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		values := pending[owner.ModelID]
		sort.Slice(values, func(a, b int) bool { return values[a].ID < values[b].ID })
		owner.FactTemplates = values
	}
	return nil
}

func validateFacts(c *catalog, contract ir.ContractIR, contracts map[ir.ModelID]ir.ModelContractIR) error {
	seen := map[ir.FactTemplateID]bool{}
	for _, owner := range contract.Models {
		if len(owner.FactTemplates) > MaxTemplatesPerModel {
			return fmt.Errorf("fact template inventory exceeds its bound")
		}
		var previous ir.FactTemplateID
		for index, template := range owner.FactTemplates {
			var scope ir.ScopeTemplateIR
			for _, candidate := range owner.ScopeTemplates {
				if candidate.ID == template.ScopeTemplateID {
					scope = candidate
				}
			}
			if scope.ID == "" {
				return fmt.Errorf("fact template %q has no authority template on its own model", template.Name)
			}
			normalized, projection, err := c.normalizeFact(owner.ModelID, template, scope)
			if err != nil {
				return err
			}
			if seen[normalized.ID] || index != 0 && previous >= normalized.ID || !reflect.DeepEqual(template, normalized) {
				return fmt.Errorf("fact template %q is not its exact unique sorted normalized contract", template.Name)
			}
			if err := requireSubscriptions(contracts, projection); err != nil {
				return err
			}
			seen[normalized.ID], previous = true, normalized.ID
		}
	}
	return nil
}

func (c *catalog) normalizeFact(root ir.ModelID, template ir.FactTemplateIR, scope ir.ScopeTemplateIR) (ir.FactTemplateIR, []CaptureProjection, error) {
	body, projection, err := c.normalizeMode(root, ir.ScopeTemplateIR{Name: template.Name, Arguments: template.Arguments, Parameters: template.Parameters, Expression: template.Expression}, true)
	if err != nil {
		return ir.FactTemplateIR{}, nil, err
	}
	result := ir.FactTemplateIR{ID: ir.FactTemplateID(body.ID), Name: body.Name, ScopeTemplateID: scope.ID, Arguments: body.Arguments, Parameters: body.Parameters, Expression: body.Expression}
	payload, err := json.Marshal(struct {
		Template    ir.FactTemplateIR
		BodyCapture ir.Fingerprint
		Authority   ir.Fingerprint
	}{result, body.Fingerprint, scope.Fingerprint})
	if err != nil || len(payload) > MaxTemplateBytes {
		return ir.FactTemplateIR{}, nil, fmt.Errorf("fact template exceeds canonical byte limit or cannot be encoded")
	}
	hash := sha256.Sum256(append([]byte("golem:fact-template:v1\x00"), payload...))
	result.Fingerprint = ir.Fingerprint(hex.EncodeToString(hash[:]))
	return result, projection, nil
}

// FactProjection derives the data body's complete footprint, including dead
// branches and empty relation extents. Consumers union it with its scope's
// Projection; body truth must never be used as observation authority.
func FactProjection(model ir.ModelIR, template ir.FactTemplateIR) ([]CaptureProjection, error) {
	c, err := newCatalog(model)
	if err != nil {
		return nil, err
	}
	if template.ScopeTemplateID == "" {
		return nil, fmt.Errorf("fact projection requires an explicit static authority association")
	}
	_, projection, err := c.normalizeMode(template.Expression.ModelID, ir.ScopeTemplateIR{Name: template.Name, Arguments: template.Arguments, Parameters: template.Parameters, Expression: template.Expression}, true)
	return projection, err
}
