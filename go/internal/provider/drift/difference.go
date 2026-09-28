package drift

import (
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/physical"
)

// Between identifies the first deterministic catalog object that differs.
func Between(expected, actual physical.PhysicalSchema) (Object, bool) {
	if !reflect.DeepEqual(expected.Provider, actual.Provider) || expected.Namespace != actual.Namespace {
		return Object{Type: "schema", Name: string(expected.Namespace.Name)}, true
	}
	expectedTables := tablesByName(expected.Tables)
	actualTables := tablesByName(actual.Tables)
	for _, name := range joinedNames(expectedTables, actualTables) {
		want, wantOK := expectedTables[name]
		got, gotOK := actualTables[name]
		if !wantOK || !gotOK {
			return Object{Type: "table", Name: name, Table: name}, true
		}
		if column := namedDifference(want.Columns, got.Columns, func(value physical.PhysicalColumn) string { return string(value.Name) }); column != "" {
			return Object{Type: "column", Name: column, Table: name}, true
		}
		if !reflect.DeepEqual(want.PrimaryKey, got.PrimaryKey) {
			key := want.PrimaryKey
			if key == nil {
				key = got.PrimaryKey
			}
			keyName := "primary_key"
			if key != nil {
				keyName = string(key.Name)
			}
			return Object{Type: "primary_key", Name: keyName, Table: name}, true
		}
		if key := namedDifference(want.Uniques, got.Uniques, func(value physical.PhysicalKey) string { return string(value.Name) }); key != "" {
			return Object{Type: "unique", Name: key, Table: name}, true
		}
		if key := namedDifference(want.ForeignKeys, got.ForeignKeys, func(value physical.PhysicalForeignKey) string { return string(value.Name) }); key != "" {
			return Object{Type: "foreign_key", Name: key, Table: name}, true
		}
		if key := namedDifference(want.Checks, got.Checks, func(value physical.PhysicalCheck) string { return string(value.Name) }); key != "" {
			return Object{Type: "check", Name: key, Table: name}, true
		}
		if key := namedDifference(want.Indexes, got.Indexes, func(value physical.PhysicalIndex) string { return string(value.Name) }); key != "" {
			return Object{Type: "index", Name: key, Table: name}, true
		}
		if !reflect.DeepEqual(want, got) {
			return Object{Type: "table", Name: name, Table: name}, true
		}
	}
	if object := namedDifference(expected.System.Objects, actual.System.Objects, func(value physical.SystemObject) string { return string(value.Name) }); object != "" {
		return Object{Type: "table", Name: object, Table: object}, true
	}
	if extension := namedDifference(expected.Extensions, actual.Extensions, func(value physical.Extension) string { return string(value.ID) }); extension != "" {
		return Object{Type: "extension", Name: extension}, true
	}
	if unmanaged := namedDifference(expected.Unmanaged, actual.Unmanaged, func(value physical.UnmanagedObject) string { return string(value.Name) }); unmanaged != "" {
		return Object{Type: "object", Name: unmanaged}, true
	}
	if !reflect.DeepEqual(expected, actual) {
		return Object{Type: "schema", Name: string(expected.Namespace.Name)}, true
	}
	return Object{}, false
}

func tablesByName(values []physical.PhysicalTable) map[string]physical.PhysicalTable {
	result := make(map[string]physical.PhysicalTable, len(values))
	for _, value := range values {
		result[string(value.Name)] = value
	}
	return result
}

func joinedNames[T any](left, right map[string]T) []string {
	names := make(map[string]bool, len(left)+len(right))
	for name := range left {
		names[name] = true
	}
	for name := range right {
		names[name] = true
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func namedDifference[T any](expected, actual []T, name func(T) string) string {
	expectedByName := make(map[string]T, len(expected))
	actualByName := make(map[string]T, len(actual))
	for _, value := range expected {
		expectedByName[name(value)] = value
	}
	for _, value := range actual {
		actualByName[name(value)] = value
	}
	for _, valueName := range joinedNames(expectedByName, actualByName) {
		want, wantOK := expectedByName[valueName]
		got, gotOK := actualByName[valueName]
		if !wantOK || !gotOK || !reflect.DeepEqual(want, got) {
			return valueName
		}
	}
	return ""
}
