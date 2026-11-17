package plan

import (
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type dependencyAuthority uint8

const (
	readableDependencies dependencyAuthority = iota
	physicalDependencies
)

// prepareMasks selects one authority for the private record graph of this
// model occurrence. Facts need physical rows. When a fact and an ordinary mask
// share a relation, both must evaluate against that same physical graph; the
// ordinary mask therefore retains its fully expanded target-read guards.
// Public relation plans keep their independent caller authorization.
func (plan *Plan) prepareMasks(conditions []policyir.Condition) ([]policyir.Condition, dependencyAuthority, error) {
	physical := plan.system
	for _, condition := range conditions {
		physical = physical || policyir.HasFacts(condition)
	}
	if !physical {
		return conditions, readableDependencies, nil
	}
	masks := make([]policyir.Condition, 0, len(conditions))
	bind := func(mask **policyir.Condition, reach *policyir.Condition, field policyir.FieldID) error {
		if *mask == nil {
			return nil
		}
		if reach == nil || reach.ModelID() != plan.model {
			return fail(CodePolicy, plan.model, field, "physical dependency mask has no expanded authorization reach", nil)
		}
		if err := policyir.CheckFactExpansion(*reach); err != nil {
			return fail(CodeLimit, plan.model, field, "physical dependency mask exceeds the fact expansion limit", err)
		}
		*mask = reach
		masks = append(masks, *reach)
		return nil
	}
	for index := range plan.fields {
		field := &plan.fields[index]
		if err := bind(&field.mask, field.sqlMask, field.id); err != nil {
			return nil, 0, err
		}
		if field.mask != nil {
			dependencies, err := dependency.Collect(*field.mask)
			if err != nil {
				return nil, 0, fail(CodePolicy, plan.model, field.id, "physical field-mask dependencies could not be derived", err)
			}
			field.dependencies = dependencies.Dependencies()
		}
	}
	for index := range plan.relations {
		relation := &plan.relations[index]
		if err := bind(&relation.mask, relation.sqlMask, relation.field); err != nil {
			return nil, 0, err
		}
	}
	for index := range plan.counts {
		count := &plan.counts[index]
		if err := bind(&count.mask, count.sqlMask, count.field); err != nil {
			return nil, 0, err
		}
	}
	return masks, physicalDependencies, nil
}
