package queryplanreport

import "errors"

type Code string

const (
	CodeUnavailable Code = "PLAN_UNAVAILABLE"
	CodeTooComplex  Code = "PLAN_TOO_COMPLEX"
	CodeInvalid     Code = "PLAN_INVALID"
)

type planError struct{ code Code }

func (failure *planError) Error() string {
	if failure == nil {
		return "query plan is invalid"
	}
	switch failure.code {
	case CodeUnavailable:
		return "query plan is unavailable"
	case CodeTooComplex:
		return "query plan is too complex"
	default:
		return "query plan is invalid"
	}
}

func fail(code Code) error {
	switch code {
	case CodeUnavailable, CodeTooComplex, CodeInvalid:
		return &planError{code: code}
	default:
		return &planError{code: CodeInvalid}
	}
}

// NewError constructs one fixed, sanitized query-plan failure. Unknown codes
// canonicalize to PLAN_INVALID; this boundary never accepts or retains a raw
// provider cause.
func NewError(code Code) error { return fail(code) }

func CodeOf(err error) (Code, bool) {
	var failure *planError
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	switch failure.code {
	case CodeUnavailable, CodeTooComplex, CodeInvalid:
		return failure.code, true
	default:
		return "", false
	}
}
