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
	// Re-pinned deliberately for the additive UpgradeSemanticState branch and
	// the full-text rewrite branch, neither of which is reachable in a released
	// snapshot.
	// TestNoReleasedSnapshotCarriesTheShadowStateVersionAttribute and
	// TestNoReleasedSnapshotCarriesFullTextExtension plus the replay test prove
	// every released entry still reproduces its exact recorded operation graph.
	const (
		name      = "historical_v3_diff_frozen.go"
		wantSHA   = "96a6e3bc41cfaef9122abf8c776aaf5f59c324eb134393570231c7b7419f48ae"
		wantLines = 1459
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
