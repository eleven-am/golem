package handle

import (
	"context"
	"path/filepath"
	"testing"

	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestReportedSQLitePoolWidthIsTheEnforcedOne(t *testing.T) {
	database, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	stats := database.UnsafeSQLX().Stats()
	pool := database.Pool()
	if pool.MaximumOpen() != stats.MaxOpenConnections {
		t.Fatalf("reported MaximumOpen=%d but the pool enforces %d", pool.MaximumOpen(), stats.MaxOpenConnections)
	}
	if pool.MaximumOpen() != internalsqlite.VerifiedPoolWidth || pool.MaximumIdle() != internalsqlite.VerifiedPoolWidth {
		t.Fatalf("reported pool width open=%d idle=%d does not follow the provider-owned width %d", pool.MaximumOpen(), pool.MaximumIdle(), internalsqlite.VerifiedPoolWidth)
	}
}
