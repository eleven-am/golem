package queue

import (
	"context"
	"testing"
	"time"
)

type failurePayload struct {
	Name string `json:"name"`
}

func TestDecodeFailureCarriesMetaAndPayload(t *testing.T) {
	registry := NewRegistry()
	jobType, err := Register(registry, Definition[failurePayload]{Type: "failure.decode", Handle: func(context.Context, Job[failurePayload]) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if jobType.Name() != "failure.decode" {
		t.Fatalf("name = %q", jobType.Name())
	}
	enqueued := time.Unix(100, 0).UTC()
	meta := Meta{ID: "job-1", Attempt: 3, MaxAttempts: 3, EnqueuedAt: enqueued}
	failure := jobType.DecodeFailure(meta, []byte(`{"name":"x"}`), FailureAttemptsExhausted)
	if failure.ID != "job-1" || failure.Payload.Name != "x" || failure.Code != FailureAttemptsExhausted || failure.Attempt != 3 || failure.MaxAttempts != 3 || !failure.EnqueuedAt.Equal(enqueued) {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestDecodeFailureReportsUndecodablePayload(t *testing.T) {
	registry := NewRegistry()
	jobType, err := Register(registry, Definition[failurePayload]{Type: "failure.invalid", Handle: func(context.Context, Job[failurePayload]) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	failure := jobType.DecodeFailure(Meta{ID: "job-2"}, []byte(`[1,2]`), FailureTerminal)
	if failure.Code != FailurePayloadInvalid || failure.Payload.Name != "" || failure.ID != "job-2" {
		t.Fatalf("failure = %+v", failure)
	}
}
