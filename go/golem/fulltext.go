package golem

import (
	"fmt"
	"math"
)

// FullTextResult is one authorized model row and its provider-relative search
// score. Higher scores rank before lower scores; scores are not portable
// across providers, indexes, or corpus revisions.
type FullTextResult[M any] struct {
	row   Row[M]
	score float64
}

// Row returns the authorized and masked model row.
func (result FullTextResult[M]) Row() Row[M] { return cloneRow(result.row) }

// Score returns the provider-relative full-text rank.
func (result FullTextResult[M]) Score() float64 { return result.score }

func RuntimeFullTextResult[M any](row Row[M], score float64) (FullTextResult[M], error) {
	if row.model == (ModelID{}) || math.IsNaN(score) || math.IsInf(score, 0) {
		return FullTextResult[M]{}, fmt.Errorf("full-text result: invalid row or score")
	}
	return FullTextResult[M]{row: cloneRow(row), score: score}, nil
}

// RuntimeFullTextRowFromResult converts one result to the ordinary authorized
// row transport used by generated GraphQL bindings.
func RuntimeFullTextRowFromResult[M any](result FullTextResult[M]) (RuntimeModelRow, error) {
	if result.row.identity == nil {
		return RuntimeModelRow{}, fmt.Errorf("full-text result: row identity is unavailable")
	}
	return RuntimeModelRowFromTyped(result.row), nil
}
