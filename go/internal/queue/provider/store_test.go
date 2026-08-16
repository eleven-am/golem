package provider

import (
	"testing"
	"time"
)

func TestValidationRefusesUncanonicalWork(t *testing.T) {
	valid := EnqueueRequest{ID: "job", Type: "email.welcome", Payload: []byte(`{}`), MaxAttempts: 1}
	if err := ValidateEnqueue(valid); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name    string
		request EnqueueRequest
	}{
		{name: "empty identity", request: EnqueueRequest{Type: "a.b", Payload: []byte(`{}`), MaxAttempts: 1}},
		{name: "uncanonical type", request: EnqueueRequest{ID: "job", Type: "Email", Payload: []byte(`{}`), MaxAttempts: 1}},
		{name: "empty payload", request: EnqueueRequest{ID: "job", Type: "a.b", MaxAttempts: 1}},
		{name: "oversized payload", request: EnqueueRequest{ID: "job", Type: "a.b", Payload: make([]byte, MaximumPayloadBytes+1), MaxAttempts: 1}},
		{name: "unbounded attempts", request: EnqueueRequest{ID: "job", Type: "a.b", Payload: []byte(`{}`)}},
		{name: "sub-microsecond delay", request: EnqueueRequest{ID: "job", Type: "a.b", Payload: []byte(`{}`), MaxAttempts: 1, Delay: 500 * time.Nanosecond}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if err := ValidateEnqueue(row.request); err == nil {
				t.Fatal("request was accepted")
			}
		})
	}
	if err := ValidateClaim(ClaimOptions{Types: []string{"a.b"}, Limit: MaximumClaimJobs + 1, LeaseDuration: time.Second}); err == nil {
		t.Fatal("oversized claim was accepted")
	}
	if err := ValidateClaim(ClaimOptions{Types: []string{"A.B"}, Limit: 1, LeaseDuration: time.Second}); err == nil {
		t.Fatal("uncanonical claimed type was accepted")
	}
	if err := ValidateLease(11 * time.Minute); err == nil {
		t.Fatal("unbounded lease was accepted")
	}
	if err := ValidateCode("Not Canonical"); err == nil {
		t.Fatal("uncanonical code was accepted")
	}
	if err := ValidateRetention(RetentionPolicy{MaxRows: 1}); err == nil {
		t.Fatal("zero retention floor was accepted")
	}
}

func TestIdentifiersAreDistinctCanonicalUUIDs(t *testing.T) {
	seen := make(map[string]struct{}, 512)
	for index := 0; index < 512; index++ {
		identity, err := NewIdentifier()
		if err != nil {
			t.Fatal(err)
		}
		if len(identity) != 36 || identity[14] != '4' {
			t.Fatalf("identifier %q is not a canonical UUIDv4", identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("identifier %q repeated", identity)
		}
		seen[identity] = struct{}{}
	}
}

func TestCloneRecordsOwnsItsPayload(t *testing.T) {
	source := []Record{{ID: "job", Payload: []byte(`{"a":1}`)}}
	cloned := CloneRecords(source)
	source[0].Payload[0] = 'X'
	if cloned[0].Payload[0] != '{' {
		t.Fatalf("clone aliased the provider buffer: %s", cloned[0].Payload)
	}
}
