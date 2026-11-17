// Package capture validates actor-bound policy declarations against the static
// producer inventory. It proves shape coverage, never row existence or authority.
package capture

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

type Error struct {
	Model  ir.ModelID
	Fact   ir.FactID
	Detail string
}

func (failure *Error) Error() string {
	return fmt.Sprintf("policy capture: model=%x fact=%x: %s", failure.Model, failure.Fact, failure.Detail)
}

type edge struct {
	model, target ir.ModelID
	field         ir.FieldID
	relation      ir.RelationID
}

type question struct {
	root      ir.ModelID
	condition ir.Condition
	path      string
}

// Validator owns one admission or historical matching operation. The optional
// charge callback shares its caller's aggregate work/cancellation budget. Without
// one, the validator owns the ordinary IR expansion budget itself.
type Validator struct {
	registry    *schema.Registry
	charge      func(int) error
	nodes       int
	projections map[ir.ModelID][]scopecontract.CaptureProjection
	verified    map[question]bool
}

func NewValidator(registry *schema.Registry, charge func(int) error) *Validator {
	return &Validator{registry: registry, charge: charge, projections: map[ir.ModelID][]scopecontract.CaptureProjection{}, verified: map[question]bool{}}
}

func (validator *Validator) step(count int) error {
	if validator.charge != nil {
		return validator.charge(count)
	}
	if count < 0 || count > ir.MaxFactExpansionNodes-validator.nodes {
		return &ir.FactLimitError{Limit: ir.FactExpansionNodes, Maximum: ir.MaxFactExpansionNodes}
	}
	validator.nodes += count
	return nil
}

func failure(model ir.ModelID, fact ir.FactID, detail string) error {
	return &Error{Model: model, Fact: fact, Detail: detail}
}

// ValidatePolicy checks every raw definition, including unused definitions and
// Boolean-dead branches, before policy normalization. References are followed
// without constructing or normalizing an expanded expression.
func ValidatePolicy(registry *schema.Registry, policy ir.Policy) error {
	facts := policy.Facts()
	if len(facts) == 0 {
		return nil
	}
	validator := NewValidator(registry, nil)
	if err := validator.step(len(facts)); err != nil {
		return err
	}
	definitions := make(map[ir.FactID]ir.FactDefinition, len(facts))
	for _, definition := range facts {
		if definition.ModelID() != policy.ModelID() || definition.ID() == (ir.FactID{}) {
			return failure(policy.ModelID(), definition.ID(), "definition belongs to another owner")
		}
		if _, duplicate := definitions[definition.ID()]; duplicate {
			return failure(policy.ModelID(), definition.ID(), "definition identity is duplicated")
		}
		definitions[definition.ID()] = definition
	}
	for _, definition := range facts {
		var binding *ir.FactBinding
		if value, bound := definition.Binding(); bound {
			binding = &value
		}
		if err := validator.owner(policy.FactScope(), definition.Body(), definition.ID(), binding, definitions); err != nil {
			return err
		}
	}
	return nil
}

// Owner validates a primary fact/scope observation. Its MODEL must own static
// projections; this does not say which individual rows are affected roots.
func (validator *Validator) Owner(scope ir.FactScope, body ir.Condition, fact ir.FactID, binding *ir.FactBinding) error {
	return validator.owner(scope, body, fact, binding, nil)
}

func (validator *Validator) owner(scope ir.FactScope, body ir.Condition, fact ir.FactID, binding *ir.FactBinding, definitions map[ir.FactID]ir.FactDefinition) error {
	model := scope.ModelID()
	if body.ModelID() != model || body.Validate() != nil {
		return failure(model, fact, "body belongs to another owner or is invalid")
	}
	if err := validator.scopeBinding(scope, fact); err != nil {
		return err
	}
	if binding != nil {
		metadata, _ := validator.registry.Model(golem.ModelID(model))
		authority, bound := scope.Binding()
		template, present := metadata.FactTemplate(compilerir.FactTemplateID(hex.EncodeToString(binding.TemplateID[:])))
		if !bound || !present || binding.ScopeTemplateID != authority.TemplateID || string(template.ScopeTemplateID) != hex.EncodeToString(authority.TemplateID[:]) || string(template.Fingerprint) != hex.EncodeToString(binding.Template[:]) {
			return failure(model, fact, "data fact template provenance differs from the owning scope")
		}
	}
	projections, err := validator.inventory(model, fact)
	if err != nil {
		return err
	}
	for _, condition := range []ir.Condition{scope.Condition(), body} {
		if err := validator.footprint(model, fact, condition, nil, "", projections, definitions, 0); err != nil {
			return err
		}
	}
	return nil
}

// CapturedScope preserves the narrower ordinary-occurrence proof. An unbound
// local scope needs no root-row claim: the graph evaluator must prove its exact
// captured scalars. Bound/related scopes retain their static template contract.
func (validator *Validator) CapturedScope(scope ir.FactScope) error {
	if err := validator.scopeBinding(scope, ir.FactID{}); err != nil {
		return err
	}
	if _, bound := scope.Binding(); !bound {
		return nil
	}
	projections, err := validator.inventory(scope.ModelID(), ir.FactID{})
	if err != nil {
		return err
	}
	return validator.footprint(scope.ModelID(), ir.FactID{}, scope.Condition(), nil, "", projections, nil, 0)
}

func (validator *Validator) scopeBinding(scope ir.FactScope, fact ir.FactID) error {
	if err := validator.step(1); err != nil {
		return err
	}
	model := scope.ModelID()
	if validator.registry == nil || model == (ir.ModelID{}) || scope.Condition().Validate() != nil {
		return failure(model, fact, "scope or schema is absent")
	}
	metadata, present := validator.registry.Model(golem.ModelID(model))
	if !present {
		return failure(model, fact, "owning model is absent")
	}
	binding, bound := scope.Binding()
	if scope.UsesRelations() && !bound {
		return failure(model, fact, "related owning scope has no static template binding")
	}
	if bound {
		template, present := metadata.ScopeTemplate(compilerir.ScopeTemplateID(hex.EncodeToString(binding.TemplateID[:])))
		if !present || string(template.Fingerprint) != hex.EncodeToString(binding.Template[:]) {
			return failure(model, fact, "owning scope template provenance differs")
		}
	}
	return nil
}

func (validator *Validator) inventory(model ir.ModelID, fact ir.FactID) ([]scopecontract.CaptureProjection, error) {
	if value, cached := validator.projections[model]; cached {
		return value, nil
	}
	value := validator.registry.PolicyCaptureProjections(golem.ModelID(model))
	if len(value) == 0 {
		return nil, failure(model, fact, "owning model has no registered capture inventory")
	}
	for _, projection := range value {
		if err := validator.step(1 + len(projection.Path) + len(projection.Fields)); err != nil {
			return nil, err
		}
	}
	validator.projections[model] = value
	return value, nil
}

func (validator *Validator) footprint(root ir.ModelID, fact ir.FactID, condition ir.Condition, path []edge, pathKey string, projections []scopecontract.CaptureProjection, definitions map[ir.FactID]ir.FactDefinition, depth int) error {
	if err := validator.step(1); err != nil {
		return err
	}
	if depth > ir.MaxFactDepth {
		return &ir.FactLimitError{Limit: ir.FactDepth, Maximum: ir.MaxFactDepth}
	}
	key := question{root: root, condition: condition, path: pathKey}
	if validator.verified[key] {
		return nil
	}
	visit := func(child ir.Condition, next []edge, nextKey string) error {
		return validator.footprint(root, fact, child, next, nextKey, projections, definitions, depth+1)
	}
	switch condition.Kind() {
	case ir.ConditionConstant:
	case ir.ConditionFactReference:
		id, _ := condition.FactReference()
		definition, present := definitions[id]
		if !present || condition.ModelID() != root || definition.ModelID() != root {
			return failure(root, fact, "reference has no definition in this owner")
		}
		// Keep the physical prefix. Coverage of this definition at its root
		// says nothing about its use after a self-returning relation path.
		if err := visit(definition.Body(), path, pathKey); err != nil {
			return err
		}
	case ir.ConditionFact:
		_, body, _ := condition.Fact()
		if err := visit(body, path, pathKey); err != nil {
			return err
		}
	case ir.ConditionLogical:
		if err := validator.step(condition.ChildCount()); err != nil {
			return err
		}
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := visit(child, path, pathKey); err != nil {
				return err
			}
		}
	case ir.ConditionScalar:
		field, _ := condition.Field()
		covered := false
		for _, projection := range projections {
			matches, err := validator.matches(projection, condition.ModelID(), path)
			if err != nil {
				return err
			}
			if !matches {
				continue
			}
			for _, captured := range projection.Fields {
				if err := validator.step(1); err != nil {
					return err
				}
				if captured == compilerir.FieldID(hex.EncodeToString(field[:])) {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			return failure(root, fact, "scope or fact scalar is outside the registered capture inventory")
		}
	case ir.ConditionRelation:
		field, relation, target, _, child, _ := condition.Relation()
		model := condition.ModelID()
		if err := validator.step(1 + len(path)); err != nil {
			return err
		}
		next := make([]edge, len(path)+1)
		copy(next, path)
		next[len(path)] = edge{model: model, field: field, relation: relation, target: target}
		nextKey := pathKey + string(model[:]) + string(field[:]) + string(relation[:]) + string(target[:])
		covered := false
		for _, projection := range projections {
			matches, err := validator.matches(projection, target, next)
			if err != nil {
				return err
			}
			if matches {
				covered = true
				break
			}
		}
		if !covered {
			return failure(root, fact, "scope or fact relationship is outside the registered capture inventory")
		}
		if child != nil {
			if err := visit(*child, next, nextKey); err != nil {
				return err
			}
		}
	default:
		return failure(root, fact, "capture footprint requires closed scalar and relationship predicates")
	}
	if err := validator.step(1 + len(path)); err != nil {
		return err
	}
	validator.verified[key] = true
	return nil
}

func (validator *Validator) matches(projection scopecontract.CaptureProjection, model ir.ModelID, path []edge) (bool, error) {
	if err := validator.step(1); err != nil {
		return false, err
	}
	if projection.ModelID != compilerir.ModelID(hex.EncodeToString(model[:])) || len(projection.Path) != len(path) {
		return false, nil
	}
	for index, hop := range path {
		if err := validator.step(1); err != nil {
			return false, err
		}
		actual := projection.Path[index]
		if actual.ModelID != compilerir.ModelID(hex.EncodeToString(hop.model[:])) || actual.FieldID != compilerir.FieldID(hex.EncodeToString(hop.field[:])) || actual.RelationID != compilerir.RelationID(hex.EncodeToString(hop.relation[:])) || actual.TargetModelID != compilerir.ModelID(hex.EncodeToString(hop.target[:])) {
			return false, nil
		}
	}
	return true, nil
}
