// Package plan derives immutable provider-neutral mutation authorization plans.
// It performs no SQL rendering, transaction work, or row authorization.
package plan

import (
	"fmt"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type ErrorCode string

const (
	CodeRequest        ErrorCode = "P4_PLAN_REQUEST"
	CodePolicy         ErrorCode = "P4_PLAN_POLICY"
	CodeClassification ErrorCode = "P4_PLAN_CLASSIFICATION"
	CodeRequirements   ErrorCode = "P4_PLAN_REQUIREMENTS"
	CodeIR             ErrorCode = "P4_PLAN_IR"
	CodeExposure       ErrorCode = "P4_PLAN_EXPOSURE"
)

// Error contains logical identities only. Cause is trusted diagnostic context;
// Error intentionally omits predicates, selector values, and provider details.
type Error struct {
	Code      ErrorCode
	Operation mutationir.Operation
	Model     policyir.ModelID
	Field     policyir.FieldID
	Detail    string
	Cause     error
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return fmt.Sprintf("%s: operation=%d model=%x field=%x: %s", failure.Code, failure.Operation, failure.Model, failure.Field, failure.Detail)
}

func (failure *Error) Unwrap() error { return failure.Cause }

func fail(code ErrorCode, request RootRequest, field policyir.FieldID, detail string, cause error) error {
	return &Error{Code: code, Operation: request.Operation, Model: request.Model, Field: field, Detail: detail, Cause: cause}
}
