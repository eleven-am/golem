package bind

import (
	"fmt"
	"strconv"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type inputState struct {
	binder *Binder
	nodes  int
}

type fieldBinding struct {
	model    compilerir.FieldIR
	contract compilerir.FieldContractIR
}

func (s *inputState) step(path string, depth int) error {
	s.nodes++
	if s.nodes > s.binder.limits.MaxInputNodes {
		return fmt.Errorf("P5_BIND_LIMIT: %s exceeds %d input nodes", path, s.binder.limits.MaxInputNodes)
	}
	if depth > s.binder.limits.MaxInputDepth {
		return fmt.Errorf("P5_BIND_LIMIT: %s exceeds input depth %d", path, s.binder.limits.MaxInputDepth)
	}
	return nil
}

func (s *inputState) where(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, raw any, path string, depth int) (policyir.Condition, error) {
	if err := s.step(path, depth); err != nil {
		return policyir.Condition{}, err
	}
	value, err := object(raw, path)
	if err != nil {
		return policyir.Condition{}, err
	}
	if len(value) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_WHERE: %s must not be empty", path)
	}
	modelID, err := policyModelID(model.ID)
	if err != nil {
		return policyir.Condition{}, err
	}
	if all, present := value["all"]; present {
		truth, ok := all.(bool)
		if !ok || len(value) != 1 {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_WHERE: %s.all must be the only member and Boolean", path)
		}
		condition, err := policyir.NewConstant(modelID, truth)
		if err != nil {
			return policyir.Condition{}, err
		}
		return validateCondition(condition, s.binder.providers)
	}
	fields := indexFields(model, contract)
	var conditions []policyir.Condition
	for _, key := range sortedKeys(value) {
		rawChild := value[key]
		switch key {
		case "AND", "OR", "NOT":
			children, childErr := list(rawChild, joinPath(path, key))
			if childErr != nil || len(children) == 0 {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_WHERE: %s must be a non-empty list", joinPath(path, key))
			}
			if len(children) > s.binder.limits.MaxListItems {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_LIMIT: %s exceeds list item limit", joinPath(path, key))
			}
			bound := make([]policyir.Condition, 0, len(children))
			for index, child := range children {
				condition, bindErr := s.where(model, contract, child, fmt.Sprintf("%s[%d]", joinPath(path, key), index), depth+1)
				if bindErr != nil {
					return policyir.Condition{}, bindErr
				}
				if key == "NOT" {
					condition, bindErr = policyir.NewLogical(modelID, policyir.LogicalNot, []policyir.Condition{condition})
					if bindErr != nil {
						return policyir.Condition{}, bindErr
					}
				}
				bound = append(bound, condition)
			}
			operation := policyir.LogicalAnd
			if key == "OR" {
				operation = policyir.LogicalOr
			}
			condition, bindErr := combine(modelID, operation, bound)
			if bindErr != nil {
				return policyir.Condition{}, bindErr
			}
			conditions = append(conditions, condition)
		default:
			field, exists := fields[key]
			if !exists || !readable(field.contract) {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s is unknown or not readable", joinPath(path, key))
			}
			condition, bindErr := s.fieldCondition(model, contract, field, rawChild, joinPath(path, key), depth+1)
			if bindErr != nil {
				return policyir.Condition{}, bindErr
			}
			conditions = append(conditions, condition)
		}
	}
	condition, err := combine(modelID, policyir.LogicalAnd, conditions)
	if err != nil {
		return policyir.Condition{}, err
	}
	return validateCondition(condition, s.binder.providers)
}

func (s *inputState) fieldCondition(owner compilerir.ModelDeclIR, ownerContract compilerir.ModelContractIR, field fieldBinding, raw any, path string, depth int) (policyir.Condition, error) {
	if raw == nil {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s cannot be null", path)
	}
	if field.model.Kind == compilerir.FieldRelation {
		return s.relationCondition(owner, field, raw, path, depth)
	}
	if field.model.Scalar == nil {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s has no scalar type", path)
	}
	filter, err := object(raw, path)
	if err != nil {
		return policyir.Condition{}, err
	}
	if len(filter) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s must not be empty", path)
	}
	if field.model.Scalar.Type.Kind == compilerir.TypeScalarList {
		return s.listCondition(owner, field, filter, path)
	}
	if field.model.Scalar.Type.Kind == compilerir.TypeJSON {
		return s.jsonCondition(owner, field, filter, path)
	}
	return s.scalarCondition(owner, field, filter, path)
}

func (s *inputState) scalarCondition(owner compilerir.ModelDeclIR, field fieldBinding, filter map[string]any, path string) (policyir.Condition, error) {
	typ, err := bindType(field.model.Scalar.Type, field.model.Scalar.Nullable)
	if err != nil {
		return policyir.Condition{}, err
	}
	mode, err := comparisonMode(filter)
	if err != nil {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.mode: %w", path, err)
	}
	modelID, _ := policyModelID(owner.ID)
	fieldID, _ := policyFieldID(field.model.ID)
	operators := map[string]policyir.OperatorID{
		"equals": policyir.OperatorEqual, "not": policyir.OperatorNotEqual,
		"in": policyir.OperatorIn, "notIn": policyir.OperatorNotIn,
		"lt": policyir.OperatorLessThan, "lte": policyir.OperatorLessThanOrEqual,
		"gt": policyir.OperatorGreaterThan, "gte": policyir.OperatorGreaterThanOrEqual,
		"contains": policyir.OperatorContains, "startsWith": policyir.OperatorStartsWith, "endsWith": policyir.OperatorEndsWith,
	}
	var conditions []policyir.Condition
	for _, name := range sortedKeys(filter) {
		if name == "mode" {
			continue
		}
		raw := filter[name]
		if name == "isNull" {
			flag, ok := raw.(bool)
			if !ok {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.isNull must be Boolean", path)
			}
			operatorID := policyir.OperatorIsNull
			if !flag {
				operatorID = policyir.OperatorIsNotNull
			}
			condition, conditionErr := s.leafScalar(modelID, fieldID, typ, operatorID, mode, policyir.NoOperand())
			if conditionErr != nil {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.isNull: %w", path, conditionErr)
			}
			conditions = append(conditions, condition)
			continue
		}
		operatorID, ok := operators[name]
		if !ok {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: unknown operator %s.%s", path, name)
		}
		var operand policyir.Operand
		if name == "in" || name == "notIn" {
			items, listErr := list(raw, joinPath(path, name))
			if listErr != nil || len(items) > s.binder.limits.MaxListItems {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s must be a bounded list", path, name)
			}
			values := make([]policyir.Value, len(items))
			for index, item := range items {
				value, valueErr := s.value(field.model.Scalar.Type, item)
				if valueErr != nil {
					return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.%s[%d]: %w", path, name, index, valueErr)
				}
				values[index] = value
			}
			operand, err = policyir.ManyOperand(values)
		} else {
			value, valueErr := s.value(field.model.Scalar.Type, raw)
			if valueErr != nil {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.%s: %w", path, name, valueErr)
			}
			operand, err = policyir.OneOperand(value)
		}
		if err != nil {
			return policyir.Condition{}, err
		}
		condition, conditionErr := s.leafScalar(modelID, fieldID, typ, operatorID, mode, operand)
		if conditionErr != nil {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s: %w", path, name, conditionErr)
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s contains no operation", path)
	}
	return combine(modelID, policyir.LogicalAnd, conditions)
}

func (s *inputState) listCondition(owner compilerir.ModelDeclIR, field fieldBinding, filter map[string]any, path string) (policyir.Condition, error) {
	typ, err := bindType(field.model.Scalar.Type, field.model.Scalar.Nullable)
	if err != nil {
		return policyir.Condition{}, err
	}
	element := *field.model.Scalar.Type.Element
	modelID, _ := policyModelID(owner.ID)
	fieldID, _ := policyFieldID(field.model.ID)
	operators := map[string]policyir.OperatorID{"equals": policyir.OperatorListEqual, "has": policyir.OperatorListHas, "hasEvery": policyir.OperatorListHasEvery, "hasSome": policyir.OperatorListHasSome}
	var conditions []policyir.Condition
	for _, name := range sortedKeys(filter) {
		raw := filter[name]
		var operatorID policyir.OperatorID
		var operand policyir.Operand
		switch name {
		case "isNull":
			flag, ok := raw.(bool)
			if !ok {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.isNull must be Boolean", path)
			}
			operatorID = policyir.OperatorListIsNull
			if !flag {
				operatorID = policyir.OperatorListIsNotNull
			}
			operand = policyir.NoOperand()
		case "isEmpty":
			flag, ok := raw.(bool)
			if !ok {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.isEmpty must be Boolean", path)
			}
			operatorID, operand = policyir.OperatorListIsEmpty, policyir.FlagOperand(flag)
		case "has":
			operatorID = operators[name]
			value, valueErr := s.value(element, raw)
			if valueErr != nil {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.has: %w", path, valueErr)
			}
			operand, err = policyir.OneOperand(value)
		case "equals":
			operatorID = operators[name]
			value, valueErr := s.value(field.model.Scalar.Type, raw)
			if valueErr != nil {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.equals: %w", path, valueErr)
			}
			operand, err = policyir.OneOperand(value)
		case "hasEvery", "hasSome":
			operatorID = operators[name]
			items, listErr := list(raw, joinPath(path, name))
			if listErr != nil || len(items) > s.binder.limits.MaxListItems {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s must be a bounded list", path, name)
			}
			values := make([]policyir.Value, len(items))
			for index, item := range items {
				value, valueErr := s.value(element, item)
				if valueErr != nil {
					return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.%s[%d]: %w", path, name, index, valueErr)
				}
				values[index] = value
			}
			operand, err = policyir.ManyOperand(values)
		default:
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: unknown list operator %s.%s", path, name)
		}
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, shapeErr := operator.ValidateShape(operatorID, operator.Shape{Node: policyir.ConditionList, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: s.binder.providers})
		if shapeErr != nil {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s: %w", path, name, shapeErr)
		}
		condition, conditionErr := policyir.NewList(modelID, fieldID, typ, operatorID, operand, requirements)
		if conditionErr != nil {
			return policyir.Condition{}, conditionErr
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s contains no operation", path)
	}
	return combine(modelID, policyir.LogicalAnd, conditions)
}

func (s *inputState) jsonCondition(owner compilerir.ModelDeclIR, field fieldBinding, filter map[string]any, path string) (policyir.Condition, error) {
	typ, err := bindType(field.model.Scalar.Type, field.model.Scalar.Nullable)
	if err != nil {
		return policyir.Condition{}, err
	}
	mode, err := comparisonMode(filter)
	if err != nil {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.mode: %w", path, err)
	}
	jsonPath, err := jsonPath(filter["path"])
	if err != nil {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.path: %w", path, err)
	}
	modelID, _ := policyModelID(owner.ID)
	fieldID, _ := policyFieldID(field.model.ID)
	operators := map[string]policyir.OperatorID{
		"equals": policyir.OperatorJSONEqual, "not": policyir.OperatorJSONNotEqual,
		"lt": policyir.OperatorJSONLessThan, "lte": policyir.OperatorJSONLessThanOrEqual,
		"gt": policyir.OperatorJSONGreaterThan, "gte": policyir.OperatorJSONGreaterThanOrEqual,
		"stringContains": policyir.OperatorJSONStringContains, "stringStartsWith": policyir.OperatorJSONStringStartsWith, "stringEndsWith": policyir.OperatorJSONStringEndsWith,
		"arrayContains": policyir.OperatorJSONArrayContains, "arrayStartsWith": policyir.OperatorJSONArrayStartsWith, "arrayEndsWith": policyir.OperatorJSONArrayEndsWith,
	}
	var conditions []policyir.Condition
	for _, name := range sortedKeys(filter) {
		if name == "path" || name == "mode" {
			continue
		}
		raw := filter[name]
		var operatorID policyir.OperatorID
		var operand policyir.Operand
		if name == "isNull" {
			flag, ok := raw.(bool)
			if !ok {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.isNull must be Boolean", path)
			}
			operatorID = policyir.OperatorJSONIsNull
			if !flag {
				operatorID = policyir.OperatorJSONIsNotNull
			}
			operand = policyir.NoOperand()
		} else {
			var ok bool
			operatorID, ok = operators[name]
			if !ok {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: unknown JSON operator %s.%s", path, name)
			}
			if raw == nil && (name == "equals" || name == "not") {
				operand, err = policyir.JSONNullOperand(policyir.JSONDocumentNull)
			} else {
				value, valueErr := jsonPolicyValue(raw)
				if valueErr != nil {
					return policyir.Condition{}, fmt.Errorf("P5_BIND_VALUE: %s.%s: %w", path, name, valueErr)
				}
				wrapped, wrapErr := policyir.NewJSONValue(value)
				if wrapErr != nil {
					return policyir.Condition{}, wrapErr
				}
				operand, err = policyir.OneOperand(wrapped)
			}
		}
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, shapeErr := operator.ValidateShape(operatorID, operator.Shape{Node: policyir.ConditionJSON, FieldType: typ, Operand: operand, Mode: mode, Path: jsonPath, Providers: s.binder.providers})
		if shapeErr != nil {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s: %w", path, name, shapeErr)
		}
		condition, conditionErr := policyir.NewJSON(modelID, fieldID, typ, operatorID, mode, jsonPath, operand, requirements)
		if conditionErr != nil {
			return policyir.Condition{}, conditionErr
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_FIELD: %s contains no operation", path)
	}
	return combine(modelID, policyir.LogicalAnd, conditions)
}

func (s *inputState) relationCondition(owner compilerir.ModelDeclIR, field fieldBinding, raw any, path string, depth int) (policyir.Condition, error) {
	filter, err := object(raw, path)
	if err != nil || len(filter) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_RELATION: %s must be a non-empty relation filter", path)
	}
	relation, targetID, err := s.relation(owner.ID, field.model)
	if err != nil {
		return policyir.Condition{}, err
	}
	target, targetContract, err := s.binder.model(targetID)
	if err != nil {
		return policyir.Condition{}, err
	}
	modelID, _ := policyModelID(owner.ID)
	fieldID, _ := policyFieldID(field.model.ID)
	relationIDBytes, err := fixedID(string(relation.ID))
	if err != nil {
		return policyir.Condition{}, err
	}
	policyTarget, _ := policyModelID(targetID)
	cardinality := policyir.RelationToOne
	operators := map[string]policyir.OperatorID{"is": policyir.OperatorRelationIs, "isNot": policyir.OperatorRelationIsNot}
	if field.model.Relation.Kind == compilerir.RelationHasMany {
		cardinality = policyir.RelationToMany
		operators = map[string]policyir.OperatorID{"some": policyir.OperatorRelationSome, "every": policyir.OperatorRelationEvery, "none": policyir.OperatorRelationNone}
	}
	var conditions []policyir.Condition
	for _, name := range sortedKeys(filter) {
		operatorID, ok := operators[name]
		if !ok {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: unknown relation operator %s.%s", path, name)
		}
		var child *policyir.Condition
		if filter[name] == nil {
			if cardinality != policyir.RelationToOne {
				return policyir.Condition{}, fmt.Errorf("P5_BIND_RELATION: %s.%s cannot be null", path, name)
			}
			if name == "is" {
				operatorID = policyir.OperatorRelationIsNull
			} else {
				operatorID = policyir.OperatorRelationIsNotNull
			}
		} else {
			bound, childErr := s.where(target, targetContract, filter[name], joinPath(path, name), depth+1)
			if childErr != nil {
				return policyir.Condition{}, childErr
			}
			child = &bound
		}
		requirements, shapeErr := operator.ValidateShape(operatorID, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: s.binder.providers})
		if shapeErr != nil {
			return policyir.Condition{}, fmt.Errorf("P5_BIND_OPERATOR: %s.%s: %w", path, name, shapeErr)
		}
		condition, conditionErr := policyir.NewRelation(modelID, fieldID, policyir.RelationID(relationIDBytes), policyTarget, cardinality, operatorID, child, requirements)
		if conditionErr != nil {
			return policyir.Condition{}, conditionErr
		}
		conditions = append(conditions, condition)
	}
	return combine(modelID, policyir.LogicalAnd, conditions)
}

func (s *inputState) leafScalar(model policyir.ModelID, field policyir.FieldID, typ policyir.TypeRef, operatorID policyir.OperatorID, mode policyir.ComparisonMode, operand policyir.Operand) (policyir.Condition, error) {
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: mode, Providers: s.binder.providers})
	if err != nil {
		return policyir.Condition{}, err
	}
	return policyir.NewScalar(model, field, typ, operatorID, mode, operand, requirements)
}

func (s *inputState) selector(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, raw any, path string, depth int) (readir.Selector, policyir.Condition, error) {
	if err := s.step(path, depth); err != nil {
		return readir.Selector{}, policyir.Condition{}, err
	}
	input, err := object(raw, path)
	if err != nil || len(input) != 1 {
		return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s must contain exactly one selector arm", path)
	}
	name := sortedKeys(input)[0]
	rawValue := input[name]
	if rawValue == nil {
		return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s cannot be null", path, name)
	}
	var selector *compilerir.SelectorContractIR
	for index := range contract.Selectors {
		if contract.Selectors[index].Name == name {
			selector = &contract.Selectors[index]
			break
		}
	}
	if selector == nil || len(selector.Fields) == 0 {
		return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s is unknown", path, name)
	}
	fields := indexFields(model, contract)
	components := make(map[compilerir.FieldID]any, len(selector.Fields))
	if len(selector.Fields) == 1 {
		components[selector.Fields[0]] = rawValue
	} else {
		compound, objectErr := object(rawValue, joinPath(path, name))
		if objectErr != nil || len(compound) != len(selector.Fields) {
			return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s must contain every compound component exactly once", path, name)
		}
		for _, fieldID := range selector.Fields {
			binding, ok := fieldByID(fields, fieldID)
			if !ok {
				return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: selector field %s is absent", fieldID)
			}
			value, present := compound[binding.contract.GraphQLName]
			if !present || value == nil {
				return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s.%s is required", path, name, binding.contract.GraphQLName)
			}
			components[fieldID] = value
		}
		for key := range compound {
			known := false
			for _, fieldID := range selector.Fields {
				binding, _ := fieldByID(fields, fieldID)
				known = known || binding.contract.GraphQLName == key
			}
			if !known {
				return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s.%s is unknown", path, name, key)
			}
		}
	}
	modelID, _ := policyModelID(model.ID)
	selectorFields := make([]policyir.FieldID, len(selector.Fields))
	conditions := make([]policyir.Condition, len(selector.Fields))
	for index, id := range selector.Fields {
		binding, ok := fieldByID(fields, id)
		if !ok || !readable(binding.contract) || binding.model.Scalar == nil {
			return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s contains an unavailable field", path, name)
		}
		fieldID, _ := policyFieldID(id)
		typ, typeErr := bindType(binding.model.Scalar.Type, binding.model.Scalar.Nullable)
		if typeErr != nil {
			return readir.Selector{}, policyir.Condition{}, typeErr
		}
		value, valueErr := s.value(binding.model.Scalar.Type, components[id])
		if valueErr != nil {
			return readir.Selector{}, policyir.Condition{}, fmt.Errorf("P5_BIND_SELECTOR: %s.%s component %s: %w", path, name, binding.contract.GraphQLName, valueErr)
		}
		operand, _ := policyir.OneOperand(value)
		condition, conditionErr := s.leafScalar(modelID, fieldID, typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand)
		if conditionErr != nil {
			return readir.Selector{}, policyir.Condition{}, conditionErr
		}
		selectorFields[index], conditions[index] = fieldID, condition
	}
	keyID, err := golemKeyID(selector.KeyID)
	if err != nil {
		return readir.Selector{}, policyir.Condition{}, err
	}
	boundSelector, err := readir.NewSelector(keyID, selectorFields)
	if err != nil {
		return readir.Selector{}, policyir.Condition{}, err
	}
	predicate, err := combine(modelID, policyir.LogicalAnd, conditions)
	if err != nil {
		return readir.Selector{}, policyir.Condition{}, err
	}
	predicate, err = validateCondition(predicate, s.binder.providers)
	return boundSelector, predicate, err
}

func (s *inputState) orderBy(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, raw any, path string) ([]readir.Order, error) {
	items, err := list(raw, path)
	if err != nil || len(items) == 0 || len(items) > s.binder.limits.MaxListItems {
		return nil, fmt.Errorf("P5_BIND_ORDER: %s must be a non-empty bounded list", path)
	}
	fields := indexFields(model, contract)
	result := make([]readir.Order, len(items))
	seen := map[compilerir.FieldID]bool{}
	for index, item := range items {
		entry, objectErr := object(item, fmt.Sprintf("%s[%d]", path, index))
		if objectErr != nil || len(entry) != 1 {
			return nil, fmt.Errorf("P5_BIND_ORDER: %s[%d] must contain exactly one field", path, index)
		}
		name := sortedKeys(entry)[0]
		field, ok := fields[name]
		if !ok || !readable(field.contract) || field.model.Scalar == nil {
			return nil, fmt.Errorf("P5_BIND_ORDER: %s[%d].%s is unavailable or relational", path, index, name)
		}
		if seen[field.model.ID] {
			return nil, fmt.Errorf("P5_BIND_ORDER: field %s is duplicated", name)
		}
		seen[field.model.ID] = true
		direction, ok := entry[name].(string)
		if !ok {
			return nil, fmt.Errorf("P5_BIND_ORDER: %s[%d].%s must be asc or desc", path, index, name)
		}
		sortDirection := readir.Ascending
		if direction == "desc" {
			sortDirection = readir.Descending
		} else if direction != "asc" {
			return nil, fmt.Errorf("P5_BIND_ORDER: %s[%d].%s must be asc or desc", path, index, name)
		}
		fieldID, _ := policyFieldID(field.model.ID)
		order, orderErr := readir.NewOrder(fieldID, sortDirection)
		if orderErr != nil {
			return nil, orderErr
		}
		result[index] = order
	}
	return result, nil
}

func (s *inputState) distinct(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, raw any) ([]policyir.FieldID, error) {
	items, err := list(raw, "distinct")
	if err != nil || len(items) == 0 || len(items) > s.binder.limits.MaxListItems {
		return nil, fmt.Errorf("P5_BIND_DISTINCT: distinct must be a non-empty bounded list")
	}
	fields := indexFields(model, contract)
	result := make([]policyir.FieldID, len(items))
	seen := map[compilerir.FieldID]bool{}
	for index, item := range items {
		name, ok := item.(string)
		field, exists := fields[name]
		if !ok || !exists || !readable(field.contract) || field.model.Scalar == nil || seen[field.model.ID] {
			return nil, fmt.Errorf("P5_BIND_DISTINCT: item %d is unknown, relational, or duplicate", index)
		}
		seen[field.model.ID] = true
		result[index], _ = policyFieldID(field.model.ID)
	}
	return result, nil
}

func (s *inputState) relation(owner compilerir.ModelID, field compilerir.FieldIR) (compilerir.RelationIR, compilerir.ModelID, error) {
	if field.Relation == nil {
		return compilerir.RelationIR{}, "", fmt.Errorf("P5_BIND_RELATION: field %s has no relation metadata", field.ID)
	}
	relation, ok := s.binder.relations[field.Relation.RelationID]
	if !ok {
		return compilerir.RelationIR{}, "", fmt.Errorf("P5_BIND_RELATION: relation %s is absent", field.Relation.RelationID)
	}
	target := relation.TargetModel
	if field.Relation.Role == compilerir.RelationInverse || relation.TargetModel == owner {
		target = relation.SourceModel
	}
	return relation, target, nil
}

func indexFields(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR) map[string]fieldBinding {
	contracts := map[compilerir.FieldID]compilerir.FieldContractIR{}
	for _, field := range contract.Fields {
		contracts[field.FieldID] = field
	}
	result := map[string]fieldBinding{}
	for _, field := range model.Fields {
		if value, ok := contracts[field.ID]; ok {
			result[value.GraphQLName] = fieldBinding{model: field, contract: value}
		}
	}
	return result
}

func fieldByID(fields map[string]fieldBinding, id compilerir.FieldID) (fieldBinding, bool) {
	for _, field := range fields {
		if field.model.ID == id {
			return field, true
		}
	}
	return fieldBinding{}, false
}

func combine(model policyir.ModelID, operation policyir.LogicalOperator, conditions []policyir.Condition) (policyir.Condition, error) {
	if len(conditions) == 0 {
		return policyir.Condition{}, fmt.Errorf("P5_BIND_CONDITION: no conditions were supplied")
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(model, operation, conditions)
}

func comparisonMode(filter map[string]any) (policyir.ComparisonMode, error) {
	raw, present := filter["mode"]
	if !present {
		return policyir.ComparisonSensitive, nil
	}
	value, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("must be sensitive or insensitive")
	}
	switch value {
	case "sensitive":
		return policyir.ComparisonSensitive, nil
	case "insensitive":
		return policyir.ComparisonASCIIInsensitive, nil
	default:
		return 0, fmt.Errorf("must be sensitive or insensitive")
	}
}

func jsonPath(raw any) (policyir.JSONPath, error) {
	if raw == nil {
		return policyir.NewJSONPath()
	}
	items, err := list(raw, "path")
	if err != nil {
		return policyir.JSONPath{}, err
	}
	segments := make([]policyir.JSONPathSegment, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return policyir.JSONPath{}, fmt.Errorf("segment %d is not String", index)
		}
		if number, parseErr := strconv.ParseUint(text, 10, 64); parseErr == nil && strconv.FormatUint(number, 10) == text {
			segments[index] = policyir.JSONIndexSegment(number)
		} else {
			segment, segmentErr := policyir.JSONKeySegment(text)
			if segmentErr != nil {
				return policyir.JSONPath{}, segmentErr
			}
			segments[index] = segment
		}
	}
	return policyir.NewJSONPath(segments...)
}
