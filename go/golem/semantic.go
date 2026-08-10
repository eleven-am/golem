package golem

import (
	"fmt"
	"math"

	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
)

// SemanticResult is one authorized model row with its portable cosine
// distance. Lower distances are more similar; similarity is 1-distance.
type SemanticResult[M any] struct {
	row      Row[M]
	distance float64
}

func RuntimeSemanticResult[M any](row Row[M], distance float64) (SemanticResult[M], error) {
	if row.model == (ModelID{}) || math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 || distance > 2.000001 {
		return SemanticResult[M]{}, fmt.Errorf("semantic result: invalid row or cosine distance")
	}
	return SemanticResult[M]{row: cloneRow(row), distance: distance}, nil
}

func (result SemanticResult[M]) Row() Row[M]       { return cloneRow(result.row) }
func (result SemanticResult[M]) Distance() float64 { return result.distance }
func (result SemanticResult[M]) Similarity() float64 {
	return 1 - result.distance
}

// RuntimeSemanticRecordKey derives the opaque managed-storage key from an
// already authorized row. Null, masked, or unselected identity fields fail
// closed and therefore cannot participate in semantic ranking.
func RuntimeSemanticRecordKey[M any](row Row[M], fields []FieldID) (string, error) {
	if row.model == (ModelID{}) || len(fields) == 0 {
		return "", fmt.Errorf("semantic key: row and identity fields are required")
	}
	values := make([]any, len(fields))
	for index, field := range fields {
		cell, ok := row.cells[field]
		if !ok || cell.state != ReadPresent {
			return "", fmt.Errorf("semantic key: authorized identity is unavailable")
		}
		values[index] = cloneReadCell(cell).value
	}
	return semantickey.Encode(values)
}
