// Package selectset lowers validated GraphQL selection sets into the existing
// occurrence-aware P3 projection IR. It binds public names once to stable IDs;
// authorization and SQL planning remain entirely in P3.
package selectset

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2/ast"
)

type SlotKind uint8

const (
	SlotScalar SlotKind = iota + 1
	SlotRelation
	SlotRelationCount
	SlotTypename
	SlotComputed
)

type Slot struct {
	Kind         SlotKind
	ResponseName string
	FieldID      compilerir.FieldID
	RelationID   compilerir.RelationID
	Occurrence   readir.OccurrenceID
	Children     []Slot
	Computed     *graphqlextension.ComputedSelection
}

type ChildRequestBuilder func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error)
type CountRequestBuilder func(target compilerir.ModelID, field *ast.Field) (readir.Request, error)

type Request struct {
	Compilation  compilerir.CompilationIR
	Model        compilerir.ModelID
	Selections   ast.SelectionSet
	Fragments    ast.FragmentDefinitionList
	Variables    map[string]any
	Child        ChildRequestBuilder
	Count        CountRequestBuilder
	MaxDepth     int
	MaxFields    int
	MaxAliases   int
	MaxListItems int
}

type Result struct {
	Selections []readir.Selection
	Slots      []Slot
	Depth      int
	Fields     int
	Aliases    int
}

type compiler struct {
	request        Request
	models         map[compilerir.ModelID]compilerir.ModelDeclIR
	contracts      map[compilerir.ModelID]compilerir.ModelContractIR
	relations      map[compilerir.RelationID]compilerir.RelationIR
	fragments      map[string]*ast.FragmentDefinition
	nextOccurrence readir.OccurrenceID
	fields         int
	aliases        int
	maxDepth       int
	contractNames  map[string]compilerir.ModelID
}

func Compile(request Request) (Result, error) {
	if request.Model == "" || len(request.Selections) == 0 {
		return Result{}, fmt.Errorf("P5_SELECT_INPUT: model and selection set are required")
	}
	if request.MaxDepth <= 0 {
		request.MaxDepth = 12
	}
	if request.MaxFields <= 0 {
		request.MaxFields = 256
	}
	if request.MaxAliases <= 0 {
		request.MaxAliases = 128
	}
	entry := &compiler{request: request, models: map[compilerir.ModelID]compilerir.ModelDeclIR{}, contracts: map[compilerir.ModelID]compilerir.ModelContractIR{}, relations: map[compilerir.RelationID]compilerir.RelationIR{}, fragments: map[string]*ast.FragmentDefinition{}, contractNames: map[string]compilerir.ModelID{}, nextOccurrence: 1}
	for _, model := range request.Compilation.Model.Models {
		entry.models[model.ID] = model
	}
	for _, contract := range request.Compilation.Contract.Models {
		entry.contracts[contract.ModelID] = contract
		if contract.Exposed {
			entry.contractNames[contract.GraphQLName] = contract.ModelID
		}
	}
	for _, relation := range request.Compilation.Model.Relations {
		entry.relations[relation.ID] = relation
	}
	for _, fragment := range request.Fragments {
		entry.fragments[fragment.Name] = fragment
	}
	selections, slots, err := entry.compileModel(request.Model, request.Selections, 1, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	return Result{Selections: selections, Slots: slots, Depth: entry.maxDepth, Fields: entry.fields, Aliases: entry.aliases}, nil
}

type fieldOccurrence struct {
	field        *ast.Field
	responseName string
	selections   ast.SelectionSet
}

func (in *compiler) compileModel(modelID compilerir.ModelID, set ast.SelectionSet, depth int, stack map[string]bool) ([]readir.Selection, []Slot, error) {
	if depth > in.request.MaxDepth {
		return nil, nil, fmt.Errorf("P5_SELECT_LIMIT: selection depth exceeds %d", in.request.MaxDepth)
	}
	if depth > in.maxDepth {
		in.maxDepth = depth
	}
	model, ok := in.models[modelID]
	if !ok {
		return nil, nil, fmt.Errorf("P5_SELECT_MODEL: model %s is absent", modelID)
	}
	contract, ok := in.contracts[modelID]
	if !ok || !contract.Exposed {
		return nil, nil, fmt.Errorf("P5_SELECT_MODEL: model %s is not exposed", modelID)
	}
	occurrences, err := in.expand(set, stack)
	if err != nil {
		return nil, nil, err
	}
	fieldsByName := map[string]struct {
		model    compilerir.FieldIR
		contract compilerir.FieldContractIR
	}{}
	contractFields := map[compilerir.FieldID]compilerir.FieldContractIR{}
	fieldsByID := map[compilerir.FieldID]compilerir.FieldIR{}
	for _, field := range contract.Fields {
		contractFields[field.FieldID] = field
	}
	for _, field := range model.Fields {
		fieldsByID[field.ID] = field
		if value, exists := contractFields[field.ID]; exists {
			fieldsByName[value.GraphQLName] = struct {
				model    compilerir.FieldIR
				contract compilerir.FieldContractIR
			}{field, value}
		}
	}
	computedByName := make(map[string]compilerir.ComputedFieldContractIR, len(contract.Computed))
	for _, field := range contract.Computed {
		computedByName[field.Name] = field
	}
	var selections []readir.Selection
	var slots []Slot
	selectedScalars := map[compilerir.FieldID]bool{}
	selectedDependencyRelations := map[compilerir.FieldID]graphqlextension.DependencyAccess{}
	for _, occurrence := range occurrences {
		in.fields++
		if in.fields > in.request.MaxFields {
			return nil, nil, fmt.Errorf("P5_SELECT_LIMIT: selected occurrences exceed %d", in.request.MaxFields)
		}
		if occurrence.responseName != occurrence.field.Name {
			in.aliases++
			if in.aliases > in.request.MaxAliases {
				return nil, nil, fmt.Errorf("P5_SELECT_LIMIT: aliases exceed %d", in.request.MaxAliases)
			}
		}
		if occurrence.field.Name == "__typename" {
			slots = append(slots, Slot{Kind: SlotTypename, ResponseName: occurrence.responseName})
			continue
		}
		if occurrence.field.Name == "_count" {
			countSelections, countSlots, countErr := in.compileCounts(modelID, occurrence.selections, depth+1, stack)
			if countErr != nil {
				return nil, nil, countErr
			}
			selections = append(selections, countSelections...)
			slots = append(slots, Slot{Kind: SlotRelationCount, ResponseName: occurrence.responseName, Children: countSlots})
			continue
		}
		if computed, exists := computedByName[occurrence.field.Name]; exists {
			arguments, canonical, argumentErr := bindComputedArguments(in.request.Compilation, computed, occurrence.field.Arguments, in.request.Variables, in.request.MaxListItems)
			if argumentErr != nil {
				return nil, nil, argumentErr
			}
			dependencies, injected, dependencyErr := in.computedDependencies(modelID, fieldsByID, computed, selectedScalars, selectedDependencyRelations)
			if dependencyErr != nil {
				return nil, nil, dependencyErr
			}
			selections = append(selections, injected...)
			var outputProjection []readir.Selection
			var children []Slot
			if target, object := in.computedResultModel(computed.Result); object {
				if len(occurrence.selections) == 0 {
					return nil, nil, fmt.Errorf("P5_SELECT_COMPUTED: object field %s.%s requires a selection set", contract.GraphQLName, computed.Name)
				}
				outputProjection, children, dependencyErr = in.compileModel(target, occurrence.selections, depth+1, stack)
				if dependencyErr != nil {
					return nil, nil, dependencyErr
				}
			} else if len(occurrence.selections) != 0 {
				return nil, nil, fmt.Errorf("P5_SELECT_COMPUTED: leaf field %s.%s has a selection set", contract.GraphQLName, computed.Name)
			}
			slots = append(slots, Slot{Kind: SlotComputed, ResponseName: occurrence.responseName, Children: children, Computed: &graphqlextension.ComputedSelection{
				ExtensionID: computed.ExtensionID, Result: computed.Result, Arguments: arguments, CanonicalArguments: canonical,
				Dependencies: dependencies, Batch: cloneBatch(computed.Batch), Projection: outputProjection,
			}})
			continue
		}
		binding, exists := fieldsByName[occurrence.field.Name]
		if !exists || !compilerir.ModesReadable(binding.contract.Modes) {
			return nil, nil, fmt.Errorf("P5_SELECT_FIELD: %s.%s is absent or not readable", contract.GraphQLName, occurrence.field.Name)
		}
		if binding.model.Kind != compilerir.FieldRelation {
			if len(occurrence.selections) != 0 {
				return nil, nil, fmt.Errorf("P5_SELECT_FIELD: scalar %s.%s has a selection set", contract.GraphQLName, occurrence.field.Name)
			}
			if !selectedScalars[binding.model.ID] {
				fieldID, idErr := policyFieldID(binding.model.ID)
				if idErr != nil {
					return nil, nil, idErr
				}
				selection, selectionErr := readir.NewScalarSelection(fieldID)
				if selectionErr != nil {
					return nil, nil, selectionErr
				}
				selections = append(selections, selection)
				selectedScalars[binding.model.ID] = true
			}
			slots = append(slots, Slot{Kind: SlotScalar, ResponseName: occurrence.responseName, FieldID: binding.model.ID})
			continue
		}
		if len(occurrence.selections) == 0 {
			return nil, nil, fmt.Errorf("P5_SELECT_FIELD: relation %s.%s requires a selection set", contract.GraphQLName, occurrence.field.Name)
		}
		relation, target, relationErr := in.relation(modelID, binding.model)
		if relationErr != nil {
			return nil, nil, relationErr
		}
		childSelections, childSlots, childErr := in.compileModel(target, occurrence.selections, depth+1, stack)
		if childErr != nil {
			return nil, nil, childErr
		}
		childRequest, childErr := in.childRequest(target, occurrence.field, childSelections)
		if childErr != nil {
			return nil, nil, childErr
		}
		id := in.nextOccurrence
		in.nextOccurrence++
		fieldID, idErr := policyFieldID(binding.model.ID)
		if idErr != nil {
			return nil, nil, idErr
		}
		relationID, idErr := policyRelationID(relation.ID)
		if idErr != nil {
			return nil, nil, idErr
		}
		targetID, idErr := policyModelID(target)
		if idErr != nil {
			return nil, nil, idErr
		}
		selection, selectionErr := readir.NewRelationSelectionOccurrence(id, fieldID, relationID, targetID, childRequest)
		if selectionErr != nil {
			return nil, nil, selectionErr
		}
		selections = append(selections, selection)
		slots = append(slots, Slot{Kind: SlotRelation, ResponseName: occurrence.responseName, FieldID: binding.model.ID, RelationID: relation.ID, Occurrence: id, Children: childSlots})
	}
	return selections, slots, nil
}

func (in *compiler) compileCounts(modelID compilerir.ModelID, set ast.SelectionSet, depth int, stack map[string]bool) ([]readir.Selection, []Slot, error) {
	if depth > in.request.MaxDepth {
		return nil, nil, fmt.Errorf("P5_SELECT_LIMIT: selection depth exceeds %d", in.request.MaxDepth)
	}
	model := in.models[modelID]
	contract := in.contracts[modelID]
	fieldContracts := map[compilerir.FieldID]compilerir.FieldContractIR{}
	for _, field := range contract.Fields {
		fieldContracts[field.FieldID] = field
	}
	byName := map[string]compilerir.FieldIR{}
	for _, field := range model.Fields {
		fc := fieldContracts[field.ID]
		if field.Kind == compilerir.FieldRelation && compilerir.ModesReadable(fc.Modes) {
			byName[fc.GraphQLName] = field
		}
	}
	occurrences, err := in.expand(set, stack)
	if err != nil {
		return nil, nil, err
	}
	var selections []readir.Selection
	var slots []Slot
	for _, occurrence := range occurrences {
		field, ok := byName[occurrence.field.Name]
		if !ok || field.Relation == nil || field.Relation.Kind != compilerir.RelationHasMany {
			return nil, nil, fmt.Errorf("P5_SELECT_COUNT: %s.%s is not a readable to-many relation count", contract.GraphQLName, occurrence.field.Name)
		}
		relation, target, relationErr := in.relation(modelID, field)
		if relationErr != nil {
			return nil, nil, relationErr
		}
		child, childErr := in.countRequest(target, occurrence.field)
		if childErr != nil {
			return nil, nil, childErr
		}
		id := in.nextOccurrence
		in.nextOccurrence++
		fieldID, idErr := policyFieldID(field.ID)
		if idErr != nil {
			return nil, nil, idErr
		}
		relationID, idErr := policyRelationID(relation.ID)
		if idErr != nil {
			return nil, nil, idErr
		}
		targetID, idErr := policyModelID(target)
		if idErr != nil {
			return nil, nil, idErr
		}
		selection, selectionErr := readir.NewRelationCountSelectionOccurrence(id, fieldID, relationID, targetID, child)
		if selectionErr != nil {
			return nil, nil, selectionErr
		}
		selections = append(selections, selection)
		slots = append(slots, Slot{Kind: SlotRelationCount, ResponseName: occurrence.responseName, FieldID: field.ID, RelationID: relation.ID, Occurrence: id})
	}
	return selections, slots, nil
}

func (in *compiler) expand(set ast.SelectionSet, stack map[string]bool) ([]fieldOccurrence, error) {
	var ordered []fieldOccurrence
	byResponse := map[string]int{}
	var visit func(ast.SelectionSet) error
	visit = func(current ast.SelectionSet) error {
		for _, selection := range current {
			switch value := selection.(type) {
			case *ast.Field:
				include, err := included(value.Directives, in.request.Variables)
				if err != nil {
					return err
				}
				if !include {
					continue
				}
				response := value.Alias
				if response == "" {
					response = value.Name
				}
				if index, exists := byResponse[response]; exists {
					same, argumentErr := sameFieldArguments(ordered[index].field.Arguments, value.Arguments, in.request.Variables)
					if argumentErr != nil {
						return argumentErr
					}
					if ordered[index].field.Name != value.Name || !same {
						return fmt.Errorf("P5_SELECT_MERGE: response name %q refers to incompatible fields", response)
					}
					ordered[index].selections = append(ordered[index].selections, value.SelectionSet...)
				} else {
					byResponse[response] = len(ordered)
					ordered = append(ordered, fieldOccurrence{field: value, responseName: response, selections: append(ast.SelectionSet(nil), value.SelectionSet...)})
				}
			case *ast.InlineFragment:
				include, err := included(value.Directives, in.request.Variables)
				if err != nil {
					return err
				}
				if include {
					if err := visit(value.SelectionSet); err != nil {
						return err
					}
				}
			case *ast.FragmentSpread:
				include, err := included(value.Directives, in.request.Variables)
				if err != nil {
					return err
				}
				if !include {
					continue
				}
				fragment := in.fragments[value.Name]
				if fragment == nil {
					return fmt.Errorf("P5_SELECT_FRAGMENT: fragment %q is absent", value.Name)
				}
				if stack[value.Name] {
					return fmt.Errorf("P5_SELECT_FRAGMENT: fragment cycle through %q", value.Name)
				}
				stack[value.Name] = true
				err = visit(fragment.SelectionSet)
				delete(stack, value.Name)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("P5_SELECT_AST: unknown selection node %T", selection)
			}
		}
		return nil
	}
	if err := visit(set); err != nil {
		return nil, err
	}
	return ordered, nil
}

func included(directives ast.DirectiveList, variables map[string]any) (bool, error) {
	result := true
	for _, directive := range directives {
		if directive.Name != "skip" && directive.Name != "include" {
			continue
		}
		argument := directive.Arguments.ForName("if")
		if argument == nil || argument.Value == nil {
			return false, fmt.Errorf("P5_SELECT_DIRECTIVE: @%s requires if", directive.Name)
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return false, fmt.Errorf("P5_SELECT_DIRECTIVE: @%s if: %w", directive.Name, err)
		}
		boolean, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("P5_SELECT_DIRECTIVE: @%s if is not Boolean", directive.Name)
		}
		if directive.Name == "skip" && boolean {
			result = false
		}
		if directive.Name == "include" && !boolean {
			result = false
		}
	}
	return result, nil
}

func (in *compiler) relation(owner compilerir.ModelID, field compilerir.FieldIR) (compilerir.RelationIR, compilerir.ModelID, error) {
	if field.Relation == nil {
		return compilerir.RelationIR{}, "", fmt.Errorf("P5_SELECT_RELATION: field %s has no relation metadata", field.ID)
	}
	relation, ok := in.relations[field.Relation.RelationID]
	if !ok {
		return compilerir.RelationIR{}, "", fmt.Errorf("P5_SELECT_RELATION: relation %s is absent", field.Relation.RelationID)
	}
	target := relation.TargetModel
	if relation.TargetModel == owner {
		target = relation.SourceModel
	}
	if field.Relation.Role == compilerir.RelationInverse {
		target = relation.SourceModel
	}
	return relation, target, nil
}

func (in *compiler) childRequest(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
	if in.request.Child != nil {
		return in.request.Child(target, field, selections)
	}
	model, err := policyModelID(target)
	if err != nil {
		return readir.Request{}, err
	}
	return readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: model, Projection: readir.ProjectionSelect, Selection: selections})
}

func (in *compiler) countRequest(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
	if in.request.Count != nil {
		return in.request.Count(target, field)
	}
	model, err := policyModelID(target)
	if err != nil {
		return readir.Request{}, err
	}
	return readir.NewRequest(readir.RequestInput{Operation: readir.Count, Model: model})
}

func (in *compiler) computedResultModel(value compilerir.GraphQLTypeIR) (compilerir.ModelID, bool) {
	for value.Kind == compilerir.GraphQLTypeList && value.Element != nil {
		value = *value.Element
	}
	if value.Kind != compilerir.GraphQLTypeModel {
		return "", false
	}
	model, ok := in.contractNames[value.Name]
	return model, ok
}

func (in *compiler) computedDependencies(modelID compilerir.ModelID, fields map[compilerir.FieldID]compilerir.FieldIR, computed compilerir.ComputedFieldContractIR, scalars map[compilerir.FieldID]bool, relations map[compilerir.FieldID]graphqlextension.DependencyAccess) ([]graphqlextension.DependencyAccess, []readir.Selection, error) {
	fieldIDs := append([]compilerir.FieldID(nil), computed.Requires...)
	if computed.Batch != nil {
		fieldIDs = append(fieldIDs, computed.Batch.KeyField)
	}
	seen := map[compilerir.FieldID]bool{}
	accesses := make([]graphqlextension.DependencyAccess, 0, len(fieldIDs))
	var injected []readir.Selection
	for _, id := range fieldIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		field, ok := fields[id]
		if !ok {
			return nil, nil, fmt.Errorf("P5_SELECT_COMPUTED_DEPENDENCY: %s references absent field %s", computed.Name, id)
		}
		if field.Kind != compilerir.FieldRelation {
			access := graphqlextension.DependencyAccess{Kind: graphqlextension.DependencyMaskedScalar, FieldID: id}
			accesses = append(accesses, access)
			if scalars[id] {
				continue
			}
			fieldID, err := policyFieldID(id)
			if err != nil {
				return nil, nil, err
			}
			selection, err := readir.NewScalarSelection(fieldID)
			if err != nil {
				return nil, nil, err
			}
			scalars[id] = true
			injected = append(injected, selection)
			continue
		}
		if access, exists := relations[id]; exists {
			accesses = append(accesses, access)
			continue
		}
		relation, target, err := in.relation(modelID, field)
		if err != nil {
			return nil, nil, err
		}
		targetID, err := policyModelID(target)
		if err != nil {
			return nil, nil, err
		}
		child, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: targetID, Projection: readir.ProjectionDefault})
		if err != nil {
			return nil, nil, err
		}
		fieldID, err := policyFieldID(id)
		if err != nil {
			return nil, nil, err
		}
		relationID, err := policyRelationID(relation.ID)
		if err != nil {
			return nil, nil, err
		}
		occurrence := in.nextOccurrence
		in.nextOccurrence++
		selection, err := readir.NewRelationSelectionOccurrence(occurrence, fieldID, relationID, targetID, child)
		if err != nil {
			return nil, nil, err
		}
		access := graphqlextension.DependencyAccess{Kind: graphqlextension.DependencyMaskedRelation, FieldID: id, RelationID: relation.ID, Occurrence: occurrence}
		relations[id] = access
		accesses = append(accesses, access)
		injected = append(injected, selection)
	}
	return accesses, injected, nil
}

func cloneBatch(value *compilerir.ComputedBatchContractIR) *compilerir.ComputedBatchContractIR {
	if value == nil {
		return nil
	}
	result := *value
	if value.CacheKey != nil {
		codec := *value.CacheKey
		result.CacheKey = &codec
	}
	return &result
}

func sameFieldArguments(left, right ast.ArgumentList, variables map[string]any) (bool, error) {
	leftKey, err := canonicalFieldArguments(left, variables)
	if err != nil {
		return false, err
	}
	rightKey, err := canonicalFieldArguments(right, variables)
	if err != nil {
		return false, err
	}
	return leftKey == rightKey, nil
}

func canonicalFieldArguments(arguments ast.ArgumentList, variables map[string]any) (string, error) {
	values := make([]graphqlextension.CanonicalArgument, 0, len(arguments))
	seen := map[string]bool{}
	for _, argument := range arguments {
		if seen[argument.Name] {
			return "", fmt.Errorf("P5_SELECT_ARGUMENT: argument %q is duplicated", argument.Name)
		}
		seen[argument.Name] = true
		if argument.Value == nil {
			return "", fmt.Errorf("P5_SELECT_ARGUMENT: argument %q has no value", argument.Name)
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return "", fmt.Errorf("P5_SELECT_ARGUMENT: argument %q: %w", argument.Name, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("P5_SELECT_ARGUMENT: argument %q cannot be canonicalized", argument.Name)
		}
		values = append(values, graphqlextension.CanonicalArgument{Name: argument.Name, Value: encoded})
	}
	return graphqlextension.CanonicalArguments(values...)
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, fmt.Errorf("P5_SELECT_ID: %q is not a canonical 128-bit identity", value)
	}
	copy(result[:], decoded)
	return result, nil
}
func policyModelID(value compilerir.ModelID) (policyir.ModelID, error) {
	parsed, err := fixedID(string(value))
	return policyir.ModelID(parsed), err
}
func policyFieldID(value compilerir.FieldID) (policyir.FieldID, error) {
	parsed, err := fixedID(string(value))
	return policyir.FieldID(parsed), err
}
func policyRelationID(value compilerir.RelationID) (policyir.RelationID, error) {
	parsed, err := fixedID(string(value))
	return policyir.RelationID(parsed), err
}

// StableSlots returns a detached response map ordered as GraphQL will encode it.
func StableSlots(values []Slot) []Slot {
	result := make([]Slot, len(values))
	copy(result, values)
	for index := range result {
		result[index].Children = StableSlots(result[index].Children)
		result[index].Computed = graphqlextension.CloneComputedSelection(result[index].Computed)
	}
	return result
}
