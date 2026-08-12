package migration

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHistoricalV1TaggedPlannerSourceProvenance(t *testing.T) {
	const (
		upstreamSHA256 = "4d7271550104a57a6f9766bbe9456a5544cdf429eda4540df366550ace572679"
		upstreamLines  = 871
		adaptedSHA256  = "f3b20cf9f4819fbe28f45a61d5b97754ccb253786f6ba46252c59e2ab6e04dd6"
	)
	if upstreamSHA256 != historicalV1DiffUpstreamSHA256 || upstreamLines != historicalV1DiffUpstreamLines {
		t.Fatalf("retained planner provenance changed: sha=%s lines=%d", historicalV1DiffUpstreamSHA256, historicalV1DiffUpstreamLines)
	}
	_, current, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "historical_v1_diff_tagged.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != adaptedSHA256 {
		t.Fatalf("retained planner adaptation changed: got %s want %s", got, adaptedSHA256)
	}
}
