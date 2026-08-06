package plan

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func conjoin(model policyir.ModelID, conditions ...policyir.Condition) (policyir.Condition, error) {
	values := make([]policyir.Condition, 0, len(conditions))
	for _, condition := range conditions {
		if condition.ModelID() != model {
			return policyir.Condition{}, fmt.Errorf("constraint model mismatch")
		}
		values = append(values, condition)
	}
	if len(values) == 0 {
		return policyir.NewConstant(model, true)
	}
	if len(values) == 1 {
		return normalize.Condition(values[0])
	}
	combined, err := policyir.NewLogical(model, policyir.LogicalAnd, values)
	if err != nil {
		return policyir.Condition{}, err
	}
	return normalize.Condition(combined)
}

func conditionFields(condition policyir.Condition) []policyir.FieldID {
	seen := make(map[policyir.FieldID]struct{})
	var fields []policyir.FieldID
	var walk func(policyir.Condition)
	walk = func(current policyir.Condition) {
		switch current.Kind() {
		case policyir.ConditionLogical:
			_, children, _ := current.Logical()
			for _, child := range children {
				walk(child)
			}
		case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
			field, _ := current.Field()
			if _, exists := seen[field]; !exists {
				seen[field] = struct{}{}
				fields = append(fields, field)
			}
		case policyir.ConditionRelation:
			field, _, _, _, child, _ := current.Relation()
			if _, exists := seen[field]; !exists {
				seen[field] = struct{}{}
				fields = append(fields, field)
			}
			if child != nil {
				walk(*child)
			}
		}
	}
	walk(condition)
	return fields
}

type imageBuilder struct {
	model        policyir.ModelID
	registry     *schema.Registry
	fields       map[policyir.FieldID]struct{}
	dependencies map[string]mutationir.Dependency
}

func newImageBuilder(model policyir.ModelID, registry *schema.Registry) *imageBuilder {
	return &imageBuilder{model: model, registry: registry, fields: make(map[policyir.FieldID]struct{}), dependencies: make(map[string]mutationir.Dependency)}
}

func (builder *imageBuilder) addFields(fields ...policyir.FieldID) {
	for _, field := range fields {
		if field != (policyir.FieldID{}) {
			builder.fields[field] = struct{}{}
		}
	}
}

func (builder *imageBuilder) addImage(image mutationir.ImageRequirements) error {
	if image.ModelID() == (policyir.ModelID{}) {
		return nil
	}
	if image.ModelID() != builder.model {
		return fmt.Errorf("image model mismatch")
	}
	builder.addFields(image.Fields()...)
	for _, item := range image.Dependencies() {
		encoded := dependencyKey(item)
		builder.dependencies[encoded] = item
	}
	return nil
}

func (builder *imageBuilder) addCondition(condition policyir.Condition) error {
	if condition.ModelID() != builder.model {
		return fmt.Errorf("condition model mismatch")
	}
	plan, err := dependency.Collect(condition)
	if err != nil {
		return err
	}
	return builder.addTree(plan.Dependencies(), nil)
}

func (builder *imageBuilder) addTree(tree dependency.Tree, path []mutationir.RelationHop) error {
	for _, entry := range tree.Entries() {
		if entry.Kind() == dependency.Scalar {
			if len(path) == 0 {
				builder.addFields(entry.FieldID())
			}
			item, err := mutationir.NewDependency(builder.model, path, entry.FieldID())
			if err != nil {
				return err
			}
			builder.dependencies[dependencyKey(item)] = item
			continue
		}
		target, ok := entry.TargetModel()
		if !ok {
			return fmt.Errorf("relation dependency has no target")
		}
		endpoint, ok := builder.registry.RelationEndpoint(golem.ModelID(tree.ModelID()), golem.FieldID(entry.FieldID()), relationIDForField(builder.registry, tree.ModelID(), entry.FieldID()))
		if !ok || policyir.ModelID(endpoint.TargetModelID()) != target {
			return fmt.Errorf("relation dependency is absent from active schema")
		}
		hop, err := mutationir.NewRelationHop(tree.ModelID(), entry.FieldID(), policyir.RelationID(endpoint.RelationID()), target)
		if err != nil {
			return err
		}
		next := append(append([]mutationir.RelationHop(nil), path...), hop)
		if err := builder.addTree(entry.Children(), next); err != nil {
			return err
		}
	}
	return nil
}

func relationIDForField(registry *schema.Registry, model policyir.ModelID, field policyir.FieldID) golem.RelationID {
	fact, ok := registry.Field(golem.ModelID(model), golem.FieldID(field))
	if !ok {
		return golem.RelationID{}
	}
	relation, _ := fact.RelationID()
	return relation
}

func (builder *imageBuilder) build() (mutationir.ImageRequirements, error) {
	fields := make([]policyir.FieldID, 0, len(builder.fields))
	for field := range builder.fields {
		fields = append(fields, field)
	}
	dependencies := make([]mutationir.Dependency, 0, len(builder.dependencies))
	for _, item := range builder.dependencies {
		dependencies = append(dependencies, item)
	}
	sort.Slice(dependencies, func(i, j int) bool { return dependencyKey(dependencies[i]) < dependencyKey(dependencies[j]) })
	return mutationir.NewImageRequirements(builder.model, fields, dependencies)
}

func dependencyKey(value mutationir.Dependency) string {
	root := value.RootModelID()
	result := string(root[:])
	for _, hop := range value.Path() {
		model, field, relation, target := hop.ModelID(), hop.FieldID(), hop.RelationID(), hop.TargetModelID()
		result += string(model[:]) + string(field[:]) + string(relation[:]) + string(target[:])
	}
	field := value.FieldID()
	return result + string(field[:])
}

func providerSet(registry *schema.Registry) (policyir.ProviderSet, error) {
	providers := registry.Providers()
	values := make([]policyir.Provider, len(providers))
	for index, provider := range providers {
		switch provider {
		case golem.SQLite:
			values[index] = policyir.ProviderSQLite
		case golem.PostgreSQL:
			values[index] = policyir.ProviderPostgreSQL
		default:
			return 0, fmt.Errorf("unsupported schema provider")
		}
	}
	return policyir.NewProviderSet(values...)
}

func validateImageAgainstRegistry(registry *schema.Registry, model policyir.ModelID, image mutationir.ImageRequirements) error {
	if image.ModelID() == (policyir.ModelID{}) {
		return nil
	}
	if image.ModelID() != model {
		return fmt.Errorf("image model mismatch")
	}
	for _, field := range image.Fields() {
		if !registry.HasField(golem.ModelID(model), golem.FieldID(field)) {
			return fmt.Errorf("image field %x is absent from root model", field)
		}
	}
	for _, item := range image.Dependencies() {
		if item.RootModelID() != model {
			return fmt.Errorf("dependency root mismatch")
		}
		current := model
		for _, hop := range item.Path() {
			if hop.ModelID() != current {
				return fmt.Errorf("dependency path is discontinuous")
			}
			field, ok := registry.Field(golem.ModelID(current), golem.FieldID(hop.FieldID()))
			if !ok {
				return fmt.Errorf("dependency relation field is absent")
			}
			relation, ok := field.RelationID()
			if !ok || policyir.RelationID(relation) != hop.RelationID() {
				return fmt.Errorf("dependency relation identity mismatch")
			}
			endpoint, ok := registry.RelationEndpoint(golem.ModelID(current), golem.FieldID(hop.FieldID()), relation)
			if !ok || policyir.ModelID(endpoint.TargetModelID()) != hop.TargetModelID() {
				return fmt.Errorf("dependency relation endpoint mismatch")
			}
			current = hop.TargetModelID()
		}
		if !registry.HasField(golem.ModelID(current), golem.FieldID(item.FieldID())) {
			return fmt.Errorf("dependency terminal field is absent")
		}
	}
	return nil
}

func providerRequirements(registry *schema.Registry, operation mutationir.Operation, scalar []mutationir.ScalarOperation) ([]mutationir.ProviderRequirement, error) {
	providers, err := providerSet(registry)
	if err != nil {
		return nil, err
	}
	capabilities := []mutationir.ProviderCapability{mutationir.CapabilityTransaction}
	switch operation {
	case mutationir.Create:
		capabilities = append(capabilities, mutationir.CapabilityPersistedResult)
	case mutationir.Update, mutationir.Delete:
		capabilities = append(capabilities, mutationir.CapabilityTargetLock, mutationir.CapabilityPersistedResult)
	case mutationir.UpdateMany, mutationir.DeleteMany:
		capabilities = append(capabilities, mutationir.CapabilityTargetLock, mutationir.CapabilityExactAffectedIdentities)
	case mutationir.Upsert:
		capabilities = append(capabilities, mutationir.CapabilityTargetLock, mutationir.CapabilitySelectorGuard, mutationir.CapabilityPersistedResult)
	}
	for _, operation := range scalar {
		if operation.Kind() == mutationir.ScalarIncrement || operation.Kind() == mutationir.ScalarDecrement {
			capabilities = append(capabilities, mutationir.CapabilityAtomicNumericUpdate)
			break
		}
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	result := make([]mutationir.ProviderRequirement, 0, len(capabilities))
	for index, capability := range capabilities {
		if index > 0 && capabilities[index-1] == capability {
			continue
		}
		requirement, requirementErr := mutationir.NewProviderRequirement(providers, capability)
		if requirementErr != nil {
			return nil, requirementErr
		}
		result = append(result, requirement)
	}
	return result, nil
}
