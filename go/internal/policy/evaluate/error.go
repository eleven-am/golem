// Package evaluate owns the one provider-neutral in-memory policy evaluator.
package evaluate

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type ErrorCode string

const (
	CodeMissing  ErrorCode = "P2_EVAL_MISSING"
	CodeModel    ErrorCode = "P2_EVAL_MODEL"
	CodeType     ErrorCode = "P2_EVAL_TYPE"
	CodeOperator ErrorCode = "P2_EVAL_OPERATOR"
	CodeInternal ErrorCode = "P2_EVAL_INTERNAL"
)

type Error struct {
	Code       ErrorCode
	Detail     string
	ModelID    ir.ModelID
	FieldID    ir.FieldID
	OperatorID ir.OperatorID
	Cause      error
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	result := string(failure.Code)
	if failure.ModelID != (ir.ModelID{}) {
		result += " model=" + hex.EncodeToString(failure.ModelID[:])
	}
	if failure.FieldID != (ir.FieldID{}) {
		result += " field=" + hex.EncodeToString(failure.FieldID[:])
	}
	if failure.OperatorID != 0 {
		result += fmt.Sprintf(" operator=%d", failure.OperatorID)
	}
	if failure.Detail != "" {
		result += ": " + failure.Detail
	}
	return result
}

func (failure *Error) Unwrap() error { return failure.Cause }
