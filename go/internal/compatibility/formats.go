package compatibility

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

const PersistedInventoryFormatVersion uint16 = 1

// PersistedInventory freezes every compiler or runtime identity whose bytes
// may survive a process or release boundary. Current identifies bytes emitted
// by this release; Historical is the exact closed decoder inventory.
type PersistedInventory struct {
	FormatVersion uint16            `json:"formatVersion"`
	Formats       []PersistedFormat `json:"formats"`
}

type PersistedFormat struct {
	Name       string   `json:"name"`
	Current    string   `json:"current"`
	Historical []string `json:"historical"`
}

func EncodePersistedInventory(value PersistedInventory) ([]byte, error) {
	if !canonicalPersisted(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func ParsePersistedInventory(encoded []byte) (PersistedInventory, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value PersistedInventory
	if err := decoder.Decode(&value); err != nil {
		return PersistedInventory{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return PersistedInventory{}, fail(ReasonInvalidEncoding)
	}
	canonical, err := EncodePersistedInventory(value)
	if err != nil {
		return PersistedInventory{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return PersistedInventory{}, fail(ReasonNoncanonical)
	}
	return value, nil
}

func ComparePersisted(previous, current PersistedInventory) LayerChange {
	if !canonicalPersisted(previous) || !canonicalPersisted(current) {
		return LayerBreaking
	}
	change := LayerUnchanged
	currentByName := make(map[string]PersistedFormat, len(current.Formats))
	for _, format := range current.Formats {
		currentByName[format.Name] = format
	}
	seen := make(map[string]bool, len(previous.Formats))
	for _, before := range previous.Formats {
		seen[before.Name] = true
		after, exists := currentByName[before.Name]
		if !exists || !containsAll(after.Historical, before.Historical) {
			return LayerBreaking
		}
		if before.Current != after.Current {
			if !contains(after.Historical, before.Current) {
				return LayerBreaking
			}
			change = LayerAdditive
		} else if len(after.Historical) != len(before.Historical) {
			change = LayerAdditive
		}
	}
	for _, format := range current.Formats {
		if !seen[format.Name] {
			change = LayerAdditive
		}
	}
	return change
}

func canonicalPersisted(value PersistedInventory) bool {
	if value.FormatVersion != PersistedInventoryFormatVersion || len(value.Formats) == 0 {
		return false
	}
	for index, format := range value.Formats {
		if format.Name == "" || format.Current == "" || index > 0 && value.Formats[index-1].Name >= format.Name ||
			!sort.StringsAreSorted(format.Historical) || !contains(format.Historical, format.Current) {
			return false
		}
		for historicalIndex, identity := range format.Historical {
			if identity == "" || historicalIndex > 0 && format.Historical[historicalIndex-1] >= identity {
				return false
			}
		}
	}
	return true
}

func contains(values []string, expected string) bool {
	index := sort.SearchStrings(values, expected)
	return index < len(values) && values[index] == expected
}

func containsAll(values, expected []string) bool {
	for _, identity := range expected {
		if !contains(values, identity) {
			return false
		}
	}
	return true
}
