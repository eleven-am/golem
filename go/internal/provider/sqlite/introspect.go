package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
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
	return provider.introspectNormalizedCatalog(ctx, database, normalized)
}

func (provider *Provider) introspectReviewedCatalog(ctx context.Context, database catalogQuerier, reviewed reviewedSnapshot) (physical.PhysicalSchema, error) {
	normalized, err := physical.NormalizeHistorical(reviewed.schema)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectNormalizedCatalog(ctx, database, normalized)
}

func (provider *Provider) introspectNormalizedCatalog(ctx context.Context, database catalogQuerier, normalized physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	var rows []schemaRow
	if err := database.SelectContext(ctx, &rows, "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND type IN ('table','index','view','trigger') ORDER BY type,name"); err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect schema: %w", err)
	}
	actual := make(map[string]schemaRow, len(rows))
	for _, row := range rows {
		actual[row.Type+"\x00"+row.Name] = row
	}
	expectedObjects := make(map[string]string)
	for _, object := range normalized.System.Objects {
		statement, renderErr := renderSystemObject(object)
		if renderErr != nil {
			return physical.PhysicalSchema{}, renderErr
		}
		expectedObjects["table\x00"+string(object.Name)] = statement
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
		for _, index := range table.Indexes {
			statement, renderErr := renderIndex(table, index)
			if renderErr != nil {
				return physical.PhysicalSchema{}, renderErr
			}
			expectedObjects["index\x00"+string(index.Name)] = statement
		}
	}
	if normalized.Version != 1 || normalized.CanonicalVersion != 1 {
		for _, extension := range normalized.Extensions {
			statements, renderErr := renderSemanticExtension(extension, false)
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
			for index, name := range indexNames {
				expectedObjects["index\x00"+string(name)] = statements[index+1]
			}
			vectorKey := "table\x00" + vectorName
			vector, exists := actual[vectorKey]
			if !exists || strings.TrimSpace(vector.SQL) != statements[len(statements)-1] {
				return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect drift: semantic vector table %s", vectorName)
			}
			delete(actual, vectorKey)
			for _, suffix := range []string{"_chunks", "_info", "_rowids", "_vector_chunks00"} {
				key := "table\x00" + vectorName + suffix
				if _, exists := actual[key]; !exists {
					return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect drift: semantic vector shadow table %s%s", vectorName, suffix)
				}
				delete(actual, key)
			}
		}
	}
	for _, unmanaged := range normalized.Unmanaged {
		delete(actual, unmanaged.Kind+"\x00"+string(unmanaged.Name))
	}
	if len(actual) != len(expectedObjects) {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect drift: object count got=%d want=%d", len(actual), len(expectedObjects))
	}
	for key, expectedSQL := range expectedObjects {
		row, exists := actual[key]
		if !exists {
			return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect drift: missing %s", key)
		}
		expectedAST, parseErr := parseDDL(expectedSQL)
		if parseErr != nil {
			return physical.PhysicalSchema{}, parseErr
		}
		actualAST, parseErr := parseDDL(row.SQL)
		if parseErr != nil {
			return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect parse %s: %w", key, parseErr)
		}
		if !reflect.DeepEqual(expectedAST, actualAST) {
			return physical.PhysicalSchema{}, fmt.Errorf("sqlite introspect drift: semantic definition changed for %s", key)
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

func inspectColumns(ctx context.Context, database catalogQuerier, table physical.PhysicalTable) error {
	var rows []columnRow
	if err := database.SelectContext(ctx, &rows, "PRAGMA table_xinfo("+quote(table.Name)+")"); err != nil {
		return fmt.Errorf("sqlite introspect columns %s: %w", table.Name, err)
	}
	if len(rows) != len(table.Columns) {
		return fmt.Errorf("sqlite introspect drift table=%s column count", table.ID)
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
			return fmt.Errorf("sqlite introspect drift table=%s column=%s catalog fact", table.ID, column.ID)
		}
		generated := column.Generated != nil
		if generated != (row.Hidden == 2 || row.Hidden == 3) {
			return fmt.Errorf("sqlite introspect drift table=%s column=%s generated flag", table.ID, column.ID)
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
			return fmt.Errorf("sqlite introspect drift table=%s column=%s default", table.ID, column.ID)
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
		return fmt.Errorf("sqlite introspect drift table=%s foreign-key arity", table.ID)
	}
	for _, row := range rows {
		matched := false
		for _, fk := range table.ForeignKeys {
			target := tables[fk.ReferencedTable]
			if row.Table != string(target.Name) || row.Seq >= len(fk.Columns) {
				continue
			}
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
			return fmt.Errorf("sqlite introspect drift table=%s foreign key", table.ID)
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
			return fmt.Errorf("sqlite introspect drift table=%s index=%s", table.ID, row.Name)
		}
		delete(explicit, row.Name)
	}
	if len(explicit) != 0 {
		return fmt.Errorf("sqlite introspect drift table=%s missing index", table.ID)
	}
	return nil
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
