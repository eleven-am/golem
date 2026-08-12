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
	const (
		name      = "historical_v3_diff_frozen.go"
		wantSHA   = "a02bb8b7dd9b3685990eb62d95e577af0d9edcc5e770a0a583ba74cd919f4db3"
		wantLines = 1446
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
