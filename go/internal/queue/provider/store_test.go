package provider

import (
	"strconv"
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
	resource := ClaimResource{Name: "shared.api", Concurrency: 3, Costs: map[string]int64{"a.b": 2}}
	if err := ValidateClaim(ClaimOptions{Types: []string{"a.b"}, Limit: 1, LeaseDuration: time.Second, Resource: &resource}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []ClaimResource{
		{},
		{Name: "Shared", Concurrency: 1, Costs: map[string]int64{"a.b": 1}},
		{Name: "shared", Concurrency: 0, Costs: map[string]int64{"a.b": 1}},
		{Name: "shared", Concurrency: 1, Costs: map[string]int64{}},
		{Name: "shared", Concurrency: 1, Costs: map[string]int64{"A.B": 1}},
		{Name: "shared", Concurrency: 1, Costs: map[string]int64{"a.b": 2}},
	} {
		if err := ValidateClaimResource(invalid); err == nil {
			t.Fatalf("resource was accepted: %#v", invalid)
		}
	}
	missing := ClaimResource{Name: "shared", Concurrency: 1, Costs: map[string]int64{"other": 1}}
	if err := ValidateClaim(ClaimOptions{Types: []string{"a.b"}, Limit: 1, LeaseDuration: time.Second, Resource: &missing}); err == nil {
		t.Fatal("claim type absent from its resource was accepted")
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
	retentionFloor := time.Now()
	for _, invalid := range []RetentionPolicy{
		{OlderThan: retentionFloor, MaxRows: 1, States: []State{StatePending}},
		{OlderThan: retentionFloor, MaxRows: 1, States: []State{StateLeased}},
		{OlderThan: retentionFloor, MaxRows: 1, States: []State{StateFailed, StateFailed}},
	} {
		if err := ValidateRetention(invalid); err == nil {
			t.Fatalf("retention state selection was accepted: %#v", invalid.States)
		}
	}
	if states := RetentionStates(RetentionPolicy{}); len(states) != 3 || states[0] != StateSucceeded || states[1] != StateFailed || states[2] != StateCanceled {
		t.Fatalf("default retention states=%#v", states)
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

func TestFailedOperatorValidationIsBoundedAndUnambiguous(t *testing.T) {
	valid := FailedQuery{Types: []string{"email.welcome"}, Limit: 1}
	if err := ValidateFailedQuery(valid); err != nil {
		t.Fatal(err)
	}
	for _, query := range []FailedQuery{
		{},
		{Limit: MaximumOperatorBatch + 1},
		{Types: []string{"Email"}, Limit: 1},
		{Types: []string{"email.welcome", "email.welcome"}, Limit: 1},
		{Limit: 1, Before: &FailedCursor{ID: "job"}},
		{Limit: 1, Before: &FailedCursor{FinishedAt: time.Now()}},
	} {
		if err := ValidateFailedQuery(query); err == nil {
			t.Fatalf("query was accepted: %#v", query)
		}
	}
	if err := ValidateOperatorIDs([]string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperatorIDs(nil); err != nil {
		t.Fatalf("empty recovery batch: %v", err)
	}
	for _, ids := range [][]string{{""}, {"one", "one"}} {
		if err := ValidateOperatorIDs(ids); err == nil {
			t.Fatalf("identities were accepted: %#v", ids)
		}
	}
	oversized := make([]string, MaximumOperatorBatch+1)
	for index := range oversized {
		oversized[index] = "job-" + strconv.Itoa(index)
	}
	if err := ValidateOperatorIDs(oversized); err == nil {
		t.Fatal("oversized recovery batch was accepted")
	}
}

func TestGeneralOperatorValidationIsBoundedAndUnambiguous(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	valid := JobQuery{
		Types:  []string{"email.welcome"},
		States: []State{StatePending, StateFailed},
		Limit:  1,
		Before: &JobCursor{EnqueuedAt: now, ID: "job"},
	}
	if err := ValidateJobQuery(valid); err != nil {
		t.Fatal(err)
	}
	for _, query := range []JobQuery{
		{},
		{Limit: MaximumOperatorBatch + 1},
		{Types: []string{"Email"}, Limit: 1},
		{Types: []string{"email.welcome", "email.welcome"}, Limit: 1},
		{States: []State{"unknown"}, Limit: 1},
		{States: []State{StatePending, StatePending}, Limit: 1},
		{Limit: 1, Before: &JobCursor{ID: "job"}},
		{Limit: 1, Before: &JobCursor{EnqueuedAt: now}},
	} {
		if err := ValidateJobQuery(query); err == nil {
			t.Fatalf("query was accepted: %#v", query)
		}
	}
	if err := ValidateCountQuery(CountQuery{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCountQuery(CountQuery{Types: []string{"email.welcome"}}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []CountQuery{
		{Types: []string{"Email"}},
		{Types: []string{"email.welcome", "email.welcome"}},
	} {
		if err := ValidateCountQuery(query); err == nil {
			t.Fatalf("count query was accepted: %#v", query)
		}
	}
}
