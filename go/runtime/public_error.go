package runtime

import (
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	scoped "github.com/eleven-am/golem/go/internal/scoped"
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

type limitDetail struct {
	limit   golem.Limit
	maximum int
	field   golem.FieldID
}

func limitFailure(err error) (limitDetail, bool) {
	var state *mutationLimitError
	if errors.As(err, &state) {
		return limitDetail{limit: state.limit, maximum: state.max}, true
	}
	var batch *mutationbatch.Error
	if errors.As(err, &batch) && batch.Code == mutationbatch.CodeLimit && batch.Limit != "" {
		return limitDetail{limit: batch.Limit, maximum: batch.Maximum}, true
	}
	var nested *mutationnested.Error
	if errors.As(err, &nested) && nested.Limit != "" {
		return limitDetail{limit: nested.Limit, maximum: nested.Maximum, field: nested.Field}, true
	}
	var planned *readplan.Error
	if errors.As(err, &planned) && planned.Limit != "" {
		return limitDetail{limit: planned.Limit, maximum: planned.Maximum}, true
	}
	var rendered *readsql.Error
	if errors.As(err, &rendered) && rendered.Limit != "" {
		return limitDetail{limit: rendered.Limit, maximum: rendered.Maximum}, true
	}
	return limitDetail{}, false
}

func publicLimitError(operation string, model golem.ModelID, err error) (error, bool) {
	detail, ok := limitFailure(err)
	if !ok {
		return nil, false
	}
	return publicLimit(operation, model, detail.limit, detail.maximum, detail.field, err), true
}

func publicLimit(operation string, model golem.ModelID, limit golem.Limit, maximum int, field golem.FieldID, cause error) error {
	failure := golem.RuntimeLimitError(operation, model, limit, maximum, reasonMessage(golem.ReasonLimitExceeded, string(limit), maximum), cause).(*golem.Error)
	failure.Field = field
	return failure
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

func publicBindReasonError(registry *schema.Registry, operation string, model golem.ModelID, err error) (error, bool) {
	var bound *policybind.Error
	if !errors.As(err, &bound) || bound.Reason == "" {
		return nil, false
	}
	field := golem.FieldID(bound.FieldID)
	return golem.RuntimeOperationErrorWithReason(golem.CodeBadUserInput, bound.Reason, operation, model, field, reasonMessage(bound.Reason, fieldSubject(registry, golem.ModelID(bound.ModelID), field), 0), err), true
}

func publicScopedValueError(model golem.ModelID, err error) (error, bool) {
	var value *scoped.ValueError
	if !errors.As(err, &value) {
		return nil, false
	}
	subject := value.FieldName
	if value.ModelName != "" {
		subject = value.ModelName + "." + value.FieldName
	}
	return golem.RuntimeOperationErrorWithReason(golem.CodeBadUserInput, value.Reason, "scoped", model, value.Field, reasonMessage(value.Reason, subject, 0), err), true
}

func fieldSubject(registry *schema.Registry, model golem.ModelID, field golem.FieldID) string {
	if registry == nil {
		return "field"
	}
	fact, ok := registry.Field(model, field)
	if !ok {
		return "field"
	}
	if declaration, ok := registry.Model(model); ok {
		return declaration.LogicalName() + "." + fact.LogicalName()
	}
	return fact.LogicalName()
}

func rebasePublicError(public *golem.Error, operation string, model golem.ModelID, message string, cause error) error {
	rebased := golem.RuntimeOperationErrorWithReason(public.Code, public.Reason, operation, model, public.Field, message, cause).(*golem.Error)
	rebased.Limit = public.Limit
	rebased.LimitMax = public.LimitMax
	return rebased
}

func publicReadPlanError(operation string, model golem.ModelID, request readir.Request, cause error) (error, bool) {
	mentions := func(field policyir.FieldID) bool { return requestMentionsField(request, field) }
	if public, ok := publicReadReasonError(operation, model, mentions, cause); ok {
		return public, true
	}
	var failure *readplan.Error
	if !errors.As(cause, &failure) || (failure.Code != readplan.CodeField && failure.Code != readplan.CodePolicy) {
		return nil, false
	}
	return golem.RuntimeReadError(golem.CodeForbidden, operation, model, namedField(failure.Field, mentions), "read is not permitted", cause), true
}

func publicReadReasonError(operation string, model golem.ModelID, mentions func(policyir.FieldID) bool, cause error) (error, bool) {
	if limited, ok := publicLimitError(operation, model, cause); ok {
		return limited, true
	}
	var failure *readplan.Error
	if !errors.As(cause, &failure) || failure.Reason == "" {
		return nil, false
	}
	return golem.RuntimeOperationErrorWithReason(golem.CodeForbidden, failure.Reason, operation, model, namedField(failure.Field, mentions), reasonMessage(failure.Reason, "", 0), cause), true
}

func namedField(field policyir.FieldID, mentions func(policyir.FieldID) bool) golem.FieldID {
	if field == (policyir.FieldID{}) || mentions == nil || !mentions(field) {
		return golem.FieldID{}
	}
	return golem.FieldID(field)
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
