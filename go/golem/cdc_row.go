package golem

import (
	"bytes"
	"sort"
)

// RuntimeCDCModelRow is the explicit trusted-adapter assertion that cells are
// an exact persisted stored-scalar image, not an authorized/public projection.
// The P7 CDC encoder still proves the complete compiler-owned field inventory,
// field ownership, scalar kinds, nullability, and logical values before use.
//
// A CDC adapter is a trusted installation boundary. Calling this constructor
// with masked or otherwise projected cells violates that adapter contract; an
// ordinary RuntimeModelReadRow never acquires this provenance marker.
func RuntimeCDCModelRow(model ModelID, cells ...RuntimeReadCell) (RuntimeModelRow, error) {
	row, err := RuntimeModelReadRow(model, cells...)
	if err != nil {
		return RuntimeModelRow{}, err
	}
	row.cdcExact = true
	return row, nil
}

// RuntimeCDCModelRowInventory exposes only the owned field identities needed
// by the runtime CDC validator. The boolean is false for ordinary rows and for
// rows carrying relation counts or response-path occurrences. Values remain
// representation-opaque and clone-on-read through RuntimeTransportField.
func RuntimeCDCModelRowInventory(row RuntimeModelRow) ([]FieldID, bool) {
	if !row.cdcExact || len(row.counts) != 0 || len(row.occurrences) != 0 {
		return nil, false
	}
	fields := make([]FieldID, 0, len(row.cells))
	for field := range row.cells {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool {
		return bytes.Compare(fields[i][:], fields[j][:]) < 0
	})
	return fields, true
}
