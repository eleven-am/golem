package golem

import (
	"crypto/sha256"
	"fmt"
	"math"

	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
)

// RuntimeSemanticIdentityProjection selects only the identity needed to apply
// the ordinary read planner's identity masks to semantic vector sources.
func RuntimeSemanticIdentityProjection[M any](descriptor ModelDescriptor[M]) (Projection[M], error) {
	for _, identity := range descriptor.Metadata().Identities() {
		if identity.Kind() != PrimaryIdentity {
			continue
		}
		fields := identity.Fields()
		if len(fields) == 0 {
			break
		}
		selection := make([]readSelectionNode, len(fields))
		for i, field := range fields {
			selection[i] = readSelectionNode{kind: readSelectionScalar, field: field}
		}
		return projectionValue[M]{node: readOptionNode{kind: readOptionSelect, selection: selection}}, nil
	}
	return nil, fmt.Errorf("semantic source: model has no primary identity")
}

// RuntimeSemanticVectorCell preserves the ordinary Get/presence contract:
// absent, unauthorized and unready vector sources all have the same null cell.
func RuntimeSemanticVectorCell[V interface{ Validate() error }](vector V, present bool) (ReadValue[V], error) {
	if !present {
		return ReadValue[V]{state: ReadNull}, nil
	}
	if err := vector.Validate(); err != nil {
		return ReadValue[V]{}, err
	}
	return ReadValue[V]{state: ReadPresent, value: vector}, nil
}

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

type RuntimeSemanticIdentityToken struct{ digest [sha256.Size]byte }

func runtimeSemanticIdentityToken(key string) RuntimeSemanticIdentityToken {
	return RuntimeSemanticIdentityToken{digest: sha256.Sum256([]byte(key))}
}

func RuntimeSemanticRowFromResult[M any](result SemanticResult[M]) (RuntimeModelRow, error) {
	if result.row.identity == nil {
		return RuntimeModelRow{}, fmt.Errorf("semantic result: row identity is unavailable")
	}
	return RuntimeModelRowFromTyped(result.row), nil
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
