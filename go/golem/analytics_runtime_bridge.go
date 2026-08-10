package golem

import "fmt"

// RuntimeAnalyticsRequestInput is the model-erased handoff used by generated
// GraphQL. Stable identities and exact values have already been bound, but the
// request is validated and cloned here before entering the shared P6 planner.
type RuntimeAnalyticsRequestInput struct {
	Operation  AnalyticsOperation
	Model      ModelID
	Where      *FrozenPredicate
	Dimensions []FrozenAnalyticsTerm
	Measures   []FrozenAnalyticsTerm
	Having     *FrozenGroupPredicate
	OrderBy    []FrozenAnalyticsOrder
	Take       *int
	Skip       *int
}

const (
	RuntimeGroupCompare uint8 = 1
	RuntimeGroupAnd     uint8 = 2
	RuntimeGroupOr      uint8 = 3
	RuntimeGroupNot     uint8 = 4
)

func RuntimeFreezeAnalyticsRequest(input RuntimeAnalyticsRequestInput) (FrozenAnalyticsRequest, error) {
	if input.Operation < AnalyticsAggregate || input.Operation > AnalyticsRelationGroupBy || input.Model == (ModelID{}) {
		return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: operation and model are required")
	}
	result := FrozenAnalyticsRequest{
		operation:  input.Operation,
		model:      input.Model,
		dimensions: append([]FrozenAnalyticsTerm(nil), input.Dimensions...),
		measures:   append([]FrozenAnalyticsTerm(nil), input.Measures...),
		orders:     append([]FrozenAnalyticsOrder(nil), input.OrderBy...),
	}
	if input.Where != nil {
		if input.Where.View().RootModelID() != input.Model {
			return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: where model does not match")
		}
		value := cloneFrozenPredicate(*input.Where)
		result.where = &value
	}
	if input.Having != nil {
		value := cloneFrozenGroupPredicate(*input.Having)
		if err := validateRuntimeGroupPredicate(value, input.Model, 0); err != nil {
			return FrozenAnalyticsRequest{}, err
		}
		result.having = &value
	}
	if input.Take != nil {
		value := *input.Take
		result.take = &value
	}
	if input.Skip != nil {
		value := *input.Skip
		result.skip = &value
	}
	if input.Operation == AnalyticsAggregate && len(result.dimensions) != 0 {
		return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: aggregate cannot group")
	}
	if input.Operation != AnalyticsAggregate && len(result.dimensions) == 0 {
		return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: grouping requires dimensions")
	}
	if result.take != nil && *result.take == 0 {
		return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: take must be non-zero")
	}
	if result.skip != nil && *result.skip < 0 {
		return FrozenAnalyticsRequest{}, fmt.Errorf("runtime analytics request: skip must be non-negative")
	}
	if err := validateFrozenAnalytics(result); err != nil {
		return FrozenAnalyticsRequest{}, err
	}
	return result, nil
}

func validateRuntimeGroupPredicate(value FrozenGroupPredicate, model ModelID, depth int) error {
	if depth > 256 {
		return fmt.Errorf("runtime analytics request: having depth exceeds 256")
	}
	switch value.Kind {
	case RuntimeGroupCompare:
		if len(value.Children) != 0 || value.Term.Model != model {
			return fmt.Errorf("runtime analytics request: malformed having comparison")
		}
		switch value.Operator {
		case "eq", "ne", "lt", "lte", "gt", "gte", "isNull", "isNotNull", "contains", "startsWith", "endsWith":
		default:
			return fmt.Errorf("runtime analytics request: unknown having operator")
		}
	case RuntimeGroupAnd, RuntimeGroupOr:
		if len(value.Children) == 0 {
			return fmt.Errorf("runtime analytics request: empty having logical node")
		}
	case RuntimeGroupNot:
		if len(value.Children) != 1 {
			return fmt.Errorf("runtime analytics request: NOT requires one child")
		}
	default:
		return fmt.Errorf("runtime analytics request: unknown having node")
	}
	for _, child := range value.Children {
		if err := validateRuntimeGroupPredicate(child, model, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// RuntimeAnalyticsCellValue exposes only the decoded provider-neutral cell
// carried from the shared runtime planner to the generated GraphQL encoder.
func RuntimeAnalyticsCellValue(cell RuntimeAnalyticsCell) (key string, state ReadState, value any) {
	return cell.key, cell.state, cell.value
}
