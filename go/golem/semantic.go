package golem

import (
	"crypto/sha256"
	"fmt"
	"math"

	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
)

// SemanticResult is one authorized model row with its portable cosine
// distance. Lower distances are more similar; similarity is 1-distance.
type SemanticResult[M any] struct {
	row            Row[M]
	distance       float64
	identity       FrozenPredicate
	identityFields []FieldID
	identityToken  RuntimeSemanticIdentityToken
}

func RuntimeSemanticResult[M any](row Row[M], distance float64) (SemanticResult[M], error) {
	if row.model == (ModelID{}) || math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 || distance > 2.000001 {
		return SemanticResult[M]{}, fmt.Errorf("semantic result: invalid row or cosine distance")
	}
	return SemanticResult[M]{row: cloneRow(row), distance: distance}, nil
}

// RuntimeSemanticResultWithIdentity retains the private equality predicate
// needed by generated GraphQL relation hydration without adding identity cells
// to the authorized public row.
func RuntimeSemanticResultWithIdentity[M any](row Row[M], distance float64, identity FrozenPredicate, fields []FieldID, key string) (SemanticResult[M], error) {
	result, err := RuntimeSemanticResult(row, distance)
	if err != nil {
		return SemanticResult[M]{}, err
	}
	if identity.rootModel != row.model || identity.root == nil || len(fields) == 0 || key == "" {
		return SemanticResult[M]{}, fmt.Errorf("semantic result: private identity is invalid")
	}
	for _, field := range fields {
		if field == (FieldID{}) {
			return SemanticResult[M]{}, fmt.Errorf("semantic result: private identity field is absent")
		}
	}
	result.identity = cloneFrozenPredicate(identity)
	result.identityFields = append([]FieldID(nil), fields...)
	result.identityToken = runtimeSemanticIdentityToken(key)
	return result, nil
}

func (result SemanticResult[M]) Row() Row[M]       { return cloneRow(result.row) }
func (result SemanticResult[M]) Distance() float64 { return result.distance }
func (result SemanticResult[M]) Similarity() float64 {
	return 1 - result.distance
}

// RuntimeSemanticRow is the opaque generated-GraphQL transport for one
// authorized semantic result. Its private selector is never exposed as a row
// cell or predicate view.
type RuntimeSemanticRow struct {
	row            RuntimeModelRow
	identity       FrozenPredicate
	identityFields []FieldID
	identityToken  RuntimeSemanticIdentityToken
}

// RuntimeSemanticIdentityToken is an opaque, comparable rank-order identity.
type RuntimeSemanticIdentityToken struct{ digest [sha256.Size]byte }

func runtimeSemanticIdentityToken(key string) RuntimeSemanticIdentityToken {
	return RuntimeSemanticIdentityToken{digest: sha256.Sum256([]byte(key))}
}

// RuntimeSemanticRowFromResult embeds the opaque hydration envelope in the
// ordinary authorized runtime row consumed by generated GraphQL bindings.
func RuntimeSemanticRowFromResult[M any](result SemanticResult[M]) (RuntimeModelRow, error) {
	if result.identity.root == nil || len(result.identityFields) == 0 || result.identityToken == (RuntimeSemanticIdentityToken{}) {
		return RuntimeModelRow{}, fmt.Errorf("semantic result: private identity is unavailable")
	}
	transport := RuntimeSemanticRow{
		row:            RuntimeModelRowFromTyped(result.row),
		identity:       cloneFrozenPredicate(result.identity),
		identityFields: append([]FieldID(nil), result.identityFields...),
		identityToken:  result.identityToken,
	}
	row := cloneRuntimeModelRow(transport.row)
	row.semanticTransport = &transport
	return row, nil
}

// RuntimeSemanticPublicRow returns the authorized row without exposing the
// retained private selector.
func RuntimeSemanticPublicRow(value RuntimeSemanticRow) RuntimeModelRow {
	return cloneRuntimeModelRow(value.row)
}

// RuntimeSemanticTransport retrieves the opaque hydration envelope, when the
// row came from a generated semantic resolver.
func RuntimeSemanticTransport(row RuntimeModelRow) (RuntimeSemanticRow, bool) {
	if row.semanticTransport == nil {
		return RuntimeSemanticRow{}, false
	}
	value := *row.semanticTransport
	value.row = cloneRuntimeModelRow(value.row)
	value.identity = cloneFrozenPredicate(value.identity)
	value.identityFields = append([]FieldID(nil), value.identityFields...)
	return value, true
}

// RuntimeSemanticRowIdentity returns the non-reversible rank token and the
// stable identity field inventory.
func RuntimeSemanticRowIdentity(value RuntimeSemanticRow) (RuntimeSemanticIdentityToken, []FieldID) {
	return value.identityToken, append([]FieldID(nil), value.identityFields...)
}

// RuntimeSemanticHydrationRequest adds only the retained private identity
// predicates to an ordinary generated read request. Authorization, masks, and
// relation selection remain owned by the normal read executor.
func RuntimeSemanticHydrationRequest(base FrozenReadRequest, rows []RuntimeSemanticRow, filters ...FrozenPredicate) (FrozenReadRequest, error) {
	if base.operation != ReadFindMany || base.model == (ModelID{}) || len(rows) == 0 || len(filters) > 1 {
		return FrozenReadRequest{}, fmt.Errorf("semantic hydration: findMany request and identities are required")
	}
	fields := rows[0].identityFields
	children := make([]*frozenCondition, 0, len(rows))
	for index, row := range rows {
		if row.row.model != base.model || row.identity.rootModel != base.model || row.identity.root == nil || row.identityToken == (RuntimeSemanticIdentityToken{}) || !equalFieldIDs(fields, row.identityFields) {
			return FrozenReadRequest{}, fmt.Errorf("semantic hydration: identity %d does not match the request", index)
		}
		children = append(children, cloneFrozenCondition(row.identity.root))
	}
	selector := children[0]
	if len(children) > 1 {
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorOr, operand: frozenOperand{kind: FrozenOperandNone}, children: children}
	}
	if base.where != nil {
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: []*frozenCondition{cloneFrozenCondition(base.where.root), selector}}
	}
	if len(filters) == 1 {
		if filters[0].rootModel != base.model || filters[0].root == nil {
			return FrozenReadRequest{}, fmt.Errorf("semantic hydration: filter does not match the request")
		}
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: []*frozenCondition{cloneFrozenCondition(filters[0].root), selector}}
	}
	canonical, err := encodeFrozenPredicate(base.model, selector)
	if err != nil {
		return FrozenReadRequest{}, fmt.Errorf("semantic hydration: identity predicate is invalid: %w", err)
	}
	result := base.clone()
	where := FrozenPredicate{rootModel: base.model, root: selector, canonical: canonical}
	result.where = &where
	result.semanticIdentity = append([]FieldID(nil), fields...)
	return result, nil
}

// RuntimeSemanticHydratedIdentity returns the non-reversible token attached by
// an ordinary authorized semantic hydration read.
func RuntimeSemanticHydratedIdentity(row RuntimeModelRow) (RuntimeSemanticIdentityToken, bool) {
	return row.semanticIdentity, row.semanticIdentity != (RuntimeSemanticIdentityToken{})
}

// RuntimeSemanticIdentityFields returns the stable fields needed to identify
// hydrated rows without exposing their values.
func RuntimeSemanticIdentityFields(request FrozenReadRequest) []FieldID {
	return append([]FieldID(nil), request.semanticIdentity...)
}

// RuntimeModelRowWithSemanticIdentity attaches a non-reversible identity token
// to a cloned authorized row.
func RuntimeModelRowWithSemanticIdentity(row RuntimeModelRow, key string) RuntimeModelRow {
	result := cloneRuntimeModelRow(row)
	if key != "" {
		result.semanticIdentity = runtimeSemanticIdentityToken(key)
	}
	return result
}

func equalFieldIDs(left, right []FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
