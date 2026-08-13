package physical

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV3RetainedAdaptedSourceProvenance(t *testing.T) {
	// These are honest digests of the reviewed retained adaptations created at
	// the v3 publication boundary. No byte-exact pre-publication source tag
	// exists, so this test claims tamper evidence only; release equivalence is
	// proved by current-vs-frozen behavior tests and canonical artifacts.
	want := map[string]struct {
		sha   string
		lines int
	}{
		"historical_v3_normalize_frozen.go": {sha: "1b8fe5f4aa791662c681c976976c0a535098a5f7b219d6bb4537d105a68533c1", lines: 266},
		"historical_v3_validate_frozen.go":  {sha: "ad7ead6542cee35d63404826ffcf75c0b307a3749199a3854ab55e7a63f47600", lines: 955},
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
