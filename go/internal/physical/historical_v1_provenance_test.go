package physical

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV1TaggedValidatorAndNormalizerSourceProvenance(t *testing.T) {
	const (
		validatorUpstreamSHA  = "bf4ef0b2ee7eeaa82ade0ab35a4a548bba580d1780aaee5b4f3a0c79bd75c35b"
		validatorLines        = 819
		normalizerUpstreamSHA = "e2af7ff44eec451af2fbb4e3bc018c6dc60237821da65b079818c47e6a024714"
		normalizerLines       = 261
		validatorAdaptedSHA   = "054d6e64efd4a0a058795c954e37125a84170c15d40f1efd054705c8ffca97ce"
		normalizerAdaptedSHA  = "5a2ea4aea8e0347c4436fa25af376e0f4c11f04df6fb0af17ecfc21dcb7af145"
	)
	if historicalV1ValidateUpstreamSHA256 != validatorUpstreamSHA || historicalV1ValidateUpstreamLines != validatorLines ||
		historicalV1NormalizeUpstreamSHA256 != normalizerUpstreamSHA || historicalV1NormalizeUpstreamLines != normalizerLines {
		t.Fatal("retained physical v1 provenance constants changed")
	}
	_, current, _, _ := runtime.Caller(0)
	for name, want := range map[string]string{
		"historical_v1_validate_tagged.go":  validatorAdaptedSHA,
		"historical_v1_normalize_tagged.go": normalizerAdaptedSHA,
	} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Fatalf("retained %s adaptation changed: got %s want %s", name, got, want)
		}
	}
}
