package physical

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV2RetainedAdaptedSourceProvenance(t *testing.T) {
	// These are honest digests of the reviewed retained adaptations created at
	// the v3 closure boundary. No byte-exact pre-v3 source tag existed, so this
	// test claims tamper evidence only; release equivalence is proved by the
	// published-artifact and branch-behavior goldens.
	want := map[string]struct {
		sha   string
		lines int
	}{
		"historical_v2_validate_frozen.go":  {sha: "7779c52b8db38c0bf39b5f6279977eb078a250b63bec3e8686e2e9d79209f25c", lines: 901},
		"historical_v2_normalize_frozen.go": {sha: "e7df68847ac91aa58773208f0cb1fee03f9db74de6e0f4ba66b8137d239f497e", lines: 265},
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
