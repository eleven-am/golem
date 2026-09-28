package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
	providerdrift "github.com/eleven-am/golem/go/internal/provider/drift"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
)

type schemaRow struct {
	Type  string `db:"type"`
	Name  string `db:"name"`
	Table string `db:"tbl_name"`
	SQL   string `db:"sql"`
}
type columnRow struct {
	CID     int     `db:"cid"`
	Name    string  `db:"name"`
	Type    string  `db:"type"`
	NotNull int     `db:"notnull"`
	Default *string `db:"dflt_value"`
	PK      int     `db:"pk"`
	Hidden  int     `db:"hidden"`
}

type catalogQuerier interface {
	SelectContext(context.Context, any, string, ...any) error
}

// reviewedSnapshot can only be constructed from a sealed migration entry.
// Bare historical schemas cannot cross the active provider boundary.
type reviewedSnapshot struct{ schema physical.PhysicalSchema }

func (provider *Provider) introspect(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	normalized, err := physical.Normalize(expected)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	if _, err := provider.probe(ctx, database); err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectCatalog(ctx, database, normalized)
}

func (provider *Provider) introspectCatalog(ctx context.Context, database catalogQuerier, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	normalized, err := physical.Normalize(expected)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectNormalizedCatalog(ctx, database, normalized, false)
}

func (provider *Provider) introspectReviewedCatalog(ctx context.Context, database catalogQuerier, reviewed reviewedSnapshot) (physical.PhysicalSchema, error) {
	normalized, err := physical.NormalizeHistorical(reviewed.schema)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectNormalizedCatalog(ctx, database, normalized, true)
}

func (provider *Provider) introspectNormalizedCatalog(ctx context.Context, database catalogQuerier, normalized physical.PhysicalSchema, reviewedReplay bool) (physical.PhysicalSchema, error) {
	var rows []schemaRow
	if err := database.SelectContext(ctx, &rows, "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND type IN ('table','index','view','trigger') ORDER BY type,name"); err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect schema: %w", err)
	}
	actual := make(map[string]schemaRow, len(rows))
	for _, row := range rows {
		actual[row.Type+"\x00"+row.Name] = row
	}
	expectedObjects := make(map[string]string)
	expectedTokenObjects := make(map[string]string)
	expectedObjectTables := make(map[string]string)
	for _, object := range normalized.System.Objects {
		statement, renderErr := renderSystemObject(object)
		if renderErr != nil {
			return physical.PhysicalSchema{}, renderErr
		}
		expectedObjects["table\x00"+string(object.Name)] = statement
		expectedObjectTables["table\x00"+string(object.Name)] = string(object.Name)
		indexStatements, renderErr := renderSystemIndexes(object)
		if renderErr != nil {
			return physical.PhysicalSchema{}, renderErr
		}
		indexNames := renderedSystemIndexNames(object)
		if len(indexNames) != len(indexStatements) {
			return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect system index registry mismatch for %s", object.ID)
		}
		for index, indexStatement := range indexStatements {
			expectedObjects["index\x00"+indexNames[index]] = indexStatement
			expectedObjectTables["index\x00"+indexNames[index]] = string(object.Name)
		}
	}
	tableMap := make(map[ir.ModelID]physical.PhysicalTable, len(normalized.Tables))
	for _, table := range normalized.Tables {
		tableMap[table.ID] = table
	}
	for _, table := range normalized.Tables {
		statement, renderErr := renderTable(table, tableMap)
		if renderErr != nil {
			return physical.PhysicalSchema{}, renderErr
		}
		expectedObjects["table\x00"+string(table.Name)] = statement
		expectedObjectTables["table\x00"+string(table.Name)] = string(table.Name)
		for _, index := range table.Indexes {
			statement, renderErr := renderIndex(table, index)
			if renderErr != nil {
				return physical.PhysicalSchema{}, renderErr
			}
			expectedObjects["index\x00"+string(index.Name)] = statement
			expectedObjectTables["index\x00"+string(index.Name)] = string(table.Name)
		}
	}
	if normalized.Version != 1 || normalized.CanonicalVersion != 1 {
		for _, extension := range normalized.Extensions {
			if extension.Kind == fulltextcontract.IndexKind {
				descriptor, decodeErr := fulltextstorage.Decode(extension)
				if decodeErr != nil {
					return physical.PhysicalSchema{}, decodeErr
				}
				owner, exists := tableMap[descriptor.ModelID]
				if !exists {
					return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect full-text extension %s: owner is absent", extension.ID)
				}
				statements, renderErr := renderFullTextExtension(extension, owner)
				if renderErr != nil {
					return physical.PhysicalSchema{}, renderErr
				}
				if len(statements) != 5 {
					return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect full-text statement registry mismatch for %s", extension.ID)
				}
				names := sqliteFullTextNames(descriptor)
				expectedObjects["table\x00"+string(names.keys)] = statements[0]
				expectedTokenObjects["table\x00"+string(names.index)] = statements[1]
				expectedTokenObjects["trigger\x00"+string(names.insert)] = statements[2]
				expectedTokenObjects["trigger\x00"+string(names.update)] = statements[3]
				expectedTokenObjects["trigger\x00"+string(names.delete)] = statements[4]
				expectedObjectTables["table\x00"+string(names.keys)] = string(names.keys)
				expectedObjectTables["table\x00"+string(names.index)] = string(names.index)
				expectedObjectTables["trigger\x00"+string(names.insert)] = string(owner.Name)
				expectedObjectTables["trigger\x00"+string(names.update)] = string(owner.Name)
				expectedObjectTables["trigger\x00"+string(names.delete)] = string(owner.Name)
				indexKey := "table\x00" + string(names.index)
				if _, exists := actual[indexKey]; !exists {
					return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: "table", Name: string(names.index), Table: string(names.index)}, "sqlite introspect drift: missing full-text table %s", names.index)
				}
				for _, suffix := range []string{"_config", "_data", "_docsize", "_idx"} {
					key := "table\x00" + string(names.index) + suffix
					if _, exists := actual[key]; !exists {
						name := string(names.index) + suffix
						return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: "table", Name: name, Table: string(names.index)}, "sqlite introspect drift: full-text shadow table %s", name)
					}
					delete(actual, key)
				}
				continue
			}
			statements, renderErr := renderSemanticExtension(extension, reviewedReplay)
			if renderErr != nil {
				return physical.PhysicalSchema{}, renderErr
			}
			descriptor, decodeErr := semanticstorage.Decode(extension)
			if decodeErr != nil {
				return physical.PhysicalSchema{}, decodeErr
			}
			stateName := string(descriptor.Storage) + "_state"
			vectorName := string(descriptor.Storage) + "_vec"
			indexNames := semanticStateIndexNames(descriptor)
			if len(statements) != len(indexNames)+2 {
				return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect semantic statement registry mismatch for %s", extension.ID)
			}
			expectedObjects["table\x00"+stateName] = statements[0]
			expectedObjectTables["table\x00"+stateName] = stateName
			for index, name := range indexNames {
				expectedObjects["index\x00"+string(name)] = statements[index+1]
				expectedObjectTables["index\x00"+string(name)] = stateName
			}
			vectorKey := "table\x00" + vectorName
			if descriptor.StateVersion >= semanticstorage.StateVersionExactVectors {
				expectedObjects[vectorKey] = statements[len(statements)-1]
				expectedObjectTables[vectorKey] = vectorName
				continue
			}
			vector, exists := actual[vectorKey]
			if !exists || strings.TrimSpace(vector.SQL) != statements[len(statements)-1] {
				return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: "table", Name: vectorName, Table: vectorName}, "sqlite introspect drift: semantic vector table %s", vectorName)
			}
			delete(actual, vectorKey)
			for _, suffix := range []string{"_chunks", "_info", "_rowids", "_vector_chunks00"} {
				key := "table\x00" + vectorName + suffix
				if _, exists := actual[key]; !exists {
					name := vectorName + suffix
					return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: "table", Name: name, Table: vectorName}, "sqlite introspect drift: semantic vector shadow table %s", name)
				}
				delete(actual, key)
			}
		}
	}
	for _, unmanaged := range normalized.Unmanaged {
		delete(actual, unmanaged.Kind+"\x00"+string(unmanaged.Name))
	}
	if len(actual) != len(expectedObjects)+len(expectedTokenObjects) {
		if object, exists := firstUnexpectedSchemaObject(actual, expectedObjects, expectedTokenObjects); exists {
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: object.Type, Name: object.Name, Table: object.Table}, "sqlite introspect drift: unexpected %s %s on %s", object.Type, object.Name, object.Table)
		}
		if key, exists := firstMissingSchemaObject(actual, expectedObjects, expectedTokenObjects); exists {
			kind, name := splitSchemaObjectKey(key)
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: kind, Name: name, Table: expectedObjectTables[key]}, "sqlite introspect drift: missing %s", key)
		}
		return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: "schema", Name: string(normalized.Namespace.Name)}, "sqlite introspect drift: object count got=%d want=%d objects=%v", len(actual), len(expectedObjects)+len(expectedTokenObjects), sortedSchemaObjectKeys(actual))
	}
	for key, expectedSQL := range expectedTokenObjects {
		row, exists := actual[key]
		if !exists {
			kind, name := splitSchemaObjectKey(key)
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: kind, Name: name, Table: expectedObjectTables[key]}, "sqlite introspect drift: missing %s", key)
		}
		expectedTokens, lexErr := lexDDL(expectedSQL)
		if lexErr != nil {
			return physical.PhysicalSchema{}, lexErr
		}
		actualTokens, lexErr := lexDDL(row.SQL)
		if lexErr != nil {
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: row.Type, Name: row.Name, Table: row.Table}, "sqlite introspect parse %s: %v", key, lexErr)
		}
		if !reflect.DeepEqual(expectedTokens, actualTokens) {
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: row.Type, Name: row.Name, Table: row.Table}, "sqlite introspect drift: full-text definition changed for %s", key)
		}
	}
	for key, expectedSQL := range expectedObjects {
		row, exists := actual[key]
		if !exists {
			kind, name := splitSchemaObjectKey(key)
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: kind, Name: name, Table: expectedObjectTables[key]}, "sqlite introspect drift: missing %s", key)
		}
		expectedAST, parseErr := parseDDL(expectedSQL)
		if parseErr != nil {
			return physical.PhysicalSchema{}, parseErr
		}
		actualAST, parseErr := parseDDL(row.SQL)
		if parseErr != nil {
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: row.Type, Name: row.Name, Table: row.Table}, "sqlite introspect parse %s: %v", key, parseErr)
		}
		if !reflect.DeepEqual(expectedAST, actualAST) {
			return physical.PhysicalSchema{}, providerdrift.New(providerdrift.Object{Type: row.Type, Name: row.Name, Table: row.Table}, "sqlite introspect drift: semantic definition changed for %s", key)
		}
	}
	for _, table := range normalized.Tables {
		if err := inspectColumns(ctx, database, table); err != nil {
			return physical.PhysicalSchema{}, err
		}
		if err := inspectForeignKeys(ctx, database, table, tableMap); err != nil {
			return physical.PhysicalSchema{}, err
		}
		if err := inspectIndexes(ctx, database, table); err != nil {
			return physical.PhysicalSchema{}, err
		}
	}
	// Stable IDs and registered expression identities are reattached only after
	// every catalog fact and parsed semantic definition has matched.
	return normalized, nil
}

func firstUnexpectedSchemaObject(actual map[string]schemaRow, expected ...map[string]string) (schemaRow, bool) {
	keys := sortedSchemaObjectKeys(actual)
	for _, key := range keys {
		known := false
		for _, objects := range expected {
			if _, known = objects[key]; known {
				break
			}
		}
		if !known {
			return actual[key], true
		}
	}
	return schemaRow{}, false
}

func firstMissingSchemaObject(actual map[string]schemaRow, expected ...map[string]string) (string, bool) {
	var keys []string
	for _, objects := range expected {
		for key := range objects {
			if _, exists := actual[key]; !exists {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	sort.Strings(keys)
	return keys[0], true
}

func splitSchemaObjectKey(key string) (string, string) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return "object", key
	}
	return parts[0], parts[1]
}

func sortedSchemaObjectKeys(objects map[string]schemaRow) []string {
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func inspectColumns(ctx context.Context, database catalogQuerier, table physical.PhysicalTable) error {
	var rows []columnRow
	if err := database.SelectContext(ctx, &rows, "PRAGMA table_xinfo("+quote(table.Name)+")"); err != nil {
		return fmt.Errorf("sqlite introspect columns %s: %w", table.Name, err)
	}
	if len(rows) != len(table.Columns) {
		name := firstColumnDifference(rows, table.Columns)
		return providerdrift.New(providerdrift.Object{Type: "column", Name: name, Table: string(table.Name)}, "sqlite introspect drift table=%s column count", table.ID)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CID < rows[j].CID })
	pk := map[string]int{}
	if table.PrimaryKey != nil {
		for index, id := range table.PrimaryKey.Columns {
			for _, column := range table.Columns {
				if column.ID == id {
					pk[string(column.Name)] = index + 1
				}
			}
		}
	}
	for index, row := range rows {
		column := table.Columns[index]
		if row.CID != index || row.Name != string(column.Name) || strings.ToUpper(row.Type) != renderStorage(column.Storage) || row.NotNull != boolInt(!column.Nullable) || row.PK != pk[row.Name] {
			return providerdrift.New(providerdrift.Object{Type: "column", Name: columnDifferenceName(row, column), Table: string(table.Name)}, "sqlite introspect drift table=%s column=%s catalog fact", table.ID, column.ID)
		}
		generated := column.Generated != nil
		if generated != (row.Hidden == 2 || row.Hidden == 3) {
			return providerdrift.New(providerdrift.Object{Type: "column", Name: string(column.Name), Table: string(table.Name)}, "sqlite introspect drift table=%s column=%s generated flag", table.ID, column.ID)
		}
		expectedDefault := ""
		if column.Default.Kind == physical.DefaultLiteral {
			expectedDefault, _ = renderLiteral(*column.Default.Literal)
		}
		actualDefault := ""
		if row.Default != nil {
			actualDefault = *row.Default
		}
		if expectedDefault != actualDefault {
			return providerdrift.New(providerdrift.Object{Type: "column", Name: string(column.Name), Table: string(table.Name)}, "sqlite introspect drift table=%s column=%s default", table.ID, column.ID)
		}
	}
	return nil
}

func inspectForeignKeys(ctx context.Context, database catalogQuerier, table physical.PhysicalTable, tables map[ir.ModelID]physical.PhysicalTable) error {
	type fkRow struct {
		ID       int    `db:"id"`
		Seq      int    `db:"seq"`
		Table    string `db:"table"`
		From     string `db:"from"`
		To       string `db:"to"`
		OnUpdate string `db:"on_update"`
		OnDelete string `db:"on_delete"`
		Match    string `db:"match"`
	}
	var rows []fkRow
	if err := database.SelectContext(ctx, &rows, "PRAGMA foreign_key_list("+quote(table.Name)+")"); err != nil {
		return err
	}
	want := 0
	for _, fk := range table.ForeignKeys {
		want += len(fk.Columns)
	}
	if len(rows) != want {
		name := string(table.Name)
		return providerdrift.New(providerdrift.Object{Type: "table", Name: name, Table: name}, "sqlite introspect drift table=%s foreign-key arity", table.ID)
	}
	for _, row := range rows {
		matched := false
		object := providerdrift.Object{Type: "table", Name: string(table.Name), Table: string(table.Name)}
		for _, fk := range table.ForeignKeys {
			target := tables[fk.ReferencedTable]
			if row.Table != string(target.Name) || row.Seq >= len(fk.Columns) {
				continue
			}
			object.Type = "foreign_key"
			object.Name = string(fk.Name)
			local, localOK := columnName(table, fk.Columns[row.Seq])
			remote, remoteOK := columnName(target, fk.ReferencedColumns[row.Seq])
			if !localOK || !remoteOK {
				return fmt.Errorf("sqlite introspect expected foreign key references missing field")
			}
			if local == row.From && remote == row.To && row.OnUpdate == renderAction(fk.OnUpdate) && row.OnDelete == renderAction(fk.OnDelete) {
				matched = true
				break
			}
		}
		if !matched {
			return providerdrift.New(object, "sqlite introspect drift table=%s foreign key", table.ID)
		}
	}
	return nil
}

func inspectIndexes(ctx context.Context, database catalogQuerier, table physical.PhysicalTable) error {
	type indexRow struct {
		Seq     int    `db:"seq"`
		Name    string `db:"name"`
		Unique  int    `db:"unique"`
		Origin  string `db:"origin"`
		Partial int    `db:"partial"`
	}
	var rows []indexRow
	if err := database.SelectContext(ctx, &rows, "PRAGMA index_list("+quote(table.Name)+")"); err != nil {
		return err
	}
	explicit := map[string]physical.PhysicalIndex{}
	for _, index := range table.Indexes {
		explicit[string(index.Name)] = index
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Name, "sqlite_autoindex_") {
			continue
		}
		index, ok := explicit[row.Name]
		if !ok || row.Unique != boolInt(index.Unique) || row.Partial != boolInt(index.Predicate != nil) {
			return providerdrift.New(providerdrift.Object{Type: "index", Name: row.Name, Table: string(table.Name)}, "sqlite introspect drift table=%s index=%s", table.ID, row.Name)
		}
		delete(explicit, row.Name)
	}
	if len(explicit) != 0 {
		names := make([]string, 0, len(explicit))
		for name := range explicit {
			names = append(names, name)
		}
		sort.Strings(names)
		return providerdrift.New(providerdrift.Object{Type: "index", Name: names[0], Table: string(table.Name)}, "sqlite introspect drift table=%s missing index", table.ID)
	}
	return nil
}

func firstColumnDifference(actual []columnRow, expected []physical.PhysicalColumn) string {
	expectedNames := make(map[string]bool, len(expected))
	for _, column := range expected {
		expectedNames[string(column.Name)] = true
	}
	for _, row := range actual {
		if !expectedNames[row.Name] {
			return row.Name
		}
	}
	actualNames := make(map[string]bool, len(actual))
	for _, row := range actual {
		actualNames[row.Name] = true
	}
	for _, column := range expected {
		if !actualNames[string(column.Name)] {
			return string(column.Name)
		}
	}
	if len(expected) != 0 {
		return string(expected[0].Name)
	}
	return "column"
}

func columnDifferenceName(actual columnRow, expected physical.PhysicalColumn) string {
	if actual.Name != "" && actual.Name != string(expected.Name) {
		return actual.Name
	}
	return string(expected.Name)
}

func (provider *Provider) verify(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) error {
	actual, err := provider.introspect(ctx, database, expected)
	if err != nil {
		return err
	}
	if err := physical.CompareFingerprints(expected, actual); err != nil {
		return fmt.Errorf("sqlite verify: %w", err)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func columnName(table physical.PhysicalTable, id ir.FieldID) (string, bool) {
	for _, column := range table.Columns {
		if column.ID == id {
			return string(column.Name), true
		}
	}
	return "", false
}
