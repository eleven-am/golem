package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func WriteReach(policies PolicySet, model policyir.ModelID, condition policyir.Condition, readDepth int) (policyir.Condition, error) {
	if policies == nil {
		return policyir.Condition{}, fmt.Errorf("write reach requires caller policies")
	}
	return readplan.ReachWithin(policies, model, condition, readDepth)
}

func RowConstraint(policies PolicySet, policy policyir.Policy, action policyir.Action, model policyir.ModelID) (policyir.Condition, error) {
	return readplan.RowConstraint(policies, policy, action, model)
}

func FieldCondition(policies PolicySet, policy policyir.Policy, action policyir.Action, model policyir.ModelID, field policyir.FieldID) (policyir.Condition, error) {
	return readplan.FieldCondition(policies, policy, action, model, field)
}

func RowReach(policies PolicySet, model policyir.ModelID, action policyir.Action, row policyir.Condition, readDepth int) (policyir.Condition, error) {
	if policies == nil {
		return WriteReach(policies, model, row, readDepth)
	}
	depth := readplan.ReachDepth(readDepth)
	key := policyruntime.MemoKey{Kind: policyruntime.MemoWriteRowReach, Model: model, Action: action, Depth: depth}
	return readplan.Memoize(policies, key, func(source readplan.PolicySet) (policyir.Condition, error) {
		policy, ok := source.Policy(model)
		if !ok {
			return policyir.Condition{}, fmt.Errorf("write reach policy is absent")
		}
		row, err := resolve.RowConstraint(policy, action, model)
		if err != nil {
			return policyir.Condition{}, err
		}
		return readplan.ReachWithin(source, model, row, depth)
	}, func() (policyir.Condition, error) {
		return WriteReach(policies, model, row, readDepth)
	})
}

func FieldReach(policies PolicySet, model policyir.ModelID, action policyir.Action, field policyir.FieldID, condition policyir.Condition, readDepth int) (policyir.Condition, error) {
	if policies == nil {
		return WriteReach(policies, model, condition, readDepth)
	}
	depth := readplan.ReachDepth(readDepth)
	key := policyruntime.MemoKey{Kind: policyruntime.MemoWriteFieldReach, Model: model, Action: action, Field: field, Depth: depth}
	return readplan.Memoize(policies, key, func(source readplan.PolicySet) (policyir.Condition, error) {
		policy, ok := source.Policy(model)
		if !ok {
			return policyir.Condition{}, fmt.Errorf("write reach policy is absent")
		}
		condition, err := resolve.FieldCondition(policy, action, model, field)
		if err != nil {
			return policyir.Condition{}, err
		}
		return readplan.ReachWithin(source, model, condition, depth)
	}, func() (policyir.Condition, error) {
		return WriteReach(policies, model, condition, readDepth)
	})
}

func WriteLens(policies PolicySet, model policyir.ModelID, readDepth int) classify.Reach {
	return readplan.ReadLens(policies, model, readDepth)
}

func ExistingConstraint(policies PolicySet, model policyir.ModelID, row policyir.Condition, fields []policyir.FieldID, readDepth int, position ...policyir.Condition) (policyir.Condition, error) {
	expanded, err := WriteReach(policies, model, row, readDepth)
	if err != nil {
		return policyir.Condition{}, err
	}
	return existingConstraint(policies, model, expanded, fields, readDepth, position)
}

func ExistingActionConstraint(policies PolicySet, model policyir.ModelID, action policyir.Action, row policyir.Condition, fields []policyir.FieldID, readDepth int, position ...policyir.Condition) (policyir.Condition, error) {
	expanded, err := RowReach(policies, model, action, row, readDepth)
	if err != nil {
		return policyir.Condition{}, err
	}
	return existingConstraint(policies, model, expanded, fields, readDepth, position)
}

func existingConstraint(policies PolicySet, model policyir.ModelID, expanded policyir.Condition, fields []policyir.FieldID, readDepth int, position []policyir.Condition) (policyir.Condition, error) {
	base, err := conjoin(model, append([]policyir.Condition{expanded}, position...)...)
	if err != nil {
		return policyir.Condition{}, err
	}
	return Readable(policies, model, base, fields, readDepth)
}

func Readable(policies PolicySet, model policyir.ModelID, base policyir.Condition, fields []policyir.FieldID, readDepth int) (policyir.Condition, error) {
	if len(fields) == 0 {
		return ReadableConstraint(policies, model, base, readDepth)
	}
	conditions := []policyir.Condition{base}
	for _, field := range fields {
		reach, reachErr := readplan.ReadFieldReach(policies, model, field, readDepth)
		if reachErr != nil {
			return policyir.Condition{}, reachErr
		}
		if truth, constant := reach.Constant(); constant && truth {
			continue
		}
		proved, implyErr := imply.Condition(base, reach)
		if implyErr != nil {
			return policyir.Condition{}, implyErr
		}
		if !proved {
			conditions = append(conditions, reach)
		}
	}
	if len(conditions) == 1 {
		return base, nil
	}
	return conjoin(model, conditions...)
}

func ReadableConstraint(policies PolicySet, model policyir.ModelID, complete policyir.Condition, readDepth int) (policyir.Condition, error) {
	if policies == nil {
		return policyir.Condition{}, fmt.Errorf("readable constraint requires caller policies")
	}
	readable, err := readplan.ReadReach(policies, model, readDepth)
	if err != nil {
		return policyir.Condition{}, err
	}
	if truth, constant := readable.Constant(); constant && truth {
		return complete, nil
	}
	proved, err := imply.Condition(complete, readable)
	if err != nil {
		return policyir.Condition{}, err
	}
	if proved {
		return complete, nil
	}
	return conjoin(model, complete, readable)
}

func ActionName(action policyir.Action) string {
	switch action {
	case policyir.ActionRead:
		return "read"
	case policyir.ActionCreate:
		return "create"
	case policyir.ActionUpdate:
		return "update"
	case policyir.ActionDelete:
		return "delete"
	default:
		return "unknown"
	}
}

func failReason(code ErrorCode, reason golem.Reason, request RootRequest, field policyir.FieldID, detail string, cause error) error {
	return &Error{Code: code, Reason: reason, Operation: request.Operation, Model: request.Model, Field: field, Detail: detail, Cause: cause}
}
