// Package analytics lowers validated GraphQL analytics roots into the same
// frozen requests consumed by generated Go clients. It performs no policy
// decision, SQL rendering, or result aggregation.
package analytics

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/vektah/gqlparser/v2/ast"
)

type Limits struct {
	Bind      graphqlbind.Limits
	MaxGroups int
	ListItems int
}

type Root struct {
	ResponseName string
	Model        compilerir.ModelID
	Operation    golem.AnalyticsOperation
	Request      golem.FrozenAnalyticsRequest
	Slots        []Slot
	MaxGroups    int
	ExplicitTake bool
	TypeName     string
}

type SlotKind uint8

const (
	SlotTypename SlotKind = iota + 1
	SlotCount
	SlotKey
	SlotCountFields
	SlotSum
	SlotAverage
	SlotMinimum
	SlotMaximum
)

type Slot struct {
	Kind         SlotKind
	ResponseName string
	TypeName     string
	Fields       []FieldSlot
}

type FieldSlot struct {
	ResponseName string
	Typename     bool
	Term         golem.FrozenAnalyticsTerm
	Logical      compilerir.LogicalTypeIR
}

type Compiler struct {
	compilation compilerir.CompilationIR
	binder      *graphqlbind.Binder
	limits      Limits
	bindings    map[string]rootBinding
	models      map[compilerir.ModelID]modelBinding
	enumWire    map[compilerir.EnumID]map[string]string
}

type rootBinding struct {
	model     compilerir.ModelID
	operation golem.AnalyticsOperation
}

type modelBinding struct {
	model              compilerir.ModelDeclIR
	contract           compilerir.ModelContractIR
	localDimensions    map[string]termBinding
	relationDimensions map[string]termBinding
	measureFields      map[string]compilerir.FieldIR
}

type termBinding struct {
	term    golem.FrozenAnalyticsTerm
	logical compilerir.LogicalTypeIR
}

func New(compilation compilerir.CompilationIR, limits Limits) (*Compiler, error) {
	if limits.Bind.MaxInputDepth <= 0 {
		limits.Bind.MaxInputDepth = 32
	}
	if limits.Bind.MaxInputNodes <= 0 {
		limits.Bind.MaxInputNodes = 16_384
	}
	if limits.Bind.MaxListItems <= 0 {
		limits.Bind.MaxListItems = 4_096
	}
	binder, err := graphqlbind.New(compilation, limits.Bind)
	if err != nil {
		return nil, err
	}
	if limits.MaxGroups <= 0 {
		limits.MaxGroups = 10_000
	}
	if limits.ListItems <= 0 {
		limits.ListItems = limits.Bind.MaxListItems
		if limits.ListItems <= 0 {
			limits.ListItems = 1_000
		}
	}
	compiler := &Compiler{
		compilation: compilation, binder: binder, limits: limits,
		bindings: map[string]rootBinding{}, models: map[compilerir.ModelID]modelBinding{},
		enumWire: map[compilerir.EnumID]map[string]string{},
	}
	models := map[compilerir.ModelID]compilerir.ModelDeclIR{}
	contracts := map[compilerir.ModelID]compilerir.ModelContractIR{}
	relations := map[compilerir.RelationID]compilerir.RelationIR{}
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	for _, contract := range compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	for _, relation := range compilation.Model.Relations {
		relations[relation.ID] = relation
	}
	for _, enum := range compilation.Contract.Enums {
		wire := map[string]string{}
		for _, value := range enum.Values {
			for _, modelEnum := range compilation.Model.Enums {
				if modelEnum.ID != enum.EnumID {
					continue
				}
				for _, modelValue := range modelEnum.Values {
					if modelValue.ID == value.ValueID {
						wire[value.GraphQLName] = modelValue.WireValue
					}
				}
			}
		}
		compiler.enumWire[enum.EnumID] = wire
	}
	for _, contract := range compilation.Contract.Models {
		if !contract.Exposed {
			continue
		}
		enabled := map[compilerir.Operation]bool{}
		for _, operation := range contract.Operations {
			enabled[operation] = true
		}
		if !enabled[compilerir.OperationAggregate] && !enabled[compilerir.OperationGroupBy] && !enabled[compilerir.OperationRelationGroupBy] {
			continue
		}
		if contract.Aggregation == nil || !contract.Aggregation.Enabled {
			return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_CONTRACT: model %s has no analytics contract", contract.GraphQLName)
		}
		binding, bindErr := buildModelBinding(models[contract.ModelID], contract, models, contracts, relations)
		if bindErr != nil {
			return nil, bindErr
		}
		compiler.models[contract.ModelID] = binding
		roots := []struct {
			enabled bool
			name    string
			kind    golem.AnalyticsOperation
		}{
			{enabled[compilerir.OperationAggregate], contract.Roots.Aggregate, golem.AnalyticsAggregate},
			{enabled[compilerir.OperationGroupBy], contract.Roots.GroupBy, golem.AnalyticsGroupBy},
			{enabled[compilerir.OperationRelationGroupBy], contract.Roots.RelationGroupBy, golem.AnalyticsRelationGroupBy},
		}
		for _, root := range roots {
			if !root.enabled {
				continue
			}
			if root.name == "" {
				return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_ROOT: model %s has an empty root", contract.GraphQLName)
			}
			if _, duplicate := compiler.bindings[root.name]; duplicate {
				return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_ROOT: root %q is duplicated", root.name)
			}
			compiler.bindings[root.name] = rootBinding{model: contract.ModelID, operation: root.kind}
		}
	}
	return compiler, nil
}

func (c *Compiler) groupMaximum(contract compilerir.ModelContractIR) int {
	maximum := int(contract.Aggregation.GraphQLMaxGroups)
	if maximum <= 0 || c.limits.MaxGroups < maximum {
		return c.limits.MaxGroups
	}
	return maximum
}

func (c *Compiler) GroupRootMaxima() map[string]int {
	maxima := map[string]int{}
	for name, binding := range c.bindings {
		if binding.operation == golem.AnalyticsGroupBy || binding.operation == golem.AnalyticsRelationGroupBy {
			maxima[name] = c.groupMaximum(c.models[binding.model].contract)
		}
	}
	return maxima
}

func buildModelBinding(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, models map[compilerir.ModelID]compilerir.ModelDeclIR, contracts map[compilerir.ModelID]compilerir.ModelContractIR, relations map[compilerir.RelationID]compilerir.RelationIR) (modelBinding, error) {
	result := modelBinding{model: model, contract: contract, localDimensions: map[string]termBinding{}, relationDimensions: map[string]termBinding{}, measureFields: map[string]compilerir.FieldIR{}}
	fields := map[compilerir.FieldID]compilerir.FieldIR{}
	fieldContracts := map[compilerir.FieldID]compilerir.FieldContractIR{}
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	for _, field := range contract.Fields {
		fieldContracts[field.FieldID] = field
	}
	allowedDimensions := identitySet(contract.Aggregation.Dimensions)
	allowedMeasures := identitySet(contract.Aggregation.Measures)
	allDimensions, allMeasures := !contract.Aggregation.DimensionsExplicit, !contract.Aggregation.MeasuresExplicit
	if contract.Aggregation.DimensionsExplicit && len(allowedDimensions) == 0 {
		return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_CONTRACT: model %s has an explicitly empty dimension allowlist", contract.GraphQLName)
	}
	if contract.Aggregation.MeasuresExplicit && len(allowedMeasures) == 0 {
		return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_CONTRACT: model %s has an explicitly empty measure allowlist", contract.GraphQLName)
	}
	modelID, err := publicModelID(model.ID)
	if err != nil {
		return result, err
	}
	localNames := map[string]bool{}
	for _, field := range model.Fields {
		fc, visible := fieldContracts[field.ID]
		if !visible || !available(fc) || field.Scalar == nil || !groupable(field.Scalar.Type.Kind) {
			continue
		}
		localNames[fc.GraphQLName] = true
		fieldID, idErr := publicFieldID(field.ID)
		if idErr != nil {
			return result, idErr
		}
		if allDimensions || allowedDimensions[field.ID] {
			result.localDimensions[fc.GraphQLName] = termBinding{term: golem.FrozenAnalyticsTerm{Model: modelID, Field: fieldID}, logical: field.Scalar.Type}
		}
		if allMeasures || allowedMeasures[field.ID] {
			result.measureFields[fc.GraphQLName] = field
		}
	}
	if !allDimensions && len(result.localDimensions) != len(allowedDimensions) {
		return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_CONTRACT: model %s dimension allowlist contains an unavailable field", contract.GraphQLName)
	}
	if !allMeasures && len(result.measureFields) != len(allowedMeasures) {
		return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_CONTRACT: model %s measure allowlist contains an unavailable field", contract.GraphQLName)
	}
	for _, dimension := range contract.Aggregation.RelationDimensions {
		if localNames[dimension.Name] {
			return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RELATION: %s collides with a local GraphQL field", dimension.Name)
		}
		if _, duplicate := result.relationDimensions[dimension.Name]; duplicate {
			return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RELATION: %s is duplicated", dimension.Name)
		}
		current := model.ID
		for _, relationID := range dimension.Path {
			relation, ok := relations[relationID]
			if !ok || relation.SourceModel != current {
				return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RELATION: %s has an invalid path", dimension.Name)
			}
			current = relation.TargetModel
		}
		terminal := models[current]
		terminalContract, exposedModel := contracts[current]
		if !exposedModel || !terminalContract.Exposed {
			return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RELATION: %s terminal model is not GraphQL-visible", dimension.Name)
		}
		var field compilerir.FieldIR
		for _, candidate := range terminal.Fields {
			if candidate.ID == dimension.TerminalField {
				field = candidate
				break
			}
		}
		var fc compilerir.FieldContractIR
		for _, candidate := range terminalContract.Fields {
			if candidate.FieldID == dimension.TerminalField {
				fc = candidate
				break
			}
		}
		if field.Scalar == nil || !available(fc) || !groupable(field.Scalar.Type.Kind) {
			return result, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RELATION: %s terminal field is unavailable", dimension.Name)
		}
		fieldID, idErr := publicFieldID(field.ID)
		if idErr != nil {
			return result, idErr
		}
		path := make([]golem.RelationID, len(dimension.Path))
		for index, relationID := range dimension.Path {
			path[index], idErr = publicRelationID(relationID)
			if idErr != nil {
				return result, idErr
			}
		}
		result.relationDimensions[dimension.Name] = termBinding{term: golem.FrozenAnalyticsTerm{Model: modelID, Field: fieldID, RelationName: dimension.Name, RelationPath: path}, logical: field.Scalar.Type}
	}
	return result, nil
}

func (c *Compiler) Compile(field *ast.Field, fragments ast.FragmentDefinitionList, variables map[string]any) (Root, bool, error) {
	if c == nil || field == nil {
		return Root{}, false, nil
	}
	binding, ok := c.bindings[field.Name]
	if !ok {
		return Root{}, false, nil
	}
	root, err := c.compile(field, binding, fragments, variables)
	if err != nil {
		return Root{}, true, compileerr.On(err, c.models[binding.model].contract.GraphQLName)
	}
	return root, true, nil
}

func (c *Compiler) compile(field *ast.Field, binding rootBinding, fragments ast.FragmentDefinitionList, variables map[string]any) (Root, error) {
	model := c.models[binding.model]
	arguments, err := argumentValues(field, variables)
	if err != nil {
		return Root{}, err
	}
	selection, selectedMeasures, selectedKeys, err := c.compileSelection(model, binding.operation, field.SelectionSet, fragments, variables)
	if err != nil {
		return Root{}, err
	}
	request := golem.RuntimeAnalyticsRequestInput{Operation: binding.operation}
	request.Model, err = publicModelID(binding.model)
	if err != nil {
		return Root{}, err
	}
	root := Root{Model: binding.model, Operation: binding.operation, Slots: selection, TypeName: outputTypeName(model.contract.GraphQLName, binding.operation)}
	if binding.operation == golem.AnalyticsAggregate {
		if err := allowedArguments(arguments, "where"); err != nil {
			return Root{}, err
		}
		if len(selectedMeasures) == 0 {
			return Root{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedAggregateNeedsMeasure)
		}
		request.Measures = selectedMeasures
	} else {
		if err := allowedArguments(arguments, "by", "where", "having", "orderBy", "skip", "take"); err != nil {
			return Root{}, err
		}
		by, present := arguments["by"]
		if !present {
			return Root{}, compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path("by"))
		}
		dimensions, byNames, bindErr := bindDimensions(model, binding.operation, by)
		if bindErr != nil {
			return Root{}, compileerr.At(bindErr, "by")
		}
		for _, name := range sortedBools(selectedKeys) {
			if !byNames[name] {
				return Root{}, keyNotInBy(name)
			}
		}
		request.Dimensions, request.Measures = dimensions, selectedMeasures
		if raw, present := arguments["having"]; present {
			if raw == nil {
				return Root{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path("having"))
			}
			having, havingErr := c.bindHaving(model, binding.operation, byNames, raw, 1)
			if havingErr != nil {
				return Root{}, compileerr.At(havingErr, "having")
			}
			request.Having = &having
		}
		if raw, present := arguments["orderBy"]; present {
			orders, orderErr := c.bindOrder(model, binding.operation, raw)
			if orderErr != nil {
				return Root{}, compileerr.At(orderErr, "orderBy")
			}
			for _, order := range orders {
				if order.Term.Operator == 0 {
					name := termName(model, binding.operation, order.Term)
					if !byNames[name] {
						return Root{}, keyNotInBy(name)
					}
				}
			}
			request.OrderBy = orders
		}
		if raw, present := arguments["skip"]; present {
			value, valueErr := exactInt(raw)
			if valueErr != nil {
				return Root{}, compileerr.At(valueErr, "skip")
			}
			if value < 0 {
				return Root{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerSkip, compileerr.Path("skip"))
			}
			request.Skip = &value
		}
		maximum := c.groupMaximum(model.contract)
		root.MaxGroups = maximum
		if raw, present := arguments["take"]; present {
			value, valueErr := exactInt(raw)
			if valueErr != nil {
				return Root{}, compileerr.At(valueErr, "take")
			}
			if value == 0 {
				return Root{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerTakeNonZero, compileerr.Path("take"))
			}
			if abs(value) > maximum {
				return Root{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerTake, compileerr.Path("take"), compileerr.Bounds(-maximum, maximum))
			}
			request.Take, root.ExplicitTake = &value, true
		} else {
			probe := maximum + 1
			request.Take = &probe
		}
	}
	if raw, present := arguments["where"]; present {
		if raw == nil {
			return Root{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path("where"))
		}
		condition, whereErr := c.binder.WhereAt(binding.model, raw, "where")
		if whereErr != nil {
			return Root{}, whereErr
		}
		where, freezeErr := c.binder.FreezePredicate(condition)
		if freezeErr != nil {
			return Root{}, freezeErr
		}
		request.Where = &where
	}
	frozen, err := golem.RuntimeFreezeAnalyticsRequest(request)
	if err != nil {
		return Root{}, compileerr.Ensure(err)
	}
	root.Request = frozen
	return root, nil
}

func (c *Compiler) compileSelection(model modelBinding, operation golem.AnalyticsOperation, set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any) ([]Slot, []golem.FrozenAnalyticsTerm, map[string]bool, error) {
	fields, err := expand(set, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(fields) == 0 {
		return nil, nil, nil, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	var slots []Slot
	measureSet := map[string]golem.FrozenAnalyticsTerm{}
	selectedKeys := map[string]bool{}
	for _, field := range fields {
		slot := Slot{ResponseName: responseName(field)}
		switch field.Name {
		case "__typename":
			if len(field.SelectionSet) != 0 {
				return nil, nil, nil, forbidsSelection(field.Name)
			}
			slot.Kind, slot.TypeName = SlotTypename, outputTypeName(model.contract.GraphQLName, operation)
		case "count":
			if len(field.SelectionSet) != 0 {
				return nil, nil, nil, forbidsSelection(field.Name)
			}
			slot.Kind = SlotCount
			term := golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Operator: golem.AggregateCountAll}
			measureSet[golem.RuntimeAnalyticsTermKey(term)] = term
		case "key":
			if operation == golem.AnalyticsAggregate {
				return nil, nil, nil, unavailableField(field.Name)
			}
			slot.Kind = SlotKey
			if operation == golem.AnalyticsRelationGroupBy {
				slot.TypeName = model.contract.GraphQLName + "RelationGroupKey"
			} else {
				slot.TypeName = model.contract.GraphQLName + "GroupKey"
			}
			slot.Fields, err = c.compileKeyFields(model, operation, field.SelectionSet, fragments, variables, selectedKeys)
		case "countFields", "sum", "avg", "min", "max":
			slot.Kind = map[string]SlotKind{"countFields": SlotCountFields, "sum": SlotSum, "avg": SlotAverage, "min": SlotMinimum, "max": SlotMaximum}[field.Name]
			slot.TypeName = model.contract.GraphQLName + map[string]string{"countFields": "Count", "sum": "Sum", "avg": "Avg", "min": "Min", "max": "Max"}[field.Name] + "Aggregate"
			slot.Fields, err = c.compileMeasureFields(model, field.Name, field.SelectionSet, fragments, variables, measureSet)
		default:
			return nil, nil, nil, unavailableField(field.Name)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		slots = append(slots, slot)
	}
	keys := make([]string, 0, len(measureSet))
	for key := range measureSet {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	measures := make([]golem.FrozenAnalyticsTerm, len(keys))
	for index, key := range keys {
		measures[index] = measureSet[key]
	}
	return slots, measures, selectedKeys, nil
}

func (c *Compiler) compileKeyFields(model modelBinding, operation golem.AnalyticsOperation, set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, selected map[string]bool) ([]FieldSlot, error) {
	fields, err := expand(set, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	result := make([]FieldSlot, len(fields))
	for index, field := range fields {
		result[index].ResponseName = responseName(field)
		if field.Name == "__typename" {
			result[index].Typename = true
			continue
		}
		term, ok := model.localDimensions[field.Name]
		if !ok && operation == golem.AnalyticsRelationGroupBy {
			term, ok = model.relationDimensions[field.Name]
		}
		if !ok || len(field.SelectionSet) != 0 {
			return nil, unavailableField(field.Name)
		}
		result[index].Term, result[index].Logical = term.term, term.logical
		selected[field.Name] = true
	}
	return result, nil
}

func (c *Compiler) compileMeasureFields(model modelBinding, category string, set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, selected map[string]golem.FrozenAnalyticsTerm) ([]FieldSlot, error) {
	fields, err := expand(set, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	result := make([]FieldSlot, len(fields))
	for index, field := range fields {
		result[index].ResponseName = responseName(field)
		if field.Name == "__typename" {
			result[index].Typename = true
			continue
		}
		stored, ok := model.measureFields[field.Name]
		if !ok || stored.Scalar == nil || len(field.SelectionSet) != 0 {
			return nil, unavailableField(field.Name)
		}
		operator, valid := categoryOperator(category, stored.Scalar.Type.Kind)
		if !valid {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(compileerr.Join(category, field.Name)))
		}
		term := golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Field: mustPublicFieldID(stored.ID), Operator: operator}
		result[index].Term, result[index].Logical = term, stored.Scalar.Type
		selected[golem.RuntimeAnalyticsTermKey(term)] = term
	}
	return result, nil
}

func bindDimensions(model modelBinding, operation golem.AnalyticsOperation, raw any) ([]golem.FrozenAnalyticsTerm, map[string]bool, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(items) == 0 {
		return nil, nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
	}
	result := make([]golem.FrozenAnalyticsTerm, len(items))
	names := map[string]bool{}
	relations := 0
	for index, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Path(compileerr.Index("", index)), compileerr.Type("String"))
		}
		if names[name] {
			return nil, nil, compileerr.New(compileerr.KindDuplicate, compileerr.DuplicateField)
		}
		names[name] = true
		binding, exists := model.localDimensions[name]
		if !exists && operation == golem.AnalyticsRelationGroupBy {
			binding, exists = model.relationDimensions[name]
			if exists {
				relations++
			}
		}
		if !exists {
			return nil, nil, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableInput, compileerr.Path(compileerr.Index("", index)))
		}
		result[index] = binding.term
	}
	if operation == golem.AnalyticsRelationGroupBy && relations == 0 {
		return nil, nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedRelationGroupNeedsDimension)
	}
	return result, names, nil
}

func (c *Compiler) bindHaving(model modelBinding, operation golem.AnalyticsOperation, grouped map[string]bool, raw any, depth int) (golem.FrozenGroupPredicate, error) {
	if depth > c.limits.Bind.MaxInputDepth {
		return golem.FrozenGroupPredicate{}, limitError("MaxInputDepth", c.limits.Bind.MaxInputDepth)
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return golem.FrozenGroupPredicate{}, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(object) == 0 {
		return golem.FrozenGroupPredicate{}, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
	}
	var predicates []golem.FrozenGroupPredicate
	for _, name := range sortedKeys(object) {
		value := object[name]
		switch name {
		case "AND", "OR":
			items, ok := value.([]any)
			if !ok {
				return golem.FrozenGroupPredicate{}, compileerr.At(compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape), name)
			}
			if len(items) == 0 {
				return golem.FrozenGroupPredicate{}, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone, compileerr.Path(name))
			}
			if len(items) > c.limits.ListItems {
				return golem.FrozenGroupPredicate{}, limitError("MaxListItems", c.limits.ListItems)
			}
			children := make([]golem.FrozenGroupPredicate, len(items))
			for index, item := range items {
				child, err := c.bindHaving(model, operation, grouped, item, depth+1)
				if err != nil {
					return golem.FrozenGroupPredicate{}, compileerr.At(err, compileerr.Index(name, index))
				}
				children[index] = child
			}
			kind := golem.RuntimeGroupAnd
			if name == "OR" {
				kind = golem.RuntimeGroupOr
			}
			predicates = append(predicates, golem.FrozenGroupPredicate{Kind: kind, Children: children})
		case "NOT":
			child, err := c.bindHaving(model, operation, grouped, value, depth+1)
			if err != nil {
				return golem.FrozenGroupPredicate{}, compileerr.At(err, name)
			}
			predicates = append(predicates, golem.FrozenGroupPredicate{Kind: golem.RuntimeGroupNot, Children: []golem.FrozenGroupPredicate{child}})
		case "key":
			bound, err := c.bindHavingFields(model, operation, grouped, "key", value)
			if err != nil {
				return golem.FrozenGroupPredicate{}, compileerr.At(err, name)
			}
			predicates = append(predicates, bound...)
		case "count":
			term := golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Operator: golem.AggregateCountAll}
			bound, err := c.bindHavingLeaf(term, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, true, value)
			if err != nil {
				return golem.FrozenGroupPredicate{}, compileerr.At(err, name)
			}
			predicates = append(predicates, bound...)
		case "countFields", "sum", "avg", "min", "max":
			bound, err := c.bindHavingFields(model, operation, grouped, name, value)
			if err != nil {
				return golem.FrozenGroupPredicate{}, compileerr.At(err, name)
			}
			predicates = append(predicates, bound...)
		default:
			return golem.FrozenGroupPredicate{}, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableInput, compileerr.Path(name))
		}
	}
	return combinePredicates(predicates)
}

func (c *Compiler) bindHavingFields(model modelBinding, operation golem.AnalyticsOperation, grouped map[string]bool, category string, raw any) ([]golem.FrozenGroupPredicate, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(object) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
	}
	var result []golem.FrozenGroupPredicate
	for _, name := range sortedKeys(object) {
		var binding termBinding
		var valid bool
		countLike := false
		if category == "key" {
			binding, valid = model.localDimensions[name]
			if !valid && operation == golem.AnalyticsRelationGroupBy {
				binding, valid = model.relationDimensions[name]
			}
		} else {
			field, exists := model.measureFields[name]
			if exists && field.Scalar != nil {
				operator, supported := categoryOperator(category, field.Scalar.Type.Kind)
				if supported {
					binding = termBinding{term: golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Field: mustPublicFieldID(field.ID), Operator: operator}, logical: field.Scalar.Type}
					valid, countLike = true, operator == golem.AggregateCountField
				}
			}
		}
		if !valid {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(name))
		}
		if category == "key" && !grouped[name] {
			return nil, keyNotInBy(name)
		}
		predicates, err := c.bindHavingLeaf(binding.term, binding.logical, countLike, object[name])
		if err != nil {
			return nil, compileerr.At(err, name)
		}
		result = append(result, predicates...)
	}
	return result, nil
}

func (c *Compiler) bindHavingLeaf(term golem.FrozenAnalyticsTerm, logical compilerir.LogicalTypeIR, countLike bool, raw any) ([]golem.FrozenGroupPredicate, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(object) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
	}
	mode := golem.FrozenComparisonSensitive
	if rawMode, present := object["mode"]; present {
		switch fmt.Sprint(rawMode) {
		case "sensitive":
			mode = golem.FrozenComparisonSensitive
		case "insensitive":
			mode = golem.FrozenComparisonASCIIInsensitive
		default:
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueNotOneOf, compileerr.Path("mode"))
		}
	}
	var result []golem.FrozenGroupPredicate
	for _, name := range sortedKeys(object) {
		operator := map[string]string{"equals": "eq", "not": "ne", "lt": "lt", "lte": "lte", "gt": "gt", "gte": "gte"}[name]
		if name == "mode" {
			continue
		}
		if name == "contains" || name == "startsWith" || name == "endsWith" {
			if logical.Kind != compilerir.TypeString || countLike {
				return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(name))
			}
			text, ok := object[name].(string)
			if !ok {
				return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Path(name), compileerr.Type("String"))
			}
			result = append(result, golem.FrozenGroupPredicate{Kind: golem.RuntimeGroupCompare, Term: term, Operator: name, Value: text, Mode: mode})
			continue
		}
		if name == "isNull" {
			flag, ok := object[name].(bool)
			if !ok {
				return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Path(name), compileerr.Type("Boolean"))
			}
			operator = "isNull"
			if !flag {
				operator = "isNotNull"
			}
			result = append(result, golem.FrozenGroupPredicate{Kind: golem.RuntimeGroupCompare, Term: term, Operator: operator, Value: true})
			continue
		}
		if operator == "" {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(name))
		}
		if _, hasMode := object["mode"]; hasMode {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path("mode"))
		}
		if object[name] == nil {
			return nil, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(name))
		}
		value, err := c.coerceTermValue(term, logical, countLike, object[name])
		if err != nil {
			return nil, compileerr.At(err, name)
		}
		result = append(result, golem.FrozenGroupPredicate{Kind: golem.RuntimeGroupCompare, Term: term, Operator: operator, Value: value})
	}
	return result, nil
}

func (c *Compiler) bindOrder(model modelBinding, operation golem.AnalyticsOperation, raw any) ([]golem.FrozenAnalyticsOrder, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(items) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
	}
	if len(items) > c.limits.ListItems {
		return nil, limitError("MaxListItems", c.limits.ListItems)
	}
	result := make([]golem.FrozenAnalyticsOrder, len(items))
	seen := map[string]bool{}
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok || len(object) != 1 {
			return nil, compileerr.New(compileerr.KindExactlyOne, compileerr.DetailNone, compileerr.Path(compileerr.Index("", index)))
		}
		category := sortedKeys(object)[0]
		if category == "count" {
			descending, err := sortDirection(object[category])
			if err != nil {
				return nil, err
			}
			term := golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Operator: golem.AggregateCountAll}
			if duplicateOrder(seen, term) {
				return nil, compileerr.New(compileerr.KindDuplicate, compileerr.DuplicateField)
			}
			result[index] = golem.FrozenAnalyticsOrder{Term: term, Descending: descending}
			continue
		}
		nested, ok := object[category].(map[string]any)
		if !ok || len(nested) != 1 {
			return nil, compileerr.New(compileerr.KindExactlyOne, compileerr.DetailNone, compileerr.Path(compileerr.Join(compileerr.Index("", index), category)))
		}
		name := sortedKeys(nested)[0]
		descending, err := sortDirection(nested[name])
		if err != nil {
			return nil, err
		}
		var term golem.FrozenAnalyticsTerm
		valid := false
		if category == "key" {
			binding, exists := model.localDimensions[name]
			if !exists && operation == golem.AnalyticsRelationGroupBy {
				binding, exists = model.relationDimensions[name]
			}
			if exists {
				term, valid = binding.term, true
			}
		} else {
			field, exists := model.measureFields[name]
			if exists && field.Scalar != nil {
				operator, supported := categoryOperator(category, field.Scalar.Type.Kind)
				if supported {
					term = golem.FrozenAnalyticsTerm{Model: mustPublicModelID(model.model.ID), Field: mustPublicFieldID(field.ID), Operator: operator}
					valid = true
				}
			}
		}
		if !valid {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(compileerr.Join(compileerr.Index("", index), compileerr.Join(category, name))))
		}
		if duplicateOrder(seen, term) {
			return nil, compileerr.New(compileerr.KindDuplicate, compileerr.DuplicateField)
		}
		result[index] = golem.FrozenAnalyticsOrder{Term: term, Descending: descending}
	}
	return result, nil
}

func (c *Compiler) coerceTermValue(term golem.FrozenAnalyticsTerm, logical compilerir.LogicalTypeIR, countLike bool, raw any) (any, error) {
	if countLike || term.Operator == golem.AggregateCountAll {
		exact, err := golem.ParseExactInteger(exactText(raw))
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueMalformed, compileerr.Type("BigInt"))
		}
		value, ok := exact.Int64()
		if !ok {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueOutOfRange, compileerr.Type("BigInt"))
		}
		return value, nil
	}
	if term.Operator == golem.AggregateSum && (logical.Kind == compilerir.TypeInt16 || logical.Kind == compilerir.TypeInt32 || logical.Kind == compilerir.TypeInt64) {
		exact, err := golem.ParseExactInteger(exactText(raw))
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueMalformed, compileerr.Type("BigInt"))
		}
		return exact, nil
	}
	if (term.Operator == golem.AggregateSum || term.Operator == golem.AggregateAverage) && logical.Kind == compilerir.TypeDecimal {
		exact, err := golem.ParseExactDecimal(exactText(raw))
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueMalformed, compileerr.Type("Decimal"))
		}
		return exact, nil
	}
	if term.Operator == golem.AggregateAverage {
		return graphqlscalar.Float(raw, 64)
	}
	if logical.Kind == compilerir.TypeEnum && logical.EnumID != nil {
		wire, ok := c.enumWire[*logical.EnumID][fmt.Sprint(raw)]
		if !ok {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueNotOneOf)
		}
		return wire, nil
	}
	typ, err := graphQLType(logical)
	if err != nil {
		return nil, err
	}
	value, err := selectset.CoerceExtensionValue(c.compilation, typ, raw, c.limits.ListItems)
	if err != nil {
		return nil, compileerr.Ensure(err)
	}
	return value, nil
}

func (c *Compiler) Encode(root Root, rows [][]golem.RuntimeAnalyticsCell) (any, error) {
	if root.Operation == golem.AnalyticsAggregate {
		if len(rows) != 1 {
			return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RESULT: aggregate returned %d rows", len(rows))
		}
		return c.encodeRow(root, rows[0])
	}
	if !root.ExplicitTake && len(rows) > root.MaxGroups {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", root.Request.ModelID(), golem.FieldID{}, fmt.Sprintf("analytics result exceeds %d groups", root.MaxGroups), nil)
	}
	result := make([]any, len(rows))
	for index, row := range rows {
		value, err := c.encodeRow(root, row)
		if err != nil {
			return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RESULT: row %d: %w", index, err)
		}
		result[index] = value
	}
	return result, nil
}

func (c *Compiler) encodeRow(root Root, cells []golem.RuntimeAnalyticsCell) (map[string]any, error) {
	values := map[string]golem.RuntimeAnalyticsCell{}
	for _, cell := range cells {
		key, _, _ := golem.RuntimeAnalyticsCellValue(cell)
		values[key] = cell
	}
	result := map[string]any{}
	for _, slot := range root.Slots {
		if slot.Kind == SlotTypename {
			result[slot.ResponseName] = slot.TypeName
			continue
		}
		if slot.Kind == SlotCount {
			term := golem.FrozenAnalyticsTerm{Model: root.Request.ModelID(), Operator: golem.AggregateCountAll}
			value, err := c.encodeCell(values[golem.RuntimeAnalyticsTermKey(term)], term, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64})
			if err != nil {
				return nil, err
			}
			result[slot.ResponseName] = value
			continue
		}
		container := map[string]any{}
		for _, field := range slot.Fields {
			if field.Typename {
				container[field.ResponseName] = slot.TypeName
				continue
			}
			value, err := c.encodeCell(values[golem.RuntimeAnalyticsTermKey(field.Term)], field.Term, field.Logical)
			if err != nil {
				return nil, err
			}
			container[field.ResponseName] = value
		}
		result[slot.ResponseName] = container
	}
	return result, nil
}

func (c *Compiler) encodeCell(cell golem.RuntimeAnalyticsCell, term golem.FrozenAnalyticsTerm, logical compilerir.LogicalTypeIR) (any, error) {
	_, state, value := golem.RuntimeAnalyticsCellValue(cell)
	if state == golem.ReadNull {
		return nil, nil
	}
	if state != golem.ReadPresent {
		return nil, fmt.Errorf("selected analytics cell is absent")
	}
	if term.Operator == golem.AggregateCountAll || term.Operator == golem.AggregateCountField {
		count, ok := value.(int64)
		if !ok {
			return nil, fmt.Errorf("count cell has value %T", value)
		}
		return strconv.FormatInt(count, 10), nil
	}
	if term.Operator == golem.AggregateSum && (logical.Kind == compilerir.TypeInt16 || logical.Kind == compilerir.TypeInt32 || logical.Kind == compilerir.TypeInt64) {
		exact, ok := value.(golem.ExactInteger)
		if !ok {
			return nil, fmt.Errorf("integer sum has value %T", value)
		}
		return exact.String(), nil
	}
	if (term.Operator == golem.AggregateSum || term.Operator == golem.AggregateAverage) && logical.Kind == compilerir.TypeDecimal {
		exact, ok := value.(golem.ExactDecimal)
		if !ok {
			return nil, fmt.Errorf("decimal aggregate has value %T", value)
		}
		return exact.String(), nil
	}
	if term.Operator == golem.AggregateAverage {
		return graphqlscalar.Float(value, 64)
	}
	return c.encodeLogical(logical, value)
}

func (c *Compiler) encodeLogical(logical compilerir.LogicalTypeIR, value any) (any, error) {
	switch logical.Kind {
	case compilerir.TypeBool, compilerir.TypeString:
		return value, nil
	case compilerir.TypeInt16:
		if typed, ok := value.(int16); ok {
			return int32(typed), nil
		}
	case compilerir.TypeInt32:
		if typed, ok := value.(int32); ok {
			return typed, nil
		}
	case compilerir.TypeInt64:
		if typed, ok := value.(int64); ok {
			return strconv.FormatInt(typed, 10), nil
		}
	case compilerir.TypeFloat32:
		if typed, ok := value.(float32); ok {
			return graphqlscalar.Float(float64(typed), 32)
		}
	case compilerir.TypeFloat64:
		return graphqlscalar.Float(value, 64)
	case compilerir.TypeDecimal:
		if typed, ok := value.(golem.Decimal); ok {
			return typed.String(), nil
		}
	case compilerir.TypeUUID:
		if typed, ok := value.(golem.UUID); ok {
			return typed.String(), nil
		}
	case compilerir.TypeDate:
		if typed, ok := value.(golem.Date); ok {
			return typed.String(), nil
		}
	case compilerir.TypeTime:
		if typed, ok := value.(golem.Time); ok {
			return typed.String(), nil
		}
	case compilerir.TypeDateTime:
		if typed, ok := value.(time.Time); ok {
			return graphqlscalar.SerializeDateTime(typed), nil
		}
	case compilerir.TypeEnum:
		if logical.EnumID != nil {
			wire := fmt.Sprint(value)
			for name, candidate := range c.enumWire[*logical.EnumID] {
				if candidate == wire {
					return name, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("analytics value %T does not match %s", value, logical.Kind)
}

func expand(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, stack map[string]bool) ([]*ast.Field, error) {
	var result []*ast.Field
	byResponse := map[string]int{}
	var visit func(ast.SelectionSet) error
	visit = func(current ast.SelectionSet) error {
		for _, selection := range current {
			switch value := selection.(type) {
			case *ast.Field:
				include, err := included(value.Directives, variables)
				if err != nil || !include {
					if err != nil {
						return err
					}
					continue
				}
				response := responseName(value)
				if previous, duplicate := byResponse[response]; duplicate {
					if result[previous].Name != value.Name {
						return compileerr.New(compileerr.KindFragmentInvalid, compileerr.FragmentIncompatible)
					}
					result[previous].SelectionSet = append(result[previous].SelectionSet, value.SelectionSet...)
					continue
				}
				copy := *value
				copy.SelectionSet = append(ast.SelectionSet(nil), value.SelectionSet...)
				byResponse[response] = len(result)
				result = append(result, &copy)
			case *ast.InlineFragment:
				include, err := included(value.Directives, variables)
				if err != nil {
					return err
				}
				if include {
					if err := visit(value.SelectionSet); err != nil {
						return err
					}
				}
			case *ast.FragmentSpread:
				include, err := included(value.Directives, variables)
				if err != nil || !include {
					if err != nil {
						return err
					}
					continue
				}
				fragment := fragments.ForName(value.Name)
				if fragment == nil {
					return compileerr.New(compileerr.KindFragmentInvalid, compileerr.FragmentMissing)
				}
				if stack[value.Name] {
					return compileerr.New(compileerr.KindFragmentInvalid, compileerr.FragmentCycle)
				}
				stack[value.Name] = true
				err = visit(fragment.SelectionSet)
				delete(stack, value.Name)
				if err != nil {
					return err
				}
			default:
				return compileerr.Invariantf("unsupported analytics selection %T", selection)
			}
		}
		return nil
	}
	return result, visit(set)
}

func included(directives ast.DirectiveList, variables map[string]any) (bool, error) {
	result := true
	for _, directive := range directives {
		if directive.Name != "skip" && directive.Name != "include" {
			continue
		}
		argument := directive.Arguments.ForName("if")
		if argument == nil || argument.Value == nil {
			return false, directiveError(directive.Name)
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return false, directiveError(directive.Name)
		}
		flag, ok := value.(bool)
		if !ok {
			return false, directiveError(directive.Name)
		}
		if directive.Name == "skip" && flag || directive.Name == "include" && !flag {
			result = false
		}
	}
	return result, nil
}

func argumentValues(field *ast.Field, variables map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for _, argument := range field.Arguments {
		value, err := argument.Value.Value(variables)
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Path(argument.Name), compileerr.Model(field.Name))
		}
		result[argument.Name] = value
	}
	return result, nil
}

func allowedArguments(values map[string]any, names ...string) error {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for _, name := range sortedKeys(values) {
		if !allowed[name] {
			return compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableArgument, compileerr.Path(name))
		}
	}
	return nil
}

func categoryOperator(category string, kind compilerir.LogicalTypeKind) (golem.AggregateOperator, bool) {
	switch category {
	case "countFields":
		return golem.AggregateCountField, groupable(kind)
	case "sum":
		return golem.AggregateSum, numeric(kind)
	case "avg":
		return golem.AggregateAverage, numeric(kind)
	case "min":
		return golem.AggregateMinimum, minMax(kind)
	case "max":
		return golem.AggregateMaximum, minMax(kind)
	default:
		return 0, false
	}
}

func groupable(kind compilerir.LogicalTypeKind) bool {
	switch kind {
	case compilerir.TypeBool, compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal, compilerir.TypeString, compilerir.TypeUUID, compilerir.TypeDate, compilerir.TypeTime, compilerir.TypeDateTime, compilerir.TypeEnum:
		return true
	default:
		return false
	}
}
func numeric(kind compilerir.LogicalTypeKind) bool {
	switch kind {
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal:
		return true
	default:
		return false
	}
}
func minMax(kind compilerir.LogicalTypeKind) bool {
	return numeric(kind) || kind == compilerir.TypeString || kind == compilerir.TypeDate || kind == compilerir.TypeTime || kind == compilerir.TypeDateTime
}

func graphQLType(logical compilerir.LogicalTypeIR) (compilerir.GraphQLTypeIR, error) {
	name := map[compilerir.LogicalTypeKind]string{
		compilerir.TypeBool: "Boolean", compilerir.TypeInt16: "Int", compilerir.TypeInt32: "Int", compilerir.TypeInt64: "BigInt",
		compilerir.TypeFloat32: "Float", compilerir.TypeFloat64: "Float", compilerir.TypeDecimal: "Decimal", compilerir.TypeString: "String",
		compilerir.TypeUUID: "UUID", compilerir.TypeDate: "Date", compilerir.TypeTime: "Time", compilerir.TypeDateTime: "DateTime",
	}[logical.Kind]
	kind := compilerir.GraphQLTypeScalar
	if logical.Kind == compilerir.TypeEnum && logical.EnumID != nil {
		kind = compilerir.GraphQLTypeEnum
		name = string(*logical.EnumID)
		// CoerceExtensionValue indexes enums by GraphQL type name. Callers with
		// enum terms use the dedicated wire mapping before this name matters.
	}
	if name == "" {
		return compilerir.GraphQLTypeIR{}, compileerr.Invariantf("unsupported analytics value type %s", logical.Kind)
	}
	return compilerir.GraphQLTypeIR{Kind: kind, Name: name}, nil
}

func identitySet(values []compilerir.FieldID) map[compilerir.FieldID]bool {
	result := map[compilerir.FieldID]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func available(value compilerir.FieldContractIR) bool {
	return compilerir.ModesGraphQLReadable(value.Modes) && value.GraphQLName != ""
}
func publicModelID(value compilerir.ModelID) (golem.ModelID, error) {
	var result golem.ModelID
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(result) {
		return result, compileerr.Invariantf("invalid analytics model identity")
	}
	copy(result[:], decoded)
	return result, nil
}
func publicFieldID(value compilerir.FieldID) (golem.FieldID, error) {
	var result golem.FieldID
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(result) {
		return result, compileerr.Invariantf("invalid analytics field identity")
	}
	copy(result[:], decoded)
	return result, nil
}
func publicRelationID(value compilerir.RelationID) (golem.RelationID, error) {
	var result golem.RelationID
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(result) {
		return result, compileerr.Invariantf("invalid analytics relation identity")
	}
	copy(result[:], decoded)
	return result, nil
}
func mustPublicModelID(value compilerir.ModelID) golem.ModelID {
	result, _ := publicModelID(value)
	return result
}
func mustPublicFieldID(value compilerir.FieldID) golem.FieldID {
	result, _ := publicFieldID(value)
	return result
}
func responseName(field *ast.Field) string {
	if field.Alias != "" {
		return field.Alias
	}
	return field.Name
}
func outputTypeName(model string, operation golem.AnalyticsOperation) string {
	switch operation {
	case golem.AnalyticsGroupBy:
		return model + "Group"
	case golem.AnalyticsRelationGroupBy:
		return model + "RelationGroup"
	default:
		return model + "Aggregate"
	}
}
func exactInt(raw any) (int, error) {
	switch value := raw.(type) {
	case int:
		return value, nil
	case int32:
		return int(value), nil
	case int64:
		converted := int(value)
		if int64(converted) != value {
			return 0, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerPlatform)
		}
		return converted, nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 32)
		if err != nil {
			return 0, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerPlatform)
		}
		return int(parsed), nil
	default:
		return 0, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Type("Int"))
	}
}
func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
func exactText(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return string(value)
	default:
		return fmt.Sprint(raw)
	}
}
func sortedKeys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
func combinePredicates(values []golem.FrozenGroupPredicate) (golem.FrozenGroupPredicate, error) {
	if len(values) == 0 {
		return golem.FrozenGroupPredicate{}, compileerr.Invariantf("analytics having has no comparison")
	}
	if len(values) == 1 {
		return values[0], nil
	}
	return golem.FrozenGroupPredicate{Kind: golem.RuntimeGroupAnd, Children: values}, nil
}
func sortDirection(raw any) (bool, error) {
	value, ok := raw.(string)
	if !ok || value != "asc" && value != "desc" {
		return false, compileerr.Invariantf("analytics order direction is invalid")
	}
	return value == "desc", nil
}
func termName(model modelBinding, operation golem.AnalyticsOperation, term golem.FrozenAnalyticsTerm) string {
	key := golem.RuntimeAnalyticsTermKey(term)
	for name, value := range model.localDimensions {
		if golem.RuntimeAnalyticsTermKey(value.term) == key {
			return name
		}
	}
	if operation == golem.AnalyticsRelationGroupBy {
		for name, value := range model.relationDimensions {
			if golem.RuntimeAnalyticsTermKey(value.term) == key {
				return name
			}
		}
	}
	return ""
}

func keyNotInBy(name string) error {
	return compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedKeyNotInBy, compileerr.Path(name))
}

func unavailableField(name string) error {
	return compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableField, compileerr.Path(name))
}

func forbidsSelection(name string) error {
	return compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeForbidsSelection, compileerr.Path(name))
}

func directiveError(name string) error {
	return compileerr.New(compileerr.KindDirectiveInvalid, compileerr.DirectiveNeedsBoolean, compileerr.Path(name))
}

func limitError(name string, maximum int) error {
	return compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit(name, maximum))
}

func duplicateOrder(seen map[string]bool, term golem.FrozenAnalyticsTerm) bool {
	key := golem.RuntimeAnalyticsTermKey(term)
	if seen[key] {
		return true
	}
	seen[key] = true
	return false
}

func sortedBools(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
