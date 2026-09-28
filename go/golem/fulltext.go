package golem

import (
	"fmt"
	"math"
)

// FullTextResult is one authorized model row and its provider-relative search
// score. Higher scores rank before lower scores; scores are not portable
// across providers, indexes, or corpus revisions.
type FullTextResult[M any] struct {
	row            Row[M]
	score          float64
	identity       FrozenPredicate
	identityFields []FieldID
	identityToken  RuntimeSemanticIdentityToken
}

// Row returns the authorized and masked model row.
func (result FullTextResult[M]) Row() Row[M] { return cloneRow(result.row) }

// Score returns the provider-relative full-text rank.
func (result FullTextResult[M]) Score() float64 { return result.score }

// RuntimeFullTextResultWithIdentity constructs a ranked result while retaining
// the private selector required by generated GraphQL relation hydration.
func RuntimeFullTextResultWithIdentity[M any](row Row[M], score float64, identity FrozenPredicate, fields []FieldID, key string) (FullTextResult[M], error) {
	if row.model == (ModelID{}) || math.IsNaN(score) || math.IsInf(score, 0) || identity.rootModel != row.model || identity.root == nil || len(fields) == 0 || key == "" {
		return FullTextResult[M]{}, fmt.Errorf("full-text result: invalid row, score, or identity")
	}
	for _, field := range fields {
		if field == (FieldID{}) {
			return FullTextResult[M]{}, fmt.Errorf("full-text result: private identity field is absent")
		}
	}
	return FullTextResult[M]{row: cloneRow(row), score: score, identity: cloneFrozenPredicate(identity), identityFields: append([]FieldID(nil), fields...), identityToken: runtimeSemanticIdentityToken(key)}, nil
}

// RuntimeFullTextRowFromResult converts one result to the ordinary authorized
// row transport used by generated GraphQL bindings.
func RuntimeFullTextRowFromResult[M any](result FullTextResult[M]) (RuntimeModelRow, error) {
	if result.identity.root == nil || len(result.identityFields) == 0 || result.identityToken == (RuntimeSemanticIdentityToken{}) {
		return RuntimeModelRow{}, fmt.Errorf("full-text result: private identity is unavailable")
	}
	transport := RuntimeSemanticRow{
		row: RuntimeModelRowFromTyped(result.row), identity: cloneFrozenPredicate(result.identity),
		identityFields: append([]FieldID(nil), result.identityFields...), identityToken: result.identityToken,
	}
	row := cloneRuntimeModelRow(transport.row)
	row.semanticTransport = &transport
	return row, nil
}
