package queryplan

import internalreport "github.com/eleven-am/golem/go/internal/queryplanreport"

// ErrorCode is the closed query-plan failure classification.
type ErrorCode string

const (
	ErrorUnavailable ErrorCode = "PLAN_UNAVAILABLE"
	ErrorTooComplex  ErrorCode = "PLAN_TOO_COMPLEX"
	ErrorInvalid     ErrorCode = "PLAN_INVALID"
)

// CodeOf recognizes a query-plan failure through ordinary Go error wrapping.
// It never classifies an error from its public text.
func CodeOf(err error) (ErrorCode, bool) {
	code, ok := internalreport.CodeOf(err)
	if !ok {
		return "", false
	}
	switch code {
	case internalreport.CodeUnavailable:
		return ErrorUnavailable, true
	case internalreport.CodeTooComplex:
		return ErrorTooComplex, true
	case internalreport.CodeInvalid:
		return ErrorInvalid, true
	default:
		return "", false
	}
}
