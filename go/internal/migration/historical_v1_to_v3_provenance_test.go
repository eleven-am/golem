package migration

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHistoricalV1ToV3CompositionRetainedSourceProvenance(t *testing.T) {
	// This is an honest pin of the reviewed composition introduced after v3
	// publication. It claims tamper evidence, not ancestry from a source tag;
	// exact behavior is owned by the published-v2 and direct-upgrade gates.
	const (
		name      = "historical_v1_to_v3_composition.go"
		wantSHA   = "29f516522996df507bc63e74e29e963a0fc82e8add919e9cbbc9db24d9334974"
		wantLines = 303
	)
	_, current, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != wantSHA {
		t.Fatalf("retained composition %s changed: got %s want %s", name, got, wantSHA)
	}
	if lines := strings.Count(string(raw), "\n"); lines != wantLines {
		t.Fatalf("retained composition %s lines=%d want=%d", name, lines, wantLines)
	}
	for _, forbidden := range []string{"physical.Normalize(", "diffSchemas(", "Diff(before", "Diff(middle"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("retained composition depends on mutable current authority %q", forbidden)
		}
	}
}
