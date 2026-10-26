package runtime

import (
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

type mutationLimitError struct {
	limit golem.Limit
	max   int
}

func (failure *mutationLimitError) Error() string {
	return fmt.Sprintf("P4_MUTATION_LIMIT: %s exceeds %d", failure.limit, failure.max)
}

func reasonMessage(reason golem.Reason, subject string, maximum int) string {
	switch reason {
	case golem.ReasonValueTooLong:
		return fmt.Sprintf("value for %s exceeds maximum length %d", subject, maximum)
	case golem.ReasonValuePrecision:
		return fmt.Sprintf("value for %s exceeds the declared precision", subject)
	case golem.ReasonValueRange:
		return fmt.Sprintf("value for %s is outside the supported range", subject)
	case golem.ReasonInvalidValue:
		return fmt.Sprintf("value for %s is invalid", subject)
	case golem.ReasonLimitExceeded:
		return fmt.Sprintf("request exceeds the %s limit of %d", subject, maximum)
	case golem.ReasonNotGranted:
		return "operation is not granted"
	case golem.ReasonFieldNotReadable:
		return "field is not readable in this position"
	case golem.ReasonAuthorizationUnproven:
		return "authorization could not be proven"
	case golem.ReasonPolicyInvalid:
		return "policy cannot be applied to this request"
	default:
		return "request is invalid"
	}
}

func limitFailure(err error) (golem.Limit, int, bool) {
	var state *mutationLimitError
	if errors.As(err, &state) {
		return state.limit, state.max, true
	}
	var batch *mutationbatch.Error
	if errors.As(err, &batch) && batch.Code == mutationbatch.CodeLimit && batch.Limit != "" {
		return batch.Limit, batch.Maximum, true
	}
	var nested *mutationnested.Error
	if errors.As(err, &nested) && nested.Limit != "" {
		return nested.Limit, nested.Maximum, true
	}
	var planned *readplan.Error
	if errors.As(err, &planned) && planned.Limit != "" {
		return planned.Limit, planned.Maximum, true
	}
	var rendered *readsql.Error
	if errors.As(err, &rendered) && rendered.Limit != "" {
		return rendered.Limit, rendered.Maximum, true
	}
	return "", 0, false
}

func publicLimitError(operation string, model golem.ModelID, err error) (error, bool) {
	limit, maximum, ok := limitFailure(err)
	if !ok {
		return nil, false
	}
	return golem.RuntimeLimitError(operation, model, limit, maximum, reasonMessage(golem.ReasonLimitExceeded, string(limit), maximum), err), true
}

func publicValueError(operation string, model golem.ModelID, err error) (error, bool) {
	var bound *mutationbind.Error
	if !errors.As(err, &bound) || bound.Code != mutationbind.CodeValue {
		return nil, false
	}
	var value *mutationbind.ValueError
	if !errors.As(bound.Cause, &value) {
		return nil, false
	}
	subject := bound.FieldName
	if bound.ModelName != "" {
		subject = bound.ModelName + "." + bound.FieldName
	}
	return golem.RuntimeOperationErrorWithReason(golem.CodeBadUserInput, value.Reason, operation, model, bound.Field, reasonMessage(value.Reason, subject, int(value.Maximum)), err), true
}

func rebasePublicError(public *golem.Error, operation string, model golem.ModelID, message string, cause error) error {
	rebased := golem.RuntimeOperationErrorWithReason(public.Code, public.Reason, operation, model, public.Field, message, cause).(*golem.Error)
	rebased.Limit = public.Limit
	rebased.LimitMax = public.LimitMax
	return rebased
}

func publicReadPlanError(operation string, model golem.ModelID, request readir.Request, cause error) (error, bool) {
	if limited, ok := publicLimitError(operation, model, cause); ok {
		return limited, true
	}
	var failure *readplan.Error
	if !errors.As(cause, &failure) || (failure.Code != readplan.CodeField && failure.Code != readplan.CodePolicy && failure.Reason == "") {
		return nil, false
	}
	field := golem.FieldID{}
	if failure.Field != (policyir.FieldID{}) && requestMentionsField(request, failure.Field) {
		field = golem.FieldID(failure.Field)
	}
	if failure.Reason == "" {
		return golem.RuntimeReadError(golem.CodeForbidden, operation, model, field, "read is not permitted", cause), true
	}
	return golem.RuntimeOperationErrorWithReason(golem.CodeForbidden, failure.Reason, operation, model, field, reasonMessage(failure.Reason, "", 0), cause), true
}

func publicReadRenderError(operation string, model golem.ModelID, message string, cause error) error {
	if limited, ok := publicLimitError(operation, model, cause); ok {
		return limited
	}
	return golem.RuntimeReadError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, message, cause)
}

func requestMentionsField(request readir.Request, field policyir.FieldID) bool {
	if where, ok := request.Where(); ok && conditionMentionsField(where, field) {
		return true
	}
	for _, order := range request.OrderBy() {
		if order.FieldID() == field {
			return true
		}
	}
	for _, candidate := range request.Distinct() {
		if candidate == field {
			return true
		}
	}
	for _, candidate := range request.Omitted() {
		if candidate == field {
			return true
		}
	}
	if selector, ok := request.Selector(); ok {
		for _, candidate := range selector.Fields() {
			if candidate == field {
				return true
			}
		}
	}
	if cursor, ok := request.Cursor(); ok {
		for _, candidate := range cursor.Selector().Fields() {
			if candidate == field {
				return true
			}
		}
	}
	for _, selection := range request.Selection() {
		if selection.FieldID() == field {
			return true
		}
		if child, ok := selection.Request(); ok && requestMentionsField(child, field) {
			return true
		}
	}
	return false
}

func conditionMentionsField(condition policyir.Condition, field policyir.FieldID) bool {
	if candidate, ok := condition.Field(); ok && candidate == field {
		return true
	}
	if _, children, ok := condition.Logical(); ok {
		for _, child := range children {
			if conditionMentionsField(child, field) {
				return true
			}
		}
	}
	if _, _, _, _, child, ok := condition.Relation(); ok && child != nil {
		return conditionMentionsField(*child, field)
	}
	return false
}
