package nested

import (
	"bytes"
	"fmt"
	"sort"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ExistingRowWork derives both execution identity and canonical ordering from
// the active model's declared primary key. Composite component order is never
// inferred from field IDs or physical column order.
func ExistingRowWork(registry *schema.Registry, row mutationdecode.Row) (RuntimeWork, error) {
	if registry == nil {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_ROWS_INPUT: active registry is required")
	}
	identity, err := mutationdecode.PrimaryIdentity(registry, row)
	if err != nil {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_ROWS_IDENTITY: %w", err)
	}
	key, err := mutationfact.EncodeIdentity(identity)
	if err != nil {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_ROWS_IDENTITY: %w", err)
	}
	return NewExistingWork(row.ModelID(), identity, key)
}

// ExistingRelationWorks converts locked target rows for an inverse endpoint
// into immutable execution work. Each row owns its own FK membership.
func ExistingRelationWorks(registry *schema.Registry, rows []mutationdecode.Row, effect MembershipEffect) ([]RuntimeWork, error) {
	result := make([]RuntimeWork, len(rows))
	for index, row := range rows {
		base, err := ExistingRowWork(registry, row)
		if err != nil {
			return nil, err
		}
		identity, _ := base.Identity()
		result[index], err = NewResolvedRelationWork(base.ModelID(), identity, base.OrderKey(), row, effect)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// OwnerRelationWork represents a source endpoint: the work identity belongs
// to the FK-owning anchor while the retained row is the resolved target.
func OwnerRelationWork(registry *schema.Registry, owner, related mutationdecode.Row, effect MembershipEffect) (RuntimeWork, error) {
	base, err := ExistingRowWork(registry, owner)
	if err != nil {
		return RuntimeWork{}, err
	}
	identity, _ := base.Identity()
	return NewResolvedRelationWork(owner.ModelID(), identity, base.OrderKey(), related, effect)
}

// RelationSetDifference computes exact membership effects over primary
// identities. Inputs may arrive in any order; outputs are canonical and
// duplicate identities fail closed rather than silently collapsing rows.
func RelationSetDifference(registry *schema.Registry, current, desired []mutationdecode.Row) (connect, disconnect []mutationdecode.Row, err error) {
	currentSet, err := indexRelationRows(registry, current)
	if err != nil {
		return nil, nil, err
	}
	desiredSet, err := indexRelationRows(registry, desired)
	if err != nil {
		return nil, nil, err
	}
	connect = differenceRows(desiredSet, currentSet)
	disconnect = differenceRows(currentSet, desiredSet)
	return connect, disconnect, nil
}

type relationRowIndex struct {
	rows map[string]mutationdecode.Row
	keys map[string][]byte
}

func indexRelationRows(registry *schema.Registry, rows []mutationdecode.Row) (relationRowIndex, error) {
	result := relationRowIndex{rows: make(map[string]mutationdecode.Row, len(rows)), keys: make(map[string][]byte, len(rows))}
	var model policyir.ModelID
	for index, row := range rows {
		if index == 0 {
			model = row.ModelID()
		} else if row.ModelID() != model {
			return relationRowIndex{}, fmt.Errorf("P4_NESTED_ROWS_MODEL: relation set mixes models")
		}
		work, err := ExistingRowWork(registry, row)
		if err != nil {
			return relationRowIndex{}, err
		}
		keyBytes := work.OrderKey()
		key := string(keyBytes)
		if _, duplicate := result.rows[key]; duplicate {
			return relationRowIndex{}, fmt.Errorf("P4_NESTED_ROWS_SET: duplicate primary identity at row %d", index)
		}
		result.rows[key], result.keys[key] = row, keyBytes
	}
	return result, nil
}

func differenceRows(source, subtract relationRowIndex) []mutationdecode.Row {
	keys := make([][]byte, 0, len(source.rows))
	for key, canonical := range source.keys {
		if _, present := subtract.rows[key]; !present {
			keys = append(keys, canonical)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	result := make([]mutationdecode.Row, len(keys))
	for index, key := range keys {
		result[index] = source.rows[string(key)]
	}
	return result
}
