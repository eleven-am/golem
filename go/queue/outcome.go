package queue

import (
	"errors"
	"math/rand/v2"
	"time"
)

// Resolution is the durable disposition one handler return maps to.
type Resolution string

const (
	// ResolutionSucceeded terminates the job successfully.
	ResolutionSucceeded Resolution = "succeeded"
	// ResolutionRetry returns the job to pending and consumes an attempt.
	ResolutionRetry Resolution = "retry"
	// ResolutionFailed terminates the job as a permanent refusal.
	ResolutionFailed Resolution = "failed"
)

// InvalidCode replaces a recorded code a handler supplied in non-canonical
// form. The error itself is never discarded.
const InvalidCode = "invalid_code"

// Outcome is the classification of one handler return. Scheduled reports
// whether Delay was stated by the handler rather than left to the backoff.
type Outcome struct {
	Resolution Resolution
	Code       string
	Delay      time.Duration
	Scheduled  bool
	Uncounted  bool
	Err        error
}

type terminalError struct{ err error }

func (failure terminalError) Error() string {
	if failure.err == nil {
		return "queue: terminal failure"
	}
	return failure.err.Error()
}

func (failure terminalError) Unwrap() error { return failure.err }

type retryError struct {
	delay     time.Duration
	uncounted bool
	err       error
}

func (failure retryError) Error() string {
	if failure.err == nil {
		return "queue: retry requested"
	}
	return failure.err.Error()
}

func (failure retryError) Unwrap() error { return failure.err }

type completedError struct {
	code string
	err  error
}

func (failure completedError) Error() string {
	if failure.err == nil {
		return "queue: completed with " + failure.code
	}
	return failure.err.Error()
}

func (failure completedError) Unwrap() error { return failure.err }

// Terminal marks a handler failure as permanent. The job moves to failed with
// its remaining attempts unspent.
func Terminal(err error) error { return terminalError{err: err} }

// RetryIn overrides the scheduled delay of an ordinary retry. The attempt is
// still consumed.
func RetryIn(delay time.Duration, err error) error {
	if delay < 0 {
		delay = 0
	}
	if delay > maximumDelay {
		delay = maximumDelay
	}
	return retryError{delay: delay, err: err}
}

// RetryInWithoutAttempt reschedules work without spending the claimed attempt.
// It is intended for external capacity deferrals, not handler failures. A
// handler that always returns it produces a job that never exhausts its
// attempts and so never dead-letters: the row stays live, and automatic
// retention only deletes terminal rows, so nothing ages it out. Bound the
// deferral on something other than the attempt count.
func RetryInWithoutAttempt(delay time.Duration, err error) error {
	if delay < 0 {
		delay = 0
	}
	if delay > maximumDelay {
		delay = maximumDelay
	}
	return retryError{delay: delay, uncounted: true, err: err}
}

// CompletedWith terminates the job successfully while durably recording a code
// describing what degraded.
func CompletedWith(code string, err error) error {
	return completedError{code: canonicalCode(code), err: err}
}

// Classify maps one handler return onto the durable outcome vocabulary. A nil
// error succeeds, a plain error retries, and the sentinels override both.
func Classify(err error) Outcome {
	if err == nil {
		return Outcome{Resolution: ResolutionSucceeded}
	}
	var completed completedError
	if errors.As(err, &completed) {
		return Outcome{Resolution: ResolutionSucceeded, Code: completed.code, Err: err}
	}
	var terminal terminalError
	if errors.As(err, &terminal) {
		return Outcome{Resolution: ResolutionFailed, Err: err}
	}
	var retry retryError
	if errors.As(err, &retry) {
		return Outcome{Resolution: ResolutionRetry, Delay: retry.delay, Scheduled: true, Uncounted: retry.uncounted, Err: err}
	}
	return Outcome{Resolution: ResolutionRetry, Err: err}
}

// Delay returns the wait before the given one-based attempt is retried, as
// exponential full jitter under the configured ceiling.
func (backoff Backoff) Delay(attempt int) time.Duration {
	base := orDefaultDuration(backoff.Base, defaultBackoffBase)
	ceiling := orDefaultDuration(backoff.Cap, defaultBackoffCap)
	if attempt < 1 {
		attempt = 1
	}
	window := base
	for step := 1; step < attempt && window < ceiling; step++ {
		window *= 2
	}
	if window > ceiling || window <= 0 {
		window = ceiling
	}
	return time.Duration(rand.Int64N(int64(window)))
}

func canonicalCode(code string) string {
	if canonicalName.MatchString(code) {
		return code
	}
	return InvalidCode
}
