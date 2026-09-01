package operation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

type semanticCustomRoot struct {
	model          compilerir.ModelID
	publicModel    golem.ModelID
	hydrate        bool
	identityFields []golem.FieldID
	identityNames  []string
	identityTypes  []compilerir.LogicalTypeIR
	selection      []readir.Selection
}

func (c *Compiler) bindSemanticSearchTake(operation compilerir.CustomOperationContractIR, arguments map[string]any, explicit bool) (bool, error) {
	if c == nil || !(graphqlextension.IsSemanticSearchOperation(c.compilation, operation) || graphqlextension.IsSemanticSimilarOperation(c.compilation, operation)) {
		return false, nil
	}
	contract, ok := c.modelContractByGraphQLName(customResultModelName(operation.Result))
	if !ok {
		return false, fmt.Errorf("semantic search result model is absent")
	}
	maximum := semanticruntime.MaximumResults
	maximum = readir.NarrowCap(maximum, int(contract.Limits.MaxPageSize))
	maximum = readir.NarrowCap(maximum, int(contract.Limits.MaxTake))
	maximum = readir.NarrowCap(maximum, c.limits.Bind.MaxPageSize)
	value := int(contract.Limits.DefaultPageSize)
	if value < 1 {
		value = 50
	}
	if value > maximum {
		value = maximum
	}
	if supplied, present := arguments["take"]; explicit && present && supplied != nil {
		integer, ok := supplied.(int32)
		if !ok {
			return false, fmt.Errorf("semantic search take has value %T", supplied)
		}
		value = int(integer)
	}
	if value < 1 || value > maximum {
		return false, fmt.Errorf("semantic search take must be between 1 and %d", maximum)
	}
	// Generated semantic resolvers receive an exact non-nil value. The runtime
	// operation compiler, not generated source, owns the effective limit.
	arguments["take"] = int32(value)
	return true, nil
}

func (c *Compiler) newSemanticCustomRoot(operation compilerir.CustomOperationContractIR, modelID compilerir.ModelID, selected []readir.Selection) (*semanticCustomRoot, error) {
	if !graphqlextension.IsSemanticSearchOperation(c.compilation, operation) && !graphqlextension.IsSemanticSimilarOperation(c.compilation, operation) {
		return nil, fmt.Errorf("semantic search contract is not authoritative")
	}
	model, ok := c.modelByID(modelID)
	if !ok || model.PrimaryKey == nil || len(model.PrimaryKey.Fields) == 0 {
		return nil, fmt.Errorf("semantic search model has no primary identity")
	}
	contract, ok := c.modelContractByID(modelID)
	if !ok {
		return nil, fmt.Errorf("semantic search model contract is absent")
	}
	fields := make(map[compilerir.FieldID]compilerir.FieldContractIR, len(contract.Fields))
	for _, field := range contract.Fields {
		fields[field.FieldID] = field
	}
	modelFields := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		modelFields[field.ID] = field
	}
	publicModel, err := publicModelID(modelID)
	if err != nil {
		return nil, err
	}
	result := &semanticCustomRoot{model: modelID, publicModel: publicModel, selection: append([]readir.Selection(nil), selected...)}
	selectedFields := make(map[policyir.FieldID]bool, len(selected))
	for _, item := range selected {
		if item.Kind() == readir.SelectScalar {
			selectedFields[item.FieldID()] = true
		} else {
			result.hydrate = true
		}
	}
	if !result.hydrate {
		return result, nil
	}
	for _, identity := range model.PrimaryKey.Fields {
		field, present := fields[identity]
		modelField, modelPresent := modelFields[identity]
		public, err := publicFieldID(identity)
		if err != nil || !present || !modelPresent || modelField.Scalar == nil || field.GraphQLName == "" {
			return nil, fmt.Errorf("semantic search primary identity is unavailable")
		}
		result.identityFields = append(result.identityFields, public)
		result.identityNames = append(result.identityNames, field.GraphQLName)
		result.identityTypes = append(result.identityTypes, modelField.Scalar.Type)
		policyField := policyir.FieldID(public)
		if !selectedFields[policyField] {
			selection, selectionErr := readir.NewScalarSelection(policyField)
			if selectionErr != nil {
				return nil, selectionErr
			}
			result.selection = append(result.selection, selection)
			selectedFields[policyField] = true
		}
	}
	return result, nil
}

// SemanticCustomHydration retains ranked identities for bounded authorized reads.
type SemanticCustomHydration struct {
	model      policyir.ModelID
	selection  []readir.Selection
	conditions []policyir.Condition
	order      []string
}

// Len reports the number of ranked identities awaiting hydration.
func (hydration SemanticCustomHydration) Len() int { return len(hydration.conditions) }

// Order returns the stable vector-ranked identity order.
func (hydration SemanticCustomHydration) Order() []string {
	return append([]string(nil), hydration.order...)
}

// PrepareSemanticCustomHydration validates and retains identities for bounded reads.
func (c *Compiler) PrepareSemanticCustomHydration(root CustomRoot, value any) (SemanticCustomHydration, bool, error) {
	if c == nil || root.semantic == nil || !root.semantic.hydrate {
		return SemanticCustomHydration{}, false, nil
	}
	items, ok := value.([]any)
	if !ok {
		return SemanticCustomHydration{}, true, fmt.Errorf("semantic search result has value %T", value)
	}
	if len(items) == 0 {
		return SemanticCustomHydration{model: policyir.ModelID(root.semantic.publicModel), selection: root.semantic.selection}, true, nil
	}
	conditions := make([]policyir.Condition, len(items))
	order := make([]string, len(items))
	seen := make(map[string]bool, len(items))
	for index, item := range items {
		row, ok := item.(golem.RuntimeModelRow)
		if !ok || row.ModelID() != root.semantic.publicModel {
			return SemanticCustomHydration{}, true, fmt.Errorf("semantic search result item %d is not the declared model", index)
		}
		values, key, err := semanticIdentity(row, root.semantic.identityFields)
		if err != nil {
			return SemanticCustomHydration{}, true, fmt.Errorf("semantic search result item %d: %w", index, err)
		}
		if seen[key] {
			return SemanticCustomHydration{}, true, fmt.Errorf("semantic search returned a duplicate identity")
		}
		seen[key], order[index] = true, key
		where := make(map[string]any, len(values))
		for component, name := range root.semantic.identityNames {
			encoded, encodeErr := c.encodeLogical(root.semantic.identityTypes[component], values[component])
			if encodeErr != nil {
				return SemanticCustomHydration{}, true, fmt.Errorf("semantic search identity %d component %s: %w", index, name, encodeErr)
			}
			where[name] = map[string]any{"equals": encoded}
		}
		condition, bindErr := c.binder.MutationWhere(root.semantic.model, where)
		if bindErr != nil {
			return SemanticCustomHydration{}, true, fmt.Errorf("semantic search identity %d: %w", index, bindErr)
		}
		conditions[index] = condition
	}
	return SemanticCustomHydration{model: policyir.ModelID(root.semantic.publicModel), selection: root.semantic.selection, conditions: conditions, order: order}, true, nil
}

// SemanticCustomHydrationRequest freezes one ranked identity slice.
func (c *Compiler) SemanticCustomHydrationRequest(hydration SemanticCustomHydration, start, end int) (golem.FrozenReadRequest, error) {
	if c == nil || start < 0 || end <= start || end > len(hydration.conditions) || hydration.model == (policyir.ModelID{}) {
		return golem.FrozenReadRequest{}, fmt.Errorf("semantic search hydration range is invalid")
	}
	conditions := hydration.conditions[start:end]
	where := conditions[0]
	if len(conditions) > 1 {
		combined, err := policyir.NewLogical(hydration.model, policyir.LogicalOr, conditions)
		if err != nil {
			return golem.FrozenReadRequest{}, err
		}
		where = combined
	}
	take := len(conditions)
	request, err := readir.NewRequest(readir.RequestInput{
		Operation: readir.FindMany, Model: hydration.model, Where: &where,
		Take: &take, Selection: hydration.selection, Projection: readir.ProjectionSelect,
	})
	if err != nil {
		return golem.FrozenReadRequest{}, err
	}
	return c.freezeRequest(request)
}

// FinishSemanticCustomHydration restores vector rank after the provider read.
// Concurrently removed or newly unauthorized rows are omitted.
func (c *Compiler) FinishSemanticCustomHydration(root CustomRoot, order []string, rows []golem.RuntimeModelRow) (any, error) {
	if c == nil || root.semantic == nil {
		return nil, fmt.Errorf("semantic search hydration is unavailable")
	}
	byIdentity := make(map[string]golem.RuntimeModelRow, len(rows))
	for index, row := range rows {
		if row.ModelID() != root.semantic.publicModel {
			return nil, fmt.Errorf("semantic search hydrated item %d is not the declared model", index)
		}
		_, key, err := semanticIdentity(row, root.semantic.identityFields)
		if err != nil {
			return nil, fmt.Errorf("semantic search hydrated item %d: %w", index, err)
		}
		if _, duplicate := byIdentity[key]; duplicate {
			return nil, fmt.Errorf("semantic search hydration returned a duplicate identity")
		}
		byIdentity[key] = row
	}
	result := make([]any, 0, len(order))
	for _, key := range order {
		if row, present := byIdentity[key]; present {
			result = append(result, row)
			delete(byIdentity, key)
		}
	}
	if len(byIdentity) != 0 {
		return nil, fmt.Errorf("semantic search hydration returned an unranked identity")
	}
	return result, nil
}

func semanticIdentity(row golem.RuntimeModelRow, fields []golem.FieldID) ([]any, string, error) {
	values := make([]any, len(fields))
	for index, field := range fields {
		cell := golem.RuntimeTransportField(row, field)
		value, present := cell.Get()
		if cell.State() != golem.ReadPresent || !present {
			return nil, "", fmt.Errorf("authorized primary identity is unavailable")
		}
		values[index] = value
	}
	key, err := semantickey.Encode(values)
	return values, key, err
}

func (c *Compiler) modelByID(id compilerir.ModelID) (compilerir.ModelDeclIR, bool) {
	for _, model := range c.compilation.Model.Models {
		if model.ID == id {
			return model, true
		}
	}
	return compilerir.ModelDeclIR{}, false
}

func (c *Compiler) modelContractByID(id compilerir.ModelID) (compilerir.ModelContractIR, bool) {
	for _, model := range c.compilation.Contract.Models {
		if model.ModelID == id {
			return model, true
		}
	}
	return compilerir.ModelContractIR{}, false
}

func (c *Compiler) modelContractByGraphQLName(name string) (compilerir.ModelContractIR, bool) {
	for _, model := range c.compilation.Contract.Models {
		if model.GraphQLName == name {
			return model, true
		}
	}
	return compilerir.ModelContractIR{}, false
}

func customResultModelName(result compilerir.GraphQLTypeIR) string {
	if result.Kind == compilerir.GraphQLTypeModel {
		return result.Name
	}
	if result.Kind == compilerir.GraphQLTypeList && result.Element != nil && result.Element.Kind == compilerir.GraphQLTypeModel {
		return result.Element.Name
	}
	return ""
}
