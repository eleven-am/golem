package operation

import (
	"fmt"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"reflect"
)

func (c *Compiler) validatePageRoot(root CustomRoot) error {
	if c == nil || root.Pagination == nil || root.Kind != compilerir.CustomOperationQuery {
		return fmt.Errorf("page finalization requires its compiled query")
	}
	for _, operation := range c.compilation.Contract.CustomOperations {
		if operation.Name == root.Name && operation.Operation == root.Kind && operation.Pagination != nil && reflect.DeepEqual(operation.Result, root.Result) && reflect.DeepEqual(operation.Pagination, root.Pagination) {
			return nil
		}
	}
	return fmt.Errorf("page finalization differs from its registered contract")
}

// PreparePageValue drops unreadable direct model candidates only at the
// synthetic page.items boundary. Candidate consumption/checkpoint state stays
// owned by runtime. DTO row gates retain their existing hydration behavior.
func (c *Compiler) PreparePageValue(root CustomRoot, value any) (any, error) {
	if err := c.validatePageRoot(root); err != nil {
		return nil, err
	}
	if root.Pagination.Item.Kind != compilerir.GraphQLTypeModel {
		return value, nil
	}
	prune := func(value any) ([]any, error) {
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("hydrated page items are not a list")
		}
		result := make([]any, 0, len(items))
		for _, item := range items {
			if item != nil {
				result = append(result, item)
			}
		}
		return result, nil
	}
	switch object := value.(type) {
	case map[string]any:
		items, err := prune(object["items"])
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, len(object))
		for name, item := range object {
			result[name] = item
		}
		result["items"] = items
		return result, nil
	case hydratedObject:
		result := hydratedObject{fields: make(map[string]any, len(object.fields)), gate: object.gate}
		for name, item := range object.fields {
			result.fields[name] = item
		}
		for _, slot := range root.Slots {
			if slot.Kind != selectset.SlotObjectField || slot.ObjectField != "items" {
				continue
			}
			items, err := prune(object.fields[slot.ResponseName])
			if err != nil {
				return nil, err
			}
			result.fields[slot.ResponseName] = items
		}
		return result, nil
	default:
		return nil, fmt.Errorf("hydrated page has unsupported representation %T", value)
	}
}

// FinalizePageValue runs only after ordinary encoding succeeds without field
// failures. Encoded objects are keyed by response names, so aliases and omitted
// next selections stay owned by the ordinary compiled selection slots.
func (c *Compiler) FinalizePageValue(root CustomRoot, encoded any, finish func() (string, error)) (any, error) {
	if err := c.validatePageRoot(root); err != nil {
		return nil, err
	}
	if finish == nil {
		return nil, fmt.Errorf("page continuation finalizer is absent")
	}
	object, ok := encoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("encoded page is not an object")
	}
	for _, slot := range root.Slots {
		if slot.Kind == selectset.SlotObjectField && slot.ObjectField == "next" {
			value, present := object[slot.ResponseName]
			if !present || value != nil {
				return nil, fmt.Errorf("encoded page continuation placeholder is invalid")
			}
		}
	}
	next, err := finish()
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(object))
	for name, value := range object {
		result[name] = value
	}
	var token any
	if next != "" {
		token = next
	}
	for _, slot := range root.Slots {
		if slot.Kind == selectset.SlotObjectField && slot.ObjectField == "next" {
			result[slot.ResponseName] = token
		}
	}
	return result, nil
}
