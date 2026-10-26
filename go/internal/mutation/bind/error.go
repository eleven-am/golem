// Package bind validates frozen public mutations against the active immutable
// schema registry and converts them into provider-neutral mutation IR. It does
// no provider work and owns no SQL vocabulary.
package bind

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schema"
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

	CodeForeignKeyArithmetic ErrorCode = "P4_BIND_FOREIGN_KEY_ARITHMETIC"
)

type Error struct {
	Code      ErrorCode
	Model     golem.ModelID
	Field     golem.FieldID
	ModelName string
	FieldName string
	Detail    string
	Cause     error
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

func failValue(registry *schema.Registry, field schema.Field, fieldID golem.FieldID, detail string, cause error) error {
	failure := &Error{Code: CodeValue, Model: field.ModelID(), Field: fieldID, FieldName: field.LogicalName(), Detail: detail, Cause: cause}
	if model, ok := registry.Model(field.ModelID()); ok {
		failure.ModelName = model.LogicalName()
	}
	return failure
}
