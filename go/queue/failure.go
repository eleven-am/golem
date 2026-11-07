package queue

import (
	"encoding/json"
	"time"
)

type FailureCode string

const (
	FailureTerminal          FailureCode = "terminal"
	FailureAttemptsExhausted FailureCode = "attempts_exhausted"
	FailurePayloadInvalid    FailureCode = "payload_invalid"
)

type Failure[T any] struct {
	ID          JobID
	Payload     T
	Code        FailureCode
	Attempt     int
	MaxAttempts int
	EnqueuedAt  time.Time
}

func (jobType Type[T]) Name() string {
	return jobType.name
}

func (jobType Type[T]) DecodeFailure(meta Meta, payload []byte, code FailureCode) Failure[T] {
	failure := Failure[T]{ID: meta.ID, Code: code, Attempt: meta.Attempt, MaxAttempts: meta.MaxAttempts, EnqueuedAt: meta.EnqueuedAt}
	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		failure.Code = FailurePayloadInvalid
		return failure
	}
	failure.Payload = value
	return failure
}
