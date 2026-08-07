package events

import "errors"

type ErrorCode string

const (
	CodeEventConfig              ErrorCode = "GOLEM_EVENT_CONFIG"
	CodeEventCodec               ErrorCode = "GOLEM_EVENT_CODEC"
	CodeEventSourceClosed        ErrorCode = "GOLEM_EVENT_SOURCE_CLOSED"
	CodeEventTransport           ErrorCode = "GOLEM_EVENT_TRANSPORT"
	CodeEventPublisherRunning    ErrorCode = "GOLEM_EVENT_PUBLISHER_RUNNING"
	CodeEventPoison              ErrorCode = "GOLEM_EVENT_POISON"
	CodeSubscriptionInvalid      ErrorCode = "GOLEM_SUBSCRIPTION_INVALID"
	CodeSubscriptionOverflow     ErrorCode = "GOLEM_SUBSCRIPTION_OVERFLOW"
	CodeSubscriptionRevalidation ErrorCode = "GOLEM_SUBSCRIPTION_REVALIDATION"
	CodeSubscriptionSourceClosed ErrorCode = "GOLEM_SUBSCRIPTION_SOURCE_CLOSED"
	CodeSubscriptionCancelled    ErrorCode = "GOLEM_SUBSCRIPTION_CANCELLED"
	CodeCDCInvalid               ErrorCode = "GOLEM_CDC_INVALID"
	CodeCDCUnavailable           ErrorCode = "GOLEM_CDC_UNAVAILABLE"
)

type Error struct{ code ErrorCode }

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return string(failure.code)
}
func (failure *Error) Code() ErrorCode {
	if failure == nil {
		return ""
	}
	return failure.code
}
func (failure *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && failure != nil && other != nil && failure.code == other.code
}

func Failure(code ErrorCode) error { return &Error{code: code} }

func CodeOf(err error) (ErrorCode, bool) {
	var failure *Error
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.code, true
}
