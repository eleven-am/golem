package runtime

import (
	"errors"
	"fmt"
)

type queryValidationError struct {
	reason string
}

func InvalidQuery(format string, arguments ...any) error {
	return &queryValidationError{reason: fmt.Sprintf(format, arguments...)}
}

func (failure *queryValidationError) Error() string {
	if failure == nil {
		return ""
	}
	return "P9_FULLTEXT_QUERY: " + failure.reason
}

func QueryValidationReason(err error) (string, bool) {
	var failure *queryValidationError
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.reason, true
}

type queryExecutionError struct {
	detail string
	cause  error
}

func queryExecutionFailure(detail string, cause error) error {
	return &queryExecutionError{detail: detail, cause: cause}
}

func (failure *queryExecutionError) Error() string {
	if failure == nil {
		return ""
	}
	return "P9_FULLTEXT_QUERY: " + failure.detail
}

func (failure *queryExecutionError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}
