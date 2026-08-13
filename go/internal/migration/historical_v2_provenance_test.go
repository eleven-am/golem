package migration

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV2RetainedPlannerAdaptedSourceProvenance(t *testing.T) {
	want := map[string]struct {
		sha   string
		lines int
	}{
		"historical_v2_diff_frozen.go":                {sha: "448c3f606811a91698362eb0e46bf4a0f6707602415208adc223a5dce50217d4", lines: 1501},
		"optimistic_concurrency_transition_frozen.go": {sha: "79e2e38a6882de14ddef0cb58c0fadd6df5d47ec774c514e1ed575803850c6b1", lines: 169},
	}
	_, current, _, _ := runtime.Caller(0)
	for name, expected := range want {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != expected.sha {
			t.Fatalf("retained adapted %s changed: got %s want %s", name, got, expected.sha)
		}
		lines := 0
		for _, value := range raw {
			if value == '\n' {
				lines++
			}
		}
		if lines != expected.lines {
			t.Fatalf("retained adapted %s lines=%d want=%d", name, lines, expected.lines)
		}
	}
}
