package golemtest

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/eleven-am/golem/go/golem"
)

// ErrorCode is the closed classification every error this package returns
// carries. Compare it instead of error text: the wording is not an ABI and may
// change, the codes may not.
type ErrorCode string

const (
	// ErrorInvalidInput reports an argument this package cannot use: a zero or
	// malformed handle, an unknown action, a predicate that cannot be frozen,
	// or a value that did not come from the constructor that produces it.
	ErrorInvalidInput ErrorCode = "INVALID_INPUT"
	// ErrorGenerationMismatch reports artifacts from different generated
	// applications, or from different generations of the same one, being used
	// together.
	ErrorGenerationMismatch ErrorCode = "GENERATION_MISMATCH"
	// ErrorPolicyFactory reports that an application's own policy declaration
	// failed: it returned an error, panicked, or produced a policy that does
	// not match the schema it was declared against. The application's cause and
	// any panic payload are deliberately discarded.
	ErrorPolicyFactory ErrorCode = "POLICY_FACTORY_FAILED"
	// ErrorPolicyAnalysis reports that the production policy kernel declined to
	// answer: a constraint could not be resolved, a predicate could not be
	// bound, or the implication kernel refused the proof. It never means "the
	// answer is no".
	ErrorPolicyAnalysis ErrorCode = "POLICY_ANALYSIS_FAILED"
)

type classifiedFailure interface {
	error
	policyKitErrorCode() ErrorCode
}

type kitError struct {
	code   ErrorCode
	detail string
}

func (failure *kitError) Error() string {
	return "golemtest: " + string(failure.code) + ": " + failure.detail
}

func (failure *kitError) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = state.Write([]byte(strconv.Quote(failure.Error())))
		return
	}
	_, _ = state.Write([]byte(failure.Error()))
}

func (failure *kitError) policyKitErrorCode() ErrorCode { return failure.code }

// CodeOf reports the classification of an error produced by this package. It
// survives ordinary %w wrapping and errors.Join, and it reports false for nil
// and for errors this package did not produce. It never exposes the wrapped
// application cause or a recovered panic payload.
func CodeOf(err error) (ErrorCode, bool) {
	var classified classifiedFailure
	if errors.As(err, &classified) {
		return classified.policyKitErrorCode(), true
	}
	return "", false
}

func fail(code ErrorCode, detail string) error {
	return &kitError{code: code, detail: detail}
}

func failModel(code ErrorCode, detail string, model golem.ModelID) error {
	return &kitError{code: code, detail: detail + " [model " + hex.EncodeToString(model[:]) + "]"}
}

func failField(code ErrorCode, detail string, model golem.ModelID, field golem.FieldID) error {
	return &kitError{code: code, detail: detail + " [model " + hex.EncodeToString(model[:]) + " field " + hex.EncodeToString(field[:]) + "]"}
}
