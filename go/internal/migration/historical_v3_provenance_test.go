package migration

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV3RetainedPlannerAdaptedSourceProvenance(t *testing.T) {
	// This digest pins the reviewed retained adaptation. There is no claimed
	// byte-exact upstream tag; current-vs-frozen plan parity owns equivalence.
	// Re-pinned deliberately for the additive UpgradeSemanticState branch, which
	// is unreachable for any extension without a state_version attribute.
	// TestNoReleasedSnapshotCarriesTheShadowStateVersionAttribute and
	// TestReleasedSocialChainsReplayToTheirRecordedOperationGraph prove every
	// released entry still replays to its exact recorded operation graph.
	const (
		name      = "historical_v3_diff_frozen.go"
		wantSHA   = "534b86570bbf0b1bf6518747bacf2f59634c29a8d314e2fed00868a235b32b1c"
		wantLines = 1455
	)
	_, current, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != wantSHA {
		t.Fatalf("retained adapted %s changed: got %s want %s", name, got, wantSHA)
	}
	lines := 0
	for _, value := range raw {
		if value == '\n' {
			lines++
		}
	}
	if lines != wantLines {
		t.Fatalf("retained adapted %s lines=%d want=%d", name, lines, wantLines)
	}
}
