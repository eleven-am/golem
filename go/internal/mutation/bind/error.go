// Package bind validates frozen public mutations against the active immutable
// schema registry and converts them into provider-neutral mutation IR. It does
// no provider work and owns no SQL vocabulary.
package bind

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

// ErrorCode is a stable mutation-binding failure category. Detail and Cause
// are trusted diagnostics; Error deliberately exposes no provider or SQL text.
type ErrorCode string

const (
	CodeInput     ErrorCode = "P4_BIND_INPUT"
	CodeModel     ErrorCode = "P4_BIND_MODEL"
	CodeField     ErrorCode = "P4_BIND_FIELD"
	CodeExposure  ErrorCode = "P4_BIND_EXPOSURE"
	CodeRequired  ErrorCode = "P4_BIND_REQUIRED"
	CodeOperation ErrorCode = "P4_BIND_OPERATION"
	CodeValue     ErrorCode = "P4_BIND_VALUE"
	CodeTarget    ErrorCode = "P4_BIND_TARGET"
	CodeGuard     ErrorCode = "P4_BIND_GUARD"
	CodeInternal  ErrorCode = "P4_BIND_INTERNAL"
)

type Error struct {
	Code   ErrorCode
	Model  golem.ModelID
	Field  golem.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return fmt.Sprintf("%s: model=%x field=%x: %s", failure.Code, failure.Model, failure.Field, failure.Detail)
}

func (failure *Error) Unwrap() error { return failure.Cause }

func fail(code ErrorCode, model golem.ModelID, field golem.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}
