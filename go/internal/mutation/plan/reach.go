package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func WriteReach(policies PolicySet, model policyir.ModelID, condition policyir.Condition, readDepth int) (policyir.Condition, error) {
	if policies == nil {
		return policyir.Condition{}, fmt.Errorf("write reach requires caller policies")
	}
	return readplan.ReachWithin(policies, model, condition, readDepth)
}

func WriteLens(policies PolicySet, model policyir.ModelID, readDepth int) classify.Reach {
	return readplan.ReadLens(policies, model, readDepth)
}

func ExistingConstraint(policies PolicySet, model policyir.ModelID, row policyir.Condition, fields []policyir.FieldID, readDepth int, position ...policyir.Condition) (policyir.Condition, error) {
	expanded, err := WriteReach(policies, model, row, readDepth)
	if err != nil {
		return policyir.Condition{}, err
	}
	base, err := conjoin(model, append([]policyir.Condition{expanded}, position...)...)
	if err != nil {
		return policyir.Condition{}, err
	}
	conditions := []policyir.Condition{base}
	for _, field := range fields {
		reach, reachErr := readplan.ReadFieldReach(policies, model, field, readDepth)
		if reachErr != nil {
			return policyir.Condition{}, reachErr
		}
		if _, constant := reach.Constant(); constant {
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

func (request RootRequest) writeReach(condition policyir.Condition) (policyir.Condition, error) {
	return WriteReach(request.Policies, request.Model, condition, request.ReadRelationDepth)
}
