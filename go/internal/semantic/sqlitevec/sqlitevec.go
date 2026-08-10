// Package sqlitevec owns Golem's sqlite-vec driver boundary. The imported
// binding embeds a sqlite-vec-enabled SQLite WASM binary, keeping the released
// Go provider CGO-free on Linux, macOS, and Windows.
package sqlitevec

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	"github.com/ncruces/go-sqlite3/driver"
)

const DriverModule = "github.com/ncruces/go-sqlite3"

// Open opens an isolated sqlite-vec capable database/sql pool for focused
// capability tests and standalone semantic tooling.
func Open(dataSourceName string) (*sql.DB, error) {
	if dataSourceName == "" {
		return nil, fmt.Errorf("SQLITE_VEC_CONFIG: data source name is empty")
	}
	return driver.Open(dataSourceName, nil)
}

func Probe(ctx context.Context, database *sql.DB) (string, error) {
	if ctx == nil || database == nil {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: context and database are required")
	}
	var version string
	if err := database.QueryRowContext(ctx, "SELECT vec_version()").Scan(&version); err != nil {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: sqlite-vec is unavailable")
	}
	if version == "" {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: sqlite-vec version is empty")
	}
	return version, nil
}

func Serialize(values []float32, dimensions int) ([]byte, error) {
	if dimensions < 1 || len(values) != dimensions {
		return nil, fmt.Errorf("SQLITE_VEC_VECTOR: dimensions do not match")
	}
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("SQLITE_VEC_VECTOR: elements must be finite")
		}
	}
	encoded, err := sqlitevec.SerializeFloat32(values)
	if err != nil {
		return nil, fmt.Errorf("SQLITE_VEC_VECTOR: serialization failed")
	}
	return encoded, nil
}
