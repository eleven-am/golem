// Package operation compiles one validated GraphQL operation into the closed
// P3/P4 requests owned by the existing engines. This package is deliberately
// execution-free: generated adapters decide which typed runtime client invokes
// a compiled root, while authorization and SQL remain in P3/P4.
package operation

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlanalytics "github.com/eleven-am/golem/go/internal/graphql/analytics"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	graphqlcustom "github.com/eleven-am/golem/go/internal/graphql/custom"
	graphqlmutation "github.com/eleven-am/golem/go/internal/graphql/mutation"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2/ast"
)

type Limits struct {
	Bind      graphqlbind.Limits
	Depth     int
	Fields    int
	Aliases   int
	ListItems int
	MaxGroups int
}

type ReadRoot struct {
	ResponseName string
	Model        compilerir.ModelID
	Operation    readir.Operation
	Request      readir.Request
	Frozen       golem.FrozenReadRequest
	Slots        []selectset.Slot
}

type Result struct {
	Reads     []ReadRoot
	Analytics []graphqlanalytics.Root
	Mutations []MutationRoot
	Custom    []CustomRoot
	Event     *EventRoot
	Order     []RootRef
}

type EventRoot struct {
	ResponseName   string
	Model          compilerir.ModelID
	Read           readir.Request
	FrozenRead     golem.FrozenReadRequest
	EntitySelected bool
	IdentityFields []compilerir.FieldID
	EntitySlots    []selectset.Slot
	Slots          []EventSlot
}

type EventSlotKind uint8

const (
	EventSlotMetadata EventSlotKind = iota + 1
	EventSlotIdentity
	EventSlotEntity
	EventSlotTypename
)

type EventSlot struct {
	Kind         EventSlotKind
	ResponseName string
	FieldName    string
	Identity     []EventIdentitySlot
	EntitySlots  []selectset.Slot
}

type EventIdentitySlot struct {
	ResponseName string
	FieldID      compilerir.FieldID
	Typename     bool
}

type RootKind uint8

const (
	RootRead RootKind = iota + 1
	RootAnalytics
	RootMutation
	RootCustomQuery
	RootCustomMutation
)

type RootRef struct {
	Kind  RootKind
	Index int
}

type CustomRoot struct {
	ResponseName string
	Name         string
	Kind         compilerir.CustomOperationKind
	Arguments    map[string]any
	Result       compilerir.GraphQLTypeIR
	Slots        []selectset.Slot
	semantic     *semanticCustomRoot
	model        compilerir.ModelID
	selection    []readir.Selection
}

type MutationRoot struct {
	ResponseName string
	Model        compilerir.ModelID
	Operation    graphqlmutation.RootOperation
	Request      graphqlmutation.Request
	Frozen       golem.RuntimeMutationRequest
	Slots        []selectset.Slot
	BatchSlots   []BatchSlot
}

type BatchSlot struct {
	ResponseName string
	Typename     bool
}

type Compiler struct {
	compilation   compilerir.CompilationIR
	binder        *graphqlbind.Binder
	limits        Limits
	queries       map[string]rootBinding
	mutations     map[string]mutationRootBinding
	mutation      *graphqlmutation.MapBinder
	custom        *graphqlcustom.Registry
	analytics     *graphqlanalytics.Compiler
	subscriptions map[string]compilerir.ModelID
}

type rootBinding struct {
	model     compilerir.ModelID
	operation readir.Operation
}

type mutationRootBinding struct {
	model     compilerir.ModelID
	operation graphqlmutation.RootOperation
}

func New(compilation compilerir.CompilationIR, limits Limits) (*Compiler, error) {
	binder, err := graphqlbind.New(compilation, limits.Bind)
	if err != nil {
		return nil, err
	}
	if limits.Depth <= 0 {
		limits.Depth = 12
	}
	if limits.Fields <= 0 {
		limits.Fields = 256
	}
	if limits.Aliases <= 0 {
		limits.Aliases = 128
	}
	if limits.ListItems <= 0 {
		limits.ListItems = limits.Bind.MaxListItems
		if limits.ListItems <= 0 {
			limits.ListItems = 1_000
		}
	}
	mutationBinder, err := graphqlmutation.NewMapBinder(compilation, graphqlmutation.Limits{MaxInputDepth: limits.Bind.MaxInputDepth, MaxInputNodes: limits.Bind.MaxInputNodes, MaxListItems: limits.Bind.MaxListItems})
	if err != nil {
		return nil, err
	}
	customRegistry, err := graphqlcustom.New(compilation)
	if err != nil {
		return nil, err
	}
	analyticsCompiler, err := graphqlanalytics.New(compilation, graphqlanalytics.Limits{Bind: limits.Bind, MaxGroups: limits.MaxGroups, ListItems: limits.ListItems})
	if err != nil {
		return nil, err
	}
	compiler := &Compiler{compilation: compilation, binder: binder, mutation: mutationBinder, custom: customRegistry, analytics: analyticsCompiler, limits: limits, queries: map[string]rootBinding{}, mutations: map[string]mutationRootBinding{}, subscriptions: map[string]compilerir.ModelID{}}
	for _, contract := range compilation.Contract.Models {
		if !contract.Exposed {
			continue
		}
		enabled := map[compilerir.Operation]bool{}
		for _, operation := range contract.Operations {
			enabled[operation] = true
		}
		if enabled[compilerir.OperationFindOne] {
			if err := compiler.addRoot(contract.Roots.FindOne, rootBinding{model: contract.ModelID, operation: readir.FindUnique}); err != nil {
				return nil, err
			}
		}
		if enabled[compilerir.OperationFindMany] {
			if err := compiler.addRoot(contract.Roots.FindMany, rootBinding{model: contract.ModelID, operation: readir.FindMany}); err != nil {
				return nil, err
			}
		}
		if contract.Subscriptions {
			if contract.Event == nil || contract.Roots.Events == "" {
				return nil, fmt.Errorf("P7_OPERATION_ROOT: model %s has an incomplete subscription contract", contract.ModelID)
			}
			if _, duplicate := compiler.subscriptions[contract.Roots.Events]; duplicate {
				return nil, fmt.Errorf("P7_OPERATION_ROOT: subscription root %q is duplicated", contract.Roots.Events)
			}
			compiler.subscriptions[contract.Roots.Events] = contract.ModelID
		}
		mutationRoots := []struct {
			enabled compilerir.Operation
			name    string
			kind    graphqlmutation.RootOperation
		}{
			{compilerir.OperationCreate, contract.Roots.Create, graphqlmutation.Create},
			{compilerir.OperationUpdate, contract.Roots.Update, graphqlmutation.Update},
			{compilerir.OperationUpsert, contract.Roots.Upsert, graphqlmutation.Upsert},
			{compilerir.OperationDelete, contract.Roots.Delete, graphqlmutation.Delete},
			{compilerir.OperationUpdateMany, contract.Roots.UpdateMany, graphqlmutation.UpdateMany},
			{compilerir.OperationDeleteMany, contract.Roots.DeleteMany, graphqlmutation.DeleteMany},
		}
		for _, root := range mutationRoots {
			versionedBatch := contract.OptimisticConcurrency != nil && (root.kind == graphqlmutation.UpdateMany || root.kind == graphqlmutation.DeleteMany)
			if enabled[root.enabled] && !versionedBatch {
				if err := compiler.addMutationRoot(root.name, mutationRootBinding{model: contract.ModelID, operation: root.kind}); err != nil {
					return nil, err
				}
			}
		}
	}
	return compiler, nil
}

func (c *Compiler) addMutationRoot(name string, binding mutationRootBinding) error {
	if name == "" {
		return fmt.Errorf("P5_OPERATION_ROOT: enabled mutation has an empty materialized name")
	}
	if _, duplicate := c.mutations[name]; duplicate {
		return fmt.Errorf("P5_OPERATION_ROOT: mutation root %q is duplicated", name)
	}
	c.mutations[name] = binding
	return nil
}

func (c *Compiler) addRoot(name string, binding rootBinding) error {
	if name == "" {
		return fmt.Errorf("P5_OPERATION_ROOT: enabled query has an empty materialized name")
	}
	if _, duplicate := c.queries[name]; duplicate {
		return fmt.Errorf("P5_OPERATION_ROOT: query root %q is duplicated", name)
	}
	c.queries[name] = binding
	return nil
}

func (c *Compiler) Compile(document *ast.QueryDocument, definition *ast.OperationDefinition, variables map[string]any) (Result, error) {
	if document == nil || definition == nil {
		return Result{}, compileerr.Invariantf("document and selected operation are required")
	}
	switch definition.Operation {
	case ast.Query:
		return c.compileQuery(document, definition, variables)
	case ast.Mutation:
		return c.compileMutation(document, definition, variables)
	case ast.Subscription:
		return c.compileSubscription(document, definition, variables)
	default:
		return Result{}, compileerr.Invariantf("%s is not a supported operation", definition.Operation)
	}
}

func (c *Compiler) compileSubscription(document *ast.QueryDocument, definition *ast.OperationDefinition, variables map[string]any) (Result, error) {
	fields, err := expandRoots(definition.SelectionSet, document.Fragments, variables, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	if len(fields) != 1 {
		return Result{}, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeSingleRoot)
	}
	root := fields[0]
	modelID, ok := c.subscriptions[root.field.Name]
	if !ok {
		return Result{}, compileerr.Root(compileerr.Invariantf("subscription root %q is not generated", root.field.Name), root.responseName)
	}
	contract, model, err := c.subscriptionModel(modelID)
	if err != nil {
		return Result{}, compileerr.Root(err, root.responseName)
	}
	outer, err := expandRoots(root.selections, document.Fragments, variables, map[string]bool{})
	if err != nil {
		return Result{}, compileerr.Root(err, root.responseName)
	}
	if len(outer) == 0 {
		return Result{}, compileerr.Root(compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection), root.responseName)
	}
	compiled := &EventRoot{ResponseName: root.responseName, Model: modelID, IdentityFields: append([]compilerir.FieldID(nil), model.PrimaryKey.Fields...)}
	var entitySelections ast.SelectionSet
	entityPrefixes := map[string]int{}
	for _, field := range outer {
		slot := EventSlot{ResponseName: field.responseName, FieldName: field.field.Name}
		switch field.field.Name {
		case "eventID", "causationID", "transactionOrdinal", "recordedAt", "version", "type":
			if len(field.field.Arguments) != 0 || len(field.selections) != 0 {
				return Result{}, compileerr.Root(eventFieldShape(field.field.Name, contract.GraphQLName), root.responseName)
			}
			slot.Kind = EventSlotMetadata
		case "__typename":
			if len(field.field.Arguments) != 0 || len(field.selections) != 0 {
				return Result{}, compileerr.Root(eventFieldShape(field.field.Name, contract.GraphQLName), root.responseName)
			}
			slot.Kind = EventSlotTypename
		case "id":
			slot.Kind = EventSlotIdentity
			slot.Identity, err = compileEventIdentitySlots(contract, model, field.selections, document.Fragments, variables)
			if err != nil {
				return Result{}, compileerr.Root(err, root.responseName)
			}
		case "entity":
			if len(field.selections) == 0 {
				return Result{}, compileerr.Root(compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeNeedsSelection, compileerr.Path("entity"), compileerr.Model(contract.GraphQLName)), root.responseName)
			}
			slot.Kind = EventSlotEntity
			compiled.EntitySelected = true
			prefix := fmt.Sprintf("_golem_event_%d_", len(compiled.Slots))
			namespaced, namespaceErr := namespaceEventEntitySelections(field.selections, document.Fragments, variables, prefix)
			if namespaceErr != nil {
				return Result{}, compileerr.Root(namespaceErr, root.responseName)
			}
			entityPrefixes[prefix] = len(compiled.Slots)
			entitySelections = append(entitySelections, namespaced...)
		default:
			return Result{}, compileerr.Root(compileerr.Invariantf("event field %q is not generated", field.field.Name), root.responseName)
		}
		compiled.Slots = append(compiled.Slots, slot)
	}
	var selections []readir.Selection
	if compiled.EntitySelected {
		selected, selectErr := selectset.Compile(selectset.Request{
			Compilation: c.compilation, Model: modelID, Selections: entitySelections, Fragments: document.Fragments, Variables: variables,
			MaxDepth: c.limits.Depth, MaxFields: c.limits.Fields, MaxAliases: c.limits.Aliases, MaxListItems: c.limits.ListItems,
			Child: func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
				return c.binder.Child(target, field, selections, variables)
			},
			Count: func(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
				return c.binder.Count(target, field, variables)
			},
		})
		if selectErr != nil {
			return Result{}, compileerr.Root(selectErr, root.responseName)
		}
		selections = selected.Selections
		compiled.EntitySlots = selectset.StableSlots(selected.Slots)
		for _, selectedSlot := range compiled.EntitySlots {
			matched := false
			for prefix, slotIndex := range entityPrefixes {
				if strings.HasPrefix(selectedSlot.ResponseName, prefix) {
					selectedSlot.ResponseName = strings.TrimPrefix(selectedSlot.ResponseName, prefix)
					compiled.Slots[slotIndex].EntitySlots = append(compiled.Slots[slotIndex].EntitySlots, selectedSlot)
					matched = true
					break
				}
			}
			if !matched {
				return Result{}, compileerr.Root(compileerr.Invariantf("entity selection namespace is invalid"), root.responseName)
			}
		}
	} else {
		for _, fieldID := range model.PrimaryKey.Fields {
			public, idErr := publicFieldID(fieldID)
			if idErr != nil {
				return Result{}, compileerr.Root(idErr, root.responseName)
			}
			selection, selectErr := readir.NewScalarSelection(policyir.FieldID(public))
			if selectErr != nil {
				return Result{}, compileerr.Root(selectErr, root.responseName)
			}
			selections = append(selections, selection)
		}
	}
	arguments, err := values(root.field, variables)
	if err != nil {
		return Result{}, compileerr.Root(err, root.responseName)
	}
	compiled.Read, err = c.binder.Query(graphqlbind.QueryInput{Operation: readir.FindMany, Model: modelID, Arguments: arguments, Selections: selections})
	if err != nil {
		return Result{}, compileerr.Root(err, root.responseName)
	}
	compiled.FrozenRead, err = c.freezeRequest(compiled.Read)
	if err != nil {
		return Result{}, compileerr.Root(err, root.responseName)
	}
	return Result{Event: compiled}, nil
}

func namespaceEventEntitySelections(selections ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, prefix string) (ast.SelectionSet, error) {
	fields, err := expandRoots(selections, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, err
	}
	result := make(ast.SelectionSet, 0, len(fields))
	for _, field := range fields {
		clone := *field.field
		clone.Alias = prefix + field.responseName
		clone.SelectionSet = append(ast.SelectionSet(nil), field.selections...)
		clone.Directives = nil
		result = append(result, &clone)
	}
	return result, nil
}

func (c *Compiler) subscriptionModel(modelID compilerir.ModelID) (compilerir.ModelContractIR, compilerir.ModelDeclIR, error) {
	var contract *compilerir.ModelContractIR
	var model *compilerir.ModelDeclIR
	for index := range c.compilation.Contract.Models {
		if c.compilation.Contract.Models[index].ModelID == modelID {
			contract = &c.compilation.Contract.Models[index]
			break
		}
	}
	for index := range c.compilation.Model.Models {
		if c.compilation.Model.Models[index].ID == modelID {
			model = &c.compilation.Model.Models[index]
			break
		}
	}
	if contract == nil || model == nil || model.PrimaryKey == nil || contract.Event == nil {
		return compilerir.ModelContractIR{}, compilerir.ModelDeclIR{}, compileerr.Invariantf("subscription model %s is incomplete", modelID)
	}
	return *contract, *model, nil
}

func compileEventIdentitySlots(contract compilerir.ModelContractIR, model compilerir.ModelDeclIR, selections ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any) ([]EventIdentitySlot, error) {
	if model.PrimaryKey == nil {
		return nil, compileerr.Invariantf("event model has no primary key")
	}
	if len(model.PrimaryKey.Fields) == 1 {
		if len(selections) != 0 {
			return nil, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeForbidsSelection, compileerr.Path("id"), compileerr.Model(contract.GraphQLName))
		}
		return nil, nil
	}
	if len(selections) == 0 {
		return nil, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeNeedsSelection, compileerr.Path("id"), compileerr.Model(contract.GraphQLName))
	}
	fields, err := expandRoots(selections, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, err
	}
	byName := make(map[string]compilerir.FieldID, len(model.PrimaryKey.Fields))
	for _, fieldID := range model.PrimaryKey.Fields {
		for _, field := range contract.Fields {
			if field.FieldID == fieldID {
				byName[field.GraphQLName] = fieldID
				break
			}
		}
	}
	result := make([]EventIdentitySlot, 0, len(fields))
	for _, field := range fields {
		if len(field.field.Arguments) != 0 || len(field.selections) != 0 {
			return nil, eventFieldShape(field.field.Name, contract.GraphQLName)
		}
		if field.field.Name == "__typename" {
			result = append(result, EventIdentitySlot{ResponseName: field.responseName, Typename: true})
			continue
		}
		fieldID, ok := byName[field.field.Name]
		if !ok {
			return nil, compileerr.Invariantf("identity field %q is not generated", field.field.Name)
		}
		result = append(result, EventIdentitySlot{ResponseName: field.responseName, FieldID: fieldID})
	}
	return result, nil
}

func (c *Compiler) compileQuery(document *ast.QueryDocument, definition *ast.OperationDefinition, variables map[string]any) (Result, error) {
	fields, err := expandRoots(definition.SelectionSet, document.Fragments, variables, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	if len(fields) == 0 {
		return Result{}, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	result := Result{Reads: make([]ReadRoot, 0, len(fields))}
	for _, root := range fields {
		if queryMetaRoot(root.field.Name) {
			continue
		}
		binding, ok := c.queries[root.field.Name]
		if !ok {
			analyticsRoot, analyticsOK, analyticsErr := c.analytics.Compile(root.field, document.Fragments, variables)
			if analyticsErr != nil {
				return Result{}, compileerr.Root(analyticsErr, root.responseName)
			}
			if analyticsOK {
				analyticsRoot.ResponseName = root.responseName
				result.Analytics = append(result.Analytics, analyticsRoot)
				result.Order = append(result.Order, RootRef{Kind: RootAnalytics, Index: len(result.Analytics) - 1})
				continue
			}
			custom, customErr := c.compileCustom(root, compilerir.CustomOperationQuery, document.Fragments, variables)
			if customErr != nil {
				return Result{}, compileerr.Root(customErr, root.responseName)
			}
			result.Custom = append(result.Custom, custom)
			result.Order = append(result.Order, RootRef{Kind: RootCustomQuery, Index: len(result.Custom) - 1})
			continue
		}
		selected, selectErr := selectset.Compile(selectset.Request{
			Compilation:  c.compilation,
			Model:        binding.model,
			Selections:   root.selections,
			Fragments:    document.Fragments,
			Variables:    variables,
			MaxDepth:     c.limits.Depth,
			MaxFields:    c.limits.Fields,
			MaxAliases:   c.limits.Aliases,
			MaxListItems: c.limits.ListItems,
			Child: func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
				return c.binder.Child(target, field, selections, variables)
			},
			Count: func(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
				return c.binder.Count(target, field, variables)
			},
		})
		if selectErr != nil {
			return Result{}, compileerr.Root(selectErr, root.responseName)
		}
		arguments, argumentErr := values(root.field, variables)
		if argumentErr != nil {
			return Result{}, compileerr.Root(argumentErr, root.responseName)
		}
		request, bindErr := c.binder.Query(graphqlbind.QueryInput{Operation: binding.operation, Model: binding.model, Arguments: arguments, Selections: selected.Selections})
		if bindErr != nil {
			return Result{}, compileerr.Root(bindErr, root.responseName)
		}
		frozen, freezeErr := c.freezeRequest(request)
		if freezeErr != nil {
			return Result{}, compileerr.Root(freezeErr, root.responseName)
		}
		result.Reads = append(result.Reads, ReadRoot{ResponseName: root.responseName, Model: binding.model, Operation: binding.operation, Request: request, Frozen: frozen, Slots: selectset.StableSlots(selected.Slots)})
		result.Order = append(result.Order, RootRef{Kind: RootRead, Index: len(result.Reads) - 1})
	}
	return result, nil
}

func queryMetaRoot(name string) bool {
	return name == "__schema" || name == "__type" || name == "__typename"
}

func (c *Compiler) EncodeAnalytics(root graphqlanalytics.Root, rows [][]golem.RuntimeAnalyticsCell) (any, error) {
	if c == nil || c.analytics == nil {
		return nil, fmt.Errorf("P6_OPERATION_ANALYTICS: compiler is unavailable")
	}
	return c.analytics.Encode(root, rows)
}

func (c *Compiler) compileMutation(document *ast.QueryDocument, definition *ast.OperationDefinition, variables map[string]any) (Result, error) {
	fields, err := expandRoots(definition.SelectionSet, document.Fragments, variables, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	if len(fields) == 0 {
		return Result{}, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	result := Result{Mutations: make([]MutationRoot, 0, len(fields))}
	for _, root := range fields {
		if root.field.Name == "__typename" {
			continue
		}
		binding, ok := c.mutations[root.field.Name]
		if !ok {
			custom, customErr := c.compileCustom(root, compilerir.CustomOperationMutation, document.Fragments, variables)
			if customErr != nil {
				return Result{}, compileerr.Root(customErr, root.responseName)
			}
			result.Custom = append(result.Custom, custom)
			result.Order = append(result.Order, RootRef{Kind: RootCustomMutation, Index: len(result.Custom) - 1})
			continue
		}
		arguments, argumentErr := values(root.field, variables)
		if argumentErr != nil {
			return Result{}, compileerr.Root(argumentErr, root.responseName)
		}
		compiled := MutationRoot{ResponseName: root.responseName, Model: binding.model, Operation: binding.operation}
		var selections []readir.Selection
		var projection *golem.FrozenReadRequest
		if binding.operation == graphqlmutation.UpdateMany || binding.operation == graphqlmutation.DeleteMany {
			compiled.BatchSlots, err = compileBatchSlots(root.selections, document.Fragments, variables)
		} else {
			var selected selectset.Result
			selected, err = selectset.Compile(selectset.Request{
				Compilation: c.compilation, Model: binding.model, Selections: root.selections, Fragments: document.Fragments, Variables: variables,
				MaxDepth: c.limits.Depth, MaxFields: c.limits.Fields, MaxAliases: c.limits.Aliases,
				Child: func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
					return c.binder.Child(target, field, selections, variables)
				},
				Count: func(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
					return c.binder.Count(target, field, variables)
				},
			})
			if err == nil {
				selections, compiled.Slots = selected.Selections, selectset.StableSlots(selected.Slots)
				model, modelErr := publicModelID(binding.model)
				if modelErr != nil {
					err = modelErr
				} else {
					request, requestErr := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(model), Projection: readir.ProjectionSelect, Selection: selections})
					if requestErr != nil {
						err = requestErr
					} else {
						frozen, freezeErr := c.freezeRequest(request)
						err, projection = freezeErr, &frozen
					}
				}
			}
		}
		if err != nil {
			return Result{}, compileerr.Root(err, root.responseName)
		}
		compiled.Request, err = c.mutation.LowerValues(binding.operation, binding.model, arguments, selections)
		if err != nil {
			return Result{}, compileerr.Root(err, root.responseName)
		}
		compiled.Frozen, err = compiled.Request.FreezeExecution(projection)
		if err != nil {
			return Result{}, compileerr.Root(err, root.responseName)
		}
		result.Mutations = append(result.Mutations, compiled)
		result.Order = append(result.Order, RootRef{Kind: RootMutation, Index: len(result.Mutations) - 1})
	}
	return result, nil
}

func (c *Compiler) compileCustom(root rootField, kind compilerir.CustomOperationKind, fragments ast.FragmentDefinitionList, variables map[string]any) (CustomRoot, error) {
	arguments, err := values(root.field, variables)
	if err != nil {
		return CustomRoot{}, err
	}
	contract, ok := c.customContract(kind, root.field.Name)
	if !ok {
		return CustomRoot{}, compileerr.Invariantf("%s root %q is not generated", kind, root.field.Name)
	}
	arguments, err = c.bindCustomArguments(contract, arguments)
	if err != nil {
		return CustomRoot{}, err
	}
	semantic, err := c.bindSemanticSearchTake(contract, arguments, root.field.Arguments.ForName("take") != nil)
	if err != nil {
		return CustomRoot{}, err
	}
	prepared, err := c.custom.Prepare(kind, root.field.Name, arguments)
	if err != nil {
		return CustomRoot{}, err
	}
	result := prepared.ResultType()
	modelName := customResultModelName(result)
	isObject := modelName != ""
	if isObject && len(root.selections) == 0 {
		return CustomRoot{}, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeNeedsSelection, compileerr.Path(root.field.Name))
	}
	if !isObject && len(root.selections) != 0 {
		return CustomRoot{}, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeForbidsSelection, compileerr.Path(root.field.Name))
	}
	compiled := CustomRoot{ResponseName: root.responseName, Name: root.field.Name, Kind: kind, Arguments: arguments, Result: result}
	if isObject {
		modelID, ok := c.modelIDByGraphQLName(modelName)
		if !ok {
			return CustomRoot{}, compileerr.Invariantf("custom result model %s is absent", modelName)
		}
		selected, selectErr := selectset.Compile(selectset.Request{
			Compilation: c.compilation, Model: modelID, Selections: root.selections, Fragments: fragments, Variables: variables,
			MaxDepth: c.limits.Depth, MaxFields: c.limits.Fields, MaxAliases: c.limits.Aliases, MaxListItems: c.limits.ListItems,
			Child: func(target compilerir.ModelID, field *ast.Field, selections []readir.Selection) (readir.Request, error) {
				return c.binder.Child(target, field, selections, variables)
			},
			Count: func(target compilerir.ModelID, field *ast.Field) (readir.Request, error) {
				return c.binder.Count(target, field, variables)
			},
		})
		if selectErr != nil {
			return CustomRoot{}, selectErr
		}
		compiled.Slots = selectset.StableSlots(selected.Slots)
		compiled.model, compiled.selection = modelID, selected.Selections
		if semantic {
			compiled.semantic, err = c.newSemanticCustomRoot(contract, modelID, arguments)
			if err != nil {
				return CustomRoot{}, err
			}
		}
	}
	return compiled, nil
}

func (c *Compiler) customContract(kind compilerir.CustomOperationKind, name string) (compilerir.CustomOperationContractIR, bool) {
	for _, operation := range c.compilation.Contract.CustomOperations {
		if operation.Operation == kind && operation.Name == name {
			return operation, true
		}
	}
	return compilerir.CustomOperationContractIR{}, false
}

func (c *Compiler) bindCustomArguments(contract compilerir.CustomOperationContractIR, supplied map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(supplied))
	for _, argument := range contract.Arguments {
		raw, present := supplied[argument.Name]
		if !present {
			continue
		}
		value, err := c.bindCustomValue(argument.Type, raw, argument.Name)
		if err != nil {
			return nil, compileerr.On(err, contract.Name)
		}
		result[argument.Name] = value
	}
	for name := range supplied {
		known := false
		for _, argument := range contract.Arguments {
			if argument.Name == name {
				known = true
				break
			}
		}
		if !known {
			result[name] = supplied[name]
		}
	}
	return result, nil
}

func (c *Compiler) bindCustomValue(typ compilerir.GraphQLTypeIR, raw any, path string) (any, error) {
	if raw == nil {
		return nil, nil
	}
	modelID, modelKind := c.modelIDByGraphQLName(typ.Name)
	switch typ.Kind {
	case compilerir.GraphQLTypePredicate:
		if !modelKind {
			return nil, compileerr.Invariantf("predicate model %s is absent", typ.Name)
		}
		condition, err := c.binder.WhereAt(modelID, raw, path)
		if err != nil {
			return nil, err
		}
		return c.binder.FreezePredicate(condition)
	case compilerir.GraphQLTypeSelector:
		if !modelKind {
			return nil, compileerr.Invariantf("selector model %s is absent", typ.Name)
		}
		target, err := c.mutation.Target(modelID, raw, path)
		if err != nil {
			return nil, err
		}
		return target, nil
	case compilerir.GraphQLTypeCreateInput, compilerir.GraphQLTypeUpdateInput, compilerir.GraphQLTypeUpdateManyInput:
		if !modelKind {
			return nil, compileerr.Invariantf("mutation input model %s is absent", typ.Name)
		}
		kind := map[compilerir.GraphQLTypeKind]graphqlmutation.InputKind{compilerir.GraphQLTypeCreateInput: graphqlmutation.CreateInput, compilerir.GraphQLTypeUpdateInput: graphqlmutation.UpdateInput, compilerir.GraphQLTypeUpdateManyInput: graphqlmutation.UpdateManyInput}[typ.Kind]
		value, err := c.mutation.CustomInput(kind, modelID, raw)
		if err != nil {
			return nil, compileerr.At(err, path)
		}
		publicKind := map[compilerir.GraphQLTypeKind]golem.RuntimeMutationInputKind{compilerir.GraphQLTypeCreateInput: golem.RuntimeMutationCreateInput, compilerir.GraphQLTypeUpdateInput: golem.RuntimeMutationUpdateInput, compilerir.GraphQLTypeUpdateManyInput: golem.RuntimeMutationUpdateManyInput}[typ.Kind]
		input, err := golem.RuntimeCustomMutationInputValue(publicKind, value)
		if err != nil {
			return nil, compileerr.Ensure(err)
		}
		return input, nil
	default:
		value, err := selectset.CoerceExtensionValue(c.compilation, typ, raw, c.limits.ListItems)
		if err != nil {
			return nil, compileerr.At(err, path)
		}
		return value, nil
	}
}

func (c *Compiler) modelIDByGraphQLName(name string) (compilerir.ModelID, bool) {
	for _, contract := range c.compilation.Contract.Models {
		if contract.Exposed && contract.GraphQLName == name {
			return contract.ModelID, true
		}
	}
	return "", false
}

func compileBatchSlots(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any) ([]BatchSlot, error) {
	fields, err := expandRoots(set, fragments, variables, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.EmptySelection)
	}
	result := make([]BatchSlot, len(fields))
	for index, field := range fields {
		if field.field.Name != "count" && field.field.Name != "__typename" {
			return nil, compileerr.Invariantf("batch payload field %q is not generated", field.field.Name)
		}
		if len(field.field.Arguments) != 0 || len(field.selections) != 0 {
			return nil, compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeForbidsSelection, compileerr.Path(field.field.Name))
		}
		result[index] = BatchSlot{ResponseName: field.responseName, Typename: field.field.Name == "__typename"}
	}
	return result, nil
}

type rootField struct {
	field        *ast.Field
	responseName string
	selections   ast.SelectionSet
}

func expandRoots(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, stack map[string]bool) ([]rootField, error) {
	var ordered []rootField
	byResponse := map[string]int{}
	var visit func(ast.SelectionSet) error
	visit = func(current ast.SelectionSet) error {
		for _, selection := range current {
			switch value := selection.(type) {
			case *ast.Field:
				include, err := included(value.Directives, variables)
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
				if previous, duplicate := byResponse[response]; duplicate {
					if ordered[previous].field.Name != value.Name || !sameArguments(ordered[previous].field.Arguments, value.Arguments, variables) {
						return compileerr.New(compileerr.KindFragmentInvalid, compileerr.FragmentIncompatible)
					}
					ordered[previous].selections = append(ordered[previous].selections, value.SelectionSet...)
					continue
				}
				byResponse[response] = len(ordered)
				ordered = append(ordered, rootField{field: value, responseName: response, selections: append(ast.SelectionSet(nil), value.SelectionSet...)})
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
				if err != nil {
					return err
				}
				if !include {
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
				return compileerr.Invariantf("unknown root selection %T", selection)
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
			return false, directiveError(directive.Name)
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return false, directiveError(directive.Name)
		}
		boolean, ok := value.(bool)
		if !ok {
			return false, directiveError(directive.Name)
		}
		if directive.Name == "skip" && boolean || directive.Name == "include" && !boolean {
			result = false
		}
	}
	return result, nil
}

func values(field *ast.Field, variables map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(field.Arguments))
	for _, argument := range field.Arguments {
		value, err := argument.Value.Value(variables)
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Path(argument.Name), compileerr.Model(field.Name))
		}
		result[argument.Name] = value
	}
	return result, nil
}

func directiveError(name string) error {
	return compileerr.New(compileerr.KindDirectiveInvalid, compileerr.DirectiveNeedsBoolean, compileerr.Path(name))
}

func eventFieldShape(field, model string) error {
	return compileerr.New(compileerr.KindSelectionShape, compileerr.ShapeEventField, compileerr.Path(field), compileerr.Model(model))
}

func sameArguments(left, right ast.ArgumentList, variables map[string]any) bool {
	leftValues := map[string]any{}
	for _, argument := range left {
		value, err := argument.Value.Value(variables)
		if err != nil {
			return false
		}
		leftValues[argument.Name] = value
	}
	if len(leftValues) != len(right) {
		return false
	}
	for _, argument := range right {
		value, err := argument.Value.Value(variables)
		if err != nil || fmt.Sprintf("%#v", leftValues[argument.Name]) != fmt.Sprintf("%#v", value) {
			return false
		}
	}
	return true
}
