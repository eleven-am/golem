// Package sqlitevec owns Golem's SQLite vector driver boundary. Importing it
// registers SQLite's vec1 extension on every ncruces connection, keeping the
// released Go provider CGO-free on Linux, macOS, and Windows.
package sqlitevec

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"

	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/vec1"
)

func init() {
	sqlite3.AutoExtension(vec1.Register)
}

const DriverModule = "github.com/ncruces/go-sqlite3"

const (
	MinimumNorm = 1e-9
	MaximumNorm = 1e9
)

// Open opens an isolated vec1 capable database/sql pool for focused
// capability tests and standalone semantic tooling.
func Open(dataSourceName string) (*sql.DB, error) {
	if dataSourceName == "" {
		return nil, fmt.Errorf("SQLITE_VEC_CONFIG: data source name is empty")
	}
	return driver.Open(dataSourceName, nil)
}

func RequireExactVectors(index string, stateVersion uint16) error {
	if stateVersion >= semanticstorage.StateVersionExactVectors {
		return nil
	}
	return fmt.Errorf("SQLITE_VEC_LEGACY_STATE: semantic index %s uses semantic state version %d, whose sqlite-vec vec0 vector table this release cannot open; open and migrate the database with golem go/v0.7.1 to upgrade it to semantic state version %d first", index, stateVersion, semanticstorage.StateVersionExactVectors)
}

func LegacyVectorTable(storage string) error {
	return fmt.Errorf("SQLITE_VEC_LEGACY_STATE: semantic storage %s still stores vectors in a sqlite-vec vec0 table that this release cannot open; open and migrate the database with golem go/v0.7.1 to upgrade it to semantic state version %d first", storage, semanticstorage.StateVersionExactVectors)
}

func Probe(ctx context.Context, database *sql.DB) (string, error) {
	if ctx == nil || database == nil {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: context and database are required")
	}
	var version string
	if err := database.QueryRowContext(ctx, "SELECT vec1_info()").Scan(&version); err != nil {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: vec1 is unavailable")
	}
	if version == "" {
		return "", fmt.Errorf("SQLITE_VEC_CAPABILITY: vec1 info is empty")
	}
	return version, nil
}

func ValidateVector(values []float32, dimensions int) error {
	if dimensions < 1 || len(values) != dimensions {
		return fmt.Errorf("SEMANTIC_VECTOR_INVALID: dimensions do not match")
	}
	var squared float64
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("SEMANTIC_VECTOR_INVALID: elements must be finite")
		}
		squared += float64(value) * float64(value)
	}
	if norm := math.Sqrt(squared); norm < MinimumNorm || norm > MaximumNorm {
		return fmt.Errorf("SEMANTIC_VECTOR_INVALID: euclidean norm must be within [%g, %g] for exact cosine distance", MinimumNorm, MaximumNorm)
	}
	return nil
}

func Serialize(values []float32, dimensions int) ([]byte, error) {
	if err := ValidateVector(values, dimensions); err != nil {
		return nil, err
	}
	encoded, err := binary.Append(make([]byte, 0, 4*len(values)), binary.LittleEndian, values)
	if err != nil {
		return nil, fmt.Errorf("SQLITE_VEC_VECTOR: serialization failed")
	}
	return encoded, nil
}
