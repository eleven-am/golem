package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
)

func TestSQLiteVerifyGatesRetainedCanonicalFormatsInIntrospectionNotComparison(t *testing.T) {
	schema := incrementalFixtureSchema(t, false)
	schema.Version, schema.CanonicalVersion = 2, 2
	if _, err := physical.HistoricalPhysicalFingerprint(schema); err != nil {
		t.Fatalf("fixture is not a usable retained snapshot: %v", err)
	}
	if err := physical.CompareFingerprints(schema, schema); err != nil {
		t.Fatalf("shared comparison refused a retained canonical format: %v", err)
	}
	err := New().Verify(context.Background(), nil, schema)
	if err == nil {
		t.Fatal("Verify accepted a retained canonical format without introspecting it")
	}
	if !strings.Contains(err.Error(), "PHYSICAL_FORMAT") {
		t.Fatalf("Verify refused for a reason other than the current-only format gate: %v", err)
	}
}
