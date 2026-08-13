package provider

import (
	"regexp"
	"testing"
	"time"
)

func TestCoordinatorValidationTokenAndFactOwnership(t *testing.T) {
	if err := ValidateClaim(ClaimOptions{Groups: 1, LeaseDuration: time.Microsecond}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateClaim(ClaimOptions{}); err == nil {
		t.Fatal("empty claim accepted")
	}
	if err := ValidateFailureCode("provider.retry", true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFailureCode("PRIVATE ERROR", true); err == nil {
		t.Fatal("noncanonical failure code accepted")
	}
	token, err := NewLeaseToken()
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(token) {
		t.Fatalf("token=%q err=%v", token, err)
	}
	rows := []FactRow{{BeforeIdentity: []byte{1}, AfterIdentity: []byte{2}, Metadata: []byte{3}, DeleteSnapshot: []byte{4}}}
	clone := CloneFacts(rows)
	clone[0].BeforeIdentity[0], clone[0].AfterIdentity[0], clone[0].Metadata[0], clone[0].DeleteSnapshot[0] = 9, 9, 9, 9
	if rows[0].BeforeIdentity[0] != 1 || rows[0].AfterIdentity[0] != 2 || rows[0].Metadata[0] != 3 || rows[0].DeleteSnapshot[0] != 4 {
		t.Fatal("cloned fact bytes alias source")
	}
}
